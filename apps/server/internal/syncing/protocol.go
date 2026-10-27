package syncing

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	MaxWireBytes     = 2 << 20
	MaxDecodedBytes  = 8 << 20
	MaxLineBytes     = 64 << 10
	MaxEvents        = 1000
	MaxPageFragments = 10000
	ProtocolVersion  = 2
)

type Error struct {
	Status             int    `json:"-"`
	Code               string `json:"code"`
	ExpectedSeq        *int64 `json:"expectedSeq,omitempty"`
	ExpectedChunkIndex *int   `json:"expectedChunkIndex,omitempty"`
	EventIndex         *int   `json:"eventIndex,omitempty"`
	RetryAfterSeconds  int    `json:"retryAfterSeconds,omitempty"`
}

func (e *Error) Error() string            { return e.Code }
func Fail(status int, code string) *Error { return &Error{Status: status, Code: code} }

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func ValidID(s string) bool {
	return uuidPattern.MatchString(s) && s != "00000000-0000-0000-0000-000000000000"
}
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// Field order and omission rules are the v2 envelope canonical encoding.
// Every semantic field is included, including an explicit null previousBatchID.
type Envelope struct {
	ProtocolVersion  int     `json:"protocolVersion"`
	DatasetID        string  `json:"datasetID"`
	DatasetEpoch     string  `json:"datasetEpoch"`
	CollectorEpoch   string  `json:"collectorEpoch"`
	StreamID         string  `json:"streamID"`
	BatchID          string  `json:"batchID"`
	ClientSeq        int64   `json:"clientSeq"`
	PreviousBatchID  *string `json:"previousBatchID"`
	PageID           string  `json:"pageID"`
	FragmentIndex    int     `json:"fragmentIndex"`
	FragmentCount    int     `json:"fragmentCount"`
	QueryFingerprint string  `json:"queryFingerprint"`
	ContentEncoding  string  `json:"contentEncoding"`
	WireBytes        int     `json:"wireBytes"`
	DecodedBytes     int     `json:"decodedBytes"`
	EventCount       int     `json:"eventCount"`
	PayloadSHA256    string  `json:"payloadSHA256"`
}

func (e Envelope) Validate() error {
	if e.ProtocolVersion != ProtocolVersion {
		return Fail(422, "unsupported_protocol")
	}
	for _, id := range []string{e.DatasetID, e.DatasetEpoch, e.CollectorEpoch, e.StreamID, e.BatchID, e.PageID} {
		if !ValidID(id) {
			return Fail(422, "unsupported_payload")
		}
	}
	if e.PreviousBatchID != nil && !ValidID(*e.PreviousBatchID) {
		return Fail(422, "unsupported_payload")
	}
	if e.ContentEncoding != "gzip" || e.ClientSeq < 1 ||
		(e.ClientSeq == 1) != (e.PreviousBatchID == nil) || e.FragmentCount < 1 || e.FragmentCount > MaxPageFragments ||
		e.FragmentIndex < 0 || e.FragmentIndex >= e.FragmentCount ||
		!strings.HasPrefix(e.QueryFingerprint, "sha256:") || !digestPattern.MatchString(strings.TrimPrefix(e.QueryFingerprint, "sha256:")) ||
		!digestPattern.MatchString(e.PayloadSHA256) || e.WireBytes < 1 || e.DecodedBytes < 0 || e.EventCount < 0 {
		return Fail(422, "unsupported_payload")
	}
	if e.WireBytes > MaxWireBytes || e.DecodedBytes > MaxDecodedBytes || e.EventCount > MaxEvents {
		return Fail(413, "batch_too_large")
	}
	return nil
}
func (e Envelope) Canonical() ([]byte, []byte) {
	b, _ := json.Marshal(e)
	h := sha256.Sum256(b)
	return b, h[:]
}

