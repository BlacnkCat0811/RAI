package workoutdetail

import (
	"fmt"
	"math"
	"rai/server/internal/workoutdomain"
	"sort"
	"strings"
)

const (
	GradePolicyVersion       = "grade_adjustment.minetti/v1"
	EnvironmentPolicyVersion = "environment.correction/v1"
	DynamicsPolicyVersion    = "running_dynamics/v1"
	DisplaySampleLimit       = 600

	HeartRate           = "HKQuantityTypeIdentifierHeartRate"
	RunningSpeed        = "HKQuantityTypeIdentifierRunningSpeed"
	RunningCadence      = "HKQuantityTypeIdentifierRunningCadence"
	StepCount           = "HKQuantityTypeIdentifierStepCount"
	RunningPower        = "HKQuantityTypeIdentifierRunningPower"
	GroundContactTime   = "HKQuantityTypeIdentifierRunningGroundContactTime"
	StrideLength        = "HKQuantityTypeIdentifierRunningStrideLength"
	VerticalOscillation = "HKQuantityTypeIdentifierRunningVerticalOscillation"
	ActiveEnergyBurned  = "HKQuantityTypeIdentifierActiveEnergyBurned"
)

// Observation is one already-validated quantity interval, clipped by storage to
// the Workout window. End may equal Start for an instantaneous sample.
type Observation struct {
	Type    string
	StartUs int64
	EndUs   int64
	Value   float64
	Unit    string
}

type Statistic struct {
	Type    string
	Average *float64
	Sum     *float64
	Maximum *float64
}

type RouteSample struct {
	AtUs              int64
	Latitude          float64
	Longitude         float64
	AltitudeM         *float64
	VerticalAccuracyM *float64
	SpeedMPS          *float64
}

func Ptr(v float64) *float64 { return &v }

func finite(v float64) bool { return workoutdomain.Finite(v) }

func clamp(v, lo, hi float64) float64 { return math.Min(hi, math.Max(lo, v)) }

func round(v float64, digits int) float64 {
	f := math.Pow10(digits)
	return math.Round(v*f) / f
}

func valid(value float64) bool { return finite(value) }

// ---------------------------------------------------------------- statistics

func StatisticAverage(stats []Statistic, kind string) *float64 {
	for _, s := range stats {
		if s.Type == kind && s.Average != nil && valid(*s.Average) {
			return Ptr(*s.Average)
		}
	}
	return nil
}

func StatisticSum(stats []Statistic, kind string) *float64 {
	for _, s := range stats {
		if s.Type == kind && s.Sum != nil && valid(*s.Sum) {
			return Ptr(*s.Sum)
		}
	}
	return nil
}

// WeightedAverage computes the time-weighted mean of the observations that
// overlap [start,end]. Instantaneous observations get a one-second weight joined
// to the next observation, capped at 30 seconds, matching the bounded read used
// by the legacy product. Overlapping samples are not merged: the caller has
// already replaced a parent summary with its sealed series when one exists.
func WeightedAverage(obs []Observation, kind string, start, end int64) *float64 {
	xs := make([]Observation, 0, len(obs))
	for _, o := range obs {
		if o.Type == kind && valid(o.Value) && o.EndUs >= o.StartUs {
			xs = append(xs, o)
		}
	}
	sort.Slice(xs, func(i, j int) bool {
		if xs[i].StartUs == xs[j].StartUs {
			return xs[i].EndUs < xs[j].EndUs
		}
		return xs[i].StartUs < xs[j].StartUs
	})
	var sum, weight float64
	for i, o := range xs {
		a, b := max64(o.StartUs, start), min64(o.EndUs, end)
		if o.EndUs == o.StartUs {
			next := end
			if i+1 < len(xs) {
				next = xs[i+1].StartUs
			}
			a, b = max64(o.StartUs, start), min64(o.StartUs+1_000_000, min64(next, end))
		}
		if b <= a {
			continue
		}
		d := float64(b-a) / 1e6
		sum += o.Value * d
		weight += d
	}
	if weight <= 0 {
		return nil
	}
	return Ptr(sum / weight)
}

func CadenceFromSteps(steps []Observation, durationS float64) *float64 {
	if durationS <= 0 {
		return nil
	}
	var total float64
	for _, o := range steps {
		if o.Type != StepCount || !valid(o.Value) || o.Value < 0 {
			continue
		}
		if o.EndUs > o.StartUs {
			total += o.Value
		}
	}
	if total <= 0 {
		return nil
	}
	return Ptr(workoutdomain.CadenceProxy(total, durationS))
}

// ---------------------------------------------------------------- series

func buildSeries(startUs, endUs int64, obs []Observation, kind, unit string, transform func(Observation) (float64, bool)) MetricSeries {
	out := MetricSeries{Unit: unit, Points: []MetricPoint{}}
	points := make([]MetricPoint, 0, len(obs))
	for _, o := range obs {
		if o.Type != kind || !valid(o.Value) || o.EndUs < o.StartUs {
			continue
		}
		v, ok := transform(o)
		if !ok || !valid(v) {
			continue
		}
		offset := float64(max64(o.StartUs, startUs)-startUs) / 1e6
		if offset < 0 || offset > float64(endUs-startUs)/1e6+1 {
			continue
		}
		points = append(points, MetricPoint{OffsetS: round(offset, 2), Value: v})
	}
	sort.SliceStable(points, func(i, j int) bool { return points[i].OffsetS < points[j].OffsetS })
	out.TotalSamples = len(points)
	out.Points = sampleMetricPoints(points, DisplaySampleLimit)
	return out
}

func BuildSeries(startUs, endUs int64, obs []Observation, route []RouteSample) SeriesBundle {
	bundle := SeriesBundle{
		Pace: buildSeries(startUs, endUs, obs, RunningSpeed, "s/km", func(o Observation) (float64, bool) {
			v, ok := speedToMetersPerSecond(o.Value, o.Unit)
			if !ok || v <= 0 {
				return 0, false
			}
			pace := 1000 / v
			return pace, pace >= 120 && pace <= 1800
		}),
		HeartRate: buildSeries(startUs, endUs, obs, HeartRate, "count/min", func(o Observation) (float64, bool) {
			if o.Unit != "count/min" || o.Value <= 0 {
				return 0, false
			}
			return o.Value, true
		}),
		Cadence: buildSeries(startUs, endUs, obs, RunningCadence, "spm", func(o Observation) (float64, bool) {
			return o.Value, o.Unit == "count/min" && o.Value >= 60 && o.Value <= 300
		}),
		StrideLength: buildSeries(startUs, endUs, obs, StrideLength, "m", func(o Observation) (float64, bool) {
			switch o.Unit {
			case "m":
				return o.Value, o.Value > 0 && o.Value < 5
			case "cm":
				return o.Value / 100, o.Value > 0 && o.Value < 500
			}
			return 0, false
		}),
		Power: buildSeries(startUs, endUs, obs, RunningPower, "W", func(o Observation) (float64, bool) {
			return o.Value, o.Unit == "W" && o.Value >= 0 && o.Value < 3000
		}),
		GroundContactTime: buildSeries(startUs, endUs, obs, GroundContactTime, "ms", func(o Observation) (float64, bool) {
			switch o.Unit {
			case "ms":
				return o.Value, o.Value > 0 && o.Value < 2000
			case "s":
				return o.Value * 1000, o.Value > 0 && o.Value < 2
			}
			return 0, false
		}),
		VerticalOscillation: buildSeries(startUs, endUs, obs, VerticalOscillation, "cm", func(o Observation) (float64, bool) {
			switch o.Unit {
			case "m":
				return o.Value * 100, o.Value > 0 && o.Value < 1
			case "cm":
				return o.Value, o.Value > 0 && o.Value < 100
			}
			return 0, false
		}),
		Elevation: buildElevationSeries(startUs, endUs, route),
	}
	if bundle.Cadence.TotalSamples == 0 {
		bundle.Cadence = buildSeries(startUs, endUs, obs, StepCount, "spm", func(o Observation) (float64, bool) {
			if o.Unit != "count" || o.Value < 0 || o.EndUs <= o.StartUs {
				return 0, false
			}
			minutes := float64(o.EndUs-o.StartUs) / 1e6 / 60
			if minutes <= 0 {
				return 0, false
			}
			spm := o.Value / minutes
			return spm, spm >= 60 && spm <= 300
		})
	}
	return bundle
}

