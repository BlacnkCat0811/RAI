package workoutfeatures

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"slices"
	"testing"
)

func synthetic(seconds int) RawWorkoutSnapshot {
	source := NewSource("com.apple.Workout", "apple_watch", json.RawMessage(`{"bundleID":"com.apple.Workout","version":"synthetic","productType":"Watch9,1"}`))
	start := int64(1793404800000000)
	end := start + int64(seconds)*1e6
	through := end + 86400e6
	s := RawWorkoutSnapshot{DatasetID: "synthetic-dataset", WorkoutID: "synthetic-workout", Generation: 1, Revision: 1, AsOf: end + 1e6, Policy: DefaultPolicy(), AlgorithmVersion: Algorithm + "/" + Version, Fact: FactRef{"workout", "synthetic", digest("synthetic"), Present}, Start: start, End: end, DurationUs: end - start, Distance: ptr(float64(seconds) * 3), ActivityType: 37, Metadata: json.RawMessage(`{"HKIndoorWorkout":false,"HKWeatherTemperature":{"value":16,"unit":"degC"},"HKWeatherHumidity":{"value":0.5,"unit":"1"}}`), Provenance: []Source{source}, Parameters: []Parameter{{ID: "synthetic-hr-reference", Hash: digest("parameter"), EffectiveFrom: start - 1e6, EffectiveThrough: &through, RecordedAt: start - 1e6, RestHR: 50, MaxHR: 200, Provenance: "synthetic measured reference"}}}
	for i := 0; i < seconds; i += 5 {
		a, b := start+int64(i)*1e6, min(end, start+int64(i+5)*1e6)
		for _, k := range []string{HR, Speed, Steps, Power, GCT, Stride, Vertical} {
			v := map[string]float64{HR: 150, Speed: 3, Steps: float64(b-a) / 1e6 * 3, Power: 240, GCT: 250, Stride: 1, Vertical: .08}[k]
			s.Quantities = append(s.Quantities, Quantity{Fact: FactRef{"quantity", fmt.Sprintf("%s:%d", k, i), digest([]any{k, i}), Present}, Type: k, Start: a, End: b, Value: v, Unit: unit(k), Source: source.Epoch, Support: IntervalObservation})
		}
	}
	r := Route{Fact: FactRef{"route", "synthetic-route", digest("route"), Present}, Source: source.Epoch}
	for i := 0; i <= seconds; i++ {
		r.Points = append(r.Points, RoutePoint{Sequence: int64(i), At: start + int64(i)*1e6, Latitude: 0, Longitude: float64(i) * 3 / 6371008.8 * 180 / math.Pi, Altitude: ptr(100), HorizontalAccuracy: ptr(3), VerticalAccuracy: ptr(1)})
	}
	s.Routes = []Route{r}
	return s
}
func compute(t *testing.T, s RawWorkoutSnapshot) WorkoutFeatures {
	t.Helper()
	f, e := Compute(context.Background(), s)
	if e != nil {
		t.Fatal(e)
	}
	return f
}
func closeTo(t *testing.T, p *float64, want, tol float64) {
	t.Helper()
	if p == nil || math.Abs(*p-want) > tol {
		t.Fatalf("got=%v want=%.9f", p, want)
	}
}
func hasReason(q Qualification, r Reason) bool { return slices.Contains(q.Reasons, r) }
func removeKind(s *RawWorkoutSnapshot, k string) {
	s.Quantities = slices.DeleteFunc(s.Quantities, func(q Quantity) bool { return q.Type == k })
}
func instant(s *RawWorkoutSnapshot, k string, step int) {
	removeKind(s, k)
	v := 150.0
	if k == Speed {
		v = 3
	}
	for a := s.Start; a <= s.End; a += int64(step) * 1e6 {
		s.Quantities = append(s.Quantities, Quantity{Fact: FactRef{Kind: "quantity", ID: fmt.Sprint(k, a), State: Present}, Type: k, Start: a, End: a, Value: v, Unit: unit(k), Source: s.Provenance[0].Epoch, Support: InstantPoint})
	}
}
func TestGoldenFeatureFixtures(t *testing.T) {
	for _, tc := range []struct {
		name    string
		seconds int
		speed   float64
	}{{"beginner_easy", 1200, 2.5}, {"intermediate_tempo", 2700, 4}, {"long_run", 7200, 3}, {"short_run", 120, 3}, {"four_hour", 14400, 3}} {
		t.Run(tc.name, func(t *testing.T) {
			s := synthetic(tc.seconds)
			ratio := tc.speed / 3
			*s.Distance *= ratio
			for i := range s.Quantities {
				if s.Quantities[i].Type == Speed {
					s.Quantities[i].Value = tc.speed
				}
			}
			for i := range s.Routes[0].Points {
				s.Routes[0].Points[i].Longitude *= ratio
			}
			f := compute(t, s)
			closeTo(t, f.Timing.Active, float64(tc.seconds), 1e-9)
			closeTo(t, f.HeartRate.Mean, 150, 1e-9)
			closeTo(t, f.Speed.Mean, tc.speed, 1e-9)
			closeTo(t, f.Distance.Preferred, float64(tc.seconds)*tc.speed, 1e-6)
			closeTo(t, f.Dynamics["cadence_proxy"].Mean, 180, 1e-9)
			if f.Qualification.Load.Status != "eligible" {
				t.Fatal(f.Qualification.Load)
			}
			if tc.seconds >= 1500 && tc.speed >= 134.0/60 && tc.speed <= 5 && f.Qualification.VO2.Status == "ineligible" {
				t.Fatal(f.Qualification.VO2)
			}
			if tc.seconds >= 3600 && f.LongRun.First == nil {
				t.Fatal("missing comparable long run halves")
			}
			b, _, e := f.MarshalArtifact()
			if e != nil || len(b) > MaxArtifactBytes {
				t.Fatal("artifact bound", len(b), e)
			}
			if bytes.Contains(b, []byte("latitude")) || bytes.Contains(b, []byte("quantities")) {
				t.Fatal("raw dump in artifact")
			}
		})
	}
}
func TestPauseStateMachine(t *testing.T) {
	for _, tc := range []struct {
		name      string
		events    []Event
		duration  int64
		uncertain bool
		active    float64
	}{
		{"manual", []Event{{Type: 1, Start: 100e6, End: 100e6}, {Type: 2, Start: 120e6, End: 120e6}}, 580e6, false, 580},
		{"motion", []Event{{Type: 5, Start: 100e6, End: 100e6}, {Type: 6, Start: 120e6, End: 120e6}}, 580e6, false, 580},
		{"overlap_states", []Event{{Type: 1, Start: 100e6, End: 100e6}, {Type: 5, Start: 110e6, End: 110e6}, {Type: 2, Start: 120e6, End: 120e6}, {Type: 6, Start: 130e6, End: 130e6}}, 570e6, false, 570},
		{"duplicate_pause_resume", []Event{{Type: 1, Start: 100e6, End: 100e6}, {Type: 1, Start: 101e6, End: 101e6}, {Type: 2, Start: 120e6, End: 120e6}, {Type: 2, Start: 121e6, End: 121e6}}, 580e6, false, 580},
		{"resume_before_pause", []Event{{Type: 2, Start: 1e6, End: 1e6}}, 600e6, true, 0},
		{"same_time_contradiction", []Event{{Type: 1, Start: 100e6, End: 100e6}, {Type: 2, Start: 100e6, End: 100e6}}, 600e6, true, 0},
		{"outside", []Event{{Type: 1, Start: -1e6, End: -1e6}}, 600e6, true, 0},
		{"unclosed", []Event{{Type: 1, Start: 100e6, End: 100e6}}, 100e6, true, 0},
		{"unlocated_duration", nil, 550e6, true, 0},
		{"request_lap_segment", []Event{{Type: 8, Start: 100e6, End: 100e6}, {Type: 3, Start: 200e6, End: 200e6}, {Type: 7, Start: 300e6, End: 310e6}}, 600e6, false, 600},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := synthetic(600)
			s.DurationUs = tc.duration
			for _, e := range tc.events {
				e.Start += s.Start
				e.End += s.Start
				s.Events = append(s.Events, e)
			}
			f := compute(t, s)
			if tc.uncertain {
				if f.Timing.Active != nil || !hasReason(f.Qualification.Load, ActiveTimeUncertain) {
					t.Fatal("guessed active time")
				}
			} else {
				closeTo(t, f.Timing.Active, tc.active, 1e-9)
			}
		})
	}
}
func TestSignalSupportAndSourceConflicts(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mutate   func(*RawWorkoutSnapshot)
		coverage float64
		mean     *float64
	}{
		{"HR_interval", func(s *RawWorkoutSnapshot) {}, 1, ptr(150)},
		{"instant_5s", func(s *RawWorkoutSnapshot) { instant(s, HR, 5) }, 1, ptr(150)},
		{"instant_gap_20s", func(s *RawWorkoutSnapshot) { instant(s, HR, 20) }, 0, nil},
		{"no_tail_extrapolation", func(s *RawWorkoutSnapshot) {
			instant(s, HR, 5)
			s.Quantities = slices.DeleteFunc(s.Quantities, func(q Quantity) bool { return q.Type == HR && (q.Start == s.Start || q.Start == s.End) })
		}, 590.0 / 600, ptr(150)},
		{"condensed_HR", func(s *RawWorkoutSnapshot) {
			removeKind(s, HR)
			s.Quantities = append(s.Quantities, Quantity{Fact: FactRef{ID: "parent", State: Present}, Type: HR, Start: s.Start, End: s.End, Value: 190, Unit: "count/min", Source: s.Provenance[0].Epoch, Support: CondensedParent})
		}, 0, nil},
		{"sealed_replaces_parent", func(s *RawWorkoutSnapshot) {
			removeKind(s, HR)
			s.Quantities = append(s.Quantities, Quantity{Fact: FactRef{ID: "parent", State: Present}, Type: HR, Start: s.Start, End: s.End, Value: 190, Unit: "count/min", Source: s.Provenance[0].Epoch, Support: CondensedParent})
			s.Series = []Series{{Fact: FactRef{ID: "series", State: SeriesBacked}, ParentID: "parent", Type: HR, Source: s.Provenance[0].Epoch, Points: []Quantity{{Start: s.Start, End: s.End, Value: 150, Unit: "count/min"}}}}
		}, 1, ptr(150)},
		{"deleted_parent", func(s *RawWorkoutSnapshot) {
			removeKind(s, HR)
			s.Quantities = append(s.Quantities, Quantity{Fact: FactRef{ID: "parent", State: Deleted}, Type: HR})
			s.Series = []Series{{Fact: FactRef{State: SeriesBacked}, ParentID: "parent", Type: HR, Source: s.Provenance[0].Epoch, Points: []Quantity{{Start: s.Start, End: s.End, Value: 150, Unit: "count/min"}}}}
		}, 0, nil},
		{"same_source_conflict", func(s *RawWorkoutSnapshot) {
			s.Quantities = append(s.Quantities, Quantity{Type: HR, Start: s.Start, End: s.End, Value: 170, Unit: "count/min", Source: s.Provenance[0].Epoch, Support: IntervalObservation})
		}, 0, nil},
		{"different_source_no_average", func(s *RawWorkoutSnapshot) {
			src := NewSource("com.apple.Workout", "apple_watch", json.RawMessage(`{"version":"other"}`))
			s.Provenance = append(s.Provenance, src)
			s.Quantities = append(s.Quantities, Quantity{Type: HR, Start: s.Start, End: s.End, Value: 150, Unit: "count/min", Source: src.Epoch, Support: IntervalObservation})
		}, 0, nil},
		{"220_bpm_outlier", func(s *RawWorkoutSnapshot) {
			for i := range s.Quantities {
				if s.Quantities[i].Type == HR {
					s.Quantities[i].Value = 220
				}
			}
		}, 0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := synthetic(600)
			tc.mutate(&s)
			f := compute(t, s)
			if math.Abs(f.HeartRate.ReconstructedCoverage-tc.coverage) > 1e-9 {
				t.Fatalf("coverage=%v want=%v", f.HeartRate.ReconstructedCoverage, tc.coverage)
			}
			if tc.mean == nil {
				if f.HeartRate.Mean != nil {
					t.Fatal("unsupported mean")
				}
			} else {
				closeTo(t, f.HeartRate.Mean, *tc.mean, 1e-8)
			}
		})
	}
}
func TestRouteDistanceAndQualification(t *testing.T) {
	for _, name := range []string{"GPS_gap", "GPS_accuracy", "altitude_unknown", "altitude_noise", "distance_mismatch", "zero_distance", "vehicle_speed", "indoor", "environment_unknown", "missing_parameter", "deleted_route", "forgot_stop", "speed_conflict"} {
		t.Run(name, func(t *testing.T) {
			s := synthetic(1800)
			switch name {
			case "GPS_gap":
				s.Routes[0].Points = slices.DeleteFunc(s.Routes[0].Points, func(p RoutePoint) bool { return p.At > s.Start+400e6 && p.At < s.Start+800e6 })
			case "GPS_accuracy":
				for i := range s.Routes[0].Points {
					s.Routes[0].Points[i].HorizontalAccuracy = ptr(100)
				}
			case "altitude_unknown":
				for i := range s.Routes[0].Points {
					s.Routes[0].Points[i].VerticalAccuracy = nil
				}
			case "altitude_noise":
				for i := range s.Routes[0].Points {
					s.Routes[0].Points[i].Altitude = ptr(100 + float64(i%2)*.2)
				}
			case "distance_mismatch":
				s.Distance = ptr(10000)
			case "zero_distance":
				s.Distance = ptr(0)
			case "vehicle_speed":
				for i := range s.Quantities {
					if s.Quantities[i].Type == Speed {
						s.Quantities[i].Value = 30
					}
				}
			case "indoor":
				s.Metadata = json.RawMessage(`{"HKIndoorWorkout":true}`)
			case "environment_unknown":
				s.Metadata = nil
			case "missing_parameter":
				s.Parameters = nil
			case "deleted_route":
				s.Routes[0].Fact.State = Deleted
			case "forgot_stop":
				s.DurationUs = 1200e6
			case "speed_conflict":
				s.Quantities = append(s.Quantities, Quantity{Type: Speed, Start: s.Start, End: s.End, Value: 4, Unit: "m/s", Source: s.Provenance[0].Epoch, Support: IntervalObservation})
			}
			f := compute(t, s)
			if name == "altitude_noise" {
				if f.Qualification.VO2.Status == "ineligible" {
					t.Fatal("robust grade rejected low symmetric noise", f.Qualification.VO2)
				}
				return
			}
			if f.Qualification.VO2.Status != "ineligible" && name != "distance_mismatch" && name != "zero_distance" {
				t.Fatal("VO2 gate too permissive", f.Qualification.VO2)
			}
			if name == "distance_mismatch" && !hasReason(f.Qualification.Performance, DistanceMismatch) {
				t.Fatal("mismatch not gated")
			}
			if name == "GPS_accuracy" && f.Route.Distance != nil {
				t.Fatal("bad accuracy used")
			}
			if name == "altitude_unknown" && f.Route.FlatDistanceFraction != nil {
				t.Fatal("unknown grade filled zero")
			}
			if name == "zero_distance" && f.Distance.DifferenceRatio != nil {
				t.Fatal("zero division")
			}
		})
	}
}
func TestContinuousWindowsNeverStitchPauseGapOrSource(t *testing.T) {
	for _, name := range []string{"pause", "gap", "source_epoch", "recovery_elapsed"} {
		t.Run(name, func(t *testing.T) {
			s := synthetic(720)
			switch name {
			case "pause":
				s.Events = []Event{{Type: 1, Start: s.Start + 300e6, End: s.Start + 300e6}, {Type: 2, Start: s.Start + 420e6, End: s.Start + 420e6}}
				s.DurationUs -= 120e6
			case "gap":
				s.Quantities = slices.DeleteFunc(s.Quantities, func(q Quantity) bool { return q.Type == Speed && q.Start >= s.Start+300e6 && q.Start < s.Start+420e6 })
			case "source_epoch":
				src := NewSource("com.apple.Workout", "apple_watch", json.RawMessage(`{"version":"new_epoch"}`))
				s.Provenance = append(s.Provenance, src)
				for i := range s.Quantities {
					if s.Quantities[i].Type == Speed && s.Quantities[i].Start >= s.Start+360e6 {
						s.Quantities[i].Source = src.Epoch
					}
				}
			case "recovery_elapsed":
				for i := range s.Quantities {
					q := &s.Quantities[i]
					if q.Type == Speed && q.Start >= s.Start+300e6 && q.Start < s.Start+420e6 {
						q.Value = 1
					}
				}
			}
			f := compute(t, s)
			if name == "recovery_elapsed" {
				if f.Windows[2].Best == nil || f.Windows[2].Best.Distance >= 1800 {
					t.Fatal("recovery removed from elapsed effort")
				}
			} else if f.Windows[2].Best != nil {
				t.Fatal("10min window crossed discontinuity")
			}
		})
	}
}
func TestDeterministicReorderDuplicateAndImmutability(t *testing.T) {
	s := synthetic(1200)
	before, _ := json.Marshal(s)
	f := compute(t, s)
	want, _, _ := f.MarshalArtifact()
	r := rand.New(rand.NewSource(42))
	for i := 0; i < 12; i++ {
		var x RawWorkoutSnapshot
		_ = json.Unmarshal(before, &x)
		r.Shuffle(len(x.Quantities), func(i, j int) { x.Quantities[i], x.Quantities[j] = x.Quantities[j], x.Quantities[i] })
		r.Shuffle(len(x.Routes[0].Points), func(i, j int) {
			x.Routes[0].Points[i], x.Routes[0].Points[j] = x.Routes[0].Points[j], x.Routes[0].Points[i]
		})
		x.Quantities = append(x.Quantities, x.Quantities...)
		x.Routes = append(x.Routes, x.Routes...)
		got, _, _ := compute(t, x).MarshalArtifact()
		if !bytes.Equal(got, want) {
			t.Fatal("logical reorder/duplicate changed artifact")
		}
	}
	after, _ := json.Marshal(s)
	if !bytes.Equal(before, after) {
		t.Fatal("input mutated")
	}
}
func TestSealedSeriesRepartitionAndInstantCountProperty(t *testing.T) {
	s := synthetic(600)
	removeKind(&s, HR)
	points := []Quantity{{Start: s.Start, End: s.Start + 300e6, Value: 140, Unit: "count/min"}, {Start: s.Start + 300e6, End: s.End, Value: 160, Unit: "count/min"}}
	s.Series = []Series{{Fact: FactRef{State: SeriesBacked}, Type: HR, ParentID: "one", Source: s.Provenance[0].Epoch, Points: points}}
	a := compute(t, s)
	s.Series = []Series{{Fact: FactRef{State: SeriesBacked}, Type: HR, ParentID: "one", Source: s.Provenance[0].Epoch, Points: points[:1]}, {Fact: FactRef{State: SeriesBacked}, Type: HR, ParentID: "two", Source: s.Provenance[0].Epoch, Points: points[1:]}}
	b := compute(t, s)
	if *a.HeartRate.Mean != *b.HeartRate.Mean || a.HeartRate.ReconstructedCoverage != b.HeartRate.ReconstructedCoverage {
		t.Fatal("partition changed value")
	}
	removeKind(&s, HR)
	s.Series = nil
	for i := 0; i < 100; i++ {
		s.Quantities = append(s.Quantities, Quantity{Type: HR, Start: s.Start, End: s.Start, Value: 150, Unit: "count/min", Source: s.Provenance[0].Epoch, Support: InstantPoint, Fact: FactRef{ID: fmt.Sprint(i)}})
	}
	f := compute(t, s)
	if f.HeartRate.ReconstructedCoverage != 0 || f.HeartRate.ObservedCoverage != 0 {
		t.Fatal("point count faked time support")
	}
}
func TestParametersAsOfSupersedesAndLineage(t *testing.T) {
	s := synthetic(1800)
	p := s.Parameters[0]
	p.ID = "replacement"
	p.Supersedes = s.Parameters[0].ID
	p.RestHR = 40
	p.Hash = digest(p)
	s.Parameters = append(s.Parameters, p)
	f := compute(t, s)
	if f.Qualification.Load.Status != "eligible" {
		t.Fatal(f.Qualification.Load)
	}
	s.Parameters[1].Supersedes = ""
	f = compute(t, s)
	if !hasReason(f.Qualification.Load, ParameterConflict) {
		t.Fatal("arbitrary parameter winner")
	}
	s.Parameters[1].RecordedAt = s.AsOf + 1
	f = compute(t, s)
	if f.Qualification.Load.Status != "eligible" {
		t.Fatal("future parameter used")
	}
	s.Parameters[0].EffectiveFrom = s.Start + 1
	f = compute(t, s)
	if !hasReason(f.Qualification.Load, ParameterMissing) {
		t.Fatal("event-time parameter violated")
	}
}
func TestBoundsAndUnsupported(t *testing.T) {
	s := synthetic(1)
	s.Quantities = make([]Quantity, MaxPoints+1)
	if _, e := Compute(context.Background(), s); ErrorCode(e) != "input_limit" {
		t.Fatal("truncated limit", e)
	}
	s = synthetic(600)
	s.Fact.State = Deleted
	f := compute(t, s)
	if f.State != Deleted || f.Qualification.Load.Status != "ineligible" {
		t.Fatal("deleted eligible")
	}
	s = synthetic(600)
	s.Policy.SourcePolicy = "unknown"
	if _, e := Compute(context.Background(), s); e == nil {
		t.Fatal("unsupported policy accepted")
	}
}
func TestCrossMidnightDSTIsAbsoluteTime(t *testing.T) {
	for _, start := range []int64{1793462100000000, 1772956500000000, 1793510100000000} {
		s := synthetic(1200)
		delta := start - s.Start
		s.Start += delta
		s.End += delta
		s.AsOf += delta
		for i := range s.Quantities {
			s.Quantities[i].Start += delta
			s.Quantities[i].End += delta
		}
		for i := range s.Routes[0].Points {
			s.Routes[0].Points[i].At += delta
		}
		s.Parameters = nil
		f := compute(t, s)
		closeTo(t, f.Timing.Active, 1200, 1e-9)
		closeTo(t, f.Speed.Mean, 3, 1e-9)
	}
}

