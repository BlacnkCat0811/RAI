//go:build integration

package integration

import (
	"context"
	"math"
	"rai/server/internal/admin"
	"rai/server/internal/analysis"
	"rai/server/internal/jobs"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
	tl "rai/server/internal/trainingload"
	wf "rai/server/internal/workoutfeatures"
	"testing"
	"time"
)

func loadFixture(t *testing.T) (*fixture, syncing.WorkoutBundle) {
	t.Helper()
	f, b := featureFixture(t)
	if err := admin.QueueTrainingLoadCandidate(context.Background(), f.s, f.p.DatasetID, b.WorkoutID, syncing.NewID(), "synthetic M2"); err != nil {
		t.Fatal(err)
	}
	runOne(t, f, wf.JobKind)
	return f, b
}
func computeLoad(t *testing.T, f *fixture, l postgres.Lease) (analysis.Snapshot, string, []analysis.Metric) {
	t.Helper()
	ctx := context.Background()
	in, e := f.s.LoadAnalysis(ctx, l, time.Now().UTC())
	if e != nil {
		t.Fatal(e)
	}
	id, e := f.s.StartAnalysisRun(ctx, l, in)
	if e != nil {
		t.Fatal(e)
	}
	if in.Generation != l.TargetGeneration {
		return in, id, nil
	}
	ms, e := analysis.CalculateCandidateLoad(&in, l.Kind)
	if e != nil {
		t.Fatal(e)
	}
	return in, id, ms
}
func TestLoadCandidatePipelineLedgerStateAndActiveIsolation(t *testing.T) {
	f, b := loadFixture(t)
	ctx := context.Background()
	runOne(t, f, "analysis.workout")
	legacy, e := f.s.CurrentWorkoutAnalysis(ctx, f.p, b.WorkoutID)
	if e != nil {
		t.Fatal(e)
	}
	runOne(t, f, tl.JobKind)
	r, id, e := f.s.CandidateLoad(ctx, f.p.DatasetID, b.WorkoutID, "2026-10-31")
	if e != nil || r.Load == nil || r.Method != "hrr_integral" {
		t.Fatal("candidate load", e, r.ReasonCodes)
	}
	want := 30 * tl.Intensity(2.0/3)
	if math.Abs(*r.Load-want) > 1e-10 {
		t.Fatal("trace replaced by 190bpm summary")
	}
	if r.HRReference == nil || r.HRReference.ID == "" || r.FeatureArtifactID == "" {
		t.Fatal("parameter/feature lineage")
	}
	var total float64
	if e = f.s.Pool.QueryRow(ctx, `SELECT sum(contribution) FROM analysis_load_ledger WHERE run_id=$1`, id).Scan(&total); e != nil || math.Abs(total-*r.Load) > 1e-12 {
		t.Fatal("ledger conservation", e)
	}
	runOne(t, f, tl.StateJobKind)
	d, _, e := f.s.CandidateLoadState(ctx, f.p.DatasetID, "2026-10-31")
	if e != nil || d.Acute.Value == nil || d.Chronic.Value == nil || d.Balance == nil || d.HistoryDaysObserved != 1 || d.Quality != "partial" {
		t.Fatal("state", e, d)
	}
	current, e := f.s.CurrentWorkoutAnalysis(ctx, f.p, b.WorkoutID)
	if e != nil || current.RunID != legacy.RunID {
		t.Fatal("candidate overwrote legacy", e)
	}
	for _, sql := range []string{`UPDATE analysis_load_ledger SET contribution=0`, `DELETE FROM analysis_load_ledger`, `UPDATE analysis_artifacts SET payload='{}'`} {
		if _, e = f.s.Pool.Exec(ctx, sql); e == nil {
			t.Fatal("mutable history")
		}
	}
	// New fact invalidates the entire dependent graph in its own transaction.
	f.commit(t, []syncing.Event{{Op: "delete", Kind: "sample", Type: kind, UUID: b.Samples[0].UUID}}, 1, nil)
	if _, _, e = f.s.CandidateLoad(ctx, f.p.DatasetID, b.WorkoutID, "2026-10-31"); e == nil {
		t.Fatal("deleted evidence still current")
	}
	if _, _, e = f.s.CandidateLoadState(ctx, f.p.DatasetID, "2026-10-31"); e == nil {
		t.Fatal("state not immediately invalidated")
	}
	runOne(t, f, wf.JobKind)
	runOne(t, f, tl.JobKind)
	runOne(t, f, tl.StateJobKind)
	d, _, e = f.s.CandidateLoadState(ctx, f.p.DatasetID, "2026-10-31")
	if e != nil || d.RecordedLoad == nil || d.Acute.Value == nil {
		t.Fatal("surviving summary unavailable", e, d)
	}
	summary, _, e := f.s.CandidateLoad(ctx, f.p.DatasetID, b.WorkoutID, "2026-10-31")
	if e != nil || summary.Method != "summary_hr_approximation" || summary.Quality != "partial" {
		t.Fatal("summary fallback disguised", e)
	}
}
func TestLoadGenerationRaceAndWorkoutDelete(t *testing.T) {
	for _, kindToRace := range []string{tl.JobKind, tl.StateJobKind} {
		t.Run(kindToRace, func(t *testing.T) {
			f, b := loadFixture(t)
			ctx := context.Background()
			if kindToRace == tl.StateJobKind {
				runOne(t, f, tl.JobKind)
			}
			l := claimAnalysis(t, f, kindToRace)
			in, id, ms := computeLoad(t, f, l)
			if _, e := f.s.DeleteWorkoutBundle(ctx, f.p, bundleDeleteRequest(f, b.WorkoutID)); e != nil {
				t.Fatal(e)
			}
			status, e := f.s.PublishAnalysis(ctx, l, id, in, ms)
			if e != nil || status != "SUPERSEDED" {
				t.Fatal("old generation published", status, e)
			}
			var artifacts int
			var clean bool
			if e = f.s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM analysis_artifacts WHERE run_id=$1),generation=clean_generation FROM dirty_scopes WHERE dataset_id=$2 AND scope_key=$3`, id, f.p.DatasetID, l.ScopeKey).Scan(&artifacts, &clean); e != nil || artifacts != 0 || clean {
				t.Fatal("stale publish/clean", e)
			}
			runOne(t, f, wf.JobKind)
			runOne(t, f, tl.JobKind)
			runOne(t, f, tl.StateJobKind)
			if _, _, e = f.s.CandidateLoad(ctx, f.p.DatasetID, b.WorkoutID, "2026-10-31"); e == nil {
				t.Fatal("workout load resurrected")
			}
			d, _, e := f.s.CandidateLoadState(ctx, f.p.DatasetID, "2026-10-31")
			if e != nil || d.RecordedLoad == nil || *d.RecordedLoad != 0 || d.RecordedWorkouts != 0 {
				t.Fatal("deleted contribution retained", e, d)
			}
		})
	}
}
func TestLoadPublicationFaultsRollbackRetry(t *testing.T) {
	for _, stage := range []string{"artifact", "load_result", "ledger", "state", "publication", "dirty_clean", "job_terminal"} {
		t.Run(stage, func(t *testing.T) {
			f, _ := loadFixture(t)
			ctx := context.Background()
			kind := tl.JobKind
			if stage == "state" {
				runOne(t, f, tl.JobKind)
				kind = tl.StateJobKind
			}
			l := claimAnalysis(t, f, kind)
			in, id, ms := computeLoad(t, f, l)
			table, op, condition := "analysis_artifacts", "INSERT", "true"
			switch stage {
			case "load_result":
				table = "analysis_results"
			case "ledger":
				table = "analysis_load_ledger"
			case "publication":
				table = "analysis_publications"
			case "dirty_clean":
				table = "dirty_scopes"
				op = "UPDATE"
				condition = "NEW.clean_generation>OLD.clean_generation"
			case "job_terminal":
				table = "jobs"
				op = "UPDATE"
				condition = "NEW.state='succeeded'"
			}
			if _, e := f.s.Pool.Exec(ctx, `CREATE FUNCTION m2_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF `+condition+` THEN RAISE EXCEPTION 'synthetic_m2_fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER m2_fault BEFORE `+op+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION m2_fault()`); e != nil {
				t.Fatal(e)
			}
			if _, e := f.s.PublishAnalysis(ctx, l, id, in, ms); e == nil {
				t.Fatal("fault published")
			}
			var rows int
			if e := f.s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM analysis_artifacts WHERE run_id=$1)+(SELECT count(*) FROM analysis_load_ledger WHERE run_id=$1)+(SELECT count(*) FROM analysis_results WHERE run_id=$1)+(SELECT count(*) FROM analysis_publications WHERE published_run_id=$1)`, id).Scan(&rows); e != nil || rows != 0 {
				t.Fatal("partial publication", e)
			}
			var state string
			if e := f.s.Pool.QueryRow(ctx, `SELECT status FROM analysis_runs WHERE id=$1`, id).Scan(&state); e != nil || state != "RUNNING" {
				t.Fatal("fault finalized run", e)
			}
			if _, e := f.s.Pool.Exec(ctx, `DROP TRIGGER m2_fault ON `+table); e != nil {
				t.Fatal(e)
			}
			if status, e := f.s.PublishAnalysis(ctx, l, id, in, ms); e != nil || status != "SUCCEEDED" {
				t.Fatal("fault retry", e)
			}
		})
	}
}
func TestLoadBoundedRecomputeMatchesFreshRun(t *testing.T) {
	f, b := loadFixture(t)
	ctx := context.Background()
	runOne(t, f, tl.JobKind)
	runOne(t, f, tl.StateJobKind)
	before, _, e := f.s.CandidateLoadState(ctx, f.p.DatasetID, "2026-11-02")
	if e != nil {
		t.Fatal(e)
	}
	n, e := admin.RecomputeTrainingLoad(ctx, f.s, f.p.DatasetID, "2026-10-31", "2026-10-31", tl.Algorithm+"/"+tl.Version, "rai.hr_reference/1")
	if e != nil || n != 1 {
		t.Fatal(e, n)
	}
	runOne(t, f, wf.JobKind)
	runOne(t, f, tl.JobKind)
	runOne(t, f, tl.StateJobKind)
	after, _, e := f.s.CandidateLoadState(ctx, f.p.DatasetID, "2026-11-02")
	if e != nil || tl.Hash(before) != tl.Hash(after) {
		t.Fatal("incremental != bounded rebuild", e)
	}
	// A future parameter never affects the older workout, then an explicit
	// bounded superseding event changes its lineage and invalidates publication.
	r, _, e := f.s.CandidateLoad(ctx, f.p.DatasetID, b.WorkoutID, "2026-10-31")
	if e != nil {
		t.Fatal(e)
	}
	through := *b.Workout.EndUs + 3600e6
	p := wf.Parameter{ID: syncing.NewID(), EffectiveFrom: *b.Workout.StartUs - 1e6, EffectiveThrough: &through, RestHR: 55, MaxHR: 205, Provenance: "synthetic revised reference", Supersedes: r.HRReference.ID}
	if e = admin.SetFeatureHRReference(ctx, f.s, f.p.DatasetID, p); e != nil {
		t.Fatal(e)
	}
	if _, _, e = f.s.CandidateLoad(ctx, f.p.DatasetID, b.WorkoutID, "2026-10-31"); e == nil {
		t.Fatal("parameter did not invalidate")
	}
	runOne(t, f, wf.JobKind)
	runOne(t, f, tl.JobKind)
	runOne(t, f, tl.StateJobKind)
	revised, _, e := f.s.CandidateLoad(ctx, f.p.DatasetID, b.WorkoutID, "2026-10-31")
	if e != nil || revised.HRReference.ID != p.ID || *revised.Load == *r.Load {
		t.Fatal("parameter versions mixed", e)
	}
}
func TestLoadWorkerCrashBeforePublishRecovery(t *testing.T) {
	f, b := loadFixture(t)
	ctx := context.Background()
	l := claimAnalysis(t, f, tl.JobKind)
	_, _, _ = computeLoad(t, f, l)
	if _, _, e := f.s.CandidateLoad(ctx, f.p.DatasetID, b.WorkoutID, "2026-10-31"); e == nil {
		t.Fatal("compute leaked result")
	}
	if _, e := f.s.Pool.Exec(ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, l.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.RequeueExpired(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Pool.Exec(ctx, `UPDATE jobs SET run_after=clock_timestamp()-interval '1 second' WHERE id=$1`, l.ID); e != nil {
		t.Fatal(e)
	}
	fresh := claimAnalysis(t, f, tl.JobKind)
	jobs.Process(ctx, f.s, fresh, analysisLog)
	if _, _, e := f.s.CandidateLoad(ctx, f.p.DatasetID, b.WorkoutID, "2026-10-31"); e != nil {
		t.Fatal("crash retry did not publish", e)
	}
}
func TestLoadUnknownWorkoutNeverBecomesZero(t *testing.T) {
	f := setup(t)
	b := featureBundle(t, f, 1800)
	b.Series = nil
	b.Samples = b.Samples[1:]
	b.Workout.Workout.Statistics = nil
	phase3Observation(&b)
	importPhase3(t, f, b)
	ctx := context.Background()
	if e := admin.QueueTrainingLoadCandidate(ctx, f.s, f.p.DatasetID, b.WorkoutID, syncing.NewID(), "synthetic no HR"); e != nil {
		t.Fatal(e)
	}
	runOne(t, f, wf.JobKind)
	runOne(t, f, tl.JobKind)
	runOne(t, f, tl.StateJobKind)
	d, _, e := f.s.CandidateLoadState(ctx, f.p.DatasetID, "2026-10-31")
	if e != nil || d.RecordedLoad != nil || d.Acute.Value != nil || d.Chronic.Value != nil || d.UnknownWorkouts != 1 || d.Acute.UnknownWeightMass <= 0 {
		t.Fatal("unknown became zero", e, d)
	}
}
func TestLoadLocalDayAdvanceKeepsUnaffectedHistory(t *testing.T) {
	f, _ := loadFixture(t)
	ctx := context.Background()
	runOne(t, f, tl.JobKind)
	runOne(t, f, tl.StateJobKind)
	before, oldRun, e := f.s.CandidateLoadState(ctx, f.p.DatasetID, "2026-10-31")
	if e != nil {
		t.Fatal(e)
	}
	tomorrow := time.Now().UTC().AddDate(0, 0, 1)
	if e = f.s.AdvanceAnalysisDay(ctx, tomorrow); e != nil {
		t.Fatal(e)
	}
	l := claimAnalysis(t, f, tl.StateJobKind)
	in, e := f.s.LoadAnalysis(ctx, l, tomorrow)
	if e != nil {
		t.Fatal(e)
	}
	id, e := f.s.StartAnalysisRun(ctx, l, in)
	if e != nil {
		t.Fatal(e)
	}
	ms, e := analysis.CalculateCandidateLoad(&in, tl.StateJobKind)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.PublishAnalysis(ctx, l, id, in, ms); e != nil {
		t.Fatal(e)
	}
	after, newRun, e := f.s.CandidateLoadState(ctx, f.p.DatasetID, "2026-10-31")
	if e != nil || newRun != oldRun || tl.Hash(after) != tl.Hash(before) {
		t.Fatal("advance rewrote unrelated history", e)
	}
	if len(in.LoadStateArtifact.Days) != 1 {
		t.Fatal("advance scanned unbounded output range")
	}
}
func TestLoadLateArrivalAndDeleteMatchLedgerRebuild(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	latest := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1).Add(8 * time.Hour)
	first := latest.AddDate(0, 0, -220)
	through := latest.Add(2 * time.Hour).UnixMicro()
	hr := 147.5
	if e := admin.SetFeatureHRReference(ctx, f.s, f.p.DatasetID, wf.Parameter{ID: syncing.NewID(), EffectiveFrom: first.Add(-time.Hour).UnixMicro(), EffectiveThrough: &through, RestHR: 50, MaxHR: 200, Provenance: "synthetic history mutations"}); e != nil {
		t.Fatal(e)
	}
	ids := []string{}
	for i, d := range []int{0, 50, 220} {
		b := simpleLoadBundle(t, f, first.AddDate(0, 0, d), 600, &hr)
		importPhase3(t, f, b)
		ids = append(ids, b.WorkoutID)
		if i == 0 {
			if e := admin.QueueTrainingLoadCandidate(ctx, f.s, f.p.DatasetID, b.WorkoutID, syncing.NewID(), "synthetic mutation"); e != nil {
				t.Fatal(e)
			}
		}
		runOne(t, f, wf.JobKind)
		runOne(t, f, tl.JobKind)
	}
	runOne(t, f, tl.StateJobKind)
	for event := 0; event < 2; event++ {
		if event == 0 {
			b := simpleLoadBundle(t, f, first.AddDate(0, 0, 100), 1200, &hr)
			importPhase3(t, f, b)
		} else {
			if _, e := f.s.DeleteWorkoutBundle(ctx, f.p, bundleDeleteRequest(f, ids[1])); e != nil {
				t.Fatal(e)
			}
		}
		runOne(t, f, wf.JobKind)
		runOne(t, f, tl.JobKind)
		l := claimAnalysis(t, f, tl.StateJobKind)
		in, id, ms := computeLoad(t, f, l)
		if _, e := f.s.PublishAnalysis(ctx, l, id, in, ms); e != nil {
			t.Fatal(e)
		}
		for _, d := range in.LoadStateArtifact.Days {
			date, _ := time.Parse("2006-01-02", d.Day)
			a, c := 0.0, 0.0
			for _, x := range in.LoadStateInput.Loads {
				at, _ := time.Parse("2006-01-02", x.Day)
				k := int(date.Sub(at).Hours() / 24)
				if k >= 0 && k < 180 {
					a += -math.Expm1(-1.0/7) * math.Exp(-float64(k)/7) * *x.Load
					c += -math.Expm1(-1.0/42) * math.Exp(-float64(k)/42) * *x.Load
				}
			}
			if math.Abs(d.Acute.KnownContribution-a) > 1e-10 || math.Abs(d.Chronic.KnownContribution-c) > 1e-10 {
				t.Fatal("incremental state != direct rebuild")
			}
		}
		if event == 1 {
			from := first.AddDate(0, 0, 50).Format("2006-01-02")
			maxDate := first.AddDate(0, 0, 50+186).Format("2006-01-02")
			if in.LoadStateInput.From != from || in.LoadStateInput.Through > maxDate {
				t.Fatal("delete output escaped bounded influence")
			}
		}
	}
}