func buildElevationSeries(startUs, endUs int64, route []RouteSample) MetricSeries {
	points := make([]MetricPoint, 0, len(route))
	for _, p := range route {
		if p.AltitudeM == nil || !valid(*p.AltitudeM) || p.AtUs < startUs || p.AtUs > endUs {
			continue
		}
		points = append(points, MetricPoint{OffsetS: round(float64(p.AtUs-startUs)/1e6, 2), Value: *p.AltitudeM})
	}
	sort.SliceStable(points, func(i, j int) bool { return points[i].OffsetS < points[j].OffsetS })
	return MetricSeries{Unit: "m", TotalSamples: len(points), Points: sampleMetricPoints(points, DisplaySampleLimit)}
}

// sampleMetricPoints keeps the first, last, both extrema and an even stride so
// the chart can never hide a spike. The returned order is source order.
func sampleMetricPoints(points []MetricPoint, limit int) []MetricPoint {
	if len(points) == 0 {
		return []MetricPoint{}
	}
	if len(points) <= limit || limit < 4 {
		return append([]MetricPoint{}, points...)
	}
	keep := make(map[int]bool)
	keep[0], keep[len(points)-1] = true, true
	minI, maxI := 0, 0
	for i := 1; i < len(points); i++ {
		if points[i].Value < points[minI].Value {
			minI = i
		}
		if points[i].Value > points[maxI].Value {
			maxI = i
		}
	}
	keep[minI], keep[maxI] = true, true
	step := int(math.Ceil(float64(len(points)-1) / float64(limit-3)))
	for i := 0; i < len(points); i += step {
		keep[i] = true
	}
	indices := make([]int, 0, len(keep))
	for i := range keep {
		indices = append(indices, i)
	}
	sort.Ints(indices)
	out := make([]MetricPoint, 0, len(indices))
	for _, i := range indices {
		out = append(out, points[i])
	}
	return out
}

func speedToMetersPerSecond(v float64, unit string) (float64, bool) {
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case "m/s":
		return v, true
	case "km/hr", "km/h":
		return v / 3.6, true
	case "mi/hr", "mph":
		return v * 0.44704, true
	}
	return 0, false
}

// ---------------------------------------------------------------- route / grade

// ElevationGain sums positive altitude changes that survive the spike filter:
// a single edge may not climb more than 30 m, and both endpoints need a usable
// vertical accuracy when the source supplied one. This is a display metric, not
// a survey.
func ElevationGain(route []RouteSample) *float64 {
	var gain float64
	edges := 0
	for i := 1; i < len(route); i++ {
		a, b := route[i-1], route[i]
		if a.AltitudeM == nil || b.AltitudeM == nil || b.AtUs <= a.AtUs {
			continue
		}
		if !usableAltitudeAccuracy(a.VerticalAccuracyM) || !usableAltitudeAccuracy(b.VerticalAccuracyM) {
			continue
		}
		diff := *b.AltitudeM - *a.AltitudeM
		if math.Abs(diff) > 30 {
			continue
		}
		if diff > 0 {
			gain += diff
		}
		edges++
	}
	if edges == 0 {
		return nil
	}
	return Ptr(round(gain, 1))
}

func usableAltitudeAccuracy(v *float64) bool { return v == nil || (*v >= 0 && *v <= 10) }

func RouteQuality(route []RouteSample, startUs, endUs int64) *float64 {
	if len(route) < 2 || endUs <= startUs {
		return nil
	}
	good := 0
	var covered int64
	for i := 1; i < len(route); i++ {
		a, b := route[i-1], route[i]
		if b.AtUs <= a.AtUs {
			continue
		}
		good++
		covered += min64(b.AtUs, endUs) - max64(a.AtUs, startUs)
	}
	if good == 0 {
		return nil
	}
	q := float64(covered) / float64(endUs-startUs)
	return Ptr(clamp(q, 0, 1))
}

// SampleRoute returns at most limit display points while retaining both ends.
func SampleRoute(points []RoutePoint, limit int) []RoutePoint {
	if len(points) == 0 {
		return []RoutePoint{}
	}
	if len(points) <= limit || limit < 2 {
		return append([]RoutePoint{}, points...)
	}
	out := make([]RoutePoint, 0, limit)
	step := float64(len(points)-1) / float64(limit-1)
	for i := 0; i < limit; i++ {
		out = append(out, points[int(math.Round(float64(i)*step))])
	}
	return out
}

func BuildSplits(startUs, endUs int64, distanceM float64, speed []Observation, heart []Observation, steps []Observation, route []RouteSample) ([]Split, *GradeSummary, []KilometerMarker) {
	gradeSummary := &GradeSummary{PolicyVersion: GradePolicyVersion, ReasonCodes: []string{}}
	if distanceM <= 0 || endUs <= startUs {
		gradeSummary.ReasonCodes = append(gradeSummary.ReasonCodes, "no_official_distance")
		return nil, gradeSummary, nil
	}
	track := buildSpeedTrack(startUs, endUs, distanceM, speed, route)
	if track == nil {
		gradeSummary.ReasonCodes = append(gradeSummary.ReasonCodes, "speed_track_unusable")
		return nil, gradeSummary, nil
	}
	boundaries := []int64{startUs}
	var accumulated float64
	next := 1000.0
	for _, sample := range track.samples {
		for next < distanceM && accumulated+sample.meters >= next {
			fraction := (next - accumulated) / sample.meters
			boundaries = append(boundaries, sample.startUs+int64(float64(sample.endUs-sample.startUs)*fraction))
			next += 1000
		}
		accumulated += sample.meters
	}
	boundaries = append(boundaries, endUs)
	// De-duplicate boundaries that a short final sliver could collapse.
	clean := boundaries[:1]
	for _, b := range boundaries[1:] {
		if b-clean[len(clean)-1] >= 1_000_000 {
			clean = append(clean, b)
		}
	}
	boundaries = clean
	if len(boundaries) < 2 {
		gradeSummary.ReasonCodes = append(gradeSummary.ReasonCodes, "no_split_boundaries")
		return nil, gradeSummary, nil
	}
	splits := make([]Split, 0, len(boundaries)-1)
	markers := make([]KilometerMarker, 0, len(boundaries)-2)
	for i := 0; i < len(boundaries)-1; i++ {
		a, b := boundaries[i], boundaries[i+1]
		duration := float64(b-a) / 1e6
		dist := math.Min(1000, distanceM-float64(i)*1000)
		if dist <= 0 || duration <= 0 {
			continue
		}
		pace := duration / (dist / 1000)
		split := Split{
			Index:               i + 1,
			DistanceM:           round(dist, 1),
			DurationS:           round(duration, 1),
			AveragePaceSPerKm:   round(pace, 1),
			AverageHeartRateBpm: WeightedAverage(heart, HeartRate, a, b),
		}
		split.AverageCadenceSpm = cadenceInWindow(steps, a, b)
		split.Grade = splitGrade(route, a, b, dist)
		splits = append(splits, split)
		if i < len(boundaries)-2 {
			if p, ok := routePointAt(route, b); ok {
				markers = append(markers, KilometerMarker{Kilometer: i + 1, Latitude: p.Latitude, Longitude: p.Longitude, OffsetS: round(float64(b-startUs)/1e6, 1)})
			}
		}
	}

	gradeSummary = ApplyGradeAdjustedPace(splits, distanceM)
	return splits, gradeSummary, markers
}

