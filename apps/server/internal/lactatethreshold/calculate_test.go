package lactatethreshold

import (
	"math"
	"math/rand"
	"slices"
	"testing"
	"time"
)

// Synthetic data only.

const vt = 3.6

var t0 = time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC)

func f(v float64) *float64 { return &v }

type stage struct {
	ratio, hr, trend float64
	tiles            int
}

// run builds one Workout: a 600 s warm-up at 0.75 vT, then the given stages,
// as adjacent 180 s windows with flat measured grade.
func run(id string, day int, stages ...stage) Workout {
	start := t0.AddDate(0, 0, day)
	w := Workout{ID: id, Day: start.Format(time.DateOnly), Start: start.UnixMicro(), SourceEpoch: "sha256:x", DistanceCheck: "consistent",
		TemperatureC: f(15), Humidity: f(60), Indoor: f2(false), Reference: &Reference{MaxHR: 190, RestHR: 50}}
	at := start.Add(120 * time.Second)
	running := 0.0
	add := func(ratio, hr, trend float64) {
		v := ratio * vt
		w.Windows = append(w.Windows, Window{Kind: "steady", Start: at.UnixMicro(), End: at.Add(180 * time.Second).UnixMicro(), Seconds: 180, RunningAt: running,
			Speed: v, FlatSpeed: f(v), MaxAbsGrade: f(.01), GradeSupport: 1, HR: hr, HRTrend: trend, SpeedCV: .01, Coverage: .98, SpeedEpoch: "s", HREpoch: "h"})
		at = at.Add(180 * time.Second)
		running += 180
	}
	for i := 0; i < 4; i++ {
		add(.75, 140, 0)
	}
	for _, s := range stages {
		for i := 0; i < s.tiles; i++ {
			add(s.ratio, s.hr, s.trend)
		}
	}
	w.End = at.UnixMicro()
	return w
}

func f2(b bool) *bool { return &b }

func anchor(basis string) *Anchor {
	return &Anchor{SpeedMPS: vt, SpeedLowMPS: vt * .98, SpeedHighMPS: vt * 1.02, Speed60MPS: vt * .96, Basis: basis, Freshness: "fresh", InputHash: "a"}
}

func input(a *Anchor, ws ...Workout) Input {
	return Input{DatasetID: "d", Date: t0.AddDate(0, 0, 20).Format(time.DateOnly), Generation: 1, AsOf: 1, Anchor: a, Workouts: ws}
}

func TestOnlyEasyRunsGiveNoThresholdHeartRate(t *testing.T) {
	var ws []Workout
	for d := 0; d < 20; d += 2 {
		ws = append(ws, run("e"+string(rune('a'+d)), d, stage{.78, 142, 0, 10}))
	}
	r := Calculate(input(anchor("estimated"), ws...))
	if r.HeartRate.BPM != nil || r.HeartRate.Basis != "collecting" || r.Pace == nil || r.Status != "estimated_proxy" {
		t.Fatalf("%+v", r.HeartRate)
	}
	if !slices.Contains(r.HeartRate.Reasons, "near_threshold_support_missing") {
		t.Fatal(r.HeartRate.Reasons)
	}
}

func TestPriorAnchorDoesNotPublishCircularHeartRate(t *testing.T) {
	r := Calculate(input(anchor("prior_based"), run("a", 10, stage{1.0, 166, 0, 6}), run("b", 18, stage{1.0, 166, 0, 6})))
	if r.HeartRate.BPM != nil || r.Status != "prior_based_proxy" || !slices.Contains(r.HeartRate.Reasons, "anchor_prior_based_hr_circular") {
		t.Fatalf("%+v", r.HeartRate)
	}
	if r.Diagnostics[0].Status != "diagnostic" || r.Diagnostics[0].BPM == nil {
		t.Fatalf("%+v", r.Diagnostics)
	}
	if r.TrainingUse.PaceEligible {
		t.Fatal("prior pace must not be eligible")
	}
}

