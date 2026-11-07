package syncing

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"sort"
	"strings"
	"unicode/utf8"
)

// The v4 bundle is the compact columnar projection of one Workout. It carries
// the same facts as a v3 bundle but never repeats field names per sample:
// streams hold positional [startUs,endUs,value] triples with a parallel UUID
// array and a source index. It is decoded once into the v3 in-memory shape so
// the existing single semantic validation and PostgreSQL write path stay
// authoritative.
const (
	MaxV4Sources        = 16
	MaxV4Streams        = 32
	MaxV4Deletions      = 10_000
	MaxV4RouteDeletions = 256
)

var errBundleV4 = errors.New("invalid v4 bundle")

// v4String, v4Int and v4Int64 reject explicit nulls; the compact format uses
// omission for absent optional values, so a null is always a contract error.
type v4String string

func (s *v4String) UnmarshalJSON(raw []byte) error {
	if isJSONNull(raw) {
		return errBundleV4
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return errBundleV4
	}
	*s = v4String(value)
	return nil
}

type v4Int int

func (n *v4Int) UnmarshalJSON(raw []byte) error {
	if isJSONNull(raw) {
		return errBundleV4
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		return errBundleV4
	}
	*n = v4Int(value)
	return nil
}

type v4Int64 int64

func (n *v4Int64) UnmarshalJSON(raw []byte) error {
	if isJSONNull(raw) {
		return errBundleV4
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return errBundleV4
	}
	*n = v4Int64(value)
	return nil
}

type v4Float float64

func (n *v4Float) UnmarshalJSON(raw []byte) error {
	if isJSONNull(raw) {
		return errBundleV4
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil {
		return errBundleV4
	}
	*n = v4Float(value)
	return nil
}

func isJSONNull(raw []byte) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

type workoutBundleV4 struct {
	ProtocolVersion         int                 `json:"protocolVersion"`
	MetadataRegistryVersion *int                `json:"metadataRegistryVersion"`
	DatasetID               v4String            `json:"datasetID"`
	DatasetEpoch            v4String            `json:"datasetEpoch"`
	CollectorEpoch          v4String            `json:"collectorEpoch"`
	WorkoutID               v4String            `json:"workoutID"`
	StartUs                 v4Int64             `json:"startUs"`
	EndUs                   v4Int64             `json:"endUs"`
	Workout                 json.RawMessage     `json:"workout"`
	Sources                 []workoutSourceV4   `json:"sources"`
	Streams                 []workoutStreamV4   `json:"streams"`
	Series                  []workoutSeriesV4   `json:"series"`
	Routes                  []workoutRouteV4    `json:"routes"`
	Deletions               []workoutDeletionV4 `json:"deletions"`
	RouteDeletions          []v4String          `json:"routeDeletions"`
	Observation             json.RawMessage     `json:"observation"`
}

type workoutSourceV4 struct {
	BundleID               v4String `json:"bundleID"`
	Name                   v4String `json:"name"`
	Version                v4String `json:"version"`
	ProductType            v4String `json:"productType"`
	OperatingSystemVersion v4String `json:"operatingSystemVersion"`
}

func (s *workoutSourceV4) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return errBundleV4
	}
	required := []string{"bundleID"}
	for _, key := range required {
		if value, ok := fields[key]; !ok || isJSONNull(value) {
			return errBundleV4
		}
	}
	for key, value := range fields {
		switch key {
		case "bundleID", "name", "version", "productType", "operatingSystemVersion":
			if isJSONNull(value) {
				return errBundleV4
			}
		default:
			return errBundleV4
		}
	}
	type alias workoutSourceV4
	var out alias
	if err := json.Unmarshal(raw, &out); err != nil {
		return errBundleV4
	}
	*s = workoutSourceV4(out)
	return nil
}

