package syncing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

// DecodeWorkoutBundle runs both the wire check and the semantic check. It is a
// caller-side convenience for tests and clients; the server write path must not
// use it, because semantic validation happens exactly once, inside the Store
// import entry point (syncing.ValidateWorkoutBundle). Using this wrapper on the
// server would restore the duplicate semantic pass that P4 removes.
func DecodeWorkoutBundle(raw []byte, bundle *WorkoutBundle) error {
	if err := DecodeWorkoutBundleJSON(raw, bundle); err != nil {
		return err
	}
	return ValidateWorkoutBundle(*bundle)
}

func DecodeWorkoutBundleJSON(raw []byte, bundle *WorkoutBundle) error {
	if !utf8.Valid(raw) {
		return bundleField("$", "invalid_json_or_schema")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	scanner := bundleScanner{decoder: d}
	if _, err := scanner.walk(reflect.TypeOf(*bundle), "", 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return bundleField("$", "invalid_json_or_schema")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	d.UseNumber()
	if err := d.Decode(bundle); err != nil {
		return bundleField("$", "invalid_json_or_schema")
	}
	return nil
}

func ValidateWorkoutBundle(bundle WorkoutBundle) error {
	if err := bundleValues(bundle); err != nil {
		return err
	}
	if err := bundle.Validate(); err != nil {
		if failure, ok := err.(*Error); ok {
			if failure.Field == "" {
				failure.Field = "$"
				failure.Reason = "invalid_value_or_membership"
			}
		}
		return err
	}
	return nil
}

func bundleField(path, reason string) *Error {
	if path == "" {
		path = "$"
	}
	return &Error{Status: 422, Code: "unsupported_payload", Field: path, Reason: reason}
}

func bundleRequired(t reflect.Type) []string {
	switch t {
	case reflect.TypeOf(WorkoutBundle{}):
		return []string{"protocolVersion", "datasetID", "datasetEpoch", "collectorEpoch", "workoutID", "workout", "samples"}
	case reflect.TypeOf(Event{}):
		return []string{"op", "kind", "type", "uuid", "startUs", "endUs", "source"}
	case reflect.TypeOf(SampleDeletion{}):
		return []string{"sampleType", "sampleUUID"}
	case reflect.TypeOf(BundleObservation{}):
		return []string{"version", "observedAtUs", "provenance", "coverage"}
	case reflect.TypeOf(BundleProvenance{}):
		return []string{"workout", "samples", "routes"}
	case reflect.TypeOf(SampleProvenance{}):
		return []string{"sampleType", "sampleUUID", "source"}
	case reflect.TypeOf(RouteProvenance{}):
		return []string{"routeID", "source"}
	case reflect.TypeOf(HealthKitSource{}):
		return []string{"bundleID"}
	case reflect.TypeOf(BundleCoverage{}):
		return []string{"state", "sampleCount", "queryCompleted", "localPersisted"}
	case reflect.TypeOf(Source{}):
		return []string{"bundleID"}
	case reflect.TypeOf(Quantity{}):
		return []string{"value", "unit"}
	case reflect.TypeOf(Workout{}):
		return []string{"activityType", "durationUs"}
	case reflect.TypeOf(WorkoutActivity{}):
		return []string{"uuid", "startUs", "durationUs", "configuration"}
	case reflect.TypeOf(WorkoutConfiguration{}):
		return []string{"activityType", "locationType", "swimmingLocationType"}
	case reflect.TypeOf(WorkoutStatistic{}):
		return []string{"type", "startUs", "endUs"}
	case reflect.TypeOf(WorkoutEvent{}):
		return []string{"type", "startUs", "endUs"}
	case reflect.TypeOf(WorkoutBundleSeries{}):
		return []string{"sampleType", "sampleUUID", "points"}
	case reflect.TypeOf(SeriesPoint{}):
		return []string{"startUs", "endUs", "value", "unit"}
	case reflect.TypeOf(WorkoutBundleRoute{}):
		return []string{"routeID", "source", "points"}
	case reflect.TypeOf(WorkoutBundlePoint{}):
		return []string{"recordedAtUs", "latitude", "longitude"}
	}
	return nil
}

func bundlePath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}

// bundleShape is the memoised reflection description of one wire struct: the
// JSON name to field type index and the statically required key list. It is
// computed once per type instead of once per decoded object, which is what a
// 50,000 sample bundle spends most of its lexical pass on.
type bundleShape struct {
	fields   map[string]reflect.Type
	required []string
}

var bundleShapes sync.Map // reflect.Type -> *bundleShape

func bundleShapeOf(t reflect.Type) *bundleShape {
	if cached, ok := bundleShapes.Load(t); ok {
		return cached.(*bundleShape)
	}
	shape := &bundleShape{fields: make(map[string]reflect.Type, t.NumField())}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		shape.fields[name] = field.Type
	}
	shape.required = bundleRequired(t)
	actual, loaded := bundleShapes.LoadOrStore(t, shape)
	if loaded {
		return actual.(*bundleShape)
	}
	return shape
}

