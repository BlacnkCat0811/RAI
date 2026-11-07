package syncing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

const (
	// A bundle is deliberately larger than one legacy page: it is the whole
	// HealthKit projection for one Workout, uploaded once and never split by
	// the client. The limits bound a single request without turning the
	// protocol back into an unbounded generic JSON sink.
	MaxWorkoutBundleBytes  = 32 << 20
	MaxWorkoutBundleEvents = 50_000
	MaxBundleRoutes        = 16
	MaxBundleRoutePoints   = 1_000_000
	MaxBundleSeries        = 50_000
	MaxBundleSeriesPoints  = 1_000_000
)

// WorkoutBundle is the RQrun-style import unit: one Workout, all associated
// scalar samples, and all routes in one immutable JSON document. Membership in
// Samples is the association; there is no separate per-sample request.
type WorkoutBundle struct {
	ProtocolVersion int `json:"protocolVersion"`
	// MetadataRegistryVersion declares which Workout metadata registry produced
	// this payload. Absent means registry version 1 (the original nine-key
	// allowlist). A server that does not know the declared version rejects the
	// bundle; it never drops the metadata it cannot read.
	MetadataRegistryVersion *int                  `json:"metadataRegistryVersion,omitempty"`
	DatasetID               string                `json:"datasetID"`
	DatasetEpoch            string                `json:"datasetEpoch"`
	CollectorEpoch          string                `json:"collectorEpoch"`
	WorkoutID               string                `json:"workoutID"`
	Workout                 Event                 `json:"workout"`
	Samples                 []Event               `json:"samples"`
	Series                  []WorkoutBundleSeries `json:"series,omitempty"`
	Routes                  []WorkoutBundleRoute  `json:"routes,omitempty"`
	// CompleteSampleTypes describes the client's read coverage only. Snapshot
	// absence, even after a successful read, is never a deletion event.
	CompleteSampleTypes []string `json:"completeSampleTypes,omitempty"`
	// CompleteRoutes describes route read coverage, never deletion authority.
	CompleteRoutes  bool               `json:"completeRoutes,omitempty"`
	DeletionOnly    bool               `json:"deletionOnly,omitempty"`
	Observation     *BundleObservation `json:"observation,omitempty"`
	SampleDeletions []SampleDeletion   `json:"sampleDeletions,omitempty"`
	RouteDeletions  []string           `json:"routeDeletions,omitempty"`
}

// WorkoutBundleSeries carries the child points of a multi-point quantity
// sample. The parent sample remains in Samples; Series never invents a new
// sample identity.
type WorkoutBundleSeries struct {
	SampleType string        `json:"sampleType"`
	SampleUUID string        `json:"sampleUUID"`
	Points     []SeriesPoint `json:"points"`
}

type WorkoutBundleRoute struct {
	RouteID string               `json:"routeID"`
	Source  Source               `json:"source"`
	Points  []WorkoutBundlePoint `json:"points"`
}

// WorkoutBundlePoint mirrors RoutePoint without a sequence. Sequence is
// derived from array order by the server, so the client cannot create gaps or
// duplicate positions while preserving source order.
type WorkoutBundlePoint struct {
	RecordedAtUs       int64    `json:"recordedAtUs"`
	Latitude           float64  `json:"latitude"`
	Longitude          float64  `json:"longitude"`
	Altitude           *float64 `json:"altitude,omitempty"`
	HorizontalAccuracy *float64 `json:"horizontalAccuracy,omitempty"`
	VerticalAccuracy   *float64 `json:"verticalAccuracy,omitempty"`
	Speed              *float64 `json:"speed,omitempty"`
	Course             *float64 `json:"course,omitempty"`
}

func (p WorkoutBundlePoint) ToRoutePoint(sequence int64) RoutePoint {
	return RoutePoint{Sequence: sequence, RecordedAtUs: p.RecordedAtUs, Latitude: p.Latitude, Longitude: p.Longitude,
		Altitude: p.Altitude, HorizontalAccuracy: p.HorizontalAccuracy, VerticalAccuracy: p.VerticalAccuracy,
		Speed: p.Speed, Course: p.Course}
}

