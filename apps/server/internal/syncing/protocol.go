package syncing

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
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
	Field              string `json:"field,omitempty"`
	Reason             string `json:"reason,omitempty"`
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
func ValidDigest(s string) bool { return digestPattern.MatchString(s) }
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

type Principal struct {
	DatasetID, DeviceID string
	CanCollect          bool
	DatasetRetired      bool
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
	"HKQuantityTypeIdentifierHeartRate":                  "count/min",
	"HKQuantityTypeIdentifierRestingHeartRate":           "count/min",
	"HKQuantityTypeIdentifierWalkingHeartRateAverage":    "count/min",
	"HKQuantityTypeIdentifierHeartRateVariabilitySDNN":   "ms",
	"HKQuantityTypeIdentifierStepCount":                  "count",
	"HKQuantityTypeIdentifierDistanceWalkingRunning":     "m",
	"HKQuantityTypeIdentifierActiveEnergyBurned":         "kcal",
	"HKQuantityTypeIdentifierBasalEnergyBurned":          "kcal",
	"HKQuantityTypeIdentifierVO2Max":                     "mL/kg/min",
	"HKQuantityTypeIdentifierBodyMass":                   "kg",
	"HKQuantityTypeIdentifierRespiratoryRate":            "count/min",
	"HKQuantityTypeIdentifierOxygenSaturation":           "1",
	"HKQuantityTypeIdentifierRunningSpeed":               "m/s",
	"HKQuantityTypeIdentifierWalkingSpeed":               "m/s",
	"HKQuantityTypeIdentifierRunningPower":               "W",
	"HKQuantityTypeIdentifierRunningGroundContactTime":   "ms",
	"HKQuantityTypeIdentifierRunningStrideLength":        "m",
	"HKQuantityTypeIdentifierRunningVerticalOscillation": "m",
	"HKQuantityTypeIdentifierDistanceCycling":            "m",
	"HKQuantityTypeIdentifierDistanceSwimming":           "m",
	"HKQuantityTypeIdentifierDistanceWheelchair":         "m",
	"HKQuantityTypeIdentifierDistanceDownhillSnowSports": "m",
	"HKQuantityTypeIdentifierSwimmingStrokeCount":        "count",
	"HKQuantityTypeIdentifierFlightsClimbed":             "count",
	"HKQuantityTypeIdentifierCyclingPower":               "W",
	"HKQuantityTypeIdentifierCyclingSpeed":               "m/s",
	"HKQuantityTypeIdentifierCyclingCadence":             "count/min",
	"HKQuantityTypeIdentifierWalkingCadence":             "count/min",
	"HKQuantityTypeIdentifierRunningCadence":             "count/min",
	"HKQuantityTypeIdentifierHeartRateRecoveryOneMinute": "count/min",
	"HKCategoryTypeIdentifierSleepAnalysis":              "",
}

func (e Event) Validate(sampleType string) error {
	if _, spoofed := e.Metadata[VO2Reserved]; spoofed {
		return Fail(422, "unsupported_metadata")
	}
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
	if e.Type == VO2Type && e.Metadata[VO2Reserved] != nil {
		b = VO2Canonical(e)
	}
	h := sha256.Sum256(b)
	return b, h[:]
}

// DecodeStrictJSON shares the wire boundary without coupling independent DTOs
// to P3's required-field rules. RawMessage is validated lexically, then by its
// owning command's typed semantics.
func DecodeStrictJSON(b []byte, v any) error {
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
	return nil
}

func exactJSONFields(raw []byte, typ reflect.Type) error {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ == reflect.TypeOf(json.RawMessage{}) {
		return nil
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
	case reflect.Map:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return err
		}
		for _, value := range object {
			if err := exactJSONFields(value, typ.Elem()); err != nil {
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
