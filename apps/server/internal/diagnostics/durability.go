package diagnostics

import (
	"math"
	"sort"
)

const (
	durabilityWarmupSeconds         = 10 * 60.0
	durabilityMinTotalSeconds       = 60 * 60.0
	durabilityHalfMinSeconds        = 20 * 60.0
	durabilitySupportGate           = 0.90
	durabilityMaxSpeedCV            = 0.10
	durabilityHalfDurationTolerance = 0.05
	durabilityPaceShiftGate         = 0.05
	durabilityTrendDays             = 42
)

// Environment is optional per-sample context. Nil means unknown; a known value
// is never imputed from population defaults.
type Environment struct {
	Indoor       *bool    `json:"indoor,omitempty"`
	TemperatureC *float64 `json:"temperatureC,omitempty"`
	Humidity     *float64 `json:"humidity,omitempty"`
}

// DurabilitySample is one interval of a long run. Speed and heart rate are
// pointers because missing support is different from a zero measurement.
// Paused intervals are never concatenated with active intervals.
type DurabilitySample struct {
	StartSeconds float64     `json:"startSeconds"`
	EndSeconds   float64     `json:"endSeconds"`
	SpeedMPS     *float64    `json:"speedMPS,omitempty"`
	HeartRateBPM *float64    `json:"heartRateBPM,omitempty"`
	Paused       bool        `json:"paused,omitempty"`
	SourceEpoch  string      `json:"sourceEpoch,omitempty"`
	Terrain      string      `json:"terrain,omitempty"`
	Environment  Environment `json:"environment,omitempty"`
	KnownAt      int64       `json:"knownAt"`
}

// DurabilityInput is one long-run workout. A run may contain pauses; only the
// longest single active segment is analysed, so a pause can never be spliced.
type DurabilityInput struct {
	WorkoutID string             `json:"workoutID"`
	Samples   []DurabilitySample `json:"samples"`
}

// DurabilityHalf describes one equal-clock half after the 10-minute warm-up.
type DurabilityHalf struct {
	StartSeconds  float64  `json:"startSeconds"`
	EndSeconds    float64  `json:"endSeconds"`
	ValidSeconds  float64  `json:"validSpeedSeconds"`
	SpeedSupport  float64  `json:"speedSupport"`
	HRSupport     float64  `json:"hrSupport"`
	MeanSpeedMPS  *float64 `json:"meanSpeedMPS,omitempty"`
	MeanHeartRate *float64 `json:"meanHeartRateBPM,omitempty"`
}

// DurabilityResult is the research-only long-run durability result.
type DurabilityResult struct {
	Header
	WorkoutID                string          `json:"workoutID"`
	KnownAt                  int64           `json:"knownAt"`
	SourceEpoch              string          `json:"sourceEpoch,omitempty"`
	Terrain                  string          `json:"terrain,omitempty"`
	TemperatureC             *float64        `json:"temperatureC,omitempty"`
	DurationSeconds          float64         `json:"durationSeconds"`
	AnalyzedSeconds          float64         `json:"analyzedSeconds"`
	LongestContinuousSeconds float64         `json:"longestContinuousSeconds"`
	MeanSpeedMPS             *float64        `json:"meanSpeedMPS,omitempty"`
	SpeedCV                  *float64        `json:"speedCV,omitempty"`
	SpeedSupport             float64         `json:"speedSupport"`
	HRSupport                float64         `json:"hrSupport"`
	JointSupport             float64         `json:"jointSupport"`
	FirstHalf                *DurabilityHalf `json:"firstHalf,omitempty"`
	SecondHalf               *DurabilityHalf `json:"secondHalf,omitempty"`
	EF1                      *float64        `json:"ef1,omitempty"`
	EF2                      *float64        `json:"ef2,omitempty"`
	Decoupling               *float64        `json:"decoupling,omitempty"`
	PaceDecay                *float64        `json:"paceDecay,omitempty"`
}

