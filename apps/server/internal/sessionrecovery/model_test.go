package sessionrecovery

import (
	"math"
	"slices"
	"testing"
)

func synthetic() Input {
	b, au, l := 60., 60., 45.
	return Input{Dataset: "synthetic", Stack: "s", Generation: 1, AsOf: 200 * 24 * Hour, ValidBefore: 201 * 24 * Hour, Workouts: []Workout{{ID: "yesterday", Hash: "h", Start: 199 * 24 * Hour, End: 199*24*Hour + Hour, AU: &au, Baseline: &b, LongRef: &l, Minutes: 45}}}
}
func TestStructureAndClockProjection(t *testing.T) {
	in := synthetic()
	easy, e := Calculate(in)
	if e != nil || easy.RemainingHours == nil || *easy.RemainingHours <= 0 {
		t.Fatal("yesterday persists", e, easy)
	}
	in.Workouts[0].HighShare = .6
	in.Workouts[0].AnaerobicShare = .3
	hard, e := Calculate(in)
	if e != nil || hard.Range.High <= easy.Range.High {
		t.Fatal("equal AU must distinguish structure", e)
	}
	later, e := Freshen(hard, in.AsOf+6*Hour)
	if e != nil || math.Abs(later.Range.High-math.Max(0, hard.Range.High-6)) > .26 {
		t.Fatal("countdown", hard.Range, later.Range, e)
	}
	in.AsOf += 6 * Hour
	cold, _ := Calculate(in)
	if math.Abs(cold.Range.High-later.Range.High) > 1e-9 {
		t.Fatal("cold replay differs")
	}
	if TailBound() >= 1e-15 || hard.TailHoursError > .001 {
		t.Fatal("tail proof")
	}
	in.Workouts = append(in.Workouts, in.Workouts[0])
	if _, e := Calculate(in); e == nil {
		t.Fatal("duplicate inflated fatigue")
	}
}
func TestUnknownStructureRangeAndLimits(t *testing.T) {
	in := synthetic()
	in.Workouts[0].UnclassifiedShare = 1
	r, e := Calculate(in)
	if e != nil || r.Mode != "estimated" || r.Range.Low >= r.Range.High {
		t.Fatal(e, r)
	}
	bad := in
	bad.Workouts = slices.Clone(in.Workouts)
	bad.Workouts[0].HighShare = math.NaN()
	if _, e := Calculate(bad); e == nil {
		t.Fatal("NaN accepted")
	}
	x, e := Freshen(r, r.ValidBefore)
	if e != nil || x.RemainingHours != nil {
		t.Fatal("day boundary")
	}
}

// A run without a load baseline (a first week, or the first runs back after a
// break) withholds the result only while its worst case can still matter.
func TestOldBaselinelessRunStopsBlocking(t *testing.T) {
	in := synthetic()
	au := 60.
	old := Workout{ID: "first-week", Hash: "o", Start: 100 * 24 * Hour, End: 100*24*Hour + Hour, AU: &au, Minutes: 45}
	in.Workouts = append(in.Workouts, old)
	r, e := Calculate(in)
	if e != nil || r.Status != "available" || r.Mode != "complete" || r.RemainingHours == nil || len(r.Reasons) != 0 {
		t.Fatal("a 100-day-old run without a baseline still blocks", e, r.Status, r.Mode, r.Reasons)
	}
	recent := old
	recent.ID, recent.Start, recent.End = "last-week", 193*24*Hour, 193*24*Hour+Hour
	in.Workouts[len(in.Workouts)-1] = recent
	r, e = Calculate(in)
	if e != nil || r.Status != "insufficient_data" || r.Residual != nil || !slices.Contains(r.Reasons, "load_baseline_insufficient") {
		t.Fatal("a recent run without a baseline must withhold the result", e, r.Status, r.Reasons)
	}
}

// Missing structure or duration references on a run that has fully decayed
// do not downgrade today's result; on a recent run they do.
func TestOnlyMaterialRunsDowngradeMode(t *testing.T) {
	in := synthetic()
	b, au := 60., 60.
	old := Workout{ID: "no-hr-months-ago", Hash: "o", Start: 60 * 24 * Hour, End: 60*24*Hour + Hour, AU: &au, Baseline: &b, Minutes: 45, UnclassifiedShare: 1}
	in.Workouts = append(in.Workouts, old)
	r, e := Calculate(in)
	if e != nil || r.Mode != "complete" || len(r.Reasons) != 0 || len(r.Terms) != 2 {
		t.Fatal("a decayed unclassified run downgraded the result", e, r.Mode, r.Reasons)
	}
	in.Workouts[1].BaselineExtended = true
	in.Workouts[1].UnclassifiedShare = 0
	in.Workouts[1].Start, in.Workouts[1].End = 198*24*Hour, 198*24*Hour+Hour
	r, e = Calculate(in)
	if e != nil || r.Mode != "estimated" || !slices.Contains(r.Reasons, "load_baseline_extended") {
		t.Fatal("a recent fallback baseline must be visible", e, r.Mode, r.Reasons)
	}
}

// An unknown load is bounded by half to twice a typical day, so it stops
// widening the range within days instead of holding it open for a week.
func TestUnknownLoadIsBounded(t *testing.T) {
	in := synthetic()
	in.Workouts[0].AU = nil
	r, e := Calculate(in)
	if e != nil || r.Mode != "estimated" || !r.Terms[0].Unknown || r.UnknownBurden <= 0 {
		t.Fatal(e, r)
	}
	p, tau := r.Terms[0].Pulse, r.Terms[0].Tau
	if p.Low <= 0 || p.High >= 3 || p.Low >= p.High || tau.High > 60 {
		t.Fatal("unknown bounds", p, tau)
	}
	later, e := Freshen(Result{Schema: r.Schema, ConfigHash: r.ConfigHash, AsOf: r.AsOf, ValidBefore: r.AsOf + 200*Hour, Terms: r.Terms, Residual: r.Residual}, r.AsOf+96*Hour)
	if e != nil || later.Residual.High > .5 {
		t.Fatal("an unknown run still dominates after four days", later.Residual, e)
	}
}
