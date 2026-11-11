package trainingload

import (
	"fmt"
	"slices"
	"sort"

	wf "rai/server/internal/workoutfeatures"
)

// Pace approximation (bounded v2 policy only): when a run has no usable heart
// rate, its load is estimated from its mean active speed relative to the
// 60-minute ability v60, with the athlete's own HR-load calibration:
//
//	AU ≈ k · active minutes · (v / v60)²
//
// k is the median of AU / (active minutes · (v/v60)²) over the athlete's
// recent complete-tier HR loads. Without v60 or enough calibration runs no
// estimate is made.
const (
	PaceCalibrationMinimum = 5
	PaceConfidence         = .5
)

// PaceIntensity is active minutes · (v/v60)² for one run, from its features.
func PaceIntensity(f wf.WorkoutFeatures, v60 float64) (float64, bool) {
	if f.Timing.Active == nil || *f.Timing.Active <= 0 || f.Distance.Preferred == nil || *f.Distance.Preferred <= 0 || !finite(v60) || v60 <= 0 {
		return 0, false
	}
	v := *f.Distance.Preferred / *f.Timing.Active
	if !finite(v) || v < .5 || v > 12 {
		return 0, false
	}
	return *f.Timing.Active / 60 * (v / v60) * (v / v60), true
}

// CalibratePace returns the median AU-per-intensity ratio, or false with
// fewer than PaceCalibrationMinimum calibration runs.
func CalibratePace(ratios []float64) (float64, bool) {
	xs := slices.DeleteFunc(append([]float64{}, ratios...), func(x float64) bool { return !finite(x) || x <= 0 })
	if len(xs) < PaceCalibrationMinimum {
		return 0, false
	}
	sort.Float64s(xs)
	n := len(xs)
	if n%2 == 1 {
		return xs[n/2], true
	}
	return xs[n/2-1]/2 + xs[n/2]/2, true
}

// paceEligible: only a missing or thin HR trace may be replaced; any other
// gate (active time, sport, reference, overlap, deletion) keeps the load unknown.
func paceEligible(r Result) bool {
	if r.Status != "insufficient_data" || r.Load != nil {
		return false
	}
	hrGap := false
	for _, c := range r.ReasonCodes {
		switch c {
		case "missing_hr", "hr_coverage_low":
			hrGap = true
		case "short_session_hr_lag", "source_conflict_excluded", "hr_above_reference_max", "hr_below_reference_rest":
		default:
			return false
		}
	}
	return hrGap
}

// ApplyPaceApproximation fills an HR-less v2 load in place. It reports whether
// it did; the result is tier approximate, quality partial, confidence 0.5.
func ApplyPaceApproximation(r *Result, f wf.WorkoutFeatures, v60, k float64) bool {
	intensity, ok := PaceIntensity(f, v60)
	if !ok || !paceEligible(*r) || !finite(k) || k <= 0 {
		return false
	}
	v := k * intensity
	perSecond := v / *f.Timing.Active
	for i := range r.Days {
		d := &r.Days[i]
		if d.ActiveSeconds == nil {
			return false
		}
		d.Load = ptr(*d.ActiveSeconds * perSecond)
		d.Quality = "partial"
	}
	r.Load, r.EstimatedLoad = ptr(v), ptr(v)
	r.Status, r.Quality, r.QualityTier = "available", "partial", TierApproximate
	r.Method, r.Confidence = "pace_approximation", PaceConfidence
	r.ReasonCodes = append(r.ReasonCodes, "pace_approximated")
	r.Evidence["assumption"] = "no usable HR: active minutes x (mean speed / v60)^2, scaled by the athlete's own HR-load calibration"
	r.Evidence["paceCalibration"] = fmt.Sprintf("%.6g", k)
	r.Evidence["paceV60"] = fmt.Sprintf("%.6g", v60)
	return true
}