// DurabilityTrendResult is the 42-day same-context durability trend. Medians
// are calculated only over independent, comparable qualified long runs.
type DurabilityTrendResult struct {
	Header
	AsOf               int64          `json:"asOf"`
	WindowDays         int            `json:"windowDays"`
	ReferenceWorkoutID string         `json:"referenceWorkoutID,omitempty"`
	ComparableRuns     int            `json:"comparableRuns"`
	MedianEF           *float64       `json:"medianEF,omitempty"`
	MedianDecoupling   *float64       `json:"medianDecoupling,omitempty"`
	MedianPaceDecay    *float64       `json:"medianPaceDecay,omitempty"`
	EFTrend            TrendDirection `json:"efTrend"`
	DecouplingTrend    TrendDirection `json:"decouplingTrend"`
	PaceDecayTrend     TrendDirection `json:"paceDecayTrend"`
}

type durabilitySegment struct {
	start   float64
	end     float64
	samples []DurabilitySample
}

type durabilityAccumulator struct {
	validSpeed float64
	validHR    float64
	joint      float64
	distance   float64
	hrWeighted float64
}

// CalculateDurability evaluates one long run against the durability gates.
// Malformed/non-finite input returns an error; ordinary evidence shortfalls
// return a typed result with StatusInsufficient and stable reason codes.
func CalculateDurability(in DurabilityInput) (DurabilityResult, error) {
	result := DurabilityResult{Header: newHeader(WorkoutDurabilityIdentity())}
	result.WorkoutID = in.WorkoutID

	samples, err := validateDurabilitySamples(in.Samples)
	if err != nil {
		return DurabilityResult{}, err
	}

	segments := durabilitySegments(samples)
	if len(segments) == 0 {
		result.Status = StatusInsufficient
		result.Reasons.Add(ReasonNoActiveSegment)
		return result, nil
	}
	if len(segments) > 1 {
		result.Reasons.Add(ReasonPauseSpanExcluded)
	}
	sort.SliceStable(segments, func(i, j int) bool {
		return segments[i].end-segments[i].start > segments[j].end-segments[j].start
	})
	seg := segments[0]
	result.DurationSeconds = seg.end - seg.start
	result.LongestContinuousSeconds = result.DurationSeconds

	if result.DurationSeconds < durabilityMinTotalSeconds {
		result.Status = StatusInsufficient
		result.Reasons.Add(ReasonDurationTooShort)
		return result, nil
	}

	source, terrain, contextReason := durabilityContext(seg.samples)
	if contextReason != "" {
		result.Status = StatusInsufficient
		result.Reasons.Add(contextReason)
		return result, nil
	}
	result.SourceEpoch = source
	result.Terrain = terrain
	if source == "" {
		result.Reasons.Add(ReasonSourceUnknown)
	}
	if terrain == "" {
		result.Reasons.Add(ReasonTerrainUnknown)
	}
	result.TemperatureC = weightedTemperature(seg.samples, seg.start, seg.end)
	if result.TemperatureC == nil {
		result.Reasons.Add(ReasonTemperatureUnknown)
	}
	result.KnownAt = latestKnownAt(seg.samples)

	analysisStart := seg.start + durabilityWarmupSeconds
	analysisEnd := seg.end
	if analysisEnd <= analysisStart {
		result.Status = StatusInsufficient
		result.Reasons.Add(ReasonDurationTooShort)
		return result, nil
	}
	result.AnalyzedSeconds = analysisEnd - analysisStart
	mid := (analysisStart + analysisEnd) / 2

	meanSpeed, cv, cvOK := weightedSpeedStats(seg.samples, analysisStart, analysisEnd)
	if cvOK {
		result.MeanSpeedMPS = ptr(meanSpeed)
		result.SpeedCV = ptr(cv)
	}

	firstAcc := accumulateDurabilityHalf(seg.samples, analysisStart, mid)
	secondAcc := accumulateDurabilityHalf(seg.samples, mid, analysisEnd)
	halfClock := (analysisEnd - analysisStart) / 2

	firstHalf := durabilityHalf(analysisStart, mid, halfClock, firstAcc)
	secondHalf := durabilityHalf(mid, analysisEnd, halfClock, secondAcc)
	result.FirstHalf = firstHalf
	result.SecondHalf = secondHalf

	totalSpeed := firstAcc.validSpeed + secondAcc.validSpeed
	totalHR := firstAcc.validHR + secondAcc.validHR
	totalJoint := firstAcc.joint + secondAcc.joint
	result.SpeedSupport = clamp01(totalSpeed / result.AnalyzedSeconds)
	result.HRSupport = clamp01(totalHR / result.AnalyzedSeconds)
	result.JointSupport = clamp01(totalJoint / result.AnalyzedSeconds)

	if !cvOK || result.SpeedSupport < durabilitySupportGate ||
		firstAcc.validSpeed/halfClock < durabilitySupportGate ||
		secondAcc.validSpeed/halfClock < durabilitySupportGate {
		result.Status = StatusInsufficient
		result.Reasons.Add(ReasonSpeedSupportLow)
		return result, nil
	}
	if cv > durabilityMaxSpeedCV {
		result.Status = StatusInsufficient
		result.Reasons.Add(ReasonSpeedCVHigh)
		return result, nil
	}
	if firstAcc.validSpeed < durabilityHalfMinSeconds || secondAcc.validSpeed < durabilityHalfMinSeconds {
		result.Status = StatusInsufficient
		result.Reasons.Add(ReasonHalfTooShort)
		return result, nil
	}
	if halfDurationDifference(firstAcc.validSpeed, secondAcc.validSpeed) > durabilityHalfDurationTolerance {
		result.Status = StatusInsufficient
		result.Reasons.Add(ReasonHalvesUnequal)
		return result, nil
	}
	if firstHalf.MeanSpeedMPS == nil || secondHalf.MeanSpeedMPS == nil {
		result.Status = StatusInsufficient
		result.Reasons.Add(ReasonSpeedSupportLow)
		return result, nil
	}

	v1, v2 := *firstHalf.MeanSpeedMPS, *secondHalf.MeanSpeedMPS
	if v1 <= 0 || v2 <= 0 {
		result.Status = StatusInsufficient
		result.Reasons.Add(ReasonInvalidInput)
		return result, nil
	}
	result.PaceDecay = ptr(1 - v2/v1)

	hrMetricsAvailable := totalHR > 0 &&
		firstAcc.validHR/halfClock >= durabilitySupportGate &&
		secondAcc.validHR/halfClock >= durabilitySupportGate &&
		firstAcc.joint/halfClock >= durabilitySupportGate &&
		secondAcc.joint/halfClock >= durabilitySupportGate

	if !hrMetricsAvailable {
		result.Status = StatusPartial
		if totalHR == 0 {
			result.Reasons.Add(ReasonMissingHR)
		} else {
			result.Reasons.Add(ReasonHRSupportLow)
		}
		result.Support = makeSupport(SupportLow, result.SpeedSupport, 2)
		return result, nil
	}

	hr1, hr2 := *firstHalf.MeanHeartRate, *secondHalf.MeanHeartRate
	if hr1 <= 0 || hr2 <= 0 {
		result.Status = StatusPartial
		result.Reasons.Add(ReasonMissingHR)
		result.Support = makeSupport(SupportLow, result.SpeedSupport, 2)
		return result, nil
	}
	ef1 := (v1 * 60) / hr1
	ef2 := (v2 * 60) / hr2
	if !allFinite(ef1, ef2) || ef1 <= 0 || ef2 <= 0 {
		result.Status = StatusInsufficient
		result.Reasons.Add(ReasonInvalidInput)
		return result, nil
	}
	result.EF1 = ptr(ef1)
	result.EF2 = ptr(ef2)

	decouplingSuppressed := false
	paceShift := math.Abs(v2-v1) / v1
	if paceShift > durabilityPaceShiftGate {
		result.Reasons.Add(ReasonPaceShiftConfoundsDrift)
		decouplingSuppressed = true
	} else {
		decoupling := 1 - ef2/ef1
		if !finite(decoupling) {
			result.Status = StatusInsufficient
			result.Reasons.Add(ReasonInvalidInput)
			return result, nil
		}
		result.Decoupling = ptr(decoupling)
	}

	result.Status = StatusOK
	if decouplingSuppressed {
		result.Status = StatusPartial
	}
	level := SupportModerate
	if result.JointSupport >= 0.95 && cv <= 0.05 {
		level = SupportHigh
	}
	if result.SourceEpoch == "" || result.Terrain == "" || result.TemperatureC == nil {
		result.Status = StatusPartial
		level = SupportLow
	}
	result.Support = makeSupport(level, result.JointSupport, 2)
	result.Evidence = []EvidenceRef{{Kind: "workout", ID: in.WorkoutID}}
	return result, nil
}

