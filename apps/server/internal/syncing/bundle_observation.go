package syncing

import (
	"bytes"
	"encoding/json"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

// HealthKitSource contains allowlisted source descriptors, never localIdentifier,
// UDI, serial number or arbitrary metadata. Classification is computed by RAI.
type HealthKitSource struct {
	BundleID               string           `json:"bundleID"`
	Name                   string           `json:"name,omitempty"`
	Version                string           `json:"version,omitempty"`
	ProductType            string           `json:"productType,omitempty"`
	OperatingSystemVersion string           `json:"operatingSystemVersion,omitempty"`
	Device                 *HealthKitDevice `json:"device,omitempty"`
}
type HealthKitDevice struct {
	Manufacturer    string `json:"manufacturer,omitempty"`
	Model           string `json:"model,omitempty"`
	HardwareVersion string `json:"hardwareVersion,omitempty"`
	FirmwareVersion string `json:"firmwareVersion,omitempty"`
	SoftwareVersion string `json:"softwareVersion,omitempty"`
}
type SampleProvenance struct {
	SampleType string          `json:"sampleType"`
	SampleUUID string          `json:"sampleUUID"`
	Source     HealthKitSource `json:"source"`
}
type RouteProvenance struct {
	RouteID string          `json:"routeID"`
	Source  HealthKitSource `json:"source"`
}
type BundleProvenance struct {
	Workout HealthKitSource    `json:"workout"`
	Samples []SampleProvenance `json:"samples"`
	Routes  []RouteProvenance  `json:"routes"`
}
type BundleCoverage struct {
	State          string   `json:"state"`
	SampleCount    int      `json:"sampleCount"`
	QueryCompleted bool     `json:"queryCompleted"`
	LocalPersisted bool     `json:"localPersisted"`
	ExpectedCount  *int     `json:"expectedCount,omitempty"`
	ErrorCode      string   `json:"errorCode,omitempty"`
	Diagnostics    []string `json:"diagnostics,omitempty"`
}

// Observation time belongs to RAI, descriptors and metadata come from HealthKit.
// History is append-only in workout_imports; it never rewrites original payloads.
type BundleObservation struct {
	Version      int                       `json:"version"`
	ObservedAtUs int64                     `json:"observedAtUs"`
	Metadata     map[string]any            `json:"metadata,omitempty"`
	Provenance   BundleProvenance          `json:"provenance"`
	Coverage     map[string]BundleCoverage `json:"coverage"`
}
type SampleDeletion struct {
	SampleType string `json:"sampleType"`
	SampleUUID string `json:"sampleUUID"`
}

var watchProduct = regexp.MustCompile(`^Watch[0-9]+,[0-9]+$`)

// Classification is the frozen RAI source classification. UA1-R3B adds exactly
// one deterministic class, user_entered, and no new eligible class: an empty
// descriptor field means "HealthKit did not report it", and the Swift client
// applies the same reading. Shared golden vectors live in
// contracts/fixtures/workout-source-policy-v1.json.
func (s HealthKitSource) Classification(parent *HealthKitSource) string {
	if s.BundleID == "" {
		return "source_unknown"
	}
	// Data entered by hand in the Health app reports no product type and no
	// device. It is a deterministic exclusion, never an unresolved source.
	if s.BundleID == "com.apple.Health" && s.ProductType == "" && (s.Device == nil || *s.Device == HealthKitDevice{}) {
		return "user_entered"
	}
	const prefix = "com.apple.health."
	scoped := strings.HasPrefix(s.BundleID, prefix) && ValidID(strings.ToLower(strings.TrimPrefix(s.BundleID, prefix)))
	if s.BundleID != "com.apple.health" && s.BundleID != "com.apple.Workout" && !scoped {
		if strings.HasPrefix(s.BundleID, "com.apple.") {
			return "source_unknown"
		}
		return "third_party"
	}
	parentMatches := parent != nil && parent.Classification(nil) == "apple_watch" && s.BundleID == parent.BundleID && s.Version != "" && s.Version == parent.Version
	if s.ProductType != "" && !watchProduct.MatchString(s.ProductType) {
		// A paired iPhone can carry the child samples of a Watch workout. The
		// parent association and the device-scoped bundle identity are stronger
		// evidence than the productType label on the child.
		if parentMatches {
			return "associated_apple_watch"
		}
		return "source_unknown"
	}
	if s.Device != nil {
		if s.Device.Manufacturer != "" && s.Device.Manufacturer != "Apple" && s.Device.Manufacturer != "Apple Inc." {
			return "source_unknown"
		}
		if s.Device.Model != "" && !strings.Contains(strings.ToLower(s.Device.Model), "watch") {
			return "source_unknown"
		}
	}
	if s.ProductType != "" && (!scoped || s.Device != nil && s.Device.Manufacturer != "" && s.Device.Model != "") {
		return "apple_watch"
	}
	if parentMatches {
		return "associated_apple_watch"
	}
	return "source_unknown"
}
func (s HealthKitSource) Validate() error {
	if len(s.BundleID) == 0 || len(s.BundleID) > 255 {
		return Fail(422, "unsupported_payload")
	}
	values := []string{s.Name, s.Version, s.ProductType, s.OperatingSystemVersion}
	if s.Device != nil {
		values = append(values, s.Device.Manufacturer, s.Device.Model, s.Device.HardwareVersion, s.Device.FirmwareVersion, s.Device.SoftwareVersion)
	}
	for _, value := range values {
		if len(value) > 255 {
			return Fail(422, "unsupported_payload")
		}
	}
	return nil
}

var coverageStates = map[string]bool{"not_queried": true, "observed": true, "no_records_observed": true, "partial": true, "complete": true, "failed": true, "unsupported": true, "unknown": true}
var metadataKeys = map[string]bool{"HKTimeZone": true, "HKIndoorWorkout": true, "HKAverageMETs": true, "HKElevationAscended": true, "HKElevationDescended": true, "HKWeatherHumidity": true, "HKWeatherTemperature": true, "HKAverageSpeed": true, "HKMaximumSpeed": true}

// Workout metadata registry. Version 1 is the original nine-key HealthKit
// allowlist above. Version 2 (UA1-R3A) adds two real WorkoutActivity metadata
// keys that Apple does not publish in the iPhoneOS SDK: their semantics are
// undocumented, so they are preserved exactly and never interpreted. Nothing
// is derived from them, and they are accepted on WorkoutActivity metadata only.
const (
	WorkoutMetadataRegistryVersion = 2
	// MetadataRegistryVersionV1 is the meaning of an absent declaration:
	// clients that predate the field only ever produced version 1 payloads.
	MetadataRegistryVersionV1 = 1
	// WorkoutMetadataRegistryV1MaxKeys bounds one metadata object at every
	// scope; it is unchanged by the version 2 expansion.
	WorkoutMetadataRegistryV1MaxKeys = 64
	metadataTextMaxBytes             = 64
)

type metadataScope int

const (
	scopeObservation metadataScope = iota
	scopeWorkout
	scopeActivity
	scopeActivityEvent
	scopeWorkoutEvent
)

var opaqueActivityTextKeys = map[string]bool{"WOIntervalStepKeyPath": true}
var opaqueActivityIntegerKeys = map[string]bool{"WOIntervalStepSuccessful": true}

// A key accepted at the wrong scope is a contract violation, never a silently
// dropped value: the Workout-level, Activity-level and event-level allowlists
// stay separate so that a later scope change cannot masquerade as compatible.
func metadataScopeAllowsOpaque(scope metadataScope) bool { return scope == scopeActivity }

func opaqueActivityMetadataPresent(metadata map[string]any) bool {
	for key := range metadata {
		if opaqueActivityTextKeys[key] || opaqueActivityIntegerKeys[key] {
			return true
		}
	}
	return false
}

// metadataInteger accepts only an exact signed 64-bit integer. A boolean,
// fractional, exponent-bearing or out-of-range number is rejected instead of
// being truncated or coerced into a different semantic type.
func metadataInteger(value any) (int64, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) || typed != math.Trunc(typed) ||
			typed < -9.007199254740992e15 || typed > 9.007199254740992e15 {
			return 0, false
		}
		return int64(typed), true
	}
	return 0, false
}

