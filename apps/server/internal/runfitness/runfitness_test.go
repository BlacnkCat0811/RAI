package runfitness

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"

	wf "rai/server/internal/workoutfeatures"
)

var testRef = &Reference{MaxHR: 190, RestHR: 50, MaxBasis: "measured", RestBasis: "user", Hash: "ref"}

func f64(v float64) *float64 { return &v }
func bptr(v bool) *bool      { return &v }

// features builds n steady 180-second segments at speed (flat, measured
// grade) and heart rate, after the warm-up, outdoors with mild weather.
func features(n int, speed, hr float64) *wf.RunFitnessFeatures {
	f := &wf.RunFitnessFeatures{Schema: wf.RunFitnessSchema, DistanceCheck: "consistent", Indoor: bptr(false), TemperatureC: f64(15), Humidity: f64(.5),
		Elapsed: float64(n*180 + 120), DistanceM: f64(speed * float64(n*180+120)), RunningSeconds: float64(n*180 + 120)}
	for i := 0; i < n; i++ {
		f.Segments = append(f.Segments, wf.RunFitnessSegment{Kind: "steady", Start: int64(120+i*180) * 1e6, End: int64(120+(i+1)*180) * 1e6, Seconds: 180,
			RunningAt: float64(120 + i*180), Speed: speed, FlatSpeed: f64(speed), Grade: f64(0), MaxAbsGrade: f64(0), HR: hr, SpeedCV: .02, MinSpeed: speed * .97,
			Coverage: 1, GradeSupport: 1, SpeedEpoch: "watch", HREpoch: "watch"})
	}
	return f
}

func single(id, day string, f *wf.RunFitnessFeatures) SingleResult {
	d, _ := time.Parse(time.DateOnly, day)
	return Single(SingleInput{WorkoutID: id, Day: day, Start: d.Add(7 * time.Hour).UnixMicro(), End: d.Add(8 * time.Hour).UnixMicro(), SourceEpoch: "watch", Features: f, Reference: testRef})
}

func TestSyntheticExampleMatchesDocument(t *testing.T) {
	r := single("w", "2026-11-01", features(4, 2.6, 141))
	if r.HR == nil || math.Abs(r.HR.A-2.8) > 1e-9 || math.Abs(r.HR.Score-48.87) > .02 {
		t.Fatalf("A/score: %+v", r.HR)
	}
	if math.Abs(r.HR.ScoreRange.Low-40.1) > .15 || math.Abs(r.HR.ScoreRange.High-57.7) > .15 {
		t.Fatalf("score sensitivity %+v", r.HR.ScoreRange)
	}
	if math.Abs(r.HR.AbilityRange.Low-23.4) > .3 || math.Abs(r.HR.AbilityRange.High-71.3) > .3 {
		t.Fatalf("ability sensitivity %+v", r.HR.AbilityRange)
	}
	if math.Abs(VTRef-3.448574) > 1e-6 || math.Abs(ARef-2.840002) > 1e-6 || math.Abs(V6Ref-3.839511) > 1e-6 {
		t.Fatalf("anchors %v %v %v", VTRef, ARef, V6Ref)
	}
}

func TestDirectionLowerHRAndFasterPaceRaiseA(t *testing.T) {
	base := single("a", "2026-11-01", features(5, 3, 150)).HR.A
	lowHR := single("b", "2026-11-01", features(5, 3, 145)).HR.A
	fast := single("c", "2026-11-01", features(5, 3.1, 150)).HR.A
	if !(lowHR > base && fast > base) {
		t.Fatal("direction", base, lowHR, fast)
	}
}

func TestShortRecordsAndSupport(t *testing.T) {
	five := single("a", "2026-11-01", features(1, 3, 150))
	if five.Status != "limited" || five.HR == nil {
		t.Fatalf("one 180s segment must be limited: %+v", five)
	}
	none := features(0, 3, 150)
	none.Elapsed = 180
	three := single("b", "2026-11-01", none)
	if three.HR != nil || three.Status != "insufficient_evidence" {
		t.Fatal("three minutes without a qualified segment must not score")
	}
	thirty := single("c", "2026-11-01", features(9, 3, 150))
	if thirty.Status != "supported" {
		t.Fatalf("30-minute steady run should be supported: %s %v", thirty.Status, thirty.Reasons)
	}
}

