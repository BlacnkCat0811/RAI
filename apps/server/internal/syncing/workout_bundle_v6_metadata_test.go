package syncing_test

// Cross-language contract check for the protocol-6 top-level metadata: the
// bytes the iOS client builds (contracts/fixtures/workout-bundle-v6-metadata.json)
// must survive the server decoder and reach the analysis input, because the
// environment projection (indoor/outdoor, weather) reads only this field.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"os"
	"reflect"
	"testing"

	"rai/server/internal/syncing"
	"rai/server/internal/workoutfeatures"
)

const metadataFixture = "../../../../contracts/fixtures/workout-bundle-v6-metadata.json"

func decodeMetadataFixture(t *testing.T) syncing.WorkoutBundle {
	t.Helper()
	raw, err := os.ReadFile(metadataFixture)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	bundle, err := syncing.DecodeWorkoutBundleV6JSON(raw)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return bundle
}

func digestOf(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func number(t *testing.T, value *float64) float64 {
	t.Helper()
	if value == nil {
		t.Fatal("expected a quantity")
	}
	return *value
}

func assertMetadata(t *testing.T, metadata map[string]any) {
	t.Helper()
	if metadata == nil {
		t.Fatal("top-level metadata must survive the protocol-6 decode")
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	var got, want map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("re-decode metadata: %v", err)
	}
	const expected = `{"HKIndoorWorkout":false,"HKWeatherHumidity":{"value":0.5,"unit":"1"},"HKWeatherTemperature":{"value":16,"unit":"degC"}}`
	if err := json.Unmarshal([]byte(expected), &want); err != nil {
		t.Fatalf("decode expectation: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("metadata got=%s want=%s", encoded, expected)
	}
}

// snapshot mirrors what the storage layer assembles from the decoded bundle:
// the event metadata, the workout facts and the projected source.
func snapshot(t *testing.T, bundle syncing.WorkoutBundle, keepMetadata bool) workoutfeatures.RawWorkoutSnapshot {
	t.Helper()
	if bundle.Workout.StartUs == nil || bundle.Workout.EndUs == nil || bundle.Workout.Workout == nil {
		t.Fatal("fixture workout must carry a time range and a core payload")
	}
	source := workoutfeatures.NewSource("com.apple.health", "apple_watch",
		json.RawMessage(`{"bundleID":"com.apple.health","productType":"Watch7,3","version":"11.2"}`))
	s := workoutfeatures.RawWorkoutSnapshot{
		DatasetID:        bundle.DatasetID,
		WorkoutID:        bundle.WorkoutID,
		Generation:       1,
		Revision:         1,
		AsOf:             *bundle.Workout.EndUs + 1e6,
		Policy:           workoutfeatures.DefaultPolicy(),
		AlgorithmVersion: workoutfeatures.Algorithm + "/" + workoutfeatures.Version,
		Fact: workoutfeatures.FactRef{Kind: "workout", ID: bundle.WorkoutID,
			Hash: digestOf(bundle.WorkoutID), State: workoutfeatures.Present},
		Start:      *bundle.Workout.StartUs,
		End:        *bundle.Workout.EndUs,
		Provenance: []workoutfeatures.Source{source},
	}
	if core := bundle.Workout.Workout; core != nil {
		if core.DurationUs != nil {
			s.DurationUs = *core.DurationUs
		}
		if core.ActivityType != nil {
			s.ActivityType = *core.ActivityType
		}
		if core.TotalDistance != nil {
			distance := core.TotalDistance.Value
			s.Distance = &distance
		}
	}
	if keepMetadata && bundle.Workout.Metadata != nil {
		encoded, err := json.Marshal(bundle.Workout.Metadata)
		if err != nil {
			t.Fatalf("marshal metadata: %v", err)
		}
		s.Metadata = encoded
	}
	for _, sample := range bundle.Samples {
		if sample.Quantity == nil || sample.StartUs == nil || sample.EndUs == nil {
			continue
		}
		s.Quantities = append(s.Quantities, workoutfeatures.Quantity{
			Fact: workoutfeatures.FactRef{Kind: "quantity", ID: sample.UUID,
				Hash: digestOf(sample.UUID), State: workoutfeatures.Present},
			Type:    workoutfeatures.HR,
			Start:   *sample.StartUs,
			End:     *sample.EndUs,
			Value:   sample.Quantity.Value,
			Unit:    sample.Quantity.Unit,
			Source:  source.Epoch,
			Support: workoutfeatures.IntervalObservation,
		})
	}
	return s
}

func computeSnapshot(t *testing.T, s workoutfeatures.RawWorkoutSnapshot) workoutfeatures.WorkoutFeatures {
	t.Helper()
	features, err := workoutfeatures.Compute(context.Background(), s)
	if err != nil {
		t.Fatalf("compute: %v", err)
	}
	return features
}

func TestV6MetadataReachesEnvironmentPresent(t *testing.T) {
	bundle := decodeMetadataFixture(t)
	assertMetadata(t, bundle.Workout.Metadata)

	features := computeSnapshot(t, snapshot(t, bundle, true))
	if features.Environment.State != workoutfeatures.Present {
		t.Fatalf("environment state=%q want present", features.Environment.State)
	}
	if features.Environment.Indoor == nil || *features.Environment.Indoor {
		t.Fatalf("indoor=%v want false", features.Environment.Indoor)
	}
	if got := number(t, features.Environment.TemperatureC); math.Abs(got-16) > 1e-9 {
		t.Fatalf("temperature=%v want 16", got)
	}
	if got := number(t, features.Environment.Humidity); math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("humidity=%v want 0.5", got)
	}
}

func TestV6MetadataAbsenceLeavesEnvironmentMissing(t *testing.T) {
	bundle := decodeMetadataFixture(t)
	features := computeSnapshot(t, snapshot(t, bundle, false))
	if features.Environment.State != workoutfeatures.Missing {
		t.Fatalf("environment state=%q want missing", features.Environment.State)
	}
	if features.Environment.Indoor != nil || features.Environment.TemperatureC != nil || features.Environment.Humidity != nil {
		t.Fatalf("environment=%+v want every field unset", features.Environment)
	}
}