// validateMetadataRegistryVersion enforces that a bundle may carry only the
// keys its declared registry version supports. An absent field means version 1.
func (b WorkoutBundle) validateMetadataRegistryVersion() error {
	if b.ProtocolVersion == 4 {
		// Protocol 4 has no metadata registry: every unknown key is preserved
		// verbatim instead of being allowlisted by version.
		return nil
	}
	version := MetadataRegistryVersionV1
	if b.MetadataRegistryVersion != nil {
		version = *b.MetadataRegistryVersion
	}
	if version < MetadataRegistryVersionV1 || version > WorkoutMetadataRegistryVersion {
		return Fail(422, "unsupported_payload")
	}
	if version < 2 && opaqueActivityMetadataPresent(b.Workout.Metadata) {
		return Fail(422, "unsupported_payload")
	}
	if b.Workout.Workout != nil {
		for _, activity := range b.Workout.Workout.Activities {
			if version < 2 && opaqueActivityMetadataPresent(activity.Metadata) {
				return Fail(422, "unsupported_payload")
			}
		}
	}
	return nil
}

// Metadata is validated per scope: a key valid at one scope is never silently
// accepted at another. Like the version 1 contract, validation runs with the
// observation record, which every supported client supplies.
func (b WorkoutBundle) validateMetadata() error {
	if err := validateHealthMetadata(scopeObservation, b.Observation.Metadata); err != nil {
		return err
	}
	if err := validateHealthMetadata(scopeWorkout, b.Workout.Metadata); err != nil {
		return err
	}
	for _, sample := range b.Samples {
		if err := validateSampleMetadata(sample.Metadata); err != nil {
			return err
		}
	}
	if b.Workout.Workout == nil {
		return nil
	}
	for _, activity := range b.Workout.Workout.Activities {
		if err := validateHealthMetadata(scopeActivity, activity.Metadata); err != nil {
			return err
		}
		for _, event := range activity.Events {
			if err := validateHealthMetadata(scopeActivityEvent, event.Metadata); err != nil {
				return err
			}
		}
	}
	for _, event := range b.Workout.Workout.Events {
		if err := validateHealthMetadata(scopeWorkoutEvent, event.Metadata); err != nil {
			return err
		}
	}
	return nil
}

