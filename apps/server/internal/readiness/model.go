// Package readiness is a pure, deterministic candidate running-readiness model.
// It consumes only versioned Recovery evidence and M2 ledger summaries.
package readiness

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
)

const (
	Model            = "rai.readiness.training/v1"
	Algorithm        = "rai.readiness.training"
	Schema           = "rai.readiness.training/1"
	InputSchema      = "rai.readiness.input/1"
	Family           = "training_readiness_candidate"
	JobKind          = "analysis.training_readiness"
	Key              = "training_readiness"
	RecoveryModel    = "rai.recovery.physiological/v1"
	LoadModel        = "rai.training_load.hrr_integral/v1"
	Hour             = int64(3600000000)
	MaxLoads         = 4096
	MaxArtifactBytes = 2 << 20
)

var ErrInvalid = errors.New("readiness_invalid_input")
var ErrLimit = errors.New("readiness_input_limit")

type Config struct {
	Timezone          string  `json:"timezone"`
	Policy            string  `json:"policy"`
	TauUs             int64   `json:"tauLoadUs"`
	LookbackUs        int64   `json:"lookbackUs"`
	BaselineDays      int     `json:"baselineDays"`
	MinimumDays       int     `json:"minimumTrainingDays"`
	MinimumSpanDays   int     `json:"minimumSpanDays"`
	BaselineFreshDays int     `json:"baselineFreshDays"`
	BaselineFloor     float64 `json:"baselineFloorAU"`
	ConfidenceCap     float64 `json:"confidenceCap"`
	CoverageFreshUs   int64   `json:"coverageFreshUs"`
	MaxWorkoutAU      float64 `json:"maxWorkoutAU"`
}

