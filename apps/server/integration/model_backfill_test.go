//go:build integration

package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	am "rai/server/internal/athletemodel"
	"rai/server/internal/backfill"
	"rai/server/internal/modelmanifest"
	"rai/server/internal/storage/postgres"
)

func TestBackfillOperationLifecycleAndStagedCandidate(t *testing.T) {
	f := setup(t)
	api, worker := b4Roles(t, f)
	ctx := context.Background()
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
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(loc)
	today := now.Format(time.DateOnly)
	req := backfill.Request{DatasetID: f.p.DatasetID, ManifestSHA: manifest, StackID: modelmanifest.StackID(),
		From: now.AddDate(0, 0, -3).Format(time.DateOnly), Through: now.AddDate(0, 0, -1).Format(time.DateOnly),
		AsOf: today, View: backfill.ViewRevised, Operation: backfill.OperationBackfill, Actor: "synthetic-admin", BlockDays: 2}
	plan, err := backfill.Plan(req, backfill.Facts{Today: today})
	if err != nil {
		t.Fatal(err)
	}
	op, err := f.s.CreateBackfillOperation(ctx, f.p.DatasetID, req.Actor, req, plan)
	if err != nil || op.Status != "planned" || op.TotalBlocks != 2 {
		t.Fatal("create", op.Status, err)
	}
	replay, err := f.s.CreateBackfillOperation(ctx, f.p.DatasetID, req.Actor, req, plan)
	if err != nil || replay.ID != op.ID {
		t.Fatal("execute idempotency", err)
	}
	remaining, err := f.s.ResumeBackfillOperation(ctx, f.p.DatasetID, op.ID)
	if err != nil || len(remaining) != 2 {
		t.Fatal("resume", len(remaining), err)
	}
	for i := 0; i < 4; i++ { // one initial current day plus three backfill days
		l, err := worker.Claim(ctx, []string{modelmanifest.StackJobKind})
		if err != nil {
			t.Fatal("claim", err)
		}
		if status, err := worker.ProcessModelStack(ctx, l, now); err != nil || status != "SUCCEEDED" {
			t.Fatal("execute", status, err)
		}
	}
	done, err := f.s.ReadBackfillOperation(ctx, f.p.DatasetID, op.ID)
	if err != nil || done.Status != "candidate_ready" || done.CompletedBlocks != 2 {
		t.Fatal("completion", done.Status, done.CompletedBlocks, err)
	}
	var visible int
	if err := api.Pool.QueryRow(ctx, `SELECT count(*) FROM analysis_published_artifacts WHERE dataset_id=$1`, f.p.DatasetID).Scan(&visible); err != nil || visible != 0 {
		t.Fatal("candidate visible", visible, err)
	}
	req.View = backfill.ViewAsKnown
	if _, err := f.s.CreateBackfillOperation(ctx, f.p.DatasetID, req.Actor, req, plan); !errors.Is(err, am.ErrHistoricalInputUnavailable) {
		t.Fatal("unsupported history did not fail closed", err)
	}
	req.View, req.BlockDays = backfill.ViewRevised, 1
	plan, err = backfill.Plan(req, backfill.Facts{Today: today})
	if err != nil {
		t.Fatal(err)
	}
	op2, err := f.s.CreateBackfillOperation(ctx, f.p.DatasetID, req.Actor, req, plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.ResumeBackfillOperation(ctx, f.p.DatasetID, op2.ID); err != nil {
		t.Fatal(err)
	}
	l, err := worker.Claim(ctx, []string{modelmanifest.StackJobKind})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := f.s.CancelBackfillOperation(ctx, f.p.DatasetID, op2.ID)
	if err != nil || cancelled.Status != "cancelled" {
		t.Fatal("cancel", cancelled.Status, err)
	}
	if status, err := worker.ProcessModelStack(ctx, l, now); err != nil || status != "SUPERSEDED" {
		t.Fatal("cancelled work published", status, err)
	}
	if _, err = f.s.ResumeBackfillOperation(ctx, f.p.DatasetID, op2.ID); !errors.Is(err, postgres.ErrBackfillConflict) {
		t.Fatal("resume cancelled must conflict", err)
	}
}
