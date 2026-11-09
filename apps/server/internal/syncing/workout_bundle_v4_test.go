package syncing

import (
	"bytes"
	"encoding/json"
	"testing"
)

const v4TestSourceBundle = "com.apple.health"

// v4TestSource mirrors the Swift source descriptor. The server classifies it;
// the label itself is not trusted.
func v4TestSource() map[string]any {
	return map[string]any{"bundleID": v4TestSourceBundle, "version": "11.0", "productType": "Watch7,1"}
}

func v4TestSample(uuid string, start, end int64, value float64, source map[string]any, metadata map[string]any) map[string]any {
	sample := map[string]any{"uuid": uuid, "startUs": start, "endUs": end, "value": value, "source": source}
	if metadata != nil {
		sample["metadata"] = metadata
	}
	return sample
}

type v4Fixture struct {
	payload    map[string]any
	start, end int64
	workoutID  string
	sampleUUID string
	routeID    string
	source     map[string]any
}

// validV4Payload is one minimal raw bundle: the Workout object, one heart-rate
// stream with an inline per-sample source, one series and one route.
func validV4Payload(t *testing.T) v4Fixture {
	t.Helper()
	start, end := int64(1_790_000_000_000_000), int64(1_790_000_060_000_000)
	source := v4TestSource()
	f := v4Fixture{
		start: start, end: end, source: source,
		workoutID: NewID(), sampleUUID: NewID(), routeID: NewID(),
	}
	f.payload = map[string]any{
		"protocolVersion": 4,
		"datasetID":       NewID(),
		"datasetEpoch":    NewID(),
		"collectorEpoch":  NewID(),
		"workoutID":       f.workoutID,
		"startUs":         start,
		"endUs":           end,
		"source":          source,
		"metadata":        map[string]any{"HKTimeZone": "Asia/Shanghai"},
		"workout":         map[string]any{"activityType": 37, "durationUs": 60_000_000},
		"streams": []any{map[string]any{
			"type": "HKQuantityTypeIdentifierHeartRate", "unit": "count/min",
			"status": "complete", "error": nil,
			"samples": []any{v4TestSample(f.sampleUUID, start, end, 120, source, nil)},
		}},
		"series": []any{map[string]any{
			"type": "HKQuantityTypeIdentifierHeartRate", "sampleUUID": f.sampleUUID,
			"unit": "count/min", "points": []any{[]any{start, end, 121}},
		}},
		"routes": []any{map[string]any{
			"routeID": f.routeID, "source": source,
			"points": []any{[]any{start, 31.2, 121.4, nil, nil, nil, nil, nil}},
		}},
	}
	return f
}

