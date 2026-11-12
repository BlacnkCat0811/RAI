package trainingload

import (
	"fmt"
	"math"
	"math/rand"
	"slices"
	"testing"
	"time"

	wf "rai/server/internal/workoutfeatures"
)

// watchTrace replaces the interval fixture with Apple Watch style instant HR
// points every 5 s. The first point arrives 3 s after the workout starts, as on
// a real Watch, so the trace can never cover the active interval exactly.
// hr returns the value of point i and whether the point exists.
func watchTrace(seconds int, hr func(i int) (float64, bool)) wf.RawWorkoutSnapshot {
	s := fixture(seconds, 0)
	s.Quantities = nil
	i := 0
	for at := 3; at < seconds; at += 5 {
		v, ok := hr(i)
		i++
		if !ok {
			continue
		}
		stamp := s.Start + int64(at)*1e6
		s.Quantities = append(s.Quantities, wf.Quantity{Fact: wf.FactRef{Kind: "quantity", ID: fmt.Sprint("hr", at), State: wf.Present}, Type: wf.HR, Start: stamp, End: stamp, Value: v, Unit: "count/min", Source: s.Provenance[0].Epoch, Support: wf.InstantPoint})
	}
	return s
}

func steady(v float64) func(int) (float64, bool) { return func(int) (float64, bool) { return v, true } }

func reference(rest, max float64) *Reference {
	return &Reference{ID: "synthetic-reference", Hash: Hash("parameter"), RestHR: rest, MaxHR: max, MeasuredAt: 1, RecordedAt: 1, Provenance: "explicit synthetic reference"}
}

func boundedLoad(t *testing.T, s wf.RawWorkoutSnapshot, ref *Reference) Result {
	t.Helper()
	r, err := CalculateTrainingLoadV2(V2Input{WorkoutInput: input(t, s), Reference: ref}, 1)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// E1: real Watch sampling must not be downgraded just because the first
// sample arrives a few seconds late.
func TestBoundedWatchSamplingIsComplete(t *testing.T) {
	s := watchTrace(3600, steady(150))
	r := boundedLoad(t, s, reference(50, 190))
	if r.Load == nil || r.Quality != "complete" || r.QualityTier != TierComplete {
		t.Fatalf("watch trace %s/%s reasons=%v", r.Quality, r.QualityTier, r.ReasonCodes)
	}
	close(t, *r.Load, 60*Intensity(100.0/140), 1e-9)
	if r.Method != "hrr_integral_gap_scaled" || r.Confidence >= 1 {
		t.Fatalf("scaled support must stay visible: %s %v", r.Method, r.Confidence)
	}
	// The frozen v1 candidate keeps its own semantics for the same facts.
	v1, err := CalculateTrainingLoad(input(t, s), 1)
	if err != nil || v1.Quality != "partial" || v1.QualityTier != "" {
		t.Fatalf("v1 changed: %v %s %q", err, v1.Quality, v1.QualityTier)
	}
}

// E2: a finish sprint above the HRmax reference is dosed at HRR 1, not dropped.
func TestBoundedAboveMaxIsClamped(t *testing.T) {
	sprint := func(i int) (float64, bool) {
		if i >= 600 {
			return 175 + float64(i-600)*.15, true
		}
		return 150, true
	}
	s := watchTrace(3600, sprint)
	r := boundedLoad(t, s, reference(50, 190))
	if r.Load == nil || r.AboveMaxSeconds <= 0 || !slices.Contains(r.ReasonCodes, "hr_above_reference_max") {
		t.Fatalf("sprint %+v", r.ReasonCodes)
	}
	easy := boundedLoad(t, watchTrace(3600, steady(150)), reference(50, 190))
	if *r.Load <= *easy.Load {
		t.Fatal("a harder finish produced less load")
	}
	// Dosing above-max time at HRR 1 is bounded by dosing the whole sprint at 1.
	capped := boundedLoad(t, watchTrace(3600, func(i int) (float64, bool) {
		if i >= 600 {
			return 190, true
		}
		return 150, true
	}), reference(50, 190))
	if *r.Load > *capped.Load+1e-9 {
		t.Fatalf("clamp exceeded HRR 1: %v > %v", *r.Load, *capped.Load)
	}
	// The frozen v1 candidate keeps zero tolerance against the same reference.
	in := input(t, s)
	in.Exposure.Parameter.MaxHR = 190
	v1, err := CalculateTrainingLoad(in, 1)
	if err != nil || v1.Load != nil || !slices.Contains(v1.ReasonCodes, "parameter_conflict") {
		t.Fatalf("v1 conflict semantics changed: %v %v", err, v1.ReasonCodes)
	}
}

// E3: a warm-up below the resting reference is not an input error.
func TestBoundedBelowRestIsClamped(t *testing.T) {
	warmup := func(i int) (float64, bool) {
		if i < 12 {
			return 45 + float64(i)*2.5, true
		}
		if i < 40 {
			return 75 + float64(i-12)*2.7, true
		}
		return 150, true
	}
	s := watchTrace(3600, warmup)
	r := boundedLoad(t, s, reference(50, 190))
	if r.Load == nil || r.BelowRestSeconds <= 0 || !slices.Contains(r.ReasonCodes, "hr_below_reference_rest") {
		t.Fatalf("warm-up %+v", r)
	}
	in := input(t, s)
	in.Exposure.Parameter.RestHR = 50
	if _, err := CalculateTrainingLoad(in, 1); err == nil {
		t.Fatal("v1 below-rest semantics changed")
	}
}

// E4/E5: dropouts move the trace between tiers instead of discarding it.
func TestBoundedGapTiers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		missing int // consecutive 5 s points removed
		tier    string
	}{{"20s", 3, TierComplete}, {"40s", 7, TierEstimated}, {"200s", 39, TierUnknown}} {
		t.Run(tc.name, func(t *testing.T) {
			s := watchTrace(3600, func(i int) (float64, bool) { return 150, i < 400 || i >= 400+tc.missing })
			r := boundedLoad(t, s, reference(50, 190))
			if r.QualityTier != tc.tier {
				t.Fatalf("tier=%s want %s gap=%v coverage=%v", r.QualityTier, tc.tier, r.MaxGap, r.Coverage)
			}
			if (r.Load == nil) != (tc.tier == TierUnknown) {
				t.Fatalf("load availability %v for %s", r.Load, tc.tier)
			}
			if tc.tier == TierEstimated && r.Quality != "partial" {
				t.Fatal("estimated must stay partial")
			}
		})
	}
}

