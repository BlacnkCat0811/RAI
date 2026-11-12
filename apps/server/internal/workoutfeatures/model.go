// Package workoutfeatures implements the candidate-only, deterministic Workout
// Feature Foundation. It has no clock, I/O, global mutable policy or score model.
package workoutfeatures

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"rai/server/internal/workoutdomain"
)

const (
	Algorithm        = "rai.workout_features"
	Version          = "v1"
	Schema           = "rai.workout_features/1"
	InputVersion     = "rai.raw_workout_snapshot/1"
	JobKind          = "analysis.workout_features"
	ArtifactKey      = "workout_features"
	MaxPoints        = 250000
	MaxDetails       = 2000
	MaxMetadataBytes = 4 << 20
	MaxArtifactBytes = 1 << 20
	MaxSeconds       = 24 * 60 * 60
	HR               = "HKQuantityTypeIdentifierHeartRate"
	Speed            = "HKQuantityTypeIdentifierRunningSpeed"
	Steps            = "HKQuantityTypeIdentifierStepCount"
	Power            = "HKQuantityTypeIdentifierRunningPower"
	GCT              = "HKQuantityTypeIdentifierRunningGroundContactTime"
	Stride           = "HKQuantityTypeIdentifierRunningStrideLength"
	Vertical         = "HKQuantityTypeIdentifierRunningVerticalOscillation"
)

type State string

const (
	Present      State = "present"
	Missing      State = "missing"
	Deleted      State = "deleted"
	Ambiguous    State = "ambiguous"
	Condensed    State = "condensed"
	SeriesBacked State = "series_backed"
	Unsupported  State = "unsupported"
)

type TimeSupport string

const (
	InstantPoint        TimeSupport = "instant_point"
	IntervalObservation TimeSupport = "interval_observation"
	CondensedParent     TimeSupport = "condensed_parent_summary"
	SealedSeries        TimeSupport = "sealed_series"
)

type Reason string

const (
	MissingHR                    Reason = "missing_hr"
	HRCoverageLow                Reason = "hr_coverage_low"
	ActiveTimeUncertain          Reason = "active_time_uncertain"
	SpeedGap                     Reason = "speed_gap"
	RouteAccuracyLow             Reason = "route_accuracy_low"
	GradeUnknown                 Reason = "grade_unknown"
	DistanceMismatch             Reason = "distance_mismatch"
	PauseContaminated            Reason = "pause_contaminated"
	SourceConflict               Reason = "source_conflict"
	SourceUnknown                Reason = "source_unknown"
	DurationTooShort             Reason = "duration_too_short"
	EnvironmentUnknown           Reason = "environment_unknown"
	ParameterMissing             Reason = "parameter_missing"
	ParameterConflict            Reason = "parameter_conflict"
	WorkoutDeleted               Reason = "workout_deleted"
	InvalidObservation           Reason = "invalid_observation"
	SignalOutlier                Reason = "signal_outlier"
	InputLimit                   Reason = "input_limit"
	UnsupportedInput             Reason = "unsupported_input"
	EffortUnconfirmed            Reason = "effort_unconfirmed"
	CrossWorkoutEvidenceRequired Reason = "cross_workout_evidence_required"
	IndoorUnsupported            Reason = "indoor_unsupported"
	FutureModel                  Reason = "future_model"
	DistanceMissing              Reason = "distance_missing"
	SteadySupportLow             Reason = "steady_support_low"
)

type Error struct{ Code string }

func (e *Error) Error() string  { return e.Code }
func failure(code string) error { return &Error{code} }
func ErrorCode(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return "feature_invalid_input"
}
func finite(v float64) bool  { return workoutdomain.Finite(v) }
func ptr(v float64) *float64 { return &v }
func digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Policy is part of the immutable stack identity. Changes require a new stack.
type Policy struct {
	Version             string  `json:"version"`
	SourcePolicy        string  `json:"sourcePolicy"`
	MaxGapSeconds       int     `json:"maxGapSeconds"`
	HorizontalAccuracyM float64 `json:"horizontalAccuracyM"`
	VerticalAccuracyM   float64 `json:"verticalAccuracyM"`
	MaxSpeedMPS         float64 `json:"maxSpeedMPS"`
	HRSpikeBPM          float64 `json:"hrSpikeBPM"`
}

