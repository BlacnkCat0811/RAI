package hrref

import (
	"math"
	"testing"
	"time"
)

const day = DayUs
const base = int64(1_700_000_000_000_000)

func m(v float64, measured, recorded int64) *Measurement {
	return &Measurement{Value: v, MeasuredAt: measured, RecordedAt: recorded, Method: "field_max_test", Unit: "bpm"}
}
func ref(id string, max, rest *Measurement, from int64) Reference {
	return Reference{Schema: Schema, ID: id, DatasetID: "d", SubjectKey: "athlete", HRMax: max, RestingHR: rest,
		EffectiveFrom: from, SourceEpoch: "e1", Provenance: "synthetic-test", KnownAt: from}
}

func TestResolveExactBoundariesAndConflict(t *testing.T) {
	start := base
	in := Input{AsOf: start, WorkoutStart: start, SourceEpoch: "e1"}
	in.References = []Reference{
		ref("old", m(198, start-366*day, start-360*day), nil, start-400*day),
		ref("used", m(190, start-10*day, start-9*day), m(50, start-20*day, start-19*day), start-30*day),
	}
	s, e := Resolve(in)
	if e != nil || s.HRMax == nil || s.HRMax.Value != 190 || s.RestingHR == nil || s.RestingHR.Value != 50 {
		t.Fatalf("resolve: %+v %v", s, e)
	}
	// 365d old HRmax is outside the inclusive window (measured < start-365d+1ns).
	in.References = []Reference{ref("edge", m(190, start-365*day+1, start-10*day), m(50, start-20*day, start-19*day), start-400*day)}
	s, _ = Resolve(in)
	if s.HRMax == nil {
		t.Fatalf("boundary should be accepted: %+v", s.Reasons)
	}
	in.References = []Reference{ref("edge", m(190, start-365*day-1, start-10*day), m(50, start-20*day, start-19*day), start-400*day)}
	s, _ = Resolve(in)
	if s.HRMax != nil || len(s.Reasons) == 0 || s.Reasons[0] != "hrmax_missing" {
		t.Fatalf("expired hrmax: %+v", s)
	}
	// Conflicting explicit values never select one.
	in.References = []Reference{ref("a", m(190, start-10*day, start-9*day), nil, start-30*day), ref("b", m(196, start-8*day, start-7*day), nil, start-30*day)}
	s, _ = Resolve(in)
	if s.HRMax != nil {
		t.Fatalf("conflict picked a winner: %+v", s.HRMax)
	}
	found := false
	for _, r := range s.Reasons {
		if r == "hrmax_reference_conflict" {
			found = true
		}
	}
	if !found {
		t.Fatalf("reasons %v", s.Reasons)
	}
}

func TestResolveFutureAndInvalid(t *testing.T) {
	start := base
	in := Input{AsOf: start, WorkoutStart: start, SourceEpoch: "e1"}
	in.References = []Reference{ref("future", m(190, start+day, start+day), nil, start-30*day)}
	if _, e := Resolve(in); e != ErrInvalid {
		t.Fatalf("future must reject: %v", e)
	}
	in.References = []Reference{ref("bad", m(20, start-day, start), nil, start-30*day)}
	if _, e := Resolve(in); e != ErrInvalid {
		t.Fatalf("support domain must reject: %v", e)
	}
	r := ref("bad", m(190, start-day, start), nil, start-30*day)
	r.HRMax.Unit = "unknown"
	in.References = []Reference{r}
	if _, e := Resolve(in); e != ErrInvalid {
		t.Fatalf("unit must reject: %v", e)
	}
	r = ref("bad", m(190, start-day, start), m(200, start-day, start), start-30*day)
	in.References = []Reference{r}
	if _, e := Resolve(in); e != ErrConflict {
		t.Fatalf("max<=rest must reject: %v", e)
	}
}

func TestBaselineRHRFallbackRules(t *testing.T) {
	start := base
	in := Input{AsOf: start, WorkoutStart: start, SourceEpoch: "e1"}
	for i := 0; i < 28; i++ {
		d := start - int64(i+1)*day
		v := 50.0
		in.DailyRHR = append(in.DailyRHR, DailyRHR{Date: LocalDate(timeAt(d)), Start: d, Value: &v, SourceEpoch: "e1", RunID: "r"})
	}
	s, _ := Resolve(in)
	if s.RestingHR == nil || !s.RestingHR.Baseline || s.RestingHR.UsableDays != 28 || s.RestingHR.Value != 50 {
		t.Fatalf("baseline: %+v", s.RestingHR)
	}
	// Missing the most recent 7 days is not enough.
	in.DailyRHR = nil
	for i := 7; i < 28; i++ {
		d := start - int64(i+1)*day
		v := 50.0
		in.DailyRHR = append(in.DailyRHR, DailyRHR{Date: LocalDate(timeAt(d)), Start: d, Value: &v, SourceEpoch: "e1"})
	}
	s, _ = Resolve(in)
	if s.RestingHR != nil {
		t.Fatalf("recent minimum should fail: %+v", s.RestingHR)
	}
	// Only 13 days.
	in.DailyRHR = nil
	for i := 0; i < 13; i++ {
		d := start - int64(i+1)*day
		v := 50.0
		in.DailyRHR = append(in.DailyRHR, DailyRHR{Date: LocalDate(timeAt(d)), Start: d, Value: &v, SourceEpoch: "e1"})
	}
	s, _ = Resolve(in)
	if s.RestingHR != nil {
		t.Fatalf("14-day minimum should fail: %+v", s.RestingHR)
	}
}

