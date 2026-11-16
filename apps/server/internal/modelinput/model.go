// Package modelinput validates append-only user model inputs. It performs no
// I/O and never writes HealthKit or derives clinical facts.
package modelinput

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
)

const (
	SchemaSleepTarget     = "rai.sleep_target/1"
	SchemaWorkoutEffort   = "rai.workout_effort/1"
	SchemaOverlap         = "rai.workout_overlap_resolution/1"
	SchemaSourceSelection = "rai.source_context_selection/1"
	SchemaHRReference     = "rai.hr_reference/2"
	SchemaVO2Selection    = "rai.vo2max.source_selection/1"
	SchemaTRIMPParameters = "rai.trimp_parameters/1"
)

var ErrInvalid = errors.New("model_input_invalid")
var ErrConflict = errors.New("model_input_conflict")
var ErrNotFound = errors.New("model_input_not_found")

var payloadFields = map[string][]string{
	"hr_reference":               {"maxHR", "restingHR", "measuredAt", "restingMeasuredAt", "method", "unit"},
	"sleep_target":               {"hours"},
	"workout_effort":             {"workoutID", "mode", "userConfirmed", "independentlyVerified", "segment"},
	"workout_overlap_resolution": {"primaryWorkoutID", "duplicateWorkoutID"},
	"source_context_selection":   {"sourceEpoch"},
	"vo2max_source_selection":    {"sourceEpoch", "revoked"},
	"trimp_parameters":           {"restingHR", "maxHR", "coefficientA", "coefficientB"},
	"revocation":                 {"targetSchema", "reason"},
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)
var sourceEpochRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type Payload map[string]any

type Request struct {
	SchemaVersion    string  `json:"schemaVersion,omitempty"`
	ID               string  `json:"id"`
	Kind             string  `json:"kind"`
	SubjectKey       string  `json:"subjectKey"`
	EffectiveFrom    string  `json:"effectiveFrom"`
	EffectiveThrough *string `json:"effectiveThrough,omitempty"`
	SupersedesID     *string `json:"supersedesId,omitempty"`
	RevokesID        *string `json:"revokesId,omitempty"`
	Provenance       string  `json:"provenance"`
	Payload          Payload `json:"payload"`
}

type Canonical struct {
	ID               string
	Kind             string
	SubjectKey       string
	Schema           string
	EffectiveFrom    time.Time
	EffectiveThrough *time.Time
	SupersedesID     *string
	RevokesID        *string
	Provenance       string
	Payload          []byte
	ContentHash      string
}

type Receipt struct {
	InputID        string   `json:"inputID"`
	Schema         string   `json:"schemaVersion"`
	ContentHash    string   `json:"contentSHA256"`
	AcceptedAtUS   int64    `json:"acceptedAtUs"`
	AnalysisStatus string   `json:"analysisStatus"`
	JobIDs         []string `json:"jobIDs"`
	// Only trimp_parameters appends a legacy analysis parameter version and
	// re-queues recent workouts; other kinds omit both.
	ParameterVersion *int64 `json:"parameterVersion,omitempty"`
	WorkoutCount     *int64 `json:"workoutCount,omitempty"`
}