func (s workoutSourceV4) healthKitSource() HealthKitSource {
	return HealthKitSource{
		BundleID:               string(s.BundleID),
		Name:                   string(s.Name),
		Version:                string(s.Version),
		ProductType:            string(s.ProductType),
		OperatingSystemVersion: string(s.OperatingSystemVersion),
	}
}

type workoutStreamV4 struct {
	Type     v4String          `json:"type"`
	Kind     v4String          `json:"kind"`
	Unit     v4String          `json:"unit"`
	Samples  []workoutSampleV4 `json:"samples"`
	UUIDs    []v4String        `json:"uuids"`
	Source   v4Int             `json:"source"`
	Src      []v4Int           `json:"src"`
	Metadata []v4Metadata      `json:"metadata"`
}

func (s *workoutStreamV4) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return errBundleV4
	}
	for _, key := range []string{"type", "kind", "unit", "samples", "uuids", "source"} {
		if value, ok := fields[key]; !ok || isJSONNull(value) {
			return errBundleV4
		}
	}
	for key, value := range fields {
		switch key {
		case "type", "kind", "unit", "samples", "uuids", "source", "src", "metadata":
			if isJSONNull(value) {
				return errBundleV4
			}
		default:
			return errBundleV4
		}
	}
	type alias workoutStreamV4
	var out alias
	if err := json.Unmarshal(raw, &out); err != nil {
		return errBundleV4
	}
	*s = workoutStreamV4(out)
	return nil
}

// workoutSampleV4 is one compact sample triple. Every element is required and
// must be a finite number; category values are integral and checked later.
type workoutSampleV4 struct {
	StartUs int64
	EndUs   int64
	Value   float64
}

func (s *workoutSampleV4) UnmarshalJSON(raw []byte) error {
	values, err := v4FixedArray(raw, 3)
	if err != nil {
		return err
	}
	start, err := v4Int64Value(values[0])
	if err != nil {
		return err
	}
	end, err := v4Int64Value(values[1])
	if err != nil {
		return err
	}
	value, err := v4FloatValue(values[2])
	if err != nil {
		return err
	}
	s.StartUs, s.EndUs, s.Value = start, end, value
	return nil
}

type v4Metadata map[string]any

func (m *v4Metadata) UnmarshalJSON(raw []byte) error {
	if isJSONNull(raw) {
		return errBundleV4
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return errBundleV4
	}
	var value map[string]any
	if err := decodeV4Object(raw, &value); err != nil {
		return err
	}
	*m = value
	return nil
}

type workoutSeriesV4 struct {
	Stream v4Int            `json:"stream"`
	Sample v4Int            `json:"sample"`
	Points []workoutPointV4 `json:"points"`
}

type workoutPointV4 struct {
	StartUs int64
	EndUs   int64
	Value   float64
}

func (p *workoutPointV4) UnmarshalJSON(raw []byte) error {
	values, err := v4FixedArray(raw, 3)
	if err != nil {
		return err
	}
	start, err := v4Int64Value(values[0])
	if err != nil {
		return err
	}
	end, err := v4Int64Value(values[1])
	if err != nil {
		return err
	}
	value, err := v4FloatValue(values[2])
	if err != nil {
		return err
	}
	p.StartUs, p.EndUs, p.Value = start, end, value
	return nil
}

type workoutRouteV4 struct {
	RouteID v4String              `json:"routeID"`
	Source  v4Int                 `json:"source"`
	Points  []workoutRoutePointV4 `json:"points"`
}

type workoutRoutePointV4 struct {
	RecordedAtUs       int64
	Latitude           float64
	Longitude          float64
	Altitude           *float64
	HorizontalAccuracy *float64
	VerticalAccuracy   *float64
	Speed              *float64
	Course             *float64
}