func TestIntensityDomains(t *testing.T) {
	// r = (HR-50)/140. 0.88 -> high edge, limited; 0.95 -> lower bound only.
	edge := single("a", "2026-11-01", features(5, 4, 50+.88*140))
	if edge.HR == nil || edge.Status != "limited" || edge.HR.Segments[0].Domain != "high_edge" {
		t.Fatalf("tempo edge %+v", edge)
	}
	above := single("b", "2026-11-01", features(5, 4.5, 50+.95*140))
	if above.HR != nil || above.HR == nil && above.Status != "insufficient_evidence" {
		t.Fatal("r>0.92 must not produce an HR A")
	}
	exceeded := single("c", "2026-11-01", features(5, 4.5, 200))
	if exceeded.HR != nil || !contains(exceeded.Reasons, "hr_reference_exceeded") {
		t.Fatal("r>1 must be isolated")
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func TestMissingHRAndConfirmedRace(t *testing.T) {
	f := features(0, 3, 0)
	f.Elapsed, f.DistanceM = 1500, f64(5000)
	r := Single(SingleInput{WorkoutID: "r", Day: "2026-11-01", Features: f, Reference: testRef, Effort: &Effort{Kind: "race"}})
	if r.HR != nil || r.Race == nil || r.Race.Status != "supported" {
		t.Fatalf("race without HR: %+v", r)
	}
	r = Single(SingleInput{WorkoutID: "x", Day: "2026-11-01", Features: f})
	if r.HR != nil || r.Race != nil || !contains(r.Reasons, "hr_reference_missing") {
		t.Fatal("no reference, no effort")
	}
}

func TestLongRunDriftDoesNotPunishDistance(t *testing.T) {
	short := features(15, 3, 145)
	long := features(30, 3, 145)
	for i := range long.Segments {
		if long.Segments[i].RunningAt > 3600 {
			long.Segments[i].HR += 6 // cardiovascular drift
		}
	}
	a := single("s", "2026-11-01", short).HR.Score
	b := single("l", "2026-11-01", long).HR.Score
	if a-b > 1.0 {
		t.Fatalf("drift lowered the long run by %.2f", a-b)
	}
}

func TestDeterministicUnderReordering(t *testing.T) {
	f := features(6, 3, 150)
	f.Segments[2].Speed, f.Segments[2].FlatSpeed = 3.2, f64(3.2)
	a, _ := json.Marshal(single("a", "2026-11-01", f))
	rev := *f
	rev.Segments = append([]wf.RunFitnessSegment(nil), f.Segments...)
	for i, j := 0, len(rev.Segments)-1; i < j; i, j = i+1, j-1 {
		rev.Segments[i], rev.Segments[j] = rev.Segments[j], rev.Segments[i]
	}
	b, _ := json.Marshal(single("a", "2026-11-01", &rev))
	var x, y SingleResult
	_ = json.Unmarshal(a, &x)
	_ = json.Unmarshal(b, &y)
	if x.HR.A != y.HR.A || x.HR.Score != y.HR.Score {
		t.Fatal("segment order changed the result")
	}
}

func days(from string, n int) []string {
	d, _ := time.Parse(time.DateOnly, from)
	out := []string{}
	for i := 0; i < n; i++ {
		out = append(out, d.AddDate(0, 0, i).Format(time.DateOnly))
	}
	return out
}

func easyHistory(n int, speed, hr float64) []SingleResult {
	var out []SingleResult
	for i, d := range days("2026-10-01", n) {
		out = append(out, single(fmt.Sprintf("w%02d", i), d, features(9, speed, hr)))
	}
	return out
}

func TestEasyRunsOnlyGiveOverallAndAerobicNotSpeed(t *testing.T) {
	obs := easyHistory(25, 3, 145)
	st := CalculateState(StateInput{DatasetID: "d", Date: "2026-10-25", Observations: obs})
	if st.Status != "established" || st.Overall.Score == nil || st.Overall.Basis == "none" {
		t.Fatalf("easy history should give an estimated overall score: %+v", st.Overall)
	}
	if st.Dimensions.Endurance.Status != "supported" || st.Dimensions.Speed.SpeedMPS != nil || st.Dimensions.Sprint.SpeedMPS != nil {
		t.Fatalf("speed/sprint must stay missing: %+v", st.Dimensions)
	}
	if st.Dimensions.Speed.Rating != nil {
		t.Fatal("prior must not be a measured speed rating")
	}
	if math.Abs(*st.ReferenceDailyScore-*st.Dimensions.Endurance.Rating) > 1e-12 {
		t.Fatal("aerobic rating must equal the reference daily score")
	}
}

func TestThirtyMinuteRunGivesInitialAerobicState(t *testing.T) {
	st := CalculateState(StateInput{DatasetID: "d", Date: "2026-10-01", Observations: easyHistory(1, 3, 145)})
	if st.Status != "initial" || st.Params.A == nil || st.Dimensions.Endurance.Status != "estimated" {
		t.Fatalf("one qualified run: %+v %+v", st.Status, st.Dimensions.Endurance)
	}
}

// Closed form: without confirmed performances the joint fit equals the
// weighted Huber location of daily A, bit for bit.
func TestJointFitWithoutRacesEqualsClosedForm(t *testing.T) {
	p := DefaultPolicy()
	xs := []float64{math.Log(2.7), math.Log(2.8), math.Log(2.75), math.Log(3.4), math.Log(2.78)}
	ws := []float64{1, .8, .6, .9, .3}
	var obs []fitObs
	for i := range xs {
		obs = append(obs, fitObs{kind: obsA, x: xs[i], w: ws[i], g: [3]float64{1, 0, 0}})
	}
	f, err := Fit(p, obs)
	if err != nil {
		t.Fatal(err)
	}
	want := huberLocation(p, xs, ws)
	if f.Theta[0] != want || f.Theta[1] != 0 || f.Theta[2] != 0 || f.BL != p.BL0 {
		t.Fatalf("joint %v vs closed form %v", f.Theta, want)
	}
}

func TestFourDimensionsAtSixtyGiveSixty(t *testing.T) {
	ratio := math.Pow(2, 10/(50*BL0))
	if math.Abs(Rating(ARef*ratio, ARef)-60) > 1e-9 {
		t.Fatal("rating slope")
	}
	s, _, _ := ScoreA(ARef * ratio)
	if math.Abs(s-60) > 1e-9 {
		t.Fatalf("neutral 60-rated runner overall %v", s)
	}
	x, _, _ := ScoreA(2.9)
	if math.Abs(x-Rating(2.9, ARef)) > 1e-9 {
		t.Fatal("score and aerobic rating differ")
	}
}

func raceObs(id, day string, meters, seconds float64) SingleResult {
	f := features(0, 0, 0)
	f.DistanceM, f.Elapsed = f64(meters), seconds
	d, _ := time.Parse(time.DateOnly, day)
	return Single(SingleInput{WorkoutID: id, Day: day, Start: d.Add(8 * time.Hour).UnixMicro(), End: d.Add(8*time.Hour + time.Duration(seconds)*time.Second).UnixMicro(), Features: f, Effort: &Effort{Kind: "race"}})
}

func TestRaceAgingIsSmoothAndDailyExitHasNoStep(t *testing.T) {
	base := easyHistory(10, 3, 145)
	race := raceObs("m", "2026-10-11", 42195, 4*3600)
	var prev *float64
	var prevBL float64
	start, _ := time.Parse(time.DateOnly, "2026-10-11")
	for i := 0; i < 100; i++ {
		day := start.AddDate(0, 0, i).Format(time.DateOnly)
		st := CalculateState(StateInput{DatasetID: "d", Date: day, Observations: append(append([]SingleResult{}, base...), race)})
		if st.Overall.RawScore == nil {
			if i < 89 {
				t.Fatalf("day %d lost the score", i)
			}
			break
		}
		if prev != nil && math.Abs(*st.Overall.RawScore-*prev) > .5 {
			t.Fatalf("day %s step %.3f", day, *st.Overall.RawScore-*prev)
		}
		if i > 0 && *st.Params.BL > prevBL+1e-9 && prevBL > 1.10 || i > 0 && *st.Params.BL < prevBL-1e-9 && prevBL < 1.10 {
			t.Fatalf("bL not monotone toward the prior on day %d: %v -> %v", i, prevBL, *st.Params.BL)
		}
		prevBL = *st.Params.BL
		prev = st.Overall.RawScore
	}
}

func TestShortTestMovesSpeedNotMarathonShape(t *testing.T) {
	base := easyHistory(12, 3, 145)
	without := CalculateState(StateInput{DatasetID: "d", Date: "2026-10-12", Observations: base})
	with := CalculateState(StateInput{DatasetID: "d", Date: "2026-10-12", Observations: append(append([]SingleResult{}, base...), raceObs("t", "2026-10-12", 1800, 360))})
	if *with.Params.V6 <= *without.Params.V6 || with.Dimensions.Speed.Status != "estimated" {
		t.Fatalf("speed test must lift v6: %v -> %v (%s)", *without.Params.V6, *with.Params.V6, with.Dimensions.Speed.Status)
	}
	if *with.Params.BL != *without.Params.BL {
		t.Fatal("a 6-minute test must not change bL")
	}
	p5 := *with.Predictions[0].Seconds / *without.Predictions[0].Seconds
	pm := *with.Predictions[3].Seconds / *without.Predictions[3].Seconds
	if !(p5 < pm) {
		t.Fatalf("5K must improve more than marathon: %v vs %v", p5, pm)
	}
}

func TestPredictionsAndZonesAreOrdered(t *testing.T) {
	st := CalculateState(StateInput{DatasetID: "d", Date: "2026-10-20", Observations: easyHistory(20, 3, 145)})
	last := 0.0
	for _, p := range st.Predictions {
		if p.Seconds == nil || *p.Seconds <= last || p.Range == nil || p.Range.Low > *p.Seconds || p.Range.High < *p.Seconds {
			t.Fatalf("prediction %+v", p)
		}
		last = *p.Seconds
	}
	c := Curve{VT: *st.Params.VT, V6: *st.Params.V6, BL: *st.Params.BL}
	for x := 360.0; x < 28000; x += 97 {
		if c.Distance(x+1) <= c.Distance(x) || c.Speed(x+1) > c.Speed(x)+1e-12 {
			t.Fatalf("curve not monotone at %v", x)
		}
	}
	var prevFast float64
	for _, z := range st.Zones.Zones[:4] {
		if z.SlowSpeed == nil || z.FastSpeed == nil || *z.FastSpeed <= *z.SlowSpeed || *z.FastPace >= *z.SlowPace {
			t.Fatalf("zone %+v", z)
		}
		if prevFast != 0 && math.Abs(*z.SlowSpeed-prevFast) > 1e-12 {
			t.Fatal("zones must be contiguous")
		}
		prevFast = *z.FastSpeed
	}
	z5 := st.Zones.Zones[4]
	if z5.Support != "prior_based" || z5.Schedulable {
		t.Fatalf("Z5 from the vT prior must be reference only: %+v", z5)
	}
	if st.Zones.Zones[5].SlowSpeed != nil {
		t.Fatal("Z6 needs direct sprint evidence")
	}
}

func TestZ5LevelsAndAnchorConflict(t *testing.T) {
	base := easyHistory(12, 3, 145)
	est := CalculateState(StateInput{DatasetID: "d", Date: "2026-10-12", Observations: append(append([]SingleResult{}, base...), raceObs("t", "2026-10-12", 1800, 360))})
	if z := est.Zones.Zones[4]; z.Support != "estimated" || !z.Schedulable || *z.FastSpeed > est.Zones.Zones[4].FastRange.Low+1e-12 {
		t.Fatalf("estimated Z5: %+v", z)
	}
	sup := CalculateState(StateInput{DatasetID: "d", Date: "2026-10-12", Observations: append(append([]SingleResult{}, base...),
		raceObs("t", "2026-10-12", 1800, 360), raceObs("u", "2026-10-05", 3300, 720))})
	if z := sup.Zones.Zones[4]; z.Support != "supported" || !z.Schedulable {
		t.Fatalf("supported Z5: %+v", z)
	}
	// A slow calibrated threshold (0.95 vT <= A) leaves Z3 empty.
	st := State{Status: "established", Freshness: "fresh", Dimensions: Dimensions{Threshold: Dimension{Status: "supported"}}}
	z := buildZones(DefaultPolicy(), &st, 3.0, Curve{VT: 3.1, V6: 3.6, BL: 1.1}, false, false)
	if z.Zones[2].Support != "conflict" || z.Zones[2].SlowSpeed != nil || z.Zones[3].SlowSpeed == nil || z.Zones[1].SlowSpeed == nil {
		t.Fatalf("anchor conflict %+v", z.Zones[2])
	}
}

func TestFreshnessAndRaceOnly(t *testing.T) {
	obs := easyHistory(5, 3, 145)
	for _, c := range []struct{ day, want string }{{"2026-10-10", "fresh"}, {"2026-10-25", "aging"}, {"2026-11-10", "stale"}, {"2026-11-20", "expired"}} {
		st := CalculateState(StateInput{DatasetID: "d", Date: c.day, Observations: obs})
		if st.Freshness != c.want {
			t.Fatalf("%s: %s", c.day, st.Freshness)
		}
	}
	st := CalculateState(StateInput{DatasetID: "d", Date: "2026-11-20", Observations: append(obs, raceObs("r", "2026-10-20", 10000, 2700))})
	if st.Freshness != "race_only" || st.Status != "preliminary" && st.Status != "race_only" {
		t.Fatalf("race only: %s %s", st.Freshness, st.Status)
	}
}

func TestSameDaySplitDoesNotAddSupport(t *testing.T) {
	one := single("a", "2026-10-01", features(10, 3, 145))
	half1 := single("b", "2026-10-01", features(5, 3, 145))
	half2 := single("c", "2026-10-01", features(5, 3, 145))
	half2.Start += 1
	s1 := CalculateState(StateInput{Date: "2026-10-01", Observations: []SingleResult{one}})
	s2 := CalculateState(StateInput{Date: "2026-10-01", Observations: []SingleResult{half1, half2}})
	if s2.Support.EffectiveWeight > s1.Support.EffectiveWeight+1e-12 || s2.Support.IndependentDays != 1 {
		t.Fatal("split runs on one day must not add support")
	}
	dup := CalculateState(StateInput{Date: "2026-10-01", Observations: []SingleResult{one, one}})
	if dup.Support.Workouts != 1 {
		t.Fatal("duplicate upload counted twice")
	}
}

func TestHotDayDoesNotRewriteAllDimensions(t *testing.T) {
	obs := easyHistory(12, 3, 145)
	hot := features(9, 2.7, 155)
	hot.TemperatureC = f64(33)
	obs = append(obs, single("hot", "2026-10-13", hot))
	before := CalculateState(StateInput{Date: "2026-10-13", Observations: obs[:12]})
	after := CalculateState(StateInput{Date: "2026-10-13", Observations: obs})
	if math.Abs(*after.Params.A / *before.Params.A - 1) > .03 {
		t.Fatalf("one hot run moved A by %.3f", *after.Params.A / *before.Params.A - 1)
	}
}

func TestImprovementRaisesAerobic(t *testing.T) {
	var obs []SingleResult
	for i, d := range days("2026-10-01", 20) {
		obs = append(obs, single(fmt.Sprintf("w%d", i), d, features(9, 3+float64(i)*.01, 145)))
	}
	a := CalculateState(StateInput{Date: "2026-10-10", Observations: obs})
	b := CalculateState(StateInput{Date: "2026-10-20", Observations: obs})
	if *b.Params.A <= *a.Params.A {
		t.Fatal("consistent efficiency gains must lift the aerobic anchor")
	}
}

func TestIndoorCannotRaiseEstablishedAbility(t *testing.T) {
	obs := easyHistory(10, 3, 145)
	in := features(9, 3.6, 145)
	in.Indoor = bptr(true)
	before := CalculateState(StateInput{Date: "2026-10-11", Observations: obs})
	after := CalculateState(StateInput{Date: "2026-10-11", Observations: append(obs, single("tm", "2026-10-11", in))})
	if *after.Params.A > *before.Params.A+1e-12 {
		t.Fatal("treadmill evidence raised the established anchor")
	}
}

// A day state is rebuilt from bounded evidence only: evidence older than the
// 90-day window or after the evaluation day changes nothing, and the input
// order changes no byte. This is what makes a bounded re-analysis equal to a
// full replay.
func TestDayStateIsBoundedAndOrderIndependent(t *testing.T) {
	obs := easyHistory(30, 3, 145)
	base := CalculateState(StateInput{DatasetID: "d", Date: "2026-10-30", Observations: obs})
	old := single("old", "2026-07-01", features(9, 4, 140))
	future := single("future", "2026-11-05", features(9, 4, 140))
	extended := CalculateState(StateInput{DatasetID: "d", Date: "2026-10-30", Observations: append(append([]SingleResult{old}, obs...), future)})
	reversed := append([]SingleResult(nil), obs...)
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	shuffled := CalculateState(StateInput{DatasetID: "d", Date: "2026-10-30", Observations: reversed})
	strip := func(s State) []byte { s.InputHash = ""; b, _ := json.Marshal(s); return b }
	if string(strip(base)) != string(strip(extended)) || string(strip(base)) != string(strip(shuffled)) {
		t.Fatal("day state depends on evidence outside its window or on input order")
	}
}

// An observed peak above the reference only suggests a review; it never
// changes the published single result.
func TestHRMaxPeakOnlySuggests(t *testing.T) {
	f := features(6, 3, 150)
	plain := single("a", "2026-11-01", f)
	g := *f
	g.PeakHR15 = f64(197)
	peaked := single("a", "2026-11-01", &g)
	if peaked.HRMaxSuggest == nil || *peaked.HRMaxSuggest != 197 || peaked.HR.A != plain.HR.A || peaked.HR.Score != plain.HR.Score {
		t.Fatal("peak must only suggest a review")
	}
}

func TestAgeEstimatedReferenceIsLimitedAndWider(t *testing.T) {
	f := features(9, 3, 145)
	measured := single("a", "2026-11-01", f)
	est := Single(SingleInput{WorkoutID: "b", Day: "2026-11-01", Features: f, Reference: &Reference{MaxHR: 190, RestHR: 50, MaxBasis: "age_estimated", RestBasis: "user", Hash: "age"}})
	if measured.Status != "supported" || est.Status != "limited" || est.HR.ScoreRange.High-est.HR.ScoreRange.Low <= measured.HR.ScoreRange.High-measured.HR.ScoreRange.Low {
		t.Fatalf("age-estimated reference: %s %+v", est.Status, est.HR.ScoreRange)
	}
}

func TestOrdinaryRunBest90sIsOnlyALowerBound(t *testing.T) {
	obs := easyHistory(10, 3, 145)
	for i := range obs {
		obs[i].Sprint90 = f64(3.1)
	}
	st := CalculateState(StateInput{Date: "2026-10-10", Observations: obs})
	sp := st.Dimensions.Sprint
	if sp.Status != "lower_bound_only" || sp.Rating != nil || st.Params.V90s != nil || sp.LowerBound == nil {
		t.Fatalf("ordinary 90s bests must not rate sprint: %+v", sp)
	}
	if z := st.Zones.Zones[5]; z.SlowSpeed != nil || z.Support == "conflict" {
		t.Fatalf("Z6 must stay empty without a sprint test: %+v", z)
	}
}
