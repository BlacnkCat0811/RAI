package syncing

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"unicode/utf8"
)

// Protocol 5 is the compact encoding of the same raw Workout facts as protocol
// 4. Sources and metadata move to top-level tables, samples become columnar
// arrays, and every time is a signed microsecond delta from the envelope
// startUs. Sample UUIDs stay per sample: they are the health_samples identity,
// not a repeated envelope value.
const (
	maxV5Sources      = 64
	maxV5MetadataRows = 50_000
)

var errBundleV5 = Fail(422, "unsupported_payload")

type v5Envelope struct {
	ProtocolVersion *int            `json:"protocolVersion"`
	DatasetID       *string         `json:"datasetID"`
	DatasetEpoch    *string         `json:"datasetEpoch"`
	CollectorEpoch  *string         `json:"collectorEpoch"`
	WorkoutID       *string         `json:"workoutID"`
	StartUs         *int64          `json:"startUs"`
	EndUs           *int64          `json:"endUs"`
	Source          json.RawMessage `json:"source"`
	Metadata        json.RawMessage `json:"metadata"`
	Workout         json.RawMessage `json:"workout"`
	SourceTable     json.RawMessage `json:"sourceTable"`
	MetadataTable   json.RawMessage `json:"metadataTable"`
	Streams         json.RawMessage `json:"streams"`
	Series          json.RawMessage `json:"series"`
	Routes          json.RawMessage `json:"routes"`
}

type v5Stream struct {
	Type    *string           `json:"type"`
	Unit    *string           `json:"unit"`
	Status  json.RawMessage   `json:"status"`
	Error   json.RawMessage   `json:"error"`
	Source  *int              `json:"source"`
	Samples []json.RawMessage `json:"samples"`
}

type v5Series struct {
	Type       *string           `json:"type"`
	SampleUUID *string           `json:"sampleUUID"`
	Unit       *string           `json:"unit"`
	Points     []json.RawMessage `json:"points"`
}

type v5Route struct {
	RouteID *string           `json:"routeID"`
	Source  *int              `json:"source"`
	Points  []json.RawMessage `json:"points"`
}

type v5SourceEntry struct {
	source HealthKitSource
	ok     bool
}

type v5SampleValue struct {
	uuid          string
	start         int64
	end           int64
	value         float64
	source        HealthKitSource
	metadataIndex *int
}

func decodeV5Envelope(raw []byte) (v5Envelope, error) {
	var zero v5Envelope
	if !utf8.Valid(raw) {
		return zero, errBundleV5
	}
	lexical := json.NewDecoder(bytes.NewReader(raw))
	if err := walkV4JSON(lexical, 0); err != nil {
		return zero, errBundleV5
	}
	if _, err := lexical.Token(); err != io.EOF {
		return zero, errBundleV5
	}
	var wire v5Envelope
	if err := json.Unmarshal(raw, &wire); err != nil {
		return zero, errBundleV5
	}
	if wire.ProtocolVersion == nil || *wire.ProtocolVersion != 5 || wire.DatasetID == nil ||
		wire.DatasetEpoch == nil || wire.CollectorEpoch == nil || wire.WorkoutID == nil ||
		wire.StartUs == nil || wire.EndUs == nil || len(wire.Source) == 0 || isJSONNull(wire.Source) ||
		len(wire.Workout) == 0 || isJSONNull(wire.Workout) {
		return zero, errBundleV5
	}
	if !ValidID(*wire.DatasetID) || !ValidID(*wire.DatasetEpoch) ||
		!ValidID(*wire.CollectorEpoch) || !ValidID(*wire.WorkoutID) {
		return zero, errBundleV5
	}
	if !validV4Range(*wire.StartUs, *wire.EndUs) {
		return zero, errBundleV5
	}
	return wire, nil
}

func v5SourceEntries(raw json.RawMessage) ([]v5SourceEntry, error) {
	if len(raw) == 0 || isJSONNull(raw) {
		return nil, nil
	}
	items, ok := v4Array(raw)
	if !ok {
		return nil, errBundleV5
	}
	if len(items) > maxV5Sources {
		return nil, Fail(413, "bundle_too_large")
	}
	entries := make([]v5SourceEntry, len(items))
	for index, item := range items {
		source, ok := v4HealthSource(item)
		entries[index] = v5SourceEntry{source: source, ok: ok}
	}
	return entries, nil
}

