package diagnostics

import (
	"errors"
	"math"
	"sort"
)

// Model identities. Each model is versioned and released independently; a
// change to one model's contract must not silently change another identity.
const (
	WorkoutDurabilityModel   = "rai.workout_durability"
	WorkoutDurabilityVersion = "v1"
	WorkoutDurabilitySchema  = "rai.workout_durability/1"
	WorkoutDurabilityID      = "rai.workout_durability/v1"

	AerobicEfficiencyModel   = "rai.aerobic_efficiency"
	AerobicEfficiencyVersion = "v1"
	AerobicEfficiencySchema  = "rai.aerobic_efficiency/1"
	AerobicEfficiencyID      = "rai.aerobic_efficiency/v1"

	CriticalSpeedModel   = "rai.critical_speed"
	CriticalSpeedVersion = "v1"
	CriticalSpeedSchema  = "rai.critical_speed/1"
	CriticalSpeedID      = "rai.critical_speed/v1"

	LoadSettlingModel   = "rai.load_settling"
	LoadSettlingVersion = "v1"
	LoadSettlingSchema  = "rai.load_settling/1"
	LoadSettlingID      = "rai.load_settling/v1"
)

const (
	microsecondsPerDay int64 = 86_400_000_000
)

// Identity is the immutable version identity of one research model. All four
// models in this package are Experimental by contract.
type Identity struct {
	ID           string `json:"identity"`
	Model        string `json:"model"`
	Version      string `json:"version"`
	Schema       string `json:"schemaVersion"`
	Experimental bool   `json:"experimental"`
}

// WorkoutDurabilityIdentity returns the durability model identity.
func WorkoutDurabilityIdentity() Identity {
	return Identity{
		ID:           WorkoutDurabilityID,
		Model:        WorkoutDurabilityModel,
		Version:      WorkoutDurabilityVersion,
		Schema:       WorkoutDurabilitySchema,
		Experimental: true,
	}
}

// AerobicEfficiencyIdentity returns the aerobic-efficiency model identity.
func AerobicEfficiencyIdentity() Identity {
	return Identity{
		ID:           AerobicEfficiencyID,
		Model:        AerobicEfficiencyModel,
		Version:      AerobicEfficiencyVersion,
		Schema:       AerobicEfficiencySchema,
		Experimental: true,
	}
}

// CriticalSpeedIdentity returns the critical-speed model identity.
func CriticalSpeedIdentity() Identity {
	return Identity{
		ID:           CriticalSpeedID,
		Model:        CriticalSpeedModel,
		Version:      CriticalSpeedVersion,
		Schema:       CriticalSpeedSchema,
		Experimental: true,
	}
}

// LoadSettlingIdentity returns the load-settling model identity.
func LoadSettlingIdentity() Identity {
	return Identity{
		ID:           LoadSettlingID,
		Model:        LoadSettlingModel,
		Version:      LoadSettlingVersion,
		Schema:       LoadSettlingSchema,
		Experimental: true,
	}
}

// Status is the common result lifecycle shared by all four diagnostics.
type Status string

const (
	// StatusOK means the requested values are available under the model gates.
	StatusOK Status = "ok"
	// StatusPartial means only a documented subset of values is available.
	StatusPartial Status = "partial"
	// StatusCollecting means more independent observations are required.
	StatusCollecting Status = "collecting"
	// StatusInsufficient means the supplied observations cannot support the
	// model, for example because qualification gates failed.
	StatusInsufficient Status = "insufficient"
	// StatusUnstable means a fit existed but failed identifiability or
	// leave-one-out stability gates, so no usable estimate is emitted.
	StatusUnstable Status = "unstable"
)

// ReasonCode is a stable machine-readable explanation. Codes are additive and
// never replace the distinction between a missing value and a failed gate.
type ReasonCode string

// ReasonCodes is an ordered, duplicate-free set of reason codes.
type ReasonCodes []ReasonCode

