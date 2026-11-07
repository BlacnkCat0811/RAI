package diagnostics

import "sort"

const (
	aerobicMinValidSeconds = 300.0
	aerobicMinHRSupport    = 0.90
	aerobicTrendWindowDays = 42
	aerobicTrendMinRuns    = 3
)

// SteadySupport is a common steady-state segment shared by speed and heart
// rate. HeartRateBPM nil means the segment has no HR support. ValidSeconds is
// the duration for which both speed and the declared HR support are usable.
// HRSupportFraction is the fraction of ValidSeconds with valid HR, in [0,1].
type SteadySupport struct {
	SpeedMPS          *float64    `json:"speedMPS,omitempty"`
	HeartRateBPM      *float64    `json:"heartRateBPM,omitempty"`
	ValidSeconds      float64     `json:"validSeconds"`
	HRSupportFraction float64     `json:"hrSupportFraction"`
	SourceEpoch       string      `json:"sourceEpoch,omitempty"`
	Terrain           string      `json:"terrain,omitempty"`
	Environment       Environment `json:"environment,omitempty"`
	KnownAt           int64       `json:"knownAt"`
}

// AerobicEfficiencyInput is one workout's common steady support. Multiple
// steady segments are pooled only within this workout.
type AerobicEfficiencyInput struct {
	WorkoutID string          `json:"workoutID"`
	Supports  []SteadySupport `json:"supports"`
}

// AerobicEfficiencyResult is the research-only aerobic efficiency bypass. The
// value is speed in metres per minute divided by heart rate, with unit
// m/beat. It deliberately has no HRmax or resting-HR input.
type AerobicEfficiencyResult struct {
	Header
	WorkoutID          string   `json:"workoutID"`
	KnownAt            int64    `json:"knownAt"`
	SourceEpoch        string   `json:"sourceEpoch,omitempty"`
	Terrain            string   `json:"terrain,omitempty"`
	ValidSeconds       float64  `json:"validSeconds"`
	HRSupport          float64  `json:"hrSupport"`
	SpeedMPerMin       *float64 `json:"speedMPerMin,omitempty"`
	HeartRateBPM       *float64 `json:"heartRateBPM,omitempty"`
	EfficiencyMPerBeat *float64 `json:"efficiencyMPerBeat,omitempty"`
}

// AerobicEfficiencyTrendResult is a same-context trend. It is descriptive and
// never a VO2/economy claim.
type AerobicEfficiencyTrendResult struct {
	Header
	AsOf               int64          `json:"asOf"`
	WindowDays         int            `json:"windowDays"`
	ReferenceWorkoutID string         `json:"referenceWorkoutID,omitempty"`
	ComparableRuns     int            `json:"comparableRuns"`
	MedianEfficiency   *float64       `json:"medianEfficiencyMPerBeat,omitempty"`
	EfficiencyTrend    TrendDirection `json:"efficiencyTrend"`
}

