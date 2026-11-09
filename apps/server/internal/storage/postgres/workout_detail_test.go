package postgres

import (
	"testing"
	"time"

	"rai/server/internal/analysis"
	"rai/server/internal/syncing"
	"rai/server/internal/training"
	"rai/server/internal/workoutdetail"
)

func detailProjectionFixture() *workoutDetailData {
	start := time.Unix(1_700_000_000, 0).UTC()
	d := &workoutDetailData{ID: syncing.NewID(), Start: start, End: start.Add(600 * time.Second), DurationUs: 600e6, ActivityType: 37,
		Payload: syncing.Event{Workout: &syncing.Workout{TotalDistance: &syncing.Quantity{Value: 2000, Unit: "m"}}, Metadata: map[string]any{
			"HKWeatherTemperature": map[string]any{"value": 30.0, "unit": "degC"}, "HKWeatherHumidity": map[string]any{"value": .8, "unit": "1"},
		}}, Parameters: &analysis.Parameters{RestHR: 50, MaxHR: 200},
		Execution: &executionRow{Snapshot: training.Session{Type: "EASY", Effort: 3, DurationS: 600, Modified: true}},
	}
	for _, spec := range []struct {
		typ, unit string
		value     float64
	}{{workoutdetail.RunningSpeed, "m/s", 1000.0 / 300}, {workoutdetail.HeartRate, "count/min", 150}} {
		d.Observations = append(d.Observations, workoutdetail.Observation{Type: spec.typ, Unit: spec.unit, StartUs: start.UnixMicro(), EndUs: d.End.UnixMicro(), Value: spec.value})
	}
	for i := 0; i <= 20; i++ {
		d.Route = append(d.Route, workoutdetail.RouteSample{AtUs: start.Add(time.Duration(i) * 30 * time.Second).UnixMicro(), AltitudeM: workoutdetail.Ptr(float64(i))})
	}
	return d
}

func TestMeasuredMergesNestedActivityEvents(t *testing.T) {
	start := time.Unix(1_700_000_000, 0).UTC()
	end := start.Add(600 * time.Second)
	typ := int64(7)
	firstStart, firstEnd := start.UnixMicro(), start.Add(300*time.Second).UnixMicro()
	secondStart, secondEnd := firstEnd, end.UnixMicro()
	d := &workoutDetailData{
		Start: start,
		End:   end,
		Events: []syncing.WorkoutEvent{
			{Type: &typ, StartUs: &firstStart, EndUs: &firstEnd},
		},
		Activities: []syncing.WorkoutActivity{{
			Events: []syncing.WorkoutEvent{
				{Type: &typ, StartUs: &firstStart, EndUs: &firstEnd},
				{Type: &typ, StartUs: &secondStart, EndUs: &secondEnd},
			},
		}},
	}
	_, events := d.measured()
	if len(events) != 2 {
		t.Fatalf("nested activity events not merged or deduplicated: %+v", events)
	}
	if events[0].StartUs != firstStart || events[1].StartUs != secondStart {
		t.Fatalf("event order not deterministic: %+v", events)
	}
}

func TestDetailDerivedFieldsRequirePublication(t *testing.T) {
	d := detailProjectionFixture()
	for _, status := range []string{"pending", "failed"} {
		d.AnalysisStatus = status
		v := d.buildDetail()
		if v.Workout.AnalysisStatus != status || v.Workout.AveragePaceSPerKm == nil || v.Environment.TemperatureC == nil {
			t.Fatal("raw facts not available")
		}
		if v.Summary.GradeAdjustedPaceSPerKm != nil || v.Summary.ElevationGainM != nil || v.Summary.RouteQuality != nil || v.TrainingLoad != nil || v.ZoneSummary != nil || v.Environment.Correction != nil || v.Environment.Adjusted != nil || v.Workout.QualityScore != nil || v.Workout.SourceConfidence != nil || v.Workout.HeartRateCoverage != nil {
			t.Fatal("unpublished derived field exposed")
		}
		s := d.buildSeries()
		if s.Grade != nil || s.Dynamics != nil || len(s.Splits) != 2 || len(s.Series.Pace.Points) == 0 {
			t.Fatal("pending series lost facts or exposed analysis")
		}
		for _, split := range s.Splits {
			if split.Grade != nil {
				t.Fatal("unpublished split grade exposed")
			}
		}
	}
	d.AnalysisAvailable, d.AnalysisStatus = true, "published"
	d.Published = []analysis.Metric{{Name: "workout_load", Quality: "insufficient"}}
	v := d.buildDetail()
	if v.Summary.GradeAdjustedPaceSPerKm == nil || v.ZoneSummary == nil || v.Environment.Correction == nil || d.buildSeries().Dynamics == nil {
		t.Fatal("publication did not release derived fields")
	}
	if v.TrainingLoad == nil || v.TrainingLoad.Source != "planned_effort_fallback/v1" || v.TrainingLoad.RPE != nil || *v.TrainingLoad.Confidence != .4 || v.Override.RPE != nil {
		t.Fatalf("planned effort misrepresented as RPE: %+v", v.TrainingLoad)
	}
	d.Execution.Snapshot.Effort = 0
	v = d.buildDetail()
	if v.TrainingLoad.Source != "session_type_fallback/v1" || v.TrainingLoad.RPE != nil {
		t.Fatal("missing planned effort did not use session type")
	}
}