func TestClampedIntegralOracle(t *testing.T) {
	for _, x := range [][2]float64{{.2, .9}, {.65, .65}, {-.3, .7}, {.7, -.3}, {0, 1}} {
		load, above, below := ClampedIntegral(x[0], x[1], 600)
		close(t, load, LinearIntegral(x[0], x[1], 600), 1e-9)
		if above != 0 {
			t.Fatalf("%v above=%v", x, above)
		}
		if math.Min(x[0], x[1]) < 0 {
			want := 600 * -math.Min(x[0], x[1]) / math.Abs(x[1]-x[0])
			close(t, below, want, 1e-9)
		}
	}
	load, above, _ := ClampedIntegral(1.2, 1.4, 300)
	close(t, load, LinearIntegral(1, 1, 300), 1e-12)
	close(t, above, 300, 1e-12)
	load, above, _ = ClampedIntegral(.8, 1.2, 600)
	close(t, above, 300, 1e-9)
	close(t, load, LinearIntegral(.8, 1, 300)+LinearIntegral(1, 1, 300), 1e-9)
}

func ledger(days int, unknown ...int) StateInput {
	today := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	in := StateInput{DatasetID: "synthetic", Generation: 1, Timezone: "UTC", From: today.Format(time.DateOnly), Through: today.Format(time.DateOnly), HistoryFrom: today.AddDate(0, 0, -186).Format(time.DateOnly)}
	for k := 0; k < days; k++ {
		load := ptr(60)
		if slices.Contains(unknown, k) {
			load = nil
		}
		in.Loads = append(in.Loads, LedgerLoad{WorkoutID: fmt.Sprint("w", k), Day: today.AddDate(0, 0, -k).Format(time.DateOnly), Load: load, Quality: "partial"})
	}
	return in
}