func (p *workoutRoutePointV4) UnmarshalJSON(raw []byte) error {
	values, err := v4FixedArray(raw, 8)
	if err != nil {
		return err
	}
	recordedAt, err := v4Int64Value(values[0])
	if err != nil {
		return err
	}
	latitude, err := v4FloatValue(values[1])
	if err != nil {
		return err
	}
	longitude, err := v4FloatValue(values[2])
	if err != nil {
		return err
	}
	optional := make([]*float64, 5)
	for i := range optional {
		if isJSONNull(values[3+i]) {
			continue
		}
		value, err := v4FloatValue(values[3+i])
		if err != nil {
			return err
		}
		v := value
		optional[i] = &v
	}
	*p = workoutRoutePointV4{RecordedAtUs: recordedAt, Latitude: latitude, Longitude: longitude,
		Altitude: optional[0], HorizontalAccuracy: optional[1], VerticalAccuracy: optional[2], Speed: optional[3], Course: optional[4]}
	return nil
}

func (p workoutRoutePointV4) bundlePoint() WorkoutBundlePoint {
	return WorkoutBundlePoint{RecordedAtUs: p.RecordedAtUs, Latitude: p.Latitude, Longitude: p.Longitude,
		Altitude: p.Altitude, HorizontalAccuracy: p.HorizontalAccuracy, VerticalAccuracy: p.VerticalAccuracy, Speed: p.Speed, Course: p.Course}
}

type workoutDeletionV4 struct {
	Type v4String `json:"type"`
	UUID v4String `json:"uuid"`
}