type StreamRequest struct {
	DatasetID        string `json:"datasetID"`
	DatasetEpoch     string `json:"datasetEpoch"`
	CollectorEpoch   string `json:"collectorEpoch"`
	StreamID         string `json:"streamID"`
	SampleType       string `json:"sampleType"`
	QueryFingerprint string `json:"queryFingerprint"`
}

func (s StreamRequest) Validate() error {
	for _, id := range []string{s.DatasetID, s.DatasetEpoch, s.CollectorEpoch, s.StreamID} {
		if !ValidID(id) {
			return Fail(422, "unsupported_payload")
		}
	}
	if _, ok := Units[s.SampleType]; !ok && s.SampleType != WorkoutType {
		return Fail(422, "unsupported_payload")
	}
	if !strings.HasPrefix(s.QueryFingerprint, "sha256:") || !digestPattern.MatchString(strings.TrimPrefix(s.QueryFingerprint, "sha256:")) {
		return Fail(422, "unsupported_payload")
	}
	return nil
}

type Principal struct {
	DatasetID, DeviceID string
	CanCollect          bool
}
type Quantity struct {
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
}
type Source struct {
	BundleID string `json:"bundleID"`
}
type Event struct {
	Op       string         `json:"op"`
	Kind     string         `json:"kind"`
	Type     string         `json:"type"`
	UUID     string         `json:"uuid"`
	StartUs  *int64         `json:"startUs,omitempty"`
	EndUs    *int64         `json:"endUs,omitempty"`
	Quantity *Quantity      `json:"quantity,omitempty"`
	Category *int           `json:"category,omitempty"`
	Source   *Source        `json:"source,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Workout  *Workout       `json:"workout,omitempty"`
}

// This capability registry is intentionally narrower than all HealthKit types.
// Workout has its own structured payload; route and series containers cannot be scalars.
var Units = map[string]string{
	"HKQuantityTypeIdentifierHeartRate":                "count/min",
	"HKQuantityTypeIdentifierRestingHeartRate":         "count/min",
	"HKQuantityTypeIdentifierWalkingHeartRateAverage":  "count/min",
	"HKQuantityTypeIdentifierHeartRateVariabilitySDNN": "ms",
	"HKQuantityTypeIdentifierStepCount":                "count",
	"HKQuantityTypeIdentifierDistanceWalkingRunning":   "m",
	"HKQuantityTypeIdentifierActiveEnergyBurned":       "kcal",
	"HKQuantityTypeIdentifierBasalEnergyBurned":        "kcal",
	"HKQuantityTypeIdentifierVO2Max":                   "mL/kg/min",
	"HKQuantityTypeIdentifierBodyMass":                 "kg",
	"HKQuantityTypeIdentifierRespiratoryRate":          "count/min",
	"HKQuantityTypeIdentifierOxygenSaturation":         "1",
	"HKQuantityTypeIdentifierRunningSpeed":             "m/s",
	"HKQuantityTypeIdentifierWalkingSpeed":             "m/s",
	"HKQuantityTypeIdentifierRunningPower":             "W",
	"HKQuantityTypeIdentifierRunningCadence":           "count/min",
	"HKQuantityTypeIdentifierCyclingCadence":           "count/min",
	"HKQuantityTypeIdentifierWalkingCadence":           "count/min",
	"HKCategoryTypeIdentifierSleepAnalysis":            "",
}

func (e Event) Validate(sampleType string) error {
	unit, ok := Units[e.Type]
	isWorkout := e.Type == WorkoutType
	if (!ok && !isWorkout) || e.Type != sampleType || !ValidID(e.UUID) ||
		(isWorkout && e.Kind != "workout") || (!isWorkout && e.Kind != "sample") {
		return Fail(422, "unsupported_payload")
	}
	if e.Op == "delete" {
		if e.StartUs != nil || e.EndUs != nil || e.Quantity != nil || e.Category != nil || e.Source != nil || e.Metadata != nil || e.Workout != nil {
			return Fail(422, "unsupported_payload")
		}
		return nil
	}
	if e.Op != "upsert" || e.StartUs == nil || e.EndUs == nil || *e.StartUs > *e.EndUs ||
		*e.StartUs < -62135596800000000 || *e.EndUs > 253402300799999999 ||
		e.Source == nil || len(e.Source.BundleID) == 0 || len(e.Source.BundleID) > 255 {
		return Fail(422, "unsupported_payload")
	}
	if isWorkout {
		if e.Quantity != nil || e.Category != nil || e.Workout == nil {
			return Fail(422, "unsupported_payload")
		}
		return e.Workout.Validate(*e.StartUs, *e.EndUs)
	}
	if e.Workout != nil {
		return Fail(422, "unsupported_payload")
	}
	if unit == "" {
		if e.Category == nil || *e.Category < 0 || *e.Category > 5 || e.Quantity != nil {
			return Fail(422, "unsupported_payload")
		}
	} else if e.Quantity == nil || e.Category != nil || e.Quantity.Unit != unit || math.IsNaN(e.Quantity.Value) || math.IsInf(e.Quantity.Value, 0) {
		if e.Quantity != nil && e.Quantity.Unit != unit {
			return Fail(422, "unsupported_unit")
		}
		return Fail(422, "unsupported_payload")
	}
	return nil
}
func (e Event) Canonical() ([]byte, []byte) {
	// Normalize absent/empty metadata to one identity; JSON map keys are sorted.
	if len(e.Metadata) == 0 {
		e.Metadata = nil
	}
	b, _ := json.Marshal(e)
	h := sha256.Sum256(b)
	return b, h[:]
}

// DecodeJSON rejects unknown fields, duplicate keys at any depth, and trailing JSON.
func DecodeJSON(b []byte, v any) error {
	if !utf8.Valid(b) {
		return Fail(422, "unsupported_payload")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := walkJSON(d); err != nil {
		return Fail(422, "unsupported_payload")
	}
	if _, err := d.Token(); err != io.EOF {
		return Fail(422, "unsupported_payload")
	}
	// encoding/json matches struct fields case-insensitively, even with
	// DisallowUnknownFields. Wire identities require exact schema spelling.
	if err := exactJSONFields(b, reflect.TypeOf(v)); err != nil {
		return Fail(422, "unsupported_payload")
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return Fail(422, "unsupported_payload")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil || fields == nil {
		return Fail(422, "unsupported_payload")
	}
	var required []string
	switch value := v.(type) {
	case *Envelope:
		required = []string{"protocolVersion", "datasetID", "datasetEpoch", "collectorEpoch", "streamID", "batchID", "clientSeq", "previousBatchID", "pageID", "fragmentIndex", "fragmentCount", "queryFingerprint", "contentEncoding", "wireBytes", "decodedBytes", "eventCount", "payloadSHA256"}
	case *StreamRequest:
		required = []string{"datasetID", "datasetEpoch", "collectorEpoch", "streamID", "sampleType", "queryFingerprint"}
	case *SeriesRequest:
		required = []string{"datasetID", "datasetEpoch", "collectorEpoch", "seriesID", "sampleType", "sampleUUID"}
	case *SeriesSeal:
		required = []string{"chunkCount", "totalPoints", "manifestSHA256"}
	case *SeriesPoint:
		required = []string{"startUs", "endUs", "value", "unit"}
		for _, raw := range fields {
			if bytes.Equal(raw, []byte("null")) {
				return Fail(422, "unsupported_payload")
			}
		}
	case *WorkoutPackageRequest:
		required = []string{"datasetID", "datasetEpoch", "collectorEpoch", "workoutID", "startUs", "endUs", "source", "workout"}
		for _, raw := range fields {
			if bytes.Equal(raw, []byte("null")) {
				return Fail(422, "unsupported_payload")
			}
		}
		if raw, ok := fields["workout"]; ok {
			if err := validateWorkoutJSON(raw); err != nil {
				return err
			}
		}
	case *WorkoutPackageSeal:
		required = []string{"chunkCount", "totalDetails", "manifestSHA256"}
		for _, raw := range fields {
			if bytes.Equal(raw, []byte("null")) {
				return Fail(422, "unsupported_payload")
			}
		}
	case *WorkoutDetail:
		required = []string{"kind", "ordinal"}
		for _, raw := range fields {
			if bytes.Equal(raw, []byte("null")) {
				return Fail(422, "unsupported_payload")
			}
		}
		present := 0
		for _, key := range []string{"activity", "statistic", "event"} {
			if _, ok := fields[key]; ok {
				present++
			}
		}
		if present != 1 {
			return Fail(422, "unsupported_payload")
		}
		for _, key := range []string{"activity", "statistic", "event"} {
			if raw, ok := fields[key]; ok {
				if err := validateWorkoutJSON(raw); err != nil {
					return err
				}
			}
		}
	case *Event:
		required = []string{"op", "kind", "type", "uuid"}
		for _, raw := range fields {
			if bytes.Equal(raw, []byte("null")) {
				return Fail(422, "unsupported_payload")
			}
		}
		if value.Op == "delete" && len(fields) != 4 {
			return Fail(422, "unsupported_payload")
		}
		if value.Op == "upsert" {
			required = append(required, "startUs", "endUs", "source")
			if value.Quantity != nil {
				var quantity map[string]json.RawMessage
				if json.Unmarshal(fields["quantity"], &quantity) != nil || quantity["value"] == nil || bytes.Equal(quantity["value"], []byte("null")) {
					return Fail(422, "unsupported_payload")
				}
			}
			if value.Workout != nil {
				if err := validateWorkoutJSON(fields["workout"]); err != nil {
					return err
				}
			}
		}
	case *RouteManifest:
		required = []string{"protocolVersion", "datasetID", "datasetEpoch", "collectorEpoch", "routeID", "workoutID", "source", "expectedChunkCount", "expectedPointCount", "firstRecordedAtUs", "lastRecordedAtUs"}
		for _, raw := range fields {
			if bytes.Equal(raw, []byte("null")) {
				return Fail(422, "unsupported_payload")
			}
		}
	case *RouteSeal:
		required = []string{"protocolVersion", "chunkCount", "totalPoints", "manifestSHA256", "payloadManifestSHA256"}
		for _, raw := range fields {
			if bytes.Equal(raw, []byte("null")) {
				return Fail(422, "unsupported_payload")
			}
		}
	case *RouteDeleteRequest:
		required = []string{"protocolVersion", "datasetEpoch", "collectorEpoch"}
		for _, raw := range fields {
			if bytes.Equal(raw, []byte("null")) {
				return Fail(422, "unsupported_payload")
			}
		}
	case *RoutePoint:
		required = []string{"sequence", "recordedAtUs", "latitude", "longitude"}
		for _, raw := range fields {
			if bytes.Equal(raw, []byte("null")) {
				return Fail(422, "unsupported_payload")
			}
		}
	case *AssociationRequest:
		required = []string{"protocolVersion", "datasetID", "datasetEpoch", "collectorEpoch", "associationID", "sampleType", "sampleUUID", "workoutUUID", "source"}
		for _, raw := range fields {
			if bytes.Equal(raw, []byte("null")) {
				return Fail(422, "unsupported_payload")
			}
		}
	case *WorkoutEnrichmentRequest:
		required = []string{"protocolVersion", "datasetID", "datasetEpoch", "collectorEpoch", "enrichmentID", "workoutID", "source", "sourceVersion", "observedAtUs"}
		for _, raw := range fields {
			if bytes.Equal(raw, []byte("null")) {
				return Fail(422, "unsupported_payload")
			}
		}
	}
	for _, key := range required {
		raw, ok := fields[key]
		if !ok || (key != "previousBatchID" && bytes.Equal(raw, []byte("null"))) {
			return Fail(422, "unsupported_payload")
		}
	}
	return nil
}

func exactJSONFields(raw []byte, typ reflect.Type) error {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return err
		}
		fields := make(map[string]reflect.Type, typ.NumField())
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name != "" && name != "-" {
				fields[name] = field.Type
			}
		}
		for key, value := range object {
			field, ok := fields[key]
			if !ok {
				return errors.New("unknown field")
			}
			if err := exactJSONFields(value, field); err != nil {
				return err
			}
		}
	case reflect.Slice:
		var array []json.RawMessage
		if err := json.Unmarshal(raw, &array); err != nil {
			return err
		}
		for _, value := range array {
			if err := exactJSONFields(value, typ.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}
func walkJSON(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	delim, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := k.(string)
			if !ok || seen[key] {
				return errors.New("duplicate key")
			}
			seen[key] = true
			if err := walkJSON(d); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := walkJSON(d); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid delimiter")
	}
	_, err = d.Token()
	return err
}

// ParseBody performs all network/hash/gzip/schema work before any database lock.
func ParseBody(r io.Reader, e Envelope, sampleType string) ([]Event, error) {
	wire, err := io.ReadAll(io.LimitReader(r, MaxWireBytes+1))
	if err != nil {
		return nil, Fail(400, "body_read_failed")
	}
	if len(wire) > MaxWireBytes {
		return nil, Fail(413, "batch_too_large")
	}
	h := sha256.Sum256(wire)
	if len(wire) != e.WireBytes || hex.EncodeToString(h[:]) != e.PayloadSHA256 {
		return nil, Fail(422, "payload_hash_mismatch")
	}
	gz, err := gzip.NewReader(bytes.NewReader(wire))
	if err != nil {
		return nil, Fail(422, "unsupported_payload")
	}
	defer gz.Close()
	decoded, err := io.ReadAll(io.LimitReader(gz, MaxDecodedBytes+1))
	if len(decoded) > MaxDecodedBytes {
		return nil, Fail(413, "batch_too_large")
	}
	if err != nil || len(decoded) != e.DecodedBytes {
		return nil, Fail(422, "unsupported_payload")
	}
	lines := bytes.Split(decoded, []byte{'\n'})
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > MaxEvents {
		return nil, Fail(413, "batch_too_large")
	}
	if len(lines) != e.EventCount {
		return nil, Fail(422, "event_count_mismatch")
	}
	events := make([]Event, 0, len(lines))
	for i, line := range lines {
		if len(line) > MaxLineBytes {
			return nil, Fail(413, "event_too_large")
		}
		var ev Event
		if err := DecodeJSON(line, &ev); err != nil {
			return nil, &Error{Status: 422, Code: "unsupported_payload", EventIndex: &i}
		}
		if err := ev.Validate(sampleType); err != nil {
			var typed *Error
			if errors.As(err, &typed) {
				copy := *typed
				copy.EventIndex = &i
				return nil, &copy
			}
			return nil, &Error{Status: 422, Code: "unsupported_payload", EventIndex: &i}
		}
		events = append(events, ev)
	}
	if err := ValidateDetailBudget(events); err != nil {
		return nil, err
	}
	return events, nil
}

// Collapse applies delete-wins independent of arrival order and gives a fixed key order.
func Collapse(events []Event) ([]Event, error) {
	deletes := map[string]Event{}
	upserts := map[string]Event{}
	for _, e := range events {
		if e.Op == "delete" {
			deletes[e.Type+":"+e.UUID] = e
		}
	}
	for _, e := range events {
		k := e.Type + ":" + e.UUID
		if e.Op == "delete" {
			continue
		}
		if _, ok := deletes[k]; ok {
			continue
		}
		if prev, ok := upserts[k]; ok {
			_, a := prev.Canonical()
			_, b := e.Canonical()
			if !bytes.Equal(a, b) {
				return nil, Fail(409, "sample_identity_conflict")
			}
		}
		upserts[k] = e
	}
	for k, e := range deletes {
		upserts[k] = e
	}
	keys := make([]string, 0, len(upserts))
	for k := range upserts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]Event, 0, len(keys))
	for _, k := range keys {
		out = append(out, upserts[k])
	}
	return out, nil
}
