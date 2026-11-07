package diagnostics

import (
	"math"
	"sort"
)

const (
	criticalSpeedWindowDays        = 42
	criticalSpeedMinDuration       = 3 * 60.0
	criticalSpeedMaxDuration       = 15 * 60.0
	criticalSpeedMinWorkouts       = 3
	criticalSpeedMinDurations      = 3
	criticalSpeedMinDurationRatio  = 2.5
	criticalSpeedMaxCSChange       = 0.05
	criticalSpeedMaxRelativeRMSE   = 0.03
	criticalSpeedDurationTolerance = 0.01
)

// CriticalSpeedEffort is one running effort. Duration is seconds, distance is
// metres and mean speed is m/s. MeanSpeedMPS may be zero, in which case
// distance/duration is used. Quality is a positive confidence weight; weights
// are normalised within a workout so one workout can never add more than 1.
type CriticalSpeedEffort struct {
	WorkoutID       string  `json:"workoutID"`
	DurationSeconds float64 `json:"durationSeconds"`
	DistanceMeters  float64 `json:"distanceMeters"`
	MeanSpeedMPS    float64 `json:"meanSpeedMPS,omitempty"`
	Quality         float64 `json:"quality"`
	SourceEpoch     string  `json:"sourceEpoch,omitempty"`
	KnownAt         int64   `json:"knownAt"`
}

// CriticalSpeedInput is the candidate set. AsOf is the right edge of the
// 42-day window; when zero, the latest KnownAt is used.
type CriticalSpeedInput struct {
	AsOf     int64                 `json:"asOf,omitempty"`
	Workouts []CriticalSpeedEffort `json:"workouts"`
}

// CriticalSpeedResult is the research-only CS/D' estimate. Estimate fields are
// nil unless all stability gates pass; R2 alone is never sufficient.
type CriticalSpeedResult struct {
	Header
	AsOf              int64    `json:"asOf"`
	WindowDays        int      `json:"windowDays"`
	WorkoutCount      int      `json:"workoutCount"`
	DistinctDurations int      `json:"distinctDurations"`
	DurationSpanRatio *float64 `json:"durationSpanRatio,omitempty"`
	CSMPS             *float64 `json:"csMPS,omitempty"`
	DPrimeMeters      *float64 `json:"dPrimeMeters,omitempty"`
	R2                *float64 `json:"r2,omitempty"`
	RelativeRMSE      *float64 `json:"relativeRMSE,omitempty"`
	LOOMaxCSChange    *float64 `json:"looMaxCSChange,omitempty"`
}

type criticalEffort struct {
	duration  float64
	distance  float64
	meanSpeed float64
	quality   float64
	source    string
}

type criticalWorkout struct {
	id      string
	efforts []criticalEffort
}

type criticalPoint struct {
	t         float64
	d         float64
	meanSpeed float64
	weight    float64
	workoutID string
	duration  float64
}

type criticalFit struct {
	cs      float64
	dPrime  float64
	r2      float64
	relRMSE float64
	reason  ReasonCode
}