func TestNativeGapAndInstantInterpolationAnalytic(t *testing.T) {
	s := synthetic(1800)
	instant(&s, Speed, 10)
	f := compute(t, s)
	if f.Speed.MaxGap != 10 || f.Speed.ObservedCoverage != 0 || f.Speed.ReconstructedCoverage != 1 {
		t.Fatal("native interpolation gap lost")
	}
	if f.Windows[0].Best != nil || f.Qualification.Performance.Status != "ineligible" {
		t.Fatal("performance accepted >5s native speed gaps")
	}
	if f.Qualification.VO2.Status == "ineligible" {
		t.Fatal("independent VO2 support rejected", f.Qualification.VO2)
	}
	s = synthetic(600)
	removeKind(&s, HR)
	s.Quantities = append(s.Quantities, Quantity{Type: HR, Start: s.Start + 100e6, End: s.Start + 100e6, Value: 120, Unit: "count/min", Source: s.Provenance[0].Epoch, Support: InstantPoint}, Quantity{Type: HR, Start: s.Start + 110e6, End: s.Start + 110e6, Value: 180, Unit: "count/min", Source: s.Provenance[0].Epoch, Support: InstantPoint})
	f = compute(t, s)
	closeTo(t, f.HeartRate.Mean, 150, 1e-9)
	if math.Abs(f.HeartRate.ReconstructedCoverage-10.0/600) > 1e-12 || f.HeartRate.RobustMax != nil {
		t.Fatal("support/extrapolation incorrect")
	}
}
func TestMissingUnsupportedAndIsolatedSpikeStayMissing(t *testing.T) {
	s := synthetic(600)
	for i := range s.Quantities {
		if s.Quantities[i].Type == Power {
			s.Quantities[i].Fact.State = Missing
			s.Quantities[i].Value = 0
		}
	}
	f := compute(t, s)
	if f.Dynamics["power"].Mean != nil {
		t.Fatal("missing became zero")
	}
	instant(&s, HR, 5)
	for i := range s.Quantities {
		q := &s.Quantities[i]
		if q.Type == HR && q.Start == s.Start+300e6 {
			q.Value = 220
		}
	}
	f = compute(t, s)
	if f.HeartRate.ReconstructedCoverage >= 1 || !slices.Contains(f.HeartRate.Reasons, SignalOutlier) {
		t.Fatal("isolated spike was bridged")
	}
	if f.HeartRate.Mean != nil && *f.HeartRate.Mean != 150 {
		t.Fatal("spike entered mean")
	}
}
func TestNegativeAndFractionalEpochIndex(t *testing.T) {
	for _, start := range []int64{-1500000, -1000000, 333333} {
		s := synthetic(600)
		delta := start - s.Start
		s.Start += delta
		s.End += delta
		s.AsOf += delta
		for i := range s.Quantities {
			s.Quantities[i].Start += delta
			s.Quantities[i].End += delta
		}
		s.Routes = nil
		s.Parameters = nil
		n, e := Normalize(s)
		if e != nil {
			t.Fatal(e)
		}
		timeline := BuildTimeline(n)
		expected := int64(math.Ceil(float64(start)/1e6)) * 1e6
		if timeline.Start != expected {
			t.Fatal("incorrect grid origin", start, timeline.Start, expected)
		}
		f := compute(t, s)
		closeTo(t, f.Speed.Mean, 3, 1e-9)
	}
}
func FuzzFeatureOrderAndDuplicateProperty(f *testing.F) {
	for _, seed := range []int64{1, 42, 1001} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed int64) {
		s := synthetic(180)
		want, _, _ := compute(t, s).MarshalArtifact()
		r := rand.New(rand.NewSource(seed))
		r.Shuffle(len(s.Quantities), func(i, j int) { s.Quantities[i], s.Quantities[j] = s.Quantities[j], s.Quantities[i] })
		s.Quantities = append(s.Quantities, s.Quantities...)
		got, _, _ := compute(t, s).MarshalArtifact()
		if !bytes.Equal(want, got) {
			t.Fatal("order/duplicate changed feature")
		}
	})
}

