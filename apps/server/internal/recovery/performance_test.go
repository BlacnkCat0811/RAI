//go:build modelperf

package recovery

import (
	"fmt"
	"sort"
	"testing"
	"time"
)

func TestRecoveryV2PerformanceP95(t *testing.T) {
	in := nightInput(t)
	base := append([]Fact(nil), in.Facts...)
	for len(in.Facts) < MaxFacts {
		i := len(in.Facts)
		f := base[i%len(base)]
		f.ID = fmt.Sprintf("perf-%05d", i)
		f.SemanticHash, f.Hash = Hash(f.ID), Hash(f.ID)
		in.Facts = append(in.Facts, f)
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
	t.Logf("MODEL_PERF recovery v2 n=30 warmups=3 fixture=%d p50=%s p95=%s max=%s", MaxFacts, ds[(len(ds)-1)/2], p95, ds[len(ds)-1])
	if p95 > 250*time.Millisecond {
		t.Fatalf("recovery p95 %s", p95)
	}
}