// E6: one unknown Workout 100 days ago must not blank the load state.
func TestBoundedStateImputesSmallUnknownMass(t *testing.T) {
	all, err := CalculateLoadStateV2(ledger(120))
	if err != nil {
		t.Fatal(err)
	}
	r, err := CalculateLoadStateV2(ledger(120, 100))
	if err != nil {
		t.Fatal(err)
	}
	d, ref := r.Days[0], all.Days[0]
	if d.Acute.Value == nil || d.Chronic.Value == nil || d.Balance == nil || !d.Imputed || d.Quality != "partial" {
		t.Fatalf("old unknown blanked state: %+v", d)
	}
	close(t, *d.Acute.Value, *ref.Acute.Value, 1e-9)
	close(t, *d.Chronic.Value, *ref.Chronic.Value, 1e-9)
	v1, err := CalculateLoadState(ledger(120, 100))
	if err != nil || v1.Days[0].Acute.Value != nil || v1.Days[0].Imputed {
		t.Fatal("v1 state semantics changed")
	}
	// Two unknown days at the top of the acute kernel exceed its limit, but the
	// chronic kernel can still value them.
	r, err = CalculateLoadStateV2(ledger(120, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	if r.Days[0].Acute.Value != nil || r.Days[0].Chronic.Value == nil || r.Days[0].Quality != "insufficient" {
		t.Fatalf("recent unknown mass: %+v", r.Days[0])
	}
	// Nothing known in the ring: nothing to impute from.
	r, err = CalculateLoadStateV2(ledger(1, 0))
	if err != nil || r.Days[0].Chronic.Value != nil {
		t.Fatalf("imputed from nothing: %+v %v", r.Days[0], err)
	}
}

// Imputation reads only the 180-day ring, so the bounded replacement range
// used by incremental rebuilds still reproduces a fresh full rebuild exactly.
func TestBoundedStateRebuildEqualsFresh(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	loads := map[int]*float64{}
	for d := 0; d < 450; d++ {
		if rng.Intn(3) > 0 {
			loads[d] = ptr(20 + rng.Float64()*100)
		}
	}
	build := func(from, through int) []DailyState {
		in := StateInput{DatasetID: "synthetic", Generation: 1, Timezone: "UTC", From: start.AddDate(0, 0, from).Format(time.DateOnly), Through: start.AddDate(0, 0, through).Format(time.DateOnly), HistoryFrom: "2025-01-01"}
		for date, v := range loads {
			if date >= from-186 && date <= through {
				in.Loads = append(in.Loads, LedgerLoad{WorkoutID: fmt.Sprint(date), Day: start.AddDate(0, 0, date).Format(time.DateOnly), Load: v, Quality: "complete"})
			}
		}
		r, e := CalculateLoadStateV2(in)
		if e != nil {
			t.Fatal(e)
		}
		return r.Days
	}
	cache := build(0, 449)
	imputed := 0
	for event := 0; event < 60; event++ {
		d := rng.Intn(450)
		if event%2 == 0 {
			loads[d] = nil
		} else {
			loads[d] = ptr(rng.Float64() * 120)
		}
		tail := min(449, d+186)
		copy(cache[d:tail+1], build(d, tail))
		fresh := build(0, 449)
		for i := range fresh {
			a, b := cache[i], fresh[i]
			if (a.Acute.Value == nil) != (b.Acute.Value == nil) || (a.Chronic.Value == nil) != (b.Chronic.Value == nil) {
				t.Fatalf("availability differs at day %d", i)
			}
			if a.Acute.Value != nil {
				close(t, *a.Acute.Value, *b.Acute.Value, 1e-9)
			}
			if a.Chronic.Value != nil {
				close(t, *a.Chronic.Value, *b.Chronic.Value, 1e-9)
			}
			if b.Imputed {
				imputed++
			}
		}
	}
	if imputed == 0 {
		t.Fatal("property never exercised imputation")
	}
}

// Running fitness v1 retired the v60 pace approximation: an HR-less run keeps
// an unknown load instead of a load derived from an ability estimate.
func TestHRLessRunStaysUnknown(t *testing.T) {
	noHR := watchTrace(3600, func(int) (float64, bool) { return 0, false })
	noHR.Distance = ptr(3600 * 3.2)
	r := boundedLoad(t, noHR, reference(50, 190))
	if r.Load != nil || !slices.Contains(r.ReasonCodes, "missing_hr") || r.QualityTier != TierUnknown {
		t.Fatalf("HR-less load must stay unknown: %+v", r)
	}
}

// Day-by-day incremental state equals a fresh rebuild for every day, and the
// incremental path is taken whenever the past ledger did not change.
func TestIncrementalLoadStateMatchesRebuild(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	loads := map[int]*float64{}
	for d := 0; d < 420; d++ {
		switch rng.Intn(4) {
		case 0:
		case 1:
			loads[d] = nil
		default:
			loads[d] = ptr(20 + rng.Float64()*100)
		}
	}
	ledgerFor := func(day int) StateInput {
		date := start.AddDate(0, 0, day).Format(time.DateOnly)
		in := StateInput{DatasetID: "synthetic", Generation: 1, Timezone: "UTC", From: date, Through: date, HistoryFrom: "2025-01-01"}
		for d, v := range loads {
			if d >= day-186 && d <= day {
				in.Loads = append(in.Loads, LedgerLoad{WorkoutID: fmt.Sprint(d), Day: start.AddDate(0, 0, d).Format(time.DateOnly), Load: v, Quality: "complete"})
			}
		}
		return in
	}
	var cp *StateCheckpoint
	incremental := 0
	for day := 200; day < 420; day++ {
		if day == 300 {
			loads[250] = ptr(999) // an old Workout changes: next day must rebuild
		}
		r, next, used, err := CalculateLoadStateV2Incremental(ledgerFor(day), cp)
		if err != nil {
			t.Fatal(err)
		}
		if used {
			incremental++
		}
		if day == 300 && used {
			t.Fatal("changed ledger used the stale checkpoint")
		}
		fresh, err := CalculateLoadStateV2(ledgerFor(day))
		if err != nil {
			t.Fatal(err)
		}
		a, b := r.Days[0], fresh.Days[0]
		if (a.Acute.Value == nil) != (b.Acute.Value == nil) || (a.Chronic.Value == nil) != (b.Chronic.Value == nil) || (a.Trend7 == nil) != (b.Trend7 == nil) || a.Quality != b.Quality {
			t.Fatalf("day %d availability differs: %+v vs %+v", day, a, b)
		}
		for _, x := range [][2]*float64{{a.Acute.Value, b.Acute.Value}, {a.Chronic.Value, b.Chronic.Value}, {a.Trend7, b.Trend7}, {a.Balance, b.Balance}} {
			if x[0] != nil {
				close(t, *x[0], *x[1], 1e-9)
			}
		}
		cp = &next
	}
	if incremental < 200 {
		t.Fatalf("incremental path used only %d times", incremental)
	}
}