func cadenceInWindow(steps []Observation, a, b int64) *float64 {
	duration := float64(b-a) / 1e6
	if duration <= 0 {
		return nil
	}
	var count float64
	for _, o := range steps {
		if o.Type != StepCount || !valid(o.Value) || o.Value < 0 || o.EndUs <= o.StartUs {
			continue
		}
		overlap := min64(b, o.EndUs) - max64(a, o.StartUs)
		if overlap > 0 {
			count += o.Value * float64(overlap) / float64(o.EndUs-o.StartUs)
		}
	}
	if count <= 0 {
		return nil
	}
	return Ptr(workoutdomain.CadenceProxy(count, duration))
}

type speedSample struct {
	startUs, endUs int64
	mps            float64
	meters         float64
}

type speedTrack struct {
	samples        []speedSample
	officialMeters float64
}

func buildSpeedTrack(startUs, endUs int64, distanceM float64, speed []Observation, route []RouteSample) *speedTrack {
	intervals := make([]speedSample, 0, len(speed))
	for _, o := range speed {
		if o.Type != RunningSpeed || !valid(o.Value) || o.Value <= 0 {
			continue
		}
		mps, ok := speedToMetersPerSecond(o.Value, o.Unit)
		if !ok || mps <= 0 {
			continue
		}
		pace := 1000 / mps
		if pace < 150 || pace > 900 {
			continue
		}
		a, b := max64(o.StartUs, startUs), min64(o.EndUs, endUs)
		if b > a {
			intervals = append(intervals, speedSample{startUs: a, endUs: b, mps: mps})
		}
	}
	if len(intervals) == 0 {
		for i, p := range route {
			if p.SpeedMPS == nil || *p.SpeedMPS <= 0 {
				continue
			}
			end := endUs
			if i+1 < len(route) {
				end = route[i+1].AtUs
			}
			a, b := max64(p.AtUs, startUs), min64(end, endUs)
			if b > a {
				intervals = append(intervals, speedSample{startUs: a, endUs: b, mps: *p.SpeedMPS})
			}
		}
	}
	if len(intervals) == 0 {
		return nil
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i].startUs < intervals[j].startUs })
	trimmed := intervals[:0]
	coveredUntil := startUs
	for _, s := range intervals {
		if s.startUs < coveredUntil {
			s.startUs = coveredUntil
		}
		if s.endUs <= s.startUs {
			continue
		}
		trimmed = append(trimmed, s)
		coveredUntil = s.endUs
	}
	if len(trimmed) == 0 {
		return nil
	}
	var raw, covered int64
	for _, s := range trimmed {
		raw += int64(s.mps * float64(s.endUs-s.startUs) / 1e6 * 1000)
		covered += s.endUs - s.startUs
	}
	if raw <= 0 {
		return nil
	}
	coverage := float64(covered) / float64(endUs-startUs)
	scale := distanceM * 1000 / float64(raw)
	if coverage < 0.8 || scale < 0.75 || scale > 1.25 {
		return nil
	}
	for i := range trimmed {
		trimmed[i].meters = trimmed[i].mps * float64(trimmed[i].endUs-trimmed[i].startUs) / 1e6 * scale
	}
	return &speedTrack{samples: trimmed, officialMeters: distanceM}
}

func splitGrade(route []RouteSample, startUs, endUs int64, distanceM float64) *SplitGrade {
	out := &SplitGrade{PolicyVersion: GradePolicyVersion, ReasonCodes: []string{}}
	if distanceM < 50 {
		out.ReasonCodes = append(out.ReasonCodes, "split_too_short_for_grade")
		return out
	}
	var first, last *float64
	var gain, loss float64
	var previous *float64
	samples := 0
	for _, p := range route {
		if p.AtUs < startUs || p.AtUs > endUs || p.AltitudeM == nil || !valid(*p.AltitudeM) {
			continue
		}
		if !usableAltitudeAccuracy(p.VerticalAccuracyM) {
			continue
		}
		samples++
		v := *p.AltitudeM
		if first == nil {
			first = Ptr(v)
		}
		if previous != nil {
			d := v - *previous
			if d > 0 {
				gain += d
			} else {
				loss -= d
			}
		}
		previous = Ptr(v)
		last = Ptr(v)
	}
	if samples < 2 || first == nil || last == nil {
		out.ReasonCodes = append(out.ReasonCodes, "elevation_samples_insufficient")
		return out
	}
	change := *last - *first
	out.ElevationChangeMeters = Ptr(round(change, 1))
	out.GainMeters = Ptr(round(gain, 1))
	out.LossMeters = Ptr(round(loss, 1))
	grade := change / distanceM * 100
	if !valid(grade) || math.Abs(grade) > 60 {
		out.ReasonCodes = append(out.ReasonCodes, "grade_implausible_for_split")
		return out
	}
	out.GradePercent = Ptr(round(grade, 2))
	if out.GainMeters != nil || out.LossMeters != nil {
		// Split duration is not stored here; pace is supplied by the caller's split.
	}
	return out
}

