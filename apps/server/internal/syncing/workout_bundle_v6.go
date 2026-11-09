package syncing

import (
	"bytes"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"unicode/utf8"
)

// Protocol 6 is the RQrun-style raw stream encoding. One Workout carries one
// top-level source that every projected fact inherits, per-type scalar triples
// and series groups without per-sample identity, and routes without route IDs.
// The server owns fact identity: a deterministic UUIDv5 over the Workout UUID
// and the wire array index makes an exact-byte retry idempotent.
const (
	// V6IdentityNamespace is fixed forever. Changing it would give the same
	// accepted payload a second set of fact identities on replay.
	V6IdentityNamespace = "c9f3e2a1-7b64-5d8e-9a0b-1c2d3e4f5a6b"
)

var v6Namespace = [16]byte{0xc9, 0xf3, 0xe2, 0xa1, 0x7b, 0x64, 0x5d, 0x8e,
	0x9a, 0x0b, 0x1c, 0x2d, 0x3e, 0x4f, 0x5a, 0x6b}

var errBundleV6 = Fail(422, "unsupported_payload")

type v6Envelope struct {
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
	Routes          json.RawMessage `json:"routes"`
}

type v6Stream struct {
	Type    *string           `json:"type"`
	Unit    *string           `json:"unit"`
	Status  json.RawMessage   `json:"status"`
	Error   json.RawMessage   `json:"error"`
	Samples []json.RawMessage `json:"samples"`
	Series  []json.RawMessage `json:"series"`
}

type v6SeriesGroup struct {
	StartDeltaUs *int64            `json:"startDeltaUs"`
	EndDeltaUs   *int64            `json:"endDeltaUs"`
	Value        *float64          `json:"value"`
	Points       []json.RawMessage `json:"points"`
}

type v6Route struct {
	Points []json.RawMessage `json:"points"`
}

func decodeV6Envelope(raw []byte) (v6Envelope, error) {
	var zero v6Envelope
	if !utf8.Valid(raw) {
		return zero, errBundleV6
	}
	lexical := json.NewDecoder(bytes.NewReader(raw))
	if err := walkV4JSON(lexical, 0); err != nil {
		return zero, errBundleV6
	}
	if _, err := lexical.Token(); err != io.EOF {
		return zero, errBundleV6
	}
	var wire v6Envelope
	if err := json.Unmarshal(raw, &wire); err != nil {
		return zero, errBundleV6
	}
	if wire.ProtocolVersion == nil || *wire.ProtocolVersion != 6 || wire.DatasetID == nil ||
		wire.DatasetEpoch == nil || wire.CollectorEpoch == nil || wire.WorkoutID == nil ||
		wire.StartUs == nil || wire.EndUs == nil || len(wire.Source) == 0 || isJSONNull(wire.Source) ||
		len(wire.Workout) == 0 || isJSONNull(wire.Workout) {
		return zero, errBundleV6
	}
	if !ValidID(*wire.DatasetID) || !ValidID(*wire.DatasetEpoch) ||
		!ValidID(*wire.CollectorEpoch) || !ValidID(*wire.WorkoutID) {
		return zero, errBundleV6
	}
	if !validV4Range(*wire.StartUs, *wire.EndUs) {
		return zero, errBundleV6
	}
	return wire, nil
}

