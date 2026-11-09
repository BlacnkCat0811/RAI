package syncing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type v6Fixture struct {
	payload   map[string]any
	start     int64
	end       int64
	datasetID string
	workoutID string
	source    map[string]any
	third     map[string]any
}

// validV6Payload is one raw protocol-6 Workout: two scalar heart-rate triples,
// one running-power series group whose parent is not in samples, and one route.
// It carries no per-sample UUID, metadata or source.
func validV6Payload(t *testing.T) v6Fixture {
	t.Helper()
	start := int64(1_790_000_000_000_000)
	end := start + 3_600_000_000
	f := v6Fixture{
		start: start, end: end, datasetID: NewID(), workoutID: NewID(),
		source: v4TestSource(),
		third:  map[string]any{"bundleID": "com.example.thirdparty", "version": "1.0"},
	}
	f.payload = map[string]any{
		"protocolVersion": 6,
		"datasetID":       f.datasetID,
		"datasetEpoch":    NewID(),
		"collectorEpoch":  NewID(),
		"workoutID":       f.workoutID,
		"startUs":         start,
		"endUs":           end,
		"source":          f.source,
		"metadata":        map[string]any{"HKTimeZone": "Asia/Shanghai"},
		"workout":         map[string]any{"activityType": 37, "durationUs": 60_000_000},
		"streams": []any{
			map[string]any{
				"type": "HKQuantityTypeIdentifierHeartRate", "unit": "count/min", "status": "complete",
				"samples": []any{
					[]any{0, 60_000_000, 120},
					[]any{60_000_000, 120_000_000, 121},
				},
			},
			map[string]any{
				"type": "HKQuantityTypeIdentifierRunningPower", "unit": "W",
				"series": []any{map[string]any{
					"startDeltaUs": 0, "endDeltaUs": 60_000_000, "value": 192.5,
					"points": []any{[]any{0, 1_000_000, 190}, []any{1_000_000, 2_000_000, 191}},
				}},
			},
		},
		"routes": []any{map[string]any{
			"points": []any{[]any{0, 31.2, 121.4, nil, nil, nil, nil, nil}},
		}},
	}
	return f
}

