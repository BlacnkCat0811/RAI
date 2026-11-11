//go:build integration && modelperf

package integration

import (
	"context"
	"fmt"
	"rai/server/internal/modelmanifest"
	"rai/server/internal/syncing"
	"runtime"
	"sort"
	"testing"
	"time"
)

// The same file is executable against the pre-optimization checkout. It uses
// 30 synthetic HR-supported runs, the real PG path and production Worker; no
// values, identifiers or credentials are logged. Run without -race for timing.
func TestModelOptimizationResources(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	loc, e := time.LoadLocation("Asia/Shanghai")
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().In(loc)
	seedDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	for i := 1; i <= 30; i++ {
		acceptIngest(t, f, f.p, stackRun(t, f, syncing.NewID(), seedDay.AddDate(0, 0, -2*i).Add(8*time.Hour), "11.0", steadyHR))
		o := processIngest(t, f.s)
		if o.Status != "committed" {
			t.Fatal("synthetic ingestion failed")
		}
	}
	today, _ := runStackDay(t, f, nil, stackDayOptions{})
	for _, cold := range []bool{false, true} {
		var durations []time.Duration
		var allocation, peak uint64
		n := 30
		if cold {
			n = 10
		}
		for i := -2; i < n; i++ {
			if cold {
				if _, e := f.s.Pool.Exec(ctx, `DELETE FROM model_workout_results WHERE dataset_id=$1 AND stack_id=$2`, f.p.DatasetID, modelmanifest.StackID()); e != nil {
					t.Fatal(e)
				}
			}
			if _, e := f.s.Pool.Exec(ctx, `SELECT rai_schedule_model_day($1,$2,$3::date,clock_timestamp())`, f.p.DatasetID, modelmanifest.StackID(), today.Format(time.DateOnly)); e != nil {
				t.Fatal(e)
			}
			lease, e := f.s.Claim(ctx, []string{modelmanifest.StackJobKind})
			if e != nil {
				t.Fatal(e)
			}
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			stop := make(chan struct{})
			done := make(chan uint64, 1)
			go func() {
				maxHeap := before.HeapAlloc
				ticker := time.NewTicker(time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-stop:
						done <- maxHeap
						return
					case <-ticker.C:
						var s runtime.MemStats
						runtime.ReadMemStats(&s)
						if s.HeapAlloc > maxHeap {
							maxHeap = s.HeapAlloc
						}
					}
				}
			}()
			started := time.Now()
			status, e := f.s.ProcessModelStack(ctx, lease, time.Now())
			elapsed := time.Since(started)
			close(stop)
			maxHeap := <-done
			runtime.ReadMemStats(&after)
			if e != nil || status != "SUCCEEDED" {
				t.Fatal(status, e)
			}
			if i >= 0 {
				durations = append(durations, elapsed)
				allocation += after.TotalAlloc - before.TotalAlloc
				if maxHeap > peak {
					peak = maxHeap
				}
			}
		}
		sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
		t.Logf("MODEL_STACK_RESOURCE cold=%t runs=30 n=%d p50_ms=%.3f p95_ms=%.3f alloc_mean_bytes=%d sampled_peak_heap_bytes=%d", cold, n, float64(durations[(n-1)/2])/float64(time.Millisecond), float64(durations[(n*95+99)/100-1])/float64(time.Millisecond), allocation/uint64(n), peak)
		if durations[len(durations)-1] > 5*time.Second {
			t.Fatal(fmt.Sprintf("bounded stack exceeded 5s: cold=%t", cold))
		}
	}
}
