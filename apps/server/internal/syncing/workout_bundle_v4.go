package syncing

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"sort"
	"unicode/utf8"
)

// Protocol 4 is the raw Workout upload. The client sends the HealthKit values it
// observed, one object per sample, with the source descriptor inline. The
// server interprets only the fields it knows, projects usable facts into the
// normalized tables, and keeps everything else in the exact stored bytes.
//
// Unlike v3, a bad optional stream, sample or point does not fail the bundle:
// it is skipped in the projection while the original payload remains the
// authoritative copy. Only the envelope identity and the Workout core are
// strict, because a bundle without them cannot be placed on the timeline.
const maxV4JSONDepth = 128

var errBundleV4 = errors.New("invalid v4 bundle")

const (
	minBundleTimeUs = -62135596800000000
	maxBundleTimeUs = 253402300799999999
)

type v4Envelope struct {
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
	Streams         json.RawMessage `json:"streams"`
	Series          json.RawMessage `json:"series"`
	Routes          json.RawMessage `json:"routes"`
}

type v4Stream struct {
	Type    *string           `json:"type"`
	Unit    *string           `json:"unit"`
	Status  json.RawMessage   `json:"status"`
	Error   json.RawMessage   `json:"error"`
	Samples []json.RawMessage `json:"samples"`
}

type v4Sample struct {
	UUID     *string         `json:"uuid"`
	StartUs  *int64          `json:"startUs"`
	EndUs    *int64          `json:"endUs"`
	Value    *float64        `json:"value"`
	Source   json.RawMessage `json:"source"`
	Metadata json.RawMessage `json:"metadata"`
}

type v4Series struct {
	Type       *string           `json:"type"`
	SampleUUID *string           `json:"sampleUUID"`
	Unit       *string           `json:"unit"`
	Points     []json.RawMessage `json:"points"`
}

type v4Route struct {
	RouteID *string           `json:"routeID"`
	Source  json.RawMessage   `json:"source"`
	Points  []json.RawMessage `json:"points"`
}

func isJSONNull(raw []byte) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

func v4Bad() (WorkoutBundle, error) { return WorkoutBundle{}, Fail(422, "unsupported_payload") }

// walkV4JSON rejects duplicate object keys and pathological nesting before the
// typed decode. Unknown fields are intentionally legal in this protocol.
func walkV4JSON(decoder *json.Decoder, depth int) error {
	if depth > maxV4JSONDepth {
		return errBundleV4
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]bool, 8)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok || seen[key] {
				return errBundleV4
			}
			seen[key] = true
			if err := walkV4JSON(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := walkV4JSON(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return errBundleV4
	}
	_, err = decoder.Token()
	return err
}

// decodeV4Envelope performs only the lexical and identity checks. It never
// validates facts: those are projected one by one in DecodeWorkoutBundleV4JSON.
func decodeV4Envelope(raw []byte) (v4Envelope, error) {
	var zero v4Envelope
	if !utf8.Valid(raw) {
		return zero, errBundleV4
	}
	lexical := json.NewDecoder(bytes.NewReader(raw))
	if err := walkV4JSON(lexical, 0); err != nil {
		return zero, errBundleV4
	}
	if _, err := lexical.Token(); err != io.EOF {
		return zero, errBundleV4
	}
	var wire v4Envelope
	if err := json.Unmarshal(raw, &wire); err != nil {
		return zero, errBundleV4
	}
	if wire.ProtocolVersion == nil || wire.DatasetID == nil || wire.DatasetEpoch == nil ||
		wire.CollectorEpoch == nil || wire.WorkoutID == nil || wire.StartUs == nil || wire.EndUs == nil ||
		len(wire.Source) == 0 || isJSONNull(wire.Source) || len(wire.Workout) == 0 || isJSONNull(wire.Workout) {
		return zero, errBundleV4
	}
	if !ValidID(*wire.DatasetID) || !ValidID(*wire.DatasetEpoch) ||
		!ValidID(*wire.CollectorEpoch) || !ValidID(*wire.WorkoutID) {
		return zero, errBundleV4
	}
	if !validV4Range(*wire.StartUs, *wire.EndUs) {
		return zero, errBundleV4
	}
	return wire, nil
}

func validV4Range(start, end int64) bool {
	return start >= minBundleTimeUs && start <= maxBundleTimeUs && end >= start && end <= maxBundleTimeUs
}

func v4Object(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 || isJSONNull(raw) {
		return nil, false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, false
	}
	return fields, true
}

func v4Array(raw json.RawMessage) ([]json.RawMessage, bool) {
	if len(raw) == 0 || isJSONNull(raw) {
		return nil, false
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, false
	}
	return items, true
}