func v5MetadataEntries(raw json.RawMessage) ([]map[string]any, error) {
	if len(raw) == 0 || isJSONNull(raw) {
		return nil, nil
	}
	items, ok := v4Array(raw)
	if !ok {
		return nil, errBundleV5
	}
	if len(items) > maxV5MetadataRows {
		return nil, Fail(413, "bundle_too_large")
	}
	entries := make([]map[string]any, len(items))
	for index, item := range items {
		metadata, ok := v4MetadataValue(item)
		if ok {
			entries[index] = metadata
		}
	}
	return entries, nil
}

func addBundleBase(base, delta int64) (int64, bool) {
	value := base + delta
	if (delta > 0 && value < base) || (delta < 0 && value > base) {
		return 0, false
	}
	if value < minBundleTimeUs || value > maxBundleTimeUs {
		return 0, false
	}
	return value, true
}

func decodeV5Sample(raw json.RawMessage, base int64, sources []v5SourceEntry, fallback *int) (v5SampleValue, bool) {
	items, ok := v4Array(raw)
	if !ok || len(items) < 4 || len(items) > 6 {
		return v5SampleValue{}, false
	}
	var uuid *string
	var startDelta, endDelta *int64
	var value *float64
	if json.Unmarshal(items[0], &uuid) != nil || uuid == nil || !ValidID(*uuid) ||
		json.Unmarshal(items[1], &startDelta) != nil || startDelta == nil ||
		json.Unmarshal(items[2], &endDelta) != nil || endDelta == nil ||
		json.Unmarshal(items[3], &value) != nil || value == nil ||
		math.IsNaN(*value) || math.IsInf(*value, 0) {
		return v5SampleValue{}, false
	}
	start, ok := addBundleBase(base, *startDelta)
	if !ok {
		return v5SampleValue{}, false
	}
	end, ok := addBundleBase(base, *endDelta)
	if !ok || start > end {
		return v5SampleValue{}, false
	}
	var sourceIndex *int
	if len(items) >= 6 && !isJSONNull(items[5]) {
		if json.Unmarshal(items[5], &sourceIndex) != nil || sourceIndex == nil {
			return v5SampleValue{}, false
		}
	}
	source, ok := v5ResolveSource(sourceIndex, fallback, sources)
	if !ok {
		return v5SampleValue{}, false
	}
	var metadataIndex *int
	if len(items) >= 5 && !isJSONNull(items[4]) {
		if json.Unmarshal(items[4], &metadataIndex) != nil || metadataIndex == nil {
			return v5SampleValue{}, false
		}
	}
	return v5SampleValue{uuid: *uuid, start: start, end: end, value: *value,
		source: source, metadataIndex: metadataIndex}, true
}

func v5ResolveSource(index *int, fallback *int, sources []v5SourceEntry) (HealthKitSource, bool) {
	chosen := index
	if chosen == nil {
		chosen = fallback
	}
	if chosen == nil || *chosen < 0 || *chosen >= len(sources) || !sources[*chosen].ok {
		return HealthKitSource{}, false
	}
	return sources[*chosen].source, true
}

func projectV5Streams(bundle *WorkoutBundle, table *BundleSourceTable, raw json.RawMessage,
	sources []v5SourceEntry, metadataTable []map[string]any, base int64, parent *HealthKitSource) error {
	streams, ok := v4Array(raw)
	if !ok {
		return nil
	}
	seenTypes := make(map[string]bool, len(streams))
	rawSamples := 0
	for _, rawStream := range streams {
		var stream v5Stream
		if json.Unmarshal(rawStream, &stream) != nil || stream.Type == nil {
			continue
		}
		sampleType := *stream.Type
		unit, known := Units[sampleType]
		if !known || sampleType == WorkoutType || seenTypes[sampleType] {
			continue
		}
		seenTypes[sampleType] = true
		if stream.Unit != nil && *stream.Unit != unit {
			continue
		}
		rawSamples += len(stream.Samples)
		if rawSamples > MaxWorkoutBundleEvents {
			return Fail(413, "bundle_too_large")
		}
		var fallback *int
		if stream.Source != nil && *stream.Source >= 0 && *stream.Source < len(sources) {
			fallback = stream.Source
		}
		for _, rawSample := range stream.Samples {
			sample, ok := decodeV5Sample(rawSample, base, sources, fallback)
			if !ok {
				continue
			}
			var metadata map[string]any
			if sample.metadataIndex != nil && *sample.metadataIndex >= 0 && *sample.metadataIndex < len(metadataTable) {
				metadata = metadataTable[*sample.metadataIndex]
			}
			projectBundleSample(bundle, table, sampleType, unit, bundleSampleValue{
				uuid: sample.uuid, start: sample.start, end: sample.end, value: sample.value,
				source: sample.source, metadata: metadata,
			}, parent)
		}
	}
	return nil
}

