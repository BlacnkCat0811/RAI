package readiness

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func fixture() ReadinessSnapshot {
	at := time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC).UnixMicro()
	start := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	in := ReadinessSnapshot{Dataset: "synthetic", Epoch: "epoch", Collector: "collector", Assignment: "assignment", Generation: 1, AsOf: at, CapturedAt: at, Config: DefaultConfig("UTC"), Loads: []WorkoutLoad{}}
	in.Sync = Sync{CommittedWorkouts: 6, LastCommitAt: at}
	in.Recovery = &RecoveryEvidence{Model: RecoveryModel, RunID: "recovery", Hash: "hash", Generation: 1, AsOf: at, FreshUntil: at + 96*Hour, Status: "available", Quality: "complete", Score: ptr(80), Confidence: .5, Coverage: 1}
	for d := -42; d <= 0; d++ {
		x := start.AddDate(0, 0, d)
		in.Days = append(in.Days, Day{x.Format("2006-01-02"), x.UnixMicro(), x.AddDate(0, 0, 1).UnixMicro()})
	}
	// Five complete, nonzero days spanning >=14d; outside the short burden examples.
	for i, n := range []int{42, 38, 34, 30, 26} {
		d := in.Days[42-n]
		in.Loads = append(in.Loads, WorkoutLoad{ID: fmt.Sprint(i), End: d.Start + 8*Hour, FactHash: "f", RunID: "l", Hash: "h", Model: LoadModel, ConfigHash: "c", ParameterID: "p", AU: ptr(100), Quality: "complete", Compatible: true, Days: []DailyLoad{{d.Date, ptr(100), "complete"}}})
	}
	// Include a fresh training day to meet the explicit returning-athlete policy.
	d := in.Days[30]
	in.Loads = append(in.Loads, WorkoutLoad{ID: "fresh-baseline", End: d.Start + 8*Hour, AU: ptr(100), Model: LoadModel, Quality: "complete", Compatible: true, Days: []DailyLoad{{d.Date, ptr(100), "complete"}}})
	return in
}
func calculate(t *testing.T, in ReadinessSnapshot) Result {
	t.Helper()
	r, e := Calculate(in)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func near(t *testing.T, a, b float64) {
	t.Helper()
	if math.Abs(a-b) > 1e-10*math.Max(1, math.Abs(b)) {
		t.Fatalf("oracle mismatch: %.15g %.15g", a, b)
	}
}
func TestIndependentOracles(t *testing.T) {
	c := DefaultConfig("UTC")
	at := int64(1000 * Hour)
	for i, want := range []float64{100, 36.787944117144235, 13.53352832366127} {
		near(t, DecayedBurden([]Term{{at, 100}}, at+int64(i)*36*Hour, c), want)
	}
	for _, x := range []struct{ p, d, b, w float64 }{{100, 0, 100, 100}, {80, 0, 100, 80}, {80, 100, 100, 40}, {80, 200, 100, 20}} {
		v, e := Score(x.p, x.d, x.b)
		if e != nil {
			t.Fatal(e)
		}
		near(t, v, x.w)
	}
	if TailUpperBound(c) >= 3e-10 {
		t.Fatal("tail bound exceeds contract")
	}
	// M2's maximum valid duration and HRR=1 have AU below the proof ceiling.
	if 1440/.65*math.Exp(1.92*(1-.65)) >= c.MaxWorkoutAU {
		t.Fatal("invalid per-workout bound")
	}
}
func TestGoldenAthletes(t *testing.T) {
	for _, x := range []struct {
		name                              string
		p, load                           float64
		missing, unknown, cold, returning bool
	}{{name: "well_no_recent", p: 95}, {name: "well_heavy", p: 95, load: 200}, {name: "poor_no_recent", p: 50}, {name: "poor_heavy", p: 50, load: 200}, {name: "moderate_typical", p: 80, load: 100}, {name: "new", p: 80, cold: true}, {name: "missing_recovery", p: 80, missing: true}, {name: "unknown_workout", p: 80, unknown: true}, {name: "returning", p: 80, returning: true}} {
		t.Run(x.name, func(t *testing.T) {
			in := fixture()
			in.Recovery.Score = ptr(x.p)
			if x.missing {
				in.Recovery = nil
			}
			if x.cold {
				in.Loads = nil
			}
			if x.returning {
				in.Loads = in.Loads[:5]
			}
			if x.load > 0 || x.unknown {
				w := WorkoutLoad{ID: "today", End: in.AsOf, Model: LoadModel, Compatible: true, Quality: "complete", AU: ptr(x.load), Days: []DailyLoad{{in.Days[42].Date, ptr(x.load), "complete"}}}
				if x.unknown {
					w.AU = nil
					w.Days[0].AU = nil
				}
				in.Loads = append(in.Loads, w)
			}
			r := calculate(t, in)
			if x.missing || x.unknown || x.cold || x.returning {
				if r.Score != nil {
					t.Fatal("missing became score")
				}
				return
			}
			if r.Score == nil || r.Baseline.Value == nil {
				t.Fatal("available golden missing", r.Reasons)
			}
			near(t, *r.Score, x.p*math.Pow(.5, *r.Burden / *r.Baseline.Value))
			near(t, *in.Recovery.Score, x.p)
		})
	}
}
func TestProperties(t *testing.T) {
	rng := rand.New(rand.NewSource(6))
	for i := 0; i < 2000; i++ {
		p, d, b := rng.Float64()*100, rng.Float64()*1000, .01+rng.Float64()*500
		v, _ := Score(p, d, b)
		more, _ := Score(p, d+1, b)
		higher, _ := Score(math.Min(100, p+1), d, b)
		if v < 0 || v > p || more > v || higher < v {
			t.Fatal("monotonicity or bound")
		}
	}
	in := fixture()
	a := calculate(t, in)
	rng.Shuffle(len(in.Loads), func(i, j int) { in.Loads[i], in.Loads[j] = in.Loads[j], in.Loads[i] })
	b := calculate(t, in)
	if Hash(a) != Hash(b) {
		t.Fatal("order changes result")
	}
	at := in.AsOf
	c := in.Config
	aD := DecayedBurden([]Term{{at - 2*Hour, 50}, {at - Hour, 75}}, at, c)
	near(t, aD, 50*math.Exp(-2./36)+75*math.Exp(-1./36))
	if DecayedBurden([]Term{{at - 2*Hour, 100}}, at, c) >= DecayedBurden([]Term{{at - Hour, 100}}, at, c) {
		t.Fatal("older workout has greater burden")
	}
	near(t, DecayedBurden([]Term{{at + Hour, 100}}, at, c), 0)
	// Delete removes a complete contribution; a late arrival adds one exactly once.
	in.Loads = append(in.Loads, WorkoutLoad{ID: "late", End: at - Hour, AU: ptr(100), Model: LoadModel, Quality: "complete", Compatible: true, Days: []DailyLoad{{in.Days[42].Date, ptr(100), "complete"}}})
	late := calculate(t, in)
	if *late.Score >= *a.Score {
		t.Fatal("late workout not applied")
	}
	in.Loads = in.Loads[:len(in.Loads)-1]
	deleted := calculate(t, in)
	near(t, *deleted.Score, *a.Score)
}
func TestMissingStates(t *testing.T) {
	for _, kind := range []string{"recovery_missing", "recovery_partial", "recovery_stale", "source_conflict", "collecting_baseline", "unknown_load", "partial_load", "parameter_conflict", "unknown_history"} {
		t.Run(kind, func(t *testing.T) {
			in := fixture()
			switch kind {
			case "recovery_missing":
				in.Recovery = nil
			case "recovery_partial":
				in.Recovery.Status = "partial"
				in.Recovery.Score = nil
			case "recovery_stale":
				in.Recovery.FreshUntil = in.AsOf - 1
			case "source_conflict":
				in.Recovery.Status = "source_conflict"
			case "collecting_baseline":
				in.Loads = nil
			case "unknown_load":
				in.Loads[0].AU = nil
				in.Loads[0].Days[0].AU = nil
			case "partial_load":
				in.Loads[0].Quality = "partial"
			case "parameter_conflict":
				in.Loads[0].Compatible = false
			case "unknown_history":
				// A dataset with no committed Workout at all has no history to judge:
				// the load baseline is collecting, never a sync verdict.
				in.Loads = nil
			}
			r := calculate(t, in)
			if r.Score != nil || r.Confidence != 0 || r.Quality != "insufficient" || len(r.Reasons) == 0 {
				t.Fatal("missingness was imputed")
			}
		})
	}
}
func TestBaselineMedianCalendarAndFloor(t *testing.T) {
	in := fixture()
	b := LoadBaseline(in)
	near(t, *b.Value, 100)
	if b.KnownZeroDays != 36 || b.TrainingDays != 6 {
		t.Fatal("zero-day accounting", b)
	}
	in.Loads[0].AU = ptr(9999)
	in.Loads[0].Days[0].AU = ptr(9999)
	near(t, *LoadBaseline(in).Value, 100)
	for i := range in.Loads {
		in.Loads[i].AU = ptr(.01)
		in.Loads[i].Days[0].AU = ptr(.01)
	}
	b = LoadBaseline(in)
	if !b.FloorApplied || *b.Value != 1 || *b.Median != .01 {
		t.Fatal("floor")
	}
	// Today's 1000 AU is excluded from its own historical baseline.
	d := in.Days[42]
	in.Loads = append(in.Loads, WorkoutLoad{ID: "today", End: in.AsOf, AU: ptr(1000), Model: LoadModel, Quality: "complete", Compatible: true, Days: []DailyLoad{{d.Date, ptr(1000), "complete"}}})
	near(t, *LoadBaseline(in).Median, .01)
	// Calendar supplied by the I/O boundary handles a DST 25-hour day.
	loc, e := time.LoadLocation("America/New_York")
	if e != nil {
		t.Fatal(e)
	}
	start := time.Date(2026, 11, 4, 0, 0, 0, 0, loc)
	in = fixture()
	in.Config = DefaultConfig(loc.String())
	for i := range in.Days {
		d := start.AddDate(0, 0, i-42)
		in.Days[i] = Day{d.Format("2006-01-02"), d.UnixMicro(), d.AddDate(0, 0, 1).UnixMicro()}
	}
	in.AsOf = start.Add(8 * time.Hour).UnixMicro()
	in.CapturedAt = in.AsOf
	in.Recovery.AsOf = in.AsOf
	in.Recovery.FreshUntil = in.AsOf + Hour
	if _, e = Calculate(in); e != nil {
		t.Fatal("DST", e)
	}
}
func TestFreshenAndGoldenTimeProgression(t *testing.T) {
	in := fixture()
	r := calculate(t, in)
	old := Hash(r)
	// Independent fixed-P/B progression over 72h, regardless of evidence expiry.
	prev := 0.
	for _, h := range []int64{0, 12, 24, 36, 72} {
		d := DecayedBurden([]Term{{in.AsOf, 100}}, in.AsOf+h*Hour, in.Config)
		v, _ := Score(80, d, 100)
		if v < prev {
			t.Fatal("time progression")
		}
		prev = v
	}
	next, e := Freshen(r, in.AsOf+Hour)
	if e != nil || next.Score == nil || *next.Score < *r.Score || next.Confidence > r.Confidence {
		t.Fatal("fresh projection", e)
	}
	if Hash(r) != old {
		t.Fatal("anchor mutated")
	}
	r.Recovery.FreshUntil = in.AsOf + Hour
	expired, e := Freshen(r, in.AsOf+Hour+1)
	if e != nil || expired.Score != nil || expired.Status != "stale" {
		t.Fatal("expired recovery used")
	}
	r = calculate(t, in)
	day, e := Freshen(r, r.ValidBefore)
	if e != nil || day.Score != nil {
		t.Fatal("calendar crossed")
	}
	r = calculate(t, in)
	r.ValidBefore = in.AsOf + 100*Hour
	r.Recovery.FreshUntil = in.AsOf + 100*Hour
	// Upload evidence never gates freshness: a dataset with no new commit still
	// projects forward until the candidate's own ValidBefore, and crossing that
	// boundary is refused by candidate supersession instead.
	fwd, e := Freshen(r, in.AsOf+25*Hour)
	if e != nil || fwd.Score == nil {
		t.Fatal("sync evidence gated freshness", e)
	}
	if in.Sync.CommittedWorkouts == 0 {
		t.Fatal("fixture lost its upload evidence")
	}
}

func TestSyncEvidenceNeverGates(t *testing.T) {
	in := fixture()
	in.Sync = Sync{}
	r, e := Calculate(in)
	if e != nil || r.Score == nil || r.Status == "sync_incomplete" {
		t.Fatal("zero sync evidence gated the result", e, r.Status)
	}
	for _, reason := range r.Reasons {
		if reason == "load_sync_incomplete" || reason == "sync_incomplete" {
			t.Fatal("sync reason survived", reason)
		}
	}
}
func TestModelInputBoundaryAndInvalid(t *testing.T) {
	// No raw HRV/RHR/Sleep, Ability, VO2, plan, E7 or E42 fields in the type.
	for _, v := range []any{ReadinessSnapshot{}, WorkoutLoad{}, RecoveryEvidence{}} {
		typ := reflect.TypeOf(v)
		for _, name := range []string{"HRV", "RHR", "Sleep", "Ability", "VO2", "Plan", "Acute", "Chronic", "Pace", "Power", "Distance"} {
			if _, ok := typ.FieldByName(name); ok {
				t.Fatal("forbidden model input")
			}
		}
	}
	for _, mutate := range []func(*ReadinessSnapshot){func(in *ReadinessSnapshot) { in.Config.TauUs++ }, func(in *ReadinessSnapshot) { in.Recovery.Score = ptr(math.NaN()) }, func(in *ReadinessSnapshot) { in.Loads[0].AU = ptr(-1) }, func(in *ReadinessSnapshot) { in.Loads = append(in.Loads, in.Loads[0]) }, func(in *ReadinessSnapshot) { in.Loads[0].Days[0].AU = ptr(2) }} {
		in := fixture()
		mutate(&in)
		if _, e := Calculate(in); e == nil {
			t.Fatal("invalid accepted")
		}
	}
	in := fixture()
	for i := 0; i < MaxLoads; i++ {
		w := in.Loads[0]
		w.ID = fmt.Sprint(i + 100)
		in.Loads = append(in.Loads, w)
	}
	if _, e := Calculate(in); e != ErrLimit {
		t.Fatal("cap")
	}
}
func BenchmarkReadiness(b *testing.B) {
	for _, n := range []int{0, 1, 5, 20, 100} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			in := fixture()
			for i := 0; i < n; i++ {
				w := in.Loads[0]
				w.ID = fmt.Sprint("recent", i)
				w.End = in.AsOf - int64(i)*Hour
				in.Loads = append(in.Loads, w)
			}
			r, _ := Calculate(in)
			b.Run("load_decay", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					DecayedBurden(r.Terms, in.AsOf, in.Config)
				}
			})
			b.Run("baseline", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					LoadBaseline(in)
				}
			})
			b.Run("formula", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					_, _ = Score(80, 100, 100)
				}
			})
			b.Run("calculate", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					_, _ = Calculate(in)
				}
			})
			b.Run("serialization", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					_, _ = json.Marshal(r)
				}
			})
			b.Run("freshen", func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					_, _ = Freshen(r, in.AsOf+Hour)
				}
			})
		})
	}
}

