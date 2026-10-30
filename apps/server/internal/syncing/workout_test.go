package syncing

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func workoutFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../../../contracts/fixtures/workout.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestWorkoutRoundTrip(t *testing.T) {
	raw := workoutFixture(t)
	e, wire := payload(t, raw, 1)
	events, err := ParseBody(bytes.NewReader(wire), e, WorkoutType)
	if err != nil || len(events) != 1 {
		t.Fatal(err)
	}
	w := events[0].Workout
	if *w.DurationUs != 3500000000 || *w.ActivityType != 37 || len(w.Events) != 2 || len(w.Statistics) != 1 {
		t.Fatal("workout detail lost")
	}
	canonical, hash := events[0].Canonical()
	var again Event
	if err := DecodeJSON(canonical, &again); err != nil {
		t.Fatal(err)
	}
	b, h := again.Canonical()
	if !bytes.Equal(canonical, b) || !bytes.Equal(hash, h) {
		t.Fatal("workout identity changed")
	}
	_, err = ParseBody(bytes.NewReader(wire), e, sampleType)
	assertCode(t, err, "unsupported_payload")
}

func TestWorkoutRunningDynamicsUnitsAndRoundTrip(t *testing.T) {
	for kind, unit := range map[string]string{
		"HKQuantityTypeIdentifierRunningGroundContactTime":   "ms",
		"HKQuantityTypeIdentifierRunningStrideLength":        "m",
		"HKQuantityTypeIdentifierRunningVerticalOscillation": "m",
	} {
		t.Run(kind, func(t *testing.T) {
			raw := bytes.ReplaceAll(workoutFixture(t), []byte("HKQuantityTypeIdentifierHeartRate"), []byte(kind))
			raw = bytes.ReplaceAll(raw, []byte("count/min"), []byte(unit))
			e, wire := payload(t, raw, 1)
			events, err := ParseBody(bytes.NewReader(wire), e, WorkoutType)
			if err != nil || len(events) != 1 {
				t.Fatal("running ground contact time rejected", err)
			}
			stat := events[0].Workout.Statistics[0]
			if stat.Type != kind || stat.Average.Unit != unit || stat.Average.Value != 140 || stat.Minimum.Value != 90 || stat.Maximum.Value != 170 {
				t.Fatal("source statistic aggregates changed")
			}
			canonical, hash := events[0].Canonical()
			var again Event
			if err := DecodeJSON(canonical, &again); err != nil {
				t.Fatal(err)
			}
			replayed, replayHash := again.Canonical()
			if !bytes.Equal(canonical, replayed) || !bytes.Equal(hash, replayHash) {
				t.Fatal("statistic identity changed")
			}
			wrong := bytes.ReplaceAll(raw, []byte(`"`+unit+`"`), []byte(`"s"`))
			e, wire = payload(t, wrong, 1)
			_, err = ParseBody(bytes.NewReader(wire), e, WorkoutType)
			assertCode(t, err, "unsupported_payload")
		})
	}
}