// CalculateCriticalSpeed fits D = CS*t + D' with workout-level weights and
// validates the fit with workout-level leave-one-out refits.
func CalculateCriticalSpeed(in CriticalSpeedInput) (CriticalSpeedResult, error) {
	result := CriticalSpeedResult{
		Header:     newHeader(CriticalSpeedIdentity()),
		AsOf:       in.AsOf,
		WindowDays: criticalSpeedWindowDays,
	}
	if len(in.Workouts) == 0 {
		return CriticalSpeedResult{}, ErrEmptyInput
	}
	if in.AsOf < 0 {
		return CriticalSpeedResult{}, ErrInvalidInput
	}

	// Validate every supplied effort before windowing. Malformed values are
	// input errors, while unsupported test durations are evidence shortfalls.
	var anchor int64
	for _, effort := range in.Workouts {
		if !allFinite(effort.DurationSeconds, effort.DistanceMeters, effort.MeanSpeedMPS, effort.Quality) {
			return CriticalSpeedResult{}, ErrNonFinite
		}
		if effort.DurationSeconds <= 0 || effort.DistanceMeters <= 0 ||
			effort.MeanSpeedMPS < 0 || effort.Quality < 0 || effort.KnownAt < 0 {
			return CriticalSpeedResult{}, ErrInvalidInput
		}
		if effort.KnownAt > anchor {
			anchor = effort.KnownAt
		}
	}
	if in.AsOf > 0 {
		anchor = in.AsOf
	}
	result.AsOf = anchor
	if anchor > 0 {
		cutoff := anchor - criticalSpeedWindowDays*microsecondsPerDay
		filtered := make([]CriticalSpeedEffort, 0, len(in.Workouts))
		for _, effort := range in.Workouts {
			if effort.KnownAt == 0 || effort.KnownAt < cutoff || effort.KnownAt > anchor {
				continue
			}
			filtered = append(filtered, effort)
		}
		if len(filtered) != len(in.Workouts) {
			result.Reasons.Add(ReasonOutsideWindow)
		}
		in.Workouts = filtered
	}
	if len(in.Workouts) == 0 {
		result.Status = StatusCollecting
		result.Reasons.Add(ReasonTooFewWorkouts)
		return result, nil
	}

	groups := make(map[string]*criticalWorkout)
	order := make([]string, 0)
	unsupported := 0
	invalid := 0
	for _, effort := range in.Workouts {
		canonical, ok := standardCriticalDuration(effort.DurationSeconds)
		if !ok {
			unsupported++
			continue
		}
		meanSpeed := effort.MeanSpeedMPS
		if meanSpeed == 0 {
			meanSpeed = effort.DistanceMeters / effort.DurationSeconds
		}
		if !finite(meanSpeed) || meanSpeed <= 0 || effort.Quality <= 0 {
			invalid++
			continue
		}
		group, exists := groups[effort.WorkoutID]
		if !exists {
			group = &criticalWorkout{id: effort.WorkoutID}
			groups[effort.WorkoutID] = group
			order = append(order, effort.WorkoutID)
		}
		// A workout split into several segments contributes each distinct
		// canonical duration at most once, so it cannot manufacture duplicate
		// x values or extra independence.
		duplicate := -1
		for i, existing := range group.efforts {
			if existing.duration == canonical {
				duplicate = i
				break
			}
		}
		candidate := criticalEffort{
			duration:  canonical,
			distance:  effort.DistanceMeters,
			meanSpeed: meanSpeed,
			quality:   effort.Quality,
			source:    effort.SourceEpoch,
		}
		if duplicate >= 0 {
			if candidate.quality > group.efforts[duplicate].quality {
				group.efforts[duplicate] = candidate
			}
			continue
		}
		group.efforts = append(group.efforts, candidate)
	}
	if unsupported > 0 {
		result.Reasons.Add(ReasonUnsupportedDuration)
	}
	if invalid > 0 {
		result.Reasons.Add(ReasonInvalidEffort)
	}

	workouts := make([]criticalWorkout, 0, len(groups))
	for _, id := range order {
		group := groups[id]
		if len(group.efforts) == 0 {
			continue
		}
		sort.SliceStable(group.efforts, func(i, j int) bool {
			return group.efforts[i].duration < group.efforts[j].duration
		})
		workouts = append(workouts, *group)
	}
	result.WorkoutCount = len(workouts)
	durations := make(map[float64]struct{})
	minDuration, maxDuration := math.Inf(1), 0.0
	for _, workout := range workouts {
		for _, effort := range workout.efforts {
			durations[effort.duration] = struct{}{}
			if effort.duration < minDuration {
				minDuration = effort.duration
			}
			if effort.duration > maxDuration {
				maxDuration = effort.duration
			}
		}
	}
	result.DistinctDurations = len(durations)
	if len(durations) > 0 {
		result.DurationSpanRatio = ptr(maxDuration / minDuration)
	}
	if len(workouts) < criticalSpeedMinWorkouts {
		result.Status = StatusCollecting
		result.Reasons.Add(ReasonTooFewWorkouts)
		return result, nil
	}

	if result.DistinctDurations < criticalSpeedMinDurations {
		result.Status = StatusCollecting
		result.Reasons.Add(ReasonTooFewDurations)
		return result, nil
	}
	ratio := maxDuration / minDuration
	if ratio < criticalSpeedMinDurationRatio {
		result.Status = StatusCollecting
		result.Reasons.Add(ReasonDurationSpanLow)
		return result, nil
	}

	sourceKnown := false
	sourceUnknown := false
	var source string
	for _, workout := range workouts {
		for _, effort := range workout.efforts {
			if effort.source == "" {
				sourceUnknown = true
				continue
			}
			if !sourceKnown {
				source = effort.source
				sourceKnown = true
				continue
			}
			if effort.source != source {
				result.Status = StatusInsufficient
				result.Reasons.Add(ReasonSourceMismatch)
				return result, nil
			}
		}
	}
	if !sourceKnown || sourceUnknown {
		result.Reasons.Add(ReasonSourceUnknown)
	}

	points := make([]criticalPoint, 0)
	evidence := make([]EvidenceRef, 0, len(workouts))
	for _, workout := range workouts {
		var qualitySum float64
		for _, effort := range workout.efforts {
			qualitySum += effort.quality
		}
		if qualitySum <= 0 {
			continue
		}
		for _, effort := range workout.efforts {
			points = append(points, criticalPoint{
				t:         effort.duration,
				d:         effort.distance,
				meanSpeed: effort.meanSpeed,
				weight:    effort.quality / qualitySum,
				workoutID: workout.id,
				duration:  effort.duration,
			})
		}
		evidence = append(evidence, EvidenceRef{Kind: "workout", ID: workout.id})
	}
	if len(points) < 2 {
		result.Status = StatusInsufficient
		result.Reasons.Add(ReasonDegenerateFit)
		return result, nil
	}

	full, reason := fitCriticalSpeed(points)
	if reason != "" {
		result.Status = StatusUnstable
		result.Reasons.Add(reason)
		return result, nil
	}
	if !criticalFitPlausible(full, points) {
		result.Status = StatusUnstable
		if full.cs <= 0 || full.cs >= minObservedSpeed(points) {
			result.Reasons.Add(ReasonCSOutsideSpeed)
		}
		if full.dPrime <= 0 {
			result.Reasons.Add(ReasonNonPositiveDPrime)
		}
		if !result.Reasons.Has(ReasonCSOutsideSpeed) && !result.Reasons.Has(ReasonNonPositiveDPrime) {
			result.Reasons.Add(ReasonDegenerateFit)
		}
		return result, nil
	}
	if full.relRMSE > criticalSpeedMaxRelativeRMSE {
		result.Status = StatusUnstable
		result.Reasons.Add(ReasonLOORMSEHigh)
		return result, nil
	}

	maxChange := 0.0
	for _, workout := range workouts {
		subset := make([]criticalPoint, 0, len(points))
		for _, point := range points {
			if point.workoutID != workout.id {
				subset = append(subset, point)
			}
		}
		subsetDurations := distinctPointDurations(subset)
		if subsetDurations < 2 {
			result.Status = StatusUnstable
			result.Reasons.Add(ReasonLOOSingleDuration)
			return result, nil
		}
		leave, leaveReason := fitCriticalSpeed(subset)
		if leaveReason != "" || !criticalFitPlausible(leave, subset) ||
			leave.relRMSE > criticalSpeedMaxRelativeRMSE {
			result.Status = StatusUnstable
			if leaveReason != "" {
				result.Reasons.Add(leaveReason)
			} else if leave.dPrime <= 0 {
				result.Reasons.Add(ReasonNonPositiveDPrime)
			} else if leave.cs <= 0 || leave.cs >= minObservedSpeed(subset) {
				result.Reasons.Add(ReasonCSOutsideSpeed)
			} else {
				result.Reasons.Add(ReasonLOORMSEHigh)
			}
			return result, nil
		}
		change := math.Abs(leave.cs-full.cs) / full.cs
		if !finite(change) || change > criticalSpeedMaxCSChange {
			result.Status = StatusUnstable
			result.Reasons.Add(ReasonLOOCSUnstable)
			return result, nil
		}
		if change > maxChange {
			maxChange = change
		}
	}

	result.CSMPS = ptr(full.cs)
	result.DPrimeMeters = ptr(full.dPrime)
	result.R2 = ptr(full.r2)
	result.RelativeRMSE = ptr(full.relRMSE)
	result.LOOMaxCSChange = ptr(maxChange)
	result.Status = StatusOK
	if !sourceKnown || sourceUnknown {
		result.Status = StatusPartial
	}
	result.Support = makeSupport(SupportHigh, 1, len(workouts))
	result.Evidence = evidence
	return result, nil
}