func (f v4Fixture) marshal(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(f.payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (f v4Fixture) stream() map[string]any {
	return f.payload["streams"].([]any)[0].(map[string]any)
}

func (f v4Fixture) route() map[string]any {
	return f.payload["routes"].([]any)[0].(map[string]any)
}

func TestDecodeWorkoutBundleV4RawRoundTrip(t *testing.T) {
	f := validV4Payload(t)
	raw := f.marshal(t)
	bundle, err := DecodeWorkoutBundleV4JSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.ProtocolVersion != 4 || bundle.WorkoutID != f.workoutID || bundle.Workout.UUID != f.workoutID {
		t.Fatalf("envelope not projected: %+v", bundle.Workout)
	}
	if len(bundle.Samples) != 1 || len(bundle.Series) != 1 || len(bundle.Routes) != 1 {
		t.Fatalf("facts not projected: samples=%d series=%d routes=%d", len(bundle.Samples), len(bundle.Series), len(bundle.Routes))
	}
	sample := bundle.Samples[0]
	if sample.Quantity == nil || sample.Quantity.Value != 120 || sample.Quantity.Unit != "count/min" ||
		sample.Category != nil || sample.Source == nil || sample.Source.BundleID != v4TestSourceBundle ||
		sample.StartUs == nil || *sample.StartUs != f.start || sample.EndUs == nil || *sample.EndUs != f.end {
		t.Fatalf("sample not converted: %+v", sample)
	}
	series := bundle.Series[0]
	if series.SampleType != "HKQuantityTypeIdentifierHeartRate" || series.SampleUUID != f.sampleUUID ||
		len(series.Points) != 1 || series.Points[0].Unit != "count/min" || series.Points[0].Value != 121 {
		t.Fatalf("series not converted: %+v", series)
	}
	route := bundle.Routes[0]
	if route.RouteID != f.routeID || route.Source.BundleID != v4TestSourceBundle ||
		len(route.Points) != 1 || route.Points[0].Latitude != 31.2 || route.Points[0].Longitude != 121.4 {
		t.Fatalf("route not converted: %+v", route)
	}
	// A raw bundle has no observation, coverage proof or deletion sidecar.
	if bundle.Observation != nil || len(bundle.CompleteSampleTypes) != 0 || bundle.CompleteRoutes ||
		len(bundle.SampleDeletions) != 0 || len(bundle.RouteDeletions) != 0 {
		t.Fatalf("raw bundle invented sidecars: %+v", bundle)
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
	table, err := WorkoutBundleV4SourceTable(raw)
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
	if canonical, _ := bundle.Samples[0].Canonical(); len(canonical) == 0 {
		t.Fatal("empty canonical")
	}
}

func TestDecodeWorkoutBundleV4PreservesMetadataAndStripsReserved(t *testing.T) {
	f := validV4Payload(t)
	f.payload["metadata"] = map[string]any{
		"HKTimeZone":     "Asia/Shanghai",
		"custom:unknown": map[string]any{"nested": 1},
		VO2Reserved:      map[string]any{"type": "string", "value": "spoof"},
	}
	f.stream()["samples"] = []any{v4TestSample(f.sampleUUID, f.start, f.end, 120, f.source,
		map[string]any{"HKAverageMETs": 10.5, VO2Reserved: "spoof"})}
	bundle, err := DecodeWorkoutBundleV4JSON(f.marshal(t))
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

func TestDecodeWorkoutBundleV4IgnoresUnsupportedSections(t *testing.T) {
	f := validV4Payload(t)
	f.payload["observation"] = map[string]any{"version": 1, "coverage": map[string]any{}}
	f.payload["sources"] = []any{map[string]any{"bundleID": v4TestSourceBundle}}
	f.payload["deletions"] = []any{map[string]any{"type": "HKQuantityTypeIdentifierHeartRate", "uuid": NewID()}}
	f.payload["routeDeletions"] = []any{NewID()}
	f.payload["metadataRegistryVersion"] = 2
	f.payload["futureSection"] = map[string]any{"nested": []any{1, 2}}
	bundle, err := DecodeWorkoutBundleV4JSON(f.marshal(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Samples) != 1 || len(bundle.Routes) != 1 {
		t.Fatalf("unknown sections changed the projection: %+v", bundle)
	}
	if bundle.Observation != nil || len(bundle.SampleDeletions) != 0 || len(bundle.RouteDeletions) != 0 ||
		bundle.MetadataRegistryVersion != nil {
		t.Fatalf("unknown sections were interpreted: %+v", bundle)
	}
}

func TestDecodeWorkoutBundleV4SkipsUnusableStreams(t *testing.T) {
	f := validV4Payload(t)
	f.payload["streams"] = []any{
		map[string]any{"type": "HKQuantityTypeIdentifierBogus", "unit": "x",
			"samples": []any{v4TestSample(NewID(), f.start, f.end, 1, f.source, nil)}},
		map[string]any{"type": "HKQuantityTypeIdentifierHeartRate", "unit": "m",
			"samples": []any{v4TestSample(NewID(), f.start, f.end, 1, f.source, nil)}},
		map[string]any{"type": "HKQuantityTypeIdentifierStepCount", "unit": "count", "samples": []any{
			map[string]any{"uuid": "not-a-uuid", "startUs": f.start, "endUs": f.end, "value": 1, "source": f.source},
			v4TestSample(NewID(), f.end, f.start, 1, f.source, nil),
			v4TestSample(NewID(), f.start, f.end, 1, map[string]any{"bundleID": "com.example.thirdparty"}, nil),
			v4TestSample(f.sampleUUID, f.start, f.end, 84, f.source, nil),
		}},
		// A repeated stream type never overwrites the first one.
		map[string]any{"type": "HKQuantityTypeIdentifierStepCount", "unit": "count",
			"samples": []any{v4TestSample(NewID(), f.start, f.end, 999, f.source, nil)}},
	}
	f.payload["series"] = []any{}
	f.payload["routes"] = []any{}
	bundle, err := DecodeWorkoutBundleV4JSON(f.marshal(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Samples) != 1 || bundle.Samples[0].Quantity == nil || bundle.Samples[0].Quantity.Value != 84 {
		t.Fatalf("unusable samples were not skipped: %+v", bundle.Samples)
	}
}

func TestDecodeWorkoutBundleV4SkipsUnusableSeriesAndRoutes(t *testing.T) {
	f := validV4Payload(t)
	f.payload["series"] = []any{
		map[string]any{"type": "HKQuantityTypeIdentifierHeartRate", "sampleUUID": NewID(), "unit": "count/min",
			"points": []any{[]any{f.start, f.end, 1}}},
		map[string]any{"type": "HKQuantityTypeIdentifierHeartRate", "sampleUUID": f.sampleUUID, "unit": "m",
			"points": []any{[]any{f.start, f.end, 1}}},
		map[string]any{"type": "HKQuantityTypeIdentifierHeartRate", "sampleUUID": f.sampleUUID, "unit": "count/min",
			"points": []any{[]any{f.start - 1, f.end + 1, 1}, []any{f.start, f.end, 122}}},
	}
	f.payload["routes"] = []any{
		map[string]any{"routeID": NewID(), "source": map[string]any{"bundleID": "com.example.thirdparty"},
			"points": []any{[]any{f.start, 31.2, 121.4, nil, nil, nil, nil, nil}}},
		map[string]any{"routeID": NewID(), "source": f.source,
			"points": []any{[]any{f.start, 200.0, 121.4, nil, nil, nil, nil, nil}}},
	}
	bundle, err := DecodeWorkoutBundleV4JSON(f.marshal(t))
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

func TestDecodeWorkoutBundleV4CategoryStream(t *testing.T) {
	f := validV4Payload(t)
	f.payload["streams"] = []any{map[string]any{
		"type": "HKCategoryTypeIdentifierSleepAnalysis", "unit": "", "status": "complete", "error": nil,
		"samples": []any{v4TestSample(NewID(), f.start, f.end, 2, f.source, nil)},
	}}
	f.payload["series"] = []any{}
	f.payload["routes"] = []any{}
	bundle, err := DecodeWorkoutBundleV4JSON(f.marshal(t))
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Samples[0].Category == nil || *bundle.Samples[0].Category != 2 || bundle.Samples[0].Quantity != nil {
		t.Fatalf("category not converted: %+v", bundle.Samples[0])
	}
}

func TestDecodeWorkoutBundleV4RouteNullableComponents(t *testing.T) {
	f := validV4Payload(t)
	f.route()["points"] = []any{[]any{f.start, 31.2, 121.4, 12.5, 3.0, 4.0, 2.5, 180.0}}
	bundle, err := DecodeWorkoutBundleV4JSON(f.marshal(t))
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

func TestDecodeWorkoutBundleV4StrictEnvelope(t *testing.T) {
	cases := map[string]func(*v4Fixture){
		"missing protocolVersion": func(f *v4Fixture) { delete(f.payload, "protocolVersion") },
		"wrong protocolVersion":   func(f *v4Fixture) { f.payload["protocolVersion"] = 3 },
		"missing source":          func(f *v4Fixture) { delete(f.payload, "source") },
		"null source":             func(f *v4Fixture) { f.payload["source"] = nil },
		"missing workout":         func(f *v4Fixture) { delete(f.payload, "workout") },
		"workout without activityType": func(f *v4Fixture) {
			f.payload["workout"] = map[string]any{"durationUs": 60_000_000}
		},
		"workout duration beyond span": func(f *v4Fixture) {
			f.payload["workout"].(map[string]any)["durationUs"] = 60_000_001
		},
		"inverted envelope range": func(f *v4Fixture) {
			f.payload["startUs"], f.payload["endUs"] = f.end, f.start
		},
		"invalid workout id": func(f *v4Fixture) { f.payload["workoutID"] = "not-a-uuid" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := validV4Payload(t)
			mutate(&f)
			_, err := DecodeWorkoutBundleV4JSON(f.marshal(t))
			requireSeriesError(t, err, 422, "unsupported_payload")
		})
	}
}

func TestDecodeWorkoutBundleV4RejectsThirdPartyWorkoutSource(t *testing.T) {
	f := validV4Payload(t)
	f.payload["source"] = map[string]any{"bundleID": "com.example.thirdparty", "version": "1.0", "productType": "Watch7,1"}
	_, err := DecodeWorkoutBundleV4JSON(f.marshal(t))
	requireSeriesError(t, err, 422, "source_policy_excluded")
}

func TestDecodeWorkoutBundleV4RejectsDuplicateAndMalformedJSON(t *testing.T) {
	f := validV4Payload(t)
	raw := f.marshal(t)
	duplicate := bytes.Replace(raw,
		[]byte(`"source":{"bundleID":"com.apple.health"`),
		[]byte(`"source":{"bundleID":"com.apple.health","bundleID":"com.apple.health"`), 1)
	if bytes.Equal(duplicate, raw) {
		t.Fatal("duplicate-key mutation did not apply")
	}
	_, err := DecodeWorkoutBundleV4JSON(duplicate)
	requireSeriesError(t, err, 422, "unsupported_payload")

	invalidUTF8 := append(append([]byte{}, raw...), 0xff)
	_, err = DecodeWorkoutBundleV4JSON(invalidUTF8)
	requireSeriesError(t, err, 422, "unsupported_payload")

	notAnObject := append([]byte("["), append(raw, ']')...)
	_, err = DecodeWorkoutBundleV4JSON(notAnObject)
	requireSeriesError(t, err, 422, "unsupported_payload")
}

func TestDecodeWorkoutBundleV4SampleLimit(t *testing.T) {
	f := validV4Payload(t)
	samples := make([]any, MaxWorkoutBundleEvents+1)
	sample := v4TestSample(f.sampleUUID, f.start, f.end, 120, f.source, nil)
	for index := range samples {
		samples[index] = sample
	}
	f.stream()["samples"] = samples
	f.payload["series"] = []any{}
	f.payload["routes"] = []any{}
	_, err := DecodeWorkoutBundleV4JSON(f.marshal(t))
	requireSeriesError(t, err, 413, "bundle_too_large")
}

func TestDecodeWorkoutBundleV4RouteLimit(t *testing.T) {
	f := validV4Payload(t)
	routes := make([]any, MaxBundleRoutes+1)
	for index := range routes {
		routes[index] = map[string]any{"routeID": NewID(), "source": f.source,
			"points": []any{[]any{f.start, 31.2, 121.4, nil, nil, nil, nil, nil}}}
	}
	f.payload["routes"] = routes
	_, err := DecodeWorkoutBundleV4JSON(f.marshal(t))
	requireSeriesError(t, err, 413, "bundle_too_large")
}