// Add appends a reason code once. A nil receiver is ignored.
func (r *ReasonCodes) Add(code ReasonCode) {
	if r == nil {
		return
	}
	for _, existing := range *r {
		if existing == code {
			return
		}
	}
	*r = append(*r, code)
}

// Has reports whether the set contains code.
func (r ReasonCodes) Has(code ReasonCode) bool {
	for _, existing := range r {
		if existing == code {
			return true
		}
	}
	return false
}

// Common reason codes. Model-specific codes live with their model files.
const (
	ReasonInvalidInput            ReasonCode = "invalid_input"
	ReasonNonFiniteInput          ReasonCode = "non_finite_input"
	ReasonEmptyInput              ReasonCode = "empty_input"
	ReasonDurationTooShort        ReasonCode = "duration_too_short"
	ReasonHalfTooShort            ReasonCode = "half_too_short"
	ReasonHalvesUnequal           ReasonCode = "halves_unequal"
	ReasonSpeedCVHigh             ReasonCode = "speed_cv_high"
	ReasonSpeedSupportLow         ReasonCode = "speed_support_low"
	ReasonHRSupportLow            ReasonCode = "hr_support_low"
	ReasonMissingHR               ReasonCode = "missing_hr"
	ReasonNoActiveSegment         ReasonCode = "no_active_segment"
	ReasonPauseSpanExcluded       ReasonCode = "pause_span_excluded"
	ReasonPaceShiftConfoundsDrift ReasonCode = "pace_shift_confounds_drift"
	ReasonSourceMismatch          ReasonCode = "source_mismatch"
	ReasonTerrainMismatch         ReasonCode = "terrain_mismatch"
	ReasonSourceUnknown           ReasonCode = "source_unknown"
	ReasonTerrainUnknown          ReasonCode = "terrain_unknown"
	ReasonContextUnknown          ReasonCode = "context_unknown"
	ReasonInsufficientHistory     ReasonCode = "insufficient_history"
	ReasonInsufficientRuns        ReasonCode = "insufficient_comparable_runs"
	ReasonOutsideWindow           ReasonCode = "outside_window"
	ReasonUnsupportedInput        ReasonCode = "unsupported_input"
	ReasonTooFewWorkouts          ReasonCode = "too_few_workouts"
	ReasonTooFewDurations         ReasonCode = "too_few_durations"
	ReasonDurationSpanLow         ReasonCode = "duration_span_too_small"
	ReasonUnsupportedDuration     ReasonCode = "unsupported_duration"
	ReasonInvalidEffort           ReasonCode = "invalid_effort"
	ReasonDegenerateFit           ReasonCode = "degenerate_fit"
	ReasonLOOCSUnstable           ReasonCode = "loo_cs_unstable"
	ReasonLOORMSEHigh             ReasonCode = "loo_rmse_high"
	ReasonLOOSingleDuration       ReasonCode = "loo_single_duration"
	ReasonCSOutsideSpeed          ReasonCode = "cs_outside_observed_speed"
	ReasonNonPositiveDPrime       ReasonCode = "non_positive_d_prime"
	ReasonSteadyTooShort          ReasonCode = "steady_support_too_short"
	ReasonHRSupportFraction       ReasonCode = "hr_support_fraction_low"
	ReasonBaselineMissing         ReasonCode = "baseline_missing"
	ReasonScoreBelowGate          ReasonCode = "score_below_threshold"
	ReasonCoverageIncomplete      ReasonCode = "coverage_incomplete"
	ReasonTemperatureUnknown      ReasonCode = "temperature_unknown"
	ReasonDecouplingPartial       ReasonCode = "decoupling_partial"
)

// SupportLevel describes how much independent support backs a result.
type SupportLevel string

const (
	SupportHigh     SupportLevel = "high"
	SupportModerate SupportLevel = "moderate"
	SupportLow      SupportLevel = "low"
	SupportNone     SupportLevel = "none"
)

// Support is the common support/coverage descriptor. Fraction is always in
// [0,1]; Count is the number of independent observations, not the number of
// raw samples or segments.
type Support struct {
	Level    SupportLevel `json:"level"`
	Fraction float64      `json:"fraction"`
	Count    int          `json:"count"`
}