func TestEnduranceRequiresTerrainAcrossComparableHalves(t *testing.T) {
	s := synthetic(7200)
	s.Routes[0].Points = s.Routes[0].Points[:601]
	f := compute(t, s)
	if f.LongRun.First == nil || f.LongRun.Second == nil {
		t.Fatal("speed primitives missing")
	}
	if f.Qualification.Endurance.Status != "ineligible" || !hasReason(f.Qualification.Endurance, GradeUnknown) {
		t.Fatal("small GPS fragment certified whole long run")
	}
}
func TestGradeQualificationUsesDistanceRatherThanTime(t *testing.T) {
	s := synthetic(1800)
	for i := range s.Quantities {
		q := &s.Quantities[i]
		if q.Type == Speed {
			q.Value = 2
			if q.Start >= s.Start+1620e6 {
				q.Value = 10
			}
		}
	}
	s.Distance = ptr(5040)
	for i := range s.Routes[0].Points {
		p := &s.Routes[0].Points[i]
		if p.At >= s.Start+1620e6 {
			p.Altitude = ptr(100 + float64(i-1620)*.3)
		}
	}
	f := compute(t, s)
	w := f.Windows[3].Best
	if w == nil || w.Qualification.Status != "ineligible" {
		t.Fatal("fast uphill distance hidden by flat seconds")
	}
}

func TestNonRunningActivityCannotBecomeRunningEvidence(t *testing.T) {
	s := synthetic(1800)
	s.ActivityType = 13
	f := compute(t, s)
	if f.ActivityType != 13 || f.HeartRate.Mean == nil {
		t.Fatal("source activity or primitives discarded")
	}
	for _, q := range []Qualification{f.Qualification.Load, f.Qualification.VO2, f.Qualification.Performance, f.Qualification.CS, f.Qualification.Endurance} {
		if q.Status != "ineligible" || !hasReason(q, UnsupportedInput) {
			t.Fatal("non-running source qualified", q)
		}
	}
	for _, w := range f.Windows {
		if w.Best != nil && w.Best.Qualification.Status != "ineligible" {
			t.Fatal("non-running window qualified")
		}
	}
}