// CalculateCriticalSpeedFromEfforts is a convenience wrapper around
// CalculateCriticalSpeed that anchors the 42-day window at the latest effort.
func CalculateCriticalSpeedFromEfforts(workouts []CriticalSpeedEffort) (CriticalSpeedResult, error) {
	return CalculateCriticalSpeed(CriticalSpeedInput{Workouts: workouts})
}

func standardCriticalDuration(seconds float64) (float64, bool) {
	for _, standard := range []float64{180, 300, 600, 720} {
		if math.Abs(seconds-standard) <= standard*criticalSpeedDurationTolerance {
			return standard, true
		}
	}
	return 0, false
}

func fitCriticalSpeed(points []criticalPoint) (criticalFit, ReasonCode) {
	var sw, st, stt, sd, std float64
	for _, point := range points {
		if !allFinite(point.t, point.d, point.weight) || point.t <= 0 || point.d <= 0 || point.weight <= 0 {
			return criticalFit{}, ReasonInvalidInput
		}
		sw += point.weight
		st += point.weight * point.t
		stt += point.weight * point.t * point.t
		sd += point.weight * point.d
		std += point.weight * point.t * point.d
	}
	if sw <= 0 {
		return criticalFit{}, ReasonDegenerateFit
	}
	det := sw*stt - st*st
	if math.Abs(det) <= 1e-12 {
		return criticalFit{}, ReasonDegenerateFit
	}
	cs := (sw*std - st*sd) / det
	dPrime := (stt*sd - st*std) / det
	meanD := sd / sw
	var sse, sst, relativeSquares float64
	for _, point := range points {
		predicted := cs*point.t + dPrime
		residual := predicted - point.d
		sse += point.weight * residual * residual
		delta := point.d - meanD
		sst += point.weight * delta * delta
		ratio := residual / point.d
		relativeSquares += point.weight * ratio * ratio
	}
	if sst <= 1e-12 || !allFinite(cs, dPrime, sse, sst) {
		return criticalFit{}, ReasonDegenerateFit
	}
	r2 := 1 - sse/sst
	relRMSE := math.Sqrt(relativeSquares / sw)
	if !allFinite(r2, relRMSE) {
		return criticalFit{}, ReasonDegenerateFit
	}
	return criticalFit{cs: cs, dPrime: dPrime, r2: r2, relRMSE: relRMSE}, ""
}

func criticalFitPlausible(fit criticalFit, points []criticalPoint) bool {
	if !finite(fit.cs) || !finite(fit.dPrime) || fit.cs <= 0 || fit.dPrime <= 0 {
		return false
	}
	return fit.cs < minObservedSpeed(points)
}

func minObservedSpeed(points []criticalPoint) float64 {
	minimum := math.Inf(1)
	for _, point := range points {
		if point.meanSpeed < minimum {
			minimum = point.meanSpeed
		}
	}
	return minimum
}

func distinctPointDurations(points []criticalPoint) int {
	seen := make(map[float64]struct{}, len(points))
	for _, point := range points {
		seen[point.duration] = struct{}{}
	}
	return len(seen)
}
