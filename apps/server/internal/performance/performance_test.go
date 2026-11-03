package performance

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	wf "rai/server/internal/workoutfeatures"
	"sort"
	"testing"
	"time"
)

var fixtureEnd = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

func fixtureRef(id int, end time.Time, v20, gamma float64, ds ...int) FeatureRef {
	if len(ds) == 0 {
		ds = []int{1200, 1800, 2400, 3600, 5400}
	}
	start := end.Add(-90 * time.Minute).UnixMicro()
	q := wf.Qualification{Status: "conditional", Reasons: []wf.Reason{wf.EffortUnconfirmed}}
	f := wf.WorkoutFeatures{Schema: wf.Schema, Algorithm: wf.Algorithm, Version: wf.Version, StackID: wf.StackID(), DatasetID: "synthetic", WorkoutID: fmt.Sprintf("workout-%04d", id), Generation: 1, State: wf.Present, ActivityType: 37, Timing: wf.Timing{Start: start, End: end.UnixMicro()}, Qualification: wf.Qualifications{Performance: q}, Distance: wf.DistanceChoice{DifferenceRatio: ptr(0)}}
	for _, d := range ds {
		dist := v20 * 1200 * math.Pow(float64(d)/1200, gamma)
		w := &wf.Window{Start: start, End: start + int64(d)*1e6, Duration: d, Distance: dist, MeanSpeed: dist / float64(d), Coverage: 1, Quality: "supported", SourceEpoch: "synthetic-watch-epoch", Evidence: "M1-synthetic", Qualification: q}
		f.Windows = append(f.Windows, wf.WindowCandidates{Duration: d, Best: w})
	}
	return FeatureRef{Feature: f, RunID: fmt.Sprintf("feature-%04d", id), KnownAt: end.UnixMicro(), FirstReceivedAt: end.UnixMicro()}
}
func established(v20, gamma float64) Input {
	in := Input{Calendar: time.UTC, Timezone: "UTC", DatasetID: "synthetic", Generation: 1, AsOf: fixtureEnd.UnixMicro(), View: "revised"}
	for i := 0; i < 20; i++ {
		in.Features = append(in.Features, fixtureRef(i, fixtureEnd.AddDate(0, 0, -57+3*i), v20, gamma))
	}
	return in
}
func mustCalculate(t *testing.T, in Input) Result {
	t.Helper()
	r, e := Calculate(in)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func closeTo(t *testing.T, a, b, tol float64) {
	t.Helper()
	if math.Abs(a-b) > tol {
		t.Fatalf("got %.15g want %.15g tolerance %g", a, b, tol)
	}
}

func TestScoreAnchorsClippingAndMonotonic(t *testing.T) {
	for _, v := range []float64{1, 1.5, 2, 2.5, 3, 3.5, 4, 5, 6, 7} {
		t.Run(fmt.Sprint(v), func(t *testing.T) {
			raw, clipped, sat := Score(v)
			closeTo(t, raw, 100*math.Log(v/1.5)/math.Log(4), 1e-12)
			if v == 3 {
				closeTo(t, clipped, 50, 1e-12)
			}
			if v < 1.5 && (clipped != 0 || sat != "lower") {
				t.Fatal("lower saturation")
			}
			if v > 6 && (clipped != 100 || sat != "upper") {
				t.Fatal("upper saturation")
			}
		})
	}
	last := -1.0
	for i := 1; i <= 10000; i++ {
		_, s, _ := Score(float64(i) / 1000)
		if s < last {
			t.Fatal("nonmonotone")
		}
		last = s
	}
}
func TestGoldenAthletes(t *testing.T) {
	for _, tc := range []struct {
		name         string
		speed, gamma float64
		kind         string
	}{
		{"Beginner", 2, .9, "beginner"}, {"Intermediate", 3.3, .9, "normal"}, {"Advanced", 5.3, .96, "normal"},
		{"High short-speed poor endurance", 4.5, .76, "normal"}, {"Moderate speed strong endurance", 3.5, .98, "normal"},
		{"Returning", 3.3, .9, "returning"}, {"Noisy GPS athlete", 3.3, .9, "noisy"}, {"Sparse long-run evidence", 3.3, .9, "short"},
		{"Single-PB outlier athlete", 3.3, .9, "outlier"}, {"True step-improvement athlete", 3.3, .9, "step"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := established(tc.speed, tc.gamma)
			switch tc.kind {
			case "beginner":
				in.Features = in.Features[len(in.Features)-3:]
			case "returning":
				in.AsOf = fixtureEnd.AddDate(0, 0, 42).UnixMicro()
			case "noisy":
				for i := range in.Features {
					in.Features[i].Feature.Qualification.Performance = wf.Qualification{Status: "ineligible", Reasons: []wf.Reason{wf.RouteAccuracyLow}}
				}
			case "short":
				for i := range in.Features {
					in.Features[i].Feature.Windows = in.Features[i].Feature.Windows[:2]
				}
			case "outlier":
				in.Features = append(in.Features, fixtureRef(999, fixtureEnd, tc.speed*1.3, tc.gamma))
			case "step":
				for i := 0; i < 18; i++ {
					in.Features = append(in.Features, fixtureRef(100+i, fixtureEnd.AddDate(0, 0, 3+2*i), tc.speed*1.05, tc.gamma))
				}
				in.AsOf = fixtureEnd.AddDate(0, 0, 42).UnixMicro()
			}
			r := mustCalculate(t, in)
			if tc.kind == "beginner" || tc.kind == "returning" || tc.kind == "noisy" || tc.kind == "short" {
				if r.Ability != nil {
					t.Fatal("unknown ability became score", r.Status)
				}
				return
			}
			if r.Ability == nil || r.Status != "available" {
				t.Fatal("missing demonstrated value", r.Status, r.ReasonCodes)
			}
			if r.Confidence > .55 || r.MaximalSupported != "unavailable" || r.EvidenceMode != "demonstrated" {
				t.Fatal("unsupported maximal/confidence")
			}
			if tc.kind != "step" {
				want := tc.speed * math.Pow(3, tc.gamma-1)
				closeTo(t, r.Ability.V60, want, 1e-10)
				closeTo(t, r.Ability.Gamma, tc.gamma, 1e-12)
			}
			t.Logf("synthetic athlete status=%s independent=%d gamma=%.3f v60=%.6f score=%.6f confidence=%.6f", r.Status, r.Support.IndependentWorkouts, r.Ability.Gamma, r.Ability.V60, r.Ability.Score, r.Confidence)
		})
	}
}
func TestEvidenceIndependenceBestBackupAndReordering(t *testing.T) {
	in := established(3.5, .92)
	want := mustCalculate(t, in)
	for i := range in.Features {
		for j := range in.Features[i].Feature.Windows {
			w := *in.Features[i].Feature.Windows[j].Best
			w.MeanSpeed *= 2
			w.Distance *= 2
			in.Features[i].Feature.Windows[j].Backup = &w
		}
	}
	in.Features = append(in.Features, in.Features[0], in.Features[2], in.Features[2])
	for i := 0; i < 10; i++ {
		rand.New(rand.NewSource(int64(i))).Shuffle(len(in.Features), func(a, b int) { in.Features[a], in.Features[b] = in.Features[b], in.Features[a] })
		r := mustCalculate(t, in)
		if Hash(r) != Hash(want) {
			t.Fatal("duplicates/order/backup changed result")
		}
	}
	weights := map[string]float64{}
	for _, e := range want.Evidence {
		weights[e.WorkoutID] += e.Weight
	}
	for _, w := range weights {
		if w > 1+1e-12 {
			t.Fatal("correlated weight >1")
		}
	}
	conflict := in.Features[0]
	conflict.RunID = "conflicting-generation"
	in.Features = append(in.Features, conflict)
	if _, e := Calculate(in); e == nil {
		t.Fatal("ambiguous feature revision accepted")
	}
}
func TestNoiseAndConfirmationAndStepResponse(t *testing.T) {
	in := established(3.4, .91)
	base := mustCalculate(t, in)
	in.Features = append(in.Features, fixtureRef(999, fixtureEnd, 3.4*1.3, .91))
	out := mustCalculate(t, in)
	closeTo(t, out.Ability.Score, base.Ability.Score, 1e-10)
	quarantine := false
	for _, e := range out.Evidence {
		if e.WorkoutID == "workout-0999" {
			quarantine = true
			if e.Weight != 0 || e.State != "confirmation_required" {
				t.Fatal("anomaly got weight")
			}
		}
	}
	if !quarantine {
		t.Fatal("missing quarantine diagnostic")
	}
	in = established(3.4, .91)
	in.Features[len(in.Features)-1] = fixtureRef(19, fixtureEnd, 3.4*1.05, .91)
	small := mustCalculate(t, in)
	if small.Ability == nil || math.Abs(small.Ability.Score-base.Ability.Score) > 2 {
		t.Fatal("single small change dominates")
	}
	in = established(3.4, .91)
	for i := 0; i < 18; i++ {
		in.Features = append(in.Features, fixtureRef(100+i, fixtureEnd.AddDate(0, 0, 2+2*i), 3.4*1.05, .91))
	}
	in.AsOf = fixtureEnd.AddDate(0, 0, 42).UnixMicro()
	step := mustCalculate(t, in)
	if step.Ability == nil {
		t.Fatal("step failed", step.Status)
	}
	absorbed := math.Log(step.Ability.V60/base.Ability.V60) / math.Log(1.05)
	if absorbed < .6 {
		t.Fatal("step response gate", absorbed)
	}
	t.Logf("42d log-speed absorption=%.6f", absorbed)
	// A second independent anomalous workout confirms the first, as of its own day.
	in = established(3.4, .91)
	in.Features = append(in.Features, fixtureRef(999, fixtureEnd, 3.4*1.3, .91), fixtureRef(1000, fixtureEnd.AddDate(0, 0, 2), 3.4*1.3, .91))
	in.AsOf = fixtureEnd.AddDate(0, 0, 2).UnixMicro()
	r := mustCalculate(t, in)
	for _, e := range r.Evidence {
		if (e.WorkoutID == "workout-0999" || e.WorkoutID == "workout-1000") && e.State != "qualified" {
			t.Fatal("independent confirmation ignored")
		}
	}
}
func TestNoEvidenceFreshnessAndNoManufacturedTargets(t *testing.T) {
	in := established(3.4, .9)
	base := mustCalculate(t, in)
	for _, age := range []int{4, 14, 27, 28, 41, 42} {
		t.Run(fmt.Sprint(age), func(t *testing.T) {
			at := fixtureEnd.AddDate(0, 0, age).UnixMicro()
			r := Freshen(base, at)
			if age < 28 {
				if r.Ability == nil {
					t.Fatal("lost fresh value")
				}
				closeTo(t, r.Ability.Score, base.Ability.Score, 0)
			} else if r.Ability != nil {
				t.Fatal("stale current visible")
			}
			if age == 14 && r.Freshness != "aging" {
				t.Fatal("aging boundary")
			}
			if age == 42 && (r.Freshness != "expired" || r.Status != "insufficient_data") {
				t.Fatal("expiry")
			}
			if r.ValidTargetDays != base.ValidTargetDays {
				t.Fatal("daily target manufactured")
			}
			if Hash(Freshen(r, at)) != Hash(r) {
				t.Fatal("non-idempotent freshness")
			}
		})
	}
	in.AsOf = fixtureEnd.AddDate(0, 0, 4).UnixMicro()
	rebuilt := mustCalculate(t, in)
	closeTo(t, rebuilt.Ability.Score, base.Ability.Score, 0)
}
func TestFutureEvidenceAndConfirmationDoNotLeak(t *testing.T) {
	in := established(3.3, .9)
	before := mustCalculate(t, in)
	in.Features = append(in.Features, fixtureRef(100, fixtureEnd.AddDate(0, 0, 2), 5, .97))
	if Hash(mustCalculate(t, in)) != Hash(before) {
		t.Fatal("future workout leaked")
	}
	late := fixtureRef(101, fixtureEnd.AddDate(0, 0, -1), 5, .97)
	late.KnownAt = fixtureEnd.AddDate(0, 0, 1).UnixMicro()
	in.Features = append(in.Features, late)
	if Hash(mustCalculate(t, in)) != Hash(before) {
		t.Fatal("future knowledge leaked")
	}
	// Earlier event-day targets must not gain confirmation from later workouts.
	in = established(3.3, .9)
	in.Features = append(in.Features, fixtureRef(200, fixtureEnd, 3.3*1.3, .9))
	one := mustCalculate(t, in)
	in.Features = append(in.Features, fixtureRef(201, fixtureEnd.AddDate(0, 0, 2), 3.3*1.3, .9))
	in.AsOf = fixtureEnd.AddDate(0, 0, 2).UnixMicro()
	two := mustCalculate(t, in)
	for _, a := range one.Targets {
		for _, b := range two.Targets {
			if a.Day == b.Day && Hash(a) != Hash(b) {
				t.Fatal("future confirmation revised past event target")
			}
		}
	}
	// Backfill is explicitly revised; as_known cannot pretend it was available.
	in = established(3.3, .9)
	for i := range in.Features {
		in.Features[i].KnownAt = fixtureEnd.UnixMicro()
	}
	in.View = "as_known"
	if r := mustCalculate(t, in); r.Ability != nil {
		t.Fatal("late facts leaked into as_known targets")
	}
}
func TestCurveBoundariesResidualAndLOO(t *testing.T) {
	for _, g := range []float64{.7, .701, .75, .9, 1} {
		in := established(3.5, g)
		r := mustCalculate(t, in)
		if g == .7 {
			if r.Status != "curve_not_identified" || r.Ability != nil {
				t.Fatal("lower boundary published")
			}
		} else {
			if r.Ability == nil {
				t.Fatal("interior/upper rejected", g, r.Status)
			}
			closeTo(t, r.Ability.Gamma, g, 1e-12)
		}
	}
	in := established(3.5, .9)
	es, _ := Extract(in)
	ws, _ := pool(es, fixtureEnd, fixtureEnd.UnixMicro(), "revised")
	// Break the power relationship coherently across independent workouts.
	for i := range ws {
		if ws[i].Duration == 1800 || ws[i].Duration == 3600 {
			ws[i].Distance *= 1.065
		}
	}
	c, status := Fit(ws)
	if c == nil || c.MedianAbsoluteResidual <= .06 || status != "curve_not_identified" {
		t.Fatal("residual gate", status, c)
	}
	// Independent identity, weights and duration coverage remain, but one workout
	// supplies the upper envelope. The LOO gate must detect the >5% dependence.
	for i := range ws {
		ws[i].Distance = 3.5 * 1200 * math.Pow(float64(ws[i].Duration)/1200, .9)
		ws[i].Weight = .001
		if ws[i].WorkoutID == "workout-0019" {
			ws[i].Distance *= 1.06
			ws[i].Weight = 1
		}
	}
	c, status = Fit(ws)
	if c == nil || c.LOOMaxChange <= .05 || status != "unstable" {
		t.Fatal("LOO gate", status, c)
	}
}
func TestRetentionAndExplanationProperties(t *testing.T) {
	for speed := 2.0; speed < 6; speed += .25 {
		prior := -1.0
		for g := .701; g <= 1; g += .007 {
			alpha := math.Log(speed*1200) - g*math.Log(1200.0/1800)
			a := ability(alpha, g)
			closeTo(t, a.V20, speed, 1e-12)
			closeTo(t, a.Retention, math.Pow(3, g-1), 1e-12)
			closeTo(t, math.Log(a.V60), a.LogSpeedAnchor+a.LogEnduranceExtension, 2e-15)
			if a.Score < prior {
				t.Fatal("retention property")
			}
			prior = a.Score
		}
	}
}
func TestIncrementalEventReplacementEqualsBoundedRebuild(t *testing.T) {
	// A real cache holds at most 103 days of feature references. Random event
	// replacement/delete/late arrival updates that cache, compared with a fresh
	// bounded reconstruction from a separate append/revision source of truth.
	rng := rand.New(rand.NewSource(3301))
	source := map[int]FeatureRef{}
	cache := map[int]FeatureRef{}
	for i, r := range established(3.3, .93).Features {
		source[i] = r
		cache[i] = r
	}
	for n := 0; n < 40; n++ {
		id := rng.Intn(30)
		if n%4 == 0 {
			delete(source, id)
			delete(cache, id)
		} else {
			ref := fixtureRef(id, fixtureEnd.AddDate(0, 0, -rng.Intn(100)), 3.3*(1+float64(rng.Intn(4))/100), .93)
			ref.Feature.Generation = int64(n + 2)
			ref.RunID = fmt.Sprint("revision-", n)
			source[id] = ref
			cache[id] = ref
		}
		in := established(3.3, .93)
		in.Features = nil
		for _, r := range cache {
			in.Features = append(in.Features, r)
		}
		incremental := mustCalculate(t, in)
		in.Features = nil
		for _, r := range source {
			if r.Feature.Timing.End >= fixtureEnd.AddDate(0, 0, -102).UnixMicro() {
				in.Features = append(in.Features, r)
			}
		}
		rebuilt := mustCalculate(t, in)
		if Hash(incremental) != Hash(rebuilt) {
			t.Fatal("cache != rebuild at event", n)
		}
	}
}
func TestSolverIndependentGridOracle(t *testing.T) {
	// Independent exhaustive pinball objective, including alpha breakpoints.
	in := established(3.3, .913)
	es, _ := Extract(in)
	ws, _ := pool(es, fixtureEnd, fixtureEnd.UnixMicro(), "revised")
	ws = ws[:15]
	for i := range ws {
		ws[i].Distance *= 1 + float64(i%3)*.007
	}
	c, status := Fit(ws)
	if c == nil || status == "collecting" {
		t.Fatal(status)
	}
	bestLoss := math.Inf(1)
	bestGamma := 0.0
	for g := 700; g <= 1000; g++ {
		gamma := float64(g) / 1000
		breaks := []float64{}
		for _, e := range ws {
			breaks = append(breaks, math.Log(e.Distance)-gamma*math.Log(float64(e.Duration)/1800))
		}
		sort.Float64s(breaks)
		for _, alpha := range breaks {
			loss := 0.0
			for _, e := range ws {
				residual := math.Log(e.Distance) - alpha - gamma*math.Log(float64(e.Duration)/1800)
				loss += e.Weight * math.Max(.8*residual, -.2*residual)
			}
			if loss < bestLoss-1e-12 || (math.Abs(loss-bestLoss) <= 1e-12 && gamma > bestGamma) {
				bestLoss = loss
				bestGamma = gamma
			}
		}
	}
	closeTo(t, c.Gamma, bestGamma, 1e-12)
	closeTo(t, c.Loss, bestLoss, 1e-12)
}
func TestTrendComparabilityAndInputLimits(t *testing.T) {
	in := established(3.3, .9)
	a := mustCalculate(t, in)
	b := a
	b.AsOf = fixtureEnd.AddDate(0, 0, -28).UnixMicro()
	if v := ComparableTrend(a, b, time.UTC); v == nil || *v != 0 {
		t.Fatal("comparable trend")
	}
	b.Version = "v2"
	if ComparableTrend(a, b, time.UTC) != nil {
		t.Fatal("cross-version trend")
	}
	for i := 20; i <= MaxWorkouts; i++ {
		in.Features = append(in.Features, fixtureRef(i, fixtureEnd, 3.3, .9))
	}
	if _, err := Calculate(in); err == nil {
		t.Fatal("unbounded evidence accepted")
	}
}