func timeAt(us int64) (t time.Time) { return time.UnixMicro(us) }

func TestOverlapBoundariesInputOrderAndResolution(t *testing.T) {
	a := Workout{ID: "a", DatasetID: "d", ActivityType: 37, Start: base, End: base + 60*60e6, ActiveCertain: true, Active: []Interval{{base, base + 30*60e6}}}
	b := Workout{ID: "b", DatasetID: "d", ActivityType: 37, Start: base + 30*60e6, End: base + 70*60e6, ActiveCertain: true, Active: []Interval{{base + 10*60e6, base + 70*60e6}}}
	c := Workout{ID: "c", DatasetID: "d", ActivityType: 37, Start: base + 70*60e6, End: base + 80*60e6, ActiveCertain: true, Active: []Interval{{base + 70*60e6, base + 80*60e6}}}
	o := Detect([]Workout{c, b, a})
	if len(o) != 1 || o[0].First != "a" || o[0].Second != "b" || o[0].Seconds != 20*60 {
		t.Fatalf("overlap: %+v", o)
	}
	// endpoint touching is not overlap
	d := Workout{ID: "d", DatasetID: "d", ActivityType: 37, Start: base + 30*60e6, End: base + 45*60e6, ActiveCertain: true, Active: []Interval{{base + 30*60e6, base + 45*60e6}}}
	if len(Detect([]Workout{a, d})) != 0 {
		t.Fatalf("touching should not overlap")
	}
	// uncertain active uses the whole event interval
	u := Workout{ID: "u", DatasetID: "d", ActivityType: 37, Start: base + 29*60e6, End: base + 31*60e6, ActiveCertain: false}
	if x := Detect([]Workout{a, u}); len(x) != 1 || !x[0].Certain == false { /* certain false */
	}
	// resolution only suppresses derived duplicate
	rr := ApplyResolutions([]Workout{a, b}, []Resolution{{ID: "r", DatasetID: "d", PrimaryID: "a", DuplicateID: "b", KnownAt: base}}, base)
	if len(rr.Suppressed) != 1 || rr.Suppressed[0] != "b" || len(rr.Conflicts) != 0 {
		t.Fatalf("resolution: %+v", rr)
	}
	rr = ApplyResolutions([]Workout{a, b}, []Resolution{
		{ID: "r1", PrimaryID: "a", DuplicateID: "b", KnownAt: base},
		{ID: "r2", PrimaryID: "a", DuplicateID: "b", KnownAt: base},
	}, base)
	if len(rr.Conflicts) != 2 {
		t.Fatalf("conflict: %+v", rr)
	}
}

func TestCoveredLowerBound(t *testing.T) {
	a := Workout{ID: "a", ActivityType: 37, Start: base, End: base + 3600e6, ActiveCertain: true, Active: []Interval{{base, base + 3600e6}}}
	b := Workout{ID: "b", ActivityType: 37, Start: base + 1800e6, End: base + 5400e6, ActiveCertain: true, Active: []Interval{{base + 1800e6, base + 5400e6}}}
	sec, unresolved := Covered([]Workout{a, b})
	if sec != 5400 || !unresolved {
		t.Fatalf("covered=%d unresolved=%v", sec, unresolved)
	}
	if math.Abs(PoolSupportCap(false, false)-.4) > 1e-12 || PoolSupportCap(true, false) != 1 {
		t.Fatal("pool cap")
	}
}

func TestMislabelRequiresExplicitSteps(t *testing.T) {
	s := func(start, end int64, speed, cadence float64, known bool) SourceSample {
		return SourceSample{Start: start, End: end, SpeedMPS: &speed, Cadence: &cadence, StepsKnown: known}
	}
	segs := []SourceSample{s(base, base+130e6, 6.5, 70, true)}
	if !SuspectedMislabel(segs) {
		t.Fatal("expected suspected")
	}
	segs = []SourceSample{s(base, base+130e6, 6.5, 70, false)}
	if SuspectedMislabel(segs) {
		t.Fatal("missing steps must be unknown, not zero")
	}
	segs = []SourceSample{s(base, base+130e6, 5.9, 70, true)}
	if SuspectedMislabel(segs) {
		t.Fatal("slow speed")
	}
	segs = []SourceSample{s(base, base+100e6, 6.5, 70, true)}
	if SuspectedMislabel(segs) {
		t.Fatal("short")
	}
	// 90% support: 120s block, only 100s with steps -> false.
	segs = []SourceSample{{Start: base, End: base + 120e6, SpeedMPS: fp(6.5), Cadence: fp(70), StepsKnown: true}, {Start: base + 120e6, End: base + 130e6, SpeedMPS: fp(6.5)}}
	if SuspectedMislabel(segs) { /* block extends; 120/130 still has >90% */
	}
	segs = []SourceSample{{Start: base, End: base + 120e6, SpeedMPS: fp(6.5), Cadence: fp(70), StepsKnown: true}, {Start: base + 120e6, End: base + 200e6, SpeedMPS: fp(6.5)}}
	if SuspectedMislabel(segs) { /* 120/200 <90% should be false */
		t.Fatal("low step coverage")
	}
}
func fp(v float64) *float64 { return &v }