func TestWorkoutRejectsIncompleteOrUnsupportedFields(t *testing.T) {
	base := string(workoutFixture(t))
	for name, edits := range map[string][2]string{
		"missing_activity":          {`"activityType":37,`, ``},
		"missing_duration":          {`"durationUs":3500000000,`, ``},
		"null_duration":             {`"durationUs":3500000000`, `"durationUs":null`},
		"negative_duration":         {`"durationUs":3500000000`, `"durationUs":-1`},
		"duration_outside_interval": {`"durationUs":3500000000`, `"durationUs":3600000001`},
		"fractional_duration":       {`"durationUs":3500000000`, `"durationUs":0.5`},
		"negative_activity":         {`"activityType":37`, `"activityType":-1`},
		"distance_unit":             {`"unit":"m"`, `"unit":"km"`},
		"missing_total_value":       {`"value":10000,`, ``},
		"null_total_value":          {`"value":10000`, `"value":null`},
		"negative_total":            {`"value":10000`, `"value":-1`},
		"null_total":                {`"totalDistance":{"value":10000,"unit":"m"}`, `"totalDistance":null`},
		"missing_stat_value":        {`"value":140,`, ``},
		"null_stat_value":           {`"value":140`, `"value":null`},
		"null_statistic":            {`"statistics":[`, `"statistics":[null,`},
		"null_statistics":           {`"statistics":[`, `"statistics":null,"ignored":[`},
		"empty_aggregate":           {`"average":{"value":140,"unit":"count/min"}`, `"average":{}`},
		"unknown_statistic":         {`"type":"HKQuantityTypeIdentifierHeartRate"`, `"type":"HKDataTypeIdentifierHeartbeatSeries"`},
		"missing_event_type":        {`"type":1,`, ``},
		"unknown_event_field":       {`"type":1,`, `"type":1,"points":[],`},
		"event_outside_interval":    {`"startUs":1793060200000001`, `"startUs":1793059199999999`},
		"malformed_activity":        {`"activityType":37`, `"activityType":37,"activities":[{}]`},
		"route":                     {`"activityType":37`, `"activityType":37,"route":[]`},
		"series":                    {`"activityType":37`, `"activityType":37,"series":[]`},
		"unknown_workout_field":     {`"activityType":37`, `"activityType":37,"other":1`},
		"duplicate_nested_field":    {`"value":140`, `"value":140,"value":141`},
		"case_alias":                {`"durationUs":3500000000`, `"durationUs":3500000000,"DurationUs":3000000000`},
		"nested_case_alias":         {`"value":140`, `"value":140,"Value":141`},
		"sample_kind":               {`"kind":"workout"`, `"kind":"sample"`},
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(base, edits[0]) {
				t.Fatal("invalid test mutation")
			}
			raw := strings.Replace(base, edits[0], edits[1], 1)
			e, wire := payload(t, []byte(raw), 1)
			_, err := ParseBody(bytes.NewReader(wire), e, WorkoutType)
			assertCode(t, err, "unsupported_payload")
		})
	}
}

func TestWorkoutBudgetsAndDelete(t *testing.T) {
	var event Event
	if err := DecodeJSON(bytes.TrimSpace(workoutFixture(t)), &event); err != nil {
		t.Fatal(err)
	}
	stat := event.Workout.Statistics[0]
	event.Workout.Statistics = append(event.Workout.Statistics, stat)
	assertCode(t, event.Validate(WorkoutType), "unsupported_payload")
	event.Workout.Statistics = []WorkoutStatistic{stat}
	first := event.Workout.Events[0]
	event.Workout.Events = make([]WorkoutEvent, MaxWorkoutEvents+1)
	for i := range event.Workout.Events {
		event.Workout.Events[i] = first
	}
	assertCode(t, event.Validate(WorkoutType), "unsupported_payload")
	event.Workout.Events = event.Workout.Events[:MaxWorkoutEvents]
	if err := event.Validate(WorkoutType); err != nil {
		t.Fatal(err)
	}
	del := Event{Op: "delete", Kind: "workout", Type: WorkoutType, UUID: event.UUID}
	if err := del.Validate(WorkoutType); err != nil {
		t.Fatal(err)
	}
	del.Workout = event.Workout
	assertCode(t, del.Validate(WorkoutType), "unsupported_payload")
	// Explicit empty arrays have the same identity as absent optional arrays.
	event.Workout.Events, event.Workout.Statistics = nil, nil
	a, _ := event.Canonical()
	event.Workout.Events, event.Workout.Statistics = []WorkoutEvent{}, []WorkoutStatistic{}
	b, _ := event.Canonical()
	if !bytes.Equal(a, b) || !json.Valid(a) {
		t.Fatal("unstable optional array encoding")
	}
}

func TestWorkoutBatchDetailBudget(t *testing.T) {
	var event Event
	if err := DecodeJSON(bytes.TrimSpace(workoutFixture(t)), &event); err != nil {
		t.Fatal(err)
	}
	first := event.Workout.Events[0]
	event.Workout.Statistics = nil
	event.Workout.Events = make([]WorkoutEvent, MaxWorkoutEvents)
	for i := range event.Workout.Events {
		event.Workout.Events[i] = first
	}
	var lines bytes.Buffer
	for range 8 {
		event.UUID = NewID()
		b, _ := event.Canonical()
		lines.Write(b)
		lines.WriteByte('\n')
	}
	e, wire := payload(t, lines.Bytes(), 8)
	_, err := ParseBody(bytes.NewReader(wire), e, WorkoutType)
	assertCode(t, err, "batch_too_large")
}
