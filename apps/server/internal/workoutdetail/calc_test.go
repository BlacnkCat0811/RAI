package workoutdetail

import (
	"math"
	"testing"
)

func TestGradeAdjustedPaceIsFlatAtZeroAndDirectional(t *testing.T) {
	flat, reasons := GradeAdjustedPace(300, 0)
	if math.Abs(flat-300) > 1e-9 || len(reasons) != 0 {
		t.Fatalf("flat=%v reasons=%v", flat, reasons)
	}
	up, _ := GradeAdjustedPace(300, 8)
	down, _ := GradeAdjustedPace(300, -8)
	if up >= 300 {
		t.Fatalf("uphill equivalent flat pace must be faster: %v", up)
	}
	if down <= 300 {
		t.Fatalf("downhill equivalent flat pace must be slower: %v", down)
	}
	_, reasons = GradeAdjustedPace(300, 80)
	if len(reasons) != 1 || reasons[0] != "grade_clamped_to_cost_model_domain" {
		t.Fatalf("missing clamp reason: %v", reasons)
	}
}

func TestSeriesSamplingPreservesExtremesAndLast(t *testing.T) {
	points := make([]MetricPoint, 5000)
	for i := range points {
		points[i] = MetricPoint{OffsetS: float64(i), Value: float64(i%100) + 1}
	}
	points[1234].Value = -50
	points[4321].Value = 999
	got := sampleMetricPoints(points, DisplaySampleLimit)
	if len(got) > DisplaySampleLimit+2 {
		t.Fatalf("too many points: %d", len(got))
	}
	var hasMin, hasMax, hasLast bool
	for _, p := range got {
		if p.Value == -50 {
			hasMin = true
		}
		if p.Value == 999 {
			hasMax = true
		}
		if p.OffsetS == points[len(points)-1].OffsetS {
			hasLast = true
		}
	}
	if !hasMin || !hasMax || !hasLast {
		t.Fatalf("sampling lost display-critical point: min=%v max=%v last=%v", hasMin, hasMax, hasLast)
	}
}

func TestBuildSplitsUsesOfficialDistanceAndGrade(t *testing.T) {
	start := int64(1_700_000_000_000_000)
	end := start + 3600*1_000_000
	speed := []Observation{{Type: RunningSpeed, StartUs: start, EndUs: end, Value: 1000.0 / 360.0, Unit: "m/s"}}
	hr := []Observation{{Type: HeartRate, StartUs: start, EndUs: end, Value: 150, Unit: "count/min"}}
	route := make([]RouteSample, 11)
	for i := range route {
		route[i] = RouteSample{AtUs: start + int64(i)*360*1_000_000, AltitudeM: Ptr(10 + float64(i)*10)}
	}
	splits, grade, _ := BuildSplits(start, end, 10_000, speed, hr, nil, route)
	if len(splits) != 10 {
		t.Fatalf("splits=%d", len(splits))
	}
	for _, s := range splits {
		if math.Abs(s.DistanceM-1000) > 0.2 || math.Abs(s.AveragePaceSPerKm-360) > 1 || s.AverageHeartRateBpm == nil || *s.AverageHeartRateBpm != 150 {
			t.Fatalf("bad split: %+v", s)
		}
		if s.Grade == nil || s.Grade.GradePercent == nil || math.Abs(*s.Grade.GradePercent-1) > 0.2 {
			t.Fatalf("bad grade: %+v pct=%v dist=%v", s.Grade, ptrValue(s.Grade.GradePercent), s.DistanceM)
		}
	}
	if grade == nil || grade.CoveredSplits != 10 || grade.GradeAdjustedPaceSPerKm == nil || *grade.GradeAdjustedPaceSPerKm >= 360 {
		t.Fatalf("bad grade summary: %+v", grade)
	}
}