// ApplyGradeAdjustedPace fills each split's Minetti equivalent pace and the
// summary in one place. Split grade context and measured pace stay what they
// were; this is a display-only re-expression.
func ApplyGradeAdjustedPace(splits []Split, distanceM float64) *GradeSummary {
	summary := &GradeSummary{PolicyVersion: GradePolicyVersion, TotalSplits: len(splits), ReasonCodes: []string{}}
	var weightedPace, weightedDistance, gain, loss float64
	for i := range splits {
		s := &splits[i]
		if s.Grade == nil || s.Grade.GradePercent == nil {
			continue
		}
		adjusted, reason := GradeAdjustedPace(s.AveragePaceSPerKm, *s.Grade.GradePercent)
		s.Grade.GradeAdjustedPaceSPerKm = Ptr(round(adjusted, 1))
		s.Grade.ReasonCodes = append(s.Grade.ReasonCodes, reason...)
		summary.CoveredSplits++
		weightedPace += adjusted * s.DistanceM
		weightedDistance += s.DistanceM
		if s.Grade.GainMeters != nil {
			gain += *s.Grade.GainMeters
		}
		if s.Grade.LossMeters != nil {
			loss += *s.Grade.LossMeters
		}
	}
	if summary.CoveredSplits > 0 && weightedDistance > 0 {
		summary.GradeAdjustedPaceSPerKm = Ptr(round(weightedPace/weightedDistance, 1))
		summary.GainMeters = Ptr(round(gain, 1))
		summary.LossMeters = Ptr(round(loss, 1))
	} else {
		summary.ReasonCodes = append(summary.ReasonCodes, "no_split_with_elevation")
	}
	if summary.CoveredSplits > 0 && summary.CoveredSplits < len(splits) {
		summary.ReasonCodes = append(summary.ReasonCodes, "partial_elevation_coverage")
	}
	return summary
}

func GradeAdjustedPace(paceSPerKm, gradePercent float64) (float64, []string) {
	if !valid(paceSPerKm) || paceSPerKm <= 0 || !valid(gradePercent) {
		return 0, []string{"invalid_grade_or_pace"}
	}
	grade := gradePercent / 100
	clamped := math.Abs(grade) > 0.45
	grade = clamp(grade, -0.45, 0.45)
	cost := gradeCost(grade)
	if !valid(cost) || cost <= 0 {
		return 0, []string{"grade_cost_unavailable"}
	}
	reason := []string{}
	if clamped {
		reason = append(reason, "grade_clamped_to_cost_model_domain")
	}
	return paceSPerKm * (gradeCost(0) / cost), reason
}

func gradeCost(grade float64) float64 { return workoutdomain.GradeCost(grade) }

func routePointAt(route []RouteSample, at int64) (RouteSample, bool) {
	if len(route) == 0 {
		return RouteSample{}, false
	}
	i := sort.Search(len(route), func(i int) bool { return route[i].AtUs >= at })
	if i == len(route) {
		i = len(route) - 1
	}
	return route[i], true
}

// ---------------------------------------------------------------- environment

func BuildEnvironment(metadata map[string]any, distanceM, durationS, elevationGainM *float64) *Environment {
	temp, hasTemp := metadataQuantity(metadata, "HKWeatherTemperature", "degC")
	humidity, hasHumidity := metadataQuantity(metadata, "HKWeatherHumidity", "1")
	if hasHumidity && humidity <= 1 {
		humidity *= 100
	}
	condition := metadataWeatherCondition(metadata["HKWeatherCondition"])
	out := &Environment{WeatherCondition: condition, Status: "unavailable", PolicyVersion: EnvironmentPolicyVersion, ReasonCodes: []string{}}
	if hasTemp {
		out.TemperatureC = Ptr(round(temp, 1))
	}
	if hasHumidity {
		out.HumidityPercent = Ptr(round(clamp(humidity, 0, 100), 0))
	}
	if !hasTemp {
		out.ReasonCodes = append(out.ReasonCodes, "temperature_missing")
	}
	if !hasHumidity {
		out.ReasonCodes = append(out.ReasonCodes, "humidity_missing")
	}
	if elevationGainM == nil {
		out.ReasonCodes = append(out.ReasonCodes, "elevation_missing")
	}
	known := hasTemp && hasHumidity && elevationGainM != nil && distanceM != nil && durationS != nil && *distanceM > 0 && *durationS > 0
	if !known {
		return out
	}
	envPenalty := heatHumidityPenalty(temp, humidity)
	elevPenalty := elevationPenalty(*distanceM, *elevationGainM)
	total := clamp(envPenalty+elevPenalty, 0, 0.2)
	out.Correction = &Correction{Environment: Ptr(round(envPenalty, 4)), Elevation: Ptr(round(elevPenalty, 4)), Total: Ptr(round(total, 4))}
	adjusted := *durationS / (1 + total)
	out.Adjusted = &Adjusted{DurationS: Ptr(round(adjusted, 1)), PaceSPerKm: Ptr(round(adjusted/(*distanceM/1000), 1))}
	if total == 0 {
		out.Status = "not_needed"
		out.ReasonCodes = append(out.ReasonCodes, "reference_conditions")
	} else {
		out.Status = "applied"
		if envPenalty > 0 {
			out.ReasonCodes = append(out.ReasonCodes, "heat_humidity_adjustment")
		}
		if elevPenalty > 0 {
			out.ReasonCodes = append(out.ReasonCodes, "elevation_adjustment")
		}
	}
	return out
}

func heatHumidityPenalty(temperatureC, humidityPercent float64) float64 {
	humidity := clamp(humidityPercent, 0, 100)
	heat := math.Max(0, temperatureC-15) * 0.0025 * (1 + math.Max(0, humidity-50)/100)
	cold := math.Max(0, 5-temperatureC) * 0.001
	return clamp(heat+cold, 0, 0.15)
}

func elevationPenalty(distanceM, gainM float64) float64 {
	if distanceM <= 0 || gainM <= 0 {
		return 0
	}
	return clamp(gainM/(distanceM/1000)*0.00035, 0, 0.1)
}

func metadataQuantity(metadata map[string]any, key, defaultUnit string) (float64, bool) {
	raw, ok := metadata[key]
	if !ok {
		return 0, false
	}
	switch v := raw.(type) {
	case float64:
		return v, valid(v)
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case map[string]any:
		value, ok := numeric(v["value"])
		if !ok {
			return 0, false
		}
		unit, _ := v["unit"].(string)
		switch {
		case strings.Contains(strings.ToLower(unit), "f"):
			return (value - 32) * 5 / 9, true
		case strings.Contains(strings.ToLower(unit), "percent"), unit == "%":
			return value * 100, true
		case unit == defaultUnit || strings.Contains(strings.ToLower(unit), "degc") || strings.Contains(unit, "°c"):
			return value, true
		default:
			return value, true
		}
	}
	return 0, false
}

func metadataWeatherCondition(raw any) *int {
	if raw == nil {
		return nil
	}
	if xs, ok := raw.([]any); ok && len(xs) > 0 {
		raw = xs[len(xs)-1]
	}
	switch v := raw.(type) {
	case float64:
		n := int(v)
		if n >= 1 && n <= 27 {
			return &n
		}
	case string:
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &n); err == nil && n >= 1 && n <= 27 {
			return &n
		}
	}
	return nil
}

