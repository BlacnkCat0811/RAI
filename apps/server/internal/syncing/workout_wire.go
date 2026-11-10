package syncing

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Raw Workout wire helpers shared by the protocol-6 projection. The original
// bytes stay authoritative; these helpers only turn known fields into facts.

const (
	maxRawJSONDepth = 128
	minBundleTimeUs = -62135596800000000
	maxBundleTimeUs = 253402300799999999
)

var errRawPayload = errors.New("invalid raw payload")

func isJSONNull(raw []byte) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

// walkRawJSON rejects duplicate object keys and pathological nesting before the
// typed decode. Unknown fields are intentionally legal in the raw protocol.
func walkRawJSON(decoder *json.Decoder, depth int) error {
	if depth > maxRawJSONDepth {
		return errRawPayload
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
				return errRawPayload
			}
			seen[key] = true
			if err := walkRawJSON(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := walkRawJSON(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return errRawPayload
	}
	_, err = decoder.Token()
	return err
}

func validTimeRange(start, end int64) bool {
	return start >= minBundleTimeUs && start <= maxBundleTimeUs && end >= start && end <= maxBundleTimeUs
}

func rawObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if len(raw) == 0 || isJSONNull(raw) {
		return nil, false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, false
	}
	return fields, true
}

func rawArray(raw json.RawMessage) ([]json.RawMessage, bool) {
	if len(raw) == 0 || isJSONNull(raw) {
		return nil, false
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, false
	}
	return items, true
}

// Metadata is an optional fact projection. Values that JSONB or the analysis
// number decoder cannot represent stay exclusively in the authoritative raw
// bytes, rather than preventing other known facts from being committed/read.
func rawMetadataValue(raw json.RawMessage) (map[string]any, bool) {
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
	return projectMetadata(object), true
}

func projectMetadata(object map[string]any) map[string]any {
	if object == nil {
		return nil
	}
	out := make(map[string]any, len(object))
	for key, value := range object {
		if !strings.ContainsRune(key, 0) {
			if projected, ok := projectMetadataValue(value); ok {
				out[key] = projected
			}
		}
	}
	return out
}

func projectMetadataValue(value any) (any, bool) {
	switch value := value.(type) {
	case string:
		return value, !strings.ContainsRune(value, 0)
	case json.Number:
		// Bound the optional read model's decimal representation as well as its
		// float value: e.g. 1e-1000000 underflows to zero without a Float64 error,
		// but its original decimal scale still cannot be stored in JSONB.
		literal := value.String()
		if len(literal) > 1024 {
			return nil, false
		}
		if i := strings.IndexAny(literal, "eE"); i >= 0 {
			exponent, err := strconv.Atoi(literal[i+1:])
			if err != nil || exponent < -1024 || exponent > 1024 {
				return nil, false
			}
		}
		number, err := value.Float64()
		return value, err == nil && !math.IsNaN(number) && !math.IsInf(number, 0)
	case float64:
		return value, !math.IsNaN(value) && !math.IsInf(value, 0)
	case map[string]any:
		return projectMetadata(value), true
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			// Preserve array positions even when an unsupported extension is omitted.
			if projected, ok := projectMetadataValue(item); ok {
				out[i] = projected
			}
		}
		return out, true
	default:
		return value, true
	}
}

func decodeRawDetail(raw json.RawMessage, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return decoder.Decode(value)
}

func rawHealthSource(raw json.RawMessage) (HealthKitSource, bool) {
	var source HealthKitSource
	if len(raw) == 0 || isJSONNull(raw) || json.Unmarshal(raw, &source) != nil || source.Validate() != nil {
		return HealthKitSource{}, false
	}
	return source, true
}

func eligibleSource(source HealthKitSource, parent *HealthKitSource) bool {
	class := source.Classification(parent)
	return class == "apple_watch" || class == "associated_apple_watch"
}

// bundleSampleValue is one decoded sample ready for projection. Duplicate
// suppression, source eligibility, value interpretation and metadata projection
// are identical for every stream shape.
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
	if !eligibleSource(sample.source, parent) {
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
		// A metadata table entry can be referenced by many samples. Each
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

// decodeWorkoutCore keeps the Workout core strict and drops unusable optional
// details. One unknown statistic or malformed segment never discards the run.
func decodeWorkoutCore(raw json.RawMessage, start, end int64, datasetID, workoutID string) (Workout, error) {
	bad := func() (Workout, error) { return Workout{}, Fail(422, "unsupported_payload") }
	fields, ok := rawObject(raw)
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
	if quantity, ok := decodeQuantity(fields["totalDistance"], "m", true); ok {
		workout.TotalDistance = quantity
	}
	if quantity, ok := decodeQuantity(fields["totalEnergyBurned"], "kcal", true); ok {
		workout.TotalEnergyBurned = quantity
	}
	workout.Statistics = decodeStatistics(fields["statistics"], start, end)
	workout.Events = decodeWorkoutEvents(fields["events"], start, end)
	workout.Activities = decodeWorkoutActivities(fields["activities"], start, end, datasetID, workoutID)
	if err := workout.Validate(start, end); err != nil {
		return Workout{}, err
	}
	return workout, nil
}

func decodeQuantity(raw json.RawMessage, unit string, nonnegative bool) (*Quantity, bool) {
	if len(raw) == 0 || isJSONNull(raw) {
		return nil, false
	}
	var quantity Quantity
	if json.Unmarshal(raw, &quantity) != nil || !validQuantity(&quantity, unit, nonnegative) {
		return nil, false
	}
	return &quantity, true
}

func decodeStatistics(raw json.RawMessage, start, end int64) []WorkoutStatistic {
	items, ok := rawArray(raw)
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

func decodeWorkoutEvents(raw json.RawMessage, start, end int64) []WorkoutEvent {
	items, ok := rawArray(raw)
	if !ok {
		return nil
	}
	out := make([]WorkoutEvent, 0, len(items))
	for _, item := range items {
		var event WorkoutEvent
		if decodeRawDetail(item, &event) != nil {
			continue
		}
		event.Metadata = projectMetadata(event.Metadata)
		if validateWorkoutEvents([]WorkoutEvent{event}, start, end) != nil {
			continue
		}
		out = append(out, event)
	}
	return out
}

func decodeWorkoutActivities(raw json.RawMessage, start, end int64, datasetID, workoutID string) []WorkoutActivity {
	items, ok := rawArray(raw)
	if !ok {
		return nil
	}
	out := make([]WorkoutActivity, 0, len(items))
	for index, item := range items {
		// Shadow the embedded UUID field: v6 only carries a top-level Workout
		// identity. Any producer-supplied activity UUID remains raw extension
		// data and must not affect projection or validation.
		var wire struct {
			WorkoutActivity
			UUID json.RawMessage `json:"uuid"`
		}
		if decodeRawDetail(item, &wire) != nil {
			continue
		}
		activity := wire.WorkoutActivity
		activity.Metadata = projectMetadata(activity.Metadata)
		for i := range activity.Events {
			activity.Events[i].Metadata = projectMetadata(activity.Events[i].Metadata)
		}
		activity.UUID = V6FactUUID(datasetID, workoutID, "", "activity", index)
		if activity.Validate(start, end) != nil {
			continue
		}
		out = append(out, activity)
	}
	return out
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

func decodeTriple(raw json.RawMessage, base int64) (start, end int64, value float64, ok bool) {
	items, ok := rawArray(raw)
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

func decodeRoutePoint(raw json.RawMessage, base int64) (WorkoutBundlePoint, bool) {
	items, ok := rawArray(raw)
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
