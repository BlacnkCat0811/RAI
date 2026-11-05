package syncing

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

type registryFixture struct {
	Contract  string `json:"contract"`
	Version   int    `json:"version"`
	WireField string `json:"wireField"`
	MaxKeys   int    `json:"maxKeysPerObject"`
	TextMax   int    `json:"textMaxBytes"`
	Keys      []struct {
		Key          string   `json:"key"`
		Kind         string   `json:"kind"`
		Unit         string   `json:"unit"`
		MaxBytes     int      `json:"maxBytes"`
		IntroducedIn int      `json:"introducedIn"`
		Scopes       []string `json:"scopes"`
	} `json:"keys"`
	Vectors []struct {
		Name     string          `json:"name"`
		Scope    string          `json:"scope"`
		Wire     json.RawMessage `json:"wire"`
		RawJSON  string          `json:"rawJSON"`
		Expect   string          `json:"expect"`
		SynthKey int             `json:"syntheticKeys"`
	} `json:"vectors"`
}

func loadRegistryFixture(t *testing.T) registryFixture {
	t.Helper()
	raw, err := os.ReadFile("../../../../contracts/fixtures/workout-metadata-registry-v2.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture registryFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

// bundleWithMetadata injects a metadata object at one Workout metadata scope and
// returns the exact wire bytes, so the test exercises the real strict decoder.
func bundleWithMetadata(t *testing.T, scope string, metadata json.RawMessage, version *int) []byte {
	t.Helper()
	bundle := validBundle()
	// The observation record is what carries per-scope metadata validation for
	// supported clients, so the fixture always includes one.
	nativeObservation(&bundle)
	encoded, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var doc map[string]any
	if err := decoder.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if version != nil {
		doc["metadataRegistryVersion"] = *version
	}
	workout := doc["workout"].(map[string]any)
	switch scope {
	case "workout":
		workout["metadata"] = metadata
	case "activity", "activityEvent":
		inner := workout["workout"].(map[string]any)
		activity := map[string]any{
			"uuid": NewID(), "startUs": workout["startUs"], "endUs": workout["endUs"], "durationUs": inner["durationUs"],
			"configuration": map[string]any{"activityType": 37, "locationType": 1, "swimmingLocationType": 0},
		}
		if scope == "activity" {
			activity["metadata"] = metadata
		} else {
			activity["events"] = []any{map[string]any{"type": 1, "startUs": workout["startUs"], "endUs": workout["endUs"], "metadata": metadata}}
		}
		inner["activities"] = []any{activity}
	case "workoutEvent":
		inner := workout["workout"].(map[string]any)
		inner["events"] = []any{map[string]any{"type": 1, "startUs": workout["startUs"], "endUs": workout["endUs"], "metadata": metadata}}
	default:
		t.Fatalf("unknown scope %s", scope)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestMetadataRegistryMatchesContractFixture(t *testing.T) {
	fixture := loadRegistryFixture(t)
	if fixture.Contract != "rai.workout.metadata_registry" || fixture.WireField != "metadataRegistryVersion" {
		t.Fatal("contract fixture identity changed")
	}
	if int64(fixture.Version) != WorkoutMetadataRegistryVersion || fixture.MaxKeys != WorkoutMetadataRegistryV1MaxKeys || fixture.TextMax != metadataTextMaxBytes {
		t.Fatalf("Go registry %d/%d/%d does not match contract %d/%d/%d", WorkoutMetadataRegistryVersion,
			WorkoutMetadataRegistryV1MaxKeys, metadataTextMaxBytes, fixture.Version, fixture.MaxKeys, fixture.TextMax)
	}
	known := map[string]bool{}
	for _, key := range fixture.Keys {
		known[key.Key] = true
		switch key.Kind {
		case "text":
			if key.MaxBytes == 0 || (key.Key != "HKTimeZone" && !opaqueActivityTextKeys[key.Key]) {
				t.Fatalf("text key %s has no Go contract", key.Key)
			}
		case "signedInteger":
			if !opaqueActivityIntegerKeys[key.Key] {
				t.Fatalf("integer key %s has no Go contract", key.Key)
			}
		default:
			if !metadataKeys[key.Key] {
				t.Fatalf("version 1 key %s is not in the Go allowlist", key.Key)
			}
		}
	}
	for key := range opaqueActivityTextKeys {
		if !known[key] {
			t.Fatalf("Go accepts %s outside the shared contract", key)
		}
	}
	for key := range opaqueActivityIntegerKeys {
		if !known[key] {
			t.Fatalf("Go accepts %s outside the shared contract", key)
		}
	}
}

func TestMetadataRegistryGoldenVectors(t *testing.T) {
	fixture := loadRegistryFixture(t)
	version := WorkoutMetadataRegistryVersion
	for _, vector := range fixture.Vectors {
		vector := vector
		t.Run(vector.Name, func(t *testing.T) {
			metadata := vector.Wire
			if vector.RawJSON != "" {
				metadata = json.RawMessage(vector.RawJSON)
			}
			if vector.SynthKey > 0 {
				fields := map[string]any{}
				for i := 0; i < vector.SynthKey; i++ {
					fields[NewID()] = "synthetic"
				}
				encoded, err := json.Marshal(fields)
				if err != nil {
					t.Fatal(err)
				}
				metadata = encoded
			}
			raw := bundleWithMetadata(t, vector.Scope, metadata, &version)
			var bundle WorkoutBundle
			err := DecodeWorkoutBundle(raw, &bundle)
			switch vector.Expect {
			case "accepted":
				if err != nil {
					t.Fatalf("vector %s rejected: %v", vector.Name, err)
				}
			case "rejected":
				if err == nil {
					t.Fatalf("vector %s accepted", vector.Name)
				}
			default:
				t.Fatalf("vector %s has unknown expectation %s", vector.Name, vector.Expect)
			}
		})
	}
}

func TestMetadataRegistryVersionGate(t *testing.T) {
	fixture := loadRegistryFixture(t)
	metadata := json.RawMessage(`{"WOIntervalStepSuccessful":1}`)
	for _, key := range fixture.Keys {
		if key.IntroducedIn < 2 {
			continue
		}
		_ = key
	}
	version := 2
	if err := DecodeWorkoutBundle(bundleWithMetadata(t, "activity", metadata, &version), &WorkoutBundle{}); err != nil {
		t.Fatalf("declared version 2 rejected: %v", err)
	}
	// Absent declaration means version 1: a version 2 key must not be accepted.
	if err := DecodeWorkoutBundle(bundleWithMetadata(t, "activity", metadata, nil), &WorkoutBundle{}); err == nil {
		t.Fatal("version 2 metadata accepted without a declared registry version")
	}
	legacy := 1
	if err := DecodeWorkoutBundle(bundleWithMetadata(t, "activity", metadata, &legacy), &WorkoutBundle{}); err == nil {
		t.Fatal("version 2 metadata accepted under registry version 1")
	}
	for _, unknown := range []int{0, 3, 99} {
		value := unknown
		if err := DecodeWorkoutBundle(bundleWithMetadata(t, "activity", metadata, &value), &WorkoutBundle{}); err == nil {
			t.Fatalf("unknown registry version %d accepted", unknown)
		}
	}
	// A version 1 payload stays valid without the field, exactly as before.
	if err := DecodeWorkoutBundle(bundleWithMetadata(t, "activity", json.RawMessage(`{"HKTimeZone":"Etc/UTC"}`), nil), &WorkoutBundle{}); err != nil {
		t.Fatalf("legacy version 1 bundle rejected: %v", err)
	}
}

// Preservation: the exact native string and the exact signed integer survive
// decode and re-encode without normalization, truncation or reinterpretation.
func TestMetadataRegistryPreservesOpaqueValues(t *testing.T) {
	version := WorkoutMetadataRegistryVersion
	metadata := json.RawMessage(`{"WOIntervalStepKeyPath":"synthetic-步-1","WOIntervalStepSuccessful":-1}`)
	raw := bundleWithMetadata(t, "activity", metadata, &version)
	var bundle WorkoutBundle
	if err := DecodeWorkoutBundle(raw, &bundle); err != nil {
		t.Fatal(err)
	}
	activity := bundle.Workout.Workout.Activities[0]
	if activity.Metadata["WOIntervalStepKeyPath"] != "synthetic-步-1" {
		t.Fatalf("string metadata changed: %v", activity.Metadata["WOIntervalStepKeyPath"])
	}
	number, ok := metadataInteger(activity.Metadata["WOIntervalStepSuccessful"])
	if !ok || number != -1 {
		t.Fatalf("integer metadata changed: %v", activity.Metadata["WOIntervalStepSuccessful"])
	}
	encoded, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var again WorkoutBundle
	if err := DecodeWorkoutBundle(encoded, &again); err != nil {
		t.Fatalf("re-encoded bundle rejected: %v", err)
	}
	if again.Workout.Workout.Activities[0].Metadata["WOIntervalStepKeyPath"] != "synthetic-步-1" {
		t.Fatal("replay lost the exact string")
	}
}

// The fix must not become "allow arbitrary future metadata".
func TestMetadataRegistryUnknownKeysStillFailClosed(t *testing.T) {
	version := WorkoutMetadataRegistryVersion
	for _, scope := range []string{"workout", "activity", "activityEvent", "workoutEvent"} {
		raw := bundleWithMetadata(t, scope, json.RawMessage(`{"synthetic_future_key":"synthetic"}`), &version)
		if err := DecodeWorkoutBundle(raw, &WorkoutBundle{}); err == nil {
			t.Fatalf("unknown key accepted at scope %s", scope)
		}
	}
}
