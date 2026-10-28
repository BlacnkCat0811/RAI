//go:build integration

package integration

import (
	"context"
	"net/http/httptest"
	"rai/server/internal/httpapi"
	"rai/server/internal/syncing"
	"strings"
	"testing"
	"time"
)

func TestProductReadPublicationPaginationAndIsolation(t *testing.T) {
	f, w := analysisFixture(t)
	ctx := context.Background()
	page, e := f.s.ProductWorkouts(ctx, f.p, "", 1, "")
	if e != nil || len(page.Items) != 1 || page.Items[0].ID != w.UUID || page.Items[0].DistanceM == nil || *page.Items[0].DistanceM != 10000 || page.Items[0].AnalysisStatus != "pending" {
		t.Fatalf("raw summary %v", e)
	}
	runOne(t, f, "analysis.workout")
	page, e = f.s.ProductWorkouts(ctx, f.p, "", 1, w.UUID)
	if e != nil || page.Items[0].AnalysisStatus != "published" || page.Items[0].RouteStatus != "unavailable_no_read_api" {
		t.Fatalf("publication %v", e)
	}
	overview, e := f.s.ProductOverview(ctx, f.p)
	if e != nil || len(overview.Days) != 14 {
		t.Fatalf("overview %v", e)
	}
	total := 0.
	missing := false
	for _, d := range overview.Days {
		if d.DistanceM != nil {
			total += *d.DistanceM
		}
		if d.Count > 0 && d.Load == nil {
			missing = true
		}
	}
	if total != 10000 || !missing {
		t.Fatal("missing load became zero")
	}
	var previous string
	if e = f.s.Pool.QueryRow(ctx, `SELECT last_batch_id::text FROM sync_streams WHERE id=$1`, f.stream.StreamID).Scan(&previous); e != nil {
		t.Fatal(e)
	}
	w2 := workoutAt(time.Now().UTC().Add(-time.Hour).Truncate(time.Second), 1800)
	secondBatch, _, _ := f.commit(t, []syncing.Event{w2}, 2, &previous)
	first, e := f.s.ProductWorkouts(ctx, f.p, "", 1, "")
	if e != nil || first.Next == "" {
		t.Fatalf("cursor %v", e)
	}
	second, e := f.s.ProductWorkouts(ctx, f.p, first.Next, 1, "")
	if e != nil || len(second.Items) != 1 || second.Items[0].ID == first.Items[0].ID {
		t.Fatalf("pagination %v", e)
	}
	other := f.p
	other.DatasetID = syncing.NewID()
	empty, e := f.s.ProductWorkouts(ctx, other, "", 20, "")
	if e != nil || len(empty.Items) != 0 {
		t.Fatal("dataset leak", e)
	}
	if _, e = f.s.ProductWorkouts(ctx, f.p, "malformed", 20, ""); e == nil {
		t.Fatal("invalid cursor accepted")
	}
	if _, e = f.s.ProductHistory(ctx, f.p, 91); e == nil {
		t.Fatal("unbounded history")
	}
	handler := httpapi.New(f.s, analysisLog)
	for _, path := range []string{"/v2/workouts", "/v2/workouts/" + w.UUID, "/v2/product/overview", "/v2/analysis/history"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
		v := httptest.NewRecorder()
		handler.ServeHTTP(v, r)
		if v.Code != 200 {
			t.Fatalf("%s %d %s", path, v.Code, v.Body.String())
		}
		r = httptest.NewRequest("GET", path+"?unexpected=1", nil)
		r.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
		v = httptest.NewRecorder()
		handler.ServeHTTP(v, r)
		if v.Code != 422 {
			t.Fatal("unknown query accepted")
		}
	}
	del := syncing.Event{Op: "delete", Kind: "workout", Type: syncing.WorkoutType, UUID: w.UUID}
	f.commit(t, []syncing.Event{del}, 3, &secondBatch.BatchID)
	if _, e = f.s.ProductWorkouts(ctx, f.p, "", 20, w.UUID); e == nil {
		t.Fatal("deleted workout visible")
	}
	history, e := f.s.ProductHistory(ctx, f.p, 14)
	if e != nil {
		t.Fatal(e)
	}
	for _, m := range history.Metrics {
		if strings.HasPrefix(m.Name, "route") {
			t.Fatal("unexpected raw projection")
		}
	}
}