func Validate(r Request, now time.Time) (Canonical, error) {
	var c Canonical
	allowed, ok := payloadFields[r.Kind]
	if !ok {
		return c, ErrInvalid
	}
	for k := range r.Payload {
		valid := false
		for _, field := range allowed {
			if k == field {
				valid = true
				break
			}
		}
		if !valid {
			return c, ErrInvalid
		}
	}
	if !uuidRE.MatchString(r.ID) || r.SubjectKey != "athlete" || strings.TrimSpace(r.Provenance) == "" || len(r.Provenance) > 200 || (len(r.Payload) == 0 && r.Kind != "revocation") {
		return c, ErrInvalid
	}
	if r.EffectiveThrough != nil && *r.EffectiveThrough == "" {
		return c, ErrInvalid
	}
	from, err := time.Parse(time.RFC3339, r.EffectiveFrom)
	if err != nil || from.IsZero() {
		return c, ErrInvalid
	}
	from = from.UTC().Truncate(time.Microsecond)
	var through *time.Time
	if r.EffectiveThrough != nil {
		v, e := time.Parse(time.RFC3339, *r.EffectiveThrough)
		v = v.UTC().Truncate(time.Microsecond)
		if e != nil || !v.After(from) {
			return c, ErrInvalid
		}
		through = &v
	}
	if r.RevokesID != nil && r.SupersedesID != nil {
		return c, ErrInvalid
	}
	if r.SupersedesID != nil && !uuidRE.MatchString(*r.SupersedesID) || r.RevokesID != nil && !uuidRE.MatchString(*r.RevokesID) {
		return c, ErrInvalid
	}
	schema := ""
	switch r.Kind {
	case "hr_reference":
		if r.SchemaVersion == SchemaHRReferenceV3 {
			if r.SupersedesID != nil {
				return c, ErrInvalid
			}
			if err = validateHRV3(r.Payload, now); err != nil {
				return c, err
			}
			schema = SchemaHRReferenceV3
			break
		}
		if err = validateHR(r.Payload, now); err != nil {
			return c, err
		}
		schema = SchemaHRReference
	case "sleep_target":
		if err = validateSleep(r.Payload); err != nil {
			return c, err
		}
		schema = SchemaSleepTarget
	case "workout_effort":
		if err = validateEffort(r.Payload); err != nil {
			return c, err
		}
		schema = SchemaWorkoutEffort
	case "workout_overlap_resolution":
		if err = validateOverlap(r.Payload); err != nil {
			return c, err
		}
		schema = SchemaOverlap
	case "source_context_selection":
		if err = validateSource(r.Payload); err != nil {
			return c, err
		}
		schema = SchemaSourceSelection
	case "vo2max_source_selection":
		if err = validateVO2Selection(r.Payload); err != nil {
			return c, err
		}
		schema = SchemaVO2Selection
	case "trimp_parameters":
		if err = validateTRIMP(r.Payload); err != nil {
			return c, err
		}
		schema = SchemaTRIMPParameters
	case "revocation":
		if r.RevokesID == nil || r.SupersedesID != nil {
			return c, ErrInvalid
		}
		schema, err = validateRevocation(r.Payload)
		if err != nil {
			return c, err
		}
	default:
		return c, ErrInvalid
	}
	if r.SchemaVersion != "" && r.SchemaVersion != schema {
		return c, ErrInvalid
	}
	// Include every identity field in the hash, using the same precision as PG.
	r.ID = strings.ToLower(r.ID)
	payload := make(Payload, len(r.Payload))
	for key, value := range r.Payload {
		switch key {
		case "workoutID", "primaryWorkoutID", "duplicateWorkoutID":
			value = strings.ToLower(value.(string))
		case "hours", "maxHR", "restingHR", "coefficientA", "coefficientB":
			if schema != SchemaHRReferenceV3 {
				value, _ = number(r.Payload, key)
			}
		}
		payload[key] = value
	}
	r.Payload = payload
	for _, ref := range []**string{&r.SupersedesID, &r.RevokesID} {
		if *ref != nil {
			id := strings.ToLower(**ref)
			*ref = &id
		}
	}
	r.EffectiveFrom = from.Format(time.RFC3339Nano)
	if through != nil {
		value := through.Format(time.RFC3339Nano)
		r.EffectiveThrough = &value
	}
	b, err := json.Marshal(r.Payload)
	if err != nil || len(b) > 65536 {
		return c, ErrInvalid
	}
	identity, err := json.Marshal(struct {
		Schema string
		Body   Request
	}{schema, r})
	if err != nil {
		return c, ErrInvalid
	}
	sum := sha256.Sum256(identity)
	return Canonical{ID: r.ID, Kind: r.Kind, SubjectKey: r.SubjectKey, Schema: schema, EffectiveFrom: from, EffectiveThrough: through, SupersedesID: r.SupersedesID, RevokesID: r.RevokesID, Provenance: r.Provenance, Payload: b, ContentHash: hex.EncodeToString(sum[:])}, nil
}