func BenchmarkPerformance(b *testing.B) {
	for _, n := range []int{10, 30, 60, 90} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			in := established(3.4, .92)
			in.Features = nil
			for i := 0; i < n; i++ {
				in.Features = append(in.Features, fixtureRef(i, fixtureEnd.AddDate(0, 0, -(n-1-i)), 3.4, .92))
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				r, err := Calculate(in)
				if err != nil {
					b.Fatal(err)
				}
				blob, _ := json.Marshal(r)
				b.ReportMetric(float64(len(blob)), "artifact-B")
			}
		})
	}
}

func BenchmarkPerformanceStages(b *testing.B) {
	in := established(3.4, .92)
	es, _ := Extract(in)
	ws, _ := pool(es, fixtureEnd, in.AsOf, in.View)
	b.Run("extraction", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, e := Extract(in); e != nil {
				b.Fatal(e)
			}
		}
	})
	b.Run("curve_fit", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, s := fitCurve(ws, false); s != "available" {
				b.Fatal(s)
			}
		}
	})
	b.Run("curve_and_LOO", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, s := fitCurve(ws, true); s != "available" {
				b.Fatal(s)
			}
		}
	})
}

func TestOutsideWindowLateArrivalCannotEnterRebuild(t *testing.T) {
	in := established(3.3, .91)
	base := mustCalculate(t, in)
	old := fixtureRef(900, fixtureEnd.AddDate(0, 0, -95), 4.5, .97)
	old.KnownAt = fixtureEnd.UnixMicro()
	old.FirstReceivedAt = fixtureEnd.UnixMicro()
	in.Features = append(in.Features, old)
	if Hash(mustCalculate(t, in)) != Hash(base) {
		t.Fatal("outside-window late arrival changed bounded reconstruction")
	}
}