func TestBuildSegmentsUsesMeasuredActivitiesAndPlanLabels(t *testing.T) {
	start := int64(1_700_000_000_000_000)
	end := start + 600*1_000_000
	speed := []Observation{{Type: RunningSpeed, StartUs: start, EndUs: end, Value: 1000.0 / 300.0, Unit: "m/s"}}
	activities := []ActivityBoundary{{StartUs: start, EndUs: start + 300e6}, {StartUs: start + 300e6, EndUs: end}}
	plan := &PlanContext{LocalDate: "2026-10-31", Kind: "INTERVAL", Revision: 3, HasStructure: true, Steps: []PlannedStep{{Purpose: "warmup", DurationS: 300}, {Purpose: "work", DurationS: 300, TargetHR: Ptr(170)}}}
	segments, match, _, measured := BuildSegments(start, end, 2000, speed, nil, nil, nil, activities, nil, plan)
	if !measured || len(segments) != 2 || match == nil || match.ExplainedFraction != 1 {
		t.Fatalf("segments=%+v match=%+v measured=%v", segments, match, measured)
	}
	if segments[0].Label != "热身" || segments[1].AlertKind != "heart_rate" || segments[1].AlertUpper == nil {
		t.Fatalf("plan labels not applied: %+v", segments)
	}
}

func TestBuildHRZonesCoverageAndFractions(t *testing.T) {
	start := int64(1_700_000_000_000_000)
	heart := []Observation{
		{Type: HeartRate, StartUs: start, EndUs: start + 60e6, Value: 100},
		{Type: HeartRate, StartUs: start + 60e6, EndUs: start + 120e6, Value: 170},
	}
	zones := BuildHRZones(start, start+120e6, 120, heart, 50, 200)
	if zones == nil || zones.HeartRateCoverage != 1 {
		t.Fatalf("zones %+v", zones)
	}
	var total float64
	for _, z := range zones.HeartRateExposure {
		total += z.Fraction
	}
	if math.Abs(total-1) > 1e-9 {
		t.Fatalf("fractions sum to %v", total)
	}
}

func TestEnvironmentRequiresAllInputsForCorrection(t *testing.T) {
	partial := BuildEnvironment(map[string]any{"HKWeatherTemperature": map[string]any{"value": 30.0, "unit": "degC"}}, Ptr(10000), Ptr(3600), nil)
	if partial == nil || partial.Status != "unavailable" || partial.Correction != nil || len(partial.ReasonCodes) != 2 {
		t.Fatalf("partial environment fabricated correction: %+v", partial)
	}
	full := BuildEnvironment(map[string]any{
		"HKWeatherTemperature": map[string]any{"value": 30.0, "unit": "degC"},
		"HKWeatherHumidity":    map[string]any{"value": 0.8, "unit": "1"},
	}, Ptr(10000), Ptr(3600), Ptr(50))
	if full == nil || full.Status != "applied" || full.Correction == nil || full.Adjusted == nil {
		t.Fatalf("full environment missing correction: %+v", full)
	}
	if full.TemperatureC == nil || *full.TemperatureC != 30 || full.HumidityPercent == nil || *full.HumidityPercent != 80 {
		t.Fatalf("raw environment observations wrong: %+v", full)
	}
}

