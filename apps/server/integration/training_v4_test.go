//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"io"
	"log/slog"
	"testing"
	"time"

	"rai/server/internal/analysis"
	"rai/server/internal/jobs"
	"rai/server/internal/modelmanifest"
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
		if errors.Is(err, pgx.ErrNoRows) {
			return n
		}
		if err != nil {
			t.Fatal(err)
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
	if err = f.s.Pool.QueryRow(ctx, `SELECT generator,parameter_version FROM training_generation_runs WHERE dataset_id=$1 AND plan_id=$2 AND status='SUCCEEDED' ORDER BY started_at DESC LIMIT 1`, f.p.DatasetID, planID).Scan(&generator, &params); err != nil || generator != training.GeneratorV4 || params != training.ParameterVersionV3 {
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
	if err != nil || in.Context.Coach == nil || *in.Context.Coach != training.V3Parameters() || len(in.Context.PreviousWeeks) == 0 {
		t.Fatalf("v4 input %v", err)
	}
	// A version switch through update_config is refused.
	v1 := training.Config{StartDate: cfg.StartDate, Weeks: 8, AvailableDays: cfg.AvailableDays, LongRunDay: 7, Goal: race.Goal}
	_, err = f.s.ChangeTrainingPlan(ctx, f.p, planID, training.ChangeRequest{CommandID: syncing.NewID(), Operation: "update_config", ExpectedConfigRevision: 2, Config: &v1})
	requireSyncCode(t, err, "invalid_training_config")
}

func datasetNow(t *testing.T, f *fixture) time.Time {
	t.Helper()
	var zone string
	if err := f.s.Pool.QueryRow(context.Background(), `SELECT timezone FROM datasets WHERE id=$1`, f.p.DatasetID).Scan(&zone); err != nil {
		t.Fatal(err)
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		t.Fatal(err)
	}
	return time.Now().In(loc)
}

func trainingRuns(t *testing.T, f *fixture, planID string) int {
	t.Helper()
	var n int
	if err := f.s.Pool.QueryRow(context.Background(), `SELECT count(*) FROM training_generation_runs WHERE dataset_id=$1 AND plan_id=$2`, f.p.DatasetID, planID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func queuedTrainingJobs(t *testing.T, f *fixture) int {
	t.Helper()
	var n int
	if err := f.s.Pool.QueryRow(context.Background(), `SELECT count(*) FROM jobs WHERE dataset_id=$1 AND kind='training.plan' AND state='queued'`, f.p.DatasetID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func sessionsJSON(t *testing.T, s []training.Session) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// An existing v1-config plan keeps the training_plan/v3 lineage. Creating a
// v4 plan archives it without regenerating or rewriting its published
// sessions, and nothing can queue the archived plan again.
func TestTrainingPlanV3AndV4CoexistOnPostgres(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	now := datasetNow(t, f)
	monday := mondayOf(now)
	v3ID := syncing.NewID()
	if _, err := f.s.CreateTrainingPlan(ctx, f.p, training.CreateRequest{CommandID: syncing.NewID(), PlanID: v3ID, Config: training.Config{
		StartDate: monday.Format(time.DateOnly), Weeks: 4, AvailableDays: []int{2, 4, 7}, LongRunDay: 7, BaselineWeeklyM: training.Ptr(20000),
		Goal: training.Goal{Type: "running", Date: monday.AddDate(0, 0, 26).Format(time.DateOnly), DistanceM: 5000}}}); err != nil {
		t.Fatal(err)
	}
	if processTrainingJobs(t, f, now) == 0 {
		t.Fatal("no v3 training job")
	}
	v3, err := f.s.TrainingPlan(ctx, f.p, v3ID)
	if err != nil || v3.Effective == nil || len(v3.Effective.Sessions) == 0 || !v3.Active || v3.Config.V2() {
		t.Fatalf("v3 not published: %+v %v", v3, err)
	}
	var generator, params string
	if err = f.s.Pool.QueryRow(ctx, `SELECT generator,parameter_version FROM training_generation_runs WHERE dataset_id=$1 AND plan_id=$2 AND status='SUCCEEDED' ORDER BY started_at DESC LIMIT 1`, f.p.DatasetID, v3ID).Scan(&generator, &params); err != nil || generator != training.Generator || params != training.ParameterVersion {
		t.Fatalf("v3 lineage %s %s %v", generator, params, err)
	}
	in, err := f.s.LoadTraining(ctx, analysis.Lease{DatasetID: f.p.DatasetID, ScopeKey: "plan:" + v3ID}, now)
	if err != nil || in.Context.Coach != nil || in.Context.Parameters != training.V1Parameters() {
		t.Fatalf("v3 input must stay on coach_parameters/v1: %v", err)
	}
	// The v3 path regenerates deterministically through the store.
	before := sessionsJSON(t, v3.Effective.Sessions)
	if _, err = f.s.ChangeTrainingPlan(ctx, f.p, v3ID, training.ChangeRequest{CommandID: syncing.NewID(), Operation: "recalculate", ExpectedConfigRevision: 1}); err != nil {
		t.Fatal(err)
	}
	processTrainingJobs(t, f, now)
	v3, err = f.s.TrainingPlan(ctx, f.p, v3ID)
	if err != nil || sessionsJSON(t, v3.Effective.Sessions) != before {
		t.Fatalf("v3 regeneration changed the plan: %v", err)
	}
	v3Effective, v3Runs := v3.Effective.ID, trainingRuns(t, f, v3ID)

	// "改用自适应计划" creates a v4 plan and archives the v3 plan.
	v4ID := syncing.NewID()
	if _, err = f.s.CreateTrainingPlan(ctx, f.p, training.CreateRequest{CommandID: syncing.NewID(), PlanID: v4ID, Config: training.Config{
		Version: training.ConfigVersionV2, StartDate: monday.Format(time.DateOnly), AvailableDays: []int{2, 4, 6, 7}, LongRunDay: 7}}); err != nil {
		t.Fatal(err)
	}
	if processTrainingJobs(t, f, now) == 0 {
		t.Fatal("no v4 training job")
	}
	current, err := f.s.TrainingPlan(ctx, f.p, "")
	if err != nil || current.ID != v4ID || !current.Active || !current.Config.V2() || current.Effective == nil {
		t.Fatalf("current plan is not the v4 plan: %+v %v", current, err)
	}
	if err = f.s.Pool.QueryRow(ctx, `SELECT generator FROM training_generation_runs WHERE dataset_id=$1 AND plan_id=$2 AND status='SUCCEEDED' ORDER BY started_at DESC LIMIT 1`, f.p.DatasetID, v4ID).Scan(&generator); err != nil || generator != training.GeneratorV4 {
		t.Fatalf("v4 lineage %s %v", generator, err)
	}
	old, err := f.s.TrainingPlan(ctx, f.p, v3ID)
	if err != nil || old.Active || old.Effective == nil || old.Effective.ID != v3Effective || sessionsJSON(t, old.Effective.Sessions) != before {
		t.Fatalf("archived v3 plan was rewritten: %+v %v", old, err)
	}
	if n := trainingRuns(t, f, v3ID); n != v3Runs {
		t.Fatalf("archived v3 plan regenerated: %d runs, want %d", n, v3Runs)
	}
	// An archived plan refuses changes and cannot be queued again.
	_, err = f.s.ChangeTrainingPlan(ctx, f.p, v3ID, training.ChangeRequest{CommandID: syncing.NewID(), Operation: "recalculate", ExpectedConfigRevision: 1})
	requireSyncCode(t, err, "training_plan_archived")
	if _, err = f.s.Pool.Exec(ctx, `SELECT rai_training_dirty($1,$2)`, f.p.DatasetID, v3ID); err != nil {
		t.Fatal(err)
	}
	if n := queuedTrainingJobs(t, f); n != 0 {
		t.Fatalf("archived plan queued %d jobs", n)
	}
}

// Today's release of the active model stack marks the active plan dirty in
// the same transaction, and the worker regenerates it with the past frozen.
// A candidate-only release must not touch the plan.
func TestTrainingPlanV4FollowsActiveStackDayRelease(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	today, _ := runStackDay(t, f, nil, stackDayOptions{})
	now := datasetNow(t, f)
	if now.Format(time.DateOnly) != today.Format(time.DateOnly) {
		t.Skip("dataset day rolled over during setup")
	}
	planID := syncing.NewID()
	if _, err := f.s.CreateTrainingPlan(ctx, f.p, training.CreateRequest{CommandID: syncing.NewID(), PlanID: planID, Config: training.Config{
		Version: training.ConfigVersionV2, StartDate: mondayOf(now).AddDate(0, 0, -21).Format(time.DateOnly), AvailableDays: []int{2, 4, 6, 7}, LongRunDay: 7, BaselineWeeklyS: training.Ptr(9000)}}); err != nil {
		t.Fatal(err)
	}
	if processTrainingJobs(t, f, now) == 0 {
		t.Fatal("no training job")
	}
	first, err := f.s.TrainingPlan(ctx, f.p, planID)
	if err != nil || first.Effective == nil {
		t.Fatalf("not published: %v", err)
	}
	runs := trainingRuns(t, f, planID)
	stack := modelmanifest.StackID()
	release := func() {
		t.Helper()
		if _, err := f.s.Pool.Exec(ctx, `SELECT rai_schedule_model_day($1,$2,$3::date,now())`, f.p.DatasetID, stack, today.Format(time.DateOnly)); err != nil {
			t.Fatal(err)
		}
		if processStackJobs(t, f) == 0 {
			t.Fatal("day release did not succeed")
		}
	}
	// The stack is only the candidate: its release leaves the plan alone.
	release()
	if n := queuedTrainingJobs(t, f); n != 0 {
		t.Fatalf("candidate release queued %d training jobs", n)
	}
	hash, err := modelmanifest.CurrentHash()
	if err != nil {
		t.Fatal(err)
	}
	setModelChannel(t, f, "active", stack, hash)
	release()
	if n := queuedTrainingJobs(t, f); n != 1 {
		t.Fatalf("active release queued %d training jobs, want 1", n)
	}
	var dirty, clean int64
	if err = f.s.Pool.QueryRow(ctx, `SELECT generation,clean_generation FROM dirty_scopes WHERE dataset_id=$1 AND scope_key=$2`, f.p.DatasetID, "plan:"+planID).Scan(&dirty, &clean); err != nil || dirty <= clean {
		t.Fatalf("plan scope not dirty: generation %d clean %d %v", dirty, clean, err)
	}
	// The worker reads the clock per job, after the release committed; a
	// clock older than the readiness asOf is rejected by FreshenV4.
	if processTrainingJobs(t, f, time.Now()) == 0 {
		t.Fatal("dirty plan was not regenerated")
	}
	next, err := f.s.TrainingPlan(ctx, f.p, planID)
	if err != nil || next.Effective == nil || next.Effective.ID == first.Effective.ID || next.GenerationState == "dirty" {
		t.Fatalf("regenerated plan not published: %+v %v", next, err)
	}
	if n := trainingRuns(t, f, planID); n != runs+1 {
		t.Fatalf("runs %d, want %d", n, runs+1)
	}
	var generator, status string
	if err = f.s.Pool.QueryRow(ctx, `SELECT generator,status FROM training_generation_runs WHERE dataset_id=$1 AND plan_id=$2 ORDER BY started_at DESC LIMIT 1`, f.p.DatasetID, planID).Scan(&generator, &status); err != nil || generator != training.GeneratorV4 || status != "SUCCEEDED" {
		t.Fatalf("latest run %s %s %v", generator, status, err)
	}
	// Sessions before today are frozen from the previous base.
	past := func(s []training.Session) []training.Session {
		var out []training.Session
		for _, x := range s {
			if x.Date < today.Format(time.DateOnly) {
				out = append(out, x)
			}
		}
		return out
	}
	if p := past(first.Effective.Sessions); len(p) == 0 || sessionsJSON(t, p) != sessionsJSON(t, past(next.Effective.Sessions)) {
		t.Fatal("past sessions changed after the day release")
	}
	if err = f.s.Pool.QueryRow(ctx, `SELECT generation,clean_generation FROM dirty_scopes WHERE dataset_id=$1 AND scope_key=$2`, f.p.DatasetID, "plan:"+planID).Scan(&dirty, &clean); err != nil || dirty != clean {
		t.Fatalf("plan scope still dirty: generation %d clean %d %v", dirty, clean, err)
	}
}

// A failed training input read names its step (never its values), so a
// production failure log says which read failed.
func TestTrainingInputFailureNamesStep(t *testing.T) {
	f := setup(t)
	_, err := f.s.LoadTraining(context.Background(), analysis.Lease{DatasetID: f.p.DatasetID, ScopeKey: "plan:" + syncing.NewID()}, time.Now())
	var se *training.StepError
	if !errors.As(err, &se) || se.Step != "plan" {
		t.Fatalf("missing step: %v", err)
	}
}
