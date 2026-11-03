//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"rai/server/internal/admin"
	"rai/server/internal/syncing"
	wf "rai/server/internal/workoutfeatures"
	"runtime"
	"testing"
	"time"
)

func TestFeatureSnapshotPerformanceMatrix(t *testing.T) {
	if os.Getenv("RAI_M1_PERF") != "1" {
		t.Skip("explicit synthetic M1 performance matrix")
	}
	for _, minutes := range []int{20, 45, 120, 240} {
		t.Run(fmt.Sprint(minutes), func(t *testing.T) {
			f := setup(t)
			b := featureBundle(t, f, minutes*60)
			importPhase3(t, f, b)
			ctx := context.Background()
			if err := admin.QueueFeatureCandidate(ctx, f.s, f.p.DatasetID, b.WorkoutID, syncing.NewID(), "synthetic benchmark"); err != nil {
				t.Fatal(err)
			}
			l := claimAnalysis(t, f, wf.JobKind)
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			start := time.Now()
			in, err := f.s.LoadAnalysis(ctx, l, time.Now().UTC())
			load := time.Since(start)
			if err != nil {
				t.Fatal(err)
			}
			start = time.Now()
			v, err := wf.ComputeNormalized(ctx, *in.FeatureInput)
			calculate := time.Since(start)
			if err != nil {
				t.Fatal(err)
			}
			data, _, err := v.MarshalArtifact()
			if err != nil {
				t.Fatal(err)
			}
			runtime.ReadMemStats(&after)
			t.Logf("M1_PERF minutes=%d snapshot_load_ms=%.3f calculation_ms=%.3f total_ms=%.3f allocations=%d allocated_bytes=%d artifact_bytes=%d raw_quantities=%d series_points=%d route_points=%d", minutes, float64(load.Microseconds())/1000, float64(calculate.Microseconds())/1000, float64((load+calculate).Microseconds())/1000, after.Mallocs-before.Mallocs, after.TotalAlloc-before.TotalAlloc, len(data), len(in.FeatureInput.Quantities), len(in.FeatureInput.Series[0].Points), len(in.FeatureInput.Routes[0].Points))
		})
	}
}