func number(m map[string]any, k string) (float64, bool) {
	v, ok := m[k].(float64)
	if n, isNumber := m[k].(json.Number); isNumber {
		var err error
		v, err = n.Float64()
		ok = err == nil
	}
	if !ok || v != v || v > 1e308 || v < -1e308 {
		return 0, false
	}
	return v, true
}
func validateHR(m map[string]any, now time.Time) error {
	maxHR, ok := number(m, "maxHR")
	if !ok || maxHR < 30 || maxHR >= 220 || m["unit"] != "bpm" {
		return ErrInvalid
	}
	switch m["method"] {
	// age_estimated is an explicit, user-chosen cold start; it is never applied
	// silently and running fitness marks every result based on it as limited.
	case "lab_max_test", "field_max_test", "clinical", "user_entered", "wearable_estimate", "age_estimated":
	default:
		return ErrInvalid
	}
	if !validMeasurementTime(m["measuredAt"], now) {
		return ErrInvalid
	}
	if _, supplied := m["restingHR"]; supplied {
		rest, ok := number(m, "restingHR")
		if !ok || rest < 30 || rest >= 220 || maxHR <= rest || !validMeasurementTime(m["restingMeasuredAt"], now) {
			return ErrInvalid
		}
	} else if _, supplied := m["restingMeasuredAt"]; supplied {
		return ErrInvalid
	}
	return nil
}

func validMeasurementTime(raw any, now time.Time) bool {
	value, ok := raw.(string)
	if !ok {
		return false
	}
	t, err := time.Parse(time.RFC3339, value)
	return err == nil && t.UnixMicro() > 0 && !t.After(now)
}

func validateSleep(m map[string]any) error {
	h, ok := number(m, "hours")
	if !ok || h <= 0 || h > 24 {
		return ErrInvalid
	}
	return nil
}
func validateEffort(m map[string]any) error {
	w, ok := m["workoutID"].(string)
	if !ok || !uuidRE.MatchString(w) {
		return ErrInvalid
	}
	mode, _ := m["mode"].(string)
	switch mode {
	// maximal_supported is the original all-out label and reads as a race.
	case "maximal_supported", "race", "threshold_test", "speed_test", "sprint_test", "maximal_time_trial", "controlled_threshold":
	default:
		return ErrInvalid
	}
	raw, hasSegment := m["segment"]
	if (mode == "maximal_time_trial" || mode == "controlled_threshold") && !hasSegment {
		return ErrInvalid
	}
	if hasSegment {
		if err := validateEffortSegment(mode, raw); err != nil {
			return err
		}
	}
	user, _ := m["userConfirmed"].(bool)
	independent, _ := m["independentlyVerified"].(bool)
	if raw, exists := m["independentlyVerified"]; exists && raw != false {
		return ErrInvalid
	}
	if !user || independent {
		return ErrInvalid
	}
	return nil
}

// Effort main-segment protocols (lactate threshold plan section 11.2).
const (
	ProtocolTT30         = "rai.tt30/1"
	ProtocolControlled25 = "rai.controlled25/1"
	ProtocolUserDefined  = "user_defined/1"
)

