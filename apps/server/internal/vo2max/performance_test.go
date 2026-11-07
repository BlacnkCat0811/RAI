//go:build modelperf

package vo2max

import (
	"fmt"
	"sort"
	"testing"
	"time"
)

func TestAggregatePerformanceP95(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	const maxHR, rest, hr, speed = 209., 35., 182., 134.
	const center = 3.5 + .2*speed*(maxHR-rest)/(hr-rest)
	es := make([]Evidence, MaxWorkouts)
	for i := range es {
		at := now.AddDate(0, 0, -32+i%32).Add(time.Duration(i) * time.Millisecond)
		e := ev(fmt.Sprintf("workout-%04d", i), at.Format(time.DateOnly), at.UnixMicro(), center, hr, speed/60, .9)
		e.MaxHR, e.RestHR = maxHR, rest
		es[i] = e
	}
	in := AggregateInput{DatasetID: "d", Generation: 1, AsOf: now.UnixMicro(), ComputedAt: now.UnixMicro(), ReferenceHash: "h", SourceEpoch: "e1", Evidence: es, View: "revised"}
	ds := make([]time.Duration, 0, 30)
	for i := -3; i < 30; i++ {
		start := time.Now()
		r, err := Aggregate(in)
		elapsed := time.Since(start)
		if err != nil || r.Value == nil || r.Sensitivity.Corners != 8 || !r.LOO.SupportOK || r.LOO.Folds != MaxWorkouts {
			t.Fatalf("performance fixture failed to exercise full calculation: status=%s corners=%d loo=%d err=%v", r.Status, r.Sensitivity.Corners, r.LOO.Folds, err)
		}
		if i >= 0 {
			ds = append(ds, elapsed)
		}
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	p95 := ds[(len(ds)*95+99)/100-1]
	t.Logf("MODEL_PERF vo2max n=%d warmups=3 workouts=%d corners=8 loo=%d p50=%s p95=%s max=%s", len(ds), MaxWorkouts, MaxWorkouts, ds[(len(ds)-1)/2], p95, ds[len(ds)-1])
	if p95 > 250*time.Millisecond {
		t.Fatalf("vo2max p95 %s", p95)
	}
}
