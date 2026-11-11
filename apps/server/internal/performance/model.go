// Package performance implements candidate-only demonstrated running ability.
// It consumes M1 features, never raw signals, training load or physiological models.
package performance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"time"

	wf "rai/server/internal/workoutfeatures"
)

const (
	CurveModel       = "rai.performance_curve"
	AbilityModel     = "rai.running_ability"
	EnduranceModel   = "rai.endurance"
	Version          = "v1"
	Schema           = "rai.performance/1"
	EvidenceSchema   = "rai.performance_evidence/1"
	CurveSchema      = "rai.performance_curve/1"
	AbilitySchema    = "rai.running_ability/1"
	EnduranceSchema  = "rai.endurance/1"
	JobKind          = "analysis.performance"
	ArtifactKey      = "performance_state"
	EvidenceKey      = "performance_evidence"
	CurveKey         = "performance_curve"
	AbilityKey       = "running_ability"
	EnduranceKey     = "endurance"
	Mode             = "demonstrated"
	WindowDays       = 90
	SmoothingDays    = 14
	MaxWorkouts      = 96
	MaxArtifactBytes = 2 << 20
)

func Scope() string { return ScopeFor(wf.StackID()) }
func ScopeFor(stack string) string {
	return "model/" + stack + "/athlete/performance"
}
func Hash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func ConfigHash() string {
	return Hash(struct {
		Identity                                                                                              string
		EvidencePolicy, QualityPolicy, SolverPolicy, ScorePolicy, ConfidencePolicy, FreshnessPolicy           string
		Window, Smoothing, MaxWorkouts                                                                        int
		Quantile, GammaMin, GammaMax, GammaStep, Recency, Residual, Outlier, Confirmation, LOO, ConfidenceCap float64
	}{"rai.performance.policy/1",
		"M1_best_only;workout_sum_weight<=1;five_workouts;14_calendar_days;two_short;two_long;max_extrapolation=1.5;late_arrival_limit=90d",
		"coverage*distance_agreement*terrain*source*gap*qualification;unknown_source=.5;non_supported_gap=.8;conditional=.9",
		"weighted_quantile=.8;loss_tie_tolerance=1e-12;larger_gamma_then_smaller_alpha;unweighted_median_absolute_residual",
		"100*ln(v60/1.5)/ln(4);clamp_0_100;retain_raw;integer_round_for_display",
		".55*min(n/10,1)*min(span/28,1)*min(neff/8,1)*mean_quality*exp(-age/45)",
		"aging>=14d;hidden>=28d;expired>=42d;no_detraining;three_event_targets;log_smoothing_exp(-k/14)",
		WindowDays, SmoothingDays, MaxWorkouts, .8, .7, 1, .001, 45, .06, .10, .05, .05, .55})
}
func ptr(v float64) *float64 { return &v }
func finite(v float64) bool  { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func invalid() error         { return errors.New("performance_invalid_input") }
func date(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}
func days(a, b time.Time) int { // Calendar days, including DST calendars.
	au := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, time.UTC)
	bu := time.Date(b.Year(), b.Month(), b.Day(), 0, 0, 0, 0, time.UTC)
	return int(au.Sub(bu) / (24 * time.Hour))
}