func validateDurabilitySamples(samples []DurabilitySample) ([]DurabilitySample, error) {
	if len(samples) == 0 {
		return nil, ErrEmptyInput
	}
	out := append([]DurabilitySample(nil), samples...)
	for _, sample := range out {
		if !allFinite(sample.StartSeconds, sample.EndSeconds) {
			return nil, ErrNonFinite
		}
		if sample.EndSeconds <= sample.StartSeconds {
			return nil, ErrInvalidInput
		}
		if sample.SpeedMPS != nil {
			if !finite(*sample.SpeedMPS) {
				return nil, ErrNonFinite
			}
			if *sample.SpeedMPS <= 0 {
				return nil, ErrInvalidInput
			}
		}
		if sample.HeartRateBPM != nil {
			if !finite(*sample.HeartRateBPM) {
				return nil, ErrNonFinite
			}
			if *sample.HeartRateBPM <= 0 {
				return nil, ErrInvalidInput
			}
		}
		if sample.Environment.TemperatureC != nil && !finite(*sample.Environment.TemperatureC) {
			return nil, ErrNonFinite
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StartSeconds == out[j].StartSeconds {
			return out[i].EndSeconds < out[j].EndSeconds
		}
		return out[i].StartSeconds < out[j].StartSeconds
	})
	var previousEnd float64
	for i, sample := range out {
		if i > 0 && sample.StartSeconds < previousEnd-1e-9 {
			return nil, ErrInvalidInput
		}
		if sample.EndSeconds > previousEnd {
			previousEnd = sample.EndSeconds
		}
	}
	return out, nil
}

