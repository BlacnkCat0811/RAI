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
	rd "rai/server/internal/readiness"
	"rai/server/internal/syncing"
	tl "rai/server/internal/trainingload"
	wf "rai/server/internal/workoutfeatures"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestReadinessPerformanceMatrix(t *testing.T) {
	if os.Getenv("RAI_M6_PERF") != "1" {
		t.Skip("explicit synthetic M6 matrix")
	}
	ctx := context.Background()
	for _, history := range []int{14, 42, 90, 180, 365} {
		t.Run(fmt.Sprint(history), func(t *testing.T) {
			f, ws, _ := readinessFixture(t)
			now := time.Now()
			if history == 14 {
				for _, w := range ws[:4] {
					if _, e := f.s.DeleteWorkoutBundle(ctx, f.p, bundleDeleteRequest(f, w.WorkoutID)); e != nil {
						t.Fatal(e)
					}
				}
			}
			// Sparse old history is deliberately outside/inside the certified 180d bound.
			for back := 50; back <= history; back += 30 {
				hr := 150.
				b := simpleLoadBundle(t, f, now.AddDate(0, 0, -back), 300, &hr)
				importPhase3(t, f, b)
				if e := admin.QueueTrainingLoadCandidate(ctx, f.s, f.p.DatasetID, b.WorkoutID, syncing.NewID(), "synthetic M6 history"); e != nil {
					t.Fatal(e)
				}
			}
			drainReadinessUpstream(t, f, wf.JobKind)
			drainReadinessUpstream(t, f, tl.JobKind)
			previous := 0
			for _, density := range []int{0, 1, 5, 20, 100} {
				for i := previous; i < density; i++ {
					hr := 150.
					b := simpleLoadBundle(t, f, now.Add(-time.Duration(10+i)*time.Minute), 300, &hr)
					importPhase3(t, f, b)
					if e := admin.QueueTrainingLoadCandidate(ctx, f.s, f.p.DatasetID, b.WorkoutID, syncing.NewID(), "synthetic M6 density"); e != nil {
						t.Fatal(e)
					}
				}
				previous = density
				drainReadinessUpstream(t, f, wf.JobKind)
				drainReadinessUpstream(t, f, tl.JobKind)
				readinessSyntheticCoverage(t, f)
				l := latestReadinessLease(t, f)
				runtime.GC()
				var a, b runtime.MemStats
				runtime.ReadMemStats(&a)
				at := time.Now()
				in, e := f.s.LoadAnalysis(ctx, l, time.Now())
				snapshot := time.Since(at)
				if e != nil {
					t.Fatal(e)
				}
				at = time.Now()
				base := rd.LoadBaseline(*in.ReadinessInput)
				baseline := time.Since(at)
				_ = base
				at = time.Now()
				ms, e := analysis.CalculateCandidateReadiness(&in)
				calculate := time.Since(at)
				if e != nil {
					t.Fatal(e)
				}
				at = time.Now()
				_ = rd.DecayedBurden(in.ReadinessArtifact.Terms, in.ReadinessInput.AsOf, in.ReadinessInput.Config)
				decay := time.Since(at)
				at = time.Now()
				_, _ = rd.Score(80, 100, 100)
				formula := time.Since(at)
				at = time.Now()
				data, e := json.Marshal(in.ReadinessArtifact)
				serialization := time.Since(at)
				if e != nil {
					t.Fatal(e)
				}
				runtime.ReadMemStats(&b)
				id, e := f.s.StartAnalysisRun(ctx, l, in)
				if e != nil {
					t.Fatal(e)
				}
				at = time.Now()
				if status, e := f.s.PublishAnalysis(ctx, l, id, in, ms); e != nil || status != "SUCCEEDED" {
					t.Fatal("publication", status, e)
				}
				publication := time.Since(at)
				at = time.Now()
				r, _, e := f.s.CandidateReadiness(ctx, f.p.DatasetID, time.Now())
				reader := time.Since(at)
				if e != nil {
					t.Fatal(e)
				}
				at = time.Now()
				_, e = rd.Freshen(*in.ReadinessArtifact, in.ReadinessInput.AsOf+rd.Hour/2)
				freshen := time.Since(at)
				if e != nil {
					t.Fatal(e)
				}
				if history != 14 && r.Score == nil {
					t.Fatal("dense known ledger unavailable", r.Reasons)
				}
				t.Logf("M6_PERF history=%d recent_entries=%d input_workouts=%d snapshot_us=%d decay_ns=%d baseline_ns=%d formula_ns=%d calculate_us=%d serialization_us=%d publication_us=%d reader_us=%d freshen_ns=%d allocations=%d allocated_bytes=%d artifact_bytes=%d", history, density, len(in.ReadinessInput.Loads), snapshot.Microseconds(), decay.Nanoseconds(), baseline.Nanoseconds(), formula.Nanoseconds(), calculate.Microseconds(), serialization.Microseconds(), publication.Microseconds(), reader.Microseconds(), freshen.Nanoseconds(), b.Mallocs-a.Mallocs, b.TotalAlloc-a.TotalAlloc, len(data))
			}
		})
	}
}
func TestReadinessSnapshotRepeatableRead(t *testing.T) {
	f, _, _ := readinessFixture(t)
	ctx := context.Background()
	l := latestReadinessLease(t, f)
	block, e := f.s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer block.Rollback(ctx)
	if _, e = block.Exec(ctx, `LOCK TABLE analysis_load_ledger IN ACCESS EXCLUSIVE MODE`); e != nil {
		t.Fatal(e)
	}
	type outcome struct {
		in analysis.Snapshot
		e  error
	}
	done := make(chan outcome, 1)
	go func() { in, e := f.s.LoadAnalysis(ctx, l, time.Now()); done <- outcome{in, e} }()
	waiting := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if e = f.s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%FROM analysis_load_ledger WHERE%')`).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !waiting {
		t.Fatal("snapshot barrier not reached")
	}
	readinessSyntheticCoverage(t, f)
	if e = block.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	got := <-done
	if got.e != nil || got.in.Generation != l.TargetGeneration {
		t.Fatal("mixed snapshot", got.e)
	}
	id, e := f.s.StartAnalysisRun(ctx, l, got.in)
	if e != nil {
		t.Fatal(e)
	}
	ms, e := analysis.CalculateCandidateReadiness(&got.in)
	if e != nil {
		t.Fatal(e)
	}
	if status, e := f.s.PublishAnalysis(ctx, l, id, got.in, ms); e != nil || status != "SUPERSEDED" {
		t.Fatal("RR snapshot did not fence", status, e)
	}
}
func TestReadinessBoundedQueryAndIndependentInputs(t *testing.T) {
	f, ws, _ := readinessFixture(t)
	ctx := context.Background()
	runReadinessLatest(t, f)
	base, id := readReadiness(t, f)
	// M3 assignment and actual external VO2 facts do not change M2/M5B identities.
	if _, e := f.s.Pool.Exec(ctx, `UPDATE analysis_model_channels SET extension_configs=extension_configs||'{"rai.performance_curve/v1":"synthetic-context-only"}'::jsonb WHERE dataset_id=$1 AND channel='candidate'`, f.p.DatasetID); e != nil {
		t.Fatal(e)
	}
	v := externalBatch(t, f)
	requireVO2(t, f, v)
	got, gotID, e := f.s.CandidateReadiness(ctx, f.p.DatasetID, time.UnixMicro(base.EvaluationAt))
	if e != nil || gotID != id || rd.Hash(got) != rd.Hash(base) {
		t.Fatal("M3/M4 affected readiness", e)
	}
	detail, e := f.s.WorkoutDetail(ctx, f.p, ws[0].WorkoutID)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(detail)
	if bytesContainsM6(raw) {
		t.Fatal("workout DTO leak")
	}
	// An old Workout is not read, and cannot invalidate the current 180d scope.
	before := base.Generation
	b := m3Bundle(t, f, time.Now().AddDate(0, 0, -365), 300)
	importPhase3(t, f, b)
	var generation int64
	if e = f.s.Pool.QueryRow(ctx, `SELECT generation FROM dirty_scopes WHERE dataset_id=$1 AND scope_key=$2`, f.p.DatasetID, rd.Scope(base.Config.Timezone)).Scan(&generation); e != nil || before != generation {
		t.Fatal("unbounded old workout invalidation", e)
	}
	// Actual EXPLAIN has an end-time range in the dedicated running-workout index.
	if _, e = f.s.Pool.Exec(ctx, `ANALYZE workouts; SET enable_seqscan=off`); e != nil {
		t.Fatal(e)
	}
	var plan []byte
	if e = f.s.Pool.QueryRow(ctx, `EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) SELECT workout_uuid,event_end FROM workouts WHERE dataset_id=$1 AND event_end>=$2 AND event_end<$3 AND payload->'workout'->>'activityType'='37'`, f.p.DatasetID, time.UnixMicro(base.AsOf-base.Config.LookbackUs), time.UnixMicro(base.ValidBefore)).Scan(&plan); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(plan), "readiness_workout_end") || !strings.Contains(string(plan), "event_end >=") || !strings.Contains(string(plan), "event_end <") {
		t.Fatal("unbounded query plan")
	}
	t.Logf("M6_QUERY_PLAN %s", strings.ReplaceAll(string(plan), f.p.DatasetID, "<dataset>"))
	if math.IsNaN(base.TailBound) || base.TailBound >= 3e-10 {
		t.Fatal("tail proof")
	}
}
func bytesContainsM6(raw []byte) bool {
	return strings.Contains(string(raw), rd.Model) || strings.Contains(string(raw), rd.Key)
}