func TestRepeatedNearPointRunsAreSupported(t *testing.T) {
	r := Calculate(input(anchor("estimated"), run("a", 5, stage{1.0, 165, 0, 6}), run("b", 12, stage{1.01, 167, 0, 6}), run("c", 19, stage{.99, 166, 0, 6})))
	h := r.HeartRate
	if h.BPM == nil || math.Abs(*h.BPM-166) > 1.5 || h.Basis != "supported" || h.Method != "near_anchor_observed" || h.Freshness != "fresh" || r.UpdateStatus != "confirmed" {
		t.Fatalf("%+v", h)
	}
	if h.ModelRange.High-h.ModelRange.Low > 12 || h.ModelRange.Low > *h.BPM || h.IndependentDays != 3 {
		t.Fatalf("%+v", h.ModelRange)
	}
	if r.TrainingUse.HeartRateEligible || !r.TrainingUse.PaceEligible {
		t.Fatalf("%+v", r.TrainingUse)
	}
}

func TestSingleDayIsPreliminary(t *testing.T) {
	r := Calculate(input(anchor("estimated"), run("a", 18, stage{1.0, 166, 0, 6})))
	if r.HeartRate.BPM == nil || r.HeartRate.Basis != "preliminary" || !slices.Contains(r.HeartRate.Reasons, "independent_days_insufficient") {
		t.Fatalf("%+v", r.HeartRate)
	}
}

func TestShortNearSupportIsNotEnough(t *testing.T) {
	// 540 s of near-anchor support (first tile is the stage settling wait).
	r := Calculate(input(anchor("estimated"), run("a", 18, stage{1.0, 166, 0, 4})))
	if r.HeartRate.BPM != nil {
		t.Fatalf("%+v", r.HeartRate)
	}
}

func TestCorrectedTierMapsToAnchor(t *testing.T) {
	b0 := .773 * 140
	x := math.Log(1.035)
	hr := 166 + b0*x
	r := Calculate(input(anchor("estimated"), run("a", 18, stage{1.035, hr, 0, 6})))
	h := r.HeartRate
	if h.BPM == nil || math.Abs(*h.BPM-166) > .5 || h.Method != "near_anchor_corrected" {
		t.Fatalf("%+v", h)
	}
	// A different true slope keeps the centre inside the range.
	r = Calculate(input(anchor("estimated"), run("a", 18, stage{1.035, 166 + 1.2*b0*x, 0, 6})))
	if r.HeartRate.ModelRange.High < 166+.2*b0*x-.5 || r.HeartRate.ModelRange.Low > 166 {
		t.Fatalf("%+v", r.HeartRate.ModelRange)
	}
}

func TestBeyondFivePercentDoesNotExtrapolate(t *testing.T) {
	r := Calculate(input(anchor("estimated"), run("a", 18, stage{1.06, 172, 0, 6})))
	if r.HeartRate.BPM != nil {
		t.Fatalf("%+v", r.HeartRate)
	}
}

func TestModerateDriftNearThresholdIsKept(t *testing.T) {
	r := Calculate(input(anchor("estimated"), run("a", 18, stage{1.0, 166, 1.3, 6})))
	if r.HeartRate.BPM == nil || !slices.Contains(r.Diagnostics[0].Reasons, "hr_drift_moderate") {
		t.Fatalf("%+v %+v", r.HeartRate, r.Diagnostics)
	}
	r = Calculate(input(anchor("estimated"), run("a", 18, stage{1.0, 166, 2, 6})))
	if r.HeartRate.BPM != nil {
		t.Fatal("2 bpm/min must be excluded")
	}
}