func (b WorkoutBundle) Validate() error {
	bad := func() error { return Fail(422, "unsupported_payload") }
	if (b.ProtocolVersion != 3 && b.ProtocolVersion != 4) || !ValidID(b.DatasetID) || !ValidID(b.DatasetEpoch) ||
		!ValidID(b.CollectorEpoch) || !ValidID(b.WorkoutID) || !ValidID(b.Workout.UUID) ||
		b.Workout.UUID != b.WorkoutID || len(b.Samples) > MaxWorkoutBundleEvents ||
		len(b.Routes) > MaxBundleRoutes {
		return bad()
	}
	if err := b.validateMetadataRegistryVersion(); err != nil {
		return err
	}
	if err := b.Workout.Validate(WorkoutType); err != nil {
		return bundleValidationContext(err, "workout", "invalid_workout")
	}
	if err := ValidateDetailBudget([]Event{b.Workout}); err != nil {
		return Fail(413, "bundle_too_large")
	}
	if b.Workout.Op != "upsert" || b.Workout.Kind != "workout" || b.Workout.Type != WorkoutType ||
		b.Workout.Workout == nil {
		return bad()
	}
	seen := make(map[string]bool, len(b.Samples))
	for _, sample := range b.Samples {
		if sample.Op != "upsert" || sample.Kind != "sample" || sample.Type == WorkoutType ||
			!ValidID(sample.UUID) || sample.Source == nil {
			return bad()
		}
		if err := sample.Validate(sample.Type); err != nil {
			return err
		}
		key := sample.Type + ":" + sample.UUID
		if seen[key] {
			return bad()
		}
		seen[key] = true
	}
	samplesByKey := make(map[string]Event, len(b.Samples))
	for _, sample := range b.Samples {
		samplesByKey[sample.Type+":"+sample.UUID] = sample
	}
	completeTypes := make(map[string]bool, len(b.CompleteSampleTypes))
	presentTypes := make(map[string]bool, len(b.Samples))
	for _, sample := range b.Samples {
		presentTypes[sample.Type] = true
	}
	for _, sampleType := range b.CompleteSampleTypes {
		if _, ok := Units[sampleType]; !ok || sampleType == WorkoutType || completeTypes[sampleType] || !presentTypes[sampleType] {
			return bad()
		}
		completeTypes[sampleType] = true
	}
	if len(b.Series) > MaxBundleSeries {
		return Fail(413, "bundle_too_large")
	}
	seriesSeen := make(map[string]bool, len(b.Series))
	seriesPoints := 0
	for _, series := range b.Series {
		parent, ok := samplesByKey[series.SampleType+":"+series.SampleUUID]
		if !ok || !ValidID(series.SampleUUID) || series.SampleType == WorkoutType || len(series.Points) == 0 {
			return bad()
		}
		key := series.SampleType + ":" + series.SampleUUID
		if seriesSeen[key] {
			return bad()
		}
		seriesSeen[key] = true
		seriesPoints += len(series.Points)
		if seriesPoints > MaxBundleSeriesPoints {
			return Fail(413, "bundle_too_large")
		}
		for _, point := range series.Points {
			if err := point.Validate(series.SampleType, *parent.StartUs, *parent.EndUs); err != nil {
				return err
			}
		}
	}
	routeIDs := make(map[string]bool, len(b.Routes))
	totalPoints := int64(0)
	for _, route := range b.Routes {
		if !ValidID(route.RouteID) || route.Source.BundleID == "" || len(route.Source.BundleID) > 255 ||
			len(route.Points) == 0 || routeIDs[route.RouteID] {
			return bad()
		}
		routeIDs[route.RouteID] = true
		totalPoints += int64(len(route.Points))
		if totalPoints > MaxBundleRoutePoints {
			return Fail(413, "bundle_too_large")
		}
		for _, point := range route.Points {
			if err := point.ToRoutePoint(0).Validate(); err != nil {
				return err
			}
		}
	}
	maxRouteDeletions := MaxBundleRoutes
	if b.ProtocolVersion == 4 {
		maxRouteDeletions = MaxV4RouteDeletions
	}
	if len(b.SampleDeletions) > MaxWorkoutBundleEvents || len(b.RouteDeletions) > maxRouteDeletions {
		return bad()
	}
	for _, d := range b.SampleDeletions {
		if _, ok := Units[d.SampleType]; !ok || d.SampleType == WorkoutType || !ValidID(d.SampleUUID) {
			return bad()
		}
		key := d.SampleType + ":" + d.SampleUUID
		if seen[key] {
			return bad()
		}
		seen[key] = true
	}
	for _, id := range b.RouteDeletions {
		if !ValidID(id) || routeIDs[id] {
			return bad()
		}
		routeIDs[id] = true
	}
	if b.DeletionOnly && (len(b.SampleDeletions)+len(b.RouteDeletions) == 0 || len(b.Samples)+len(b.Series)+len(b.Routes) != 0 || b.Observation != nil) {
		return bad()
	}
	return bundleValidationContext(b.validateObservation(), "observation", "invalid_observation")
}