// CalculateAerobicEfficiency computes v(m/min)/HR on qualified common steady
// support. It does not require HRmax, resting HR, or any other reference.
func CalculateAerobicEfficiency(in AerobicEfficiencyInput) (AerobicEfficiencyResult, error) {
	result := AerobicEfficiencyResult{Header: newHeader(AerobicEfficiencyIdentity()), WorkoutID: in.WorkoutID}
	if len(in.Supports) == 0 {
		return AerobicEfficiencyResult{}, ErrEmptyInput
	}

	var totalValid, weightedSpeed, weightedHR, weightedHRSupport float64
	var speedWeight, hrWeight float64
	source := ""
	terrain := ""
	sourceKnown := false
	terrainKnown := false
	var latestKnownAt int64
	for _, support := range in.Supports {
		if !allFinite(support.ValidSeconds, support.HRSupportFraction) {
			return AerobicEfficiencyResult{}, ErrNonFinite
		}
		if support.ValidSeconds <= 0 {
			return AerobicEfficiencyResult{}, ErrInvalidInput
		}
		if support.HRSupportFraction < 0 || support.HRSupportFraction > 1 {
			return AerobicEfficiencyResult{}, ErrInvalidInput
		}
		if support.SpeedMPS == nil {
			continue
		}
		if !finite(*support.SpeedMPS) {
			return AerobicEfficiencyResult{}, ErrNonFinite
		}
		if *support.SpeedMPS <= 0 {
			return AerobicEfficiencyResult{}, ErrInvalidInput
		}
		if support.HeartRateBPM != nil {
			if !finite(*support.HeartRateBPM) {
				return AerobicEfficiencyResult{}, ErrNonFinite
			}
			if *support.HeartRateBPM <= 0 {
				return AerobicEfficiencyResult{}, ErrInvalidInput
			}
		}
		if support.Environment.TemperatureC != nil && !finite(*support.Environment.TemperatureC) {
			return AerobicEfficiencyResult{}, ErrNonFinite
		}
		totalValid += support.ValidSeconds
		speedWeight += support.ValidSeconds
		weightedSpeed += *support.SpeedMPS * support.ValidSeconds
		if support.HeartRateBPM != nil && support.HRSupportFraction > 0 {
			hrSeconds := support.ValidSeconds * support.HRSupportFraction
			hrWeight += hrSeconds
			weightedHR += *support.HeartRateBPM * hrSeconds
			weightedHRSupport += support.HRSupportFraction * support.ValidSeconds
		}
		if support.SourceEpoch != "" {
			if sourceKnown && source != support.SourceEpoch {
				result.Status = StatusInsufficient
				result.Reasons.Add(ReasonSourceMismatch)
				return result, nil
			}
			source = support.SourceEpoch
			sourceKnown = true
		}
		if support.Terrain != "" {
			if terrainKnown && terrain != support.Terrain {
				result.Status = StatusInsufficient
				result.Reasons.Add(ReasonTerrainMismatch)
				return result, nil
			}
			terrain = support.Terrain
			terrainKnown = true
		}
		if support.KnownAt > latestKnownAt {
			latestKnownAt = support.KnownAt
		}
	}
	result.ValidSeconds = totalValid
	if totalValid < aerobicMinValidSeconds || speedWeight < aerobicMinValidSeconds {
		result.Status = StatusInsufficient
		result.Reasons.Add(ReasonSteadyTooShort)
		return result, nil
	}
	result.SourceEpoch = source
	result.Terrain = terrain
	result.KnownAt = latestKnownAt
	if !sourceKnown {
		result.Reasons.Add(ReasonSourceUnknown)
	}
	if !terrainKnown {
		result.Reasons.Add(ReasonTerrainUnknown)
	}

	hrSupport := 0.0
	if totalValid > 0 {
		hrSupport = clamp01(weightedHRSupport / totalValid)
	}
	result.HRSupport = hrSupport
	if hrWeight <= 0 {
		result.Status = StatusInsufficient
		result.Reasons.Add(ReasonMissingHR)
		return result, nil
	}
	if hrSupport < aerobicMinHRSupport {
		result.Status = StatusInsufficient
		result.Reasons.Add(ReasonHRSupportFraction)
		return result, nil
	}

	speed := weightedSpeed / speedWeight
	hr := weightedHR / hrWeight
	if !allFinite(speed, hr) || speed <= 0 || hr <= 0 {
		result.Status = StatusInsufficient
		result.Reasons.Add(ReasonInvalidInput)
		return result, nil
	}
	efficiency := (speed * 60) / hr
	if !finite(efficiency) || efficiency <= 0 {
		result.Status = StatusInsufficient
		result.Reasons.Add(ReasonInvalidInput)
		return result, nil
	}
	result.SpeedMPerMin = ptr(speed * 60)
	result.HeartRateBPM = ptr(hr)
	result.EfficiencyMPerBeat = ptr(efficiency)

	result.Status = StatusOK
	level := SupportModerate
	if hrSupport >= 0.95 && totalValid >= 600 {
		level = SupportHigh
	}
	if !sourceKnown || !terrainKnown {
		// Context is descriptive for the single-workout bypass; it becomes a
		// hard requirement only in the same-context trend.
		level = SupportLow
	}
	result.Support = makeSupport(level, hrSupport, 1)
	result.Evidence = []EvidenceRef{{Kind: "workout", ID: in.WorkoutID}}
	return result, nil
}

