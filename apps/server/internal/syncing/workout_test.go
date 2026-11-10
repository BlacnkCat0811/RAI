package syncing

import (
	"bytes"
	"encoding/json"
	"os"
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
	_, wire := payload(t, raw, 1)
	events, err := parseWire(t, wire, WorkoutType)
	if err != nil || len(events) != 1 {
		t.Fatal(err)
	}
	w := events[0].Workout
	if *w.DurationUs != 3500000000 || *w.ActivityType != 37 || len(w.Events) != 2 || len(w.Statistics) != 1 {
		t.Fatal("workout detail lost")
	}
	canonical, hash := events[0].Canonical()
	var again Event
	if err := DecodeStrictJSON(canonical, &again); err != nil {
		t.Fatal(err)
	}
	b, h := again.Canonical()
	if !bytes.Equal(canonical, b) || !bytes.Equal(hash, h) {
		t.Fatal("workout identity changed")
	}
	_, err = parseWire(t, wire, sampleType)
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
			_, wire := payload(t, raw, 1)
			events, err := parseWire(t, wire, WorkoutType)
			if err != nil || len(events) != 1 {
				t.Fatal("running ground contact time rejected", err)
			}
			stat := events[0].Workout.Statistics[0]
			if stat.Type != kind || stat.Average.Unit != unit || stat.Average.Value != 140 || stat.Minimum.Value != 90 || stat.Maximum.Value != 170 {
				t.Fatal("source statistic aggregates changed")
			}
			canonical, hash := events[0].Canonical()
			var again Event
			if err := DecodeStrictJSON(canonical, &again); err != nil {
				t.Fatal(err)
			}
			replayed, replayHash := again.Canonical()
			if !bytes.Equal(canonical, replayed) || !bytes.Equal(hash, replayHash) {
				t.Fatal("statistic identity changed")
			}
			wrong := bytes.ReplaceAll(raw, []byte(`"`+unit+`"`), []byte(`"s"`))
			_, wire = payload(t, wrong, 1)
			_, err = parseWire(t, wire, WorkoutType)
			assertCode(t, err, "unsupported_payload")
		})
	}
}

func TestWorkoutBudgetsAndDelete(t *testing.T) {
	var event Event
	if err := DecodeStrictJSON(bytes.TrimSpace(workoutFixture(t)), &event); err != nil {
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