func numeric(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, valid(n)
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

// ---------------------------------------------------------------- dynamics

func BuildDynamics(durationS float64, bundle SeriesBundle) *Dynamics {
	out := &Dynamics{PolicyVersion: DynamicsPolicyVersion, Metrics: []DynamicsMetric{}, Flags: []DynamicsFlag{}}
	if durationS <= 0 {
		out.Note = "运动时长不足，无法比较前后半程"
		return out
	}
	half := durationS / 2
	pacePoints := bundle.Pace.Points
	medianPace := medianValues(pacePoints)
	for _, spec := range []struct {
		key, label, unit string
		series           MetricSeries
	}{
		{"cadence", "步频", "spm", bundle.Cadence},
		{"stride_length", "步幅", "m", bundle.StrideLength},
		{"ground_contact_time", "触地时间", "ms", bundle.GroundContactTime},
		{"vertical_oscillation", "垂直振幅", "cm", bundle.VerticalOscillation},
	} {
		m := DynamicsMetric{Key: spec.key, Label: spec.label, Unit: spec.unit, ReasonCodes: []string{}}
		var first, second []float64
		for _, p := range spec.series.Points {
			pace, ok := paceAt(pacePoints, p.OffsetS)
			if !ok || medianPace == nil || math.Abs(pace-*medianPace) > *medianPace*0.05 {
				continue
			}
			if p.OffsetS <= half {
				first = append(first, p.Value)
			} else {
				second = append(second, p.Value)
			}
		}
		if len(first)+len(second) > 0 {
			mean := meanValues(append(append([]float64{}, first...), second...))
			m.Average = Ptr(round(mean, 1))
			m.Samples = len(first) + len(second)
			span := 0.0
			if len(spec.series.Points) > 1 {
				span = spec.series.Points[len(spec.series.Points)-1].OffsetS - spec.series.Points[0].OffsetS
			}
			m.Coverage = clamp(span/durationS, 0, 1)
		}
		if len(first) >= 2 && len(second) >= 2 {
			a, b := meanValues(first), meanValues(second)
			if a != 0 {
				m.DriftFraction = Ptr(round((b-a)/a, 4))
			}
		} else {
			m.ReasonCodes = append(m.ReasonCodes, "no_comparable_pace_samples")
		}
		if m.Average != nil || m.DriftFraction != nil {
			out.Metrics = append(out.Metrics, m)
		}
	}
	for _, m := range out.Metrics {
		if m.DriftFraction == nil {
			continue
		}
		d := *m.DriftFraction
		switch m.Key {
		case "cadence":
			if d < -0.03 {
				out.Flags = append(out.Flags, DynamicsFlag{Code: "cadence_drop", Detail: "后半程步频下降超过 3%", ComparableSamples: m.Samples})
			}
		case "stride_length":
			if d < -0.03 {
				out.Flags = append(out.Flags, DynamicsFlag{Code: "stride_shortening", Detail: "后半程步幅缩短超过 3%", ComparableSamples: m.Samples})
			}
		case "ground_contact_time":
			if d > 0.03 {
				out.Flags = append(out.Flags, DynamicsFlag{Code: "ground_contact_increase", Detail: "后半程触地时间增加超过 3%", ComparableSamples: m.Samples})
			}
		case "vertical_oscillation":
			if d > 0.03 {
				out.Flags = append(out.Flags, DynamicsFlag{Code: "vertical_oscillation_increase", Detail: "后半程垂直振幅增加超过 3%", ComparableSamples: m.Samples})
			}
		}
	}
	if len(out.Metrics) == 0 {
		out.Note = "这次训练没有可用的跑姿动态样本"
	} else {
		out.Analysable = true
	}
	return out
}

func paceAt(points []MetricPoint, offset float64) (float64, bool) {
	if len(points) == 0 {
		return 0, false
	}
	i := sort.Search(len(points), func(i int) bool { return points[i].OffsetS >= offset })
	if i == len(points) {
		i = len(points) - 1
	}
	best := points[i]
	if i > 0 && math.Abs(points[i-1].OffsetS-offset) < math.Abs(best.OffsetS-offset) {
		best = points[i-1]
	}
	if math.Abs(best.OffsetS-offset) > 30 {
		return 0, false
	}
	return best.Value, true
}

func medianValues(points []MetricPoint) *float64 {
	if len(points) == 0 {
		return nil
	}
	xs := make([]float64, 0, len(points))
	for _, p := range points {
		if p.Value > 0 {
			xs = append(xs, p.Value)
		}
	}
	if len(xs) == 0 {
		return nil
	}
	sort.Float64s(xs)
	return Ptr(xs[len(xs)/2])
}

func meanValues(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, v := range xs {
		sum += v
	}
	return sum / float64(len(xs))
}

// HeartRateSupport converts instant samples into bounded display support. It
// never extrapolates first/last points or bridges gaps over the feature policy's
// 15-second limit. This fallback has no source/pause context; published feature
// coverage is preferred by storage. Model eligibility uses the feature pipeline.
func HeartRateSupport(obs []Observation) []Observation {
	xs := append([]Observation(nil), obs...)
	sort.Slice(xs, func(i, j int) bool { return xs[i].StartUs < xs[j].StartUs })
	unique := xs[:0]
	for _, x := range xs {
		if len(unique) == 0 || unique[len(unique)-1] != x {
			unique = append(unique, x)
		}
	}
	xs = unique
	out := []Observation{}
	for i, x := range xs {
		if x.Type != HeartRate || !valid(x.Value) || x.Value <= 0 || x.Value >= 220 {
			continue
		}
		if x.EndUs > x.StartUs {
			out = append(out, x)
			continue
		}
		if x.EndUs != x.StartUs || i+1 == len(xs) {
			continue
		}
		next := xs[i+1]
		// Duplicate/conflicting timestamps and invalid points are barriers.
		if i > 0 && xs[i-1].StartUs == x.StartUs {
			continue
		}
		if next.Type != HeartRate || next.StartUs <= x.StartUs || next.EndUs != next.StartUs ||
			next.StartUs-x.StartUs > 15_000_000 || !valid(next.Value) || next.Value <= 0 || next.Value >= 220 {
			continue
		}
		if i+2 < len(xs) && xs[i+2].StartUs == next.StartUs {
			continue
		}
		if next.StartUs-x.StartUs <= 5_000_000 && math.Abs(next.Value-x.Value) > 30 {
			continue
		}
		x.EndUs = next.StartUs
		out = append(out, x)
	}
	return out
}

// ---------------------------------------------------------------- zones / quality

// Split interpolated instant support at HRR boundaries so classification uses
// the same linear signal assumption as features, without counting points as seconds.
func heartZoneSupport(obs []Observation, restHR, maxHR float64) []Observation {
	points := map[int64]Observation{}
	intervals := map[Observation]bool{}
	for _, o := range obs {
		if o.EndUs > o.StartUs {
			intervals[o] = true
		}
		if o.StartUs == o.EndUs {
			points[o.StartUs] = o
		}
	}
	out := []Observation{}
	for _, o := range HeartRateSupport(obs) {
		first, instant := points[o.StartUs]
		last, ok := points[o.EndUs]
		if intervals[o] || !instant || !ok || first.Value == last.Value {
			out = append(out, o)
			continue
		}
		cuts := []float64{0, 1}
		for _, fraction := range []float64{.6, .7, .8, .9} {
			hr := restHR + fraction*(maxHR-restHR)
			f := (hr - first.Value) / (last.Value - first.Value)
			if f > 0 && f < 1 {
				cuts = append(cuts, f)
			}
		}
		sort.Float64s(cuts)
		for i := 1; i < len(cuts); i++ {
			piece := o
			piece.StartUs = o.StartUs + int64(math.Round(cuts[i-1]*float64(o.EndUs-o.StartUs)))
			piece.EndUs = o.StartUs + int64(math.Round(cuts[i]*float64(o.EndUs-o.StartUs)))
			piece.Value = first.Value + (last.Value-first.Value)*(cuts[i-1]+cuts[i])/2
			out = append(out, piece)
		}
	}
	return out
}

func BuildHRZones(startUs, endUs int64, elapsedS float64, heart []Observation, restHR, maxHR float64) *ZoneSummary {
	out := &ZoneSummary{
		StimulusType:           "unclassified",
		SecondaryStimulusTypes: []string{},
		HeartRateExposure:      []ZoneExposure{},
		PaceExposure:           []ZoneExposure{},
		StimulusScores:         []StimulusScore{},
		ExplanationCodes:       []string{"hr_zones_from_parameter_set", "stimulus_requires_matched_plan", "pace_zone_threshold_unavailable"},
		PaceZoneStatus:         "threshold_pace_unavailable",
	}
	if !valid(restHR) || !valid(maxHR) || maxHR <= restHR || elapsedS <= 0 {
		return nil
	}
	out.ZoneProfileID = fmt.Sprintf("hrr:%.0f:%.0f", restHR, maxHR)
	type band struct {
		zone         string
		lower, upper float64
	}
	bands := []band{
		{"recovery", 0, 0.6},
		{"aerobic_base", 0.6, 0.7},
		{"aerobic_power", 0.7, 0.8},
		{"lactate_threshold", 0.8, 0.9},
		{"anaerobic", 0.9, math.Inf(1)},
	}
	// Count each instant once. Identical overlapping readings are duplicates;
	// conflicting readings leave an ambiguous interval with no exposure/score.
	type edge struct {
		at    int64
		value float64
		delta int
	}
	edges := []edge{}
	for _, o := range heartZoneSupport(heart, restHR, maxHR) {
		if o.Type != HeartRate || !valid(o.Value) || o.Value <= 0 || o.EndUs <= o.StartUs {
			continue
		}
		a, b := max64(o.StartUs, startUs), min64(o.EndUs, endUs)
		if b > a {
			edges = append(edges, edge{a, o.Value, 1}, edge{b, o.Value, -1})
		}
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].at < edges[j].at })
	duration := map[string]float64{}
	active := map[float64]int{}
	var covered float64
	var previous int64
	var duplicate, ambiguous bool
	for i := 0; i < len(edges); {
		at := edges[i].at
		if at > previous && len(active) == 1 {
			for value, count := range active {
				duplicate = duplicate || count > 1
				fraction := clamp((value-restHR)/(maxHR-restHR), 0, 1)
				for _, band := range bands {
					if fraction >= band.lower && fraction < band.upper {
						d := float64(at-previous) / 1e6
						duration[band.zone] += d
						covered += d
						break
					}
				}
			}
		} else if at > previous && len(active) > 1 {
			ambiguous = true
		}
		for i < len(edges) && edges[i].at == at {
			e := edges[i]
			active[e.value] += e.delta
			if active[e.value] == 0 {
				delete(active, e.value)
			}
			i++
		}
		previous = at
	}
	if duplicate {
		out.ExplanationCodes = append(out.ExplanationCodes, "identical_heart_rate_intervals_deduplicated")
	}
	if ambiguous {
		out.ExplanationCodes = append(out.ExplanationCodes, "ambiguous_heart_rate_intervals_excluded")
	}
	if covered <= 0 {
		return nil
	}
	out.HeartRateCoverage = clamp(covered/elapsedS, 0, 1)
	for _, band := range bands {
		out.HeartRateExposure = append(out.HeartRateExposure, ZoneExposure{Zone: band.zone, DurationSeconds: round(duration[band.zone], 1), Fraction: round(duration[band.zone]/covered, 4)})
	}
	return out
}

