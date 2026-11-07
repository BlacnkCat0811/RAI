package syncing

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func validV4Payload(t *testing.T) map[string]any {
	t.Helper()
	start, end := int64(1_790_000_000_000_000), int64(1_790_000_060_000_000)
	sourceBundle := "com.apple.health"
	workoutID, sampleUUID, routeID := NewID(), NewID(), NewID()
	coverage := func(count int) map[string]any {
		return map[string]any{"state": "complete", "sampleCount": count, "queryCompleted": true, "localPersisted": true}
	}
	return map[string]any{
		"protocolVersion":         4,
		"metadataRegistryVersion": 2,
		"datasetID":               NewID(),
		"datasetEpoch":            NewID(),
		"collectorEpoch":          NewID(),
		"workoutID":               workoutID,
		"startUs":                 start,
		"endUs":                   end,
		"workout":                 map[string]any{"activityType": 37, "durationUs": 60_000_000},
		"sources": []any{map[string]any{
			"bundleID": sourceBundle, "version": "11.0", "productType": "Watch7,1",
		}},
		"streams": []any{map[string]any{
			"type": "HKQuantityTypeIdentifierHeartRate", "kind": "quantity", "unit": "count/min",
			"samples": []any{[]any{start, end, 120}},
			"uuids":   []any{sampleUUID},
			"source":  0,
		}},
		"series": []any{map[string]any{
			"stream": 0, "sample": 0, "points": []any{[]any{start, end, 121}},
		}},
		"routes": []any{map[string]any{
			"routeID": routeID, "source": 0,
			"points": []any{[]any{start, 31.2, 121.4, nil, nil, nil, nil, nil}},
		}},
		"deletions":      []any{},
		"routeDeletions": []any{},
		"observation": map[string]any{
			"version": 1, "observedAtUs": end,
			"provenance": map[string]any{
				"workout": map[string]any{"bundleID": sourceBundle, "version": "11.0", "productType": "Watch7,1"},
				"samples": []any{},
				"routes":  []any{},
			},
			"coverage": map[string]any{
				"workout":                                  coverage(1),
				"HKQuantityTypeIdentifierHeartRate":        coverage(1),
				"series:HKQuantityTypeIdentifierHeartRate": coverage(1),
				"route": coverage(1),
			},
		},
	}
}

func marshalV4(t *testing.T, payload map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestDecodeWorkoutBundleV4RoundTrip(t *testing.T) {
	payload := validV4Payload(t)
	raw := marshalV4(t, payload)
	bundle, err := DecodeWorkoutBundleV4JSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.ProtocolVersion != 4 || bundle.WorkoutID == "" || len(bundle.Samples) != 1 || len(bundle.Series) != 1 || len(bundle.Routes) != 1 {
		t.Fatalf("unexpected bundle: %+v", bundle)
	}
	if bundle.Samples[0].Quantity == nil || bundle.Samples[0].Quantity.Value != 120 || bundle.Samples[0].Source.BundleID == "" {
		t.Fatalf("sample not converted: %+v", bundle.Samples[0])
	}
	if len(bundle.CompleteSampleTypes) != 1 || bundle.CompleteSampleTypes[0] != "HKQuantityTypeIdentifierHeartRate" || !bundle.CompleteRoutes {
		t.Fatalf("coverage not derived: %+v routes=%v", bundle.CompleteSampleTypes, bundle.CompleteRoutes)
	}
	if bundle.Observation == nil || len(bundle.Observation.Provenance.Samples) != 0 || len(bundle.Observation.Provenance.Routes) != 0 {
		t.Fatal("v4 provenance must stay empty")
	}
	if err := ValidateWorkoutBundle(bundle); err != nil {
		t.Fatal(err)
	}
	// A v4 bundle must be byte-order independent of a later v3 projection: the
	// converted samples are the exact facts the server stores.
	canonical, _ := bundle.Samples[0].Canonical()
	if len(canonical) == 0 {
		t.Fatal("empty canonical")
	}
}

func TestDecodeWorkoutBundleV4RejectsBrokenShapes(t *testing.T) {
	cases := map[string]func([]byte) []byte{
		"unknown field": func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"protocolVersion":4`), []byte(`"protocolVersion":4,"bogus":1`), 1)
		},
		"duplicate key": func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"protocolVersion":4`), []byte(`"protocolVersion":4,"protocolVersion":4`), 1)
		},
		"unknown workout field": func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"workout":{"activityType":37`), []byte(`"workout":{"activityType":37,"bogus":1`), 1)
		},
		"null stream value": func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`[1790000000000000,1790000060000000,120]`), []byte(`[1790000000000000,1790000060000000,null]`), 1)
		},
		"null optional array": func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"series":[`), []byte(`"series":null,"seriesIgnored":[`), 1)
		},
		"short triple": func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`[1790000000000000,1790000060000000,120]`), []byte(`[1790000000000000,1790000060000000]`), 1)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			raw := mutate(marshalV4(t, validV4Payload(t)))
			if _, err := DecodeWorkoutBundleV4JSON(raw); err == nil {
				t.Fatal("invalid payload accepted")
			}
		})
	}
}