func v5Triple(raw json.RawMessage, base int64) (start, end int64, value float64, ok bool) {
	items, ok := v4Array(raw)
	if !ok || len(items) != 3 {
		return 0, 0, 0, false
	}
	var startDelta, endDelta *int64
	var floatValue *float64
	if json.Unmarshal(items[0], &startDelta) != nil || startDelta == nil ||
		json.Unmarshal(items[1], &endDelta) != nil || endDelta == nil ||
		json.Unmarshal(items[2], &floatValue) != nil || floatValue == nil ||
		math.IsNaN(*floatValue) || math.IsInf(*floatValue, 0) {
		return 0, 0, 0, false
	}
	start, ok = addBundleBase(base, *startDelta)
	if !ok {
		return 0, 0, 0, false
	}
	end, ok = addBundleBase(base, *endDelta)
	if !ok || start > end {
		return 0, 0, 0, false
	}
	return start, end, *floatValue, true
}

func projectV5Series(samples *[]Event, raw json.RawMessage, base int64) ([]WorkoutBundleSeries, error) {
	items, ok := v4Array(raw)
	if !ok {
		return nil, nil
	}
	if len(items) > MaxBundleSeries {
		return nil, Fail(413, "bundle_too_large")
	}
	parents := make(map[string]Event, len(*samples))
	for _, sample := range *samples {
		parents[sample.Type+":"+sample.UUID] = sample
	}
	seen := make(map[string]bool, len(items))
	out := make([]WorkoutBundleSeries, 0, len(items))
	rawPoints := 0
	for _, rawSeries := range items {
		var wire v5Series
		if json.Unmarshal(rawSeries, &wire) != nil || wire.Type == nil || wire.SampleUUID == nil {
			continue
		}
		sampleType := *wire.Type
		unit, known := Units[sampleType]
		if !known || unit == "" || sampleType == WorkoutType || !ValidID(*wire.SampleUUID) {
			continue
		}
		if wire.Unit != nil && *wire.Unit != unit {
			continue
		}
		parent, ok := parents[sampleType+":"+*wire.SampleUUID]
		if !ok {
			continue
		}
		key := sampleType + ":" + *wire.SampleUUID
		if seen[key] {
			continue
		}
		rawPoints += len(wire.Points)
		if rawPoints > MaxBundleSeriesPoints {
			return nil, Fail(413, "bundle_too_large")
		}
		points := make([]SeriesPoint, 0, len(wire.Points))
		for _, rawPoint := range wire.Points {
			start, end, value, ok := v5Triple(rawPoint, base)
			if !ok || start < *parent.StartUs || end > *parent.EndUs {
				continue
			}
			points = append(points, SeriesPoint{StartUs: start, EndUs: end, Value: value, Unit: unit})
		}
		if len(points) == 0 {
			continue
		}
		seen[key] = true
		out = append(out, WorkoutBundleSeries{SampleType: sampleType, SampleUUID: *wire.SampleUUID, Points: points})
	}
	return out, nil
}

func v5RoutePoint(raw json.RawMessage, base int64) (WorkoutBundlePoint, bool) {
	items, ok := v4Array(raw)
	if !ok || len(items) < 3 || len(items) > 8 {
		return WorkoutBundlePoint{}, false
	}
	var delta *int64
	var latitude, longitude *float64
	if json.Unmarshal(items[0], &delta) != nil || delta == nil ||
		json.Unmarshal(items[1], &latitude) != nil || latitude == nil ||
		json.Unmarshal(items[2], &longitude) != nil || longitude == nil {
		return WorkoutBundlePoint{}, false
	}
	recordedAt, ok := addBundleBase(base, *delta)
	if !ok {
		return WorkoutBundlePoint{}, false
	}
	point := WorkoutBundlePoint{RecordedAtUs: recordedAt, Latitude: *latitude, Longitude: *longitude}
	optional := []**float64{&point.Altitude, &point.HorizontalAccuracy, &point.VerticalAccuracy, &point.Speed, &point.Course}
	for index := 0; index < len(items)-3; index++ {
		if isJSONNull(items[3+index]) {
			continue
		}
		var value *float64
		if json.Unmarshal(items[3+index], &value) != nil || value == nil ||
			math.IsNaN(*value) || math.IsInf(*value, 0) {
			return WorkoutBundlePoint{}, false
		}
		*optional[index] = value
	}
	if point.ToRoutePoint(0).Validate() != nil {
		return WorkoutBundlePoint{}, false
	}
	return point, true
}

