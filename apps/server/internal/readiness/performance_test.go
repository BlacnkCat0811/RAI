//go:build modelperf

package readiness

import (
	"fmt"
	"sort"
	"testing"
	"time"
)

func TestReadinessV2PerformanceP95(t *testing.T) {
	in := v2Fixture()
	base := append([]WorkoutLoad(nil), in.Loads...)
	for len(in.Loads) < MaxLoads {
		i := len(in.Loads)
		w := base[i%len(base)]
		w.ID = fmt.Sprintf("perf-%05d", i)
		in.Loads = append(in.Loads, w)
	}
	ds := make([]time.Duration, 0, 30)
	for i := -3; i < 30; i++ {
		start := time.Now()
		if _, err := CalculateV2(in); err != nil {
			t.Fatal(err)
		}
		if i >= 0 {
			ds = append(ds, time.Since(start))
		}
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	p95 := ds[(len(ds)*95+99)/100-1]
	t.Logf("MODEL_PERF readiness v2 n=30 warmups=3 fixture=%d p50=%s p95=%s max=%s", MaxLoads, ds[(len(ds)-1)/2], p95, ds[len(ds)-1])
	if p95 > 250*time.Millisecond {
		t.Fatalf("readiness p95 %s", p95)
	}
}