func durabilitySegments(samples []DurabilitySample) []durabilitySegment {
	var segments []durabilitySegment
	var current *durabilitySegment
	flush := func() {
		if current != nil {
			segments = append(segments, *current)
			current = nil
		}
	}
	for _, sample := range samples {
		if sample.Paused {
			flush()
			continue
		}
		if current == nil {
			current = &durabilitySegment{start: sample.StartSeconds, end: sample.EndSeconds}
			current.samples = append(current.samples, sample)
			continue
		}
		if sample.StartSeconds <= current.end+1e-9 {
			current.samples = append(current.samples, sample)
			if sample.EndSeconds > current.end {
				current.end = sample.EndSeconds
			}
			continue
		}
		flush()
		current = &durabilitySegment{start: sample.StartSeconds, end: sample.EndSeconds}
		current.samples = append(current.samples, sample)
	}
	flush()
	return segments
}

func durabilityContext(samples []DurabilitySample) (source, terrain string, reason ReasonCode) {
	for _, sample := range samples {
		if sample.SourceEpoch != "" {
			if source == "" {
				source = sample.SourceEpoch
			} else if source != sample.SourceEpoch {
				return "", "", ReasonSourceMismatch
			}
		}
		if sample.Terrain != "" {
			if terrain == "" {
				terrain = sample.Terrain
			} else if terrain != sample.Terrain {
				return "", "", ReasonTerrainMismatch
			}
		}
	}
	return source, terrain, ""
}

func latestKnownAt(samples []DurabilitySample) int64 {
	var latest int64
	for _, sample := range samples {
		if sample.KnownAt > latest {
			latest = sample.KnownAt
		}
	}
	return latest
}

func weightedTemperature(samples []DurabilitySample, start, end float64) *float64 {
	var weighted, weight float64
	for _, sample := range samples {
		if sample.Environment.TemperatureC == nil {
			continue
		}
		seconds := overlapSeconds(sample.StartSeconds, sample.EndSeconds, start, end)
		if seconds <= 0 {
			continue
		}
		weighted += *sample.Environment.TemperatureC * seconds
		weight += seconds
	}
	if weight <= 0 {
		return nil
	}
	return ptr(weighted / weight)
}

func overlapSeconds(aStart, aEnd, bStart, bEnd float64) float64 {
	start := math.Max(aStart, bStart)
	end := math.Min(aEnd, bEnd)
	if end <= start {
		return 0
	}
	return end - start
}