func TestEasyRunsDoNotBecomeMeanRegression(t *testing.T) {
	in := established(3.3, .9)
	base := mustCalculate(t, in)
	for i := 0; i < 15; i++ {
		in.Features = append(in.Features, fixtureRef(100+i, fixtureEnd.AddDate(0, 0, -i*3), 2.7, .9))
	}
	r := mustCalculate(t, in)
	if r.Ability == nil {
		t.Fatal(r.Status)
	}
	closeTo(t, r.Ability.V60, base.Ability.V60, 1e-10)
}

func TestSaturatedOutputRetainsRawCapability(t *testing.T) {
	for _, v := range []float64{1.2, 8} {
		r := mustCalculate(t, established(v, .9))
		if r.Ability == nil {
			t.Fatal(r.Status)
		}
		a := r.Ability
		if v < 1.5 && (a.Score != 0 || a.RawScore >= 0 || a.Saturation != "lower" || a.V60 <= 0) {
			t.Fatal("lower raw capability lost")
		}
		if v > 6 && (a.Score != 100 || a.RawScore <= 100 || a.Saturation != "upper" || a.V60 <= 6) {
			t.Fatal("upper raw capability lost")
		}
	}
}

func TestM1QualificationAndShortDiagnosticsGate(t *testing.T) {
	in := established(3.3, .9)
	base := mustCalculate(t, in)
	for i := range in.Features {
		w := &wf.Window{Duration: 300, MeanSpeed: 10, Distance: 3000, Start: in.Features[i].Feature.Timing.Start, End: in.Features[i].Feature.Timing.Start + 300e6, Qualification: wf.Qualification{Status: "eligible"}}
		in.Features[i].Feature.Windows = append(in.Features[i].Feature.Windows, wf.WindowCandidates{Duration: 300, Best: w})
	}
	if Hash(mustCalculate(t, in)) != Hash(base) {
		t.Fatal("short diagnostics entered 60-minute model")
	}
	in.Features[0].Feature.Qualification.Performance = wf.Qualification{Status: "ineligible"}
	r := mustCalculate(t, in)
	if r.Support.IndependentWorkouts != 19 {
		t.Fatal("M1 entry gate ignored")
	}
	in.Features[1].Feature.Version = "unknown-version"
	if _, err := Calculate(in); err == nil {
		t.Fatal("unknown feature version masqueraded as success")
	}
}

