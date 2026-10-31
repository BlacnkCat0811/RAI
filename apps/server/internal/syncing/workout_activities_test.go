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
	if err := DecodeJSON(bytes.TrimSpace(activityFixture(t)), &event); err != nil {
		t.Fatal(err)
	}
	return event
}

func TestWorkoutActivitiesRoundTripAndIdentity(t *testing.T) {
	raw := activityFixture(t)
	envelope, wire := payload(t, raw, 1)
	events, err := ParseBody(bytes.NewReader(wire), envelope, WorkoutType)
	if err != nil || len(events) != 1 {
		t.Fatal(err)
	}
	event := events[0]
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
	if err := DecodeJSON(canonical, &again); err != nil {
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

func TestWorkoutActivitiesRejectInvalidPayloads(t *testing.T) {
	base := string(activityFixture(t))
	for name, edit := range map[string][2]string{
		"null_activities":           {string(bytes.TrimSpace(activityFixture(t)))[strings.Index(base, `"activities":`) : len(strings.TrimSpace(base))-2], `"activities":null`},
		"null_activity":             {`"activities":[`, `"activities":[null,`},
		"missing_uuid":              {`"uuid":"ffffffff-ffff-4fff-8fff-fffffffffff1",`, ``},
		"duplicate_uuid":            {`"uuid":"00000000-0000-4000-8000-000000000002"`, `"uuid":"ffffffff-ffff-4fff-8fff-fffffffffff1"`},
		"invalid_uuid":              {`"uuid":"ffffffff-ffff-4fff-8fff-fffffffffff1"`, `"uuid":"00000000-0000-0000-0000-000000000000"`},
		"missing_start":             {`"uuid":"ffffffff-ffff-4fff-8fff-fffffffffff1","startUs":1793059200000001`, `"uuid":"ffffffff-ffff-4fff-8fff-fffffffffff1"`},
		"start_before_parent":       {`"uuid":"ffffffff-ffff-4fff-8fff-fffffffffff1","startUs":1793059200000001`, `"uuid":"ffffffff-ffff-4fff-8fff-fffffffffff1","startUs":1793059200000000`},
		"end_after_parent":          {`"endUs":1793060200000001,"durationUs":900000000`, `"endUs":1793062800000002,"durationUs":900000000`},
		"end_before_start":          {`"endUs":1793060200000001,"durationUs":900000000`, `"endUs":1793059200000000,"durationUs":900000000`},
		"null_end":                  {`"endUs":1793060200000001,"durationUs":900000000`, `"endUs":null,"durationUs":900000000`},
		"missing_duration":          {`"durationUs":900000000,`, ``},
		"null_duration":             {`"durationUs":900000000`, `"durationUs":null`},
		"negative_duration":         {`"durationUs":900000000`, `"durationUs":-1`},
		"duration_exceeds_activity": {`"durationUs":900000000`, `"durationUs":1000000001`},
		"duration_with_absent_end_exceeds_parent": {`"durationUs":2400000000`, `"durationUs":2600000001`},
		"missing_configuration":                   {`"configuration":{"activityType":46,"locationType":1,"swimmingLocationType":1,"lapLength":{"value":25,"unit":"m"}},`, ``},
		"null_configuration":                      {`"configuration":{"activityType":46,"locationType":1,"swimmingLocationType":1,"lapLength":{"value":25,"unit":"m"}}`, `"configuration":null`},
		"missing_location":                        {`"locationType":1,`, ``},
		"null_location":                           {`"locationType":1`, `"locationType":null`},
		"negative_location":                       {`"locationType":1`, `"locationType":-1`},
		"overflow_location":                       {`"locationType":1`, `"locationType":2147483648`},
		"missing_swimming_location":               {`"swimmingLocationType":1,`, ``},
		"null_swimming_location":                  {`"swimmingLocationType":1`, `"swimmingLocationType":null`},
		"negative_swimming_location":              {`"swimmingLocationType":1`, `"swimmingLocationType":-1`},
		"missing_configuration_activity":          {`"activityType":46,`, ``},
		"zero_configuration_activity":             {`"activityType":46`, `"activityType":0`},
		"fractional_configuration_activity":       {`"activityType":46`, `"activityType":46.5`},
		"missing_lap_value":                       {`"value":25,`, ``},
		"null_lap_value":                          {`"value":25`, `"value":null`},
		"negative_lap":                            {`"value":25`, `"value":-1`},
		"lap_unit":                                {`"value":25,"unit":"m"`, `"value":25,"unit":"km"`},
		"null_lap":                                {`"lapLength":{"value":25,"unit":"m"}`, `"lapLength":null`},
		"activity_stat_outside":                   {`"startUs":1793059200000001,"endUs":1793060200000001,"average"`, `"startUs":1793059200000000,"endUs":1793060200000001,"average"`},
		"activity_stat_missing_value":             {`"value":125,`, ``},
		"activity_event_outside_parent":           {`"startUs":1793059201000001,"endUs":1793059201000001`, `"startUs":1793062800000002,"endUs":1793062800000002`},
		"configuration_unknown":                   {`"locationType":1`, `"locationType":1,"unknown":true`},
		"configuration_case_alias":                {`"locationType":1`, `"locationType":1,"LocationType":2`},
		"configuration_duplicate":                 {`"locationType":1`, `"locationType":1,"locationType":2`},
		"zone":                                    {`"durationUs":900000000`, `"durationUs":900000000,"zoneGroupsByType":{}`},
		"series":                                  {`"durationUs":900000000`, `"durationUs":900000000,"series":[]`},
	} {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(base, edit[0]) {
				t.Fatal("invalid mutation")
			}
			raw := strings.Replace(base, edit[0], edit[1], 1)
			e, wire := payload(t, []byte(raw), 1)
			_, err := ParseBody(bytes.NewReader(wire), e, WorkoutType)
			assertCode(t, err, "unsupported_payload")
			failure, ok := err.(*Error)
			if !ok || failure.EventIndex == nil || *failure.EventIndex != 0 {
				t.Fatal("missing row diagnostic")
			}
		})
	}
}