func weightedSpeedStats(samples []DurabilitySample, start, end float64) (mean, cv float64, ok bool) {
	var weight, weighted float64
	for _, sample := range samples {
		if sample.SpeedMPS == nil {
			continue
		}
		seconds := overlapSeconds(sample.StartSeconds, sample.EndSeconds, start, end)
		if seconds <= 0 {
			continue
		}
		weight += seconds
		weighted += *sample.SpeedMPS * seconds
	}
	if weight <= 0 {
		return 0, 0, false
	}
	mean = weighted / weight
	var varianceWeighted float64
	for _, sample := range samples {
		if sample.SpeedMPS == nil {
			continue
		}
		seconds := overlapSeconds(sample.StartSeconds, sample.EndSeconds, start, end)
		if seconds <= 0 {
			continue
		}
		delta := *sample.SpeedMPS - mean
		varianceWeighted += delta * delta * seconds
	}
	cv = math.Sqrt(varianceWeighted/weight) / mean
	return mean, cv, finite(mean) && finite(cv) && mean > 0
}

func accumulateDurabilityHalf(samples []DurabilitySample, start, end float64) durabilityAccumulator {
	var acc durabilityAccumulator
	for _, sample := range samples {
		seconds := overlapSeconds(sample.StartSeconds, sample.EndSeconds, start, end)
		if seconds <= 0 {
			continue
		}
		hasSpeed := sample.SpeedMPS != nil
		hasHR := sample.HeartRateBPM != nil
		if hasSpeed {
			acc.validSpeed += seconds
			acc.distance += *sample.SpeedMPS * seconds
		}
		if hasHR {
			acc.validHR += seconds
			acc.hrWeighted += *sample.HeartRateBPM * seconds
		}
		if hasSpeed && hasHR {
			acc.joint += seconds
		}
	}
	return acc
}

func durabilityHalf(start, end, clock float64, acc durabilityAccumulator) *DurabilityHalf {
	half := &DurabilityHalf{
		StartSeconds: start,
		EndSeconds:   end,
		ValidSeconds: acc.validSpeed,
		SpeedSupport: 0,
		HRSupport:    0,
	}
	if clock > 0 {
		half.SpeedSupport = clamp01(acc.validSpeed / clock)
		half.HRSupport = clamp01(acc.validHR / clock)
	}
	if acc.validSpeed > 0 {
		half.MeanSpeedMPS = ptr(acc.distance / acc.validSpeed)
	}
	if acc.validHR > 0 {
		half.MeanHeartRate = ptr(acc.hrWeighted / acc.validHR)
	}
	return half
}

func halfDurationDifference(a, b float64) float64 {
	largest := math.Max(a, b)
	if largest <= 0 {
		return 1
	}
	return math.Abs(a-b) / largest
}

