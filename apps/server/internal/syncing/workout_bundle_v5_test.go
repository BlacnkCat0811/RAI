package syncing

import (
	"bytes"
	"encoding/json"
	"testing"
)

type v5Fixture struct {
	payload    map[string]any
	start, end int64
	workoutID  string
	sampleUUID string
	routeID    string
	source     map[string]any
	thirdParty map[string]any
}

// validV5Payload is one minimal compact bundle: two source-table entries, one
// metadata-table entry, one heart-rate stream, one series and one route. The
// first sample uses the stream default source; the second overrides it and
// references metadata by index.
func validV5Payload(t *testing.T) v5Fixture {
	t.Helper()
	start, end := int64(1_790_000_000_000_000), int64(1_790_000_060_000_000)
	source := v4TestSource()
	thirdParty := map[string]any{"bundleID": "com.example.thirdparty", "version": "1.0"}
	f := v5Fixture{
		start: start, end: end, source: source, thirdParty: thirdParty,
		workoutID: NewID(), sampleUUID: NewID(), routeID: NewID(),
	}
	f.payload = map[string]any{
		"protocolVersion": 5,
		"datasetID":       NewID(),
		"datasetEpoch":    NewID(),
		"collectorEpoch":  NewID(),
		"workoutID":       f.workoutID,
		"startUs":         start,
		"endUs":           end,
		"source":          source,
		"metadata":        map[string]any{"HKTimeZone": "Asia/Shanghai"},
		"workout":         map[string]any{"activityType": 37, "durationUs": 60_000_000},
		"sourceTable":     []any{source},
		"metadataTable":   []any{map[string]any{"HKAverageMETs": 10.5}},
		"streams": []any{map[string]any{
			"type": "HKQuantityTypeIdentifierHeartRate", "unit": "count/min",
			"status": "complete", "error": nil, "source": 0,
			"samples": []any{
				[]any{f.sampleUUID, 0, 60_000_000, 120},
				[]any{NewID(), 60_000_000, 120_000_000, 121, 0},
			},
		}},
		"series": []any{map[string]any{
			"type": "HKQuantityTypeIdentifierHeartRate", "sampleUUID": f.sampleUUID,
			"unit": "count/min", "points": []any{[]any{0, 60_000_000, 121}},
		}},
		"routes": []any{map[string]any{
			"routeID": f.routeID, "source": 0,
			"points": []any{[]any{0, 31.2, 121.4, nil, nil, nil, nil, nil}},
		}},
	}
	return f
}