// validateEffortSegment checks the optional main segment of an effort label:
// whole non-negative seconds from the Workout start, a positive duration and a
// protocol whose nominal length matches (+-120 s). Whether the segment lies
// inside the Workout is checked by the model against the recorded timeline.
func validateEffortSegment(mode string, raw any) error {
	seg, ok := raw.(map[string]any)
	if !ok || len(seg) != 3 {
		return ErrInvalid
	}
	whole := func(k string) (float64, bool) {
		v, ok := seg[k].(float64)
		return v, ok && v >= 0 && v == float64(int64(v)) && v <= 86400
	}
	start, ok1 := whole("startOffsetSeconds")
	duration, ok2 := whole("durationSeconds")
	protocol, ok3 := seg["protocol"].(string)
	if !ok1 || !ok2 || !ok3 || duration <= 0 || start+duration > 86400 {
		return ErrInvalid
	}
	switch protocol {
	case ProtocolTT30:
		if mode != "maximal_time_trial" || duration < 1680 || duration > 1920 {
			return ErrInvalid
		}
	case ProtocolControlled25:
		if mode != "controlled_threshold" || duration < 1380 || duration > 1620 {
			return ErrInvalid
		}
	case ProtocolUserDefined:
		if duration < 60 {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

func validateOverlap(m map[string]any) error {
	for _, k := range []string{"primaryWorkoutID", "duplicateWorkoutID"} {
		v, ok := m[k].(string)
		if !ok || !uuidRE.MatchString(v) {
			return ErrInvalid
		}
	}
	if strings.EqualFold(m["primaryWorkoutID"].(string), m["duplicateWorkoutID"].(string)) {
		return ErrInvalid
	}
	return nil
}
func validateRevocation(m map[string]any) (string, error) {
	v, ok := m["targetSchema"].(string)
	if !ok {
		return "", ErrInvalid
	}
	switch v {
	case SchemaHRReferenceV3, SchemaHRReference, "rai.hr_reference/1", "rai.vo2max.source_selection/1", SchemaSleepTarget, SchemaWorkoutEffort, SchemaOverlap, SchemaSourceSelection:
	default:
		return "", ErrInvalid
	}
	if raw, ok := m["reason"]; ok {
		s, ok := raw.(string)
		if !ok || len(s) > 200 {
			return "", ErrInvalid
		}
	}
	return v, nil
}

func validateSource(m map[string]any) error {
	v, ok := m["sourceEpoch"].(string)
	if !ok || strings.TrimSpace(v) == "" || len(v) > 200 {
		return ErrInvalid
	}
	return nil
}

func validateTRIMP(m map[string]any) error {
	rest, ok := number(m, "restingHR")
	if !ok || rest < 30 || rest >= 220 {
		return ErrInvalid
	}
	maxHR, ok := number(m, "maxHR")
	if !ok || maxHR < 30 || maxHR >= 220 || maxHR <= rest {
		return ErrInvalid
	}
	for _, key := range []string{"coefficientA", "coefficientB"} {
		v, ok := number(m, key)
		if !ok || v <= 0 || v > 10 {
			return ErrInvalid
		}
	}
	return nil
}

func validateVO2Selection(m map[string]any) error {
	epoch, ok := m["sourceEpoch"].(string)
	if !ok || !sourceEpochRE.MatchString(epoch) {
		return ErrInvalid
	}
	if _, ok = m["revoked"].(bool); !ok {
		return ErrInvalid
	}
	return nil
}

func ReceiptFor(c Canonical, accepted time.Time, jobs []string) Receipt {
	if jobs == nil {
		jobs = []string{}
	}
	return Receipt{InputID: c.ID, Schema: c.Schema, ContentHash: c.ContentHash, AcceptedAtUS: accepted.UnixMicro(), AnalysisStatus: "pending", JobIDs: jobs}
}

// HRReference is the user's effective HR reference (hr_reference/2) as shown in
// settings: the user's own entries, not model output.
type HRReference struct {
	ID            string   `json:"id"`
	MaxHR         float64  `json:"maxHR"`
	RestingHR     *float64 `json:"restingHR,omitempty"`
	Method        string   `json:"method"`
	EffectiveFrom string   `json:"effectiveFrom"`
	MeasuredAt    string   `json:"measuredAt"`
}

// ObservedMaxHR is a suggestion only: the highest 30 s median HR of the last
// year, shown for the user to confirm as an HR reference.
type ObservedMaxHR struct {
	Value float64 `json:"value"`
	Date  string  `json:"date"`
}