// CalculateDurabilityTrend evaluates the latest qualified long run against
// prior qualified runs in the trailing 42 days. Fewer than three independent,
// same-context runs return StatusCollecting rather than an error.
func CalculateDurabilityTrend(history []DurabilityResult, asOf int64) (DurabilityTrendResult, error) {
	result := DurabilityTrendResult{
		Header:          newHeader(WorkoutDurabilityIdentity()),
		AsOf:            asOf,
		WindowDays:      durabilityTrendDays,
		EFTrend:         TrendUnknown,
		DecouplingTrend: TrendUnknown,
		PaceDecayTrend:  TrendUnknown,
	}
	if asOf <= 0 {
		return DurabilityTrendResult{}, ErrInvalidInput
	}

	cutoff := asOf - durabilityTrendDays*microsecondsPerDay
	candidates := make([]DurabilityResult, 0, len(history))
	for _, run := range history {
		if run.KnownAt <= 0 || run.KnownAt > asOf || run.KnownAt < cutoff {
			continue
		}
		if run.Status != StatusOK && run.Status != StatusPartial {
			continue
		}
		if run.WorkoutID == "" || run.MeanSpeedMPS == nil || !finite(run.DurationSeconds) || run.DurationSeconds <= 0 ||
			run.PaceDecay == nil {
			continue
		}
		if !allFinite(*run.MeanSpeedMPS, *run.PaceDecay) || *run.MeanSpeedMPS <= 0 {
			continue
		}
		if run.EF1 != nil && (!finite(*run.EF1) || *run.EF1 <= 0) {
			continue
		}
		candidates = append(candidates, run)
	}
	candidates = dedupeDurabilityRuns(candidates)
	if len(candidates) < 3 {
		result.Status = StatusCollecting
		result.Reasons.Add(ReasonInsufficientRuns)
		result.Support = makeSupport(SupportNone, 0, len(candidates))
		return result, nil
	}

	reference := candidates[len(candidates)-1]
	result.ReferenceWorkoutID = reference.WorkoutID
	if reference.TemperatureC == nil {
		result.Status = StatusCollecting
		result.Reasons.Add(ReasonTemperatureUnknown)
		result.Support = makeSupport(SupportNone, 0, 0)
		return result, nil
	}

	matched := make([]DurabilityResult, 0, len(candidates))
	for _, run := range candidates {
		if !durabilityComparable(reference, run) {
			continue
		}
		matched = append(matched, run)
	}
	result.ComparableRuns = len(matched)
	if len(matched) < 3 {
		result.Status = StatusCollecting
		result.Reasons.Add(ReasonInsufficientRuns)
		result.Support = makeSupport(SupportNone, 0, len(matched))
		return result, nil
	}

	efValues := make([]float64, 0, len(matched))
	decouplingValues := make([]float64, 0, len(matched))
	paceValues := make([]float64, 0, len(matched))
	efPoints := make([]trendPoint, 0, len(matched))
	decouplingPoints := make([]trendPoint, 0, len(matched))
	pacePoints := make([]trendPoint, 0, len(matched))
	earliest := matched[0].KnownAt
	for _, run := range matched {
		days := float64(run.KnownAt-earliest) / float64(microsecondsPerDay)
		if run.EF1 != nil {
			efValues = append(efValues, *run.EF1)
			efPoints = append(efPoints, trendPoint{x: days, y: *run.EF1})
		} else {
			result.Reasons.Add(ReasonDecouplingPartial)
		}
		paceValues = append(paceValues, *run.PaceDecay)
		pacePoints = append(pacePoints, trendPoint{x: days, y: *run.PaceDecay})
		if run.Decoupling != nil && finite(*run.Decoupling) {
			decouplingValues = append(decouplingValues, *run.Decoupling)
			decouplingPoints = append(decouplingPoints, trendPoint{x: days, y: *run.Decoupling})
		} else {
			result.Reasons.Add(ReasonDecouplingPartial)
		}
	}
	result.MedianEF = medianFloat64(efValues)
	result.MedianDecoupling = medianFloat64(decouplingValues)
	result.MedianPaceDecay = medianFloat64(paceValues)
	result.EFTrend = linearTrend(efPoints)
	result.DecouplingTrend = linearTrend(decouplingPoints)
	result.PaceDecayTrend = linearTrend(pacePoints)
	result.Status = StatusOK
	if len(efValues) < len(matched) || len(decouplingValues) < len(matched) {
		result.Status = StatusPartial
	}
	result.Support = makeSupport(SupportHigh, 1, len(matched))
	return result, nil
}

func dedupeDurabilityRuns(runs []DurabilityResult) []DurabilityResult {
	sort.SliceStable(runs, func(i, j int) bool {
		if runs[i].KnownAt == runs[j].KnownAt {
			return runs[i].WorkoutID < runs[j].WorkoutID
		}
		return runs[i].KnownAt < runs[j].KnownAt
	})
	out := make([]DurabilityResult, 0, len(runs))
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

func durabilityComparable(reference, candidate DurabilityResult) bool {
	if candidate.SourceEpoch == "" || candidate.Terrain == "" {
		return false
	}
	if candidate.SourceEpoch != reference.SourceEpoch || candidate.Terrain != reference.Terrain {
		return false
	}
	if candidate.MeanSpeedMPS == nil || reference.MeanSpeedMPS == nil {
		return false
	}
	if !withinRelative(*candidate.MeanSpeedMPS, *reference.MeanSpeedMPS, 0.10) {
		return false
	}
	if !withinRelative(candidate.DurationSeconds, reference.DurationSeconds, 0.25) {
		return false
	}
	if candidate.TemperatureC == nil || reference.TemperatureC == nil {
		return false
	}
	return math.Abs(*candidate.TemperatureC-*reference.TemperatureC) <= 5
}

func withinRelative(value, reference, tolerance float64) bool {
	if !allFinite(value, reference, tolerance) || reference <= 0 || tolerance < 0 {
		return false
	}
	return math.Abs(value-reference)/reference <= tolerance
}