// V6FactUUID derives the server-owned identity of one projected fact. The name
// format is part of the protocol: dataset, Workout, sample type, fact kind and
// the wire array index (including entries the projection skipped). It never
// uses randomness, time, sequences or global counters.
func V6FactUUID(datasetID, workoutID, sampleType, kind string, ordinal int) string {
	name := fmt.Sprintf("rai/v1|%s|%s|%s|%s|%d", datasetID, workoutID, sampleType, kind, ordinal)
	if kind == "route" {
		name = fmt.Sprintf("rai/v1|%s|%s|route|%d", datasetID, workoutID, ordinal)
	}
	h := sha1.New()
	_, _ = h.Write(v6Namespace[:])
	_, _ = h.Write([]byte(name))
	sum := h.Sum(nil)
	var id [16]byte
	copy(id[:], sum[:16])
	id[6] = (id[6] & 0x0f) | 0x50
	id[8] = (id[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
}

func v6Triple(raw json.RawMessage, base int64) (start, end int64, value float64, ok bool) {
	return v5Triple(raw, base)
}

func v6RoutePoint(raw json.RawMessage, base int64) (WorkoutBundlePoint, bool) {
	return v5RoutePoint(raw, base)
}

func v6SeriesHeader(raw json.RawMessage, base int64) (start, end int64, value float64, ok bool) {
	var wire v6SeriesGroup
	if json.Unmarshal(raw, &wire) != nil || wire.StartDeltaUs == nil || wire.EndDeltaUs == nil ||
		wire.Value == nil || math.IsNaN(*wire.Value) || math.IsInf(*wire.Value, 0) {
		return 0, 0, 0, false
	}
	start, ok = addBundleBase(base, *wire.StartDeltaUs)
	if !ok {
		return 0, 0, 0, false
	}
	end, ok = addBundleBase(base, *wire.EndDeltaUs)
	if !ok || start > end {
		return 0, 0, 0, false
	}
	return start, end, *wire.Value, true
}

func v6SeriesPointCounts(stream v6Stream) int {
	total := 0
	for _, raw := range stream.Series {
		var group v6SeriesGroup
		if json.Unmarshal(raw, &group) == nil {
			total += len(group.Points)
		}
	}
	return total
}

// projectV6Streams projects scalar triples and series groups. Every original
// entry occupies an ordinal even when it is skipped, so identities never shift
// when one bad point is ignored.
func projectV6Streams(bundle *WorkoutBundle, table *BundleSourceTable, raw json.RawMessage,
	source HealthKitSource, base int64) error {
	streams, ok := v4Array(raw)
	if !ok {
		return nil
	}
	seenTypes := make(map[string]bool, len(streams))
	rawEntries, rawSeriesPoints := 0, 0
	for _, rawStream := range streams {
		var stream v6Stream
		if json.Unmarshal(rawStream, &stream) != nil {
			continue
		}
		rawEntries += len(stream.Samples) + len(stream.Series)
		if rawEntries > MaxWorkoutBundleEvents {
			return Fail(413, "bundle_too_large")
		}
		rawSeriesPoints += v6SeriesPointCounts(stream)
		if rawSeriesPoints > MaxBundleSeriesPoints {
			return Fail(413, "bundle_too_large")
		}
		if stream.Type == nil {
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
		for index, rawSample := range stream.Samples {
			start, end, value, ok := v6Triple(rawSample, base)
			if !ok {
				continue
			}
			projectBundleSample(bundle, table, sampleType, unit, bundleSampleValue{
				uuid:  V6FactUUID(bundle.DatasetID, bundle.WorkoutID, sampleType, "sample", index),
				start: start, end: end, value: value, source: source,
			}, &source)
		}
		if unit == "" {
			continue
		}
		for index, rawGroup := range stream.Series {
			start, end, value, ok := v6SeriesHeader(rawGroup, base)
			if !ok {
				continue
			}
			var group v6SeriesGroup
			if json.Unmarshal(rawGroup, &group) != nil {
				continue
			}
			points := make([]SeriesPoint, 0, len(group.Points))
			for _, rawPoint := range group.Points {
				pointStart, pointEnd, pointValue, ok := v6Triple(rawPoint, base)
				if !ok || pointStart < start || pointEnd > end {
					continue
				}
				points = append(points, SeriesPoint{StartUs: pointStart, EndUs: pointEnd, Value: pointValue, Unit: unit})
			}
			if len(points) == 0 {
				continue
			}
			parentUUID := V6FactUUID(bundle.DatasetID, bundle.WorkoutID, sampleType, "series", index)
			if !projectBundleSample(bundle, table, sampleType, unit, bundleSampleValue{
				uuid: parentUUID, start: start, end: end, value: value, source: source,
			}, &source) {
				continue
			}
			bundle.Series = append(bundle.Series, WorkoutBundleSeries{
				SampleType: sampleType, SampleUUID: parentUUID, Points: points,
			})
		}
	}
	return nil
}

// projectV6Routes projects routes by wire array index. The client sends no
// route ID and no source: both are server-owned.
func projectV6Routes(bundle *WorkoutBundle, table *BundleSourceTable, raw json.RawMessage,
	source HealthKitSource, base int64) error {
	items, ok := v4Array(raw)
	if !ok {
		return nil
	}
	if len(items) > MaxBundleRoutes {
		return Fail(413, "bundle_too_large")
	}
	rawPoints := 0
	for index, rawRoute := range items {
		var route v6Route
		if json.Unmarshal(rawRoute, &route) != nil {
			continue
		}
		rawPoints += len(route.Points)
		if rawPoints > MaxBundleRoutePoints {
			return Fail(413, "bundle_too_large")
		}
		points := make([]WorkoutBundlePoint, 0, len(route.Points))
		for _, rawPoint := range route.Points {
			point, ok := v6RoutePoint(rawPoint, base)
			if !ok {
				continue
			}
			points = append(points, point)
		}
		if len(points) == 0 {
			continue
		}
		routeID := V6FactUUID(bundle.DatasetID, bundle.WorkoutID, "", "route", index)
		if _, duplicate := table.Routes[routeID]; duplicate {
			continue
		}
		table.Routes[routeID] = source
		bundle.Routes = append(bundle.Routes, WorkoutBundleRoute{
			RouteID: routeID, Source: Source{BundleID: source.BundleID}, Points: points,
		})
	}
	return nil
}

// DecodeWorkoutBundleV6JSON is the wire boundary for protocolVersion 6. The
// envelope, Workout core and source policy are strict; every optional sample,
// series group, point and route is projected independently so one bad entry
// never discards the rest of the run. The original bytes remain authoritative
// outside this projection.
func DecodeWorkoutBundleV6JSON(raw []byte) (WorkoutBundle, error) {
	wire, err := decodeV6Envelope(raw)
	if err != nil {
		return WorkoutBundle{}, errBundleV6
	}
	workoutSource, ok := v4HealthSource(wire.Source)
	if !ok {
		return WorkoutBundle{}, errBundleV6
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
		ProtocolVersion: 6,
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
	if err := projectV6Streams(&bundle, table, wire.Streams, workoutSource, start); err != nil {
		return WorkoutBundle{}, err
	}
	if err := projectV6Routes(&bundle, table, wire.Routes, workoutSource, start); err != nil {
		return WorkoutBundle{}, err
	}
	bundle.SourceTable = table
	if err := bundle.Validate(); err != nil {
		return WorkoutBundle{}, err
	}
	return bundle, nil
}

// WorkoutBundleV6SourceTable rebuilds the top-level source attribution from an
// already stored protocol-6 payload. Every projected fact inherits the same
// source, so the table only needs the identities the projection could create.
func WorkoutBundleV6SourceTable(raw []byte) (*BundleSourceTable, error) {
	wire, err := decodeV6Envelope(raw)
	if err != nil {
		return nil, errBundleV6
	}
	workoutSource, ok := v4HealthSource(wire.Source)
	if !ok {
		return nil, errBundleV6
	}
	table := &BundleSourceTable{
		Workout: workoutSource,
		Samples: make(map[string]HealthKitSource),
		Routes:  make(map[string]HealthKitSource),
	}
	streams, _ := v4Array(wire.Streams)
	for _, rawStream := range streams {
		var stream v6Stream
		if json.Unmarshal(rawStream, &stream) != nil || stream.Type == nil {
			continue
		}
		sampleType := *stream.Type
		if _, known := Units[sampleType]; !known || sampleType == WorkoutType {
			continue
		}
		for index := range stream.Samples {
			table.Samples[sampleType+":"+V6FactUUID(*wire.DatasetID, *wire.WorkoutID, sampleType, "sample", index)] = workoutSource
		}
		for index := range stream.Series {
			table.Samples[sampleType+":"+V6FactUUID(*wire.DatasetID, *wire.WorkoutID, sampleType, "series", index)] = workoutSource
		}
	}
	routes, _ := v4Array(wire.Routes)
	for index := range routes {
		table.Routes[V6FactUUID(*wire.DatasetID, *wire.WorkoutID, "", "route", index)] = workoutSource
	}
	return table, nil
}