func QualityScore(coverages []*float64, sourceConfidence *float64) *float64 {
	if sourceConfidence == nil || !valid(*sourceConfidence) {
		return nil
	}
	var sum float64
	var n int
	for _, c := range coverages {
		if c != nil && valid(*c) {
			sum += clamp(*c, 0, 1)
			n++
		}
	}
	if n == 0 {
		return nil
	}
	return Ptr(round(sum/float64(n)*clamp(*sourceConfidence, 0, 1), 3))
}

func SourceConfidence(bundleID string) float64 {
	b := strings.ToLower(bundleID)
	switch {
	case strings.HasPrefix(b, "com.apple.health"), strings.HasPrefix(b, "com.apple.workout"), b == "com.apple.health":
		return 1
	case strings.Contains(b, "watch"):
		return 0.95
	case strings.Contains(b, "strava"), strings.Contains(b, "garmin"), strings.Contains(b, "nike"):
		return 0.7
	default:
		return 0.6
	}
}

func SourceCategory(bundleID string) string {
	b := strings.ToLower(bundleID)
	switch {
	case strings.Contains(b, "watch"):
		return "apple_watch"
	case strings.Contains(b, "iphone"):
		return "iphone"
	case b == "":
		return "unknown"
	default:
		return "third_party"
	}
}

