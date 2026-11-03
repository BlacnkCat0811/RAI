//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"rai/server/internal/admin"
	"rai/server/internal/analysis"
	pf "rai/server/internal/performance"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
	wf "rai/server/internal/workoutfeatures"
)

func m3Bundle(t *testing.T, f *fixture, end time.Time, seconds int) syncing.WorkoutBundle {
	t.Helper()
	b := featureBundle(t, f, seconds)
	// Shift a complete valid synthetic bundle, including observation coverage.
	delta := end.Truncate(time.Second).UnixMicro() - *b.Workout.EndUs
	blob, _ := json.Marshal(b)
	var v any
	if json.Unmarshal(blob, &v) != nil {
		t.Fatal("fixture decode")
	}
	var shift func(any)
	shift = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for k, value := range x {
				if n, ok := value.(float64); ok && (k == "startUs" || k == "endUs" || k == "recordedAtUs" || k == "observedAtUs") {
					x[k] = n + float64(delta)
				} else {
					shift(value)
				}
			}
		case []any:
			for _, value := range x {
				shift(value)
			}
		}
	}
	shift(v)
	blob, _ = json.Marshal(v)
	if err := json.Unmarshal(blob, &b); err != nil {
		t.Fatal(err)
	}
	if err := b.Validate(); err != nil {
		t.Fatalf("shifted synthetic fixture: %#v", err)
	}
	return b
}
func performanceFixture(t *testing.T, established bool) (*fixture, []syncing.WorkoutBundle) {
	t.Helper()
	f := setup(t)
	n := 1
	if established {
		n = 7
	}
	bs := []syncing.WorkoutBundle{}
	latest := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < n; i++ {
		b := m3Bundle(t, f, latest.AddDate(0, 0, -4*(n-1-i)), 2400)
		importPhase3(t, f, b)
		bs = append(bs, b)
	}
	if err := admin.QueuePerformanceCandidate(context.Background(), f.s, f.p.DatasetID, syncing.NewID(), "synthetic M3"); err != nil {
		t.Fatal(err)
	}
	for range n {
		runOne(t, f, wf.JobKind)
	}
	return f, bs
}
func computePerformance(t *testing.T, f *fixture, l postgres.Lease) (analysis.Snapshot, string, []analysis.Metric) {
	t.Helper()
	ctx := context.Background()
	in, err := f.s.LoadAnalysis(ctx, l, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	id, err := f.s.StartAnalysisRun(ctx, l, in)
	if err != nil {
		t.Fatal(err)
	}
	if in.Generation != l.TargetGeneration {
		return in, id, nil
	}
	ms, err := analysis.CalculateCandidatePerformance(&in)
	if err != nil {
		t.Fatal(err)
	}
	return in, id, ms
}
func TestPerformancePipelineCandidateDeleteRebuild(t *testing.T) {
	f, bs := performanceFixture(t, true)
	ctx := context.Background()
	runOne(t, f, "analysis.workout")
	legacy, err := f.s.CurrentWorkoutAnalysis(ctx, f.p, bs[0].WorkoutID)
	if err != nil {
		t.Fatal(err)
	}
	runOne(t, f, pf.JobKind)
	r, run, err := f.s.CandidatePerformance(ctx, f.p.DatasetID, time.Now().UTC())
	if err != nil || r.Ability == nil || r.Status != "available" {
		t.Fatal("candidate unavailable", err, r.Status, r.ReasonCodes)
	}
	if r.Support.IndependentWorkouts != 7 || r.ValidTargetDays < 3 || r.Confidence > .55 {
		t.Fatal("support", r.Support, r.ValidTargetDays)
	}
	if math.Abs(r.Ability.V60-3) > 1e-8 || math.Abs(r.Ability.Score-50) > 1e-8 {
		t.Fatal("synthetic constant speed mismatch")
	}
	var artifacts, results int
	if err = f.s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM analysis_artifacts WHERE run_id=$1),(SELECT count(*) FROM analysis_results WHERE run_id=$1)`, run).Scan(&artifacts, &results); err != nil || artifacts != 5 || results != 3 {
		t.Fatal("non-atomic model bundle", err)
	}
	active, err := f.s.CurrentWorkoutAnalysis(ctx, f.p, bs[0].WorkoutID)
	if err != nil || active.RunID != legacy.RunID {
		t.Fatal("active overwritten", err)
	}
	// Explicit bounded reconstruction reuses the same M1 artifacts.
	if err = admin.QueuePerformanceCandidate(ctx, f.s, f.p.DatasetID, syncing.NewID(), "synthetic rebuild"); err != nil {
		t.Fatal(err)
	}
	runOne(t, f, pf.JobKind)
	rebuilt, _, err := f.s.CandidatePerformance(ctx, f.p.DatasetID, time.Now().UTC())
	if err != nil || rebuilt.Ability == nil || pf.Hash(*r.Ability) != pf.Hash(*rebuilt.Ability) {
		t.Fatal("bounded rebuild mismatch", err)
	}
	// Deletion invalidates the current pointer before any worker can run.
	if _, err = f.s.DeleteWorkoutBundle(ctx, f.p, bundleDeleteRequest(f, bs[3].WorkoutID)); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.CandidatePerformance(ctx, f.p.DatasetID, time.Now().UTC()); err == nil {
		t.Fatal("deleted workout remains current")
	}
	runOne(t, f, wf.JobKind)
	runOne(t, f, pf.JobKind)
	r, _, err = f.s.CandidatePerformance(ctx, f.p.DatasetID, time.Now().UTC())
	if err != nil || r.Ability != nil || r.Status != "collecting" {
		t.Fatal("lost target support not reflected", err, r.Status)
	}
	// Stored historical result survived the delete; new facts cannot mutate it.
	var previous pf.Result
	var body []byte
	if err = f.s.Pool.QueryRow(ctx, `SELECT payload FROM analysis_artifacts WHERE run_id=$1 AND artifact_key='performance_state'`, run).Scan(&body); err != nil || json.Unmarshal(body, &previous) != nil || previous.Ability == nil {
		t.Fatal("history lost", err)
	}
	archived, e := f.s.HistoricalPerformance(ctx, f.p.DatasetID, run)
	if e != nil || archived.Ability == nil || pf.Hash(archived.Ability) != pf.Hash(previous.Ability) {
		t.Fatal("historical inspect unavailable", e)
	}
	var plan bytes.Buffer
	if e = admin.PerformanceCommand(ctx, f.s.Pool.Config().ConnString(), []string{"promotion-plan", "--dataset", f.p.DatasetID}, &plan); e != nil {
		t.Fatal(e)
	}
	var projection struct {
		Promotable bool   `json:"promotable"`
		Active     string `json:"active"`
		Rollback   string `json:"rollback"`
	}
	if json.Unmarshal(plan.Bytes(), &projection) != nil || projection.Promotable || projection.Active != "legacy" || projection.Rollback == "" {
		t.Fatal("promotion/rollback plan missing")
	}
}
func TestPerformanceGenerationRaceAndLeaseFencing(t *testing.T) {
	for _, change := range []string{"delete", "feature_revision", "lease"} {
		t.Run(change, func(t *testing.T) {
			f, bs := performanceFixture(t, false)
			ctx := context.Background()
			l := claimAnalysis(t, f, pf.JobKind)
			in, id, ms := computePerformance(t, f, l)
			if change == "delete" {
				if _, e := f.s.DeleteWorkoutBundle(ctx, f.p, bundleDeleteRequest(f, bs[0].WorkoutID)); e != nil {
					t.Fatal(e)
				}
			} else if change == "feature_revision" {
				f.commit(t, []syncing.Event{{Op: "delete", Kind: "sample", Type: kind, UUID: bs[0].Samples[0].UUID}}, 1, nil)
				runOne(t, f, wf.JobKind)
			} else {
				if _, e := f.s.Pool.Exec(ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, l.ID); e != nil {
					t.Fatal(e)
				}
				if _, e := f.s.RequeueExpired(ctx); e != nil {
					t.Fatal(e)
				}
			}
			status, e := f.s.PublishAnalysis(ctx, l, id, in, ms)
			if change == "lease" {
				if e == nil {
					t.Fatal("expired worker published")
				}
			} else if e != nil || status != "SUPERSEDED" {
				t.Fatal("old generation published", status, e)
			}
			var artifacts int
			var clean bool
			if e = f.s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM analysis_artifacts WHERE run_id=$1),generation=clean_generation FROM dirty_scopes WHERE dataset_id=$2 AND scope_key=$3`, id, f.p.DatasetID, pf.Scope()).Scan(&artifacts, &clean); e != nil || artifacts != 0 || clean {
				t.Fatal("stale artifacts/clean", e)
			}
		})
	}
}
func TestPerformancePublicationFaultsRollback(t *testing.T) {
	for _, stage := range []string{"evidence_artifact", "curve_result", "ability_result", "publication", "dirty_clean", "job_terminal"} {
		t.Run(stage, func(t *testing.T) {
			f, _ := performanceFixture(t, true)
			ctx := context.Background()
			l := claimAnalysis(t, f, pf.JobKind)
			in, id, ms := computePerformance(t, f, l)
			table, op, condition := "analysis_artifacts", "INSERT", "NEW.artifact_key='performance_evidence'"
			switch stage {
			case "curve_result":
				table = "analysis_results"
				condition = "NEW.metric='supported_v60'"
			case "ability_result":
				table = "analysis_results"
				condition = "NEW.metric='running_ability'"
			case "publication":
				table = "analysis_publications"
				condition = "true"
			case "dirty_clean":
				table = "dirty_scopes"
				op = "UPDATE"
				condition = "NEW.clean_generation>OLD.clean_generation"
			case "job_terminal":
				table = "jobs"
				op = "UPDATE"
				condition = "NEW.state='succeeded'"
			}
			if _, err := f.s.Pool.Exec(ctx, `CREATE FUNCTION m3_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF `+condition+` THEN RAISE EXCEPTION 'synthetic_m3_fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER m3_fault BEFORE `+op+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION m3_fault()`); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.PublishAnalysis(ctx, l, id, in, ms); err == nil {
				t.Fatal("fault succeeded")
			}
			assertPerformanceRollback(t, f, id)
			if _, err := f.s.Pool.Exec(ctx, `DROP TRIGGER m3_fault ON `+table); err != nil {
				t.Fatal(err)
			}
			if status, err := f.s.PublishAnalysis(ctx, l, id, in, ms); err != nil || status != "SUCCEEDED" {
				t.Fatal("retry", status, err)
			}
		})
	}
}
func assertPerformanceRollback(t *testing.T, f *fixture, id string) {
	t.Helper()
	var partial int
	var status, state string
	var clean bool
	err := f.s.Pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM analysis_artifacts WHERE run_id=$1)+(SELECT count(*) FROM analysis_results WHERE run_id=$1)+(SELECT count(*) FROM analysis_publications WHERE published_run_id=$1),r.status,j.state,d.generation=d.clean_generation FROM analysis_runs r JOIN jobs j ON j.id=r.job_id JOIN dirty_scopes d ON d.dataset_id=r.dataset_id AND d.scope_key=r.scope_key WHERE r.id=$1`, id).Scan(&partial, &status, &state, &clean)
	if err != nil || partial != 0 || status != "RUNNING" || state != "running" || clean {
		t.Fatal("partial publication after fault/crash", err, partial, status, state, clean)
	}
}
func TestPerformanceBoundedLateArrivalAndPending(t *testing.T) {
	f, bs := performanceFixture(t, false)
	ctx := context.Background()
	runOne(t, f, pf.JobKind)
	_, before, err := f.s.CandidatePerformance(ctx, f.p.DatasetID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	old := m3Bundle(t, f, time.Now().UTC().AddDate(0, 0, -120), 1200)
	importPhase3(t, f, old)
	if _, after, e := f.s.CandidatePerformance(ctx, f.p.DatasetID, time.Now().UTC()); e != nil || before != after {
		t.Fatal("outside 90d revised current", e)
	}
	runOne(t, f, wf.JobKind)
	if _, after, e := f.s.CandidatePerformance(ctx, f.p.DatasetID, time.Now().UTC()); e != nil || before != after {
		t.Fatal("old feature publish revised current", e)
	}
	late := m3Bundle(t, f, time.Now().UTC().AddDate(0, 0, -20), 2400)
	importPhase3(t, f, late)
	if _, _, e := f.s.CandidatePerformance(ctx, f.p.DatasetID, time.Now().UTC()); e == nil {
		t.Fatal("late current not invalidated")
	}
	l := claimAnalysis(t, f, pf.JobKind)
	if _, e := f.s.LoadAnalysis(ctx, l, time.Now().UTC()); e == nil {
		t.Fatal("pending feature silently skipped")
	}
	if e := f.s.FailAnalysis(ctx, l, "", "analysis_dependency_pending", true); e != nil {
		t.Fatal(e)
	}
	runOne(t, f, wf.JobKind)
	runOne(t, f, pf.JobKind)
	r, _, err := f.s.CandidatePerformance(ctx, f.p.DatasetID, time.Now().UTC())
	if err != nil || r.Support.IndependentWorkouts != 2 || r.Ability != nil {
		t.Fatal("late evidence missing", err)
	}
	// An actual M1 fact deletion invalidates the complete feature dependency.
	f.commit(t, []syncing.Event{{Op: "delete", Kind: "sample", Type: kind, UUID: bs[0].Samples[0].UUID}}, 1, nil)
	if _, _, e := f.s.CandidatePerformance(ctx, f.p.DatasetID, time.Now().UTC()); e == nil {
		t.Fatal("feature change left pointer")
	}
}

func TestPerformanceEmptyAndIdempotentEnrollment(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	op := syncing.NewID()
	for i := 0; i < 2; i++ {
		if err := admin.QueuePerformanceCandidate(ctx, f.s, f.p.DatasetID, op, "synthetic cold start"); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE dataset_id=$1 AND kind=$2`, f.p.DatasetID, pf.JobKind).Scan(&count); err != nil || count != 1 {
		t.Fatal("operation retry created evidence/job", err, count)
	}
	runOne(t, f, pf.JobKind)
	r, _, err := f.s.CandidatePerformance(ctx, f.p.DatasetID, time.Now().UTC())
	if err != nil || r.Status != "collecting" || r.Ability != nil || r.Confidence != 0 || r.Support.IndependentWorkouts != 0 {
		t.Fatal("empty athlete fabricated a value", err, r.Status)
	}
}

func TestPerformanceOldSeedDeleteBeforeFirstPointerFenced(t *testing.T) {
	f, _ := performanceFixture(t, true)
	ctx := context.Background()
	old := m3Bundle(t, f, time.Now().UTC().AddDate(0, 0, -95), 2400)
	importPhase3(t, f, old)
	// Synthetic metadata represents a workout that was received 95 days ago,
	// rather than a new out-of-window late arrival. No real data is accessed.
	if _, err := f.s.Pool.Exec(ctx, `UPDATE workouts SET first_received_at=event_end+interval '1 hour' WHERE dataset_id=$1 AND workout_uuid=$2`, f.p.DatasetID, old.WorkoutID); err != nil {
		t.Fatal(err)
	}
	runOne(t, f, wf.JobKind)
	l := claimAnalysis(t, f, pf.JobKind)
	in, id, ms := computePerformance(t, f, l)
	found := false
	for _, ref := range in.PerformanceInput.Features {
		if ref.Feature.WorkoutID == old.WorkoutID {
			found = true
		}
	}
	if !found {
		t.Fatal("old smoothing seed missing")
	}
	if _, err := f.s.DeleteWorkoutBundle(ctx, f.p, bundleDeleteRequest(f, old.WorkoutID)); err != nil {
		t.Fatal(err)
	}
	status, err := f.s.PublishAnalysis(ctx, l, id, in, ms)
	if err != nil || status != "SUPERSEDED" {
		t.Fatal("stale seed published", status, err)
	}
	var dirty bool
	var queued, artifacts int
	if err = f.s.Pool.QueryRow(ctx, `SELECT generation>clean_generation,(SELECT count(*) FROM jobs WHERE dataset_id=$1 AND kind=$3 AND state='queued' AND target_generation>$4),(SELECT count(*) FROM analysis_artifacts WHERE run_id=$5) FROM dirty_scopes WHERE dataset_id=$1 AND scope_key=$2`, f.p.DatasetID, pf.Scope(), pf.JobKind, l.TargetGeneration, id).Scan(&dirty, &queued, &artifacts); err != nil || !dirty || queued == 0 || artifacts != 0 {
		t.Fatal("stale seed failed to queue repair", err, dirty, queued, artifacts)
	}
	runOne(t, f, wf.JobKind)
	runOne(t, f, pf.JobKind)
	r, _, err := f.s.CandidatePerformance(ctx, f.p.DatasetID, time.Now().UTC())
	if err != nil || r.Ability == nil || r.Support.IndependentWorkouts != 7 {
		t.Fatal("seed repair failed", err, r.Status)
	}
}