func TestInterpolationBracketsAnchor(t *testing.T) {
	b := 110.0
	w := run("a", 18, stage{.94, 166 + b*math.Log(.94), 0, 4}, stage{1.06, 166 + b*math.Log(1.06), 0, 4})
	r := Calculate(input(anchor("estimated"), w))
	h := r.HeartRate
	if h.BPM == nil || math.Abs(*h.BPM-166) > .5 || h.Method != "local_interpolation" {
		t.Fatalf("%+v %+v", h, r.Diagnostics)
	}
}

func timeTrial(id string, day int, tailHR float64) Workout {
	w := run(id, day, stage{1.05, tailHR - 6, 0, 3}, stage{1.04, tailHR, 0, 7})
	first := w.Windows[4].Start
	w.TimeTrial = &Span{Start: first, End: w.End}
	return w
}

func TestTimeTrialGivesHeartRateUnderPriorAnchor(t *testing.T) {
	r := Calculate(input(anchor("prior_based"), timeTrial("tt", 18, 170)))
	h := r.HeartRate
	if h.BPM == nil || math.Abs(*h.BPM-170) > .5 || h.Definition != DefinitionFieldTT || h.Basis != "preliminary" {
		t.Fatalf("%+v %+v", h, r.Diagnostics)
	}
}

func TestTimeTrialAndDailyDisagreement(t *testing.T) {
	r := Calculate(input(anchor("estimated"), run("a", 8, stage{1.0, 162, 0, 6}), timeTrial("tt", 18, 170)))
	if !slices.Contains(r.HeartRate.Reasons, "definition_disagreement") || r.HeartRate.Basis == "supported" {
		t.Fatalf("%+v", r.HeartRate)
	}
	r = Calculate(input(anchor("estimated"), run("a", 8, stage{1.0, 167, 0, 6}), timeTrial("tt", 18, 170)))
	if r.HeartRate.Basis != "supported" {
		t.Fatalf("path two: %+v", r.HeartRate)
	}
}

func TestExtremeHeatIsExcluded(t *testing.T) {
	w := run("a", 18, stage{1.0, 178, 0, 6})
	w.TemperatureC = f(33)
	r := Calculate(input(anchor("estimated"), run("b", 10, stage{1.0, 166, 0, 6}), w))
	if r.HeartRate.BPM == nil || math.Abs(*r.HeartRate.BPM-166) > .5 {
		t.Fatalf("%+v", r.HeartRate)
	}
}

func TestOrderIndependence(t *testing.T) {
	ws := []Workout{run("a", 5, stage{1.0, 165, 0, 6}), run("b", 12, stage{1.01, 167, 0, 6}), run("c", 19, stage{.99, 166, 0, 6})}
	want := Calculate(input(anchor("estimated"), ws...))
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 5; i++ {
		rng.Shuffle(len(ws), func(a, b int) { ws[a], ws[b] = ws[b], ws[a] })
		got := Calculate(input(anchor("estimated"), ws...))
		if Hash(got) != Hash(want) {
			t.Fatal("order dependent")
		}
	}
}

func TestSuddenChangeNeedsConfirmation(t *testing.T) {
	ws := []Workout{run("a", 2, stage{1.0, 158, 0, 6}), run("b", 6, stage{1.0, 158, 0, 6}), run("c", 10, stage{1.0, 159, 0, 6}), run("d", 19, stage{1.0, 175, 0, 6})}
	r := Calculate(input(anchor("estimated"), ws...))
	// The robust day aggregate either resists the single new day or the
	// change waits for confirmation; it never jumps silently.
	if math.Abs(*r.HeartRate.BPM-158.4) > 5 && r.UpdateStatus != "pending_confirmation" {
		t.Fatalf("%s %+v", r.UpdateStatus, r.HeartRate)
	}
	r = Calculate(input(anchor("estimated"), run("c", 12, stage{1.0, 158, 0, 6}), run("d", 19, stage{1.0, 172, 0, 6})))
	if r.UpdateStatus != "pending_confirmation" && r.HeartRate.Basis != "conflict" {
		t.Fatalf("%s %+v", r.UpdateStatus, r.HeartRate)
	}
	// A second agreeing day at least 48 h later confirms the change.
	r = Calculate(input(anchor("estimated"), run("c", 6, stage{1.0, 158, 0, 6}), run("d", 15, stage{1.0, 172, 0, 6}), run("e", 19, stage{1.0, 171, 0, 6})))
	if r.UpdateStatus != "confirmed" {
		t.Fatalf("%s %+v", r.UpdateStatus, r.HeartRate)
	}
}