func SplitDistanceFromSplits(splits []Split) float64 {
	var d float64
	for _, s := range splits {
		d += s.DistanceM
	}
	return d
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// ---------------------------------------------------------------- segments

type ActivityBoundary struct {
	StartUs int64
	EndUs   int64
}

type EventBoundary struct {
	Type    int64
	StartUs int64
	EndUs   int64
}

type PlannedStep struct {
	Label      string
	Purpose    string
	DurationS  float64
	Repeats    int
	RecoveryS  float64
	TargetHR   *float64
	PaceSPerKm *float64
}

type PlanContext struct {
	LocalDate    string
	Kind         string
	Revision     int64
	DistanceM    *float64
	DurationS    float64
	HasStructure bool
	Steps        []PlannedStep
}

// BuildSegments turns measured Watch activity boundaries (the preferred
// producer), lap events (second), and one unambiguous Watch segment hierarchy
// (third) into measured rows. Plans only supply labels and targets; they never
// invent boundaries. Overlapping segment hierarchies are rejected instead of
// being unioned into fabricated splits.
func BuildSegments(startUs, endUs int64, distanceM float64, speed, heart, steps []Observation, route []RouteSample, activities []ActivityBoundary, events []EventBoundary, plan *PlanContext) ([]Segment, *SegmentPlanMatch, string, bool) {
	measured := normalizeActivities(startUs, endUs, activities)
	hasMeasured := len(measured) >= 3
	descriptors := []PlannedStep(nil)
	if plan != nil && plan.HasStructure {
		descriptors = flattenPlanSteps(plan.Steps)
	}
	boundaries := measured
	source := "activity"
	note := ""
	if !hasMeasured {
		boundaries, source, note = eventBoundaries(startUs, endUs, events, len(descriptors))
	}
	if len(boundaries) < 2 {
		if note == "" {
			note = "没有可用的手表分段或计圈事件"
		}
		return nil, nil, note, false
	}
	track := buildSpeedTrack(startUs, endUs, distanceM, speed, route)
	segments := make([]Segment, 0, len(boundaries)-1)
	for i := 0; i < len(boundaries)-1; i++ {
		a, b := boundaries[i], boundaries[i+1]
		duration := float64(b-a) / 1e6
		if duration <= 0 {
			continue
		}
		dist := 0.0
		if track != nil {
			dist = math.Max(0, trackMetersAt(track, b)-trackMetersAt(track, a))
		}
		index := len(segments) + 1
		seg := Segment{
			Index:               index,
			Label:               fmt.Sprintf("第 %d 段", index),
			Purpose:             "work",
			StartOffsetSeconds:  round(float64(a-startUs)/1e6, 1),
			DurationSeconds:     round(duration, 1),
			DistanceMeters:      round(dist, 1),
			AverageHeartRateBpm: WeightedAverage(heart, HeartRate, a, b),
			AverageCadenceSpm:   cadenceInWindow(steps, a, b),
		}
		if dist > 0 {
			seg.AveragePaceSPerKm = Ptr(round(duration/(dist/1000), 1))
		}
		segments = append(segments, seg)
	}
	if len(segments) == 0 {
		return nil, nil, "分段边界内没有可测量的时长", false
	}
	if track == nil {
		note = joinNote(note, "缺少可用速度轨迹，分段距离与配速不可用")
	}
	labeled := false
	if descriptors != nil && (source == "activity" || source == "segment") {
		if len(descriptors) == len(segments) {
			for i := range segments {
				applyPlanStep(&segments[i], descriptors[i])
			}
			labeled = true
		} else {
			note = joinNote(note, fmt.Sprintf("计划 %d 步，实测 %d 段，未套用计划标签", len(descriptors), len(segments)))
		}
	} else if descriptors != nil {
		note = joinNote(note, "计圈事件与课表步骤未建立唯一对应，分段只保留实测边界")
	}
	if !labeled || plan == nil {
		if descriptors == nil {
			note = joinNote(note, "未匹配到结构化课表，分段只保留实测边界")
		}
		return segments, nil, note, hasMeasured
	}
	match := &SegmentPlanMatch{
		LocalDate:         plan.LocalDate,
		Kind:              plan.Kind,
		Revision:          plan.Revision,
		MatchedBy:         "execution_snapshot",
		PlannedDistanceM:  plan.DistanceM,
		ExplainedFraction: 1,
		Reason:            segmentMatchReason(source),
	}
	return segments, match, note, hasMeasured
}

func applyPlanStep(seg *Segment, d PlannedStep) {
	seg.Label = d.PurposeLabel()
	if d.Label != "" {
		seg.Label = d.Label
	}
	if d.Purpose != "" {
		seg.Purpose = d.Purpose
	}
	if d.DurationS > 0 {
		seg.GoalType = "time"
		seg.GoalSeconds = Ptr(d.DurationS)
	}
	if d.TargetHR != nil {
		seg.AlertKind = "heart_rate"
		seg.AlertLower = Ptr(math.Max(0, *d.TargetHR-5))
		seg.AlertUpper = Ptr(*d.TargetHR + 5)
	} else if d.PaceSPerKm != nil {
		seg.AlertKind = "pace"
		seg.AlertLower = Ptr(math.Max(0, *d.PaceSPerKm*0.95))
		seg.AlertUpper = Ptr(*d.PaceSPerKm * 1.05)
	}
}

func segmentMatchReason(source string) string {
	if source == "segment" {
		return "分段边界来自手表事件分段，计划只提供标签与目标"
	}
	return "分段边界来自手表实测活动，计划只提供标签与目标"
}

func normalizeActivities(startUs, endUs int64, activities []ActivityBoundary) []int64 {
	xs := make([]ActivityBoundary, 0, len(activities))
	for _, a := range activities {
		a.StartUs, a.EndUs = max64(a.StartUs, startUs), min64(a.EndUs, endUs)
		if a.EndUs-a.StartUs >= 5_000_000 {
			xs = append(xs, a)
		}
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i].StartUs < xs[j].StartUs })
	// A single modality spanning the Workout is not a set of workout steps.
	// Require contiguous measured activities; never fill gaps with invented steps.
	if len(xs) < 2 {
		return nil
	}
	out := []int64{xs[0].StartUs}
	for _, a := range xs {
		if a.StartUs != out[len(out)-1] {
			return nil
		}
		out = append(out, a.EndUs)
	}
	return out
}

const (
	eventBoundaryToleranceUs = 1_000_000
	eventBoundaryMergeUs     = 5_000_000
	minSegmentEventUs        = 1_000_000
)

type segmentInterval struct {
	startUs, endUs int64
}

// eventBoundaries selects at most one trustworthy event hierarchy. Laps are
// explicit partition markers; segment events are periods of interest that may
// overlap across independent producers. Overlapping segment sets are only used
// when exactly one of them matches the matched plan's step count.
func eventBoundaries(startUs, endUs int64, events []EventBoundary, planSteps int) ([]int64, string, string) {
	if points := lapEventBoundaries(startUs, endUs, events); len(points) >= 2 {
		return points, "lap", "手表未提供实测训练步骤，以下为计圈事件分段"
	}
	intervals := segmentEventIntervals(startUs, endUs, events)
	if len(intervals) < 2 {
		return nil, "", ""
	}
	if intervalsOverlap(intervals) {
		if planSteps >= 2 {
			if points := alignedSegmentBoundaries(startUs, endUs, intervals, planSteps); points != nil {
				return points, "segment", "手表未提供实测训练步骤，以下为手表事件分段"
			}
		}
		return nil, "", "检测到多套重叠的手表分段事件，无法确定唯一分段"
	}
	points := intervalBoundaries(startUs, endUs, intervals)
	if len(points) < 2 {
		return nil, "", ""
	}
	return points, "segment", "手表未提供实测训练步骤，以下为手表事件分段"
}

func lapEventBoundaries(startUs, endUs int64, events []EventBoundary) []int64 {
	points := []int64{startUs}
	for _, e := range events {
		// HKWorkoutEventTypeLap=3. Pause/resume requests are not laps.
		if e.Type != 3 || e.StartUs >= endUs || e.EndUs <= startUs {
			continue
		}
		a, b := max64(e.StartUs, startUs), min64(e.EndUs, endUs)
		if b > a {
			if a > startUs {
				points = append(points, a)
			}
			if b < endUs {
				points = append(points, b)
			}
		} else if e.StartUs > startUs && e.StartUs < endUs {
			points = append(points, e.StartUs)
		}
	}
	if len(points) == 1 {
		return nil
	}
	points = append(points, endUs)
	sort.Slice(points, func(i, j int) bool { return points[i] < points[j] })
	points = dedupeBoundaries(points, eventBoundaryMergeUs)
	if len(points) < 3 {
		return nil
	}
	return points
}

func segmentEventIntervals(startUs, endUs int64, events []EventBoundary) []segmentInterval {
	out := make([]segmentInterval, 0, len(events))
	seen := make(map[segmentInterval]bool, len(events))
	for _, e := range events {
		// HKWorkoutEventTypeSegment=7. Pause/resume requests are not segments.
		if e.Type != 7 {
			continue
		}
		a, b := max64(e.StartUs, startUs), min64(e.EndUs, endUs)
		if b-a < minSegmentEventUs {
			continue
		}
		iv := segmentInterval{startUs: a, endUs: b}
		if seen[iv] {
			continue
		}
		seen[iv] = true
		out = append(out, iv)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].startUs == out[j].startUs {
			return out[i].endUs < out[j].endUs
		}
		return out[i].startUs < out[j].startUs
	})
	return out
}