func (f v6Fixture) marshal(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal(f.payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func (f v6Fixture) stream(t *testing.T, sampleType string) map[string]any {
	t.Helper()
	for _, item := range f.payload["streams"].([]any) {
		stream := item.(map[string]any)
		if stream["type"] == sampleType {
			return stream
		}
	}
	t.Fatalf("stream %s missing", sampleType)
	return nil
}

func v6UUID(datasetID, workoutID, sampleType, kind string, ordinal int) string {
	return V6FactUUID(datasetID, workoutID, sampleType, kind, ordinal)
}

func TestDecodeWorkoutBundleV6RawRoundTrip(t *testing.T) {
	f := validV6Payload(t)
	raw := f.marshal(t)
	bundle, err := DecodeWorkoutBundleV6JSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if bundle.ProtocolVersion != 6 || bundle.WorkoutID != f.workoutID || bundle.Workout.UUID != f.workoutID {
		t.Fatalf("envelope not projected: %+v", bundle.Workout)
	}
	if len(bundle.Samples) != 3 || len(bundle.Series) != 1 || len(bundle.Routes) != 1 {
		t.Fatalf("facts not projected: samples=%d series=%d routes=%d", len(bundle.Samples), len(bundle.Series), len(bundle.Routes))
	}
	hrType := "HKQuantityTypeIdentifierHeartRate"
	for index, want := range []float64{120, 121} {
		sample := bundle.Samples[index]
		wantUUID := v6UUID(f.datasetID, f.workoutID, hrType, "sample", index)
		if sample.UUID != wantUUID || sample.Type != hrType || sample.Kind != "sample" ||
			sample.Quantity == nil || sample.Quantity.Value != want || sample.Quantity.Unit != "count/min" ||
			sample.Category != nil || sample.Metadata != nil || sample.Source == nil ||
			sample.Source.BundleID != v4TestSourceBundle {
			t.Fatalf("scalar sample %d not projected: %+v", index, sample)
		}
		if sample.StartUs == nil || *sample.StartUs != f.start+int64(index)*60_000_000 ||
			sample.EndUs == nil || *sample.EndUs != f.start+int64(index+1)*60_000_000 {
			t.Fatalf("scalar sample %d times: %+v", index, sample)
		}
	}
	powerType := "HKQuantityTypeIdentifierRunningPower"
	parentUUID := v6UUID(f.datasetID, f.workoutID, powerType, "series", 0)
	parent := bundle.Samples[2]
	if parent.UUID != parentUUID || parent.Quantity == nil || parent.Quantity.Value != 192.5 ||
		parent.Quantity.Unit != "W" || parent.StartUs == nil || *parent.StartUs != f.start ||
		parent.EndUs == nil || *parent.EndUs != f.start+60_000_000 {
		t.Fatalf("series parent not projected: %+v", parent)
	}
	series := bundle.Series[0]
	if series.SampleType != powerType || series.SampleUUID != parentUUID || len(series.Points) != 2 ||
		series.Points[0].Value != 190 || series.Points[1].StartUs != f.start+1_000_000 || series.Points[1].Unit != "W" {
		t.Fatalf("series not projected: %+v", series)
	}
	routeID := v6UUID(f.datasetID, f.workoutID, "", "route", 0)
	route := bundle.Routes[0]
	if route.RouteID != routeID || route.Source.BundleID != v4TestSourceBundle || len(route.Points) != 1 ||
		route.Points[0].RecordedAtUs != f.start || route.Points[0].Latitude != 31.2 {
		t.Fatalf("route not projected: %+v", route)
	}
	if bundle.Workout.Metadata["HKTimeZone"] != "Asia/Shanghai" {
		t.Fatalf("workout metadata lost: %+v", bundle.Workout.Metadata)
	}
	for _, sample := range bundle.Samples {
		if source, ok := bundle.SourceTable.SampleSource(sample.Type, sample.UUID); !ok || source.BundleID != v4TestSourceBundle {
			t.Fatalf("sample source not inherited: %+v", source)
		}
	}
	if source, ok := bundle.SourceTable.RouteSource(routeID); !ok || source.BundleID != v4TestSourceBundle {
		t.Fatalf("route source not inherited: %+v", source)
	}
	table, err := WorkoutBundlePayloadSourceTable(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := table.SampleSource(powerType, parentUUID); !ok {
		t.Fatal("stored payload lost the series parent source")
	}
	if _, ok := table.RouteSource(routeID); !ok {
		t.Fatal("stored payload lost the route source")
	}
	if err := ValidateWorkoutBundle(bundle); err != nil {
		t.Fatal(err)
	}
}

func TestV6FactUUIDVectorAndDeterminism(t *testing.T) {
	const datasetID = "11111111-1111-4111-8111-111111111111"
	const workoutID = "22222222-2222-4222-8222-222222222222"
	const hrType = "HKQuantityTypeIdentifierHeartRate"
	if got := V6FactUUID(datasetID, workoutID, hrType, "sample", 0); got != "34635001-1772-53c6-b1be-0d0ee6697213" {
		t.Fatalf("sample vector: %s", got)
	}
	if got := V6FactUUID(datasetID, workoutID, hrType, "series", 0); got != "e5920676-90e1-5960-9e25-7a82deac2d69" {
		t.Fatalf("series vector: %s", got)
	}
	if got := V6FactUUID(datasetID, workoutID, "", "route", 0); got != "c78232c5-3611-5aa0-97fd-ecc1f7ffb71c" {
		t.Fatalf("route vector: %s", got)
	}
	for _, got := range []string{
		V6FactUUID(datasetID, workoutID, hrType, "sample", 0),
		V6FactUUID(datasetID, workoutID, hrType, "series", 0),
		V6FactUUID(datasetID, workoutID, "", "route", 0),
	} {
		if got != strings.ToLower(got) || !ValidID(got) {
			t.Fatalf("not a canonical lowercase UUID: %s", got)
		}
	}

	f := validV6Payload(t)
	raw := f.marshal(t)
	first, err := DecodeWorkoutBundleV6JSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	second, err := DecodeWorkoutBundleV6JSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Samples) != len(second.Samples) || len(first.Routes) != len(second.Routes) {
		t.Fatal("fact counts changed between decodes")
	}
	for index := range first.Samples {
		if first.Samples[index].UUID != second.Samples[index].UUID {
			t.Fatalf("sample %d identity changed: %s != %s", index, first.Samples[index].UUID, second.Samples[index].UUID)
		}
	}
	for index := range first.Routes {
		if first.Routes[index].RouteID != second.Routes[index].RouteID {
			t.Fatalf("route %d identity changed", index)
		}
	}
	if first.Series[0].SampleUUID != second.Series[0].SampleUUID {
		t.Fatal("series parent identity changed")
	}
}

func TestDecodeWorkoutBundleV6SkipsUnusableStreams(t *testing.T) {
	f := validV6Payload(t)
	stepType := "HKQuantityTypeIdentifierStepCount"
	f.payload["streams"] = []any{
		map[string]any{"type": "HKQuantityTypeIdentifierBogus", "samples": []any{[]any{0, 1, 1}}},
		map[string]any{"type": "HKQuantityTypeIdentifierHeartRate", "unit": "m", "samples": []any{[]any{0, 1, 1}}},
		map[string]any{"type": stepType, "unit": "count", "samples": []any{
			[]any{0, 1},                         // wrong arity
			[]any{60_000_000, 0, 1},             // inverted
			[]any{0, 1, "not-a-number"},         // wrong type
			[]any{0, 60_000_000, 1.5},           // fractional quantity is valid
			[]any{int64(maxBundleTimeUs), 0, 1}, // absolute start outside range
			[]any{0, 1, true},                   // wrong type
		}},
		// A repeated stream type never adds facts; its ordinals are not re-used.
		map[string]any{"type": stepType, "unit": "count", "samples": []any{[]any{0, 1, 999}}},
	}
	f.payload["routes"] = []any{}
	raw := f.marshal(t)
	bundle, err := DecodeWorkoutBundleV6JSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Samples) != 1 {
		t.Fatalf("unusable samples were not skipped: %+v", bundle.Samples)
	}
	want := v6UUID(f.datasetID, f.workoutID, stepType, "sample", 3)
	if bundle.Samples[0].UUID != want || bundle.Samples[0].Quantity == nil || bundle.Samples[0].Quantity.Value != 1.5 {
		t.Fatalf("skipped entries shifted identity: %+v (want %s)", bundle.Samples[0], want)
	}
	if !bytes.Contains(raw, []byte(`"not-a-number"`)) {
		t.Fatal("raw bytes lost the skipped entry")
	}
}

func TestDecodeWorkoutBundleV6SkipsUnusableSeriesGroups(t *testing.T) {
	f := validV6Payload(t)
	powerType := "HKQuantityTypeIdentifierRunningPower"
	f.payload["streams"] = []any{map[string]any{
		"type": powerType, "unit": "W",
		"series": []any{
			map[string]any{"startDeltaUs": 0, "endDeltaUs": 1_000_000, "points": []any{[]any{0, 1, 1}}},
			map[string]any{"startDeltaUs": 0, "value": 1, "points": []any{[]any{0, 1, 1}}},
			map[string]any{"startDeltaUs": 0, "endDeltaUs": 1_000_000, "value": 1,
				"points": []any{[]any{0, 2_000_000, 1}}},
			map[string]any{"startDeltaUs": 0, "endDeltaUs": 1_000_000, "value": 1,
				"points": []any{[]any{10, 20, "bad"}, []any{2_000_000, 3_000_000, 7}}},
			map[string]any{"startDeltaUs": 0, "endDeltaUs": 1_000_000, "value": 2,
				"points": []any{}},
			// The valid group is the last raw entry; its parent identity must be
			// the group ordinal, not the number of accepted groups.
			map[string]any{"startDeltaUs": 0, "endDeltaUs": 1_000_000, "value": 3,
				"points": []any{[]any{0, 1_000_000, 8}}},
		},
	}}
	f.payload["routes"] = []any{}
	bundle, err := DecodeWorkoutBundleV6JSON(f.marshal(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Samples) != 1 || len(bundle.Series) != 1 {
		t.Fatalf("unusable groups were not skipped: samples=%d series=%d", len(bundle.Samples), len(bundle.Series))
	}
	parentUUID := v6UUID(f.datasetID, f.workoutID, powerType, "series", 5)
	if bundle.Samples[0].UUID != parentUUID || bundle.Samples[0].Quantity == nil ||
		bundle.Samples[0].Quantity.Value != 3 || bundle.Series[0].SampleUUID != parentUUID ||
		len(bundle.Series[0].Points) != 1 || bundle.Series[0].Points[0].Value != 8 {
		t.Fatalf("series ordinal/association wrong: parent=%+v series=%+v", bundle.Samples[0], bundle.Series[0])
	}
}

func TestDecodeWorkoutBundleV6SkipsUnusableRoutes(t *testing.T) {
	f := validV6Payload(t)
	f.payload["routes"] = []any{
		map[string]any{"points": []any{[]any{0, 200.0, 121.4}}},
		map[string]any{"points": []any{[]any{0, 31.2}}},
		map[string]any{"points": []any{}},
		map[string]any{"points": []any{[]any{10, 31.2, 121.4, nil, nil, nil, nil, nil}}},
	}
	bundle, err := DecodeWorkoutBundleV6JSON(f.marshal(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Routes) != 1 {
		t.Fatalf("unusable routes were not skipped: %+v", bundle.Routes)
	}
	if want := v6UUID(f.datasetID, f.workoutID, "", "route", 3); bundle.Routes[0].RouteID != want {
		t.Fatalf("route ordinal shifted: %s != %s", bundle.Routes[0].RouteID, want)
	}
}

func TestDecodeWorkoutBundleV6CategoryStream(t *testing.T) {
	f := validV6Payload(t)
	f.payload["streams"] = []any{map[string]any{
		"type": "HKCategoryTypeIdentifierSleepAnalysis",
		"samples": []any{
			[]any{0, 60_000_000, 2},
			[]any{60_000_000, 120_000_000, 9},
		},
		"series": []any{map[string]any{"startDeltaUs": 0, "endDeltaUs": 1, "value": 1,
			"points": []any{[]any{0, 1, 1}}}},
	}}
	f.payload["routes"] = []any{}
	bundle, err := DecodeWorkoutBundleV6JSON(f.marshal(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Samples) != 1 || bundle.Samples[0].Category == nil || *bundle.Samples[0].Category != 2 ||
		bundle.Samples[0].Quantity != nil || len(bundle.Series) != 0 {
		t.Fatalf("category stream not projected: %+v series=%+v", bundle.Samples, bundle.Series)
	}
}

func TestDecodeWorkoutBundleV6StrictEnvelope(t *testing.T) {
	cases := map[string]func(*v6Fixture){
		"missing protocolVersion": func(f *v6Fixture) { delete(f.payload, "protocolVersion") },
		"wrong protocolVersion":   func(f *v6Fixture) { f.payload["protocolVersion"] = 5 },
		"missing source":          func(f *v6Fixture) { delete(f.payload, "source") },
		"null source":             func(f *v6Fixture) { f.payload["source"] = nil },
		"missing workout":         func(f *v6Fixture) { delete(f.payload, "workout") },
		"workout without activityType": func(f *v6Fixture) {
			f.payload["workout"] = map[string]any{"durationUs": 60_000_000}
		},
		"workout duration beyond span": func(f *v6Fixture) {
			f.payload["workout"].(map[string]any)["durationUs"] = f.end - f.start + 1
		},
		"inverted envelope range": func(f *v6Fixture) {
			f.payload["startUs"], f.payload["endUs"] = f.end, f.start
		},
		"invalid workout id": func(f *v6Fixture) { f.payload["workoutID"] = "not-a-uuid" },
		"missing startUs":    func(f *v6Fixture) { delete(f.payload, "startUs") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := validV6Payload(t)
			mutate(&f)
			_, err := DecodeWorkoutBundleV6JSON(f.marshal(t))
			requireSeriesError(t, err, 422, "unsupported_payload")
		})
	}
}

func TestDecodeWorkoutBundleV6RejectsThirdPartyWorkoutSource(t *testing.T) {
	f := validV6Payload(t)
	f.payload["source"] = f.third
	_, err := DecodeWorkoutBundleV6JSON(f.marshal(t))
	requireSeriesError(t, err, 422, "source_policy_excluded")
}

func TestDecodeWorkoutBundleV6MixedSourceInheritsTopLevel(t *testing.T) {
	f := validV6Payload(t)
	// v6 has no per-sample source. A phone-written child fact is intentionally
	// attributed to the Watch Workout, exactly like the top-level device.
	f.payload["streams"].([]any)[0].(map[string]any)["samples"] = []any{
		[]any{0, 60_000_000, 120},
		[]any{60_000_000, 120_000_000, 121},
	}
	bundle, err := DecodeWorkoutBundleV6JSON(f.marshal(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range bundle.Samples {
		if sample.Source == nil || sample.Source.BundleID != v4TestSourceBundle {
			t.Fatalf("mixed-source child not attributed to top source: %+v", sample)
		}
	}
}

func TestDecodeWorkoutBundleV6PreservesUnknownTopLevelFieldInRaw(t *testing.T) {
	f := validV6Payload(t)
	f.payload["futureField"] = map[string]any{"value": 42}
	raw := f.marshal(t)
	if _, err := DecodeWorkoutBundleV6JSON(raw); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"futureField"`)) {
		t.Fatal("unknown top-level field not retained in the raw bytes")
	}
}

func TestDecodeWorkoutBundleV6StrictLexical(t *testing.T) {
	f := validV6Payload(t)
	raw := f.marshal(t)
	duplicate := bytes.Replace(raw, []byte(`"workoutID":`), []byte(`"workoutID":"`+f.workoutID+`","workoutID":`), 1)
	_, err := DecodeWorkoutBundleV6JSON(duplicate)
	requireSeriesError(t, err, 422, "unsupported_payload")

	invalidUTF8 := append(append([]byte{}, raw...), 0xff)
	_, err = DecodeWorkoutBundleV6JSON(invalidUTF8)
	requireSeriesError(t, err, 422, "unsupported_payload")

	notAnObject := append([]byte("["), append(raw, ']')...)
	_, err = DecodeWorkoutBundleV6JSON(notAnObject)
	requireSeriesError(t, err, 422, "unsupported_payload")
}

func TestDecodeWorkoutBundleV6DeltaOverflowSkipsSample(t *testing.T) {
	f := validV6Payload(t)
	f.payload["streams"].([]any)[0].(map[string]any)["samples"] = []any{
		[]any{int64(maxBundleTimeUs), 0, 120},
	}
	f.payload["streams"] = f.payload["streams"].([]any)[:1]
	f.payload["routes"] = []any{}
	bundle, err := DecodeWorkoutBundleV6JSON(f.marshal(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Samples) != 0 {
		t.Fatalf("out-of-range delta was projected: %+v", bundle.Samples)
	}
}

func TestDecodeWorkoutBundleV6SampleLimit(t *testing.T) {
	f := validV6Payload(t)
	samples := make([]any, MaxWorkoutBundleEvents+1)
	sample := []any{0, 1_000_000, 120}
	for index := range samples {
		samples[index] = sample
	}
	f.payload["streams"] = []any{map[string]any{
		"type": "HKQuantityTypeIdentifierHeartRate", "unit": "count/min", "samples": samples,
	}}
	f.payload["routes"] = []any{}
	_, err := DecodeWorkoutBundleV6JSON(f.marshal(t))
	requireSeriesError(t, err, 413, "bundle_too_large")
}

func TestDecodeWorkoutBundleV6CountsSkippedEntriesAndSeriesGroups(t *testing.T) {
	f := validV6Payload(t)
	samples := make([]any, MaxWorkoutBundleEvents)
	invalid := []any{"bad", 0, 0}
	for index := range samples {
		samples[index] = invalid
	}
	f.payload["streams"] = []any{map[string]any{
		"type": "HKQuantityTypeIdentifierHeartRate", "unit": "count/min",
		"samples": samples,
		"series": []any{map[string]any{"startDeltaUs": 0, "endDeltaUs": 1, "value": 1,
			"points": []any{[]any{0, 1, 1}}}},
	}}
	f.payload["routes"] = []any{}
	_, err := DecodeWorkoutBundleV6JSON(f.marshal(t))
	requireSeriesError(t, err, 413, "bundle_too_large")
}

func TestDecodeWorkoutBundleV6RouteLimit(t *testing.T) {
	f := validV6Payload(t)
	routes := make([]any, MaxBundleRoutes+1)
	for index := range routes {
		routes[index] = map[string]any{"points": []any{[]any{0, 31.2, 121.4}}}
	}
	f.payload["routes"] = routes
	_, err := DecodeWorkoutBundleV6JSON(f.marshal(t))
	requireSeriesError(t, err, 413, "bundle_too_large")
}

func TestProjectV6SeriesPointLimit(t *testing.T) {
	points := strings.Repeat("[0,1,1],", MaxBundleSeriesPoints) + "[0,1,1]"
	stream := json.RawMessage(fmt.Sprintf(`[{"type":"HKQuantityTypeIdentifierRunningPower","unit":"W",`+
		`"series":[{"startDeltaUs":0,"endDeltaUs":2000000,"value":1,"points":[%s]}]}]`, points))
	bundle := WorkoutBundle{DatasetID: NewID(), WorkoutID: NewID()}
	table := &BundleSourceTable{Samples: map[string]HealthKitSource{}, Routes: map[string]HealthKitSource{}}
	source := HealthKitSource{BundleID: v4TestSourceBundle}
	err := projectV6Streams(&bundle, table, stream, source, 0)
	requireSeriesError(t, err, 413, "bundle_too_large")
}

func TestProjectV6RoutePointLimit(t *testing.T) {
	points := strings.Repeat("[0,31.2,121.4],", MaxBundleRoutePoints) + "[0,31.2,121.4]"
	routes := json.RawMessage(fmt.Sprintf(`[{"points":[%s]}]`, points))
	bundle := WorkoutBundle{DatasetID: NewID(), WorkoutID: NewID()}
	table := &BundleSourceTable{Samples: map[string]HealthKitSource{}, Routes: map[string]HealthKitSource{}}
	source := HealthKitSource{BundleID: v4TestSourceBundle}
	err := projectV6Routes(&bundle, table, routes, source, 0)
	requireSeriesError(t, err, 413, "bundle_too_large")
}

// TestDecodeWorkoutBundleV6FortyMinuteSynthetic builds one 40-minute run with
// per-second heart rate, running power series points and route points. It logs
// the exact payload size used by the delivery report.
func TestDecodeWorkoutBundleV6FortyMinuteSynthetic(t *testing.T) {
	f := validV6Payload(t)
	const seconds = 40 * 60
	const stepUs = int64(1_000_000)
	duration := int64(seconds) * stepUs
	f.end = f.start + duration
	f.payload["endUs"] = f.end
	f.payload["workout"] = map[string]any{"activityType": 37, "durationUs": duration}

	hr := make([]any, 0, seconds)
	power := make([]any, 0, seconds)
	route := make([]any, 0, seconds)
	for index := 0; index < seconds; index++ {
		start, end := int64(index)*stepUs, int64(index+1)*stepUs
		hr = append(hr, []any{start, end, 88 + index%40})
		power = append(power, []any{start, end, 180 + float64(index%30)})
		route = append(route, []any{start, 31.2 + float64(index)/1e6, 121.4, nil, nil, nil, nil, nil})
	}
	f.payload["streams"] = []any{
		map[string]any{"type": "HKQuantityTypeIdentifierHeartRate", "unit": "count/min",
			"status": "complete", "samples": hr},
		map[string]any{"type": "HKQuantityTypeIdentifierRunningPower", "unit": "W",
			"series": []any{map[string]any{
				"startDeltaUs": 0, "endDeltaUs": duration, "value": 192.5, "points": power,
			}}},
	}
	f.payload["routes"] = []any{map[string]any{"points": route}}
	raw := f.marshal(t)
	bundle, err := DecodeWorkoutBundleV6JSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Samples) != seconds+1 || len(bundle.Series) != 1 || len(bundle.Series[0].Points) != seconds ||
		len(bundle.Routes) != 1 || len(bundle.Routes[0].Points) != seconds {
		t.Fatalf("40-minute projection: samples=%d series=%d points=%d routePoints=%d", len(bundle.Samples),
			len(bundle.Series), len(bundle.Series[0].Points), len(bundle.Routes[0].Points))
	}
	t.Logf("40-minute v6 payload bytes=%d samples=%d", len(raw), len(bundle.Samples))
}