func TestCalendarAndConfidenceQualitySupport(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	in := established(3.3, .9)
	in.Calendar = loc
	in.Timezone = loc.String()
	r := mustCalculate(t, in)
	if r.Ability == nil || r.Support.HistorySpan != 57 {
		t.Fatal("DST calendar lost span", r.Status)
	}
	priorConfidence := r.Confidence
	for i := range in.Features {
		for j := range in.Features[i].Feature.Windows {
			in.Features[i].Feature.Windows[j].Best.SourceEpoch = ""
		}
	}
	lowQuality := mustCalculate(t, in)
	if lowQuality.Ability == nil || lowQuality.Confidence >= priorConfidence {
		t.Fatal("quality support absent from confidence")
	}
	closeTo(t, lowQuality.Ability.Score, r.Ability.Score, 1e-10)
}

func TestLaterDayIsolatedPBIsInspectableWithoutNewTarget(t *testing.T) {
	in := established(3.3, .9)
	base := mustCalculate(t, in)
	in.Features = append(in.Features, fixtureRef(800, fixtureEnd.AddDate(0, 0, 2), 3.3*1.3, .9))
	in.AsOf = fixtureEnd.AddDate(0, 0, 2).UnixMicro()
	r := mustCalculate(t, in)
	if r.Ability == nil || r.AnchorDay != base.AnchorDay || r.ValidTargetDays != base.ValidTargetDays {
		t.Fatal("quarantine manufactured target")
	}
	closeTo(t, r.Ability.Score, base.Ability.Score, 0)
	found := false
	for _, e := range r.Evidence {
		if e.WorkoutID == "workout-0800" {
			found = true
			if e.Weight != 0 || e.State != "confirmation_required" {
				t.Fatal("isolated PB lost quarantine")
			}
		}
	}
	if !found {
		t.Fatal("later quarantine not inspectable")
	}
}