func intervalsOverlap(intervals []segmentInterval) bool {
	lastEnd := intervals[0].endUs
	for _, iv := range intervals[1:] {
		if iv.startUs < lastEnd-eventBoundaryToleranceUs {
			return true
		}
		if iv.endUs > lastEnd {
			lastEnd = iv.endUs
		}
	}
	return false
}

func intervalBoundaries(startUs, endUs int64, intervals []segmentInterval) []int64 {
	points := make([]int64, 0, len(intervals)*2+2)
	points = append(points, startUs)
	for _, iv := range intervals {
		if iv.startUs > startUs && iv.startUs < endUs {
			points = append(points, iv.startUs)
		}
		if iv.endUs > startUs && iv.endUs < endUs {
			points = append(points, iv.endUs)
		}
	}
	points = append(points, endUs)
	sort.Slice(points, func(i, j int) bool { return points[i] < points[j] })
	return dedupeBoundaries(points, eventBoundaryMergeUs)
}

// alignedSegmentBoundaries finds a partition formed by exactly planSteps
// non-overlapping intervals. A second matching partition means the source is
// ambiguous and no rows are produced.
func alignedSegmentBoundaries(startUs, endUs int64, intervals []segmentInterval, planSteps int) []int64 {
	if planSteps < 2 || planSteps > len(intervals) {
		return nil
	}
	var solution []int64
	solutions, steps := 0, 0
	var walk func(index int, chosen []segmentInterval)
	walk = func(index int, chosen []segmentInterval) {
		steps++
		if solutions > 1 || steps > 100_000 {
			return
		}
		last := chosen[len(chosen)-1]
		if len(chosen) == planSteps {
			if abs64(last.endUs-endUs) <= eventBoundaryMergeUs {
				solutions++
				solution = alignedBoundaries(startUs, endUs, chosen)
			}
			return
		}
		for j := index + 1; j < len(intervals); j++ {
			if intervals[j].startUs > last.endUs+eventBoundaryToleranceUs {
				break
			}
			if abs64(intervals[j].startUs-last.endUs) > eventBoundaryToleranceUs {
				continue
			}
			next := append(append([]segmentInterval(nil), chosen...), intervals[j])
			walk(j, next)
			if solutions > 1 || steps > 100_000 {
				return
			}
		}
	}
	for i, iv := range intervals {
		if abs64(iv.startUs-startUs) > eventBoundaryMergeUs {
			continue
		}
		walk(i, []segmentInterval{iv})
		if solutions > 1 || steps > 100_000 {
			break
		}
	}
	if solutions != 1 || steps > 100_000 {
		return nil
	}
	return solution
}

func alignedBoundaries(startUs, endUs int64, chosen []segmentInterval) []int64 {
	points := make([]int64, 0, len(chosen)+1)
	points = append(points, startUs)
	for i := 0; i < len(chosen)-1; i++ {
		points = append(points, chosen[i].endUs)
	}
	points = append(points, endUs)
	for i := 1; i < len(points); i++ {
		if points[i] <= points[i-1] {
			return nil
		}
	}
	return points
}

func dedupeBoundaries(xs []int64, minGapUs int64) []int64 {
	if len(xs) == 0 {
		return xs
	}
	out := []int64{xs[0]}
	for _, x := range xs[1:] {
		if x-out[len(out)-1] >= minGapUs {
			out = append(out, x)
		}
	}
	return out
}

func flattenPlanSteps(steps []PlannedStep) []PlannedStep {
	out := []PlannedStep{}
	for _, s := range steps {
		repeats := s.Repeats
		if repeats < 1 {
			repeats = 1
		}
		for i := 0; i < repeats; i++ {
			step := s
			step.Repeats = 0
			if repeats > 1 {
				step.Label = fmt.Sprintf("%s %d/%d", s.PurposeLabel(), i+1, repeats)
			}
			out = append(out, step)
			// Training totals charge recovery after every repetition, including
			// the last, so the segment descriptors must include it too.
			if s.RecoveryS > 0 {
				out = append(out, PlannedStep{Label: "恢复", Purpose: "recovery", DurationS: s.RecoveryS})
			}
		}
	}
	return out
}

// PurposeLabel returns the display label used for repeated work steps.
func (s PlannedStep) PurposeLabel() string {
	if s.Label != "" {
		return s.Label
	}
	switch s.Purpose {
	case "warmup":
		return "热身"
	case "cooldown":
		return "整理放松"
	case "recovery":
		return "恢复"
	default:
		return "训练"
	}
}

func joinNote(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "；" + b
}

func trackMetersAt(track *speedTrack, atUs int64) float64 {
	var meters float64
	for _, s := range track.samples {
		if atUs <= s.startUs {
			break
		}
		end := min64(atUs, s.endUs)
		if end > s.startUs {
			meters += s.meters * float64(end-s.startUs) / float64(s.endUs-s.startUs)
		}
	}
	return meters
}

// ApplyStimulusScores adds a conservative plan-backed stimulus read-out. It
// deliberately returns no score for an unmatched free run: without a target
// there is no completion fraction to report, and inventing one would turn a
// zone distribution into a performance judgement.
func ApplyStimulusScores(zones *ZoneSummary, plan *PlanContext, heartCoverage, durationS float64) {
	if zones == nil || plan == nil || plan.DurationS <= 0 || heartCoverage <= 0 {
		return
	}
	primary := ""
	switch plan.Kind {
	case "RECOVERY":
		primary = "recovery"
	case "EASY", "LONG":
		primary = "aerobic_base"
	case "TEMPO":
		primary = "lactate_threshold"
	case "INTERVAL":
		primary = "vo2max"
	default:
		return
	}
	stimulusZones := map[string][]string{
		"recovery":          {"recovery"},
		"aerobic_base":      {"aerobic_base"},
		"lactate_threshold": {"lactate_threshold"},
		"vo2max":            {"anaerobic"},
	}
	var qualifying float64
	for _, z := range zones.HeartRateExposure {
		for _, wanted := range stimulusZones[primary] {
			if z.Zone == wanted {
				qualifying += z.DurationSeconds
			}
		}
	}
	target := plan.DurationS
	if target <= 0 {
		target = durationS
	}
	if target <= 0 {
		return
	}
	confidence := clamp(heartCoverage*0.9, 0, 0.95)
	score := clamp(qualifying/target*confidence, 0, 1)
	zones.StimulusType = primary
	zones.StimulusConfidence = Ptr(round(confidence, 3))
	zones.StimulusScores = []StimulusScore{{StimulusType: primary, QualifyingSeconds: round(qualifying, 1), TargetSeconds: round(target, 1), Score: round(score, 4), Confidence: round(confidence, 3)}}
	zones.SecondaryStimulusTypes = []string{}
	for _, z := range zones.HeartRateExposure {
		if z.Zone == "" || z.Fraction < 0.10 || z.Zone == primary {
			continue
		}
		if z.Zone == "aerobic_power" && primary == "aerobic_base" {
			continue
		}
		zones.SecondaryStimulusTypes = append(zones.SecondaryStimulusTypes, z.Zone)
	}
}