// FeatureRef is transient input. Persisted evidence stores lineage and selected
// windows only, rather than a second copy of WorkoutFeatures.
type FeatureRef struct {
	Feature         wf.WorkoutFeatures `json:"feature"`
	RunID           string             `json:"runID"`
	KnownAt         int64              `json:"knownAtUs"`
	FirstReceivedAt int64              `json:"firstReceivedAtUs"`
}
type Input struct {
	Calendar   *time.Location `json:"-"`
	Timezone   string         `json:"timezone"`
	DatasetID  string         `json:"datasetID"`
	Generation int64          `json:"generation"`
	AsOf       int64          `json:"asOfUs"`
	// revised uses facts known at AsOf to revise event-day targets. as_known
	// additionally requires every feature to have been known on its target day.
	View     string       `json:"view"`
	Features []FeatureRef `json:"features"`
}
type Quality struct {
	Coverage          float64 `json:"speedCoverage"`
	DistanceAgreement float64 `json:"distanceAgreement"`
	Terrain           float64 `json:"terrainSupport"`
	Source            float64 `json:"sourceSupport"`
	Gap               float64 `json:"gapSupport"`
	Qualification     float64 `json:"qualificationSupport"`
	Q                 float64 `json:"q"`
}
type Evidence struct {
	WorkoutID      string           `json:"workoutID"`
	Duration       int              `json:"durationSeconds"`
	Distance       float64          `json:"distanceM"`
	MeanSpeed      float64          `json:"meanSpeedMPS"`
	StartedAt      int64            `json:"startedAtUs"`
	EndedAt        int64            `json:"endedAtUs"`
	WorkoutEnd     int64            `json:"workoutEndedAtUs"`
	KnownAt        int64            `json:"knownAtUs"`
	FeatureVersion string           `json:"featureVersion"`
	FeatureRunID   string           `json:"featureRunID"`
	Generation     int64            `json:"featureGeneration"`
	SourceEpoch    string           `json:"sourceEpoch"`
	TerrainContext string           `json:"terrainContext"`
	Quality        Quality          `json:"quality"`
	Qualification  wf.Qualification `json:"qualification"`
	EffortMode     string           `json:"effortMode"`
	Evidence       string           `json:"evidence"`
}
type WeightedEvidence struct {
	Evidence
	Weight float64 `json:"weight"`
	State  string  `json:"state"`
}
type Support struct {
	IndependentWorkouts int      `json:"independentWorkoutCount"`
	ShortWorkouts       int      `json:"shortGroupWorkouts"`
	LongWorkouts        int      `json:"longGroupWorkouts"`
	Durations           []int    `json:"durationCoverage"`
	SourceEpochs        []string `json:"sourceEpochs"`
	HistorySpan         int      `json:"historySpanDays"`
	EffectiveWorkouts   float64  `json:"nEff"`
	QualitySupport      float64  `json:"qualitySupport"`
	LatestEvidence      int64    `json:"latestEvidenceUs"`
	LongestDuration     int      `json:"longestDurationSeconds"`
}
type Curve struct {
	D30                    float64 `json:"D30"`
	Gamma                  float64 `json:"gamma"`
	V20                    float64 `json:"v20"`
	V60                    float64 `json:"v60"`
	Retention              float64 `json:"enduranceRetention60to20"`
	Loss                   float64 `json:"quantileLoss"`
	MedianAbsoluteResidual float64 `json:"medianAbsoluteResidual"`
	LOOMaxChange           float64 `json:"looMaxRelativeV60Change"`
}
type Target struct {
	Day     string  `json:"day"`
	View    string  `json:"view"`
	Status  string  `json:"status"`
	Curve   *Curve  `json:"curve"`
	Support Support `json:"support"`
}
type Ability struct {
	D30                   float64 `json:"D30"`
	Gamma                 float64 `json:"gamma"`
	V20                   float64 `json:"v20"`
	V60                   float64 `json:"v60"`
	Retention             float64 `json:"enduranceRetention60to20"`
	RawScore              float64 `json:"rawUnclippedScore"`
	Score                 float64 `json:"score"`
	DisplayScore          int     `json:"displayScore"`
	Saturation            string  `json:"saturation"`
	LogSpeedAnchor        float64 `json:"log30MinuteSpeedAnchor"`
	LogEnduranceExtension float64 `json:"logEnduranceExtension"`
}
type Result struct {
	Schema           string             `json:"schemaVersion"`
	DatasetID        string             `json:"datasetID"`
	Generation       int64              `json:"generation"`
	Model            string             `json:"model"`
	Version          string             `json:"modelVersion"`
	ConfigHash       string             `json:"configHash"`
	AsOf             int64              `json:"asOfUs"`
	View             string             `json:"view"`
	EvidenceMode     string             `json:"evidenceMode"`
	MaximalSupported string             `json:"maximalSupported"`
	Status           string             `json:"status"`
	Freshness        string             `json:"freshness"`
	Stability        string             `json:"stability"`
	Confidence       float64            `json:"confidence"`
	Support          Support            `json:"support"`
	AnchorDay        string             `json:"anchorDay"`
	Targets          []Target           `json:"targets"`
	ValidTargetDays  int                `json:"validTargetDays"`
	Curve            *Curve             `json:"curve"`
	Ability          *Ability           `json:"ability"`
	Evidence         []WeightedEvidence `json:"evidence"`
	ReasonCodes      []string           `json:"reasonCodes"`
	Trend            *float64           `json:"trend"`
}