func DefaultPolicy() Policy {
	return Policy{"feature-policy/1", "reject_conflicting_sources/1", 15, 20, 10, 12, 220}
}
func (p Policy) Hash() string { return digest(p) }
func StackID() string         { return "features-v1-" + DefaultPolicy().Hash()[:12] }
func Scope(id string) string  { return "model/" + StackID() + "/workout/" + id }

type FactRef struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	Hash  string `json:"hash"`
	State State  `json:"state"`
}
type Source struct {
	Epoch          string          `json:"epoch"`
	BundleID       string          `json:"bundleID"`
	Classification string          `json:"classification"`
	Descriptor     json.RawMessage `json:"descriptor,omitempty"`
}
type Quantity struct {
	Category *int            `json:"category,omitempty"`
	Fact     FactRef         `json:"fact"`
	Type     string          `json:"type"`
	Start    int64           `json:"startUs"`
	End      int64           `json:"endUs"`
	Value    float64         `json:"value"`
	Unit     string          `json:"unit"`
	Source   string          `json:"sourceEpoch"`
	Support  TimeSupport     `json:"support"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}
type Series struct {
	Fact     FactRef    `json:"fact"`
	ParentID string     `json:"parentID"`
	Type     string     `json:"type"`
	Source   string     `json:"sourceEpoch"`
	Points   []Quantity `json:"points"`
}
type Interval struct {
	Start int64 `json:"startUs"`
	End   int64 `json:"endUs"`
}
type Event struct {
	Type     int64           `json:"type"`
	Start    int64           `json:"startUs"`
	End      int64           `json:"endUs"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}
