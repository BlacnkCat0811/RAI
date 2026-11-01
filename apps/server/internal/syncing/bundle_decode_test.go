package syncing

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkoutBundleNestedPresenceAndLegalZero(t *testing.T) {
	b := validBundle()
	zero := int64(0)
	one := int64(1)
	activity := int64(37)
	b.Samples[0].Quantity.Value = 0
	b.Series[0].Points[0].Value = 0
	b.Routes[0].Points[0].Latitude = 0
	b.Routes[0].Points[0].Longitude = 0
	w := b.Workout.Workout
	w.TotalDistance = &Quantity{Value: 0, Unit: "m"}
	w.TotalEnergyBurned = &Quantity{Value: 0, Unit: "kcal"}
	w.Statistics = []WorkoutStatistic{{Type: b.Samples[0].Type, StartUs: b.Workout.StartUs, EndUs: b.Workout.EndUs, Sum: &Quantity{Value: 0, Unit: "count/min"}}}
	w.Events = []WorkoutEvent{{Type: &one, StartUs: b.Workout.StartUs, EndUs: b.Workout.EndUs}}
	w.Activities = []WorkoutActivity{{UUID: NewID(), StartUs: b.Workout.StartUs, DurationUs: &zero, Configuration: &WorkoutConfiguration{ActivityType: &activity, LocationType: &zero, SwimmingLocationType: &zero, LapLength: &Quantity{Value: 0, Unit: "m"}}}}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var decoded WorkoutBundle
	if err := DecodeWorkoutBundle(raw, &decoded); err != nil {
		t.Fatal("legal zero/optional omission rejected", err)
	}
	if decoded.Samples[0].Quantity.Value != 0 || decoded.Routes[0].Points[0].Latitude != 0 || decoded.Workout.Workout.Activities[0].EndUs != nil {
		t.Fatal("original zero or missing optional value changed")
	}
	for _, path := range []string{
		"workoutID", "workout.workout.activityType", "workout.workout.durationUs",
		"workout.workout.totalDistance.value", "workout.workout.totalDistance.unit",
		"workout.workout.totalEnergyBurned.value", "workout.workout.statistics.0.type",
		"workout.workout.statistics.0.startUs", "workout.workout.statistics.0.endUs", "workout.workout.statistics.0.sum.value",
		"workout.workout.events.0.type", "workout.workout.events.0.startUs", "workout.workout.events.0.endUs",
		"workout.workout.activities.0.uuid", "workout.workout.activities.0.startUs", "workout.workout.activities.0.durationUs",
		"workout.workout.activities.0.configuration", "workout.workout.activities.0.configuration.activityType",
		"workout.workout.activities.0.configuration.locationType", "workout.workout.activities.0.configuration.swimmingLocationType",
		"workout.workout.activities.0.configuration.lapLength.value", "series.0.sampleType", "series.0.sampleUUID",
		"series.0.points.0.startUs", "series.0.points.0.endUs", "series.0.points.0.value", "series.0.points.0.unit",
		"routes.0.routeID", "routes.0.source.bundleID", "samples.0.quantity.value", "samples.0.quantity.unit",
	} {
		for _, null := range []bool{false, true} {
			name := path + "/missing"
			if null {
				name = path + "/null"
			}
			t.Run(name, func(t *testing.T) {
				var object any
				_ = json.Unmarshal(raw, &object)
				keys := strings.Split(path, ".")
				current := object
				field := ""
				for _, key := range keys[:len(keys)-1] {
					if items, ok := current.([]any); ok {
						current = items[0]
						field += "[0]"
					} else {
						current = current.(map[string]any)[key]
						if field != "" {
							field += "."
						}
						field += key
					}
				}
				key := keys[len(keys)-1]
				field += "." + key
				if len(keys) == 1 {
					field = key
				}
				if null {
					current.(map[string]any)[key] = nil
				} else {
					delete(current.(map[string]any), key)
				}
				invalid, _ := json.Marshal(object)
				var target WorkoutBundle
				err := DecodeWorkoutBundle(invalid, &target)
				failure, ok := err.(*Error)
				reason := "required"
				if null {
					reason = "null_not_allowed"
				}
				if !ok || failure.Code != "unsupported_payload" || failure.Field != field || failure.Reason != reason {
					t.Fatalf("path=%s err=%+v", field, err)
				}
			})
		}
	}
}
