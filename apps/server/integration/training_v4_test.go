//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"rai/server/internal/analysis"
	"rai/server/internal/jobs"
	"rai/server/internal/syncing"
	"rai/server/internal/training"
)

// processTrainingJobs runs every queued training.plan job through the worker.
func processTrainingJobs(t *testing.T, f *fixture, at time.Time) int {
	t.Helper()
	ctx := context.Background()
	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	n := 0
	for {
		if _, err := f.s.Pool.Exec(ctx, `UPDATE jobs SET run_after=now() WHERE kind='training.plan' AND state='queued'`); err != nil {
			t.Fatal(err)
		}
		l, err := f.s.Claim(ctx, []string{"training.plan"})
		if err != nil {
			return n
		}
		jobs.Process(ctx, f.s, l, log, at)
		n++
		if n > 10 {
			t.Fatal("training jobs do not settle")
		}
	}
}

func mondayOf(d time.Time) time.Time {
	d = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, d.Location())
	return d.AddDate(0, 0, -((int(d.Weekday()) + 6) % 7))
}

func TestTrainingPlanV4LifecycleOnPostgres(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	var zone string
	if err := f.s.Pool.QueryRow(ctx, `SELECT timezone FROM datasets WHERE id=$1`, f.p.DatasetID).Scan(&zone); err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation(zone)
	now := time.Now().In(loc)
	// Anchored 20 weeks back: today's generated slots lie beyond the old
	// 16-week bound (slot < 112) that migration 60 lifted for v4 plans.
	anchor := mondayOf(now).AddDate(0, 0, -140)
	cfg := training.Config{Version: training.ConfigVersionV2, StartDate: anchor.Format(time.DateOnly), AvailableDays: []int{2, 4, 6, 7}, LongRunDay: 7, BaselineWeeklyS: training.Ptr(9000)}
	planID := syncing.NewID()
	plan, err := f.s.CreateTrainingPlan(ctx, f.p, training.CreateRequest{CommandID: syncing.NewID(), PlanID: planID, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if processTrainingJobs(t, f, now) == 0 {
		t.Fatal("no training job")
	}
	plan, err = f.s.TrainingPlan(ctx, f.p, planID)
	if err != nil || plan.Effective == nil {
		t.Fatalf("not published: %+v %v", plan, err)
	}
	eff := plan.Effective
	if eff.Mode != "fitness" || eff.AsOf != now.Format(time.DateOnly) || len(eff.Weeks) == 0 || len(eff.Sessions) == 0 {
		t.Fatalf("effective %+v", eff.Base.Weeks)
	}
	for _, s := range eff.Sessions {
		if s.Type != "REST" && s.Type != "RACE" && s.DurationS < 1800 {
			t.Fatalf("session below floor %+v", s)
		}
		if s.Slot < 112 && s.Date >= now.Format(time.DateOnly) {
			t.Fatalf("slot %d not beyond the old bound", s.Slot)
		}
	}
	var generator, params string
	if err = f.s.Pool.QueryRow(ctx, `SELECT generator,parameter_version FROM training_generation_runs WHERE dataset_id=$1 AND plan_id=$2 AND status='SUCCEEDED' ORDER BY started_at DESC LIMIT 1`, f.p.DatasetID, planID).Scan(&generator, &params); err != nil || generator != training.GeneratorV4 || params != training.ParameterVersionV2 {
		t.Fatalf("run lineage %s %s %v", generator, params, err)
	}
	// Regenerating without a new fact yields the same sessions.
	if _, err = f.s.ChangeTrainingPlan(ctx, f.p, planID, training.ChangeRequest{CommandID: syncing.NewID(), Operation: "recalculate", ExpectedConfigRevision: 1}); err != nil {
		t.Fatal(err)
	}
	processTrainingJobs(t, f, now)
	again, err := f.s.TrainingPlan(ctx, f.p, planID)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(eff.Sessions)
	b, _ := json.Marshal(again.Effective.Sessions)
	if string(a) != string(b) || again.Effective.ID == eff.ID {
		t.Fatal("regeneration without new facts changed the plan")
	}
	// An override on a future session is effective at once and queues a
	// regeneration that keeps it.
	var target training.Session
	for _, s := range again.Effective.Sessions {
		if s.Type == "EASY" && s.Date > now.Format(time.DateOnly) {
			target = s
			break
		}
	}
	if target.ID == "" {
		t.Fatal("no future easy session")
	}
	edited, err := f.s.PutTrainingOverride(ctx, f.p, planID, training.OverrideRequest{CommandID: syncing.NewID(), ExpectedOverrideRevision: 0, OverrideID: syncing.NewID(), SessionID: target.ID, Operation: "edit", Path: "notes", Value: json.RawMessage(`"synthetic"`)})
	if err != nil {
		t.Fatal(err)
	}
	if n := processTrainingJobs(t, f, now); n == 0 {
		t.Fatal("override did not queue a regeneration")
	}
	plan, _ = f.s.TrainingPlan(ctx, f.p, planID)
	found := false
	for _, s := range plan.Effective.Sessions {
		if s.ID == target.ID && s.Notes == "synthetic" && s.Modified {
			found = true
		}
	}
	if !found {
		t.Fatal("override lost after regeneration")
	}
	var overrides int
	if err = f.s.Pool.QueryRow(ctx, `SELECT jsonb_array_length(context->'overrides') FROM training_generation_runs WHERE dataset_id=$1 AND plan_id=$2 AND status='SUCCEEDED' ORDER BY started_at DESC LIMIT 1`, f.p.DatasetID, planID).Scan(&overrides); err != nil || overrides != 1 {
		t.Fatalf("overrides in context %d %v", overrides, err)
	}
	// Dates outside the rolling window are refused.
	_, err = f.s.PutTrainingOverride(ctx, f.p, planID, training.OverrideRequest{CommandID: syncing.NewID(), ExpectedOverrideRevision: edited.OverrideRevision, OverrideID: syncing.NewID(), SessionID: target.ID, Operation: "move", Path: "date", Value: json.RawMessage(`"` + now.AddDate(0, 0, 60).Format(time.DateOnly) + `"`)})
	requireSyncCode(t, err, "training_date_out_of_range")
	// Setting a goal switches the plan into race mode; a past goal is refused.
	past := cfg
	past.Goal = training.Goal{Type: "running", Date: now.AddDate(0, 0, -1).Format(time.DateOnly), DistanceM: 10000}
	_, err = f.s.ChangeTrainingPlan(ctx, f.p, planID, training.ChangeRequest{CommandID: syncing.NewID(), Operation: "update_config", ExpectedConfigRevision: 1, Config: &past})
	requireSyncCode(t, err, "invalid_training_config")
	race := cfg
	race.Goal = training.Goal{Type: "running", Date: mondayOf(now).AddDate(0, 0, 7*10+6).Format(time.DateOnly), DistanceM: 21097.5, TargetTimeS: training.Ptr(6600)}
	if _, err = f.s.ChangeTrainingPlan(ctx, f.p, planID, training.ChangeRequest{CommandID: syncing.NewID(), Operation: "update_config", ExpectedConfigRevision: 1, Config: &race}); err != nil {
		t.Fatal(err)
	}
	processTrainingJobs(t, f, now)
	plan, _ = f.s.TrainingPlan(ctx, f.p, planID)
	if plan.Effective.Mode != "race" || plan.Effective.GoalStatus == nil || plan.Effective.GoalStatus.Category != "half" || plan.ConfigRevision != 2 {
		t.Fatalf("race mode %s %+v", plan.Effective.Mode, plan.Effective.GoalStatus)
	}
	// The worker input carries the v4 history window and the previous base.
	in, err := f.s.LoadTraining(ctx, analysis.Lease{DatasetID: f.p.DatasetID, ScopeKey: "plan:" + planID}, now)
	if err != nil || in.Context.Coach == nil || *in.Context.Coach != training.V2Parameters() || len(in.Context.PreviousWeeks) == 0 {
		t.Fatalf("v4 input %v", err)
	}
	// A version switch through update_config is refused.
	v1 := training.Config{StartDate: cfg.StartDate, Weeks: 8, AvailableDays: cfg.AvailableDays, LongRunDay: 7, Goal: race.Goal}
	_, err = f.s.ChangeTrainingPlan(ctx, f.p, planID, training.ChangeRequest{CommandID: syncing.NewID(), Operation: "update_config", ExpectedConfigRevision: 2, Config: &v1})
	requireSyncCode(t, err, "invalid_training_config")
}
