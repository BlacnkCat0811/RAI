//go:build modelperf

package performance

import (
	"sort"
	"testing"
	"time"
)

// TestPerformanceP95F06 measures the demonstrated running-ability model on the
// established synthetic fixture. It is a wall-clock gate, not a benchmark
// micro-optimization; the fixture must remain at full density.
func TestPerformanceP95F06(t *testing.T) {
	const attempts = 30
	durations := make([]time.Duration, 0, attempts)
	in := Input{Calendar: time.UTC, Timezone: "UTC", DatasetID: "synthetic", Generation: 1, AsOf: fixtureEnd.UnixMicro(), View: "revised"}
	for i := 0; i < MaxWorkouts; i++ {
		in.Features = append(in.Features, fixtureRef(i, fixtureEnd.Add(-time.Duration(MaxWorkouts-i-1)*21*time.Hour), 3, .9))
	}
	var r Result
	for i := -3; i < attempts; i++ {
		start := time.Now()
		got, err := Calculate(in)
		if i >= 0 {
			durations = append(durations, time.Since(start))
		}
		if err != nil {
			t.Fatal(err)
		}
		r = got
	}
	allocs := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_, _ = Calculate(in)
		}
	})
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p95 := durations[(attempts*95+99)/100-1]
	if p95 > 2*time.Second {
		t.Fatalf("performance P95 %s exceeds 2s; attempts=%v", p95, durations)
	}
	t.Logf("F06 performance n=%d P50=%s P95=%s max=%s workouts=%d allocs/op=%d B/op=%d score=%.6f", attempts, durations[attempts/2-1], p95, durations[attempts-1], MaxWorkouts, allocs.AllocsPerOp(), allocs.AllocedBytesPerOp(), r.Ability.Score)
}
