//go:build integration && modelperf

package integration

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"rai/server/internal/analysis"
	"rai/server/internal/modelmanifest"
	"rai/server/internal/syncing"
)

func percentile(ds []time.Duration, q float64) float64 {
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := int(math.Ceil(q*float64(len(s)))) - 1
	if i < 0 {
		i = 0
	}
	return float64(s[i]) / float64(time.Millisecond)
}

// TestModelPerfRealistic measures the day release and the detail reads on
// ~120 realistic synthetic runs over 186 days. It logs timings only.
func TestModelPerfRealistic(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	seedDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	var latest string
	runs := 0
	for d := 185; d >= 0; d-- {
		if d%3 == 1 { // rest days
			continue
		}
		at := seedDay.AddDate(0, 0, -d).Add(7 * time.Hour)
		if d == 0 {
			at = now.Add(-90 * time.Minute)
			if at.Before(seedDay) {
				at = seedDay.Add(time.Minute)
			}
		}
		id := syncing.NewID()
		minutes := 40 + (d*7)%40
		if d == 0 {
			minutes = 45
		}
		acceptIngest(t, f, f.p, realisticRun(f, id, at, minutes, d))
		if o := processIngest(t, f.s); o.Status != "committed" {
			t.Fatalf("synthetic ingestion failed: %+v", o)
		}
		latest = id
		runs++
	}
	today, _ := runStackDay(t, f, nil, stackDayOptions{})
	day := today.Format(time.DateOnly)
	stack := modelmanifest.StackID()
	hash, _ := modelmanifest.CurrentHash()
	setModelChannel(t, f, "active", stack, hash)
	run := func(cold bool) (time.Duration, map[string]time.Duration) {
		if cold {
			if _, e := f.s.Pool.Exec(ctx, `DELETE FROM model_workout_results WHERE dataset_id=$1 AND stack_id=$2`, f.p.DatasetID, stack); e != nil {
				t.Fatal(e)
			}
		}
		if _, e := f.s.Pool.Exec(ctx, `SELECT rai_schedule_model_day($1,$2,$3::date,clock_timestamp())`, f.p.DatasetID, stack, day); e != nil {
			t.Fatal(e)
		}
		lease, e := f.s.Claim(ctx, []string{modelmanifest.StackJobKind})
		if e != nil {
			t.Fatal(e)
		}
		pctx, phases := analysis.WithPhases(ctx)
		started := time.Now()
		status, e := f.s.ProcessModelStack(pctx, lease, time.Now())
		if e != nil || status != "SUCCEEDED" {
			t.Fatal(status, e)
		}
		return time.Since(started), phases.Durations()
	}
	report := func(label string, n int, cold bool) {
		var ds []time.Duration
		sum := map[string]time.Duration{}
		var alloc uint64
		for i := -1; i < n; i++ {
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			d, phases := run(cold)
			runtime.ReadMemStats(&after)
			if i < 0 {
				continue
			}
			ds = append(ds, d)
			alloc += after.TotalAlloc - before.TotalAlloc
			for k, v := range phases {
				sum[k] += v
			}
		}
		keys := make([]string, 0, len(sum))
		for k := range sum {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%.1f", k, float64(sum[k])/float64(n)/float64(time.Millisecond)))
		}
		t.Logf("MODEL_PERF %s runs=%d n=%d p50_ms=%.1f p95_ms=%.1f alloc_mean_mb=%.1f phases_mean_ms[%s]", label, runs, n,
			percentile(ds, .5), percentile(ds, .95), float64(alloc)/float64(n)/1e6, strings.Join(parts, " "))
	}
	report("day_warm", 6, false)
	report("day_cold", 2, true)
	report("day_warm_after_cold", 2, false)

	read := func(label string, n int, fn func() error) {
		var ds []time.Duration
		for i := -1; i < n; i++ {
			started := time.Now()
			if err := fn(); err != nil {
				t.Fatalf("%s: %v", label, err)
			}
			if i >= 0 {
				ds = append(ds, time.Since(started))
			}
		}
		t.Logf("READ_PERF %s n=%d p50_ms=%.1f p95_ms=%.1f", label, n, percentile(ds, .5), percentile(ds, .95))
	}
	read("detail", 10, func() error { _, e := f.s.WorkoutDetail(ctx, f.p, latest); return e })
	read("series", 10, func() error { _, e := f.s.WorkoutSeries(ctx, f.p, latest); return e })
	read("freshness", 10, func() error { _, e := f.s.WorkoutFreshness(ctx, f.p, latest); return e })
	read("athlete", 10, func() error { _, e := f.s.ReadAthleteProduct(ctx, f.p, time.Now().UTC()); return e })
	read("pace_published", 10, func() error { _, e := f.s.WorkoutPaceAnalysis(ctx, f.p, latest); return e })
	// A later sync makes today dirty again: the review of a run that ended
	// before the new fact must not need a 90-day replay.
	if _, e := f.s.Pool.Exec(ctx, `SELECT rai_model_stack_invalidate($1,clock_timestamp(),clock_timestamp(),NULL)`, f.p.DatasetID); e != nil {
		t.Fatal(e)
	}
	read("pace_today_dirty", 5, func() error { _, e := f.s.WorkoutPaceAnalysis(ctx, f.p, latest); return e })
	read("athlete_today_dirty", 5, func() error { _, e := f.s.ReadAthleteProduct(ctx, f.p, time.Now().UTC()); return e })
}