// CalculateAerobicEfficiencyTrend compares only runs with the same non-empty
// source epoch and terrain as the latest qualified run. Fewer than three
// comparable runs return StatusCollecting.
func CalculateAerobicEfficiencyTrend(history []AerobicEfficiencyResult, asOf int64) (AerobicEfficiencyTrendResult, error) {
	result := AerobicEfficiencyTrendResult{
		Header:          newHeader(AerobicEfficiencyIdentity()),
		AsOf:            asOf,
		WindowDays:      aerobicTrendWindowDays,
		EfficiencyTrend: TrendUnknown,
	}
	if asOf <= 0 {
		return AerobicEfficiencyTrendResult{}, ErrInvalidInput
	}
	cutoff := asOf - aerobicTrendWindowDays*microsecondsPerDay
	candidates := make([]AerobicEfficiencyResult, 0, len(history))
	for _, run := range history {
		if run.KnownAt <= 0 || run.KnownAt > asOf || run.KnownAt < cutoff {
			continue
		}
		if run.Status != StatusOK && run.Status != StatusPartial {
			continue
		}
		if run.WorkoutID == "" || run.EfficiencyMPerBeat == nil ||
			!finite(*run.EfficiencyMPerBeat) || *run.EfficiencyMPerBeat <= 0 {
			continue
		}
		candidates = append(candidates, run)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].KnownAt == candidates[j].KnownAt {
			return candidates[i].WorkoutID < candidates[j].WorkoutID
		}
		return candidates[i].KnownAt < candidates[j].KnownAt
	})
	candidates = dedupeAerobicRuns(candidates)
	if len(candidates) < aerobicTrendMinRuns {
		result.Status = StatusCollecting
		result.Reasons.Add(ReasonInsufficientRuns)
		result.Support = makeSupport(SupportNone, 0, len(candidates))
		return result, nil
	}
	reference := candidates[len(candidates)-1]
	result.ReferenceWorkoutID = reference.WorkoutID
	if reference.SourceEpoch == "" || reference.Terrain == "" {
		result.Status = StatusCollecting
		result.Reasons.Add(ReasonContextUnknown)
		return result, nil
	}
	matched := make([]AerobicEfficiencyResult, 0, len(candidates))
	for _, run := range candidates {
		if run.SourceEpoch != reference.SourceEpoch || run.Terrain != reference.Terrain {
			continue
		}
		matched = append(matched, run)
	}
	result.ComparableRuns = len(matched)
	if len(matched) < aerobicTrendMinRuns {
		result.Status = StatusCollecting
		result.Reasons.Add(ReasonInsufficientRuns)
		result.Support = makeSupport(SupportNone, 0, len(matched))
		return result, nil
	}
	values := make([]float64, 0, len(matched))
	points := make([]trendPoint, 0, len(matched))
	earliest := matched[0].KnownAt
	for _, run := range matched {
		values = append(values, *run.EfficiencyMPerBeat)
		points = append(points, trendPoint{
			x: float64(run.KnownAt-earliest) / float64(microsecondsPerDay),
			y: *run.EfficiencyMPerBeat,
		})
	}
	result.MedianEfficiency = medianFloat64(values)
	result.EfficiencyTrend = linearTrend(points)
	result.Status = StatusOK
	result.Support = makeSupport(SupportHigh, 1, len(matched))
	return result, nil
}

func dedupeAerobicRuns(runs []AerobicEfficiencyResult) []AerobicEfficiencyResult {
	out := make([]AerobicEfficiencyResult, 0, len(runs))
	index := make(map[string]int, len(runs))
	for _, run := range runs {
		if at, ok := index[run.WorkoutID]; ok {
			out[at] = run
			continue
		}
		index[run.WorkoutID] = len(out)
		out = append(out, run)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].KnownAt == out[j].KnownAt {
			return out[i].WorkoutID < out[j].WorkoutID
		}
		return out[i].KnownAt < out[j].KnownAt
	})
	return out
}