func projectV5Routes(bundle *WorkoutBundle, table *BundleSourceTable, raw json.RawMessage,
	sources []v5SourceEntry, base int64, parent *HealthKitSource) ([]WorkoutBundleRoute, error) {
	items, ok := v4Array(raw)
	if !ok {
		return nil, nil
	}
	if len(items) > MaxBundleRoutes {
		return nil, Fail(413, "bundle_too_large")
	}
	seen := make(map[string]bool, len(items))
	out := make([]WorkoutBundleRoute, 0, len(items))
	rawPoints := 0
	for _, rawRoute := range items {
		var wire v5Route
		if json.Unmarshal(rawRoute, &wire) != nil || wire.RouteID == nil || !ValidID(*wire.RouteID) ||
			wire.Source == nil || seen[*wire.RouteID] {
			continue
		}
		if *wire.Source < 0 || *wire.Source >= len(sources) || !sources[*wire.Source].ok {
			continue
		}
		source := sources[*wire.Source].source
		if !v4Eligible(source, parent) {
			continue
		}
		rawPoints += len(wire.Points)
		if rawPoints > MaxBundleRoutePoints {
			return nil, Fail(413, "bundle_too_large")
		}
		points := make([]WorkoutBundlePoint, 0, len(wire.Points))
		for _, rawPoint := range wire.Points {
			point, ok := v5RoutePoint(rawPoint, base)
			if !ok {
				continue
			}
			points = append(points, point)
		}
		if len(points) == 0 {
			continue
		}
		seen[*wire.RouteID] = true
		table.Routes[*wire.RouteID] = source
		out = append(out, WorkoutBundleRoute{RouteID: *wire.RouteID, Source: Source{BundleID: source.BundleID}, Points: points})
	}
	return out, nil
}

// DecodeWorkoutBundleV5JSON is the wire boundary for protocolVersion 5. It
// performs the same lexical, identity and Workout-core checks as v4, then
// resolves the compact source/metadata tables and delta times before reusing the
// shared fact projection.
func DecodeWorkoutBundleV5JSON(raw []byte) (WorkoutBundle, error) {
	wire, err := decodeV5Envelope(raw)
	if err != nil {
		return WorkoutBundle{}, err
	}
	workoutSource, ok := v4HealthSource(wire.Source)
	if !ok {
		return v4Bad()
	}
	if workoutSource.Classification(nil) != "apple_watch" {
		return WorkoutBundle{}, Fail(422, "source_policy_excluded")
	}
	start, end := *wire.StartUs, *wire.EndUs
	workout, err := decodeV4Workout(wire.Workout, start, end)
	if err != nil {
		return WorkoutBundle{}, bundleValidationContext(err, "workout", "invalid_workout")
	}
	workoutEvent := Event{
		Op: "upsert", Kind: "workout", Type: WorkoutType, UUID: *wire.WorkoutID,
		StartUs: &start, EndUs: &end,
		Source:  &Source{BundleID: workoutSource.BundleID},
		Workout: &workout,
	}
	if metadata, ok := v4MetadataValue(wire.Metadata); ok {
		delete(metadata, VO2Reserved)
		if len(metadata) > 0 {
			workoutEvent.Metadata = metadata
		}
	}
	bundle := WorkoutBundle{
		ProtocolVersion: 5,
		DatasetID:       *wire.DatasetID,
		DatasetEpoch:    *wire.DatasetEpoch,
		CollectorEpoch:  *wire.CollectorEpoch,
		WorkoutID:       *wire.WorkoutID,
		Workout:         workoutEvent,
	}
	sources, err := v5SourceEntries(wire.SourceTable)
	if err != nil {
		return WorkoutBundle{}, err
	}
	metadataTable, err := v5MetadataEntries(wire.MetadataTable)
	if err != nil {
		return WorkoutBundle{}, err
	}
	table := &BundleSourceTable{
		Workout: workoutSource,
		Samples: make(map[string]HealthKitSource),
		Routes:  make(map[string]HealthKitSource),
	}
	if err := projectV5Streams(&bundle, table, wire.Streams, sources, metadataTable, start, &workoutSource); err != nil {
		return WorkoutBundle{}, err
	}
	series, err := projectV5Series(&bundle.Samples, wire.Series, start)
	if err != nil {
		return WorkoutBundle{}, err
	}
	bundle.Series = series
	routes, err := projectV5Routes(&bundle, table, wire.Routes, sources, start, &workoutSource)
	if err != nil {
		return WorkoutBundle{}, err
	}
	bundle.Routes = routes
	bundle.SourceTable = table
	if err := bundle.Validate(); err != nil {
		return WorkoutBundle{}, err
	}
	return bundle, nil
}

