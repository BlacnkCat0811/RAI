//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"rai/server/internal/admin"
	"rai/server/internal/analysis"
	"rai/server/internal/syncing"
	tl "rai/server/internal/trainingload"
	wf "rai/server/internal/workoutfeatures"
	"runtime"
	"testing"
	"time"
)

func simpleLoadBundle(t *testing.T, f *fixture, at time.Time, seconds int, hr *float64) syncing.WorkoutBundle {
	t.Helper()
	b, _ := testWorkoutBundle(t, f, 0)
	b.Workout = workoutAt(at, seconds)
	b.WorkoutID = b.Workout.UUID
	b.Routes = nil
	b.Series = nil
	b.Samples = nil
	if hr != nil {
		q := bundleSample(b.WorkoutID, at, *hr)
		q.EndUs = b.Workout.EndUs
		b.Samples = []syncing.Event{q}
		b.Series = []syncing.WorkoutBundleSeries{{SampleType: kind, SampleUUID: q.UUID, Points: []syncing.SeriesPoint{{StartUs: *q.StartUs, EndUs: *q.EndUs, Value: *hr, Unit: "count/min"}}}}
	}
	phase3Observation(&b)
	b.Observation.Coverage["series:"+kind] = syncing.BundleCoverage{State: "complete", SampleCount: 1, QueryCompleted: true, LocalPersisted: true}
	return b
}
func installLoadTiming(t *testing.T, f *fixture) {
	t.Helper()
	if _, e := f.s.Pool.Exec(context.Background(), `CREATE TABLE m2_timing(stage text,at timestamptz);CREATE FUNCTION m2_timing() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN INSERT INTO m2_timing VALUES(TG_TABLE_NAME||'_'||TG_WHEN,clock_timestamp());RETURN NEW;END $$;
 CREATE TRIGGER m2_timing_before BEFORE INSERT ON analysis_load_ledger FOR EACH ROW EXECUTE FUNCTION m2_timing();CREATE TRIGGER m2_timing_after AFTER INSERT ON analysis_load_ledger FOR EACH ROW EXECUTE FUNCTION m2_timing();
 CREATE TRIGGER m2_pub_before BEFORE INSERT ON analysis_publications FOR EACH ROW EXECUTE FUNCTION m2_timing();CREATE TRIGGER m2_pub_after AFTER INSERT ON analysis_publications FOR EACH ROW EXECUTE FUNCTION m2_timing()`); e != nil {
		t.Fatal(e)
	}
}
func TestLoadPerformanceMatrix(t *testing.T) {
	if os.Getenv("RAI_M2_PERF") != "1" {
		t.Skip("explicit synthetic M2 performance matrix")
	}
	ctx := context.Background()
	for _, minutes := range []int{20, 45, 120, 240} {
		t.Run(fmt.Sprint(minutes, "m"), func(t *testing.T) {
			f := setup(t)
			b := featureBundle(t, f, minutes*60)
			importPhase3(t, f, b)
			through := *b.Workout.EndUs + 3600e6
			if e := admin.SetFeatureHRReference(ctx, f.s, f.p.DatasetID, wf.Parameter{ID: syncing.NewID(), EffectiveFrom: *b.Workout.StartUs - 1e6, EffectiveThrough: &through, RestHR: 50, MaxHR: 200, Provenance: "synthetic perf"}); e != nil {
				t.Fatal(e)
			}
			if e := admin.QueueTrainingLoadCandidate(ctx, f.s, f.p.DatasetID, b.WorkoutID, syncing.NewID(), "synthetic perf"); e != nil {
				t.Fatal(e)
			}
			runOne(t, f, wf.JobKind)
			installLoadTiming(t, f)
			l := claimAnalysis(t, f, tl.JobKind)
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			at := time.Now()
			in, e := f.s.LoadAnalysis(ctx, l, time.Now().UTC())
			read := time.Since(at)
			if e != nil {
				t.Fatal(e)
			}
			id, e := f.s.StartAnalysisRun(ctx, l, in)
			if e != nil {
				t.Fatal(e)
			}
			at = time.Now()
			ms, e := analysis.CalculateCandidateLoad(&in, tl.JobKind)
			compute := time.Since(at)
			if e != nil {
				t.Fatal(e)
			}
			data, _ := json.Marshal(in.LoadArtifact)
			runtime.ReadMemStats(&after)
			at = time.Now()
			if _, e = f.s.PublishAnalysis(ctx, l, id, in, ms); e != nil {
				t.Fatal(e)
			}
			publish := time.Since(at)
			var ledger, pointer float64
			if e = f.s.Pool.QueryRow(ctx, `SELECT COALESCE(extract(epoch FROM (max(at) FILTER(WHERE stage='analysis_load_ledger_AFTER')-min(at) FILTER(WHERE stage='analysis_load_ledger_BEFORE')))*1000,0),COALESCE(extract(epoch FROM (max(at) FILTER(WHERE stage='analysis_publications_AFTER')-min(at) FILTER(WHERE stage='analysis_publications_BEFORE')))*1000,0) FROM m2_timing`).Scan(&ledger, &pointer); e != nil {
				t.Fatal(e)
			}
			l = claimAnalysis(t, f, tl.StateJobKind)
			at = time.Now()
			state, sid, sms := computeLoad(t, f, l)
			stateCompute := time.Since(at)
			at = time.Now()
			if _, e = f.s.PublishAnalysis(ctx, l, sid, state, sms); e != nil {
				t.Fatal(e)
			}
			stateWrite := time.Since(at)
			t.Logf("M2_PERF minutes=%d feature_read_ms=%.3f load_compute_ms=%.3f ledger_insert_ms=%.3f publication_pointer_ms=%.3f load_publish_transaction_ms=%.3f state_read_compute_ms=%.3f state_publish_ms=%.3f output_days=%d allocations=%d allocated_bytes=%d load_artifact_bytes=%d exposure_segments=%d", minutes, float64(read.Microseconds())/1000, float64(compute.Microseconds())/1000, ledger, pointer, float64(publish.Microseconds())/1000, float64(stateCompute.Microseconds())/1000, float64(stateWrite.Microseconds())/1000, len(state.LoadStateArtifact.Days), after.Mallocs-before.Mallocs, after.TotalAlloc-before.TotalAlloc, len(data), len(in.LoadInput.Exposure.Segments))
		})
	}
	for _, days := range []int{30, 90, 180, 365} {
		t.Run(fmt.Sprint(days, "d"), func(t *testing.T) {
			f := setup(t)
			latest := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -1).Add(8 * time.Hour)
			first := latest.AddDate(0, 0, 1-days)
			through := latest.Add(2 * time.Hour).UnixMicro()
			if e := admin.SetFeatureHRReference(ctx, f.s, f.p.DatasetID, wf.Parameter{ID: syncing.NewID(), EffectiveFrom: first.Add(-time.Hour).UnixMicro(), EffectiveThrough: &through, RestHR: 50, MaxHR: 200, Provenance: "synthetic athlete history"}); e != nil {
				t.Fatal(e)
			}
			hr := 147.5
			for d := 0; d < days; d++ {
				b := simpleLoadBundle(t, f, first.AddDate(0, 0, d), 1200, &hr)
				importPhase3(t, f, b)
				if d == 0 {
					if e := admin.QueueTrainingLoadCandidate(ctx, f.s, f.p.DatasetID, b.WorkoutID, syncing.NewID(), "synthetic athlete history"); e != nil {
						t.Fatal(e)
					}
				}
				runOne(t, f, wf.JobKind)
				runOne(t, f, tl.JobKind)
			}
			l := claimAnalysis(t, f, tl.StateJobKind)
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			at := time.Now()
			in, e := f.s.LoadAnalysis(ctx, l, time.Now().UTC())
			read := time.Since(at)
			if e != nil {
				t.Fatal(e)
			}
			id, e := f.s.StartAnalysisRun(ctx, l, in)
			if e != nil {
				t.Fatal(e)
			}
			at = time.Now()
			ms, e := analysis.CalculateCandidateLoad(&in, tl.StateJobKind)
			compute := time.Since(at)
			if e != nil {
				t.Fatal(e)
			}
			runtime.ReadMemStats(&after)
			data, _ := json.Marshal(in.LoadStateArtifact)
			at = time.Now()
			if _, e = f.s.PublishAnalysis(ctx, l, id, in, ms); e != nil {
				t.Fatal(e)
			}
			write := time.Since(at)
			// Every persisted point is independently checked against the direct kernel.
			for _, d := range in.LoadStateArtifact.Days {
				date, _ := time.Parse("2006-01-02", d.Day)
				wantA, wantC := 0.0, 0.0
				for _, load := range in.LoadStateInput.Loads {
					at, _ := time.Parse("2006-01-02", load.Day)
					k := int(date.Sub(at).Hours() / 24)
					if k >= 0 && k < 180 {
						wantA += -math.Expm1(-1.0/7) * math.Exp(-float64(k)/7) * *load.Load
						wantC += -math.Expm1(-1.0/42) * math.Exp(-float64(k)/42) * *load.Load
					}
				}
				if math.Abs(d.Acute.KnownContribution-wantA) > 1e-10 || math.Abs(d.Chronic.KnownContribution-wantC) > 1e-10 {
					t.Fatal("state != direct convolution")
				}
			}
			t.Logf("M2_PERF history_days=%d ledger_read_ms=%.3f state_compute_ms=%.3f state_publish_ms=%.3f recompute_from=%s recompute_through=%s ledger_rows=%d allocations=%d allocated_bytes=%d state_artifact_bytes=%d", days, float64(read.Microseconds())/1000, float64(compute.Microseconds())/1000, float64(write.Microseconds())/1000, in.LoadStateInput.From, in.LoadStateInput.Through, len(in.LoadStateInput.Loads), after.Mallocs-before.Mallocs, after.TotalAlloc-before.TotalAlloc, len(data))
		})
	}
}
