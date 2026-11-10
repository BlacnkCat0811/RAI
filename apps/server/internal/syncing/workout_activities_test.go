package syncing

import (
	"bytes"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

func activityFixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../../../../contracts/fixtures/workout-activities.ndjson")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func activityEvent(t *testing.T) Event {
	t.Helper()
	var event Event
	if err := DecodeStrictJSON(bytes.TrimSpace(activityFixture(t)), &event); err != nil {
		t.Fatal(err)
	}
	return event
}

func TestWorkoutActivitiesRoundTripAndIdentity(t *testing.T) {
	raw := activityFixture(t)
	event := activityEvent(t)
	if err := event.Validate(WorkoutType); err != nil {
		t.Fatal(err)
	}
	activities := event.Workout.Activities
	if len(activities) != 2 || activities[1].EndUs != nil || activities[0].Configuration.LapLength.Value != 25 || *activities[0].Configuration.SwimmingLocationType != 1 || len(activities[1].Statistics) != 1 || len(activities[1].Events) != 1 {
		t.Fatal("activity data lost")
	}
	canonical, hash := event.Canonical()
	expected, err := os.ReadFile("../../../../contracts/fixtures/workout-activities.sha256")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonical, bytes.TrimSpace(raw)) || hex.EncodeToString(hash) != strings.TrimSpace(string(expected)) {
		t.Fatal("Python/Go canonical identity differs")
	}
	var again Event
	if err := DecodeStrictJSON(canonical, &again); err != nil {
		t.Fatal(err)
	}
	other, _ := again.Canonical()
	if !bytes.Equal(canonical, other) {
		t.Fatal("activity round trip changed identity")
	}
	// Source order is part of immutable identity, not UUID sort order.
	event.Workout.Activities[0], event.Workout.Activities[1] = activities[1], activities[0]
	_, changed := event.Canonical()
	if bytes.Equal(hash, changed) {
		t.Fatal("activity order excluded from identity")
	}
	// Absent and empty activity arrays do not change legacy v1 identities.
	legacy := activityEvent(t)
	legacy.Workout.Activities = nil
	a, _ := legacy.Canonical()
	legacy.Workout.Activities = []WorkoutActivity{}
	b, _ := legacy.Canonical()
	if !bytes.Equal(a, b) {
		t.Fatal("empty activity array changes v1 identity")
	}
	old := bytes.TrimSpace(workoutFixture(t))
	if !bytes.Equal(a, old) {
		t.Fatal("legacy canonical bytes changed")
	}
}

func TestWorkoutActivityEventsUseParentWindow(t *testing.T) {
	event := activityEvent(t)
	// HealthKit segment events can start after the child activity ends while
	// still belonging to the parent workout interval.
	start, end := int64(1793060200000002), int64(1793060200000002)
	event.Workout.Activities[0].Events[0].StartUs = &start
	event.Workout.Activities[0].Events[0].EndUs = &end
	if err := event.Validate(WorkoutType); err != nil {
		t.Fatal("parent-bounded activity segment rejected", err)
	}
}
