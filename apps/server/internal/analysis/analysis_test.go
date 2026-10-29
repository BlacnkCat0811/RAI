package analysis

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"testing"
	"testing/quick"
)

func fixtureWorkout(t *testing.T) WorkoutInput {
	t.Helper()
	b, e := os.ReadFile("testdata/workout.bundle.v1.json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct{ Input WorkoutInput }
	if e = json.Unmarshal(b, &f); e != nil {
		t.Fatal(e)
	}
	return f.Input
}
func find(ms []Metric, name string) Metric {
	for _, m := range ms {
		if m.Name == name {
			return m
		}
	}
	return Metric{}
}
func TestWorkoutV2RetainsKnownV1Numerics(t *testing.T) {
	b, e := os.ReadFile("testdata/workout.bundle.v1.json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct {
		Algorithm string
		Input     WorkoutInput
		Expected  map[string]*float64
	}
	if e = json.Unmarshal(b, &f); e != nil {
		t.Fatal(e)
	}
	if f.Algorithm != WorkoutAlgorithm+"/v1" {
		t.Fatal("wrong fixture version")
	}
	ms, e := WorkoutFeatures(context.Background(), f.Input)
	if e != nil {
		t.Fatal(e)
	}
	for key, want := range f.Expected {
		m := find(ms, key)
		if m.Name == "" {
			t.Fatal("metric missing", key)
		}
		if want == nil {
			if m.Value != nil || m.Quality != "insufficient" {
				t.Fatal("missing became zero", key)
			}
			continue
		}
		if m.Value == nil || math.Abs(*m.Value-*want) > 1e-9 {
			t.Fatal("golden mismatch", key)
		}
	}
	m, e := CalculateTrainingLoad(f.Input)
	if e != nil || m.Value == nil || *m.Value != *find(ms, "workout_load").Value {
		t.Fatal("load replay entry differs", e)
	}
	// Same facts, hash/version and output, including JSON map key order.
	first, _ := json.Marshal(ms)
	again, e := WorkoutFeatures(context.Background(), f.Input)
	if e != nil {
		t.Fatal(e)
	}
	second, _ := json.Marshal(again)
	if string(first) != string(second) {
		t.Fatal("non deterministic")
	}
}
func TestStateV2RetainsKnownV1NumericsAndIncremental(t *testing.T) {
	b, e := os.ReadFile("testdata/training_state.bundle.v1.json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct {
		Algorithm string
		Input     StateInput
		Expected  map[string]map[string]float64
	}
	if e = json.Unmarshal(b, &f); e != nil {
		t.Fatal(e)
	}
	if f.Algorithm != StateAlgorithm+"/v1" {
		t.Fatal("wrong version")
	}
	ms, e := TrainingState(context.Background(), f.Input)
	if e != nil {
		t.Fatal(e)
	}
	for _, m := range ms {
		want := f.Expected[m.Day][m.Name]
		if m.Value == nil || math.Abs(*m.Value-want) > 1e-9 {
			t.Fatalf("state golden %s/%s", m.Day, m.Name)
		}
	}
	f.Input.From = f.Input.Through
	inc, e := TrainingState(context.Background(), f.Input)
	if e != nil {
		t.Fatal(e)
	}
	for _, m := range inc {
		for _, full := range ms {
			if m.Day == full.Day && m.Name == full.Name && (m.Value == nil || *m.Value != *full.Value) {
				t.Fatal("incremental/full differ")
			}
		}
	}
}
func TestMissingAndAmbiguousInputs(t *testing.T) {
	for _, name := range []string{"no_hr", "partial_hr", "instant_hr", "overlap", "no_parameters", "summary_only", "indoor", "no_cadence_power", "paused_workout"} {
		t.Run(name, func(t *testing.T) {
			w := fixtureWorkout(t)
			switch name {
			case "no_hr", "indoor":
				w.Observations = nil
			case "partial_hr":
				w.Observations[0].End = 720e6
			case "instant_hr":
				w.Observations[0].End = 0
			case "overlap":
				o := w.Observations[0]
				o.ID = "overlap"
				w.Observations = append(w.Observations, o)
			case "paused_workout":
				w.Duration = 3000
			case "no_parameters":
				w.Parameters = nil
			case "summary_only":
				w.Observations = nil
				w.Statistics = []Statistic{{Type: HeartRate, Average: Ptr(150), Maximum: Ptr(170)}}
			}
			ms, e := WorkoutFeatures(context.Background(), w)
			if e != nil {
				t.Fatal(e)
			}
			if name != "no_cadence_power" && find(ms, "workout_load").Value != nil {
				t.Fatal("insufficient load fabricated")
			}
			for _, key := range []string{"average_cadence", "average_power", "route_distance"} {
				if find(ms, key).Value != nil {
					t.Fatal("missing value fabricated", key)
				}
			}
		})
	}
	w := fixtureWorkout(t)
	w.Deleted = true
	ms, e := WorkoutFeatures(context.Background(), w)
	if e != nil || len(ms) != 0 {
		t.Fatal("deleted workout analyzed")
	}
	for _, loads := range [][]Load{nil, {{WorkoutID: "A", RunID: "A", Day: "2026-10-28", Value: nil}}} {
		ms, e := TrainingState(context.Background(), StateInput{From: "2026-10-28", Through: "2026-10-28", Timezone: "Asia/Shanghai", Loads: loads})
		if e != nil {
			t.Fatal(e)
		}
		if find(ms, "readiness").Value != nil {
			t.Fatal("missing history generated readiness")
		}
	}
}
func TestRouteCanonicalAndQuality(t *testing.T) {
	w := fixtureWorkout(t)
	w.Route = []Point{{Route: "A", At: 0, Latitude: 0, Longitude: 0, HorizontalAccuracy: Ptr(5), VerticalAccuracy: Ptr(5), Altitude: Ptr(10)}, {Route: "A", At: 10e6, Latitude: 0, Longitude: .001, HorizontalAccuracy: Ptr(5), VerticalAccuracy: Ptr(5), Altitude: Ptr(12)}}
	ms, e := WorkoutFeatures(context.Background(), w)
	if e != nil {
		t.Fatal(e)
	}
	if math.Abs(*find(ms, "route_distance").Value-111.19508023353292) > 1e-8 || *find(ms, "elevation_gain").Value != 2 {
		t.Fatal("route distance/elevation mismatch")
	}
	w.Route[1].HorizontalAccuracy = nil
	ms, e = WorkoutFeatures(context.Background(), w)
	if e != nil || find(ms, "route_distance").Value != nil {
		t.Fatal("unknown accuracy treated as reliable")
	}
}
func TestNumericalAndCoverageProperties(t *testing.T) {
	property := func(d uint16, meters uint16) bool {
		w := WorkoutInput{Start: 0, End: int64(d) * 1e6, Duration: float64(d), Distance: Ptr(float64(meters))}
		ms, e := WorkoutFeatures(context.Background(), w)
		if e != nil {
			return false
		}
		for _, m := range ms {
			if m.Value != nil && (!Finite(*m.Value) || *m.Value < 0) {
				return false
			}
		}
		return true
	}
	if e := quick.Check(property, &quick.Config{MaxCount: 500}); e != nil {
		t.Fatal(e)
	}
	w := fixtureWorkout(t)
	for _, bad := range []float64{-1, math.NaN(), math.Inf(1)} {
		w.Observations[0].Value = bad
		if _, e := WorkoutFeatures(context.Background(), w); e == nil {
			t.Fatal("invalid raw accepted")
		}
	}
	w = fixtureWorkout(t)
	w.Observations[0].Value = 201
	if _, e := WorkoutFeatures(context.Background(), w); e == nil {
		t.Fatal("out of configured HR range silently clamped")
	}
	ctx, c := context.WithCancel(context.Background())
	c()
	if _, e := WorkoutFeatures(ctx, fixtureWorkout(t)); e == nil {
		t.Fatal("cancellation ignored")
	}
	in := Snapshot{Generation: 1, Revision: 1, Workout: PtrWorkout(fixtureWorkout(t))}
	a, _ := in.Hash()
	in.Generation = 2
	in.Revision = 20
	b, _ := in.Hash()
	if Hex(a) != Hex(b) {
		t.Fatal("hash includes unrelated dataset revision")
	}
}
func PtrWorkout(w WorkoutInput) *WorkoutInput { return &w }

func TestStateBoundsFiniteWindowAndTimezone(t *testing.T) {
	property := func(load uint16) bool {
		in := StateInput{From: "2026-03-07", Through: "2026-03-09", Timezone: "America/New_York", Loads: []Load{{WorkoutID: "A", Day: "2026-03-07", Value: Ptr(float64(load))}}}
		ms, e := TrainingState(context.Background(), in)
		if e != nil {
			return false
		}
		for _, m := range ms {
			if m.Value != nil {
				if !Finite(*m.Value) {
					return false
				}
				if m.Name == "readiness" && (*m.Value < 0 || *m.Value > 100) {
					return false
				}
			}
		}
		return len(ms) == 18
	}
	if e := quick.Check(property, &quick.Config{MaxCount: 200}); e != nil {
		t.Fatal(e)
	}
	// A change outside the explicitly defined finite kernel has no influence.
	in := StateInput{From: "2026-10-28", Through: "2026-10-28", Timezone: "Asia/Shanghai", Loads: []Load{{Day: "2026-10-28", Value: Ptr(100)}}}
	ms, e := TrainingState(context.Background(), in)
	if e != nil || find(ms, "readiness").Value == nil {
		t.Fatal(e)
	}
	in.Timezone = "Not/AZone"
	if _, e = TrainingState(context.Background(), in); e == nil {
		t.Fatal("implicit OS timezone used")
	}
}

func TestRealCadencePowerAndSummaryMaximum(t *testing.T) {
	w := fixtureWorkout(t)
	w.Observations = append(w.Observations, Observation{ID: "cadence", Type: Cadence, Start: 0, End: w.End, Value: 180, Unit: "count/min"}, Observation{ID: "power", Type: Power, Start: 0, End: w.End, Value: 250, Unit: "W"})
	w.Statistics = []Statistic{{Type: HeartRate, Maximum: Ptr(170)}}
	ms, e := WorkoutFeatures(context.Background(), w)
	if e != nil {
		t.Fatal(e)
	}
	for name, want := range map[string]float64{"average_cadence": 180, "average_power": 250, "max_heart_rate": 170} {
		m := find(ms, name)
		if m.Value == nil || *m.Value != want {
			t.Fatal("real source metric missing", name)
		}
	}
	w.Observations = []Observation{{ID: "steps", Type: "HKQuantityTypeIdentifierStepCount", Start: 0, End: w.End, Value: 10000, Unit: "count"}}
	ms, e = WorkoutFeatures(context.Background(), w)
	if e != nil || find(ms, "average_cadence").Value != nil || find(ms, "average_power").Value != nil {
		t.Fatal("steps fabricated cadence/power", e)
	}
}