func TestUnknownGradeIsDownweighted(t *testing.T) {
	w := run("a", 18, stage{1.0, 166, 0, 6})
	for i := range w.Windows {
		w.Windows[i].MaxAbsGrade, w.Windows[i].FlatSpeed, w.Windows[i].GradeSupport = nil, nil, 0
	}
	w2 := run("b", 9, stage{1.0, 166, 0, 6})
	for i := range w2.Windows {
		w2.Windows[i].MaxAbsGrade, w2.Windows[i].FlatSpeed, w2.Windows[i].GradeSupport = nil, nil, 0
	}
	r := Calculate(input(anchor("estimated"), w, w2))
	if r.HeartRate.BPM == nil || r.HeartRate.Basis != "preliminary" || !slices.Contains(r.HeartRate.Reasons, "known_flat_share_low") {
		t.Fatalf("%+v", r.HeartRate)
	}
	w.DistanceCheck = "unavailable"
	r = Calculate(input(anchor("estimated"), w))
	if r.HeartRate.BPM != nil {
		t.Fatal("unknown grade without distance check must not contribute")
	}
}

func TestPaceTrainingRangeIsConservative(t *testing.T) {
	r := Calculate(input(anchor("field_calibrated")))
	if r.Pace == nil || r.Pace.TrainingSpeedRange.High != vt || r.Pace.TrainingSpeedRange.Low > vt*.98 || r.Status != "field_calibrated_proxy" {
		t.Fatalf("%+v", r.Pace)
	}
	if math.Abs(r.Pace.PaceSecondsPerKm-1000/vt) > 1e-9 {
		t.Fatal("pace conversion")
	}
}

func TestMissingAnchor(t *testing.T) {
	r := Calculate(input(nil, run("a", 18, stage{1.0, 166, 0, 6})))
	if r.Status != "collecting" || r.Pace != nil || r.HeartRate.BPM != nil {
		t.Fatalf("%+v", r)
	}
}

func TestAnchorConflict(t *testing.T) {
	r := Calculate(input(&Anchor{SpeedMPS: vt, Basis: "conflict"}, run("a", 18, stage{1.0, 166, 0, 6})))
	if r.Status != "conflict" || r.Pace != nil || r.TrainingUse.PaceEligible {
		t.Fatalf("%+v", r)
	}
}

func TestOldEvidenceExpires(t *testing.T) {
	in := input(anchor("estimated"), run("a", 0, stage{1.0, 166, 0, 6}))
	in.Date = t0.AddDate(0, 0, 42).Format(time.DateOnly)
	if r := Calculate(in); r.HeartRate.BPM != nil {
		t.Fatal("age 42 must not count")
	}
	in.Date = t0.AddDate(0, 0, 30).Format(time.DateOnly)
	if r := Calculate(in); r.HeartRate.BPM == nil || r.HeartRate.Freshness != "aging" {
		t.Fatalf("%+v", r.HeartRate)
	}
}

func TestFreshenOnlyChangesTimeStatus(t *testing.T) {
	r := Calculate(input(anchor("estimated"), run("a", 18, stage{1.0, 166, 0, 6})))
	later := Freshen(r, t0.AddDate(0, 0, 18+42).Format(time.DateOnly))
	if later.HeartRate.Freshness != "expired" || *later.HeartRate.BPM != *r.HeartRate.BPM || r.HeartRate.Freshness != "fresh" {
		t.Fatalf("%+v", later.HeartRate)
	}
}