// WorkoutBundleV5SourceTable extracts the source descriptors from an already
// stored protocol-5 payload. Analysis and feature loaders use it instead of
// re-projecting facts they have already read from the normalized tables.
func WorkoutBundleV5SourceTable(raw []byte) (*BundleSourceTable, error) {
	wire, err := decodeV5Envelope(raw)
	if err != nil {
		return nil, err
	}
	workoutSource, ok := v4HealthSource(wire.Source)
	if !ok {
		return nil, Fail(422, "unsupported_payload")
	}
	sources, err := v5SourceEntries(wire.SourceTable)
	if err != nil {
		return nil, err
	}
	table := &BundleSourceTable{
		Workout: workoutSource,
		Samples: make(map[string]HealthKitSource),
		Routes:  make(map[string]HealthKitSource),
	}
	streams, _ := v4Array(wire.Streams)
	for _, rawStream := range streams {
		var stream v5Stream
		if json.Unmarshal(rawStream, &stream) != nil || stream.Type == nil {
			continue
		}
		sampleType := *stream.Type
		if _, known := Units[sampleType]; !known || sampleType == WorkoutType {
			continue
		}
		var fallback *int
		if stream.Source != nil && *stream.Source >= 0 && *stream.Source < len(sources) {
			fallback = stream.Source
		}
		for _, rawSample := range stream.Samples {
			items, ok := v4Array(rawSample)
			if !ok || len(items) < 4 || len(items) > 6 {
				continue
			}
			var uuid *string
			if json.Unmarshal(items[0], &uuid) != nil || uuid == nil || !ValidID(*uuid) {
				continue
			}
			var sourceIndex *int
			if len(items) >= 6 && !isJSONNull(items[5]) {
				if json.Unmarshal(items[5], &sourceIndex) != nil {
					continue
				}
			}
			source, ok := v5ResolveSource(sourceIndex, fallback, sources)
			if !ok {
				continue
			}
			table.Samples[sampleType+":"+*uuid] = source
		}
	}
	routes, _ := v4Array(wire.Routes)
	for _, rawRoute := range routes {
		var route v5Route
		if json.Unmarshal(rawRoute, &route) != nil || route.RouteID == nil ||
			!ValidID(*route.RouteID) || route.Source == nil {
			continue
		}
		if *route.Source < 0 || *route.Source >= len(sources) || !sources[*route.Source].ok {
			continue
		}
		table.Routes[*route.RouteID] = sources[*route.Source].source
	}
	return table, nil
}

// DecodeWorkoutBundlePayloadJSON dispatches on the envelope protocolVersion so
// every raw-payload entry accepts both supported compact versions.
func DecodeWorkoutBundlePayloadJSON(raw []byte) (WorkoutBundle, error) {
	var header struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if json.Unmarshal(raw, &header) != nil {
		return WorkoutBundle{}, Fail(422, "unsupported_payload")
	}
	switch header.ProtocolVersion {
	case 4:
		return DecodeWorkoutBundleV4JSON(raw)
	case 5:
		return DecodeWorkoutBundleV5JSON(raw)
	case 6:
		return DecodeWorkoutBundleV6JSON(raw)
	default:
		return WorkoutBundle{}, Fail(422, "unsupported_protocol")
	}
}

// WorkoutBundlePayloadSourceTable extracts sources from either raw protocol.
// Callers that persisted a mixed history must not special-case v4 forever.
func WorkoutBundlePayloadSourceTable(raw []byte) (*BundleSourceTable, error) {
	var header struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if json.Unmarshal(raw, &header) != nil {
		return nil, Fail(422, "unsupported_payload")
	}
	switch header.ProtocolVersion {
	case 4:
		return WorkoutBundleV4SourceTable(raw)
	case 5:
		return WorkoutBundleV5SourceTable(raw)
	case 6:
		return WorkoutBundleV6SourceTable(raw)
	default:
		return nil, Fail(422, "unsupported_protocol")
	}
}
