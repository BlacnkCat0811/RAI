// Package trainingload contains only deterministic candidate load mathematics.
package trainingload

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	wf "rai/server/internal/workoutfeatures"
	"time"
)

const (
	Algorithm        = "rai.training_load.hrr_integral"
	StateAlgorithm   = "rai.load_state.exp_finite"
	Version          = "v1"
	Schema           = "rai.training_load/1"
	StateSchema      = "rai.load_state/1"
	ArtifactKey      = "training_load"
	StateKey         = "load_state"
	JobKind          = "analysis.training_load"
	StateJobKind     = "analysis.load_state"
	Unit             = "RAI_AU"
	ScopeName        = "recorded_running_load"
	Horizon          = 180
	MaxRecomputeDays = 552
)

func Scope(workout string) string { return "model/" + wf.StackID() + "/workout_load/" + workout }
func StateScope() string          { return "model/" + wf.StackID() + "/athlete/load" }
func ConfigHash() string {
	return Hash(struct {
		Model, State, Exposure                               string
		Coverage, MaxGap, Conflict, SummaryCap, RatioMinimum float64
		Horizon                                              int
	}{Algorithm + "/" + Version, StateAlgorithm + "/" + Version, wf.ExposureSchema, .95, 30, 0, .35, 1, Horizon})
}
func Hash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func ptr(v float64) *float64 { return &v }
func finite(v float64) bool  { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func day(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}
func invalid() error { return errors.New("training_load_invalid_input") }

type WorkoutInput struct {
	Calendar         *time.Location     `json:"-"`
	CalendarIdentity string             `json:"calendarIdentity"`
	Feature          wf.WorkoutFeatures `json:"feature"`
	Exposure         wf.HRExposure      `json:"exposure"`
	FeatureRunID     string             `json:"featureArtifactID"`
	Timezone         string             `json:"timezone"`
	Deleted          bool               `json:"deleted"`
}
type Contribution struct {
	Day           string   `json:"day"`
	Load          *float64 `json:"load"`
	Observed      *float64 `json:"observedLoad"`
	ActiveSeconds *float64 `json:"activeSeconds"`
	Quality       string   `json:"quality"`
}
type ExternalExposure struct {
	ActiveMinutes *float64 `json:"activeMinutes"`
	DistanceM     *float64 `json:"distanceM"`
}
type Result struct {
	Schema          string   `json:"schemaVersion"`
	DatasetID       string   `json:"datasetID"`
	WorkoutID       string   `json:"workoutID"`
	InputGeneration int64    `json:"inputGeneration"`
	Model           string   `json:"model"`
	ModelVersion    string   `json:"modelVersion"`
	ConfigHash      string   `json:"configHash"`
	Scope           string   `json:"scope"`
	Status          string   `json:"status"`
	Load            *float64 `json:"load"`
	Unit            string   `json:"unit"`
	Method          string   `json:"method"`
	Quality         string   `json:"quality"`
	Confidence      float64  `json:"confidence"`
	ObservedLoad    *float64 `json:"observedLoad"`
	EstimatedLoad   *float64 `json:"estimatedLoad"`
	ScaleFactor     *float64 `json:"scaleFactor"`
	ActiveSeconds   *float64 `json:"activeSeconds"`
	ValidHRSeconds  float64  `json:"validHRSeconds"`
	Coverage        float64  `json:"coverage"`
	MaxGap          float64  `json:"maxGapSeconds"`
	ConflictSeconds float64  `json:"parameterConflictSeconds"`
	// Bounded (v2) policy only; omitted from frozen v1 artifacts.
	QualityTier       string            `json:"qualityTier,omitempty"`
	AboveMaxSeconds   float64           `json:"aboveReferenceMaxSeconds,omitempty"`
	BelowRestSeconds  float64           `json:"belowReferenceRestSeconds,omitempty"`
	HRReference       *wf.Parameter     `json:"hrReference"`
	FeatureArtifactID string            `json:"featureArtifactID"`
	FeatureVersion    string            `json:"featureVersion"`
	FeatureGeneration int64             `json:"featureGeneration"`
	ReasonCodes       []string          `json:"reasonCodes"`
	Evidence          map[string]string `json:"evidence"`
	External          ExternalExposure  `json:"externalExposure"`
	Days              []Contribution    `json:"dailyContributions"`
}
type LedgerLoad struct {
	WorkoutID string   `json:"workoutID"`
	RunID     string   `json:"runID"`
	Day       string   `json:"day"`
	Load      *float64 `json:"load"`
	Quality   string   `json:"quality"`
}
type StateInput struct {
	Calendar         *time.Location `json:"-"`
	CalendarIdentity string         `json:"calendarIdentity"`
	DatasetID        string         `json:"datasetID"`
	Generation       int64          `json:"generation"`
	Timezone         string         `json:"timezone"`
	From             string         `json:"from"`
	Through          string         `json:"through"`
	HistoryFrom      string         `json:"historyFrom"`
	Loads            []LedgerLoad   `json:"ledger"`
}
type KernelValue struct {
	Value             *float64 `json:"value"`
	KnownContribution float64  `json:"knownContribution"`
	UnknownWeightMass float64  `json:"unknownWeightMass"`
	// Bounded (v2) state only: unknown days valued at the ring's mean known
	// training day. Omitted when nothing was imputed, so v1 stays byte-identical.
	ImputedContribution float64 `json:"imputedContribution,omitempty"`
}
type DailyState struct {
	Day                 string      `json:"day"`
	RecordedLoad        *float64    `json:"recordedLoad"`
	KnownRecordedLoad   float64     `json:"knownRecordedLoad"`
	UnknownWorkouts     int         `json:"unknownWorkouts"`
	RecordedWorkouts    int         `json:"recordedWorkouts"`
	Quality             string      `json:"quality"`
	Acute               KernelValue `json:"acute"`
	Chronic             KernelValue `json:"chronic"`
	Balance             *float64    `json:"balance"`
	Ratio               *float64    `json:"ratio"`
	Trend7              *float64    `json:"trend7"`
	HistoryDaysObserved int         `json:"historyDaysObserved"`
	InitialState        string      `json:"initialState"`
	// Bounded (v2) state only: acute or chronic includes imputed unknown days.
	Imputed bool `json:"imputed,omitempty"`
}
type StateResult struct {
	Schema     string       `json:"schemaVersion"`
	DatasetID  string       `json:"datasetID"`
	Generation int64        `json:"generation"`
	Model      string       `json:"model"`
	Version    string       `json:"modelVersion"`
	ConfigHash string       `json:"configHash"`
	Scope      string       `json:"scope"`
	Unit       string       `json:"unit"`
	TrendUnit  string       `json:"trendUnit"`
	Days       []DailyState `json:"days"`
}

// Calendar is resolved at the I/O boundary. UTC needs no external calendar.
func resolvedCalendar(zone string, loc *time.Location) (*time.Location, error) {
	if loc == nil && zone == "UTC" {
		return time.UTC, nil
	}
	if loc == nil || loc.String() != zone {
		return nil, invalid()
	}
	return loc, nil
}

// CalendarIdentity captures the actual supplied rules over the bounded range,
// so a caller's timezone-data update changes the input identity deterministically.
func CalendarIdentity(loc *time.Location, from, through time.Time) string {
	stamps := []int64{}
	for d := day(from.In(loc)); !d.After(through); d = d.AddDate(0, 0, 1) {
		stamps = append(stamps, d.UnixMicro())
		if len(stamps) > 1000 {
			break
		}
	}
	return Hash(struct {
		Zone       string
		Boundaries []int64
	}{loc.String(), stamps})
}
