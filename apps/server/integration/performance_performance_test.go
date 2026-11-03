//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"rai/server/internal/admin"
	"rai/server/internal/analysis"
	pf "rai/server/internal/performance"
	"rai/server/internal/syncing"
	wf "rai/server/internal/workoutfeatures"
)

func TestPerformanceMatrix(t *testing.T) {
	if os.Getenv("RAI_M3_PERF") != "1" {
		t.Skip("explicit synthetic M3 performance matrix")
	}
	for _, minutes := range []int{20, 45, 120, 240} {
		t.Run(fmt.Sprint("workout_", minutes), func(t *testing.T) {
			f, _ := performanceFixture(t, true)
			b := m3Bundle(t, f, time.Now().UTC().Add(-time.Minute), minutes*60)
			importPhase3(t, f, b)
			runOne(t, f, wf.JobKind)
			measurePerformance(t, f, fmt.Sprint("workout_", minutes))
		})
	}
	for _, history := range []int{30, 90, 180, 365} {
		t.Run(fmt.Sprint("history_", history), func(t *testing.T) {
			f := setup(t)
			latest := time.Now().UTC().Add(-time.Hour)
			for d := history - 1; d >= 0; d -= 3 {
				seconds := 1200
				if d%2 == 0 {
					seconds = 2700
				}
				b := m3Bundle(t, f, latest.AddDate(0, 0, -d), seconds)
				importPhase3(t, f, b)
			}
			if err := admin.QueuePerformanceCandidate(context.Background(), f.s, f.p.DatasetID, syncing.NewID(), "synthetic matrix"); err != nil {
				t.Fatal(err)
			}
			// Claim every feature generation; successful current scopes, rather than
			// historical raw samples, are the complete athlete-model input.
			for {
				var pending int
				if err := f.s.Pool.QueryRow(context.Background(), `SELECT count(*) FROM jobs WHERE dataset_id=$1 AND kind=$2 AND state='queued'`, f.p.DatasetID, wf.JobKind).Scan(&pending); err != nil {
					t.Fatal(err)
				}
				if pending == 0 {
					break
				}
				runOne(t, f, wf.JobKind)
			}
			measurePerformance(t, f, fmt.Sprint("history_", history))
		})
	}
}
func measurePerformance(t *testing.T, f *fixture, label string) {
	t.Helper()
	ctx := context.Background()
	l := claimAnalysis(t, f, pf.JobKind)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	in, err := f.s.LoadAnalysis(ctx, l, time.Now().UTC())
	read := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	es, err := pf.Extract(*in.PerformanceInput)
	extraction := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	id, err := f.s.StartAnalysisRun(ctx, l, in)
	if err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	ms, err := analysis.CalculateCandidatePerformance(&in)
	calculation := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	status, err := f.s.PublishAnalysis(ctx, l, id, in, ms)
	publish := time.Since(start)
	if err != nil || status != "SUCCEEDED" {
		t.Fatal(status, err)
	}
	runtime.ReadMemStats(&after)
	blob, _ := json.Marshal(in.PerformanceArtifact)
	var rawWorkouts, artifactBytes int
	if err = f.s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM workouts WHERE dataset_id=$1),(SELECT sum(octet_length(payload::text)) FROM analysis_artifacts WHERE run_id=$2)`, f.p.DatasetID, id).Scan(&rawWorkouts, &artifactBytes); err != nil {
		t.Fatal(err)
	}
	if len(in.PerformanceInput.Features) > pf.MaxWorkouts || len(es) > 5*pf.MaxWorkouts {
		t.Fatal("unbounded evidence")
	}
	if in.PerformanceArtifact.Ability == nil {
		t.Fatal("established matrix has no ability", in.PerformanceArtifact.Status)
	}
	t.Logf("M3_PERF %s feature_read_ms=%.3f evidence_extract_ms=%.3f curve_smoothing_loo_ms=%.3f publish_ms=%.3f allocations=%d allocated_bytes=%d state_bytes=%d all_artifacts_bytes=%d raw_workouts=%d feature_refs=%d selected_points=%d", label, float64(read.Microseconds())/1000, float64(extraction.Microseconds())/1000, float64(calculation.Microseconds())/1000, float64(publish.Microseconds())/1000, after.Mallocs-before.Mallocs, after.TotalAlloc-before.TotalAlloc, len(blob), artifactBytes, rawWorkouts, len(in.PerformanceInput.Features), len(es))
}
