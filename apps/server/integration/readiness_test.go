//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"math"
	"math/rand"
	"rai/server/internal/admin"
	"rai/server/internal/analysis"
	"rai/server/internal/jobs"
	rd "rai/server/internal/readiness"
	rc "rai/server/internal/recovery"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
	"rai/server/internal/training"
	tl "rai/server/internal/trainingload"
	wf "rai/server/internal/workoutfeatures"
	"testing"
	"time"
)

func readinessFixture(t *testing.T) (*fixture, []syncing.WorkoutBundle, map[string]syncing.DailyBatch) {
	t.Helper()
	ctx := context.Background()
	f, bs, _, _ := recoveryFixture(t, 28)
	var tz string
	if e := f.s.Pool.QueryRow(ctx, `SELECT timezone FROM datasets WHERE id=$1`, f.p.DatasetID).Scan(&tz); e != nil {
		t.Fatal(e)
	}
	loc, e := time.LoadLocation(tz)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().In(loc)
	event := now.Add(-2 * time.Hour)
	if event.Format("2006-01-02") != now.Format("2006-01-02") {
		t.Fatal("synthetic current-day fixture requires >=02:00 local; no fabricated fresh evidence")
	}
	for _, kind := range []string{rc.HRV, rc.RHR, rc.Sleep} {
		b := nextDaily(bs[kind])
		x := bs[kind].Facts[len(bs[kind].Facts)-1]
		x.UUID = syncing.NewID()
		x.EndUs = event.UnixMicro()
		x.StartUs = x.EndUs
		if kind == rc.Sleep {
			x.StartUs -= 8 * rc.Hour
		}
		b.Facts = []syncing.DailyFact{x}
		requireDaily(t, f, b)
		bs[kind] = b
	}
	if e = admin.QueueRecoveryCandidate(ctx, f.s, f.p.DatasetID, now.Format("2006-01-02"), syncing.NewID(), "synthetic M6 Recovery"); e != nil {
		t.Fatal(e)
	}
	runRecoveryLatest(t, f)
	if r, _, e := f.s.CandidateRecovery(ctx, f.p.DatasetID, now.Format("2006-01-02"), time.Now()); e != nil || r.Score == nil {
		t.Fatal("full current M5B required", e, r.Reasons)
	}
	// One genuine M2 parameter epoch, valid over the entire bounded synthetic history.
	through := now.AddDate(0, 0, 1).UnixMicro()
	if e = admin.SetFeatureHRReference(ctx, f.s, f.p.DatasetID, wf.Parameter{ID: syncing.NewID(), EffectiveFrom: now.AddDate(0, 0, -365).UnixMicro(), EffectiveThrough: &through, RestHR: 50, MaxHR: 200, Provenance: "synthetic M6"}); e != nil {
		t.Fatal(e)
	}
	workouts := []syncing.WorkoutBundle{}
	for _, back := range []int{36, 30, 24, 18, 12, 6} {
		b := m3Bundle(t, f, now.AddDate(0, 0, -back), 300)
		importPhase3(t, f, b)
		if e = admin.QueueTrainingLoadCandidate(ctx, f.s, f.p.DatasetID, b.WorkoutID, syncing.NewID(), "synthetic M6 load"); e != nil {
			t.Fatal(e)
		}
		workouts = append(workouts, b)
	}
	drainReadinessUpstream(t, f, wf.JobKind)
	drainReadinessUpstream(t, f, tl.JobKind)
	if e = admin.QueueReadinessCandidate(ctx, f.s, f.p.DatasetID, syncing.NewID(), "synthetic M6 candidate"); e != nil {
		t.Fatal(e)
	}
	readinessSyntheticCoverage(t, f)
	return f, workouts, bs
}
func readinessSyntheticCoverage(t *testing.T, f *fixture) {
	t.Helper()
	ctx := context.Background()
	var manifest []byte
	var now time.Time
	if e := f.s.Pool.QueryRow(ctx, `SELECT clock_timestamp(),COALESCE((SELECT jsonb_agg(jsonb_build_array(s.id,s.query_fingerprint,s.last_client_seq,s.last_batch_id) ORDER BY s.id) FROM sync_streams s JOIN collector_leases c USING(dataset_id,collector_epoch) WHERE s.dataset_id=$1 AND s.sample_type='HKWorkoutTypeIdentifier'),'[]'::jsonb)`, f.p.DatasetID).Scan(&now, &manifest); e != nil {
		t.Fatal(e)
	}
	var streams any
	_ = json.Unmarshal(manifest, &streams)
	c := rd.Coverage{Identity: syncing.NewID(), From: now.AddDate(0, 0, -200).UnixMicro(), Through: now.UnixMicro(), CaughtUp: true, Synthetic: true, StreamHash: rd.Hash(streams)}
	b, _ := json.Marshal(c)
	if _, e := f.s.Pool.Exec(ctx, `UPDATE analysis_model_channels SET extension_configs=jsonb_set(extension_configs,ARRAY[$2,'syntheticCoverage'],$3::jsonb) WHERE dataset_id=$1 AND channel='candidate'`, f.p.DatasetID, rd.Model, b); e != nil {
		t.Fatal(e)
	}
}
func runReadinessLatest(t *testing.T, f *fixture) {
	t.Helper()
	for i := 0; i < 300; i++ {
		l, e := f.s.Claim(context.Background(), []string{rd.JobKind})
		if errors.Is(e, pgx.ErrNoRows) {
			return
		}
		if e != nil {
			t.Fatal(e)
		}
		jobs.Process(context.Background(), f.s, l, analysisLog)
		var state, code string
		if e = f.s.Pool.QueryRow(context.Background(), `SELECT state,COALESCE(last_error_code,'') FROM jobs WHERE id=$1`, l.ID).Scan(&state, &code); e != nil || state != "succeeded" {
			t.Fatal("M6 worker", state, code, e)
		}
	}
	t.Fatal("unbounded readiness jobs")
}
func computeReadiness(t *testing.T, f *fixture, l postgres.Lease) (analysis.Snapshot, string, []analysis.Metric) {
	t.Helper()
	ctx := context.Background()
	in, e := f.s.LoadAnalysis(ctx, l, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	id, e := f.s.StartAnalysisRun(ctx, l, in)
	if e != nil {
		t.Fatal(e)
	}
	ms, e := analysis.CalculateCandidateReadiness(&in)
	if e != nil {
		t.Fatal(e)
	}
	return in, id, ms
}
func latestReadinessLease(t *testing.T, f *fixture) postgres.Lease {
	t.Helper()
	l := claimAnalysis(t, f, rd.JobKind)
	for i := 0; i < 300; i++ {
		var g int64
		if e := f.s.Pool.QueryRow(context.Background(), `SELECT generation FROM dirty_scopes WHERE dataset_id=$1 AND scope_key=$2`, l.DatasetID, l.ScopeKey).Scan(&g); e != nil {
			t.Fatal(e)
		}
		if l.TargetGeneration == g {
			return l
		}
		jobs.Process(context.Background(), f.s, l, analysisLog)
		l = claimAnalysis(t, f, rd.JobKind)
	}
	t.Fatal("no latest generation")
	return l
}
func readReadiness(t *testing.T, f *fixture) (rd.Result, string) {
	t.Helper()
	r, id, e := f.s.CandidateReadiness(context.Background(), f.p.DatasetID, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	return r, id
}
func TestReadinessPublication(t *testing.T) {
	f, _, _ := readinessFixture(t)
	ctx := context.Background()
	runReadinessLatest(t, f)
	r, id := readReadiness(t, f)
	if r.Score == nil || r.Status != "available" || *r.Score > *r.RecoveryScore || r.Confidence > .55 || r.Confidence > r.Recovery.Confidence || !r.Coverage.Synthetic || r.Baseline.TrainingDays != 6 {
		t.Fatal("readiness unavailable", r.Status, r.Reasons)
	}
	if math.Abs(*r.Score-*r.RecoveryScore*math.Pow(.5, *r.Burden / *r.Baseline.Value)) > 1e-9 {
		t.Fatal("formula")
	}
	var n int
	if e := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM analysis_artifacts WHERE run_id=$1`, id).Scan(&n); e != nil || n != 1 {
		t.Fatal("artifact", e)
	}
	var before, after []byte
	if e := f.s.Pool.QueryRow(ctx, `SELECT payload FROM analysis_artifacts WHERE run_id=$1`, id).Scan(&before); e != nil {
		t.Fatal(e)
	}
	later, _, e := f.s.CandidateReadiness(ctx, f.p.DatasetID, time.UnixMicro(r.AsOf).Add(30*time.Minute))
	if e != nil || later.Score == nil || *later.Score < *r.Score {
		t.Fatal("freshen", e)
	}
	if e = f.s.Pool.QueryRow(ctx, `SELECT payload FROM analysis_artifacts WHERE run_id=$1`, id).Scan(&after); e != nil || !bytes.Equal(before, after) {
		t.Fatal("freshen mutated artifact", e)
	}
	var output bytes.Buffer
	if e = admin.ReadinessCandidateCommand(ctx, f.s.Pool.Config().ConnString(), []string{"inspect", "--dataset", f.p.DatasetID}, &output); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{f.p.DatasetID, id, `"score"`, `"load"`, `"recoveryScore"`} {
		if bytes.Contains(output.Bytes(), []byte(bad)) {
			t.Fatal("inspect privacy")
		}
	}
	// Product readers and generator remain on their legacy contracts.
	values := []any{}
	home, e := f.s.ProductOverview(ctx, f.p)
	if e != nil {
		t.Fatal(e)
	}
	values = append(values, home)
	history, e := f.s.ProductHistory(ctx, f.p, 14)
	if e != nil {
		t.Fatal(e)
	}
	values = append(values, history)
	ws, e := f.s.ProductWorkouts(ctx, f.p, "", 10, "")
	if e != nil {
		t.Fatal(e)
	}
	values = append(values, ws)
	plan, e := f.s.CreateTrainingPlan(ctx, f.p, training.CreateRequest{CommandID: syncing.NewID(), PlanID: syncing.NewID(), Config: trainingConfig()})
	if e != nil {
		t.Fatal(e)
	}
	saved, e := f.s.TrainingPlan(ctx, f.p, plan.ID)
	if e != nil {
		t.Fatal(e)
	}
	values = append(values, saved)
	if state, e := f.s.CurrentTrainingState(ctx, f.p); e == nil {
		values = append(values, state)
	}
	for _, v := range values {
		b, _ := json.Marshal(v)
		for _, bad := range []string{rd.Model, rd.Family, rd.Key} {
			if bytes.Contains(b, []byte(bad)) {
				t.Fatal("candidate leaked")
			}
		}
	}
}
func TestReadinessMissingCoverageAndUnknownLoad(t *testing.T) {
	f, _, _ := readinessFixture(t)
	ctx := context.Background()
	if _, e := f.s.Pool.Exec(ctx, `UPDATE analysis_model_channels SET extension_configs=extension_configs#-ARRAY[$2,'syntheticCoverage'] WHERE dataset_id=$1 AND channel='candidate'`, f.p.DatasetID, rd.Model); e != nil {
		t.Fatal(e)
	}
	runReadinessLatest(t, f)
	r, _ := readReadiness(t, f)
	if r.Score != nil || r.Status != "sync_incomplete" {
		t.Fatal("no upload became rest")
	}
	b := m3Bundle(t, f, time.Now().Add(-time.Hour), 300)
	importPhase3(t, f, b)
	readinessSyntheticCoverage(t, f)
	runReadinessLatest(t, f)
	r, _ = readReadiness(t, f)
	if r.Score != nil || r.UnknownLoads != 1 {
		t.Fatal("pending load became zero", r.Reasons)
	}
}
func TestReadinessPublicationFences(t *testing.T) {
	for _, change := range []string{"new_workout", "delete_workout", "load_revision", "recovery_withdraw", "recovery_revision", "coverage", "config", "assignment", "timezone", "collector", "device", "m2_assignment", "lease", "takeover"} {
		t.Run(change, func(t *testing.T) {
			f, ws, bs := readinessFixture(t)
			ctx := context.Background()
			l := latestReadinessLease(t, f)
			in, id, ms := computeReadiness(t, f, l)
			switch change {
			case "new_workout":
				b := m3Bundle(t, f, time.Now().Add(-time.Hour), 300)
				importPhase3(t, f, b)
			case "delete_workout":
				if _, e := f.s.DeleteWorkoutBundle(ctx, f.p, bundleDeleteRequest(f, ws[5].WorkoutID)); e != nil {
					t.Fatal(e)
				}
			case "load_revision":
				if e := admin.QueueTrainingLoadCandidate(ctx, f.s, f.p.DatasetID, ws[5].WorkoutID, syncing.NewID(), "synthetic M2 revision"); e != nil {
					t.Fatal(e)
				}
			case "recovery_withdraw":
				if _, e := f.s.Pool.Exec(ctx, `DELETE FROM analysis_publications WHERE dataset_id=$1 AND metric_family=$2`, f.p.DatasetID, rc.Family); e != nil {
					t.Fatal(e)
				}
			case "recovery_revision":
				b := nextDaily(bs[rc.RHR])
				x := bs[rc.RHR].Facts[0]
				b.DeletedUUIDs = []string{x.UUID}
				x.UUID = syncing.NewID()
				x.Quantity = &syncing.Quantity{Value: 75, Unit: "count/min"}
				b.Facts = []syncing.DailyFact{x}
				requireDaily(t, f, b)
				runRecoveryLatest(t, f)
			case "coverage":
				readinessSyntheticCoverage(t, f)
			case "config":
				if _, e := f.s.Pool.Exec(ctx, `UPDATE analysis_model_channels SET extension_configs=jsonb_set(extension_configs,ARRAY[$2,'configHash'],to_jsonb(repeat('b',64))) WHERE dataset_id=$1 AND channel='candidate'`, f.p.DatasetID, rd.Model); e != nil {
					t.Fatal(e)
				}
			case "assignment":
				if _, e := f.s.Pool.Exec(ctx, `UPDATE analysis_model_channels SET extension_configs=jsonb_set(extension_configs,ARRAY[$2,'assignmentID'],to_jsonb($3::text)) WHERE dataset_id=$1 AND channel='candidate'`, f.p.DatasetID, rd.Model, syncing.NewID()); e != nil {
					t.Fatal(e)
				}
			case "timezone":
				if _, e := f.s.Pool.Exec(ctx, `UPDATE datasets SET timezone='America/New_York' WHERE id=$1`, f.p.DatasetID); e != nil {
					t.Fatal(e)
				}
			case "collector":
				if _, e := f.s.Pool.Exec(ctx, `UPDATE collector_leases SET collector_epoch=$2 WHERE dataset_id=$1`, f.p.DatasetID, syncing.NewID()); e != nil {
					t.Fatal(e)
				}
			case "device":
				if _, e := f.s.Pool.Exec(ctx, `UPDATE devices SET revoked_at=clock_timestamp() WHERE dataset_id=$1 AND id=$2`, f.p.DatasetID, f.p.DeviceID); e != nil {
					t.Fatal(e)
				}
			case "m2_assignment":
				if _, e := f.s.Pool.Exec(ctx, `UPDATE analysis_model_channels SET assignment_version=assignment_version+1 WHERE dataset_id=$1 AND channel='candidate'`, f.p.DatasetID); e != nil {
					t.Fatal(e)
				}
			case "lease", "takeover":
				if _, e := f.s.Pool.Exec(ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, l.ID); e != nil {
					t.Fatal(e)
				}
				if change == "takeover" {
					if _, e := f.s.RequeueExpired(ctx); e != nil {
						t.Fatal(e)
					}
					if _, e := f.s.Pool.Exec(ctx, `UPDATE jobs SET run_after=clock_timestamp() WHERE id=$1`, l.ID); e != nil {
						t.Fatal(e)
					}
					newLease := latestReadinessLease(t, f)
					jobs.Process(ctx, f.s, newLease, analysisLog)
				}
			}
			status, e := f.s.PublishAnalysis(ctx, l, id, in, ms)
			if change == "lease" || change == "takeover" {
				if e == nil {
					t.Fatal("old lease published")
				}
			} else if e != nil || status != "SUPERSEDED" {
				t.Fatal("stale input published", status, e)
			}
			var n int
			if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM analysis_artifacts WHERE run_id=$1`, id).Scan(&n); e != nil || n != 0 {
				t.Fatal("superseded partial commit", e)
			}
		})
	}
}
func TestReadinessDeleteRevisionAndRebuild(t *testing.T) {
	f, ws, bs := readinessFixture(t)
	ctx := context.Background()
	runReadinessLatest(t, f)
	original, oldID := readReadiness(t, f)
	rng := rand.New(rand.NewSource(6))
	active := append([]syncing.WorkoutBundle{}, ws...)
	for step := 0; step < 18; step++ {
		switch step % 6 {
		case 0:
			b := m3Bundle(t, f, time.Now().Add(-time.Duration(1+rng.Intn(50))*time.Hour), 300)
			importPhase3(t, f, b)
			if e := admin.QueueTrainingLoadCandidate(ctx, f.s, f.p.DatasetID, b.WorkoutID, syncing.NewID(), "synthetic late insert"); e != nil {
				t.Fatal(e)
			}
			drainReadinessUpstream(t, f, wf.JobKind)
			drainReadinessUpstream(t, f, tl.JobKind)
			active = append(active, b)
		case 1:
			b := active[len(active)-1]
			active = active[:len(active)-1]
			if _, e := f.s.DeleteWorkoutBundle(ctx, f.p, bundleDeleteRequest(f, b.WorkoutID)); e != nil {
				t.Fatal(e)
			}
		case 2:
			b := nextDaily(bs[rc.RHR])
			x := bs[rc.RHR].Facts[0]
			b.DeletedUUIDs = []string{x.UUID}
			x.UUID = syncing.NewID()
			x.Quantity = &syncing.Quantity{Value: 65 + float64(step), Unit: "count/min"}
			b.Facts = []syncing.DailyFact{x}
			requireDaily(t, f, b)
			bs[rc.RHR] = b
			runRecoveryLatest(t, f)
		case 3:
			if e := admin.QueueTrainingLoadCandidate(ctx, f.s, f.p.DatasetID, ws[0].WorkoutID, syncing.NewID(), "synthetic revision"); e != nil {
				t.Fatal(e)
			}
			drainReadinessUpstream(t, f, wf.JobKind)
			drainReadinessUpstream(t, f, tl.JobKind)
		case 4:
			if _, e := f.s.Pool.Exec(ctx, `DELETE FROM analysis_publications WHERE dataset_id=$1 AND metric_family=$2`, f.p.DatasetID, rc.Family); e != nil {
				t.Fatal(e)
			}
			if _, _, e := f.s.CandidateReadiness(ctx, f.p.DatasetID, time.Now()); e == nil {
				t.Fatal("deleted recovery used")
			}
			runReadinessLatest(t, f)
			missing, _ := readReadiness(t, f)
			if missing.Score != nil {
				t.Fatal("missing recovery became score")
			}
			if e := admin.QueueRecoveryCandidate(ctx, f.s, f.p.DatasetID, original.Date, syncing.NewID(), "synthetic recovery rebuild"); e != nil {
				t.Fatal(e)
			}
			runRecoveryLatest(t, f)
		case 5:
			if _, e := f.s.Pool.Exec(ctx, `UPDATE analysis_model_channels SET extension_configs=jsonb_set(extension_configs,ARRAY[$2,'assignmentID'],to_jsonb($3::text)) WHERE dataset_id=$1 AND channel='candidate'`, f.p.DatasetID, rd.Model, syncing.NewID()); e != nil {
				t.Fatal(e)
			}
		}

		// Already-published old candidates must be immediately gone before recompute.
		if _, _, e := f.s.CandidateReadiness(ctx, f.p.DatasetID, time.Now()); e == nil {
			t.Fatal("old candidate survived event")
		}
		readinessSyntheticCoverage(t, f)
		runReadinessLatest(t, f)
		incremental, _ := readReadiness(t, f)
		// Independent bounded snapshot rebuild, evaluated at exactly the anchor time.
		l := postgres.Lease{DatasetID: f.p.DatasetID, Kind: rd.JobKind, ScopeKey: rd.Scope(incremental.Config.Timezone), TargetGeneration: incremental.Generation}
		snap, e := f.s.LoadAnalysis(ctx, l, time.UnixMicro(incremental.AsOf))
		if e != nil {
			t.Fatal(e)
		}
		rebuilt, e := rd.Calculate(*snap.ReadinessInput)
		if e != nil {
			t.Fatal(e)
		}
		at := time.UnixMicro(incremental.EvaluationAt)
		rebuilt, e = rd.Freshen(rebuilt, at.UnixMicro())
		if e != nil {
			t.Fatal(e)
		}
		a, b := readinessSemantic(incremental), readinessSemantic(rebuilt)
		if a != b {
			t.Fatal("incremental != bounded rebuild", step)
		}
	}
	var raw []byte
	if e := f.s.Pool.QueryRow(ctx, `SELECT payload FROM analysis_artifacts WHERE dataset_id=$1 AND run_id=$2 AND artifact_key=$3`, f.p.DatasetID, oldID, rd.Key).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var hist rd.Result
	_ = json.Unmarshal(raw, &hist)
	if hist.AsOf != original.AsOf || hist.InputHash != original.InputHash {
		t.Fatal("history overwritten")
	}
}
func readinessSemantic(r rd.Result) string {
	return rd.Hash(struct {
		Status           string
		Score, Burden, B *float64
		Confidence       float64
		Known, Unknown   int
		Reasons          []string
	}{r.Status, r.Score, r.Burden, r.Baseline.Value, r.Confidence, r.KnownLoads, r.UnknownLoads, r.Reasons})
}

func drainReadinessUpstream(t *testing.T, f *fixture, kind string) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 1000; i++ {
		l, e := f.s.Claim(ctx, []string{kind})
		if errors.Is(e, pgx.ErrNoRows) {
			return
		}
		if e != nil {
			t.Fatal(e)
		}
		jobs.Process(ctx, f.s, l, analysisLog)
		var state, code string
		if e = f.s.Pool.QueryRow(ctx, `SELECT state,COALESCE(last_error_code,'') FROM jobs WHERE id=$1`, l.ID).Scan(&state, &code); e != nil || state != "succeeded" {
			t.Fatal("upstream", kind, state, code, e)
		}
	}
	t.Fatal("upstream unbounded")
}

func TestReadinessActualM2RevisionAndRecoveryIndependence(t *testing.T) {
	f, ws, _ := readinessFixture(t)
	ctx := context.Background()
	runReadinessLatest(t, f)
	before, oldID := readReadiness(t, f)
	var load float64
	var parameter string
	if e := f.s.Pool.QueryRow(ctx, `SELECT x.contribution,x.parameter_id::text FROM analysis_load_ledger x JOIN analysis_publications p ON p.dataset_id=x.dataset_id AND p.published_run_id=x.run_id WHERE x.dataset_id=$1 AND x.workout_uuid=$2 AND p.metric_family='workout_load_candidate' LIMIT 1`, f.p.DatasetID, ws[5].WorkoutID).Scan(&load, &parameter); e != nil {
		t.Fatal(e)
	}
	through := time.Now().Add(24 * time.Hour).UnixMicro()
	if e := admin.SetFeatureHRReference(ctx, f.s, f.p.DatasetID, wf.Parameter{ID: syncing.NewID(), Supersedes: parameter, EffectiveFrom: time.Now().AddDate(0, 0, -180).UnixMicro(), EffectiveThrough: &through, RestHR: 80, MaxHR: 200, Provenance: "synthetic M6 revised effective M2 context"}); e != nil {
		t.Fatal(e)
	}
	if _, _, e := f.s.CandidateReadiness(ctx, f.p.DatasetID, time.Now()); e == nil {
		t.Fatal("M2 revision left old readiness current")
	}
	drainReadinessUpstream(t, f, wf.JobKind)
	drainReadinessUpstream(t, f, tl.JobKind)
	readinessSyntheticCoverage(t, f)
	runReadinessLatest(t, f)
	after, newID := readReadiness(t, f)
	if newID == oldID || after.Score == nil || after.Baseline.Value == nil || *after.Baseline.Value == *before.Baseline.Value || *after.Burden == *before.Burden {
		t.Fatal("actual M2 revision not propagated")
	}
	if after.Recovery.RunID != before.Recovery.RunID || after.Recovery.Hash != before.Recovery.Hash || *after.RecoveryScore != *before.RecoveryScore {
		t.Fatal("load changed physiological Recovery")
	}
	var revised float64
	if e := f.s.Pool.QueryRow(ctx, `SELECT x.contribution FROM analysis_load_ledger x JOIN analysis_publications p ON p.dataset_id=x.dataset_id AND p.published_run_id=x.run_id WHERE x.dataset_id=$1 AND x.workout_uuid=$2 AND p.metric_family='workout_load_candidate' LIMIT 1`, f.p.DatasetID, ws[5].WorkoutID).Scan(&revised); e != nil || load == revised {
		t.Fatal("M2 test did not change AU", e)
	}
}

func TestReadinessPersistedDependencyAlwaysQueuesCurrent(t *testing.T) {
	f, ws, _ := readinessFixture(t)
	ctx := context.Background()
	runReadinessLatest(t, f)
	before, _ := readReadiness(t, f)
	// Even if an event's range has aged outside the current live filter, a
	// persisted anchor dependency must not be left dirty without a runnable job.
	if _, e := f.s.Pool.Exec(ctx, `SELECT rai_feature_downstream_dirty($1,$2,$3,$4)`, f.p.DatasetID, tl.Scope(ws[0].WorkoutID), time.Now().AddDate(0, 0, -181), time.Now().AddDate(0, 0, -180)); e != nil {
		t.Fatal(e)
	}
	var ok bool
	if e := f.s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM dirty_scopes d JOIN jobs j ON j.dataset_id=d.dataset_id AND j.scope_key=d.scope_key AND j.target_generation=d.generation AND j.kind=$3 WHERE d.dataset_id=$1 AND d.scope_key=$2 AND d.generation>$4 AND d.clean_generation<d.generation)`, f.p.DatasetID, rd.Scope(before.Config.Timezone), rd.JobKind, before.Generation).Scan(&ok); e != nil || !ok {
		t.Fatal("orphaned current generation", e)
	}
	runReadinessLatest(t, f)
	after, _ := readReadiness(t, f)
	if after.Score == nil {
		t.Fatal("current not rebuilt")
	}
}

func TestReadinessRejectsM2UnitMismatch(t *testing.T) {
	f, ws, _ := readinessFixture(t)
	ctx := context.Background()
	runReadinessLatest(t, f)
	if e := admin.QueueTrainingLoadCandidate(ctx, f.s, f.p.DatasetID, ws[5].WorkoutID, syncing.NewID(), "synthetic invalid M2 unit fixture"); e != nil {
		t.Fatal(e)
	}
	drainReadinessUpstream(t, f, wf.JobKind)
	l := claimAnalysis(t, f, tl.JobKind)
	in, id, ms := computeLoad(t, f, l)
	if in.LoadArtifact == nil || in.LoadArtifact.Load == nil {
		t.Fatal("synthetic M2 load missing")
	}
	in.LoadArtifact.Unit = "legacy_TRIMP"
	if status, e := f.s.PublishAnalysis(ctx, l, id, in, ms); e != nil || status != "SUCCEEDED" {
		t.Fatal("synthetic mismatched M2 artifact", e, status)
	}
	readinessSyntheticCoverage(t, f)
	runReadinessLatest(t, f)
	r, _ := readReadiness(t, f)
	if r.Score != nil || r.Status != "source_conflict" {
		t.Fatal("non-RAI_AU was consumed", r.Reasons)
	}
}