func (b WorkoutBundle) validateObservation() error {
	o := b.Observation
	if o == nil {
		return nil
	}
	if o.Version != 1 || o.ObservedAtUs < -62135596800000000 || o.ObservedAtUs > 253402300799999999 || len(o.Metadata) > 64 || len(o.Coverage) > 128 {
		return Fail(422, "unsupported_payload")
	}
	if err := b.validateMetadata(); err != nil {
		return err
	}
	raw, err := json.Marshal(o.Metadata)
	if err != nil || len(raw) > 16384 {
		return Fail(422, "unsupported_payload")
	}
	counts := map[string]int{"workout": 1}
	for _, sample := range b.Samples {
		counts[sample.Type]++
	}
	for _, series := range b.Series {
		counts["series:"+series.SampleType] += len(series.Points)
	}
	for _, route := range b.Routes {
		counts["route"] += len(route.Points)
		counts["route:"+route.RouteID] = len(route.Points)
	}
	for key, c := range o.Coverage {
		if len(key) > 255 || !coverageStates[c.State] || c.SampleCount < 0 || c.SampleCount > MaxBundleRoutePoints || len(c.ErrorCode) > 128 || len(c.Diagnostics) > 16 || (c.ExpectedCount != nil && *c.ExpectedCount < 0) {
			return Fail(422, "unsupported_payload")
		}
		if c.State == "complete" && (!c.QueryCompleted || !c.LocalPersisted || c.SampleCount == 0 || c.ExpectedCount != nil && *c.ExpectedCount != c.SampleCount) {
			return Fail(422, "unsupported_payload")
		}
		if c.State == "complete" || c.State == "no_records_observed" {
			_, supported := Units[key]
			if supported || key == "workout" || key == "route" || strings.HasPrefix(key, "route:") || strings.HasPrefix(key, "series:") {
				if counts[key] != c.SampleCount {
					return Fail(422, "unsupported_payload")
				}
			}
		}
		if c.State == "no_records_observed" && (!c.QueryCompleted || c.SampleCount != 0) {
			return Fail(422, "unsupported_payload")
		}
		for _, code := range c.Diagnostics {
			if len(code) > 128 {
				return Fail(422, "unsupported_payload")
			}
		}
	}
	p := o.Provenance
	if err := p.Workout.Validate(); err != nil {
		return err
	}
	if p.Workout.BundleID != b.Workout.Source.BundleID || p.Workout.Classification(nil) != "apple_watch" {
		return Fail(409, "source_policy_pending")
	}
	if len(p.Samples) != len(b.Samples) || len(p.Routes) != len(b.Routes) {
		return Fail(422, "unsupported_payload")
	}
	samples := map[string]string{}
	for _, e := range b.Samples {
		samples[e.Type+":"+e.UUID] = e.Source.BundleID
	}
	for _, s := range p.Samples {
		key := s.SampleType + ":" + s.SampleUUID
		if samples[key] != s.Source.BundleID || samples[key] == "" {
			return Fail(422, "unsupported_payload")
		}
		delete(samples, key)
		if err := s.Source.Validate(); err != nil {
			return err
		}
		c := s.Source.Classification(&p.Workout)
		if c != "apple_watch" && c != "associated_apple_watch" {
			return Fail(409, "source_policy_pending")
		}
	}
	// A duplicate provenance row can preserve the arrays' lengths while leaving
	// another uploaded sample unattributed. Every member must be consumed once.
	if len(samples) != 0 {
		return Fail(422, "unsupported_payload")
	}
	routes := map[string]string{}
	for _, r := range b.Routes {
		routes[r.RouteID] = r.Source.BundleID
	}
	for _, r := range p.Routes {
		if routes[r.RouteID] != r.Source.BundleID || routes[r.RouteID] == "" {
			return Fail(422, "unsupported_payload")
		}
		delete(routes, r.RouteID)
		if err := r.Source.Validate(); err != nil {
			return err
		}
		c := r.Source.Classification(&p.Workout)
		if c != "apple_watch" && c != "associated_apple_watch" {
			return Fail(409, "source_policy_pending")
		}
	}
	if len(routes) != 0 {
		return Fail(422, "unsupported_payload")
	}
	return nil
}