func TestCrossMidnightAndBaselineStep(t *testing.T) {
	in := fixture()
	at := in.AsOf
	date := in.Days[42].Date
	prior := in.Days[41].Date
	w := WorkoutLoad{ID: "midnight", End: at, AU: ptr(100), Model: LoadModel, Quality: "complete", Compatible: true, Days: []DailyLoad{{prior, ptr(60), "complete"}, {date, ptr(40), "complete"}}}
	before := calculate(t, in)
	in.Loads = append(in.Loads, w)
	after := calculate(t, in)
	near(t, *after.Burden-*before.Burden, 100)
	in = fixture()
	last := 0.
	for changed := 0; changed <= len(in.Loads); changed++ {
		if changed > 0 {
			in.Loads[changed-1].AU = ptr(200)
			in.Loads[changed-1].Days[0].AU = ptr(200)
		}
		b := LoadBaseline(in)
		if *b.Value < last || *b.Value > 200 {
			t.Fatal("median step")
		}
		last = *b.Value
	}
	near(t, last, 200)
	in = fixture()
	in.Loads[0].AU = nil
	in.Loads[0].Days[0].AU = nil
	b := LoadBaseline(in)
	if b.UnknownDays != 1 || b.KnownZeroDays != 36 {
		t.Fatal("unknown day became zero")
	}
}

func TestKnownFutureWorkoutStopsProjection(t *testing.T) {
	in := fixture()
	base := calculate(t, in)
	w := in.Loads[0]
	w.ID = "future"
	w.End = in.AsOf + Hour
	w.Days = []DailyLoad{{in.Days[41].Date, w.AU, "complete"}}
	in.Loads = append(in.Loads, w)
	r := calculate(t, in)
	near(t, *r.Score, *base.Score)
	if Hash(r.Baseline) != Hash(base.Baseline) {
		t.Fatal("future workout changed historical baseline")
	}
	if r.UnknownLoads != 0 || r.ValidBefore != w.End {
		t.Fatal("future leaked into burden")
	}
	next, e := Freshen(r, w.End)
	if e != nil || next.Score != nil {
		t.Fatal("future workout boundary crossed")
	}
}