type Activity struct {
	ID            string          `json:"id"`
	Start         int64           `json:"startUs"`
	End           *int64          `json:"endUs"`
	DurationUs    int64           `json:"durationUs"`
	Configuration json.RawMessage `json:"configuration"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
	Events        []Event         `json:"events"`
	Statistics    []Statistic     `json:"statistics"`
}
type Statistic struct {
	Unit    string   `json:"unit"`
	Type    string   `json:"type"`
	Start   int64    `json:"startUs"`
	End     int64    `json:"endUs"`
	Sum     *float64 `json:"sum"`
	Average *float64 `json:"average"`
	Maximum *float64 `json:"maximum"`
	Minimum *float64 `json:"minimum"`
}
type RoutePoint struct {
	Sequence           int64    `json:"sequence"`
	At                 int64    `json:"atUs"`
	Latitude           float64  `json:"latitude"`
	Longitude          float64  `json:"longitude"`
	Altitude           *float64 `json:"altitude"`
	HorizontalAccuracy *float64 `json:"horizontalAccuracy"`
	VerticalAccuracy   *float64 `json:"verticalAccuracy"`
	Speed              *float64 `json:"speed"`
	Course             *float64 `json:"course"`
}
type Route struct {
	Fact   FactRef      `json:"fact"`
	Source string       `json:"sourceEpoch"`
	Points []RoutePoint `json:"points"`
}
type Parameter struct {
	ID               string  `json:"id"`
	Hash             string  `json:"hash"`
	EffectiveFrom    int64   `json:"effectiveFromUs"`
	EffectiveThrough *int64  `json:"effectiveThroughUs"`
	Supersedes       string  `json:"supersedes,omitempty"`
	RecordedAt       int64   `json:"recordedAtUs"`
	Revoked          bool    `json:"revoked"`
	RestHR           float64 `json:"restingHR"`
	MaxHR            float64 `json:"maxHR"`
	Provenance       string  `json:"provenance"`
}
type Enrichment struct {
	Fact           FactRef         `json:"fact"`
	SourceBundleID string          `json:"sourceBundleID"`
	SourceVersion  int64           `json:"sourceVersion"`
	ObservedAt     int64           `json:"observedAtUs"`
	Distance       *float64        `json:"distanceM"`
	EnergyKcal     *float64        `json:"energyKcal"`
	RouteAvailable *bool           `json:"routeAvailable"`
	Metadata       json.RawMessage `json:"metadata"`
}
type RawWorkoutSnapshot struct {
	EnergyKcal         *float64         `json:"energyKcal"`
	WorkoutSourceEpoch string           `json:"workoutSourceEpoch"`
	Enrichments        []Enrichment     `json:"enrichments"`
	DatasetID          string           `json:"datasetID"`
	WorkoutID          string           `json:"workoutID"`
	Generation         int64            `json:"generation"`
	Revision           int64            `json:"revision"`
	AsOf               int64            `json:"asOfUs"`
	Policy             Policy           `json:"policy"`
	AlgorithmVersion   string           `json:"algorithmVersion"`
	Fact               FactRef          `json:"fact"`
	Start              int64            `json:"startUs"`
	End                int64            `json:"endUs"`
	DurationUs         int64            `json:"sourceDurationUs"`
	Distance           *float64         `json:"distanceM"`
	ActivityType       int64            `json:"activityType"`
	Quantities         []Quantity       `json:"quantities"`
	Statistics         []Statistic      `json:"statistics"`
	Series             []Series         `json:"series"`
	Routes             []Route          `json:"routes"`
	Events             []Event          `json:"events"`
	Activities         []Activity       `json:"activities"`
	Metadata           json.RawMessage  `json:"metadata"`
	Provenance         []Source         `json:"provenance"`
	Parameters         []Parameter      `json:"parameters"`
	References         []FactRef        `json:"references"`
	Availability       map[string]State `json:"availability"`
}
type Qualification struct {
	Status  string   `json:"status"`
	Reasons []Reason `json:"reasonCodes"`
}
type Qualifications struct {
	Load        Qualification `json:"load"`
	VO2         Qualification `json:"vo2"`
	Performance Qualification `json:"performance"`
	CS          Qualification `json:"cs"`
	Endurance   Qualification `json:"endurance"`
	// PerformanceGAP admits hilly routes through grade adjustment instead of
	// requiring 90% flat distance (v2 running ability).
	PerformanceGAP *Qualification `json:"performanceGradeAdjusted,omitempty"`
}
type Timing struct {
	Start             int64    `json:"startUs"`
	End               int64    `json:"endUs"`
	Elapsed           float64  `json:"elapsedSeconds"`
	SourceDuration    float64  `json:"sourceDurationSeconds"`
	Active            *float64 `json:"activeSeconds"`
	Paused            *float64 `json:"pausedSeconds"`
	LongestContinuous float64  `json:"longestContinuousActiveSeconds"`
	State             State    `json:"state"`
}
type SignalSummary struct {
	SourceResolution      string   `json:"sourceResolution"`
	State                 State    `json:"state"`
	Mean                  *float64 `json:"mean"`
	RobustMax             *float64 `json:"robustMax30sMedian"`
	ObservedCoverage      float64  `json:"observedCoverage"`
	ReconstructedCoverage float64  `json:"reconstructedCoverage"`
	InterpolatedFraction  float64  `json:"interpolatedFraction"`
	MaxGap                float64  `json:"maxGapSeconds"`
	ExcludedSeconds       float64  `json:"excludedSeconds"`
	AmbiguousSeconds      float64  `json:"ambiguousSeconds"`
	SourceEpochs          []string `json:"sourceEpochs"`
	Reasons               []Reason `json:"reasonCodes"`
}
type DistanceChoice struct {
	RouteDifferenceRatio *float64 `json:"routeDifferenceRatio"`
	Preferred            *float64 `json:"preferredDistanceM"`
	SelectedSource       string   `json:"selectedSource"`
	CrossCheck           *float64 `json:"crossCheckDistanceM"`
	CrossCheckSource     string   `json:"crossCheckSource"`
	DifferenceRatio      *float64 `json:"differenceRatio"`
	Reason               string   `json:"reason"`
	SpeedIntegral        *float64 `json:"speedIntegralM"`
	RouteDistance        *float64 `json:"routeDistanceM"`
}
type RouteSummary struct {
	State                State    `json:"state"`
	Distance             *float64 `json:"distanceM"`
	Coverage             float64  `json:"coverage"`
	GradeCoverage        float64  `json:"gradeCoverage"`
	FlatDistanceFraction *float64 `json:"flatDistanceFraction"`
	RejectedEdges        int      `json:"rejectedEdges"`
	SourceEpochs         []string `json:"sourceEpochs"`
	Reasons              []Reason `json:"reasonCodes"`
}
type Window struct {
	Start         int64         `json:"startUs"`
	End           int64         `json:"endUs"`
	Duration      int           `json:"durationSeconds"`
	Distance      float64       `json:"distanceM"`
	MeanSpeed     float64       `json:"meanSpeedMPS"`
	Coverage      float64       `json:"coverage"`
	Quality       string        `json:"quality"`
	SourceEpoch   string        `json:"sourceEpoch"`
	Evidence      string        `json:"evidence"`
	Qualification Qualification `json:"qualification"`
	// Grade-adjusted (Minetti) flat-equivalent distance and speed; set only on
	// WindowCandidates.GAP windows.
	GAPDistance *float64 `json:"gradeAdjustedDistanceM,omitempty"`
	GAPSpeed    *float64 `json:"gradeAdjustedSpeedMPS,omitempty"`
}
type WindowCandidates struct {
	Duration int     `json:"durationSeconds"`
	Best     *Window `json:"best"`
	Backup   *Window `json:"nonOverlapBackup"`
	// GAP is the best grade-adjusted window (durations >= 20 min).
	GAP *Window `json:"gradeAdjustedBest,omitempty"`
}
type SteadySegment struct {
	Window             Window        `json:"window"`
	HR                 *float64      `json:"meanHR"`
	SpeedCV            float64       `json:"speedCV"`
	HRTrend            *float64      `json:"hrTrendBPMPerMinute"`
	HRCoverage         float64       `json:"hrCoverage"`
	Grade              *float64      `json:"grade"`
	GradeCoverage      float64       `json:"gradeCoverage"`
	PauseContamination float64       `json:"pauseContamination"`
	Environment        Environment   `json:"environment"`
	Qualification      Qualification `json:"qualification"`
}
type Half struct {
	GradeCoverage        float64  `json:"gradeCoverage"`
	FlatDistanceFraction float64  `json:"flatDistanceFraction"`
	SourceEpoch          string   `json:"sourceEpoch"`
	Start                int64    `json:"startUs"`
	End                  int64    `json:"endUs"`
	Speed                *float64 `json:"meanSpeedMPS"`
	HR                   *float64 `json:"meanHR"`
	SpeedCV              *float64 `json:"speedCV"`
	HRCoverage           float64  `json:"hrCoverage"`
}
type LongRun struct {
	LongestContinuousDuration float64 `json:"longestContinuousDurationSeconds"`
	First                     *Half   `json:"comparableFirstHalf"`
	Second                    *Half   `json:"comparableSecondHalf"`
}
type Environment struct {
	Indoor       *bool    `json:"indoor"`
	TemperatureC *float64 `json:"temperatureC"`
	Humidity     *float64 `json:"humidity"`
	State        State    `json:"state"`
}
type Evidence struct {
	InputHash        string   `json:"inputHash"`
	FactManifestHash string   `json:"factManifestHash"`
	FactCount        int      `json:"factCount"`
	SourceEpochs     []string `json:"sourceEpochs"`
	ParameterIDs     []string `json:"parameterIDs"`
	ParameterHashes  []string `json:"parameterHashes"`
	ConfigHash       string   `json:"configHash"`
}
type WorkoutFeatures struct {
	Structure     *StructureFeatures       `json:"structure,omitempty"`
	RunFitness    *RunFitnessFeatures      `json:"runFitness,omitempty"`
	ActivityType  int64                    `json:"activityType"`
	Schema        string                   `json:"schemaVersion"`
	Algorithm     string                   `json:"algorithm"`
	Version       string                   `json:"algorithmVersion"`
	StackID       string                   `json:"stackID"`
	DatasetID     string                   `json:"datasetID"`
	WorkoutID     string                   `json:"workoutID"`
	Generation    int64                    `json:"generation"`
	AsOf          int64                    `json:"asOfUs"`
	State         State                    `json:"state"`
	Timing        Timing                   `json:"timing"`
	Distance      DistanceChoice           `json:"distance"`
	HeartRate     SignalSummary            `json:"heartRate"`
	Speed         SignalSummary            `json:"speed"`
	Route         RouteSummary             `json:"route"`
	Windows       []WindowCandidates       `json:"performanceWindows"`
	Steady        []SteadySegment          `json:"steadySegments"`
	LongRun       LongRun                  `json:"longRun"`
	Dynamics      map[string]SignalSummary `json:"runningDynamics"`
	Environment   Environment              `json:"environment"`
	Qualification Qualifications           `json:"qualification"`
	Reasons       []Reason                 `json:"reasonCodes"`
	Evidence      Evidence                 `json:"evidence"`
}

func (f WorkoutFeatures) MarshalArtifact() ([]byte, string, error) {
	b, e := json.Marshal(f)
	if e != nil {
		return nil, "", failure("feature_invalid_input")
	}
	if len(b) > MaxArtifactBytes {
		return nil, "", failure("feature_artifact_limit")
	}
	h := sha256.Sum256(b)
	return b, hex.EncodeToString(h[:]), nil
}