// v4FixedArray decodes exactly n positional elements. encoding/json arrays
// silently tolerate shorter and longer inputs when decoding into fixed arrays,
// so the compact contract checks the count explicitly.
func v4FixedArray(raw []byte, n int) ([]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil {
		return nil, errBundleV4
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '[' {
		return nil, errBundleV4
	}
	values := make([]json.RawMessage, 0, n)
	for decoder.More() {
		if len(values) >= n {
			return nil, errBundleV4
		}
		var item json.RawMessage
		if err := decoder.Decode(&item); err != nil {
			return nil, errBundleV4
		}
		values = append(values, item)
	}
	if token, err = decoder.Token(); err != nil {
		return nil, errBundleV4
	}
	if delim, ok = token.(json.Delim); !ok || delim != ']' || len(values) != n {
		return nil, errBundleV4
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errBundleV4
	}
	return values, nil
}

func v4Int64Value(raw json.RawMessage) (int64, error) {
	if isJSONNull(raw) {
		return 0, errBundleV4
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, errBundleV4
	}
	return value, nil
}

func v4FloatValue(raw json.RawMessage) (float64, error) {
	if isJSONNull(raw) {
		return 0, errBundleV4
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, errBundleV4
	}
	return value, nil
}

// DecodeWorkoutBundleV4JSON is the one wire decoder for protocolVersion 4. It
// rejects duplicate object keys, unknown fields, explicit nulls and malformed
// fixed arrays before converting into the v3 in-memory bundle used by the
// single semantic validation and import path.
func DecodeWorkoutBundleV4JSON(raw []byte) (WorkoutBundle, error) {
	bad := func() (WorkoutBundle, error) { return WorkoutBundle{}, Fail(422, "unsupported_payload") }
	if !utf8.Valid(raw) {
		return bad()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := walkJSON(decoder); err != nil {
		return bad()
	}
	if _, err := decoder.Token(); err != io.EOF {
		return bad()
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope == nil {
		return bad()
	}
	for _, key := range []string{"protocolVersion", "metadataRegistryVersion", "datasetID", "datasetEpoch",
		"collectorEpoch", "workoutID", "startUs", "endUs", "workout", "sources", "streams", "observation"} {
		value, ok := envelope[key]
		if !ok || isJSONNull(value) {
			return bad()
		}
	}
	for _, key := range []string{"series", "routes", "deletions", "routeDeletions"} {
		if value, ok := envelope[key]; ok && isJSONNull(value) {
			return bad()
		}
	}
	var wire workoutBundleV4
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.UseNumber()
	strict.DisallowUnknownFields()
	if err := strict.Decode(&wire); err != nil {
		return bad()
	}
	if _, err := strict.Token(); err != io.EOF {
		return bad()
	}
	if wire.ProtocolVersion != 4 {
		return WorkoutBundle{}, bundleField("protocolVersion", "unsupported_protocol")
	}
	bundle, err := wire.toBundle()
	if err != nil {
		return WorkoutBundle{}, err
	}
	return bundle, nil
}

func (w workoutBundleV4) toBundle() (WorkoutBundle, error) {
	bad := func() (WorkoutBundle, error) { return WorkoutBundle{}, Fail(422, "unsupported_payload") }
	if len(w.Sources) == 0 || len(w.Sources) > MaxV4Sources || len(w.Streams) > MaxV4Streams ||
		len(w.Series) > MaxBundleSeries || len(w.Routes) > MaxBundleRoutes ||
		len(w.Deletions) > MaxV4Deletions || len(w.RouteDeletions) > MaxV4RouteDeletions {
		if len(w.Series) > MaxBundleSeries || len(w.Routes) > MaxBundleRoutes {
			return WorkoutBundle{}, Fail(413, "bundle_too_large")
		}
		return bad()
	}
	start, end := int64(w.StartUs), int64(w.EndUs)
	if start < -62135596800000000 || start > 253402300799999999 || end < start || end > 253402300799999999 {
		return bad()
	}
	var workout Workout
	if err := decodeV4Object(w.Workout, &workout); err != nil {
		return bad()
	}
	if err := workout.Validate(start, end); err != nil {
		return WorkoutBundle{}, bundleValidationContext(err, "workout", "invalid_workout")
	}
	observation, err := decodeV4Observation(w.Observation)
	if err != nil {
		return bad()
	}
	sources := make([]HealthKitSource, len(w.Sources))
	for i := range w.Sources {
		sources[i] = w.Sources[i].healthKitSource()
		if err := sources[i].Validate(); err != nil {
			return bad()
		}
	}
	workoutEvent := Event{
		Op: "upsert", Kind: "workout", Type: WorkoutType, UUID: string(w.WorkoutID),
		StartUs: &start, EndUs: &end,
		Source:  &Source{BundleID: observation.Provenance.Workout.BundleID},
		Workout: &workout,
	}
	bundle := WorkoutBundle{
		ProtocolVersion:         4,
		MetadataRegistryVersion: w.MetadataRegistryVersion,
		DatasetID:               string(w.DatasetID),
		DatasetEpoch:            string(w.DatasetEpoch),
		CollectorEpoch:          string(w.CollectorEpoch),
		WorkoutID:               string(w.WorkoutID),
		Workout:                 workoutEvent,
		Observation:             &observation,
	}
	seenSamples := make(map[string]bool)
	for streamIndex := range w.Streams {
		stream := &w.Streams[streamIndex]
		sampleType := string(stream.Type)
		unit, ok := Units[sampleType]
		if !ok || sampleType == WorkoutType {
			return bad()
		}
		kind := "quantity"
		if unit == "" {
			kind = "category"
		}
		if string(stream.Kind) != kind || string(stream.Unit) != unit ||
			len(stream.Samples) > MaxWorkoutBundleEvents || len(stream.UUIDs) != len(stream.Samples) {
			return bad()
		}
		if len(bundle.Samples)+len(stream.Samples) > MaxWorkoutBundleEvents {
			return WorkoutBundle{}, Fail(413, "bundle_too_large")
		}
		streamSource := int(stream.Source)
		if streamSource < 0 || streamSource >= len(sources) {
			return bad()
		}
		if stream.Src != nil && len(stream.Src) != len(stream.Samples) {
			return bad()
		}
		if stream.Metadata != nil && len(stream.Metadata) != len(stream.Samples) {
			return bad()
		}
		for sampleIndex := range stream.Samples {
			sample := stream.Samples[sampleIndex]
			uuid := string(stream.UUIDs[sampleIndex])
			if !ValidID(uuid) {
				return bad()
			}
			key := sampleType + ":" + uuid
			if seenSamples[key] {
				return bad()
			}
			seenSamples[key] = true
			sourceIndex := streamSource
			if stream.Src != nil {
				sourceIndex = int(stream.Src[sampleIndex])
				if sourceIndex < 0 || sourceIndex >= len(sources) {
					return bad()
				}
			}
			if sample.StartUs > sample.EndUs || sample.StartUs < -62135596800000000 || sample.EndUs > 253402300799999999 {
				return bad()
			}
			event := Event{Op: "upsert", Kind: "sample", Type: sampleType, UUID: uuid,
				StartUs: &sample.StartUs, EndUs: &sample.EndUs,
				Source: &Source{BundleID: sources[sourceIndex].BundleID}}
			if kind == "category" {
				if sample.Value != math.Trunc(sample.Value) || sample.Value < 0 || sample.Value > 5 {
					return bad()
				}
				category := int(sample.Value)
				event.Category = &category
			} else {
				event.Quantity = &Quantity{Value: sample.Value, Unit: unit}
			}
			if stream.Metadata != nil && len(stream.Metadata[sampleIndex]) > 0 {
				event.Metadata = stream.Metadata[sampleIndex]
			}
			bundle.Samples = append(bundle.Samples, event)
		}
	}
	seenSeries := make(map[[2]int]bool)
	seriesPoints := 0
	for _, series := range w.Series {
		streamIndex, sampleIndex := int(series.Stream), int(series.Sample)
		if streamIndex < 0 || streamIndex >= len(w.Streams) || sampleIndex < 0 || sampleIndex >= len(w.Streams[streamIndex].Samples) || len(series.Points) == 0 {
			return bad()
		}
		key := [2]int{streamIndex, sampleIndex}
		if seenSeries[key] {
			return bad()
		}
		seenSeries[key] = true
		sampleType := string(w.Streams[streamIndex].Type)
		unit := Units[sampleType]
		if unit == "" {
			return bad()
		}
		parent := w.Streams[streamIndex].Samples[sampleIndex]
		seriesPoints += len(series.Points)
		if seriesPoints > MaxBundleSeriesPoints {
			return WorkoutBundle{}, Fail(413, "bundle_too_large")
		}
		points := make([]SeriesPoint, len(series.Points))
		for i, point := range series.Points {
			if point.StartUs > point.EndUs || point.StartUs < parent.StartUs || point.EndUs > parent.EndUs {
				return bad()
			}
			points[i] = SeriesPoint{StartUs: point.StartUs, EndUs: point.EndUs, Value: point.Value, Unit: unit}
		}
		bundle.Series = append(bundle.Series, WorkoutBundleSeries{
			SampleType: sampleType, SampleUUID: string(w.Streams[streamIndex].UUIDs[sampleIndex]), Points: points,
		})
	}
	routeIDs := make(map[string]bool)
	routePoints := 0
	for _, route := range w.Routes {
		routeID := string(route.RouteID)
		sourceIndex := int(route.Source)
		if !ValidID(routeID) || routeIDs[routeID] || sourceIndex < 0 || sourceIndex >= len(sources) || len(route.Points) == 0 {
			return bad()
		}
		routeIDs[routeID] = true
		routePoints += len(route.Points)
		if routePoints > MaxBundleRoutePoints {
			return WorkoutBundle{}, Fail(413, "bundle_too_large")
		}
		points := make([]WorkoutBundlePoint, len(route.Points))
		for i := range route.Points {
			points[i] = route.Points[i].bundlePoint()
			if err := points[i].ToRoutePoint(int64(i)).Validate(); err != nil {
				return bad()
			}
		}
		bundle.Routes = append(bundle.Routes, WorkoutBundleRoute{RouteID: routeID, Source: Source{BundleID: sources[sourceIndex].BundleID}, Points: points})
	}
	for _, deletion := range w.Deletions {
		sampleType := string(deletion.Type)
		uuid := string(deletion.UUID)
		if _, ok := Units[sampleType]; !ok || sampleType == WorkoutType || !ValidID(uuid) {
			return bad()
		}
		bundle.SampleDeletions = append(bundle.SampleDeletions, SampleDeletion{SampleType: sampleType, SampleUUID: uuid})
	}
	for _, deletion := range w.RouteDeletions {
		routeID := string(deletion)
		if !ValidID(routeID) {
			return bad()
		}
		bundle.RouteDeletions = append(bundle.RouteDeletions, routeID)
	}
	// CompleteSampleTypes is derived only from explicit per-stream coverage.
	// A successful query with zero records is coverage, never completeness for
	// a stream that has no samples.
	counts := map[string]int{}
	for _, sample := range bundle.Samples {
		counts[sample.Type]++
	}
	complete := make(map[string]bool)
	for key, coverage := range observation.Coverage {
		if coverage.State != "complete" {
			continue
		}
		sampleType := key
		if strings.HasPrefix(key, "series:") {
			sampleType = strings.TrimPrefix(key, "series:")
		}
		if _, ok := Units[sampleType]; !ok || sampleType == WorkoutType || counts[sampleType] == 0 {
			continue
		}
		complete[sampleType] = true
	}
	for sampleType := range complete {
		bundle.CompleteSampleTypes = append(bundle.CompleteSampleTypes, sampleType)
	}
	sort.Strings(bundle.CompleteSampleTypes)
	if coverage, ok := observation.Coverage["route"]; ok && coverage.State == "complete" && len(bundle.Routes) > 0 {
		bundle.CompleteRoutes = true
	}
	if err := bundle.Validate(); err != nil {
		return WorkoutBundle{}, err
	}
	return bundle, nil
}

func decodeV4Object(raw json.RawMessage, destination any) error {
	if len(raw) == 0 || isJSONNull(raw) {
		return errBundleV4
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errBundleV4
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errBundleV4
	}
	return nil
}

// decodeV4Observation keeps the observation envelope strict: provenance must
// contain the workout descriptor plus explicitly empty sample/route arrays,
// because v4 carries per-sample source indices in streams instead.
func decodeV4Observation(raw json.RawMessage) (BundleObservation, error) {
	var zero BundleObservation
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return zero, errBundleV4
	}
	for _, key := range []string{"version", "observedAtUs", "provenance", "coverage"} {
		value, ok := fields[key]
		if !ok || isJSONNull(value) {
			return zero, errBundleV4
		}
	}
	if value, ok := fields["metadata"]; ok && isJSONNull(value) {
		return zero, errBundleV4
	}
	var observation BundleObservation
	if err := decodeV4Object(raw, &observation); err != nil {
		return zero, errBundleV4
	}
	var provenance map[string]json.RawMessage
	if err := json.Unmarshal(fields["provenance"], &provenance); err != nil || provenance == nil {
		return zero, errBundleV4
	}
	for _, key := range []string{"workout", "samples", "routes"} {
		value, ok := provenance[key]
		if !ok || isJSONNull(value) {
			return zero, errBundleV4
		}
	}
	for _, key := range []string{"samples", "routes"} {
		var empty []json.RawMessage
		if err := json.Unmarshal(provenance[key], &empty); err != nil || len(empty) != 0 {
			return zero, errBundleV4
		}
	}
	var coverage map[string]json.RawMessage
	if err := json.Unmarshal(fields["coverage"], &coverage); err != nil || coverage == nil {
		return zero, errBundleV4
	}
	for _, value := range coverage {
		if isJSONNull(value) {
			return zero, errBundleV4
		}
		var item map[string]json.RawMessage
		if err := json.Unmarshal(value, &item); err != nil || item == nil {
			return zero, errBundleV4
		}
		for _, required := range []string{"state", "sampleCount", "queryCompleted", "localPersisted"} {
			if field, ok := item[required]; !ok || isJSONNull(field) {
				return zero, errBundleV4
			}
		}
	}
	return observation, nil
}
