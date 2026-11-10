//go:build integration

package integration

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"rai/server/internal/jobs"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
	tl "rai/server/internal/trainingload"
	wf "rai/server/internal/workoutfeatures"
)

// TestDirtyFlowPublishAfterIngest proves the live protocol-6 pipeline can feed
// the analysis publication path end to end on a real PostgreSQL database:
// ingest acceptance + processing materializes workout facts, a dirty scope is
// queued for that workout, and a real jobs.Process claim publishes the analysis
// run, clears the dirty generation and enqueues the downstream athlete scope.
func TestDirtyFlowPublishAfterIngest(t *testing.T) {
	f := setup(t)
	ctx := context.Background()

	workoutID := syncing.NewID()
	raw := ingestRaw(t, f.p.DatasetID, f.enrollment.DatasetEpoch, f.enrollment.CollectorEpoch, workoutID, 128)
	acceptIngest(t, f, f.p, raw)
	outcomes := processAllIngests(t, f.s)
	if len(outcomes) != 1 {
		t.Fatalf("processed %d ingest outcomes, want 1", len(outcomes))
	}

	// Read the projected workout facts (what the v6 import wrote) to build the
	// analysis scope and dirty range from real stored data.
	var workoutUUID string
	var eventStart, eventEnd time.Time
	if err := f.s.Pool.QueryRow(ctx, `SELECT workout_uuid::text, event_start, event_end
		FROM workouts WHERE dataset_id=$1`, f.p.DatasetID).
		Scan(&workoutUUID, &eventStart, &eventEnd); err != nil {
		t.Fatalf("read imported workout: %v", err)
	}
	// The v6 publish path addresses workout scopes as
	// workout:<WorkoutType>:<uuid> (see analysis_publish.go).
	scope := "workout:" + syncing.WorkoutType + ":" + workoutUUID
	jobKind := "analysis.workout"

	// Queue the analysis work the same way the storage layer does for a dirty
	// workout scope (the ingest path itself does not enqueue analysis jobs).
	tx, err := f.s.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err = postgres.MarkAnalysisDirty(ctx, tx, f.p.DatasetID, scope, jobKind, eventStart, eventEnd); err != nil {
		tx.Rollback(ctx)
		t.Fatalf("mark dirty: %v", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatalf("commit dirty scope: %v", err)
	}
	generation, from, through := readDirtyScope(t, f, scope)
	if !from.Equal(eventStart) || !through.Equal(eventEnd) {
		t.Fatalf("dirty range = [%s,%s], want [%s,%s]", from, through, eventStart, eventEnd)
	}

	claimKinds := []string{jobKind}
	lease, err := f.s.Claim(ctx, claimKinds)
	if err != nil {
		t.Fatalf("claim analysis job: %v", err)
	}
	if lease.ScopeKey != scope || lease.Kind != jobKind || lease.TargetGeneration != generation {
		t.Fatalf("claim = kind %q scope %q target %d, want kind %q scope %q target %d",
			lease.Kind, lease.ScopeKey, lease.TargetGeneration, jobKind, scope, generation)
	}

	// Run the deterministic single-claim worker harness: LoadAnalysis,
	// StartAnalysisRun, PublishAnalysis/Complete inside one real claim.
	jobs.Process(ctx, f.s, lease, slog.Default(), time.Now().UTC())

	var state string
	if err := f.s.Pool.QueryRow(ctx, `SELECT state FROM jobs WHERE id=$1`, lease.ID).Scan(&state); err != nil {
		t.Fatalf("read job state: %v", err)
	}
	if state != "succeeded" {
		t.Fatalf("job state = %q, want succeeded", state)
	}
	var cleanGeneration int64
	if err := f.s.Pool.QueryRow(ctx, `SELECT clean_generation FROM dirty_scopes WHERE dataset_id=$1 AND scope_key=$2`,
		f.p.DatasetID, scope).Scan(&cleanGeneration); err != nil {
		t.Fatalf("read clean generation: %v", err)
	}
	if cleanGeneration != generation {
		t.Fatalf("clean_generation = %d, want %d (dirty scope still unpublished)", cleanGeneration, generation)
	}

	var succeededRuns int
	if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM analysis_runs
		WHERE dataset_id=$1 AND scope_key=$2 AND status='SUCCEEDED'`, f.p.DatasetID, scope).
		Scan(&succeededRuns); err != nil {
		t.Fatalf("count analysis runs: %v", err)
	}
	if succeededRuns == 0 {
		t.Fatal("no SUCCEEDED analysis run for published workout scope")
	}

	// Publishing a workout analysis run must enqueue the downstream athlete
	// training-state scope, proving the publish-side dirty write happened.
	var downstream int
	if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM dirty_scopes
		WHERE dataset_id=$1 AND scope_key='athlete/default'`, f.p.DatasetID).Scan(&downstream); err != nil {
		t.Fatalf("count downstream dirty scopes: %v", err)
	}
	if downstream == 0 {
		t.Fatal("publish did not enqueue downstream athlete/default training-state dirty scope")
	}
}

