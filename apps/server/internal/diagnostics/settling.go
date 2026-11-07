package diagnostics

import "math"

const (
	settlingTimeConstantHours = 36.0
	settlingBaselineFraction  = 0.25
	settlingScoreGate         = 85.0
)

// Bucket labels are stable display values, not recovery or medical stages.
const (
	SettlingBucketUnder12     = "<12"
	SettlingBucket12To24      = "[12,24)"
	SettlingBucket24To36      = "[24,36)"
	SettlingBucket36To48      = "[36,48)"
	SettlingBucketOver48      = ">48"
	SettlingBucketUnavailable = ""
)

// SettlingResult is a research-only load-settling window. Hours is the time
// for modelled load residual D/B to reach 0.25 if no new training is added.
// It does not mean full recovery, readiness, or a sleep prediction.
type SettlingResult struct {
	Header
	Load             float64  `json:"load"`
	Baseline         float64  `json:"baseline"`
	Score            float64  `json:"score"`
	ScoreConfidence  float64  `json:"scoreConfidence"`
	CoverageComplete bool     `json:"coverageComplete"`
	Hours            *float64 `json:"hours,omitempty"`
	Bucket           string   `json:"bucket,omitempty"`
}

// Result is an alias for SettlingResult so the settling function can keep the
// compact signature named in the contract without creating another model.
type Result = SettlingResult

// SettlingWindow computes h=36*max(0, ln(D/(0.25*B))) hours. D=0 is defined
// as h=0. A bucket is emitted only when the score is at least 85, coverage is
// complete, and a positive baseline exists. Non-finite or negative values are
// input errors; a zero baseline is an unavailable baseline, not an error.
func SettlingWindow(load, baseline, score float64, coverageComplete bool, scoreConfidence float64) (Result, error) {
	result := Result{Header: newHeader(LoadSettlingIdentity())}
	result.Load = load
	result.Baseline = baseline
	result.Score = score
	result.ScoreConfidence = scoreConfidence
	result.CoverageComplete = coverageComplete

	if !allFinite(load, baseline, score, scoreConfidence) {
		return Result{}, ErrNonFinite
	}
	if load < 0 || baseline < 0 || score < 0 || scoreConfidence < 0 {
		return Result{}, ErrNegativeValue
	}
	if scoreConfidence > 100 {
		return Result{}, ErrInvalidInput
	}
	if scoreConfidence > 1 {
		scoreConfidence /= 100
		result.ScoreConfidence = scoreConfidence
	}

	switch {
	case load == 0:
		result.Hours = ptr(0)
	case baseline == 0:
		result.Status = StatusInsufficient
		result.Reasons.Add(ReasonBaselineMissing)
		result.Support = makeSupport(SupportNone, 0, 1)
		return result, nil
	default:
		hours := settlingTimeConstantHours * math.Max(0, math.Log(load/(settlingBaselineFraction*baseline)))
		if !finite(hours) || hours < 0 {
			return Result{}, ErrNonFinite
		}
		result.Hours = ptr(hours)
	}

	result.Support = makeSupport(settlingSupportLevel(scoreConfidence, coverageComplete), scoreConfidence, 1)
	if baseline == 0 {
		result.Reasons.Add(ReasonBaselineMissing)
	}
	if score < settlingScoreGate {
		result.Reasons.Add(ReasonScoreBelowGate)
	}
	if !coverageComplete {
		result.Reasons.Add(ReasonCoverageIncomplete)
	}
	if result.Hours == nil || baseline == 0 || score < settlingScoreGate || !coverageComplete {
		result.Status = StatusPartial
		return result, nil
	}
	result.Bucket = SettlingBucket(*result.Hours)
	if result.Bucket == SettlingBucketUnavailable {
		result.Status = StatusPartial
		return result, nil
	}
	result.Status = StatusOK
	return result, nil
}

// SettlingBucket maps a non-negative, finite hour value to the stable display
// buckets. Invalid input returns SettlingBucketUnavailable. Values at 12, 24
// and 36 start the following bucket; 48 starts the overflow bucket.
func SettlingBucket(hours float64) string {
	if !finite(hours) || hours < 0 {
		return SettlingBucketUnavailable
	}
	switch {
	case hours < 12:
		return SettlingBucketUnder12
	case hours < 24:
		return SettlingBucket12To24
	case hours < 36:
		return SettlingBucket24To36
	case hours < 48:
		return SettlingBucket36To48
	default:
		return SettlingBucketOver48
	}
}

func settlingSupportLevel(confidence float64, coverageComplete bool) SupportLevel {
	if !coverageComplete {
		return SupportLow
	}
	switch {
	case confidence >= 0.9:
		return SupportHigh
	case confidence >= 0.6:
		return SupportModerate
	case confidence > 0:
		return SupportLow
	default:
		return SupportNone
	}
}