// v4MetadataValue keeps every unknown key and number exactly as submitted. The
// map is stored into jsonb; nothing here interprets or allowlists a key.
func v4MetadataValue(raw json.RawMessage) (map[string]any, bool) {
	if len(raw) == 0 || isJSONNull(raw) {
		return nil, false
	}
	var object map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, false
	}
	return object, true
}

func v4HealthSource(raw json.RawMessage) (HealthKitSource, bool) {
	var source HealthKitSource
	if len(raw) == 0 || isJSONNull(raw) || json.Unmarshal(raw, &source) != nil || source.Validate() != nil {
		return HealthKitSource{}, false
	}
	return source, true
}

func v4Eligible(source HealthKitSource, parent *HealthKitSource) bool {
	class := source.Classification(parent)
	return class == "apple_watch" || class == "associated_apple_watch"
}

// DecodeWorkoutBundleV4JSON is the wire boundary for protocolVersion 4. It
// rejects malformed JSON, duplicate keys and missing identity, then projects
// every usable fact. Per-fact errors are skipped rather than failing the whole
// upload.
func DecodeWorkoutBundleV4JSON(raw []byte) (WorkoutBundle, error) {
	wire, err := decodeV4Envelope(raw)
	if err != nil {
		return v4Bad()
	}
	if *wire.ProtocolVersion != 4 {
		return WorkoutBundle{}, bundleField("protocolVersion", "unsupported_protocol")
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
		// The reserved VO2 key is server-written provenance, never client input.
		// The exact bytes remain in the stored payload.
		delete(metadata, VO2Reserved)
		if len(metadata) > 0 {
			workoutEvent.Metadata = metadata
		}
	}
	bundle := WorkoutBundle{
		ProtocolVersion: 4,
		DatasetID:       *wire.DatasetID,
		DatasetEpoch:    *wire.DatasetEpoch,
		CollectorEpoch:  *wire.CollectorEpoch,
		WorkoutID:       *wire.WorkoutID,
		Workout:         workoutEvent,
	}
	table := &BundleSourceTable{
		Workout: workoutSource,
		Samples: make(map[string]HealthKitSource),
		Routes:  make(map[string]HealthKitSource),
	}
	if err := projectV4Streams(&bundle, table, wire.Streams, &workoutSource); err != nil {
		return WorkoutBundle{}, err
	}
	series, err := projectV4Series(&bundle.Samples, wire.Series)
	if err != nil {
		return WorkoutBundle{}, err
	}
	bundle.Series = series
	routes, err := projectV4Routes(&bundle, table, wire.Routes, &workoutSource)
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

// WorkoutBundleV4SourceTable extracts the source descriptors from an already
// stored protocol-4 payload. Analysis and feature loaders use it instead of
// re-projecting facts they have already read from the normalized tables.
func WorkoutBundleV4SourceTable(raw []byte) (*BundleSourceTable, error) {
	wire, err := decodeV4Envelope(raw)
	if err != nil {
		return nil, Fail(422, "unsupported_payload")
	}
	if *wire.ProtocolVersion != 4 {
		return nil, Fail(422, "unsupported_protocol")
	}
	workoutSource, ok := v4HealthSource(wire.Source)
	if !ok {
		return nil, Fail(422, "unsupported_payload")
	}
	table := &BundleSourceTable{
		Workout: workoutSource,
		Samples: make(map[string]HealthKitSource),
		Routes:  make(map[string]HealthKitSource),
	}
	streams, _ := v4Array(wire.Streams)
	for _, rawStream := range streams {
		var stream v4Stream
		if json.Unmarshal(rawStream, &stream) != nil || stream.Type == nil {
			continue
		}
		sampleType := *stream.Type
		if _, known := Units[sampleType]; !known || sampleType == WorkoutType {
			continue
		}
		for _, rawSample := range stream.Samples {
			var sample v4Sample
			if json.Unmarshal(rawSample, &sample) != nil || sample.UUID == nil || !ValidID(*sample.UUID) {
				continue
			}
			source, ok := v4HealthSource(sample.Source)
			if !ok {
				continue
			}
			table.Samples[sampleType+":"+*sample.UUID] = source
		}
	}
	routes, _ := v4Array(wire.Routes)
	for _, rawRoute := range routes {
		var route v4Route
		if json.Unmarshal(rawRoute, &route) != nil || route.RouteID == nil || !ValidID(*route.RouteID) {
			continue
		}
		source, ok := v4HealthSource(route.Source)
		if !ok {
			continue
		}
		table.Routes[*route.RouteID] = source
	}
	return table, nil
}

func projectV4Streams(bundle *WorkoutBundle, table *BundleSourceTable, raw json.RawMessage, parent *HealthKitSource) error {
	streams, ok := v4Array(raw)
	if !ok {
		return nil
	}
	seenTypes := make(map[string]bool, len(streams))
	rawSamples := 0
	for _, rawStream := range streams {
		var stream v4Stream
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
		for _, rawSample := range stream.Samples {
			sample, ok := decodeV4Sample(rawSample)
			if !ok {
				continue
			}
			source, ok := v4HealthSource(sample.source)
			if !ok {
				continue
			}
			metadata, _ := v4MetadataValue(sample.metadata)
			projectBundleSample(bundle, table, sampleType, unit, bundleSampleValue{
				uuid: sample.uuid, start: sample.start, end: sample.end, value: sample.value,
				source: source, metadata: metadata,
			}, parent)
		}
	}
	return nil
}

type v4SampleValue struct {
	uuid     string
	start    int64
	end      int64
	value    float64
	source   json.RawMessage
	metadata json.RawMessage
}

// bundleSampleValue is the decoded sample both protocol decoders project. The
// wire differs (v4 inline objects, v5 columnar arrays and tables), but duplicate
// suppression, source eligibility, value interpretation and metadata projection
// must stay identical.
type bundleSampleValue struct {
	uuid     string
	start    int64
	end      int64
	value    float64
	source   HealthKitSource
	metadata map[string]any
}

func projectBundleSample(bundle *WorkoutBundle, table *BundleSourceTable, sampleType, unit string,
	sample bundleSampleValue, parent *HealthKitSource) bool {
	key := sampleType + ":" + sample.uuid
	if _, duplicate := table.Samples[key]; duplicate {
		return false
	}
	if !v4Eligible(sample.source, parent) {
		return false
	}
	event := Event{
		Op: "upsert", Kind: "sample", Type: sampleType, UUID: sample.uuid,
		StartUs: &sample.start, EndUs: &sample.end,
		Source: &Source{BundleID: sample.source.BundleID},
	}
	if unit == "" {
		if math.IsNaN(sample.value) || math.IsInf(sample.value, 0) ||
			sample.value != math.Trunc(sample.value) || sample.value < 0 || sample.value > 5 {
			return false
		}
		category := int(sample.value)
		event.Category = &category
	} else {
		event.Quantity = &Quantity{Value: sample.value, Unit: unit}
	}
	if len(sample.metadata) > 0 {
		// A v5 metadata table entry can be referenced by many samples. Each
		// projected Event gets its own map so later enrichment cannot alias facts.
		metadata := make(map[string]any, len(sample.metadata))
		for key, value := range sample.metadata {
			metadata[key] = value
		}
		delete(metadata, VO2Reserved)
		if len(metadata) > 0 {
			event.Metadata = metadata
		}
	}
	table.Samples[key] = sample.source
	bundle.Samples = append(bundle.Samples, event)
	return true
}

func decodeV4Sample(raw json.RawMessage) (v4SampleValue, bool) {
	var wire v4Sample
	if json.Unmarshal(raw, &wire) != nil || wire.UUID == nil || !ValidID(*wire.UUID) ||
		wire.StartUs == nil || wire.EndUs == nil || wire.Value == nil {
		return v4SampleValue{}, false
	}
	if !validV4Range(*wire.StartUs, *wire.EndUs) ||
		math.IsNaN(*wire.Value) || math.IsInf(*wire.Value, 0) {
		return v4SampleValue{}, false
	}
	return v4SampleValue{uuid: *wire.UUID, start: *wire.StartUs, end: *wire.EndUs,
		value: *wire.Value, source: wire.Source, metadata: wire.Metadata}, true
}

func projectV4Series(samples *[]Event, raw json.RawMessage) ([]WorkoutBundleSeries, error) {
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
		var wire v4Series
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
			start, end, value, ok := v4Triple(rawPoint)
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

func projectV4Routes(bundle *WorkoutBundle, table *BundleSourceTable, raw json.RawMessage, parent *HealthKitSource) ([]WorkoutBundleRoute, error) {
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
		var wire v4Route
		if json.Unmarshal(rawRoute, &wire) != nil || wire.RouteID == nil || !ValidID(*wire.RouteID) || seen[*wire.RouteID] {
			continue
		}
		source, ok := v4HealthSource(wire.Source)
		if !ok || !v4Eligible(source, parent) {
			continue
		}
		rawPoints += len(wire.Points)
		if rawPoints > MaxBundleRoutePoints {
			return nil, Fail(413, "bundle_too_large")
		}
		points := make([]WorkoutBundlePoint, 0, len(wire.Points))
		for _, rawPoint := range wire.Points {
			point, ok := v4RoutePoint(rawPoint)
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

func v4Triple(raw json.RawMessage) (start, end int64, value float64, ok bool) {
	items, ok := v4Array(raw)
	if !ok || len(items) != 3 {
		return 0, 0, 0, false
	}
	var startValue, endValue *int64
	var floatValue *float64
	if json.Unmarshal(items[0], &startValue) != nil || json.Unmarshal(items[1], &endValue) != nil ||
		json.Unmarshal(items[2], &floatValue) != nil || startValue == nil || endValue == nil || floatValue == nil {
		return 0, 0, 0, false
	}
	if *startValue > *endValue || *startValue < minBundleTimeUs || *endValue > maxBundleTimeUs ||
		math.IsNaN(*floatValue) || math.IsInf(*floatValue, 0) {
		return 0, 0, 0, false
	}
	return *startValue, *endValue, *floatValue, true
}

func v4RoutePoint(raw json.RawMessage) (WorkoutBundlePoint, bool) {
	items, ok := v4Array(raw)
	if !ok || len(items) < 3 || len(items) > 8 {
		return WorkoutBundlePoint{}, false
	}
	var recordedAt *int64
	var latitude, longitude *float64
	if json.Unmarshal(items[0], &recordedAt) != nil || json.Unmarshal(items[1], &latitude) != nil ||
		json.Unmarshal(items[2], &longitude) != nil || recordedAt == nil || latitude == nil || longitude == nil {
		return WorkoutBundlePoint{}, false
	}
	point := WorkoutBundlePoint{RecordedAtUs: *recordedAt, Latitude: *latitude, Longitude: *longitude}
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

// decodeV4Workout keeps the Workout core strict and drops unusable optional
// details. One unknown statistic or malformed segment never discards the run.
func decodeV4Workout(raw json.RawMessage, start, end int64) (Workout, error) {
	bad := func() (Workout, error) { return Workout{}, Fail(422, "unsupported_payload") }
	fields, ok := v4Object(raw)
	if !ok {
		return bad()
	}
	var activityType, duration *int64
	if json.Unmarshal(fields["activityType"], &activityType) != nil || activityType == nil {
		return bad()
	}
	if json.Unmarshal(fields["durationUs"], &duration) != nil || duration == nil {
		return bad()
	}
	workout := Workout{ActivityType: activityType, DurationUs: duration}
	if quantity, ok := v4Quantity(fields["totalDistance"], "m", true); ok {
		workout.TotalDistance = quantity
	}
	if quantity, ok := v4Quantity(fields["totalEnergyBurned"], "kcal", true); ok {
		workout.TotalEnergyBurned = quantity
	}
	workout.Statistics = v4Statistics(fields["statistics"], start, end)
	workout.Events = v4Events(fields["events"], start, end)
	workout.Activities = v4Activities(fields["activities"], start, end)
	if err := workout.Validate(start, end); err != nil {
		return Workout{}, err
	}
	return workout, nil
}

func v4Quantity(raw json.RawMessage, unit string, nonnegative bool) (*Quantity, bool) {
	if len(raw) == 0 || isJSONNull(raw) {
		return nil, false
	}
	var quantity Quantity
	if json.Unmarshal(raw, &quantity) != nil || !validQuantity(&quantity, unit, nonnegative) {
		return nil, false
	}
	return &quantity, true
}

func v4Statistics(raw json.RawMessage, start, end int64) []WorkoutStatistic {
	items, ok := v4Array(raw)
	if !ok {
		return nil
	}
	seen := make(map[string]bool, len(items))
	out := make([]WorkoutStatistic, 0, len(items))
	for _, item := range items {
		var statistic WorkoutStatistic
		if json.Unmarshal(item, &statistic) != nil {
			continue
		}
		if _, known := Units[statistic.Type]; !known || statistic.Type == WorkoutType || seen[statistic.Type] {
			continue
		}
		if validateWorkoutStatistics([]WorkoutStatistic{statistic}, start, end) != nil {
			continue
		}
		seen[statistic.Type] = true
		out = append(out, statistic)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

func v4Events(raw json.RawMessage, start, end int64) []WorkoutEvent {
	items, ok := v4Array(raw)
	if !ok {
		return nil
	}
	out := make([]WorkoutEvent, 0, len(items))
	for _, item := range items {
		var event WorkoutEvent
		if json.Unmarshal(item, &event) != nil {
			continue
		}
		if validateWorkoutEvents([]WorkoutEvent{event}, start, end) != nil {
			continue
		}
		out = append(out, event)
	}
	return out
}

func v4Activities(raw json.RawMessage, start, end int64) []WorkoutActivity {
	items, ok := v4Array(raw)
	if !ok {
		return nil
	}
	seen := make(map[string]bool, len(items))
	out := make([]WorkoutActivity, 0, len(items))
	for _, item := range items {
		var activity WorkoutActivity
		if json.Unmarshal(item, &activity) != nil || seen[activity.UUID] || activity.Validate(start, end) != nil {
			continue
		}
		seen[activity.UUID] = true
		out = append(out, activity)
	}
	return out
}