func DefaultConfig(tz string) Config {
	return Config{tz, "recorded-running;complete-day-median;finite-tail-certified/v1", 36 * Hour, 180 * 24 * Hour, 42, 5, 14, 14, 1, .55, 24 * Hour, 10000}
}
func Hash(v any) string {
	b, e := json.Marshal(v)
	if e != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (c Config) Hash() string { return Hash(c) }
func Scope(tz string) string  { return "instant/readiness/v1/" + Hash(tz)[:16] + "/current" }

type RecoveryEvidence struct {
	Model      string   `json:"model"`
	RunID      string   `json:"runID"`
	Hash       string   `json:"hash"`
	Generation int64    `json:"generation"`
	AsOf       int64    `json:"asOf"`
	FreshUntil int64    `json:"freshUntil"`
	Status     string   `json:"status"`
	Quality    string   `json:"quality"`
	Score      *float64 `json:"score"`
	Confidence float64  `json:"confidence"`
	Coverage   float64  `json:"evidenceCoverage"`
}

// Synthetic coverage is an explicit engineering fixture, never inferred from
// uploads. The current Workout wire protocol has no server catch-up assertion.
type Coverage struct {
	Identity   string `json:"identity"`
	From       int64  `json:"fromUs"`
	Through    int64  `json:"throughUs"`
	CaughtUp   bool   `json:"caughtUp"`
	Synthetic  bool   `json:"syntheticOnly"`
	StreamHash string `json:"streamHash"`
}
type Day struct {
	Date  string `json:"date"`
	Start int64  `json:"startUs"`
	End   int64  `json:"endUs"`
}
type DailyLoad struct {
	Day     string   `json:"day"`
	AU      *float64 `json:"load"`
	Quality string   `json:"quality"`
}
type WorkoutLoad struct {
	Conflict    bool        `json:"parameterConflict"`
	ID          string      `json:"workoutID"`
	End         int64       `json:"endUs"`
	FactHash    string      `json:"factHash"`
	RunID       string      `json:"loadRunID"`
	Hash        string      `json:"loadArtifactHash"`
	Model       string      `json:"modelVersion"`
	ConfigHash  string      `json:"loadConfigHash"`
	ParameterID string      `json:"parameterID"`
	AU          *float64    `json:"load"`
	Quality     string      `json:"quality"`
	Days        []DailyLoad `json:"dailyLoads"`
	Compatible  bool        `json:"lineageCompatible"`
}
type ReadinessSnapshot struct {
	LoadAssignment string            `json:"loadAssignment"`
	Dataset        string            `json:"datasetID"`
	Epoch          string            `json:"datasetEpoch"`
	Collector      string            `json:"collectorEpoch"`
	Assignment     string            `json:"assignmentID"`
	Generation     int64             `json:"generation"`
	AsOf           int64             `json:"asOf"`
	CapturedAt     int64             `json:"capturedAt"`
	Config         Config            `json:"config"`
	Recovery       *RecoveryEvidence `json:"recovery"`
	Loads          []WorkoutLoad     `json:"loads"`
	Days           []Day             `json:"calendar"` // 42 complete prior civil days, then today.
	Coverage       Coverage          `json:"loadCoverage"`
}
type Baseline struct {
	Value         *float64 `json:"value"`
	Median        *float64 `json:"median"`
	TrainingDays  int      `json:"trainingDays"`
	KnownZeroDays int      `json:"knownZeroDays"`
	UnknownDays   int      `json:"unknownDays"`
	SpanDays      int      `json:"spanDays"`
	LatestAgeDays int      `json:"latestAgeDays"`
	FloorApplied  bool     `json:"floorApplied"`
	State         string   `json:"state"`
}
type Term struct {
	End int64   `json:"endUs"`
	AU  float64 `json:"load"`
}
type Ref struct {
	FactHash    string `json:"factHash"`
	End         int64  `json:"endUs"`
	ParameterID string `json:"parameterID"`
	ID          string `json:"workoutID"`
	RunID       string `json:"runID"`
	Hash        string `json:"hash"`
}
type Result struct {
	Date                     string            `json:"date"`
	Schema                   string            `json:"schemaVersion"`
	Dataset                  string            `json:"datasetID"`
	Generation               int64             `json:"generation"`
	ModelVersion             string            `json:"modelVersion"`
	ConfigHash               string            `json:"configHash"`
	Config                   Config            `json:"config"`
	InputHash                string            `json:"inputHash"`
	CalendarHash             string            `json:"calendarHash"`
	AsOf                     int64             `json:"asOf"`
	EvaluationAt             int64             `json:"evaluationAt"`
	CapturedAt               int64             `json:"capturedAt"`
	ValidBefore              int64             `json:"validBefore"`
	Status                   string            `json:"status"`
	Quality                  string            `json:"quality"`
	Score                    *float64          `json:"score"`
	RecoveryScore            *float64          `json:"recoveryScore"`
	RecoveryContribution     *float64          `json:"recoveryContribution"`
	Recovery                 *RecoveryEvidence `json:"recoveryInput"`
	Burden                   *float64          `json:"recentLoadBurden"`
	Baseline                 Baseline          `json:"loadBaseline"`
	Penalty                  *float64          `json:"loadPenalty"`
	BurdenRatio              *float64          `json:"burdenInTypicalDays"`
	Confidence               float64           `json:"confidence"`
	LoadSupport              float64           `json:"loadSupportConfidence"`
	ConfidenceMeaning        string            `json:"confidenceMeaning"`
	Coverage                 Coverage          `json:"loadCoverage"`
	KnownLoads               int               `json:"knownLoads"`
	UnknownLoads             int               `json:"unknownLoads"`
	Terms                    []Term            `json:"loadTerms"`
	Refs                     []Ref             `json:"inputRefs"`
	TailBound                float64           `json:"omittedTailUpperBoundAU"`
	MaxScoreTailError        float64           `json:"maxScoreTailError"`
	Reasons                  []string          `json:"reasonCodes"`
	CandidateOnly            bool              `json:"candidateOnly"`
	PhysiologicallyValidated bool              `json:"physiologicallyValidated"`
	Scope                    string            `json:"scope"`
}

func finite(v float64) bool  { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func ptr(v float64) *float64 { return &v }
func Canonical(in ReadinessSnapshot) ReadinessSnapshot {
	in.Loads = append([]WorkoutLoad{}, in.Loads...)
	sort.Slice(in.Loads, func(i, j int) bool { return in.Loads[i].ID < in.Loads[j].ID })
	for i := range in.Loads {
		in.Loads[i].Days = append([]DailyLoad{}, in.Loads[i].Days...)
		sort.Slice(in.Loads[i].Days, func(a, b int) bool { return in.Loads[i].Days[a].Day < in.Loads[i].Days[b].Day })
	}
	return in
}
func InputHash(in ReadinessSnapshot) string { return Hash(Canonical(in)) }

// Even all 2^128 distinct UUIDs at M2's conservative per-workout AU ceiling
// give < 3e-10 AU beyond 180 days. No lifetime count or rate assumption exists.
func TailUpperBound(c Config) float64 {
	return math.Ldexp(c.MaxWorkoutAU, 128) * math.Exp(-float64(c.LookbackUs)/float64(c.TauUs))
}
func DecayedBurden(terms []Term, at int64, c Config) float64 {
	total := 0.
	for _, t := range terms {
		if t.End <= at && t.End >= at-c.LookbackUs {
			total += t.AU * math.Exp(-float64(at-t.End)/float64(c.TauUs))
		}
	}
	return total
}
func Score(p, d, b float64) (float64, error) {
	if !finite(p) || !finite(d) || !finite(b) || p < 0 || p > 100 || d < 0 || b <= 0 {
		return 0, ErrInvalid
	}
	return p * math.Exp(-math.Ln2*d/b), nil
}