func (f v5Fixture) marshal(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(f.payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (f v5Fixture) stream() map[string]any {
	return f.payload["streams"].([]any)[0].(map[string]any)
}

func TestDecodeWorkoutBundleV5RawRoundTrip(t *testing.T) {
	f := validV5Payload(t)
	raw := f.marshal(t)
	bundle, err := DecodeWorkoutBundleV5JSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.ProtocolVersion != 5 || bundle.WorkoutID != f.workoutID || bundle.Workout.UUID != f.workoutID {
		t.Fatalf("envelope not projected: %+v", bundle.Workout)
	}
	if len(bundle.Samples) != 2 || len(bundle.Series) != 1 || len(bundle.Routes) != 1 {
		t.Fatalf("facts not projected: samples=%d series=%d routes=%d", len(bundle.Samples), len(bundle.Series), len(bundle.Routes))
	}
	first := bundle.Samples[0]
	if first.Quantity == nil || first.Quantity.Value != 120 || first.Quantity.Unit != "count/min" ||
		first.Category != nil || first.Source == nil || first.Source.BundleID != v4TestSourceBundle ||
		first.StartUs == nil || *first.StartUs != f.start || first.EndUs == nil || *first.EndUs != f.start+60_000_000 ||
		first.Metadata != nil {
		t.Fatalf("default-source sample not converted: %+v", first)
	}
	second := bundle.Samples[1]
	if second.Quantity == nil || second.Quantity.Value != 121 || second.Metadata["HKAverageMETs"] == nil {
		t.Fatalf("metadata-indexed sample not converted: %+v", second)
	}
	series := bundle.Series[0]
	if series.SampleType != "HKQuantityTypeIdentifierHeartRate" || series.SampleUUID != f.sampleUUID ||
		len(series.Points) != 1 || series.Points[0].Value != 121 || series.Points[0].StartUs != f.start ||
		series.Points[0].EndUs != f.start+60_000_000 {
		t.Fatalf("series not converted: %+v", series)
	}
	route := bundle.Routes[0]
	if route.RouteID != f.routeID || route.Source.BundleID != v4TestSourceBundle ||
		len(route.Points) != 1 || route.Points[0].RecordedAtUs != f.start ||
		route.Points[0].Latitude != 31.2 || route.Points[0].Longitude != 121.4 {
		t.Fatalf("route not converted: %+v", route)
	}
	if bundle.Workout.Metadata["HKTimeZone"] != "Asia/Shanghai" {
		t.Fatalf("workout metadata lost: %+v", bundle.Workout.Metadata)
	}
	if bundle.SourceTable == nil || bundle.SourceTable.Workout.BundleID != v4TestSourceBundle {
		t.Fatalf("workout source not retained: %+v", bundle.SourceTable)
	}
	if source, ok := bundle.SourceTable.SampleSource("HKQuantityTypeIdentifierHeartRate", f.sampleUUID); !ok || source.BundleID != v4TestSourceBundle {
		t.Fatalf("sample source not retained: %+v", source)
	}
	if source, ok := bundle.SourceTable.RouteSource(f.routeID); !ok || source.BundleID != v4TestSourceBundle {
		t.Fatalf("route source not retained: %+v", source)
	}
	// Analysis reads the same table back from the stored bytes.
	table, err := WorkoutBundlePayloadSourceTable(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := table.SampleSource("HKQuantityTypeIdentifierHeartRate", f.sampleUUID); !ok {
		t.Fatal("stored payload lost the sample source")
	}
	if _, ok := table.RouteSource(f.routeID); !ok {
		t.Fatal("stored payload lost the route source")
	}
	if err := ValidateWorkoutBundle(bundle); err != nil {
		t.Fatal(err)
	}
}

func TestDecodeWorkoutBundleV5PreservesMetadataAndStripsReserved(t *testing.T) {
	f := validV5Payload(t)
	f.payload["metadata"] = map[string]any{
		"HKTimeZone":     "Asia/Shanghai",
		"custom:unknown": map[string]any{"nested": 1},
		VO2Reserved:      map[string]any{"type": "string", "value": "spoof"},
	}
	f.payload["metadataTable"] = []any{map[string]any{
		"HKAverageMETs": 10.5, VO2Reserved: "spoof",
	}}
	f.stream()["samples"] = []any{[]any{f.sampleUUID, 0, 60_000_000, 120, 0}}
	bundle, err := DecodeWorkoutBundleV5JSON(f.marshal(t))
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Workout.Metadata["HKTimeZone"] != "Asia/Shanghai" || bundle.Workout.Metadata["custom:unknown"] == nil {
		t.Fatalf("unknown workout metadata was dropped: %+v", bundle.Workout.Metadata)
	}
	if _, spoofed := bundle.Workout.Metadata[VO2Reserved]; spoofed {
		t.Fatal("client-written reserved workout metadata accepted")
	}
	if bundle.Samples[0].Metadata["HKAverageMETs"] == nil {
		t.Fatalf("sample metadata lost: %+v", bundle.Samples[0].Metadata)
	}
	if _, spoofed := bundle.Samples[0].Metadata[VO2Reserved]; spoofed {
		t.Fatal("client-written reserved sample metadata accepted")
	}
}

func TestDecodeWorkoutBundleV5SkipsUnusableStreams(t *testing.T) {
	f := validV5Payload(t)
	f.payload["streams"] = []any{
		map[string]any{"type": "HKQuantityTypeIdentifierBogus", "source": 0,
			"samples": []any{[]any{NewID(), 0, 1, 1}}},
		map[string]any{"type": "HKQuantityTypeIdentifierHeartRate", "unit": "m", "source": 0,
			"samples": []any{[]any{NewID(), 0, 1, 1}}},
		map[string]any{"type": "HKQuantityTypeIdentifierStepCount", "unit": "count", "source": 0, "samples": []any{
			[]any{"not-a-uuid", 0, 1, 1},
			[]any{NewID(), 60_000_000, 0, 1},
			[]any{NewID(), 0, 1, 1, nil, 1},
			[]any{NewID(), 0, 1, "not-a-number"},
			[]any{NewID(), 0, 1, 1, 0, 1},
			[]any{f.sampleUUID, 0, 1, 84},
		}},
		// A repeated stream type never overwrites the first one.
		map[string]any{"type": "HKQuantityTypeIdentifierStepCount", "unit": "count", "source": 0,
			"samples": []any{[]any{NewID(), 0, 1, 999}}},
	}
	f.payload["series"] = []any{}
	f.payload["routes"] = []any{}
	bundle, err := DecodeWorkoutBundleV5JSON(f.marshal(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Samples) != 1 || bundle.Samples[0].Quantity == nil ||
		bundle.Samples[0].Quantity.Value != 84 || bundle.Samples[0].UUID != f.sampleUUID {
		t.Fatalf("unusable samples were not skipped: %+v", bundle.Samples)
	}
}

func TestDecodeWorkoutBundleV5SkipsUnusableSeriesAndRoutes(t *testing.T) {
	f := validV5Payload(t)
	f.payload["sourceTable"] = []any{f.source, f.thirdParty}
	f.payload["series"] = []any{
		map[string]any{"type": "HKQuantityTypeIdentifierHeartRate", "sampleUUID": NewID(), "unit": "count/min",
			"points": []any{[]any{0, 1, 1}}},
		map[string]any{"type": "HKQuantityTypeIdentifierHeartRate", "sampleUUID": f.sampleUUID, "unit": "m",
			"points": []any{[]any{0, 1, 1}}},
		map[string]any{"type": "HKQuantityTypeIdentifierHeartRate", "sampleUUID": f.sampleUUID, "unit": "count/min",
			"points": []any{[]any{-1, 61_000_000, 1}, []any{0, 60_000_000, 122}}},
	}
	f.payload["routes"] = []any{
		map[string]any{"routeID": NewID(), "source": 1,
			"points": []any{[]any{0, 31.2, 121.4, nil, nil, nil, nil, nil}}},
		map[string]any{"routeID": NewID(), "source": 0,
			"points": []any{[]any{0, 200.0, 121.4, nil, nil, nil, nil, nil}}},
	}
	bundle, err := DecodeWorkoutBundleV5JSON(f.marshal(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Series) != 1 || len(bundle.Series[0].Points) != 1 || bundle.Series[0].Points[0].Value != 122 {
		t.Fatalf("unusable series were not skipped: %+v", bundle.Series)
	}
	if len(bundle.Routes) != 0 {
		t.Fatalf("unusable routes were not skipped: %+v", bundle.Routes)
	}
}

func TestDecodeWorkoutBundleV5CategoryStream(t *testing.T) {
	f := validV5Payload(t)
	f.payload["streams"] = []any{map[string]any{
		"type": "HKCategoryTypeIdentifierSleepAnalysis", "unit": "", "source": 0,
		"samples": []any{[]any{NewID(), 0, 60_000_000, 2}},
	}}
	f.payload["series"] = []any{}
	f.payload["routes"] = []any{}
	bundle, err := DecodeWorkoutBundleV5JSON(f.marshal(t))
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Samples[0].Category == nil || *bundle.Samples[0].Category != 2 || bundle.Samples[0].Quantity != nil {
		t.Fatalf("category not converted: %+v", bundle.Samples[0])
	}
}

func TestDecodeWorkoutBundleV5RouteNullableComponents(t *testing.T) {
	f := validV5Payload(t)
	f.payload["routes"].([]any)[0].(map[string]any)["points"] = []any{
		[]any{0, 31.2, 121.4, 12.5, 3.0, 4.0, 2.5, 180.0},
	}
	bundle, err := DecodeWorkoutBundleV5JSON(f.marshal(t))
	if err != nil {
		t.Fatal(err)
	}
	point := bundle.Routes[0].Points[0]
	if point.Altitude == nil || *point.Altitude != 12.5 || point.HorizontalAccuracy == nil || *point.HorizontalAccuracy != 3 ||
		point.VerticalAccuracy == nil || *point.VerticalAccuracy != 4 || point.Speed == nil || *point.Speed != 2.5 ||
		point.Course == nil || *point.Course != 180 {
		t.Fatalf("route components not preserved: %+v", point)
	}
}

func TestDecodeWorkoutBundleV5StrictEnvelope(t *testing.T) {
	cases := map[string]func(*v5Fixture){
		"missing protocolVersion": func(f *v5Fixture) { delete(f.payload, "protocolVersion") },
		"wrong protocolVersion":   func(f *v5Fixture) { f.payload["protocolVersion"] = 4 },
		"missing source":          func(f *v5Fixture) { delete(f.payload, "source") },
		"null source":             func(f *v5Fixture) { f.payload["source"] = nil },
		"missing workout":         func(f *v5Fixture) { delete(f.payload, "workout") },
		"workout without activityType": func(f *v5Fixture) {
			f.payload["workout"] = map[string]any{"durationUs": 60_000_000}
		},
		"workout duration beyond span": func(f *v5Fixture) {
			f.payload["workout"].(map[string]any)["durationUs"] = 60_000_001
		},
		"inverted envelope range": func(f *v5Fixture) {
			f.payload["startUs"], f.payload["endUs"] = f.end, f.start
		},
		"invalid workout id": func(f *v5Fixture) { f.payload["workoutID"] = "not-a-uuid" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := validV5Payload(t)
			mutate(&f)
			_, err := DecodeWorkoutBundleV5JSON(f.marshal(t))
			requireSeriesError(t, err, 422, "unsupported_payload")
		})
	}
}

func TestDecodeWorkoutBundleV5RejectsThirdPartyWorkoutSource(t *testing.T) {
	f := validV5Payload(t)
	f.payload["source"] = f.thirdParty
	_, err := DecodeWorkoutBundleV5JSON(f.marshal(t))
	requireSeriesError(t, err, 422, "source_policy_excluded")
}

func TestDecodeWorkoutBundleV5RejectsDuplicateAndMalformedJSON(t *testing.T) {
	f := validV5Payload(t)
	raw := f.marshal(t)
	duplicate := bytes.Replace(raw,
		[]byte(`"source":{"bundleID":"com.apple.health"`),
		[]byte(`"source":{"bundleID":"com.apple.health","bundleID":"com.apple.health"`), 1)
	if bytes.Equal(duplicate, raw) {
		t.Fatal("duplicate-key mutation did not apply")
	}
	_, err := DecodeWorkoutBundleV5JSON(duplicate)
	requireSeriesError(t, err, 422, "unsupported_payload")

	invalidUTF8 := append(append([]byte{}, raw...), 0xff)
	_, err = DecodeWorkoutBundleV5JSON(invalidUTF8)
	requireSeriesError(t, err, 422, "unsupported_payload")

	notAnObject := append([]byte("["), append(raw, ']')...)
	_, err = DecodeWorkoutBundleV5JSON(notAnObject)
	requireSeriesError(t, err, 422, "unsupported_payload")
}

func TestDecodeWorkoutBundleV5DeltaOverflowSkipsSample(t *testing.T) {
	f := validV5Payload(t)
	f.stream()["samples"] = []any{[]any{f.sampleUUID, int64(maxBundleTimeUs), 0, 120}}
	bundle, err := DecodeWorkoutBundleV5JSON(f.marshal(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Samples) != 0 {
		t.Fatalf("out-of-range delta was projected: %+v", bundle.Samples)
	}
}

func TestDecodeWorkoutBundleV5SampleLimit(t *testing.T) {
	f := validV5Payload(t)
	samples := make([]any, MaxWorkoutBundleEvents+1)
	sample := []any{f.sampleUUID, 0, 1, 120}
	for index := range samples {
		samples[index] = sample
	}
	f.stream()["samples"] = samples
	f.payload["series"] = []any{}
	f.payload["routes"] = []any{}
	_, err := DecodeWorkoutBundleV5JSON(f.marshal(t))
	requireSeriesError(t, err, 413, "bundle_too_large")
}

func TestV5SourceAndMetadataTableLimits(t *testing.T) {
	sources := make([]any, maxV5Sources+1)
	for index := range sources {
		sources[index] = v4TestSource()
	}
	raw, err := json.Marshal(sources)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v5SourceEntries(raw); err == nil {
		t.Fatal("oversized source table accepted")
	}
	metadata := make([]any, maxV5MetadataRows+1)
	for index := range metadata {
		metadata[index] = map[string]any{}
	}
	raw, err = json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v5MetadataEntries(raw); err == nil {
		t.Fatal("oversized metadata table accepted")
	}
}

func TestDecodeWorkoutIngestEnvelopeAcceptsV4AndV5(t *testing.T) {
	v5 := validV5Payload(t)
	envelope, err := DecodeWorkoutIngestEnvelope(v5.marshal(t))
	if err != nil {
		t.Fatal(err)
	}
	if envelope.ProtocolVersion != 5 || envelope.WorkoutID != v5.workoutID {
		t.Fatalf("v5 ingest envelope not decoded: %+v", envelope)
	}
	v4 := validV4Payload(t)
	envelope, err = DecodeWorkoutIngestEnvelope(v4.marshal(t))
	if err != nil {
		t.Fatal(err)
	}
	if envelope.ProtocolVersion != 4 || envelope.WorkoutID != v4.workoutID {
		t.Fatalf("v4 ingest envelope not decoded: %+v", envelope)
	}
	if _, err := DecodeWorkoutIngestEnvelope([]byte(`{"protocolVersion":6}`)); err == nil {
		t.Fatal("unknown protocol accepted")
	}
}