// TestDirtyFlowLoadDayAdvance proves the load day-boundary dirty write: a clean
// athlete load scope whose latest published load day is behind the requested
// instant is re-dirtied from the next unpublished UTC day through the instant.
func TestDirtyFlowLoadDayAdvance(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	at := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	if _, err := f.s.Pool.Exec(ctx, `UPDATE datasets SET timezone='UTC' WHERE id=$1`, f.p.DatasetID); err != nil {
		t.Fatalf("pin dataset timezone: %v", err)
	}

	scope := tl.StateScope()
	jobKind := tl.StateJobKind

	// Seed a clean (published) load scope whose dirty range lies inside the
	// requested day, then mark it clean so the load advance is eligible. Doing
	// this first also creates the jobs row the analysis run must reference.
	tx, err := f.s.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err = postgres.MarkAnalysisDirty(ctx, tx, f.p.DatasetID, scope, jobKind, at, at); err != nil {
		tx.Rollback(ctx)
		t.Fatalf("mark load scope dirty: %v", err)
	}
	if _, err = tx.Exec(ctx, `UPDATE dirty_scopes SET clean_generation=generation
		WHERE dataset_id=$1 AND scope_key=$2`, f.p.DatasetID, scope); err != nil {
		tx.Rollback(ctx)
		t.Fatalf("mark load scope clean: %v", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatalf("commit clean load scope: %v", err)
	}
	cleanBefore, _, _ := readDirtyScope(t, f, scope)

	var jobID string
	if err := f.s.Pool.QueryRow(ctx, `SELECT id::text FROM jobs
		WHERE dataset_id=$1 AND scope_key=$2 AND kind=$3 ORDER BY id LIMIT 1`,
		f.p.DatasetID, scope, jobKind).Scan(&jobID); err != nil {
		t.Fatalf("read load job: %v", err)
	}

	// Seed a published analysis run + publication for the load scope so the
	// load-advance candidate sees an up-to-date last published day.
	publishedDay := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	var runID string
	if err := f.s.Pool.QueryRow(ctx, `INSERT INTO analysis_runs
		(dataset_id,id,job_id,fencing_token,scope_key,target_generation,algorithm_name,algorithm_version,
		 input_version,input_hash,input_from,input_through,lineage,status,started_at,finished_at)
		VALUES ($1,gen_random_uuid(),$2,1,$3,1,'test','v1','v1',decode(repeat('00',32),'hex'),
		 $4,$4,'{}'::jsonb,'SUCCEEDED',now(),now()) RETURNING id::text`,
		f.p.DatasetID, jobID, scope, publishedDay).Scan(&runID); err != nil {
		t.Fatalf("seed analysis run: %v", err)
	}
	if _, err := f.s.Pool.Exec(ctx, `INSERT INTO analysis_publications
		(dataset_id,scope_key,metric_family,day,published_run_id,generation)
		VALUES ($1,$2,'load',$3,$4,1)`, f.p.DatasetID, scope, publishedDay, runID); err != nil {
		t.Fatalf("seed publication: %v", err)
	}

	// A model dependency whose downstream scope is a workout_load scope makes
	// the dataset eligible for the candidate-load advance.
	// The dependency CHECK requires the upstream side to be a per-workout scope
	// (…/workout/<uuid>) and the downstream side a workout_load scope.
	dependencyWorkout := syncing.NewID()
	upstream, downstream := wf.Scope(dependencyWorkout), tl.Scope(dependencyWorkout)
	if _, err := f.s.Pool.Exec(ctx, `INSERT INTO analysis_model_dependencies
		(dataset_id,upstream_scope,downstream_scope) VALUES ($1,$2,$3)`,
		f.p.DatasetID, upstream, downstream); err != nil {
		t.Fatalf("seed model dependency: %v", err)
	}

	if err := f.s.AdvanceAnalysisDay(ctx, at); err != nil {
		t.Fatalf("advance analysis day: %v", err)
	}
	generation, from, through := readDirtyScope(t, f, scope)
	if generation != cleanBefore+1 {
		t.Fatalf("generation = %d, want %d", generation, cleanBefore+1)
	}
	wantFrom := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	if !from.Equal(wantFrom) {
		t.Fatalf("dirty_from = %s, want %s (next unpublished UTC day)", from, wantFrom)
	}
	if !through.After(wantFrom) {
		t.Fatalf("dirty_through = %s, want instant after %s", through, wantFrom)
	}
	if clean, _, _ := readDirtyScope(t, f, scope); clean == 0 {
		t.Fatal("load scope generation not incremented")
	}

	var queuedJobs int
	if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM jobs
		WHERE dataset_id=$1 AND scope_key=$2 AND kind=$3 AND state='queued'`,
		f.p.DatasetID, scope, jobKind).Scan(&queuedJobs); err != nil {
		t.Fatalf("count queued load jobs: %v", err)
	}
	if queuedJobs == 0 {
		t.Fatal("load day advance did not enqueue a queued load-state analysis job")
	}
}