func ptrValue(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

func TestApplyStimulusScoresUsesPlanTargetAndConfidence(t *testing.T) {
	zones := &ZoneSummary{
		HeartRateExposure: []ZoneExposure{
			{Zone: "aerobic_base", DurationSeconds: 900, Fraction: 0.5},
			{Zone: "lactate_threshold", DurationSeconds: 600, Fraction: 0.3333},
		},
	}
	plan := &PlanContext{Kind: "TEMPO", DurationS: 1800}
	ApplyStimulusScores(zones, plan, 0.9, 1800)
	if zones.StimulusType != "lactate_threshold" || len(zones.StimulusScores) != 1 {
		t.Fatalf("stimulus %+v", zones)
	}
	score := zones.StimulusScores[0]
	if score.TargetSeconds != 1800 || score.QualifyingSeconds != 600 || math.Abs(score.Score-0.27) > 0.001 {
		t.Fatalf("score %+v", score)
	}
	if zones.StimulusConfidence == nil || *zones.StimulusConfidence != 0.81 {
		t.Fatalf("confidence %+v", zones.StimulusConfidence)
	}
}

func TestSegmentsPreferEventsOverSingleParentActivity(t *testing.T) {
	start, end := int64(1_700_000_000_000_000), int64(1_700_000_600_000_000)
	plan := &PlanContext{Kind: "INTERVAL", HasStructure: true, Steps: []PlannedStep{{Purpose: "warmup", DurationS: 300}, {Purpose: "work", DurationS: 300}}}
	for _, typ := range []int64{3, 7} {
		events := []EventBoundary{{Type: typ, StartUs: start, EndUs: start + 300e6}, {Type: typ, StartUs: start + 300e6, EndUs: end}, {Type: 8, StartUs: start + 100e6, EndUs: start + 100e6}}
		segments, match, _, measured := BuildSegments(start, end, 2000, nil, nil, nil, nil, []ActivityBoundary{{StartUs: start, EndUs: end}}, events, plan)
		if len(segments) != 2 || measured || match != nil || segments[0].Label != "第 1 段" || segments[0].DurationSeconds != 300 {
			t.Fatalf("event type %d lost or mislabeled: segments=%+v match=%+v measured=%v", typ, segments, match, measured)
		}
	}
	for _, activities := range [][]ActivityBoundary{nil, {{StartUs: start, EndUs: end}}, {{StartUs: start, EndUs: start + 100e6}, {StartUs: start + 200e6, EndUs: end}}} {
		segments, _, _, measured := BuildSegments(start, end, 2000, nil, nil, nil, nil, activities, []EventBoundary{{Type: 8, StartUs: start + 300e6, EndUs: start + 300e6}}, nil)
		if len(segments) != 0 || measured {
			t.Fatal("invented measured steps without a subdivision")
		}
	}
}

func TestHRZonesDeduplicateAndExcludeAmbiguousIntervals(t *testing.T) {
	for _, tc := range []struct {
		name              string
		heart             []Observation
		seconds, coverage float64
	}{
		{"duplicates", []Observation{{Type: HeartRate, EndUs: 600e6, Value: 185}, {Type: HeartRate, EndUs: 600e6, Value: 185}}, 600, 1},
		{"partial conflict", []Observation{{Type: HeartRate, EndUs: 600e6, Value: 185}, {Type: HeartRate, StartUs: 300e6, EndUs: 600e6, Value: 150}}, 300, .5},
		{"below resting", []Observation{{Type: HeartRate, EndUs: 600e6, Value: 40}}, 600, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			zones := BuildHRZones(0, 600e6, 600, tc.heart, 50, 200)
			if zones == nil || zones.HeartRateCoverage != tc.coverage {
				t.Fatalf("coverage %+v", zones)
			}
			var total float64
			for _, z := range zones.HeartRateExposure {
				total += z.DurationSeconds
				if tc.name == "below resting" && z.Zone == "anaerobic" && z.DurationSeconds > 0 {
					t.Fatal("below-resting heart rate classified as anaerobic")
				}
			}
			if total != tc.seconds {
				t.Fatalf("exposure %v want %v", total, tc.seconds)
			}
			ApplyStimulusScores(zones, &PlanContext{Kind: "INTERVAL", DurationS: 600}, zones.HeartRateCoverage, 600)
			if zones.StimulusScores[0].QualifyingSeconds > tc.seconds {
				t.Fatal("duplicated stimulus seconds")
			}
		})
	}
	if zones := BuildHRZones(0, 600e6, 600, []Observation{{Type: HeartRate, EndUs: 600e6, Value: 185}, {Type: HeartRate, EndUs: 600e6, Value: 150}}, 50, 200); zones != nil {
		t.Fatal("fully ambiguous heart trace produced zones")
	}
}