// The lexical/schema pass never builds generic JSON trees or copies nested
// RawMessages. It enforces counts before typed slices can allocate them.
type bundleScanner struct {
	decoder                   *json.Decoder
	routePoints, seriesPoints int
	// seen reuses one duplicate-detection set per nesting depth. A bundle with
	// 50,000 samples would otherwise allocate 50,000 short lived maps.
	seen []map[string]bool
}

// keySet returns the duplicate-detection set for one nesting depth, cleared and
// ready for use.
func (s *bundleScanner) keySet(depth int) map[string]bool {
	for len(s.seen) <= depth {
		s.seen = append(s.seen, make(map[string]bool, 16))
	}
	set := s.seen[depth]
	for key := range set {
		delete(set, key)
	}
	return set
}

func (s *bundleScanner) walk(t reflect.Type, path string, depth int) (any, error) {
	if depth > 64 {
		return nil, bundleField(path, "nesting_limit")
	}
	token, err := s.decoder.Token()
	if err != nil {
		return nil, bundleField(path, "invalid_json_or_schema")
	}
	if token == nil {
		return nil, bundleField(path, "null_not_allowed")
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	delimiter, isContainer := token.(json.Delim)
	if !isContainer {
		if t.Kind() == reflect.Struct || t.Kind() == reflect.Map || t.Kind() == reflect.Slice {
			return nil, bundleField(path, "invalid_type")
		}
		valid := true
		switch t.Kind() {
		case reflect.String:
			_, valid = token.(string)
		case reflect.Bool:
			_, valid = token.(bool)
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			n, ok := token.(json.Number)
			valid = ok
			if ok {
				_, e := strconv.ParseInt(n.String(), 10, 64)
				valid = e == nil
			}
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			n, ok := token.(json.Number)
			valid = ok
			if ok {
				_, e := strconv.ParseUint(n.String(), 10, 64)
				valid = e == nil
			}
		case reflect.Float32, reflect.Float64:
			_, valid = token.(json.Number)
		}
		if !valid {
			return nil, bundleField(path, "invalid_type")
		}
		return token, nil
	}
	switch delimiter {
	case '{':
		if t.Kind() != reflect.Struct && t.Kind() != reflect.Map && t.Kind() != reflect.Interface {
			return nil, bundleField(path, "invalid_type")
		}
		seen := s.keySet(depth)
		eventType := ""
		for s.decoder.More() {
			k, e := s.decoder.Token()
			if e != nil {
				return nil, bundleField(path, "invalid_json_or_schema")
			}
			key, ok := k.(string)
			if !ok || seen[key] {
				return nil, bundleField(bundlePath(path, key), "duplicate_field")
			}
			seen[key] = true
			if len(seen) > 256 {
				return nil, bundleField(path, "object_field_limit")
			}
			fieldType := reflect.TypeOf((*any)(nil)).Elem()
			if t.Kind() == reflect.Struct {
				fieldType, ok = bundleShapeOf(t).fields[key]
				if !ok {
					return nil, bundleField(bundlePath(path, key), "unknown_field")
				}
			} else if t.Kind() == reflect.Map {
				fieldType = t.Elem()
			}
			value, e := s.walk(fieldType, bundlePath(path, key), depth+1)
			if e != nil {
				return nil, e
			}
			if key == "type" {
				eventType, _ = value.(string)
			}
		}
		if t.Kind() == reflect.Struct {
			for _, key := range bundleShapeOf(t).required {
				if !seen[key] {
					return nil, bundleField(bundlePath(path, key), "required")
				}
			}
		}
		if t == reflect.TypeOf(Event{}) {
			// The quantity/category/workout requirement depends on the event
			// type, so it is checked here instead of in the cached list.
			dependent := ""
			if eventType == WorkoutType {
				dependent = "workout"
			} else if unit, ok := Units[eventType]; ok {
				if unit == "" {
					dependent = "category"
				} else {
					dependent = "quantity"
				}
			}
			if dependent != "" && !seen[dependent] {
				return nil, bundleField(bundlePath(path, dependent), "required")
			}
		}
	case '[':
		if t.Kind() != reflect.Slice && t.Kind() != reflect.Interface {
			return nil, bundleField(path, "invalid_type")
		}
		elem := reflect.TypeOf((*any)(nil)).Elem()
		if t.Kind() == reflect.Slice {
			elem = t.Elem()
		}
		limit := MaxBundleSeriesPoints
		if path == "routeDeletions" {
			limit = MaxBundleRoutes
		}
		if path == "completeSampleTypes" {
			limit = len(Units)
		}
		switch elem {
		case reflect.TypeOf(Event{}), reflect.TypeOf(SampleProvenance{}), reflect.TypeOf(SampleDeletion{}):
			limit = MaxWorkoutBundleEvents
		case reflect.TypeOf(WorkoutBundleSeries{}):
			limit = MaxBundleSeries
		case reflect.TypeOf(WorkoutBundleRoute{}), reflect.TypeOf(RouteProvenance{}):
			limit = MaxBundleRoutes
		}
		for i := 0; s.decoder.More(); i++ {
			if i >= limit {
				return nil, bundleField(path, "array_count_limit")
			}
			switch elem {
			case reflect.TypeOf(WorkoutBundlePoint{}):
				s.routePoints++
				if s.routePoints > MaxBundleRoutePoints {
					return nil, bundleField(path, "point_count_limit")
				}
			case reflect.TypeOf(SeriesPoint{}):
				s.seriesPoints++
				if s.seriesPoints > MaxBundleSeriesPoints {
					return nil, bundleField(path, "point_count_limit")
				}
			}
			if _, e := s.walk(elem, path+"["+strconv.Itoa(i)+"]", depth+1); e != nil {
				return nil, e
			}
		}
	default:
		return nil, bundleField(path, "invalid_type")
	}
	close, e := s.decoder.Token()
	if e != nil || ((delimiter == '{' && close != json.Delim('}')) || (delimiter == '[' && close != json.Delim(']'))) {
		return nil, bundleField(path, "invalid_json_or_schema")
	}
	return nil, nil
}

func bundleValues(b WorkoutBundle) error {
	id := func(v, path string) error {
		if !ValidID(v) {
			return bundleField(path, "invalid_uuid")
		}
		return nil
	}
	for _, item := range []struct{ v, path string }{{b.DatasetID, "datasetID"}, {b.DatasetEpoch, "datasetEpoch"}, {b.CollectorEpoch, "collectorEpoch"}, {b.WorkoutID, "workoutID"}} {
		if err := id(item.v, item.path); err != nil {
			return err
		}
	}
	if b.ProtocolVersion != 3 && b.ProtocolVersion != 4 && b.ProtocolVersion != 5 && b.ProtocolVersion != 6 {
		return bundleField("protocolVersion", "unsupported_protocol")
	}
	events := append([]Event{b.Workout}, b.Samples...)
	for i, e := range events {
		path := "workout"
		if i > 0 {
			path = fmt.Sprintf("samples[%d]", i-1)
		}
		if err := id(e.UUID, bundlePath(path, "uuid")); err != nil {
			return err
		}
		if e.StartUs != nil && e.EndUs != nil && (*e.StartUs > *e.EndUs || *e.StartUs < -62135596800000000 || *e.EndUs > 253402300799999999) {
			return bundleField(path+".endUs", "invalid_time_range")
		}
		if e.Source != nil && (len(e.Source.BundleID) == 0 || len(e.Source.BundleID) > 255) {
			return bundleField(path+".source.bundleID", "invalid_source")
		}
		if e.Quantity != nil {
			if e.Quantity.Unit != Units[e.Type] {
				return bundleField(path+".quantity.unit", "unsupported_unit")
			}
			if math.IsNaN(e.Quantity.Value) || math.IsInf(e.Quantity.Value, 0) {
				return bundleField(path+".quantity.value", "nonfinite")
			}
		}
	}
	for i, s := range b.Series {
		for j, p := range s.Points {
			path := fmt.Sprintf("series[%d].points[%d]", i, j)
			if p.Unit != Units[s.SampleType] {
				return bundleField(path+".unit", "unsupported_unit")
			}
			if p.StartUs > p.EndUs {
				return bundleField(path+".endUs", "invalid_time_range")
			}
		}
	}
	for i, r := range b.Routes {
		for j, p := range r.Points {
			path := fmt.Sprintf("routes[%d].points[%d]", i, j)
			if p.Latitude < -90 || p.Latitude > 90 {
				return bundleField(path+".latitude", "out_of_range")
			}
			if p.Longitude < -180 || p.Longitude > 180 {
				return bundleField(path+".longitude", "out_of_range")
			}
			if err := p.ToRoutePoint(0).Validate(); err != nil {
				return bundleField(path, "invalid_route_point")
			}
		}
	}
	return nil
}