// Metadata is the only mutable observation of a Workout identity. All other
// core fields still must match. The original canonical payload/hash stay intact.
func WorkoutCoreEqual(a, b Event) bool {
	strip := func(e Event) []byte {
		raw, _ := json.Marshal(e)
		var value any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		_ = decoder.Decode(&value)
		var walk func(any)
		walk = func(v any) {
			switch x := v.(type) {
			case map[string]any:
				delete(x, "metadata")
				for _, child := range x {
					walk(child)
				}
			case []any:
				for _, child := range x {
					walk(child)
				}
			}
		}
		walk(value)
		raw, _ = json.Marshal(value)
		return raw
	}
	return string(strip(a)) == string(strip(b))
}

type BundleObservationSummary struct {
	Version       int                       `json:"version"`
	ObservedAtUs  int64                     `json:"observedAtUs"`
	SHA256        string                    `json:"sha256"`
	WorkoutSource HealthKitSource           `json:"workoutSource"`
	Metadata      map[string]any            `json:"metadata,omitempty"`
	Coverage      map[string]BundleCoverage `json:"coverage"`
}

func (b WorkoutBundle) ObservationSummary() *BundleObservationSummary {
	if b.Observation == nil {
		return nil
	}
	raw, _ := json.Marshal(b.Observation)
	return &BundleObservationSummary{Version: 1, ObservedAtUs: b.Observation.ObservedAtUs, SHA256: BundleDigest(raw), WorkoutSource: b.Observation.Provenance.Workout, Metadata: b.Observation.Metadata, Coverage: b.Observation.Coverage}
}