// EvidenceRef identifies an input observation without embedding health data.
type EvidenceRef struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Hash string `json:"hash,omitempty"`
}

// Header is embedded by every model result. The identity stays explicit so a
// consumer cannot infer one model's version from another model's output.
type Header struct {
	Identity
	Status   Status        `json:"status"`
	Reasons  ReasonCodes   `json:"reasonCodes"`
	Support  Support       `json:"support"`
	Evidence []EvidenceRef `json:"evidence,omitempty"`
}

func newHeader(id Identity) Header {
	return Header{
		Identity: id,
		Reasons:  ReasonCodes{},
		Support:  Support{Level: SupportNone},
	}
}

// TrendDirection is a descriptive direction, not a performance verdict.
type TrendDirection string

const (
	TrendIncreasing TrendDirection = "increasing"
	TrendDecreasing TrendDirection = "decreasing"
	TrendStable     TrendDirection = "stable"
	TrendUnknown    TrendDirection = "unknown"
)

type trendPoint struct {
	x float64
	y float64
}

// linearTrend returns the ordinary least-squares direction of points. A
// relative change below 0.5% across the observed span is treated as stable.
// x is in days and y is in the metric's native unit.
func linearTrend(points []trendPoint) TrendDirection {
	if len(points) < 2 {
		return TrendUnknown
	}
	pts := append([]trendPoint(nil), points...)
	sort.Slice(pts, func(i, j int) bool { return pts[i].x < pts[j].x })
	minX, maxX := pts[0].x, pts[len(pts)-1].x
	if !finite(minX) || !finite(maxX) || maxX-minX <= 0 {
		return TrendStable
	}
	var sx, sy, sxx, sxy float64
	for _, p := range pts {
		if !finite(p.x) || !finite(p.y) {
			return TrendUnknown
		}
		sx += p.x
		sy += p.y
		sxx += p.x * p.x
		sxy += p.x * p.y
	}
	n := float64(len(pts))
	den := n*sxx - sx*sx
	if math.Abs(den) <= 1e-12 {
		return TrendStable
	}
	slope := (n*sxy - sx*sy) / den
	delta := slope * (maxX - minX)
	mean := sy / n
	scale := math.Abs(mean)
	if scale <= 1e-12 {
		if math.Abs(delta) <= 1e-12 {
			return TrendStable
		}
	} else if math.Abs(delta)/scale < 0.005 {
		return TrendStable
	}
	if delta > 0 {
		return TrendIncreasing
	}
	return TrendDecreasing
}

func finite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

func allFinite(values ...float64) bool {
	for _, v := range values {
		if !finite(v) {
			return false
		}
	}
	return true
}

func ptr(v float64) *float64 { return &v }

func clamp01(v float64) float64 {
	if !finite(v) {
		return 0
	}
	return math.Max(0, math.Min(1, v))
}

func makeSupport(level SupportLevel, fraction float64, count int) Support {
	return Support{Level: level, Fraction: clamp01(fraction), Count: count}
}

func medianFloat64(values []float64) *float64 {
	clean := make([]float64, 0, len(values))
	for _, v := range values {
		if finite(v) {
			clean = append(clean, v)
		}
	}
	if len(clean) == 0 {
		return nil
	}
	sort.Float64s(clean)
	mid := len(clean) / 2
	if len(clean)%2 == 1 {
		return ptr(clean[mid])
	}
	return ptr((clean[mid-1] + clean[mid]) / 2)
}

func meanFloat64(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

// Sentinel errors. Eligibility shortfalls that are expected on ordinary
// evidence return a typed result with StatusInsufficient instead of an error;
// errors are reserved for malformed or non-finite input.
var (
	ErrInvalidInput  = errors.New("diagnostics: invalid input")
	ErrEmptyInput    = errors.New("diagnostics: empty input")
	ErrNonFinite     = errors.New("diagnostics: non-finite input")
	ErrNegativeValue = errors.New("diagnostics: negative input")
)
