//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"rai/server/internal/httpapi"
	"rai/server/internal/syncing"
)

func TestChangeNotificationsPagingAndIsolation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ids := []string{syncing.NewID(), syncing.NewID(), syncing.NewID()}
	e, _, _ := f.commit(t, []syncing.Event{sample(ids[0], 120), sample(ids[1], 121), sample(ids[2], 122)}, 1, nil)
	f.commit(t, []syncing.Event{deleted(ids[0])}, 2, &e.BatchID)
	page, err := f.s.Changes(ctx, f.p, "0", 2)
	if err != nil || len(page.Changes) != 2 || !page.HasMore || page.Changes[0].Revision != 1 || page.Changes[1].Ordinal != 1 {
		t.Fatalf("first page: %+v %v", page, err)
	}
	page2, err := f.s.Changes(ctx, f.p, page.NextCursor, 2)
	if err != nil || len(page2.Changes) != 2 || page2.HasMore || page2.Changes[0].Ordinal != 2 || page2.Changes[1].Revision != 2 || page2.Changes[1].Operation != "delete" {
		t.Fatalf("second page: %+v %v", page2, err)
	}
	empty, err := f.s.Changes(ctx, f.p, page2.NextCursor, 2)
	if err != nil || len(empty.Changes) != 0 || empty.HasMore || empty.NextCursor != page2.NextCursor {
		t.Fatalf("empty page: %+v %v", empty, err)
	}
	for _, bad := range []string{"garbage", f.p.DatasetID + ":" + f.enrollment.DatasetEpoch + ":1:99", f.p.DatasetID + ":" + f.enrollment.DatasetEpoch + ":01:0", f.p.DatasetID + ":" + syncing.NewID() + ":1:0"} {
		_, err = f.s.Changes(ctx, f.p, bad, 10)
		requireCode(t, err, "invalid_change_cursor")
	}
	other := f.p
	other.DatasetID = syncing.NewID()
	if _, err = f.s.Pool.Exec(ctx, `INSERT INTO datasets(id,epoch,service_id) VALUES($1,$2,$3)`, other.DatasetID, syncing.NewID(), syncing.NewID()); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Changes(ctx, other, page.NextCursor, 2)
	requireCode(t, err, "invalid_change_cursor")
	empty, err = f.s.Changes(ctx, other, "0", 2)
	if err != nil || len(empty.Changes) != 0 {
		t.Fatal("foreign dataset leaked changes", err)
	}

	handler := httpapi.New(f.s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, test := range []struct {
		query  string
		status int
	}{
		{"?after=0&limit=2", 200}, {"?after=invalid", 422}, {"?after=", 422}, {"?limit=101", 422}, {"?limit=2&limit=3", 422}, {"?unknown=x", 422},
	} {
		req := httptest.NewRequest("GET", "/v2/sync/changes"+test.query, nil)
		req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != test.status {
			t.Fatalf("%s: status %d", test.query, w.Code)
		}
		if test.status == 200 {
			var got syncing.ChangePage
			if err = json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got.Changes) != 2 {
				t.Fatal("invalid HTTP response", err)
			}
			if strings.Contains(w.Body.String(), `"payload"`) {
				t.Fatal("notification unexpectedly contains fact payload")
			}
		}
	}
	// Force the planner to consider the available ordered index; the normal
	// cost choice on this tiny synthetic dataset can legitimately be a seq scan.
	conn, err := f.s.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, `SET enable_seqscan=off`); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(ctx, `RESET enable_seqscan`)
	rows, err := conn.Query(ctx, `EXPLAIN SELECT commit_rev,ordinal FROM change_events WHERE dataset_id=$1 AND (commit_rev,ordinal)>($2,$3) ORDER BY commit_rev,ordinal LIMIT 101`, f.p.DatasetID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan string
	for rows.Next() {
		var line string
		if err = rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan += line
	}
	if rows.Err() != nil || !strings.Contains(plan, "change_events_pkey") {
		t.Fatal(fmt.Sprintf("ordered index unavailable: %s", plan), rows.Err())
	}
}
