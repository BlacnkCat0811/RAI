package trainingload

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	wf "rai/server/internal/workoutfeatures"
	"slices"
	"testing"
	"time"
)

func close(t *testing.T, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Fatalf("got %.15g want %.15g tolerance %g", got, want, tol)
	}
}
func oracle(a, b, seconds float64) float64 {
	// Independent composite Simpson quadrature of the definition (no primitive).
	const n = 8192
	f := func(x float64) float64 {
		r := a + (b-a)*x
		if r <= 0 {
			return 0
		}
		return r / .65 * math.Exp(1.92*(r-.65))
	}
	total := f(0) + f(1)
	for i := 1; i < n; i++ {
		w := 2.0
		if i%2 == 1 {
			w = 4
		}
		total += w * f(float64(i)/n)
	}
	return seconds / 60 * total / (3 * n)
}
func fixture(seconds int, hr float64) wf.RawWorkoutSnapshot {
	start := time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC).UnixMicro()
	end := start + int64(seconds)*1e6
	through := end + 86400e6
	source := wf.NewSource("com.apple.Workout", "apple_watch", json.RawMessage(`{"productType":"Watch-synthetic"}`))
	return wf.RawWorkoutSnapshot{DatasetID: "synthetic", WorkoutID: "synthetic-workout", Generation: 1, Revision: 1, AsOf: end + 1e6, Policy: wf.DefaultPolicy(), AlgorithmVersion: wf.Algorithm + "/" + wf.Version, Fact: wf.FactRef{Kind: "workout", ID: "synthetic", Hash: Hash("synthetic"), State: wf.Present}, Start: start, End: end, DurationUs: end - start, ActivityType: 37, WorkoutSourceEpoch: source.Epoch, Provenance: []wf.Source{source}, Parameters: []wf.Parameter{{ID: "synthetic-reference", Hash: Hash("parameter"), EffectiveFrom: start - 1e6, EffectiveThrough: &through, RecordedAt: start - 1e6, RestHR: 50, MaxHR: 200, Provenance: "explicit synthetic reference"}}, Quantities: []wf.Quantity{{Fact: wf.FactRef{Kind: "quantity", ID: "synthetic-hr", State: wf.Present}, Type: wf.HR, Start: start, End: end, Value: hr, Unit: "count/min", Source: source.Epoch, Support: wf.IntervalObservation}}}
}
func input(t testing.TB, s wf.RawWorkoutSnapshot) WorkoutInput {
	t.Helper()
	s, err := wf.Normalize(s)
	if err != nil {
		t.Fatal(err)
	}
	f, err := wf.ComputeNormalized(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	e, err := wf.BuildHRExposure(s)
	if err != nil {
		t.Fatal(err)
	}
	return WorkoutInput{Feature: f, Exposure: e, FeatureRunID: "synthetic-feature-run", Timezone: "UTC"}
}
func calc(t *testing.T, s wf.RawWorkoutSnapshot) Result {
	t.Helper()
	r, err := CalculateTrainingLoad(input(t, s), 1)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func requireLoad(t *testing.T, r Result) float64 {
	t.Helper()
	if r.Load == nil {
		t.Fatalf("missing load: %v", r.ReasonCodes)
	}
	return *r.Load
}
func TestMathOracleAndNonlinearity(t *testing.T) {
	close(t, LinearIntegral(.65, .65, 3600), 60, 1e-12)
	v := LinearIntegral(.45, .45, 1800) + LinearIntegral(.85, .85, 1800)
	close(t, v, 71.743050815, 1e-9)
	if v <= 60 {
		t.Fatal("mean HR replaced exposure")
	}
	rng := rand.New(rand.NewSource(273))
	for i := 0; i < 200; i++ {
		a, b := rng.Float64(), rng.Float64()
		if i%2 == 0 {
			b = a + 1e-12
		}
		close(t, LinearIntegral(a, b, 14400), oracle(a, b, 14400), 2e-10)
	}
	for _, v := range [][2]float64{{0, 1}, {1, 0}, {0, 0}, {-.5, 0}, {-.2, .8}, {.8, -.2}, {.65, .65 + 1e-15}} {
		got := LinearIntegral(v[0], v[1], 60)
		want := oracle(v[0], v[1], 60)
		close(t, got, want, 1e-8)
	}
}
func TestGoldenWorkouts(t *testing.T) {
	for _, tc := range []struct {
		name    string
		seconds int
		hr      float64
	}{{"easy", 1200, 125}, {"long_easy", 7200, 125}, {"tempo", 2700, 170}, {"short_sprint_like", 45, 185}, {"four_hour", 14400, 135}} {
		t.Run(tc.name, func(t *testing.T) {
			r := calc(t, fixture(tc.seconds, tc.hr))
			v := requireLoad(t, r)
			close(t, v, float64(tc.seconds)/60*Intensity((tc.hr-50)/150), 1e-9)
			if r.Method != "hrr_integral" || r.Quality != "complete" {
				t.Fatal(r.Method, r.Quality)
			}
			if tc.seconds < 180 && !slices.Contains(r.ReasonCodes, "short_session_hr_lag") {
				t.Fatal("HR lag context missing")
			}
		})
	}
	t.Run("interval", func(t *testing.T) {
		s := fixture(3600, 117.5)
		q := s.Quantities[0]
		s.Quantities[0].End = s.Start + 1800e6
		q.Start = s.Start + 1800e6
		q.Value = 177.5
		q.Fact.ID = "second"
		s.Quantities = append(s.Quantities, q)
		r := calc(t, s)
		close(t, requireLoad(t, r), 71.743050815, 1e-9)
	})
	t.Run("progression", func(t *testing.T) {
		in := input(t, fixture(3600, 147.5))
		in.Exposure.Segments[0].HR0 = 117.5
		in.Exposure.Segments[0].HR1 = 177.5
		r, e := CalculateTrainingLoad(in, 1)
		if e != nil {
			t.Fatal(e)
		}
		close(t, requireLoad(t, r), oracle(.45, .85, 3600), 1e-9)
	})
	t.Run("pause_heavy", func(t *testing.T) {
		s := fixture(600, 147.5)
		s.DurationUs = 300e6
		s.Events = []wf.Event{{Type: 1, Start: s.Start + 100e6, End: s.Start + 100e6}, {Type: 2, Start: s.Start + 400e6, End: s.Start + 400e6}}
		r := calc(t, s)
		close(t, requireLoad(t, r), 5, 1e-10)
	})
	for _, kind := range []string{"gap", "large_gap", "no_hr", "summary", "conflict", "rhr_missing", "summary_pause_unknown"} {
		t.Run(kind, func(t *testing.T) {
			s := fixture(600, 147.5)
			switch kind {
			case "gap":
				s.Quantities[0].Start += 20e6
			case "large_gap":
				s.Quantities[0].Start += 40e6
			case "no_hr":
				s.Quantities = nil
			case "summary", "summary_pause_unknown":
				s.Quantities = nil
				s.Statistics = []wf.Statistic{{Type: wf.HR, Unit: "count/min", Start: s.Start, End: s.End, Average: ptr(147.5)}}
				if kind == "summary_pause_unknown" {
					s.DurationUs -= 60e6
				}
			case "conflict":
				s.Quantities[0].Value = 201
			case "rhr_missing":
				s.Parameters = nil
			}
			r := calc(t, s)
			switch kind {
			case "gap":
				close(t, requireLoad(t, r), 10, 1e-10)
				if r.Method != "hrr_integral_gap_scaled" || r.Quality != "partial" || r.EstimatedLoad == nil {
					t.Fatal("gap lost method")
				}
			case "summary":
				close(t, requireLoad(t, r), 10, 1e-10)
				if r.Method != "summary_hr_approximation" || r.Quality != "partial" || r.Confidence > .35 {
					t.Fatal("summary disguised")
				}
			default:
				if r.Load != nil || r.Status != "insufficient_data" {
					t.Fatal("unknown became zero")
				}
			}
		})
	}
}
func TestDurationIntensitySplitMergeAndDensity(t *testing.T) {
	rng := rand.New(rand.NewSource(19))
	for i := 0; i < 400; i++ {
		a, b := rng.Float64(), rng.Float64()
		fraction := rng.Float64()
		duration := rng.Float64() * 14400
		middle := a + (b-a)*fraction
		v := LinearIntegral(a, b, duration)
		close(t, LinearIntegral(a, b, duration*2), 2*v, 1e-10)
		close(t, LinearIntegral(a, middle, duration*fraction)+LinearIntegral(middle, b, duration*(1-fraction)), v, 1e-10)
		if LinearIntegral(a+.001, b+.001, duration) < v {
			t.Fatal("intensity decreased")
		}
	}
	loads := []float64{}
	for _, step := range []int{1, 5} {
		s := fixture(600, 0)
		s.Quantities = nil
		for sec := 0; sec <= 600; sec += step {
			at := s.Start + int64(sec)*1e6
			s.Quantities = append(s.Quantities, wf.Quantity{Fact: wf.FactRef{Kind: "quantity", ID: fmt.Sprint(sec), State: wf.Present}, Type: wf.HR, Start: at, End: at, Value: 110 + float64(sec)/10, Unit: "count/min", Source: s.Provenance[0].Epoch, Support: wf.InstantPoint})
		}
		r := calc(t, s)
		loads = append(loads, requireLoad(t, r))
	}
	close(t, loads[0], loads[1], 1e-11)
	s := fixture(600, 140)
	s.Quantities[0].Start = s.Start
	s.Quantities[0].End = s.Start
	r := calc(t, s)
	if r.ValidHRSeconds != 0 || r.Load != nil {
		t.Fatal("instant manufactured duration")
	}
}
func TestMidnightDSTAndConservation(t *testing.T) {
	for _, tc := range []struct {
		date, zone string
		duration   int
	}{{"2026-11-01T23:30:00", "America/New_York", 3600}, {"2026-03-08T00:00:00", "America/New_York", 23 * 3600}, {"2026-11-01T00:00:00", "America/New_York", 24 * 3600}, {"2026-10-31T23:30:00", "Asia/Shanghai", 3600}} {
		loc, _ := time.LoadLocation(tc.zone)
		start, _ := time.ParseInLocation("2006-01-02T15:04:05", tc.date, loc)
		in := input(t, fixture(600, 147.5))
		in.Timezone = tc.zone
		in.Calendar = loc
		f := &in.Feature
		f.Timing.Start = start.UnixMicro()
		f.Timing.End = f.Timing.Start + int64(tc.duration)*1e6
		f.Timing.Active = ptr(float64(tc.duration))
		in.Exposure.Active = []wf.Interval{{Start: f.Timing.Start, End: f.Timing.End}}
		in.Exposure.Segments[0].Start = f.Timing.Start
		in.Exposure.Segments[0].End = f.Timing.End
		in.Exposure.Parameter.EffectiveFrom = f.Timing.Start - 1e6
		in.Exposure.Parameter.EffectiveThrough = nil
		r, err := CalculateTrainingLoad(in, 1)
		if err != nil {
			t.Fatal(err)
		}
		sum := 0.0
		for _, d := range r.Days {
			sum += *d.Load
		}
		close(t, sum, requireLoad(t, r), 1e-9)
		if tc.duration == 3600 && len(r.Days) != 2 {
			t.Fatal("midnight not split")
		}
		if tc.duration == 23*3600 && len(r.Days) != 1 {
			t.Fatal("23h day incorrectly split")
		}
		if tc.duration == 24*3600 && len(r.Days) != 1 {
			t.Fatal("25h day incorrectly split at 24h")
		}
	}
}
func BenchmarkWorkoutLoad(b *testing.B) {
	for _, minutes := range []int{20, 45, 120, 240} {
		b.Run(fmt.Sprint(minutes, "m"), func(b *testing.B) {
			in := input(b, fixture(minutes*60, 147.5))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := CalculateTrainingLoad(in, 1); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
func TestAsOfReferenceAndNoDoubleCounting(t *testing.T) {
	s := fixture(600, 147.5)
	original := s.Parameters[0]
	p := original
	p.ID = "future"
	p.Hash = Hash("future")
	p.EffectiveFrom = s.End + 1e6
	p.MaxHR = 210
	s.Parameters = append(s.Parameters, p)
	r := calc(t, s)
	if r.HRReference.ID != original.ID {
		t.Fatal("future reference rewrote history")
	}
	p.EffectiveFrom = s.Start - 1e6
	p.RecordedAt = s.AsOf + 1
	s.Parameters[1] = p
	r = calc(t, s)
	if r.HRReference.ID != original.ID {
		t.Fatal("future recorded parameter leaked into as-of")
	}
	p.RecordedAt = s.AsOf
	s.Parameters[1] = p
	r = calc(t, s)
	if r.Load != nil || !slices.Contains(r.ReasonCodes, "parameter_conflict") {
		t.Fatal("ambiguous parameter chosen")
	}
	p.Supersedes = original.ID
	s.Parameters[1] = p
	r = calc(t, s)
	if r.HRReference.ID != p.ID {
		t.Fatal("explicit revision not resolved")
	}
	s.Parameters = []wf.Parameter{original}
	s.Statistics = []wf.Statistic{{Type: wf.HR, Unit: "count/min", Start: s.Start, End: s.End, Average: ptr(190)}}
	r = calc(t, s)
	close(t, requireLoad(t, r), 10, 1e-12)
	if r.Method != "hrr_integral" {
		t.Fatal("summary overrode trace")
	}
}
func TestCrossMidnightGapAllocationAndUnknown(t *testing.T) {
	in := input(t, fixture(600, 147.5))
	start := time.Date(2026, 10, 31, 23, 50, 20, 0, time.UTC).UnixMicro()
	in.Feature.Timing.Start = start
	in.Feature.Timing.End = start + 600e6
	in.Exposure.Active = []wf.Interval{{Start: start, End: start + 600e6}}
	in.Exposure.Segments[0].Start = start
	in.Exposure.Segments[0].End = start + 580e6
	in.Feature.HeartRate.MaxGap = 20
	in.Exposure.Parameter.EffectiveFrom = start - 1e6
	in.Exposure.Parameter.EffectiveThrough = nil
	r, e := CalculateTrainingLoad(in, 1)
	if e != nil {
		t.Fatal(e)
	}
	close(t, requireLoad(t, r), 10, 1e-10)
	if len(r.Days) != 2 {
		t.Fatal("gap day missing")
	}
	close(t, *r.Days[1].Load, 20.0/60, 1e-10)
	close(t, *r.Days[0].Load+*r.Days[1].Load, 10, 1e-10)
	in.Exposure.Parameter = nil
	in.Exposure.ParameterReasons = []wf.Reason{wf.ParameterMissing}
	r, e = CalculateTrainingLoad(in, 1)
	if e != nil {
		t.Fatal(e)
	}
	for _, d := range r.Days {
		if d.Load != nil {
			t.Fatal("unknown day became zero")
		}
	}
}
func TestFourHourVariableExposureBoundAndRoundTrip(t *testing.T) {
	s := fixture(14400, 0)
	s.Quantities = nil
	for i := 0; i < 14400; i++ {
		s.Quantities = append(s.Quantities, wf.Quantity{Fact: wf.FactRef{Kind: "quantity", ID: fmt.Sprint(i), State: wf.Present}, Type: wf.HR, Start: s.Start + int64(i)*1e6, End: s.Start + int64(i+1)*1e6, Value: 130 + float64(i%40), Unit: "count/min", Source: s.Provenance[0].Epoch, Support: wf.IntervalObservation})
	}
	in := input(t, s)
	encoded, e := json.Marshal(in.Exposure)
	if e != nil || len(encoded) > wf.MaxExposureBytes {
		t.Fatal("4h bounded exposure", len(encoded), e)
	}
	var decoded wf.HRExposure
	if e = json.Unmarshal(encoded, &decoded); e != nil {
		t.Fatal(e)
	}
	if Hash(in.Exposure) != Hash(decoded) {
		t.Fatal("wire changed exact exposure")
	}
	in.Exposure = decoded
	r, e := CalculateTrainingLoad(in, 1)
	if e != nil || r.Load == nil {
		t.Fatal("round trip load", e, r.ReasonCodes)
	}
	t.Logf("M2_EXPOSURE four_hour_segments=%d bytes=%d", len(decoded.Segments), len(encoded))
}
func TestAllPausedWorkoutLeavesUnknownLedger(t *testing.T) {
	s := fixture(600, 147.5)
	s.DurationUs = 0
	s.Events = []wf.Event{{Type: 1, Start: s.Start, End: s.Start}, {Type: 2, Start: s.End, End: s.End}}
	r := calc(t, s)
	if r.Load != nil || len(r.Days) != 1 || r.Days[0].Load != nil || r.Days[0].ActiveSeconds == nil || *r.Days[0].ActiveSeconds != 0 {
		t.Fatal("recorded all-paused workout disappeared from unknown ledger")
	}
}
func TestUnknownPausePlacementDoesNotInventActiveSeconds(t *testing.T) {
	s := fixture(600, 147.5)
	s.DurationUs = 500e6
	r := calc(t, s)
	if r.ActiveSeconds != nil || len(r.Days) != 1 || r.Days[0].Load != nil || r.Days[0].ActiveSeconds != nil {
		t.Fatal("unknown active placement turned into elapsed/zero active time")
	}
}
