//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"rai/server/internal/admin"
	"rai/server/internal/httpapi"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
)

func TestP7RealLoginBoundariesAndRevocation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	suffix := strings.ReplaceAll(syncing.NewID(), "-", "")
	apiGroup, workerGroup, login := "rai_api_"+suffix, "rai_worker_"+suffix, "rai_login_"+suffix
	b, e := os.ReadFile("../../../deploy/postgres/roles.sql")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Pool.Exec(ctx, strings.ReplaceAll(strings.ReplaceAll(string(b), "rai_api", apiGroup), "rai_worker", workerGroup)); e != nil {
		t.Fatal(e)
	}
	password := syncing.NewID() + syncing.NewID()
	if _, e = f.s.Pool.Exec(ctx, "CREATE ROLE "+pgx.Identifier{login}.Sanitize()+" LOGIN PASSWORD '"+password+"' IN ROLE "+pgx.Identifier{apiGroup}.Sanitize()); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_, e := f.s.Pool.Exec(context.Background(), "DROP OWNED BY "+pgx.Identifier{login}.Sanitize()+","+pgx.Identifier{apiGroup}.Sanitize()+","+pgx.Identifier{workerGroup}.Sanitize()+"; DROP ROLE "+pgx.Identifier{login}.Sanitize()+","+pgx.Identifier{apiGroup}.Sanitize()+","+pgx.Identifier{workerGroup}.Sanitize())
		if e != nil {
			t.Error(e)
		}
	})
	u, _ := url.Parse(f.s.Pool.Config().ConnString())
	u.User = url.UserPassword(login, password)
	api, e := postgres.Open(ctx, u.String(), 2)
	if e != nil {
		t.Fatal(e)
	}
	defer api.Pool.Close()
	if e = api.RuntimeRoleSafe(ctx); e != nil {
		t.Fatal("actual login unsafe", e)
	}
	for _, sql := range []string{"CREATE TABLE public.p7_forbidden(x int)", "CREATE TEMP TABLE p7_shadow(x int)", "UPDATE route_points SET latitude=0", "DELETE FROM route_chunks", "UPDATE workout_routes SET state='sealed',receipt='x'", "ALTER TABLE jobs ADD COLUMN p7_forbidden int", "SET ROLE " + pgx.Identifier{workerGroup}.Sanitize()} {
		_, e = api.Pool.Exec(ctx, sql)
		var p *pgconn.PgError
		if e == nil || !errors.As(e, &p) || p.Code != "42501" {
			t.Fatalf("forbidden runtime operation was not permission denied: %s %v", sql, e)
		}
	}
	if e = admin.Migrate(ctx, u.String()); e == nil {
		t.Fatal("runtime can migrate")
	}
	f.commit(t, []syncing.Event{sample(syncing.NewID(), 100)}, 1, nil)
	// A second synthetic principal in the same database cannot read first data.
	other, device, token := syncing.NewID(), syncing.NewID(), strings.Repeat("z", 43)
	h := sha256.Sum256([]byte(token))
	if _, e = f.s.Pool.Exec(ctx, `INSERT INTO datasets(id,epoch,service_id) VALUES($1,$2,$3);`, other, syncing.NewID(), syncing.NewID()); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Pool.Exec(ctx, `INSERT INTO devices(dataset_id,id,token_sha256,can_collect) VALUES($1,$2,$3,false)`, other, device, h[:]); e != nil {
		t.Fatal(e)
	}
	var batch string
	if e = f.s.Pool.QueryRow(ctx, `SELECT batch_id::text FROM sync_batches LIMIT 1`).Scan(&batch); e != nil {
		t.Fatal(e)
	}
	handler := httpapi.New(api, analysisLog)
	request := httptest.NewRequest("GET", "/v2/sync/batches/"+batch, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, request)
	if w.Code != 404 {
		t.Fatalf("dataset leaked: %d", w.Code)
	}
	if _, e = f.s.Pool.Exec(ctx, `UPDATE devices SET revoked_at=now() WHERE dataset_id=$1 AND id=$2`, other, device); e != nil {
		t.Fatal(e)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, request)
	if w.Code != 401 {
		t.Fatal("revoked device authorized")
	}
}

func TestP7OperationsInvariantInventoryAndSanitization(t *testing.T) {
	f, p := trainingFixture(t)
	_ = p
	f.commit(t, []syncing.Event{sample(syncing.NewID(), 123)}, 1, nil)
	for _, mode := range []string{"snapshot", "verify", "retention-dry-run"} {
		var out bytes.Buffer
		if e := admin.Operations(context.Background(), f.s.Pool.Config().ConnString(), []string{mode}, &out); e != nil {
			t.Fatal(mode, e)
		}
		if !json.Valid(out.Bytes()) || strings.Contains(out.String(), f.p.DatasetID) || strings.Contains(out.String(), f.enrollment.Token) {
			t.Fatal("unsafe operational output")
		}
		if mode == "verify" {
			var value admin.Inventory
			if e := json.Unmarshal(out.Bytes(), &value); e != nil || value.SchemaVersion != 13 || len(value.Tables) != 45 || value.InvariantViolations != 0 {
				t.Fatal("incomplete verification", e, len(value.Tables))
			}
		}
	}
}

func TestP7FailedJobAlertResolvesOnlyAfterNewGenerationIsClean(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	envelope, _, _ := f.commit(t, []syncing.Event{sample(syncing.NewID(), 100)}, 1, nil)
	lease, err := f.s.Claim(ctx, []string{"analysis.sample"})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.Fail(ctx, lease, false); err != nil {
		t.Fatal(err)
	}
	read := func() map[string]any {
		t.Helper()
		var out bytes.Buffer
		if err := admin.Operations(ctx, f.s.Pool.Config().ConnString(), []string{"snapshot"}, &out); err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if err := json.Unmarshal(out.Bytes(), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	if read()["failed_jobs_needing_action"] != float64(1) {
		t.Fatal("current failure did not alert")
	}
	f.commit(t, []syncing.Event{sample(syncing.NewID(), 100)}, 2, &envelope.BatchID)
	if read()["failed_jobs_needing_action"] != float64(1) {
		t.Fatal("pending generation hid unresolved failure")
	}
	runOne(t, f, "analysis.sample")
	snapshot := read()
	if snapshot["failed_jobs_needing_action"] != float64(0) || snapshot["historical_failed_jobs"] != float64(1) {
		t.Fatal("new publication did not resolve historical alert")
	}
	var state string
	if err = f.s.Pool.QueryRow(ctx, `SELECT state FROM jobs WHERE id=$1`, lease.ID).Scan(&state); err != nil || state != "failed" {
		t.Fatal("failed audit identity was rewritten", err)
	}
}
