// Package vo2max implements the native submaximal running VO2 estimator
// rai.vo2max.submax/v1. It is a pure candidate domain: it never consumes Apple
// VO2, defaults an HR parameter, calls a model, reads raw storage or a clock.
package vo2max

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
)

func ScopeFor(stack string) string { return "model/" + stack + "/athlete/vo2_submax" }

const (
	Model        = "rai.vo2max.submax"
	Algorithm    = "rai.vo2max.submax"
	Version      = "v1"
	Schema       = "rai.vo2max.submax/1"
	JobKind      = "analysis.vo2max.submax"
	ArtifactKey  = "vo2max_submax"
	Family       = "vo2max_submax_candidate"
	ScopeName    = "native_submax_running"
	Unit         = "mL/kg/min"
	Experimental = true
	DayUs        = int64(86_400_000_000)
	MaxWorkouts  = 512
	MaxSamples   = 250_000
	MaxArtifact  = 2 << 20
)

var ErrInvalid = errors.New("vo2max_invalid_input")
var ErrLimit = errors.New("vo2max_input_limit")

func Hash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func finite(v float64) bool  { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func ptr(v float64) *float64 { return &v }

// Reference is an already resolved HR snapshot. The estimator never infers a
// default HRmax or uses a population formula.
type Reference struct {
	MaxHR        float64 `json:"maxHR"`
	RestHR       float64 `json:"restingHR"`
	MaxMeasured  int64   `json:"maxMeasuredAtUs"`
	RestMeasured int64   `json:"restMeasuredAtUs"`
	ReferenceID  string  `json:"referenceID"`
	Hash         string  `json:"hash"`
	SourceEpoch  string  `json:"sourceEpoch"`
}

func (r Reference) Validate() bool {
	return finite(r.MaxHR) && finite(r.RestHR) && r.MaxHR >= 30 && r.MaxHR < 220 && r.RestHR >= 30 && r.RestHR < 220 && r.MaxHR > r.RestHR && r.ReferenceID != "" && r.Hash != ""
}

type Sample struct {
	Start int64    `json:"startUs"`
	End   int64    `json:"endUs"`
	HR    *float64 `json:"hr,omitempty"`
	Speed *float64 `json:"speedMPS,omitempty"`
	Grade *float64 `json:"grade,omitempty"`
}

type Pause struct {
	Start int64 `json:"startUs"`
	End   int64 `json:"endUs"`
}

type Workout struct {
	ID                string   `json:"workoutID"`
	DatasetID         string   `json:"datasetID"`
	Generation        int64    `json:"generation"`
	ActivityType      int64    `json:"activityType"`
	Outdoor           bool     `json:"outdoor"`
	Start             int64    `json:"startUs"`
	End               int64    `json:"endUs"`
	Day               string   `json:"localDay"`
	Samples           []Sample `json:"samples"`
	Pauses            []Pause  `json:"pauses"`
	DistanceM         *float64 `json:"distanceM"`
	RecordedDistanceM *float64 `json:"recordedDistanceM"`
	TemperatureC      *float64 `json:"temperatureC"`
	HumidityPct       *float64 `json:"humidityPct"`
	KnownAt           int64    `json:"knownAtUs"`
}

type Support struct {
	HRCoverage       float64 `json:"hrCoverage"`
	SpeedCoverage    float64 `json:"speedCoverage"`
	CommonSeconds    float64 `json:"commonSupportSeconds"`
	GradeCoverage    float64 `json:"flatGradeCoverage"`
	PauseFraction    float64 `json:"pauseFraction"`
	DistanceError    float64 `json:"distanceError"`
	EnvironmentKnown bool    `json:"environmentKnown"`
}

type WorkoutEvidence struct {
	Schema           string   `json:"schemaVersion"`
	Model            string   `json:"model"`
	ModelVersion     string   `json:"modelVersion"`
	WorkoutID        string   `json:"workoutID"`
	DatasetID        string   `json:"datasetID"`
	Generation       int64    `json:"generation"`
	Day              string   `json:"localDay"`
	Start            int64    `json:"startUs"`
	End              int64    `json:"endUs"`
	SourceEpoch      string   `json:"sourceEpoch"`
	Context          string   `json:"context"`
	ReferenceHash    string   `json:"referenceHash"`
	ReferenceID      string   `json:"referenceID"`
	MaxHR            float64  `json:"maxHR"`
	RestHR           float64  `json:"restHR"`
	Qualified        bool     `json:"qualified"`
	Windows          int      `json:"qualifiedWindows"`
	CandidateV       *float64 `json:"candidateVO2"`
	MedianHR         *float64 `json:"medianHR"`
	MedianSpeedMPS   *float64 `json:"medianSpeedMPS"`
	Quality          float64  `json:"quality"`
	Support          Support  `json:"support"`
	EnvironmentKnown bool     `json:"environmentKnown"`
	KnownAt          int64    `json:"knownAtUs"`
	Reasons          []string `json:"reasonCodes"`
	ContentHash      string   `json:"contentHash"`
}

type Evidence struct {
	ReferenceEstimated bool    `json:"referenceEstimated,omitempty"`
	WorkoutID          string  `json:"workoutID"`
	Day                string  `json:"localDay"`
	At                 int64   `json:"startUs"`
	SourceEpoch        string  `json:"sourceEpoch"`
	Context            string  `json:"context"`
	ReferenceHash      string  `json:"referenceHash"`
	V                  float64 `json:"vo2"` // mL/kg/min
	Q                  float64 `json:"quality"`
	HRMean             float64 `json:"meanHR"`
	SpeedMPS           float64 `json:"meanSpeedMPS"`
	MaxHR              float64 `json:"maxHR"`
	RestHR             float64 `json:"restHR"`
	KnownAt            int64   `json:"knownAtUs"`
	EnvironmentKnown   bool    `json:"environmentKnown"`
}

type AggregateInput struct {
	Timezone      string     `json:"timezone,omitempty"`
	DatasetID     string     `json:"datasetID"`
	Generation    int64      `json:"generation"`
	AsOf          int64      `json:"asOfUs"`
	ReferenceHash string     `json:"referenceHash"`
	SourceEpoch   string     `json:"sourceEpoch"`
	Evidence      []Evidence `json:"evidence"`
	View          string     `json:"view"`
	ComputedAt    int64      `json:"computedAtUs"`
}

type Range struct {
	Low  float64 `json:"low"`
	High float64 `json:"high"`
}

type Sensitivity struct {
	Range     *Range   `json:"range"`
	Corners   int      `json:"cornerCount"`
	Width     *float64 `json:"relativeWidth"`
	Unbounded bool     `json:"unbounded"`
	Reasons   []string `json:"reasonCodes"`
}

type LOO struct {
	Range     *Range   `json:"range"`
	Folds     int      `json:"folds"`
	SupportOK bool     `json:"supportSufficient"`
	Reasons   []string `json:"reasonCodes"`
}

type Result struct {
	Schema                   string      `json:"schemaVersion"`
	Model                    string      `json:"model"`
	ModelVersion             string      `json:"modelVersion"`
	DatasetID                string      `json:"datasetID"`
	Generation               int64       `json:"generation"`
	AsOf                     int64       `json:"asOfUs"`
	ComputedAt               int64       `json:"computedAtUs"`
	EvidenceAt               *int64      `json:"evidenceAtUs"`
	ReferenceHash            string      `json:"referenceHash"`
	SourceEpoch              string      `json:"sourceEpoch"`
	Status                   string      `json:"status"`
	Quality                  string      `json:"quality"`
	Freshness                string      `json:"freshness"`
	Experimental             bool        `json:"experimental"`
	Unit                     string      `json:"unit"`
	Value                    *float64    `json:"value"`
	TargetLog                *float64    `json:"targetLog"`
	Sensitivity              Sensitivity `json:"sensitivity"`
	LOO                      LOO         `json:"leaveOneOut"`
	Confidence               float64     `json:"confidence"`
	ConfidenceMeaning        string      `json:"confidenceMeaning"`
	NEff                     float64     `json:"effectiveEvidence"`
	QAggregate               float64     `json:"qualityAggregate"`
	Support                  Support     `json:"support"`
	Workouts                 int         `json:"independentWorkouts"`
	Stability                string      `json:"stability"`
	SpanDays                 int         `json:"spanDays"`
	RecentDays               int         `json:"daysSinceLatestEvidence"`
	Evidence                 []Evidence  `json:"evidence"`
	Reasons                  []string    `json:"reasonCodes"`
	CandidateOnly            bool        `json:"candidateOnly"`
	PhysiologicallyValidated bool        `json:"physiologicallyValidated"`
}