func validateSampleMetadata(metadata map[string]any) error {
	for key, value := range metadata {
		switch key {
		case "HKTimeZone":
			text, ok := value.(string)
			if !ok || len(text) == 0 || len(text) > 128 {
				return Fail(422, "unsupported_payload")
			}
		case "HKIndoorWorkout":
			if _, ok := value.(bool); !ok {
				return Fail(422, "unsupported_payload")
			}
		case "HKMetadataKeyHeartRateMotionContext", "HKMetadataKeyHeartRateSensorLocation", "HKMetadataKeyUserMotionContext", "HKMetadataKeyDevicePlacementSide":
			var number float64
			switch typed := value.(type) {
			case float64:
				number = typed
			case json.Number:
				parsed, err := typed.Float64()
				if err != nil {
					return Fail(422, "unsupported_payload")
				}
				number = parsed
			case int:
				number = float64(typed)
			case int64:
				number = float64(typed)
			default:
				return Fail(422, "unsupported_payload")
			}
			if math.IsNaN(number) || math.IsInf(number, 0) || number != math.Trunc(number) || number < 0 || number > 10 {
				return Fail(422, "unsupported_payload")
			}
		case "HKMetadataKeySyncIdentifier":
			// HealthKit sync bookkeeping, preserved exactly. It never replaces
			// the sample UUID and is not a download watermark.
			text, ok := value.(string)
			if !ok || len(text) == 0 || len(text) > 255 || !utf8.ValidString(text) {
				return Fail(422, "unsupported_payload")
			}
		case "HKMetadataKeySyncVersion":
			if _, ok := metadataInteger(value); !ok {
				return Fail(422, "unsupported_payload")
			}
		default:
			return Fail(422, "unsupported_payload")
		}
	}
	return nil
}

func validateHealthMetadata(scope metadataScope, metadata map[string]any) error {
	units := map[string]string{"HKAverageMETs": "kcal/(kg*hr)", "HKElevationAscended": "m", "HKElevationDescended": "m", "HKWeatherHumidity": "1", "HKWeatherTemperature": "degC", "HKAverageSpeed": "m/s", "HKMaximumSpeed": "m/s"}
	for key, value := range metadata {
		if opaqueActivityTextKeys[key] {
			if !metadataScopeAllowsOpaque(scope) {
				return Fail(422, "unsupported_payload")
			}
			// Exact string preserved: no trimming, no case folding, no
			// Unicode normalization, no interpretation of the content.
			text, ok := value.(string)
			if !ok || len(text) == 0 || len(text) > metadataTextMaxBytes || !utf8.ValidString(text) {
				return Fail(422, "unsupported_payload")
			}
			for _, r := range text {
				if r < 0x20 || r == 0x7f {
					return Fail(422, "unsupported_payload")
				}
			}
			continue
		}
		if opaqueActivityIntegerKeys[key] {
			if !metadataScopeAllowsOpaque(scope) {
				return Fail(422, "unsupported_payload")
			}
			if _, ok := metadataInteger(value); !ok {
				return Fail(422, "unsupported_payload")
			}
			continue
		}
		if !metadataKeys[key] {
			return Fail(422, "unsupported_payload")
		}
		if key == "HKTimeZone" {
			v, ok := value.(string)
			if !ok || len(v) == 0 || len(v) > 128 {
				return Fail(422, "unsupported_payload")
			}
			continue
		}
		if key == "HKIndoorWorkout" {
			if _, ok := value.(bool); !ok {
				return Fail(422, "unsupported_payload")
			}
			continue
		}
		quantity, ok := value.(map[string]any)
		if !ok || len(quantity) != 2 || quantity["unit"] != units[key] {
			return Fail(422, "unsupported_payload")
		}
		var number float64
		switch value := quantity["value"].(type) {
		case float64:
			number = value
		case json.Number:
			var err error
			number, err = value.Float64()
			if err != nil {
				return Fail(422, "unsupported_payload")
			}
		default:
			return Fail(422, "unsupported_payload")
		}
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return Fail(422, "unsupported_payload")
		}
	}
	return nil
}