// BundleDigest is the idempotency key for a complete uploaded document. The
// server hashes the exact multipart file bytes, not a re-encoded Go value.
func BundleDigest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// RejectNulls is used at the bundle boundary. Optional values are represented
// by omission; an explicit null would otherwise be indistinguishable from a
// missing source field after decoding.
func RejectNulls(raw []byte) error {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return Fail(422, "unsupported_payload")
	}
	if hasNull(value) {
		return Fail(422, "unsupported_payload")
	}
	return nil
}

func hasNull(value any) bool {
	switch v := value.(type) {
	case nil:
		return true
	case []any:
		for _, item := range v {
			if hasNull(item) {
				return true
			}
		}
	case map[string]any:
		for _, item := range v {
			if hasNull(item) {
				return true
			}
		}
	}
	return false
}

// SortedSampleTypes is a receipt convenience; it never changes payload order.
func (b WorkoutBundle) SortedSampleTypes() []string {
	seen := make(map[string]bool)
	for _, sample := range b.Samples {
		seen[sample.Type] = true
	}
	out := make([]string, 0, len(seen))
	for sampleType := range seen {
		out = append(out, sampleType)
	}
	sort.Strings(out)
	return out
}

type WorkoutImportCounts struct {
	WorkoutInserted       bool `json:"workoutInserted"`
	SamplesInserted       int  `json:"samplesInserted"`
	SamplesUnchanged      int  `json:"samplesUnchanged"`
	SamplesDeleted        int  `json:"samplesDeleted"`
	AssociationsInserted  int  `json:"associationsInserted"`
	AssociationsUnchanged int  `json:"associationsUnchanged"`
	AssociationsDeleted   int  `json:"associationsDeleted"`
	SeriesInserted        int  `json:"seriesInserted"`
	SeriesUnchanged       int  `json:"seriesUnchanged"`
	RoutesInserted        int  `json:"routesInserted"`
	RoutesUnchanged       int  `json:"routesUnchanged"`
	RoutesDeleted         int  `json:"routesDeleted"`
}

type WorkoutImportReceipt struct {
	WorkoutID            string                    `json:"workoutID"`
	BundleSHA256         string                    `json:"bundleSHA256"`
	IngestStatus         string                    `json:"ingestStatus"`
	CommitRevision       int64                     `json:"commitRevision"`
	Counts               WorkoutImportCounts       `json:"counts"`
	AnalysisStatus       string                    `json:"analysisStatus"`
	AnalysisJobIDs       []string                  `json:"analysisJobIDs"`
	RouteDiagnostics     map[string][]string       `json:"routeDiagnostics,omitempty"`
	SourceClassification string                    `json:"sourceClassification,omitempty"`
	SourceObservation    *BundleObservationSummary `json:"sourceObservation,omitempty"`
}

// RouteDiagnostics records source time anomalies without changing any point.
// Query/transport completeness is independent of temporal quality.
func (b WorkoutBundle) RouteTimeDiagnostics() map[string][]string {
	result := map[string][]string{}
	for _, r := range b.Routes {
		seen := map[string]bool{}
		for i, p := range r.Points {
			if p.RecordedAtUs < *b.Workout.StartUs {
				seen["before_workout"] = true
			}
			if p.RecordedAtUs > *b.Workout.EndUs {
				seen["after_workout"] = true
			}
			if i > 0 && p.RecordedAtUs == r.Points[i-1].RecordedAtUs {
				seen["timestamp_equal"] = true
			}
			if i > 0 && p.RecordedAtUs < r.Points[i-1].RecordedAtUs {
				seen["timestamp_regression"] = true
			}
		}
		if len(seen) > 0 {
			codes := []string{}
			for code := range seen {
				codes = append(codes, code)
			}
			sort.Strings(codes)
			result[r.RouteID] = codes
		}
	}
	return result
}

func bundleValidationContext(err error, field, reason string) error {
	if e, ok := err.(*Error); ok && e.Field == "" {
		e.Field = field
		e.Reason = reason
	}
	return err
}