// Envelope permits independently inspected typed artifacts in the same run.
type Envelope struct {
	Schema     string `json:"schemaVersion"`
	DatasetID  string `json:"datasetID"`
	Generation int64  `json:"generation"`
	ConfigHash string `json:"configHash"`
	Value      any    `json:"value"`
}

func (r Result) Envelope(schema string, value any) Envelope {
	return Envelope{schema, r.DatasetID, r.Generation, r.ConfigHash, value}
}

func Score(v60 float64) (raw, clipped float64, saturation string) {
	raw = 100 * math.Log(v60/1.5) / math.Log(4)
	clipped = math.Max(0, math.Min(100, raw))
	saturation = "none"
	if raw < 0 {
		saturation = "lower"
	}
	if raw > 100 {
		saturation = "upper"
	}
	return
}
func ability(alpha, gamma float64) Ability {
	d30 := math.Exp(alpha)
	v20 := d30 * math.Pow(1200.0/1800, gamma) / 1200
	v60 := d30 * math.Pow(2, gamma) / 3600
	raw, score, sat := Score(v60)
	return Ability{d30, gamma, v20, v60, v60 / v20, raw, score, int(math.Round(score)), sat, alpha - math.Log(1800), (gamma - 1) * math.Ln2}
}

// Freshen changes evidence support and visibility, never invents detraining.
// Persisted results remain immutable; this function produces a read projection.
// A result that never had a curve because evidence is still insufficient keeps
// that status: age only marks its freshness and adds a staleness reason, so the
// product reports "not enough evidence" first instead of hiding it behind
// "expired" (more qualifying runs fix both).
func Freshen(r Result, at int64) Result {
	if r.Support.LatestEvidence == 0 || at < r.AsOf {
		return r
	}
	r.AsOf = at
	age := float64(at-r.Support.LatestEvidence) / 86400e6
	noCurve := r.Ability == nil && (r.Status == "insufficient_long_evidence" || r.Status == "collecting")
	switch {
	case age >= 42:
		r.Freshness = "expired"
		if !noCurve {
			r.Status = "insufficient_data"
		}
		r.Ability = nil
		r.Confidence = 0
		r.ReasonCodes = withReason(r.ReasonCodes, "evidence_expired")
	case age >= 28:
		r.Freshness = "stale"
		if noCurve {
			r.ReasonCodes = withReason(r.ReasonCodes, "stale")
		} else {
			r.Status = "stale"
		}
		r.Ability = nil
		r.Confidence = 0
	case age >= 14:
		r.Freshness = "aging"
	default:
		r.Freshness = "fresh"
	}
	// Recompute from support, not previously decayed confidence (idempotent).
	if age < 28 {
		r.Confidence = confidence(r.Support) * math.Exp(-math.Max(0, age)/45)
		if r.Stability != "stable" || r.Ability == nil {
			r.Confidence = 0
		}
	}
	return r
}
func withReason(xs []string, x string) []string {
	if slices.Contains(xs, x) {
		return xs
	}
	return append(append([]string{}, xs...), x)
}
func confidence(s Support) float64 {
	return .55 * math.Min(1, float64(s.IndependentWorkouts)/10) * math.Min(1, float64(s.HistorySpan)/28) * math.Min(1, s.EffectiveWorkouts/8) * s.QualitySupport
}

// ComparableTrend refuses model/mode/view mismatches and non-28-day comparisons.
func ComparableTrend(current, previous Result, loc *time.Location) *float64 {
	if loc == nil || current.Ability == nil || previous.Ability == nil || current.Model != previous.Model || current.Version != previous.Version || current.ConfigHash != previous.ConfigHash || current.EvidenceMode != previous.EvidenceMode || current.View != previous.View || days(date(time.UnixMicro(current.AsOf), loc), date(time.UnixMicro(previous.AsOf), loc)) != 28 {
		return nil
	}
	return ptr(current.Ability.Score - previous.Ability.Score)
}
