//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/analysis"
	"rai/server/internal/modelmanifest"
	"rai/server/internal/syncing"
	"rai/server/internal/training"
)

func createPublishedV4(t *testing.T, f *fixture) training.Plan {
	t.Helper()
	ctx := context.Background()
	now := datasetNow(t, f)
	id := syncing.NewID()
	if _, err := f.s.CreateTrainingPlan(ctx, f.p, training.CreateRequest{CommandID: syncing.NewID(), PlanID: id, Config: training.Config{
		Version: training.ConfigVersionV2, StartDate: mondayOf(now).Format(time.DateOnly), AvailableDays: []int{2, 4, 6, 7}, LongRunDay: 7, BaselineWeeklyS: training.Ptr(9000),
	}}); err != nil {
		t.Fatal(err)
	}
	processTrainingJobs(t, f, now)
	p, err := f.s.TrainingPlan(ctx, f.p, id)
	if err != nil || p.Effective == nil {
		t.Fatal("plan not published", err)
	}
	return p
}

func startTrainingGeneration(t *testing.T, f *fixture, p training.Plan) (analysis.Lease, training.Input, string, training.Base) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.s.ChangeTrainingPlan(ctx, f.p, p.ID, training.ChangeRequest{CommandID: syncing.NewID(), Operation: "recalculate", ExpectedConfigRevision: p.ConfigRevision}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(ctx, `UPDATE jobs SET run_after=now() WHERE kind='training.plan' AND state='queued'`); err != nil {
		t.Fatal(err)
	}
	l, err := f.s.Claim(ctx, []string{"training.plan"})
	if err != nil {
		t.Fatal(err)
	}
	in, err := f.s.LoadTraining(ctx, l, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	id, err := f.s.StartTrainingRun(ctx, l, in)
	if err != nil {
		t.Fatal(err)
	}
	base, err := training.Generate(in.Context)
	if err != nil {
		t.Fatal(err)
	}
	return l, in, id, base
}

// Exact snapshots are private synthetic test state; failures report only the
// table name, never payloads. Each fixture has its own disposable database.
func trainingSnapshot(t *testing.T, f *fixture, model bool) map[string]string {
	t.Helper()
	tables := []string{"datasets", "training_plans", "training_sessions", "training_base_versions", "training_effective_versions", "training_generation_runs", "training_commands", "training_overrides", "training_feedback", "dirty_scopes", "jobs", "job_attempts"}
	if model {
		tables = append(tables, "analysis_runs", "analysis_publications", "analysis_artifacts", "model_workout_results", "model_workout_failures")
	}
	out := map[string]string{}
	for _, table := range tables {
		var b string
		if err := f.s.Pool.QueryRow(context.Background(), `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM `+pgx.Identifier{table}.Sanitize()+` t`).Scan(&b); err != nil {
			t.Fatal(err)
		}
		out[table] = b
	}
	return out
}
func requireTrainingSnapshot(t *testing.T, f *fixture, before map[string]string, model bool) {
	t.Helper()
	after := trainingSnapshot(t, f, model)
	for table, b := range before {
		if after[table] != b {
			t.Fatalf("failed publication changed %s", table)
		}
	}
}

func TestTrainingPlanV4PublicationRollbackAndRetry(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := createPublishedV4(t, f)
	l, in, id, base := startTrainingGeneration(t, f, p)
	// Fail after the session, base comparison and feedback writes, while the
	// run is being marked finished. (Regenerating the unchanged plan appends no
	// base or effective version, so the failure is injected later.)
	if _, err := f.s.Pool.Exec(ctx, `CREATE FUNCTION synthetic_training_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic_training_failure'; END $$;
 CREATE TRIGGER synthetic_training_fail BEFORE UPDATE ON training_generation_runs FOR EACH ROW EXECUTE FUNCTION synthetic_training_fail()`); err != nil {
		t.Fatal(err)
	}
	before := trainingSnapshot(t, f, false)
	if _, err := f.s.PublishTraining(ctx, l, id, in, base); err == nil {
		t.Fatal("injected failure ignored")
	}
	requireTrainingSnapshot(t, f, before, false)
	if _, err := f.s.Pool.Exec(ctx, `DROP TRIGGER synthetic_training_fail ON training_generation_runs; DROP FUNCTION synthetic_training_fail()`); err != nil {
		t.Fatal(err)
	}
	if status, err := f.s.PublishTraining(ctx, l, id, in, base); err != nil || status != "SUCCEEDED" {
		t.Fatalf("retry: %s %v", status, err)
	}
	next, err := f.s.TrainingPlan(ctx, f.p, p.ID)
	if err != nil || next.Effective == nil || next.Effective.Number != p.Effective.Number || next.Effective.ID != p.Effective.ID {
		t.Fatal("retry of an unchanged plan must keep its version", err)
	}
	var clean bool
	if err = f.s.Pool.QueryRow(ctx, `SELECT generation=clean_generation FROM dirty_scopes WHERE dataset_id=$1 AND scope_key=$2`, f.p.DatasetID, l.ScopeKey).Scan(&clean); err != nil || !clean {
		t.Fatal("successful retry left plan dirty", err)
	}
}

func TestTrainingPlanV4ConcurrentOverrideSupersedesGeneration(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := createPublishedV4(t, f)
	l, in, id, base := startTrainingGeneration(t, f, p)
	var target training.Session
	for _, s := range p.Effective.Sessions {
		if s.Date >= in.Context.AsOf && s.Type != "REST" {
			target = s
			break
		}
	}
	if target.ID == "" {
		t.Fatal("no future run")
	}
	type result struct {
		label string
		err   error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			label := fmt.Sprintf("synthetic override %d", n)
			value, _ := json.Marshal(label)
			_, err := f.s.PutTrainingOverride(ctx, f.p, p.ID, training.OverrideRequest{CommandID: syncing.NewID(), ExpectedOverrideRevision: p.OverrideRevision, OverrideID: syncing.NewID(), SessionID: target.ID, Operation: "edit", Path: "notes", Value: value})
			results <- result{label, err}
		}(n)
	}
	close(start)
	wg.Wait()
	close(results)
	winner := ""
	success, conflict := 0, 0
	for r := range results {
		if r.err == nil {
			winner = r.label
			success++
		} else {
			requireSyncCode(t, r.err, "training_revision_conflict")
			conflict++
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	edited, err := f.s.TrainingPlan(ctx, f.p, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := f.s.PublishTraining(ctx, l, id, in, base); err != nil || status != "SUPERSEDED" {
		t.Fatalf("stale generation: %s %v", status, err)
	}
	stale, err := f.s.TrainingPlan(ctx, f.p, p.ID)
	if err != nil || stale.Effective.ID != edited.Effective.ID {
		t.Fatal("stale generation overwrote user version", err)
	}
	processTrainingJobs(t, f, time.Now())
	final, err := f.s.TrainingPlan(ctx, f.p, p.ID)
	if err != nil || final.Effective == nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range final.Effective.Sessions {
		if s.ID == target.ID {
			found = true
			if s.Notes != winner {
				t.Fatal("regeneration lost accepted override")
			}
		}
	}
	// The override already produced the effective version; regenerating the
	// unchanged base keeps it.
	if !found || final.OverrideRevision != p.OverrideRevision+1 || final.Effective.Number != edited.Effective.Number {
		t.Fatal("override or version sequence changed")
	}
}

func TestTrainingPlanV4ExpiredWorkerCannotPublish(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := createPublishedV4(t, f)
	l, in, id, base := startTrainingGeneration(t, f, p)
	if _, err := f.s.Pool.Exec(ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, l.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.RequeueExpired(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(ctx, `UPDATE jobs SET run_after=now() WHERE id=$1`, l.ID); err != nil {
		t.Fatal(err)
	}
	replacement, err := f.s.Claim(ctx, []string{"training.plan"})
	if err != nil || replacement.FencingToken <= l.FencingToken {
		t.Fatal("replacement not fenced", err)
	}
	before := trainingSnapshot(t, f, false)
	_, err = f.s.PublishTraining(ctx, l, id, in, base)
	requireSyncCode(t, err, "lease_lost")
	requireTrainingSnapshot(t, f, before, false)
	nextIn, err := f.s.LoadTraining(ctx, replacement, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	nextID, err := f.s.StartTrainingRun(ctx, replacement, nextIn)
	if err != nil {
		t.Fatal(err)
	}
	nextBase, err := training.Generate(nextIn.Context)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := f.s.PublishTraining(ctx, replacement, nextID, nextIn, nextBase); err != nil || status != "SUCCEEDED" {
		t.Fatalf("replacement: %s %v", status, err)
	}
}

func TestTrainingPlanV4ActiveDayPublicationRollsBackWithDirtying(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	today, _ := runStackDay(t, f, nil, stackDayOptions{})
	hash, err := modelmanifest.CurrentHash()
	if err != nil {
		t.Fatal(err)
	}
	stack := modelmanifest.StackID()
	setModelChannel(t, f, "active", stack, hash)
	createPublishedV4(t, f)
	if _, err = f.s.Pool.Exec(ctx, `SELECT rai_schedule_model_day($1,$2,$3::date,now())`, f.p.DatasetID, stack, today.Format(time.DateOnly)); err != nil {
		t.Fatal(err)
	}
	l, err := f.s.Claim(ctx, []string{modelmanifest.StackJobKind})
	if err != nil {
		t.Fatal(err)
	}
	// This fails at plan dirtying, after the model artifacts have been written.
	if _, err = f.s.Pool.Exec(ctx, `CREATE FUNCTION synthetic_dirty_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic_dirty_failure'; END $$;
 CREATE TRIGGER synthetic_dirty_fail BEFORE UPDATE ON dirty_scopes FOR EACH ROW WHEN (NEW.scope_key LIKE 'plan:%') EXECUTE FUNCTION synthetic_dirty_fail()`); err != nil {
		t.Fatal(err)
	}
	before := trainingSnapshot(t, f, true)
	if _, err = f.s.ProcessModelStack(ctx, l, time.Now()); err == nil {
		t.Fatal("dirtying failure ignored")
	}
	requireTrainingSnapshot(t, f, before, true)
	if _, err = f.s.Pool.Exec(ctx, `DROP TRIGGER synthetic_dirty_fail ON dirty_scopes; DROP FUNCTION synthetic_dirty_fail()`); err != nil {
		t.Fatal(err)
	}
	if status, err := f.s.ProcessModelStack(ctx, l, time.Now()); err != nil || status != "SUCCEEDED" {
		t.Fatalf("model retry: %s %v", status, err)
	}
	if n := queuedTrainingJobs(t, f); n != 1 {
		t.Fatalf("queued %d plans, want one", n)
	}
	processTrainingJobs(t, f, time.Now())
	// Moving the dataset's clock to tomorrow queues exactly one daily release;
	// no device upload or API request is needed to trigger it.
	tomorrow := today.AddDate(0, 0, 1).Add(30 * time.Minute)
	for i := 0; i < 2; i++ {
		if err = f.s.AdvanceAnalysisDay(ctx, tomorrow); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE dataset_id=$1 AND scope_key=$2 AND state='queued'`, f.p.DatasetID, modelmanifest.DayScope(stack, tomorrow.Format(time.DateOnly))).Scan(&n); err != nil || n != 1 {
		t.Fatalf("tomorrow jobs=%d: %v", n, err)
	}
}

func TestTrainingPlanV4RecordsFrozenContextParameters(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	p := createPublishedV4(t, f)
	if _, err := f.s.ChangeTrainingPlan(ctx, f.p, p.ID, training.ChangeRequest{CommandID: syncing.NewID(), Operation: "recalculate", ExpectedConfigRevision: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(ctx, `UPDATE jobs SET run_after=now() WHERE kind='training.plan' AND state='queued'`); err != nil {
		t.Fatal(err)
	}
	l, err := f.s.Claim(ctx, []string{"training.plan"})
	if err != nil {
		t.Fatal(err)
	}
	in, err := f.s.LoadTraining(ctx, l, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	old := training.V2Parameters()
	in.Context.Coach = &old
	id, err := f.s.StartTrainingRun(ctx, l, in)
	if err != nil {
		t.Fatal(err)
	}
	base, err := training.Generate(in.Context)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := f.s.PublishTraining(ctx, l, id, in, base); err != nil || status != "SUCCEEDED" {
		t.Fatalf("frozen context: %s %v", status, err)
	}
	var version string
	var raw []byte
	if err = f.s.Pool.QueryRow(ctx, `SELECT parameter_version,context FROM training_generation_runs WHERE dataset_id=$1 AND id=$2`, f.p.DatasetID, id).Scan(&version, &raw); err != nil {
		t.Fatal(err)
	}
	var replay training.Context
	if err = json.Unmarshal(raw, &replay); err != nil || version != training.ParameterVersionV2 || replay.Coach == nil || *replay.Coach != old {
		t.Fatal("run did not retain actual frozen parameters", err)
	}
	replayed, err := training.Generate(replay)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := training.Hash(base)
	got, _ := training.Hash(replayed)
	if string(want) != string(got) {
		t.Fatal("database context did not reproduce the published base")
	}
}