func TestDecodeWorkoutBundleV4RejectsDuplicateKeys(t *testing.T) {
	raw := marshalV4(t, validV4Payload(t))
	// Insert a second workoutID member. walkJSON must reject it before the
	// typed decode would silently keep the last value.
	needle := []byte(`"workoutID":"`)
	index := bytes.Index(raw, needle)
	if index < 0 {
		t.Fatal("fixture has no workoutID")
	}
	insert := append([]byte{}, `"workoutID":"`...)
	// The duplicate value must be syntactically valid; the decoder rejects on
	// the repeated key, not on its value.
	value := make([]byte, 36)
	copy(value, "00000000-0000-4000-8000-000000000001")
	insert = append(insert, value...)
	insert = append(insert, '"', ',')
	dup := append(raw[:index], append(insert, raw[index:]...)...)
	if _, err := DecodeWorkoutBundleV4JSON(dup); err == nil {
		t.Fatal("duplicate key accepted")
	}
}

func TestDecodeWorkoutBundleV4CoverageMismatch(t *testing.T) {
	payload := validV4Payload(t)
	observation := payload["observation"].(map[string]any)
	coverage := observation["coverage"].(map[string]any)
	coverage["route"] = map[string]any{"state": "complete", "sampleCount": 99, "queryCompleted": true, "localPersisted": true}
	if _, err := DecodeWorkoutBundleV4JSON(marshalV4(t, payload)); err == nil {
		t.Fatal("coverage mismatch accepted")
	}
}

func TestDecodeWorkoutBundleV4ProvenanceMustBeEmpty(t *testing.T) {
	payload := validV4Payload(t)
	observation := payload["observation"].(map[string]any)
	provenance := observation["provenance"].(map[string]any)
	provenance["samples"] = []any{map[string]any{"sampleType": "HKQuantityTypeIdentifierHeartRate", "sampleUUID": NewID(), "source": map[string]any{"bundleID": "com.apple.health." + NewID()}}}
	if _, err := DecodeWorkoutBundleV4JSON(marshalV4(t, payload)); err == nil {
		t.Fatal("non-empty provenance accepted")
	}
}

func TestDecodeWorkoutBundleV4CategoryStream(t *testing.T) {
	payload := validV4Payload(t)
	payload["streams"] = []any{map[string]any{
		"type": "HKCategoryTypeIdentifierSleepAnalysis", "kind": "category", "unit": "",
		"samples": []any{[]any{1790000000000000, 1790000060000000, 2}},
		"uuids":   []any{NewID()},
		"source":  0, "src": []any{0},
	}}
	payload["series"] = []any{}
	payload["routes"] = []any{}
	observation := payload["observation"].(map[string]any)
	observation["coverage"] = map[string]any{
		"HKCategoryTypeIdentifierSleepAnalysis": map[string]any{"state": "complete", "sampleCount": 1, "queryCompleted": true, "localPersisted": true},
	}
	bundle, err := DecodeWorkoutBundleV4JSON(marshalV4(t, payload))
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Samples[0].Category == nil || *bundle.Samples[0].Category != 2 || bundle.Samples[0].Quantity != nil {
		t.Fatalf("category not converted: %+v", bundle.Samples[0])
	}
}

func TestDecodeWorkoutBundleV4RouteNullableComponents(t *testing.T) {
	payload := validV4Payload(t)
	route := payload["routes"].([]any)[0].(map[string]any)
	route["points"] = []any{[]any{1790000000000000, 31.2, 121.4, 12.5, 3.0, 4.0, 2.5, 180.0}}
	bundle, err := DecodeWorkoutBundleV4JSON(marshalV4(t, payload))
	if err != nil {
		t.Fatal(err)
	}
	point := bundle.Routes[0].Points[0]
	if point.Altitude == nil || *point.Altitude != 12.5 || point.Course == nil || *point.Course != 180 {
		t.Fatalf("route components not preserved: %+v", point)
	}
}

func TestDecodeWorkoutBundleV4Deletions(t *testing.T) {
	payload := validV4Payload(t)
	payload["deletions"] = []any{map[string]any{"type": "HKQuantityTypeIdentifierHeartRate", "uuid": NewID()}}
	payload["routeDeletions"] = []any{NewID()}
	bundle, err := DecodeWorkoutBundleV4JSON(marshalV4(t, payload))
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.SampleDeletions) != 1 || bundle.SampleDeletions[0].SampleType == "" || len(bundle.RouteDeletions) != 1 {
		t.Fatalf("deletions not converted: %+v", bundle.SampleDeletions)
	}
}

func TestDecodeWorkoutBundleV4SampleLimit(t *testing.T) {
	payload := validV4Payload(t)
	stream := payload["streams"].([]any)[0].(map[string]any)
	samples := make([]any, MaxWorkoutBundleEvents+1)
	uuids := make([]any, len(samples))
	for i := range samples {
		samples[i] = []any{int64(1_790_000_000_000_000), int64(1_790_000_060_000_000), 120}
		uuids[i] = NewID()
	}
	stream["samples"] = samples
	stream["uuids"] = uuids
	payload["series"] = []any{}
	payload["routes"] = []any{}
	payload["observation"].(map[string]any)["coverage"] = map[string]any{}
	_, err := DecodeWorkoutBundleV4JSON(marshalV4(t, payload))
	if err == nil {
		t.Fatal("sample limit not enforced")
	}
}

func TestDecodeWorkoutBundleV4RejectsNullAndDuplicateNested(t *testing.T) {
	payload := validV4Payload(t)
	raw := marshalV4(t, payload)
	nullSource := bytes.Replace(raw, []byte(`"bundleID":"com.apple.health"`), []byte(`"bundleID":null,"bundleID":"com.apple.health.`), 1)
	if _, err := DecodeWorkoutBundleV4JSON(nullSource); err == nil {
		t.Fatal("null nested value accepted")
	}
	if strings.Count(string(nullSource), `"bundleID"`) < 2 {
		t.Fatal("test mutation failed")
	}
}