func TestWorkoutActivityBudgets(t *testing.T) {
	event := activityEvent(t)
	activity := event.Workout.Activities[0]
	activity.Statistics, activity.Events = nil, nil
	event.Workout.Activities = make([]WorkoutActivity, MaxWorkoutActivities)
	for i := range event.Workout.Activities {
		event.Workout.Activities[i] = activity
		event.Workout.Activities[i].UUID = NewID()
	}
	if err := event.Validate(WorkoutType); err != nil {
		t.Fatal("maximum activity count rejected", err)
	}
	event.Workout.Activities = append(event.Workout.Activities, activity)
	assertCode(t, event.Validate(WorkoutType), "unsupported_payload")
	event = activityEvent(t)
	activity = event.Workout.Activities[0]
	detail := activity.Events[0]
	activity.Statistics = nil
	activity.Events = make([]WorkoutEvent, 249)
	for i := range activity.Events {
		activity.Events[i] = detail
	}
	event.Workout.Statistics, event.Workout.Events = nil, nil
	event.Workout.Activities = []WorkoutActivity{activity}
	var lines bytes.Buffer
	for range 8 {
		b, _ := event.Canonical()
		lines.Write(b)
		lines.WriteByte('\n')
	}
	envelope, wire := payload(t, lines.Bytes(), 8)
	if _, err := ParseBody(bytes.NewReader(wire), envelope, WorkoutType); err != nil {
		t.Fatal("2000 detail limit rejected", err)
	}
	// Duplicate UUIDs still consume the input budget before delete-wins/collapse.
	event.Workout.Activities[0].Events = append(activity.Events, detail)
	extra, _ := event.Canonical()
	rows := bytes.Split(lines.Bytes(), []byte{'\n'})
	rows[0] = extra
	envelope, wire = payload(t, bytes.Join(rows, []byte{'\n'}), 8)
	_, err := ParseBody(bytes.NewReader(wire), envelope, WorkoutType)
	assertCode(t, err, "batch_too_large")
	// Line and per-activity limits continue to apply inside activities.
	event.Workout.Activities[0].Events = make([]WorkoutEvent, MaxWorkoutEvents+1)
	assertCode(t, event.Validate(WorkoutType), "unsupported_payload")
	event = activityEvent(t)
	event.Workout.Activities[0].Metadata["padding"] = strings.Repeat("x", MaxLineBytes)
	raw, _ := event.Canonical()
	envelope, wire = payload(t, raw, 1)
	_, err = ParseBody(bytes.NewReader(wire), envelope, WorkoutType)
	assertCode(t, err, "event_too_large")
}
