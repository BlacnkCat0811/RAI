//go:build integration && modelperf

package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"rai/server/internal/backfill"
	"rai/server/internal/httpapi"
	"rai/server/internal/modelmanifest"
	"rai/server/internal/syncing"
)

func TestModelBackfill180DayPerformance(t *testing.T) {
	f := setup(t)
	api, worker := b4Roles(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(loc)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	workoutStream(t, f)
	workouts := make([]syncing.Event, 180)
	for i := range workouts {
		workouts[i] = workoutAt(today.AddDate(0, 0, i-len(workouts)).Add(8*time.Hour), 1800)
	}
	f.commit(t, workouts, 1, nil)
	if _, err := f.s.Pool.Exec(ctx, `UPDATE datasets SET synthetic_test=true WHERE id=$1`, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.PrepareModelStack(ctx, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}
	manifest, err := modelmanifest.CurrentHash()
	if err != nil {
		t.Fatal(err)
	}
	req := backfill.Request{DatasetID: f.p.DatasetID, ManifestSHA: manifest, StackID: modelmanifest.StackID(),
		From: today.AddDate(0, 0, -180).Format(time.DateOnly), Through: today.AddDate(0, 0, -1).Format(time.DateOnly),
		AsOf: today.Format(time.DateOnly), View: backfill.ViewRevised, Operation: backfill.OperationBackfill, Actor: "synthetic-modelperf", BlockDays: 31}
	plan, err := backfill.Plan(req, backfill.Facts{Today: req.AsOf})
	if err != nil {
		t.Fatal(err)
	}
	op, err := f.s.CreateBackfillOperation(ctx, f.p.DatasetID, req.Actor, req, plan)
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.New(api, analysisLog)
	readLegacy := func() {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/v2/workouts?limit=1", nil)
		req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("legacy reader status=%d", res.Code)
		}
	}
	started := time.Now()
	if _, err := f.s.ResumeBackfillOperation(ctx, f.p.DatasetID, op.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= len(workouts); i++ { // current day plus the requested historical range
		l, err := worker.Claim(ctx, []string{modelmanifest.StackJobKind})
		if err != nil {
			t.Fatal("claim", i, err)
		}
		if status, err := worker.ProcessModelStack(ctx, l, now); err != nil || status != "SUCCEEDED" {
			t.Fatal("execute", i, status, err)
		}
		if i == 31 {
			checkpoint, err := f.s.ReadBackfillOperation(ctx, f.p.DatasetID, op.ID)
			if err != nil || checkpoint.CompletedBlocks != 1 {
				t.Fatal("first checkpoint", checkpoint.CompletedBlocks, err)
			}
			if remaining, err := f.s.ResumeBackfillOperation(ctx, f.p.DatasetID, op.ID); err != nil || len(remaining) != 5 {
				t.Fatal("resume", len(remaining), err)
			}
		}
		if i%15 == 0 {
			readLegacy()
		}
	}
	done, err := f.s.ReadBackfillOperation(ctx, f.p.DatasetID, op.ID)
	if err != nil || done.Status != "candidate_ready" || done.CompletedBlocks != 6 {
		t.Fatal("completion", done.Status, done.CompletedBlocks, err)
	}
	var n int
	if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE dataset_id=$1 AND payload->>'backfillID'=$2`, f.p.DatasetID, op.ID).Scan(&n); err != nil || n != len(workouts) {
		t.Fatal("resume repeated completed day jobs", n, err)
	}
	t.Logf("MC_F07 days=%d workouts=%d jobs=%d blocks=%d elapsed=%s", len(workouts), len(workouts), n, done.CompletedBlocks, time.Since(started))
}
