//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/admin"
	"rai/server/internal/analysis"
	"rai/server/internal/jobs"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
	wf "rai/server/internal/workoutfeatures"
)

func featureBundle(t *testing.T, f *fixture, seconds int) syncing.WorkoutBundle {
	b, _ := testWorkoutBundle(t, f, 0)
	start := time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)
	b.Workout = workoutAt(start, seconds)
	b.WorkoutID = b.Workout.UUID
	b.Workout.Workout.TotalDistance = &syncing.Quantity{Value: float64(seconds) * 3, Unit: "m"}
	b.Workout.Metadata = map[string]any{"HKIndoorWorkout": false, "HKWeatherTemperature": map[string]any{"value": float64(16), "unit": "degC"}, "HKWeatherHumidity": map[string]any{"value": .5, "unit": "1"}}
	lap, lapAt := int64(3), *b.Workout.StartUs+100e6
	b.Workout.Workout.Events = []syncing.WorkoutEvent{{Type: &lap, StartUs: &lapAt, EndUs: &lapAt}}
	swimming := int64(0)
	location, activity, duration := int64(2), int64(37), int64(seconds)*1e6
	b.Workout.Workout.Activities = []syncing.WorkoutActivity{{UUID: syncing.NewID(), StartUs: b.Workout.StartUs, EndUs: b.Workout.EndUs, DurationUs: &duration, Configuration: &syncing.WorkoutConfiguration{ActivityType: &activity, LocationType: &location, SwimmingLocationType: &swimming}, Metadata: map[string]any{"HKIndoorWorkout": false}}}
	b.Workout.Workout.Statistics = []syncing.WorkoutStatistic{{Type: kind, StartUs: b.Workout.StartUs, EndUs: b.Workout.EndUs, Average: &syncing.Quantity{Value: 190, Unit: "count/min"}}}
	for _, k := range []string{kind, wf.Speed, wf.Steps} {
		v := 150.0
		if k == wf.Speed {
			v = 3
		}
		if k == wf.Steps {
			v = float64(seconds) * 3
		}
		e := bundleSample(b.WorkoutID, start, v)
		e.Type = k
		e.Quantity.Unit = syncing.Units[k]
		e.EndUs = b.Workout.EndUs
		b.Samples = append(b.Samples, e)
	}
	points := []syncing.SeriesPoint{}
	for i := 0; i < seconds; i += 10 {
		points = append(points, syncing.SeriesPoint{StartUs: *b.Workout.StartUs + int64(i)*1e6, EndUs: min(*b.Workout.EndUs, *b.Workout.StartUs+int64(i+10)*1e6), Value: 150, Unit: "count/min"})
	}
	b.Samples[0].Quantity.Value = 190
	b.Series = []syncing.WorkoutBundleSeries{{SampleType: kind, SampleUUID: b.Samples[0].UUID, Points: points}}
	b.Routes[0].Points = nil
	alt, ha, va := 100.0, 3.0, 1.0
	for i := 0; i <= seconds; i++ {
		b.Routes[0].Points = append(b.Routes[0].Points, syncing.WorkoutBundlePoint{RecordedAtUs: *b.Workout.StartUs + int64(i)*1e6, Latitude: 0, Longitude: float64(i) * 3 / 6371008.8 * 180 / math.Pi, Altitude: &alt, HorizontalAccuracy: &ha, VerticalAccuracy: &va})
	}
	phase3Observation(&b)
	for _, k := range []string{kind, wf.Speed, wf.Steps} {
		b.Observation.Coverage["series:"+k] = syncing.BundleCoverage{State: "no_records_observed", SampleCount: 0, QueryCompleted: true, LocalPersisted: true}
	}
	b.Observation.Coverage["series:"+kind] = syncing.BundleCoverage{State: "complete", SampleCount: len(b.Series[0].Points), QueryCompleted: true, LocalPersisted: true}
	if err := b.Validate(); err != nil {
		t.Fatalf("feature fixture contract: %#v", err)
	}
	return b
}
func featureFixture(t *testing.T) (*fixture, syncing.WorkoutBundle) {
	t.Helper()
	f := setup(t)
	b := featureBundle(t, f, 1800)
	importPhase3(t, f, b)
	through := *b.Workout.EndUs + 3600e6
	if err := admin.SetFeatureHRReference(context.Background(), f.s, f.p.DatasetID, wf.Parameter{ID: syncing.NewID(), EffectiveFrom: *b.Workout.StartUs - 3600e6, EffectiveThrough: &through, RestHR: 50, MaxHR: 200, Provenance: "synthetic reference"}); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueueFeatureCandidate(context.Background(), f.s, f.p.DatasetID, b.WorkoutID, syncing.NewID(), "synthetic"); err != nil {
		t.Fatal(err)
	}
	return f, b
}
func computeFeature(t *testing.T, f *fixture, l postgres.Lease) (analysis.Snapshot, string, []analysis.Metric) {
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
	if in.FeatureInput == nil {
		t.Fatal("missing typed input")
	}
	v, err := wf.ComputeNormalized(ctx, *in.FeatureInput)
	if err != nil {
		t.Fatal(err)
	}
	in.FeatureArtifact = &v
	ms := []analysis.Metric{}
	if v.State != wf.Deleted {
		ms = append(ms, analysis.Metric{Day: analysis.WorkoutDay, Name: "feature_active_seconds", Algorithm: wf.Algorithm, Version: wf.Version, Value: v.Timing.Active, Unit: "s", Quality: "partial", Details: map[string]any{}})
	}
	return in, id, ms
}
func TestFeatureWorkerSnapshotArtifactAndLegacyIsolation(t *testing.T) {
	f, b := featureFixture(t)
	ctx := context.Background()
	runOne(t, f, "analysis.workout")
	old, err := f.s.CurrentWorkoutAnalysis(ctx, f.p, b.WorkoutID)
	if err != nil {
		t.Fatal(err)
	}
	before := businessSnapshot(t, f)
	featureLease := claimAnalysis(t, f, wf.JobKind)
	input, e := f.s.LoadAnalysis(ctx, featureLease, time.Now().UTC())
	if e != nil {
		t.Fatal(e)
	}
	if input.FeatureInput == nil || len(input.FeatureInput.Events) != 1 || len(input.FeatureInput.Activities) != 1 || len(input.FeatureInput.Statistics) != 1 || len(input.FeatureInput.Provenance) == 0 {
		t.Fatal("incomplete snapshot")
	}
	jobs.Process(ctx, f.s, featureLease, analysisLog)
	v, run, err := f.s.CandidateFeature(ctx, f.p.DatasetID, b.WorkoutID)
	if err != nil {
		t.Fatal(err)
	}
	if v.HeartRate.Mean == nil || *v.HeartRate.Mean != 150 || v.Qualification.Load.Status != "eligible" || v.Qualification.VO2.Status == "ineligible" {
		t.Fatal("series or qualification", v.Qualification)
	}
	if v.Evidence.FactCount < 5 || len(v.Evidence.ParameterIDs) != 1 {
		t.Fatal("missing lineage")
	}
	after := businessSnapshot(t, f)
	for table, data := range before {
		if table == "jobs" || table == "dirty_scopes" {
			continue
		}
		if !bytes.Equal(data, after[table]) {
			t.Fatal("feature changed raw or receipt", table)
		}
	}
	legacy, err := f.s.CurrentWorkoutAnalysis(ctx, f.p, b.WorkoutID)
	if err != nil || legacy.RunID != old.RunID {
		t.Fatal("candidate overwrote active", err)
	}
	var factRefs []byte
	var hash string
	if err = f.s.Pool.QueryRow(ctx, `SELECT lineage->'factRefs',encode(input_hash,'hex') FROM analysis_runs WHERE dataset_id=$1 AND id=$2`, f.p.DatasetID, run).Scan(&factRefs, &hash); err != nil || len(factRefs) == 0 || hash != v.Evidence.InputHash {
		t.Fatal("run lineage mismatch", err)
	}
	for _, sql := range []string{`UPDATE analysis_artifacts SET schema_version='invalid'`, `DELETE FROM analysis_artifacts`, `UPDATE athlete_model_inputs SET provenance='changed'`, `DELETE FROM athlete_model_inputs`, `UPDATE analysis_model_channels SET stack_id='` + wf.StackID() + `' WHERE channel='active'`, `INSERT INTO analysis_artifacts SELECT dataset_id,run_id,'workout_features',schema_version,content_sha256,payload FROM analysis_artifacts`} {
		if _, err = f.s.Pool.Exec(ctx, sql); err == nil {
			t.Fatal("immutability/promotion guard failed")
		}
	}
	var downstream, jobsN int
	if err = f.s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM dirty_scopes WHERE dataset_id=$1 AND scope_key LIKE 'model/%/athlete/%'),(SELECT count(*) FROM jobs WHERE dataset_id=$1 AND kind IN ('analysis.vo2','analysis.ability','analysis.load'))`, f.p.DatasetID).Scan(&downstream, &jobsN); err != nil || downstream != 5 || jobsN != 0 {
		t.Fatal("fake downstream handler", err)
	}
}
func TestFeatureImmediateDeletionInvalidatesBeforeWorker(t *testing.T) {
	for _, child := range []string{"quantity", "series_parent", "route", "workout"} {
		t.Run(child, func(t *testing.T) {
			f, b := featureFixture(t)
			ctx := context.Background()
			runOne(t, f, wf.JobKind)
			_, oldRun, err := f.s.CandidateFeature(ctx, f.p.DatasetID, b.WorkoutID)
			if err != nil {
				t.Fatal(err)
			}
			switch child {
			case "quantity":
				f.commit(t, []syncing.Event{{Op: "delete", Kind: "sample", Type: kind, UUID: b.Samples[0].UUID}}, 1, nil)
			case "series_parent":
				f.commit(t, []syncing.Event{{Op: "delete", Kind: "sample", Type: kind, UUID: b.Samples[0].UUID}}, 1, nil)
			case "route":
				_, err = f.s.DeleteRoute(ctx, f.p, b.Routes[0].RouteID, syncing.RouteDeleteRequest{ProtocolVersion: 2, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch, Reason: "healthkit_deleted_object"})
			case "workout":
				_, err = f.s.DeleteWorkoutBundle(ctx, f.p, bundleDeleteRequest(f, b.WorkoutID))
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = f.s.CandidateFeature(ctx, f.p.DatasetID, b.WorkoutID); err == nil {
				t.Fatal("deleted evidence still current")
			}
			var pointers, artifacts int
			if err = f.s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM analysis_publications WHERE dataset_id=$1 AND scope_key=$2),(SELECT count(*) FROM analysis_artifacts WHERE dataset_id=$1 AND run_id=$3)`, f.p.DatasetID, wf.Scope(b.WorkoutID), oldRun).Scan(&pointers, &artifacts); err != nil || pointers != 0 || artifacts != 1 {
				t.Fatal("reference not invalidated or history erased", err)
			}
			runOne(t, f, wf.JobKind)
			if child == "workout" {
				if _, _, err = f.s.CandidateFeature(ctx, f.p.DatasetID, b.WorkoutID); err == nil {
					t.Fatal("deleted workout republished")
				}
			} else {
				v, _, err := f.s.CandidateFeature(ctx, f.p.DatasetID, b.WorkoutID)
				if err != nil {
					t.Fatal(err)
				}
				if child != "route" && v.HeartRate.ReconstructedCoverage != 0 {
					t.Fatal("deleted child recovered from old import")
				}
			}
		})
	}
}
func TestFeatureGenerationAndLeaseFencing(t *testing.T) {
	f, b := featureFixture(t)
	ctx := context.Background()
	l := claimAnalysis(t, f, wf.JobKind)
	in, id, ms := computeFeature(t, f, l)
	if _, err := f.s.EnrichWorkout(ctx, f.p, syncing.WorkoutEnrichmentRequest{ProtocolVersion: 2, DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch, EnrichmentID: syncing.NewID(), WorkoutID: b.WorkoutID, Source: &syncing.Source{BundleID: "synthetic"}, SourceVersion: 1, ObservedAtUs: *b.Workout.StartUs, Metadata: map[string]any{"synthetic": true}}); err != nil {
		t.Fatal(err)
	}
	status, err := f.s.PublishAnalysis(ctx, l, id, in, ms)
	if err != nil || status != "SUPERSEDED" || count(t, f, "analysis_artifacts") != 0 {
		t.Fatal("old generation published", status, err)
	}
	newer := claimAnalysis(t, f, wf.JobKind)
	in, id, ms = computeFeature(t, f, newer)
	if _, err = f.s.Pool.Exec(ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, newer.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.RequeueExpired(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(ctx, `UPDATE jobs SET run_after=now() WHERE id=$1`, newer.ID); err != nil {
		t.Fatal(err)
	}
	recovered := claimAnalysis(t, f, wf.JobKind)
	_, err = f.s.PublishAnalysis(ctx, newer, id, in, ms)
	requireCode(t, err, "lease_lost")
	jobs.Process(ctx, f.s, recovered, analysisLog)
	if _, _, err = f.s.CandidateFeature(ctx, f.p.DatasetID, b.WorkoutID); err != nil {
		t.Fatal(err)
	}
	var clean bool
	if err = f.s.Pool.QueryRow(ctx, `SELECT generation=clean_generation FROM dirty_scopes WHERE dataset_id=$1 AND scope_key=$2`, f.p.DatasetID, wf.Scope(b.WorkoutID)).Scan(&clean); err != nil || !clean {
		t.Fatal("current dirty not clean", err)
	}
}
func TestFeatureAtomicPublicationFaults(t *testing.T) {
	for _, table := range []string{"analysis_artifacts", "analysis_results", "analysis_publications", "analysis_runs", "dirty_scopes", "jobs", "job_attempts"} {
		t.Run(table, func(t *testing.T) {
			f, _ := featureFixture(t)
			ctx := context.Background()
			l := claimAnalysis(t, f, wf.JobKind)
			in, id, ms := computeFeature(t, f, l)
			op := "INSERT"
			if table == "analysis_runs" || table == "dirty_scopes" || table == "jobs" || table == "job_attempts" {
				op = "UPDATE"
			}
			sql := `CREATE FUNCTION m1_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic'; END $$; CREATE TRIGGER m1_fail BEFORE ` + op + ` ON ` + table + ` FOR EACH ROW EXECUTE FUNCTION m1_fail()`
			if _, err := f.s.Pool.Exec(ctx, sql); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.PublishAnalysis(ctx, l, id, in, ms); err == nil {
				t.Fatal("fault succeeded")
			}
			if count(t, f, "analysis_artifacts") != 0 || count(t, f, "analysis_results") != 0 || count(t, f, "analysis_publications") != 0 {
				t.Fatal("partial publication")
			}
			var status, state string
			if err := f.s.Pool.QueryRow(ctx, `SELECT a.status,j.state FROM analysis_runs a JOIN jobs j ON j.id=a.job_id WHERE a.id=$1`, id).Scan(&status, &state); err != nil || status != "RUNNING" || state != "running" {
				t.Fatal("terminal escaped rollback", err)
			}
			if _, err := f.s.Pool.Exec(ctx, `DROP TRIGGER m1_fail ON `+table); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.PublishAnalysis(ctx, l, id, in, ms); err != nil {
				t.Fatal("retry failed", err)
			}
		})
	}
}
func TestFeatureSnapshotDoesNotMixGeneration(t *testing.T) {
	f, b := featureFixture(t)
	ctx := context.Background()
	l := claimAnalysis(t, f, wf.JobKind)
	block, err := f.s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer block.Rollback(ctx)
	if _, err = block.Exec(ctx, `LOCK TABLE workout_imports IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	type result struct {
		in  analysis.Snapshot
		err error
	}
	done := make(chan result, 1)
	go func() { in, e := f.s.LoadAnalysis(ctx, l, time.Now().UTC()); done <- result{in, e} }()
	deadline := time.Now().Add(400 * time.Millisecond)
	waiting := false
	for time.Now().Before(deadline) {
		if err = f.s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'SELECT bundle_sha256,%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !waiting {
		t.Fatal("loader did not reach stable snapshot barrier")
	}
	if _, err = f.s.EnrichWorkout(ctx, f.p, syncing.WorkoutEnrichmentRequest{ProtocolVersion: 2, DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch, EnrichmentID: syncing.NewID(), WorkoutID: b.WorkoutID, Source: &syncing.Source{BundleID: "synthetic"}, SourceVersion: 1, ObservedAtUs: *b.Workout.StartUs, Metadata: map[string]any{"newGenerationOnly": true}}); err != nil {
		t.Fatal(err)
	}
	if err = block.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	r := <-done
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.in.Generation != l.TargetGeneration || bytes.Contains(r.in.FeatureInput.Metadata, []byte("newGenerationOnly")) {
		t.Fatal("mixed input generation")
	}
}
func TestFeatureCandidateOperationRetryAndParameterInvalidation(t *testing.T) {
	f, b := featureFixture(t)
	ctx := context.Background()
	operation := syncing.NewID()
	if err := admin.QueueFeatureCandidate(ctx, f.s, f.p.DatasetID, b.WorkoutID, operation, "synthetic"); err != nil {
		t.Fatal(err)
	}
	before := count(t, f, "jobs")
	if err := admin.QueueFeatureCandidate(ctx, f.s, f.p.DatasetID, b.WorkoutID, operation, "synthetic"); err != nil || count(t, f, "jobs") != before {
		t.Fatal("operation retry changed generation", err)
	}
	for {
		l, err := f.s.Claim(ctx, []string{wf.JobKind})
		if err != nil {
			break
		}
		jobs.Process(ctx, f.s, l, analysisLog)
	}
	if _, _, err := f.s.CandidateFeature(ctx, f.p.DatasetID, b.WorkoutID); err != nil {
		t.Fatal(err)
	}
	end := *b.Workout.EndUs + 3600e6
	if err := admin.SetFeatureHRReference(ctx, f.s, f.p.DatasetID, wf.Parameter{ID: syncing.NewID(), EffectiveFrom: *b.Workout.StartUs - 1, EffectiveThrough: &end, RestHR: 55, MaxHR: 195, Provenance: "synthetic conflicting reference"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.CandidateFeature(ctx, f.p.DatasetID, b.WorkoutID); err == nil {
		t.Fatal("parameter changed but artifact remained current")
	}
	runOne(t, f, wf.JobKind)
	v, _, err := f.s.CandidateFeature(ctx, f.p.DatasetID, b.WorkoutID)
	if err != nil || v.Qualification.Load.Status != "ineligible" {
		t.Fatal("conflict not preserved", err)
	}
}
func TestFeatureRuntimeRoleBoundary(t *testing.T) {
	f, b := featureFixture(t)
	ctx := context.Background()
	suffix := strings.ReplaceAll(syncing.NewID(), "-", "")
	apiRole, workerRole := "rai_api_"+suffix, "rai_worker_"+suffix
	raw, err := os.ReadFile("../../../deploy/postgres/roles.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ReplaceAll(strings.ReplaceAll(string(raw), "rai_api", apiRole), "rai_worker", workerRole)
	if _, err = f.s.Pool.Exec(ctx, sql); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, e := f.s.Pool.Exec(context.Background(), "DROP OWNED BY "+pgx.Identifier{apiRole}.Sanitize()+", "+pgx.Identifier{workerRole}.Sanitize()+"; DROP ROLE "+pgx.Identifier{apiRole}.Sanitize()+", "+pgx.Identifier{workerRole}.Sanitize()); e != nil {
			t.Error(e)
		}
	})
	open := func(role string) *postgres.Store {
		dsn := f.s.Pool.Config().ConnString()
		sep := "&"
		if !strings.Contains(dsn, "?") {
			sep = "?"
		}
		s, e := postgres.Open(ctx, dsn+sep+"options=-crole%3D"+role, 2)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(s.Pool.Close)
		return s
	}
	worker, api := open(workerRole), open(apiRole)
	if err = worker.RuntimeRoleSafe(ctx); err != nil {
		t.Fatal(err)
	}
	if err = api.RuntimeRoleSafe(ctx); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{`SELECT * FROM analysis_artifacts`, `INSERT INTO athlete_model_inputs(dataset_id,id) VALUES(NULL,NULL)`, `UPDATE analysis_model_channels SET stack_id='legacy'`, `DELETE FROM analysis_publications`} {
		if _, err = api.Pool.Exec(ctx, sql); err == nil {
			t.Fatal("API candidate/admin privilege")
		}
	}
	for _, sql := range []string{`UPDATE health_samples SET value_numeric=0`, `UPDATE workouts SET payload='{}'`, `UPDATE athlete_model_inputs SET provenance='x'`, `UPDATE analysis_model_channels SET assignment_version=2`, `UPDATE analysis_artifacts SET payload='{}'`} {
		if _, err = worker.Pool.Exec(ctx, sql); err == nil {
			t.Fatal("worker mutation privilege")
		}
	}
	l, e := worker.Claim(ctx, []string{wf.JobKind})
	if e != nil {
		t.Fatal(e)
	}
	jobs.Process(ctx, worker, l, analysisLog)
	if _, _, err = f.s.CandidateFeature(ctx, f.p.DatasetID, b.WorkoutID); err != nil {
		t.Fatal("restricted worker failed", err)
	}
	// An API-role fact deletion invokes only the reviewed definer invalidator.
	original := f.s
	f.s = api
	f.commit(t, []syncing.Event{{Op: "delete", Kind: "sample", Type: kind, UUID: b.Samples[0].UUID}}, 1, nil)
	f.s = original
	if _, _, err = f.s.CandidateFeature(ctx, f.p.DatasetID, b.WorkoutID); err == nil {
		t.Fatal("restricted API delete left feature live")
	}
}

func TestFeatureLateStagedSeriesAndRouteDirtyCandidate(t *testing.T) {
	for _, child := range []string{"series", "route"} {
		t.Run(child, func(t *testing.T) {
			f, b := featureFixture(t)
			ctx := context.Background()
			runOne(t, f, wf.JobKind)
			if child == "series" {
				q := b.Samples[1]
				sid := syncing.NewID()
				if _, err := f.s.RegisterSeries(ctx, f.p, syncing.SeriesRequest{DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch, SeriesID: sid, SampleType: q.Type, SampleUUID: q.UUID}); err != nil {
					t.Fatal(err)
				}
				if _, _, err := f.s.CandidateFeature(ctx, f.p.DatasetID, b.WorkoutID); err == nil {
					t.Fatal("staging did not dirty candidate")
				}
				l := claimAnalysis(t, f, wf.JobKind)
				in, err := f.s.LoadAnalysis(ctx, l, time.Now().UTC())
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, x := range in.FeatureInput.Quantities {
					if x.Fact.ID == q.UUID {
						found = x.Support == wf.CondensedParent
					}
				}
				if !found {
					t.Fatal("pending parent was a trace")
				}
				jobs.Process(ctx, f.s, l, analysisLog)
				// Real late sealed speed series must replace its parent and dirty again.
				receipt, _ := commitSeries(t, f, sid, 0, []syncing.SeriesPoint{{StartUs: *q.StartUs, EndUs: *q.EndUs, Value: 3, Unit: "m/s"}})
				if _, err = f.s.SealSeries(ctx, f.p, sid, syncing.SeriesSeal{ChunkCount: 1, TotalPoints: 1, ManifestSHA256: receipt.ManifestSHA256}); err != nil {
					t.Fatal(err)
				}
				if _, _, err = f.s.CandidateFeature(ctx, f.p.DatasetID, b.WorkoutID); err == nil {
					t.Fatal("series seal left candidate current")
				}
				runOne(t, f, wf.JobKind)
				v, _, err := f.s.CandidateFeature(ctx, f.p.DatasetID, b.WorkoutID)
				if err != nil || v.Speed.Mean == nil || *v.Speed.Mean != 3 || v.Speed.ReconstructedCoverage != 1 {
					t.Fatal("late series not used", err)
				}
			} else {
				route := syncing.NewID()
				start := time.UnixMicro(*b.Workout.StartUs)
				registerTestRoute(t, f, b.WorkoutID, route, start, start.Add(9*time.Second), 1, 10)
				if _, _, err := f.s.CandidateFeature(ctx, f.p.DatasetID, b.WorkoutID); err == nil {
					t.Fatal("route registration left candidate current")
				}
				runOne(t, f, wf.JobKind)
				uploadTestRouteChunk(t, f, route, 0, testRoutePoints(start, 10, 0))
				manifest, payload := routeStateChains(t, f, route)
				sealTestRoute(t, f, route, 1, 10, manifest, payload)
				if _, _, err := f.s.CandidateFeature(ctx, f.p.DatasetID, b.WorkoutID); err == nil {
					t.Fatal("route seal left candidate current")
				}
				runOne(t, f, wf.JobKind)
			}
		})
	}
}

func TestFeatureLoaderLimitsFailWithoutTruncatingOrPublishing(t *testing.T) {
	f, b := featureFixture(t)
	ctx := context.Background()
	runOne(t, f, wf.JobKind)
	// Bounded per-workout observation history, all synthetic. Identities only are
	// generated; no real source data or existing raw fact is changed.
	// Exercise the observation-count limit with legal metadata-only imports.
	// Repeating full sample/route provenance and coverage 2001 times needlessly
	// made the Worker assertion depend on decoding load and the normal timeout.
	metadataOnly := b
	observation := *b.Observation
	observation.Provenance.Samples = nil
	observation.Provenance.Routes = nil
	observation.Coverage = map[string]syncing.BundleCoverage{}
	metadataOnly.Samples, metadataOnly.Series, metadataOnly.Routes = nil, nil, nil
	metadataOnly.Observation = &observation
	if err := metadataOnly.Validate(); err != nil {
		t.Fatal("invalid synthetic metadata-only observation", err)
	}
	raw, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(ctx, `INSERT INTO workout_imports(dataset_id,workout_uuid,bundle_sha256,payload,receipt) SELECT dataset_id,workout_uuid,decode(md5(g::text)||md5(g::text),'hex'),jsonb_build_object('observation',$3::jsonb),receipt FROM workout_imports CROSS JOIN generate_series(1,2001) g WHERE dataset_id=$1 AND workout_uuid=$2 LIMIT 2001`, f.p.DatasetID, b.WorkoutID, raw); err != nil {
		t.Fatal(err)
	}
	if count(t, f, "workout_imports") != 2002 {
		t.Fatal("synthetic observation-count boundary was not seeded")
	}
	if err := admin.QueueFeatureCandidate(ctx, f.s, f.p.DatasetID, b.WorkoutID, syncing.NewID(), "synthetic limit"); err != nil {
		t.Fatal(err)
	}
	l := claimAnalysis(t, f, wf.JobKind)
	in, err := f.s.LoadAnalysis(ctx, l, time.Now().UTC())
	code, _ := analysis.ErrorCode(err)
	if code != "analysis_input_limit" || in.FeatureArtifact != nil {
		t.Fatal("loader truncated input", code)
	}
	jobs.Process(ctx, f.s, l, analysisLog)
	if _, _, err = f.s.CandidateFeature(ctx, f.p.DatasetID, b.WorkoutID); err == nil {
		t.Fatal("failed limited run published")
	}
	var state, lastCode string
	if err = f.s.Pool.QueryRow(ctx, `SELECT state,COALESCE(last_error_code,'') FROM jobs WHERE id=$1`, l.ID).Scan(&state, &lastCode); err != nil || state != "failed" || lastCode != "analysis_input_limit" {
		t.Fatalf("input limit not durably failed: state=%s code=%s error=%v", state, lastCode, err)
	}
}

func TestFeatureFailedSeriesParentCannotFakeCoverage(t *testing.T) {
	f := setup(t)
	b := featureBundle(t, f, 1800)
	b.Series = nil
	b.Observation.Coverage["series:"+kind] = syncing.BundleCoverage{State: "failed", SampleCount: 1, ErrorCode: "synthetic_query_failure"}
	importPhase3(t, f, b)
	ctx := context.Background()
	if err := admin.QueueFeatureCandidate(ctx, f.s, f.p.DatasetID, b.WorkoutID, syncing.NewID(), "synthetic"); err != nil {
		t.Fatal(err)
	}
	runOne(t, f, wf.JobKind)
	v, _, err := f.s.CandidateFeature(ctx, f.p.DatasetID, b.WorkoutID)
	if err != nil {
		t.Fatal(err)
	}
	if v.HeartRate.State != wf.Condensed || v.HeartRate.Mean != nil || v.HeartRate.ReconstructedCoverage != 0 {
		t.Fatal("condensed parent faked HR trace")
	}
}
