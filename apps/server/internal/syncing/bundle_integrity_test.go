package syncing

import (
	"encoding/json"
	"testing"
)

func nativeObservation(b *WorkoutBundle) {
	source := HealthKitSource{BundleID: "com.apple.health", Name: "Synthetic Apple source", Version: "1", ProductType: "Watch7,1", OperatingSystemVersion: "11.0.0", Device: &HealthKitDevice{Manufacturer: "Apple Inc.", Model: "Watch", HardwareVersion: "Watch7,1"}}
	b.Workout.Source.BundleID = source.BundleID
	p := BundleProvenance{Workout: source, Samples: []SampleProvenance{}, Routes: []RouteProvenance{}}
	for i := range b.Samples {
		b.Samples[i].Source.BundleID = source.BundleID
		p.Samples = append(p.Samples, SampleProvenance{SampleType: b.Samples[i].Type, SampleUUID: b.Samples[i].UUID, Source: source})
	}
	for i := range b.Routes {
		b.Routes[i].Source.BundleID = source.BundleID
		p.Routes = append(p.Routes, RouteProvenance{RouteID: b.Routes[i].RouteID, Source: source})
	}
	b.Observation = &BundleObservation{Version: 1, ObservedAtUs: 1, Provenance: p, Coverage: map[string]BundleCoverage{"power": {State: "no_records_observed", QueryCompleted: true, LocalPersisted: true}}}
}
func TestPhase3ObservationStrictSourceCoverageAndPrivacy(t *testing.T) {
	for name, mutate := range map[string]func(*WorkoutBundle){
		"name does not prove watch": func(b *WorkoutBundle) { b.Observation.Provenance.Workout.ProductType = "" },
		"third party watch":         func(b *WorkoutBundle) { b.Observation.Provenance.Workout.BundleID = "third.party" },
		"health phone":              func(b *WorkoutBundle) { b.Observation.Provenance.Workout.ProductType = "iPhone13,4" },
		"missing sample evidence":   func(b *WorkoutBundle) { b.Observation.Provenance.Samples = nil },
		"fake complete": func(b *WorkoutBundle) {
			b.Observation.Coverage["power"] = BundleCoverage{State: "complete", SampleCount: 0, QueryCompleted: true, LocalPersisted: true}
		},
		"unfinished complete": func(b *WorkoutBundle) {
			b.Observation.Coverage["route"] = BundleCoverage{State: "complete", SampleCount: 3, QueryCompleted: false, LocalPersisted: true}
		},
		"private metadata": func(b *WorkoutBundle) { b.Observation.Metadata = map[string]any{"serialNumber": "synthetic-private"} },
		"private sample metadata": func(b *WorkoutBundle) {
			b.Samples[0].Metadata = map[string]any{"serialNumber": "synthetic-private"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			b := validBundle()
			nativeObservation(&b)
			mutate(&b)
			raw, _ := json.Marshal(b)
			var decoded WorkoutBundle
			if DecodeWorkoutBundle(raw, &decoded) == nil {
				t.Fatal("invalid observation accepted")
			}
		})
	}
	b := validBundle()
	nativeObservation(&b)
	raw, _ := json.Marshal(b)
	var object map[string]any
	_ = json.Unmarshal(raw, &object)
	object["observation"].(map[string]any)["provenance"].(map[string]any)["workout"].(map[string]any)["device"].(map[string]any)["localIdentifier"] = "synthetic-private"
	private, _ := json.Marshal(object)
	var decoded WorkoutBundle
	if DecodeWorkoutBundle(private, &decoded) == nil {
		t.Fatal("private device identifier accepted")
	}
	if err := DecodeWorkoutBundle(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	// Parent evidence permits absent child device/product while preserving its
	// own source revision; classification never invents the child's descriptors.
	b.Observation.Provenance.Samples[0].Source.Device = nil
	b.Observation.Provenance.Samples[0].Source.ProductType = ""
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
}
func TestPhase3HeartRateSampleMetadataAllowlist(t *testing.T) {
	b := validBundle()
	nativeObservation(&b)
	b.Samples[0].Metadata = map[string]any{"HKMetadataKeyHeartRateMotionContext": float64(2)}
	raw, _ := json.Marshal(b)
	var decoded WorkoutBundle
	if err := DecodeWorkoutBundle(raw, &decoded); err != nil {
		t.Fatalf("allowlisted heart-rate metadata rejected: %v", err)
	}
	b.Samples[0].Metadata = map[string]any{"HKMetadataKeyHeartRateMotionContext": float64(11)}
	raw, _ = json.Marshal(b)
	if DecodeWorkoutBundle(raw, &decoded) == nil {
		t.Fatal("out-of-range heart-rate metadata accepted")
	}
}
func TestPhase3DuplicateProvenanceCannotHideAnUnattributedMember(t *testing.T) {
	b := validBundle()
	b.Samples = append(b.Samples, b.Samples[0])
	b.Samples[1].UUID = NewID()
	nativeObservation(&b)
	b.Observation.Provenance.Samples[1] = b.Observation.Provenance.Samples[0]
	if b.Validate() == nil {
		t.Fatal("duplicate sample provenance accepted")
	}

	b = validBundle()
	b.Routes = append(b.Routes, b.Routes[0])
	b.Routes[1].RouteID = NewID()
	nativeObservation(&b)
	b.Observation.Provenance.Routes[1] = b.Observation.Provenance.Routes[0]
	if b.Validate() == nil {
		t.Fatal("duplicate route provenance accepted")
	}
}

func TestPhase3GPSRetainsOriginalOrderAndTimeEnvelopeDiagnostics(t *testing.T) {
	b := validBundle()
	start := *b.Workout.StartUs
	end := *b.Workout.EndUs
	stamps := []int64{start - 1, start, start + 2, start + 1, start + 1, start - 2, end + 1}
	for i, stamp := range stamps {
		if i == 0 {
			b.Routes[0].Points = nil
		}
		b.Routes[0].Points = append(b.Routes[0].Points, WorkoutBundlePoint{RecordedAtUs: stamp})
	}
	raw, _ := json.Marshal(b)
	var decoded WorkoutBundle
	if err := DecodeWorkoutBundle(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for i, p := range decoded.Routes[0].Points {
		if p.RecordedAtUs != stamps[i] {
			t.Fatal("source time/order changed")
		}
	}
	if len(decoded.RouteTimeDiagnostics()[b.Routes[0].RouteID]) != 4 {
		t.Fatal("missing anomaly diagnostics")
	}
}
func TestPhase3ExplicitDeletionPresenceAndConflicts(t *testing.T) {
	b := validBundle()
	b.SampleDeletions = []SampleDeletion{{SampleType: b.Samples[0].Type, SampleUUID: NewID()}}
	b.RouteDeletions = []string{NewID()}
	raw, _ := json.Marshal(b)
	var decoded WorkoutBundle
	if err := DecodeWorkoutBundle(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	b.SampleDeletions[0].SampleUUID = b.Samples[0].UUID
	if b.Validate() == nil {
		t.Fatal("upsert and delete same sample accepted")
	}
	b = validBundle()
	b.RouteDeletions = []string{b.Routes[0].RouteID}
	if b.Validate() == nil {
		t.Fatal("upsert and delete same route accepted")
	}
}

func TestPhase3DeviceScopedAppleHealthSourceRequiresRealWatchEvidence(t *testing.T) {
	source := HealthKitSource{BundleID: "com.apple.health." + NewID(), ProductType: "Watch8,3", Version: "1", Device: &HealthKitDevice{Manufacturer: "Apple Inc.", Model: "Watch"}}
	if source.Classification(nil) != "apple_watch" {
		t.Fatal("device scoped Watch evidence not recognized")
	}
	child := source
	child.Device = nil
	if child.Classification(nil) != "source_unknown" || child.Classification(&source) != "associated_apple_watch" {
		t.Fatal("missing child device fabricated or association lost")
	}
	child = source
	child.ProductType = "iPhone13,4"
	if child.Classification(&source) != "source_unknown" {
		t.Fatal("contradictory product hidden by parent")
	}
	child = source
	child.BundleID = "com.apple.health.not-a-device-uuid"
	if child.Classification(nil) != "source_unknown" {
		t.Fatal("arbitrary prefix accepted")
	}
}

func TestPhase3QuantityMetadataRetainsStrictJSONNumbers(t *testing.T) {
	b := validBundle()
	nativeObservation(&b)
	b.Observation.Metadata = map[string]any{"HKAverageMETs": map[string]any{"value": 8.0, "unit": "kcal/(kg*hr)"}, "HKWeatherTemperature": map[string]any{"value": 0.0, "unit": "degC"}}
	b.Workout.Metadata = b.Observation.Metadata
	raw, _ := json.Marshal(b)
	var decoded WorkoutBundle
	if err := DecodeWorkoutBundle(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	decoded.Observation.Metadata["HKAverageMETs"].(map[string]any)["value"] = "8"
	if decoded.Validate() == nil {
		t.Fatal("numeric string accepted")
	}
}

func TestPhase3MetadataEnrichmentCannotHideMicrosecondCoreChange(t *testing.T) {
	b := validBundle()
	start, end, duration := int64(253402300799999899), int64(253402300799999999), int64(0)
	b.Workout.StartUs = &start
	b.Workout.EndUs = &end
	b.Workout.Workout.DurationUs = &duration
	same := b.Workout
	same.Metadata = map[string]any{"HKIndoorWorkout": false}
	if !WorkoutCoreEqual(b.Workout, same) {
		t.Fatal("metadata-only observation rejected")
	}
	changed := same
	changedStart := start + 1
	changed.StartUs = &changedStart
	if WorkoutCoreEqual(b.Workout, changed) {
		t.Fatal("metadata comparison rounded away an immutable microsecond change")
	}
}
func TestPhase3CoverageOptionalFieldSpellingIsExact(t *testing.T) {
	b := validBundle()
	nativeObservation(&b)
	raw, _ := json.Marshal(b)
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	coverage := object["observation"].(map[string]any)["coverage"].(map[string]any)
	coverage["power"].(map[string]any)["ErrorCode"] = "synthetic_query_error"
	wrong, _ := json.Marshal(object)
	var decoded WorkoutBundle
	if err := DecodeWorkoutBundle(wrong, &decoded); err == nil {
		t.Fatal("case-insensitive optional coverage field accepted")
	}
}
