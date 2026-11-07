//go:build modelperf

package diagnostics

import (
	"sort"
	"testing"
	"time"
)

// TestDiagnosticsP95F06 measures durability, critical-speed and 42-day trend
// on fixed synthetic fixtures. These are research diagnostics and must stay
// bounded well below two seconds per operation.
func TestDiagnosticsP95F06(t *testing.T) {
	const attempts = 30
	base := int64(1_700_000_000_000_000)
	samples := []DurabilitySample{
		knownSample(0, 600, 3.0, 140),
		knownSample(600, 2100, 3.0, 150),
		knownSample(2100, 3600, 2.95, 155),
	}
	workouts := []CriticalSpeedEffort{
		makeCriticalEffort("w3", 180, 920, base),
		makeCriticalEffort("w5", 300, 1400, base+1_000_000),
		makeCriticalEffort("w10", 600, 2600, base+2_000_000),
	}
	history := []DurabilityResult{
		durabilityRun("a", base, 1.20, 0.040, 0.010, 3.00),
		durabilityRun("b", base+7*testDayMicros, 1.22, 0.030, 0.008, 3.05),
		durabilityRun("c", base+14*testDayMicros, 1.24, 0.020, 0.006, 3.10),
	}
	durations := make([]time.Duration, 0, attempts)
	for i := -3; i < attempts; i++ {
		start := time.Now()
		if _, err := CalculateDurability(DurabilityInput{WorkoutID: "w-durability", Samples: samples}); err != nil {
			t.Fatal(err)
		}
		if _, err := CalculateCriticalSpeed(CriticalSpeedInput{AsOf: base + 10*testDayMicros, Workouts: workouts}); err != nil {
			t.Fatal(err)
		}
		if _, err := CalculateDurabilityTrend(history, base+14*testDayMicros); err != nil {
			t.Fatal(err)
		}
		if i >= 0 {
			durations = append(durations, time.Since(start))
		}
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p95 := durations[(attempts*95+99)/100-1]
	if p95 > 2*time.Second {
		t.Fatalf("diagnostics P95 %s exceeds 2s; attempts=%v", p95, durations)
	}
	allocs := testing.AllocsPerRun(5, func() {
		_, _ = CalculateDurability(DurabilityInput{WorkoutID: "w-durability", Samples: samples})
		_, _ = CalculateCriticalSpeed(CriticalSpeedInput{AsOf: base + 10*testDayMicros, Workouts: workouts})
		_, _ = CalculateDurabilityTrend(history, base+14*testDayMicros)
	})
	t.Logf("F06 diagnostics n=%d P50=%s P95=%s max=%s allocs/op=%.1f", attempts, durations[attempts/2-1], p95, durations[attempts-1], allocs)
}
