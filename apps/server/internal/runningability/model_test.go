package runningability

import (
	"math"
	"testing"
)

func fixture() Input {
	v := 50.
	return Input{Dataset: "synthetic", Stack: "s", Generation: 1, AsOf: 100 * Day, VO2: VO2{Value: &v, Range: &Range{48, 52}, SupportOK: true, Stable: true, Count: 4, Status: "preliminary", EvidenceAt: 99 * Day, Source: "watch", Confidence: .6}}
}
func TestSubmaxHonestBranchesAndWidths(t *testing.T) {
	in := fixture()
	r, e := Calculate(in)
	if e != nil || r.Submax.Range == nil || r.Submax.Gamma != nil || r.Submax.Confidence > .35 {
		t.Fatal(e, r)
	}
	// The population prior does not withhold the point by itself: with a VO2
	// sensitivity width of 8% the estimate is a limited point with its range.
	if r.Submax.V60 == nil || r.Submax.Status != "limited" || r.SelectedMode != "submax_estimated" || r.Prior != "population_prior" ||
		r.Submax.Range.Low >= *r.Submax.V60 || r.Submax.Range.High <= *r.Submax.V60 || math.Abs(*r.Submax.V60-(50-3.5)*.85/12) > 1e-9 {
		t.Fatal("population prior with narrow VO2 evidence gave no point", r.Submax, r.SelectedMode)
	}
	// 20% is the boundary; a wider VO2 range is range-only, and once the
	// propagated width passes 45% nothing is shown.
	in.VO2.Range = &Range{45, 55}
	if r, _ = Calculate(in); r.Submax.V60 == nil {
		t.Fatal("20% VO2 width is inside the boundary")
	}
	in.VO2.Range = &Range{44.5, 55.5}
	if r, _ = Calculate(in); r.Submax.V60 != nil || r.Submax.Range == nil || r.Submax.Status != "experimental_range" || r.SelectedMode == "submax_estimated" {
		t.Fatal("22% VO2 width must stay range-only", r.Submax)
	}
	in.VO2.Range = &Range{43, 57}
	if r, _ = Calculate(in); r.Submax.V60 != nil || r.Submax.Range != nil || r.Submax.Status != "insufficient_data" {
		t.Fatal("28% VO2 width must show nothing", r.Submax)
	}
	in = fixture()
	in.VO2.CandidateOnly = true
	r, _ = Calculate(in)
	if r.Submax.Range != nil {
		t.Fatal("three-workout candidate bypass")
	}
	in = fixture()
	in.VO2.Count = 3
	r, _ = Calculate(in)
	if r.Submax.Range != nil {
		t.Fatal("three workouts bypass LOO")
	}
	in = fixture()
	for i := 0; i < 3; i++ {
		in.Calibrations = append(in.Calibrations, Calibration{WorkoutID: string(rune('a' + i)), At: int64(90+i) * Day, KnownAt: int64(90+i) * Day, V60: 3.3, VO2: 50, Source: "watch"})
	}
	r, e = Calculate(in)
	if e != nil || r.SelectedMode != "submax_estimated" || r.Submax.V60 == nil || r.Submax.Gamma != nil {
		t.Fatal(e, r)
	}
	r, e = Freshen(r, 130*Day)
	if e != nil || r.Submax.V60 != nil || r.SelectedMode == "submax_estimated" {
		t.Fatal("expired current choice", e, r)
	}
}
func TestSourceDisagreementAndNoAutomaticMaximum(t *testing.T) {
	in := fixture()
	v := 6.
	in.Maximal = Branch{V60: &v, Status: "available", EvidenceAt: 99 * Day}
	r, e := Calculate(in)
	if e != nil || r.SelectedMode != "maximal_supported" {
		t.Fatal(e, r)
	}
	in.VO2.Value = ptr(math.NaN())
	if _, e := Calculate(in); e == nil {
		t.Fatal("NaN accepted")
	}
}
