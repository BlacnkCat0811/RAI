package workoutfeatures

import (
	"math"
	"sort"

	"rai/server/internal/workoutdomain"
)

// RunFitnessSchema versions the neutral running-fitness features. They hold no
// heart-rate reference, score or ability: the runfitness model turns them into
// a single-run result for whatever reference was in effect at the time.
const RunFitnessSchema = "rai.run_fitness_features/1"

// Run-fitness timeline policy (RAI v1 design parameters, part of the features
// configuration hash).
const (
	rfSegmentSeconds    = 180
	rfWarmupSeconds     = 120
	rfResumeSeconds     = 90
	rfHRGapSeconds      = 15
	rfSpeedGapSeconds   = 10
	rfSprintGapSeconds  = 5
	rfHRMedianSeconds   = 15
	rfSpeedSmoothSecond = 10
	rfBlockSeconds      = 900
	rfLongRunSeconds    = 3600
	rfLongRunSkip       = 600
	rfMinRunningSpeed   = 1.4
)

// RunFitnessPolicy is hashed into the features configuration.
func RunFitnessPolicy() string {
	return "segment180-greedy;warmup120;resume90;hr-gap15;speed-gap10;hr-median15;speed-mean10;cycle3x-600s;best60/90/120-native5;best180/360/720/1200/2400-native10;block900-after600;peak-hr15/v1"
}

// RunFitnessSegment is one deterministic 180-second window, or one
// cycle-averaged interval block. Means are over seconds that carry both
// heart rate and speed support.
type RunFitnessSegment struct {
	Kind         string   `json:"kind"`
	Start        int64    `json:"startUs"`
	End          int64    `json:"endUs"`
	Seconds      float64  `json:"seconds"`
	RunningAt    float64  `json:"runningSecondsBefore"`
	Speed        float64  `json:"meanSpeedMPS"`
	FlatSpeed    *float64 `json:"flatSpeedMPS"`
	Grade        *float64 `json:"meanGrade"`
	MaxAbsGrade  *float64 `json:"maxAbsGrade"`
	HR           float64  `json:"meanHR"`
	HRTrend      float64  `json:"hrTrendBPMPerMinute"`
	SpeedCV      float64  `json:"speedCV"`
	MinSpeed     float64  `json:"minSmoothedSpeedMPS"`
	Coverage     float64  `json:"coverage"`
	GradeSupport float64  `json:"gradeCoverage"`
	SpeedEpoch   string   `json:"speedEpoch"`
	HREpoch      string   `json:"hrEpoch"`
	// WalkRecovery marks a cycle block whose recoveries were walks or stops;
	// it can only provide a demonstrated lower bound.
	WalkRecovery bool `json:"walkRecovery,omitempty"`
	Cycles       int  `json:"cycles,omitempty"`
}

// RunFitnessBest is the fastest contiguous native-supported window of one
// duration. It is a demonstrated lower bound, never a confirmed maximum.
type RunFitnessBest struct {
	Duration  int      `json:"durationSeconds"`
	Start     int64    `json:"startUs"`
	End       int64    `json:"endUs"`
	Speed     float64  `json:"meanSpeedMPS"`
	FlatSpeed *float64 `json:"flatSpeedMPS"`
	MaxGap    float64  `json:"maxNativeGapSeconds"`
	MeanHR    *float64 `json:"meanHR"`
}

// RunFitnessBlock is one 15-minute block of a continuous long run, used only
// for the efficiency-retention description.
type RunFitnessBlock struct {
	Start     int64    `json:"startUs"`
	End       int64    `json:"endUs"`
	Speed     float64  `json:"meanSpeedMPS"`
	FlatSpeed *float64 `json:"flatSpeedMPS"`
	MaxGrade  *float64 `json:"maxAbsGrade"`
	HR        float64  `json:"meanHR"`
	Coverage  float64  `json:"coverage"`
	SpeedCV   float64  `json:"speedCV"`
	Epoch     string   `json:"epoch"`
}

type RunFitnessFeatures struct {
	Schema         string              `json:"schemaVersion"`
	Policy         string              `json:"policy"`
	RunningSeconds float64             `json:"runningSeconds"`
	LongestRun     float64             `json:"longestContinuousRunSeconds"`
	Segments       []RunFitnessSegment `json:"segments"`
	Best           []RunFitnessBest    `json:"best"`
	Blocks         []RunFitnessBlock   `json:"longRunBlocks"`
	// PeakHR15 is the highest heart rate held for at least 15 seconds after
	// 15-second median smoothing; it can only suggest a new maximum.
	PeakHR15 *float64 `json:"peakHR15"`
	// DistanceCheck: consistent (<=5%), mismatch (>5%) or unavailable.
	DistanceCheck string   `json:"distanceCheck"`
	DistanceM     *float64 `json:"distanceM"`
	Elapsed       float64  `json:"elapsedSeconds"`
	Indoor        *bool    `json:"indoor"`
	TemperatureC  *float64 `json:"temperatureC"`
	Humidity      *float64 `json:"humidity"`
	SampleSeconds *float64 `json:"medianNativeSpeedIntervalSeconds"`
}

type rfSecond struct {
	hr, speed, grade       float64 // NaN when unsupported
	speedGap               float64
	active                 int // pause.Active index or -1
	speedEpoch, hrEpoch    string
	speedOK, hrOK, gradeOK bool
}

func runFitnessFeatures(s RawWorkoutSnapshot, t AnalysisTimeline, distance DistanceChoice, env Environment) *RunFitnessFeatures {
	out := &RunFitnessFeatures{Schema: RunFitnessSchema, Policy: RunFitnessPolicy(), Segments: []RunFitnessSegment{}, Best: []RunFitnessBest{}, Blocks: []RunFitnessBlock{},
		DistanceM: distance.Preferred, Elapsed: float64(s.End-s.Start) / 1e6, Indoor: env.Indoor, TemperatureC: env.TemperatureC, Humidity: env.Humidity}
	out.DistanceCheck = "unavailable"
	ratio := distance.DifferenceRatio
	if distance.RouteDifferenceRatio != nil && (ratio == nil || *distance.RouteDifferenceRatio > *ratio) {
		ratio = distance.RouteDifferenceRatio
	}
	if ratio != nil {
		out.DistanceCheck = "consistent"
		if *ratio > .05 {
			out.DistanceCheck = "mismatch"
		}
	}
	if gaps := nativeSpeedIntervals(t.Speed.Spans); len(gaps) > 0 {
		sort.Float64s(gaps)
		out.SampleSeconds = ptr(gaps[len(gaps)/2])
	}
	n := len(t.Cells)
	if n == 0 {
		return out
	}
	sec := make([]rfSecond, n)
	for i, c := range t.Cells {
		a := t.Start + int64(i)*1e6
		x := rfSecond{hr: math.NaN(), speed: math.NaN(), grade: math.NaN(), speedGap: c.speedGap, active: containing(t.Pause.Active, a, a+1e6), speedEpoch: c.epoch, hrEpoch: c.hrEpoch}
		if c.continuous && c.speedGap <= rfSpeedGapSeconds && finite(c.speed) {
			x.speed, x.speedOK = c.speed, true
		}
		if c.hrTime >= .95 && c.hrEpoch != "" && c.hrEpoch != "ambiguous" {
			x.hr, x.hrOK = c.hr/c.hrTime, true
		}
		if c.gradeTime >= .9 {
			x.grade, x.gradeOK = c.grade/c.gradeTime, true
		}
		if x.active < 0 {
			x.speedOK, x.hrOK = false, false
		}
		sec[i] = x
	}
	hrSmooth := medianSmooth(sec, rfHRMedianSeconds)
	speedSmooth := meanSmooth(sec, rfSpeedSmoothSecond)
	for i := range sec {
		if sec[i].speedOK && speedSmooth[i] >= rfMinRunningSpeed {
			out.RunningSeconds++
		}
	}
	out.PeakHR15 = peakHR(hrSmooth, sec)
	intervals := usableIntervals(sec, t.Pause.Active, t.Start)
	running := 0.0
	cycleSpans := [][2]int{}
	for _, iv := range intervals {
		if c, ok := cycleSegment(sec, hrSmooth, speedSmooth, iv, t.Start, running); ok {
			out.Segments = append(out.Segments, c)
			cycleSpans = append(cycleSpans, [2]int{int((c.Start - t.Start) / 1e6), int((c.End - t.Start) / 1e6)})
		}
		for a := iv.a; a+rfSegmentSeconds <= iv.b; a += rfSegmentSeconds {
			overlaps := false
			for _, span := range cycleSpans {
				if a < span[1] && a+rfSegmentSeconds > span[0] {
					overlaps = true
				}
			}
			if overlaps {
				continue
			}
			if seg, ok := steadySegment(sec, hrSmooth, speedSmooth, a, a+rfSegmentSeconds, t.Start, running+float64(a-iv.a)); ok {
				out.Segments = append(out.Segments, seg)
			}
		}
		running += float64(iv.b - iv.a)
	}
	sort.SliceStable(out.Segments, func(i, j int) bool { return out.Segments[i].Start < out.Segments[j].Start })
	for _, d := range []int{60, 90, 120, 180, 360, 720, 1200, 2400} {
		gap := float64(rfSpeedGapSeconds)
		if d <= 120 {
			gap = rfSprintGapSeconds
		}
		if b := bestWindow(sec, hrSmooth, d, gap, t.Start); b != nil {
			out.Best = append(out.Best, *b)
		}
	}
	out.LongestRun, out.Blocks = longRunBlocks(sec, hrSmooth, speedSmooth, t.Start)
	return out
}

func nativeSpeedIntervals(spans []Span) []float64 {
	out := []float64{}
	for _, x := range spans {
		if !x.Reconstructed {
			out = append(out, float64(x.End-x.Start)/1e6)
		}
	}
	return out
}

// medianSmooth is the centred 15-second median of supported heart rate.
func medianSmooth(sec []rfSecond, width int) []float64 {
	out := make([]float64, len(sec))
	buf := make([]float64, 0, width)
	half := width / 2
	for i := range sec {
		out[i] = math.NaN()
		if !sec[i].hrOK {
			continue
		}
		buf = buf[:0]
		for j := max(0, i-half); j <= min(len(sec)-1, i+half); j++ {
			if sec[j].hrOK && sec[j].active == sec[i].active {
				buf = append(buf, sec[j].hr)
			}
		}
		sort.Float64s(buf)
		m := len(buf)
		if m%2 == 1 {
			out[i] = buf[m/2]
		} else {
			out[i] = (buf[m/2-1] + buf[m/2]) / 2
		}
	}
	return out
}

// meanSmooth is the centred 10-second mean of supported speed.
func meanSmooth(sec []rfSecond, width int) []float64 {
	out := make([]float64, len(sec))
	half := width / 2
	for i := range sec {
		out[i] = math.NaN()
		if !sec[i].speedOK {
			continue
		}
		sum, k := 0.0, 0
		for j := max(0, i-half); j < min(len(sec), i-half+width); j++ {
			if sec[j].speedOK && sec[j].active == sec[i].active {
				sum += sec[j].speed
				k++
			}
		}
		out[i] = sum / float64(k)
	}
	return out
}

func peakHR(smooth []float64, sec []rfSecond) *float64 {
	var best *float64
	for i := 0; i+rfHRMedianSeconds <= len(smooth); i++ {
		low := math.Inf(1)
		for j := i; j < i+rfHRMedianSeconds; j++ {
			if math.IsNaN(smooth[j]) || sec[j].active != sec[i].active {
				low = math.NaN()
				break
			}
			low = math.Min(low, smooth[j])
		}
		if !math.IsNaN(low) && (best == nil || low > *best) {
			best = ptr(low)
		}
	}
	return best
}

type cellRange struct{ a, b int }

// usableIntervals: per active interval, skip the warm-up (first interval) or
// the post-resume settling period, then split where heart rate is missing for
// more than 15 seconds or speed for more than 10 seconds.
func usableIntervals(sec []rfSecond, active []Interval, start int64) []cellRange {
	out := []cellRange{}
	for k, iv := range active {
		skip := rfResumeSeconds
		if k == 0 {
			skip = rfWarmupSeconds
		}
		a := int((iv.Start-start+999_999)/1e6) + skip
		b := int((iv.End - start) / 1e6)
		a, b = max(a, 0), min(b, len(sec))
		cur := -1
		hrMiss, speedMiss := 0, 0
		for i := a; i < b; i++ {
			if sec[i].hrOK {
				hrMiss = 0
			} else {
				hrMiss++
			}
			if sec[i].speedOK {
				speedMiss = 0
			} else {
				speedMiss++
			}
			if cur < 0 {
				if sec[i].hrOK && sec[i].speedOK {
					cur, hrMiss, speedMiss = i, 0, 0
				}
				continue
			}
			if hrMiss > rfHRGapSeconds || speedMiss > rfSpeedGapSeconds {
				end := i - max(hrMiss, speedMiss) + 1
				if end > cur {
					out = append(out, cellRange{cur, end})
				}
				cur = -1
			}
		}
		if cur >= 0 && b > cur {
			out = append(out, cellRange{cur, b})
		}
	}
	return out
}

func flatSpeed(sec []rfSecond, a, b int) (flat, grade, maxGrade *float64, coverage float64) {
	speedSec, gradedSec := 0.0, 0.0
	sumFlat, sumGrade, top := 0.0, 0.0, 0.0
	for i := a; i < b; i++ {
		if !sec[i].speedOK {
			continue
		}
		speedSec++
		if sec[i].gradeOK {
			gradedSec++
			g := sec[i].grade
			sumFlat += sec[i].speed * workoutdomain.GradeAdjustment(g)
			sumGrade += g
			top = math.Max(top, math.Abs(g))
		}
	}
	if speedSec == 0 {
		return nil, nil, nil, 0
	}
	coverage = gradedSec / speedSec
	if coverage < .9 {
		return nil, nil, nil, coverage
	}
	// Seconds without grade support carry the window's mean flat factor.
	return ptr(sumFlat / gradedSec), ptr(sumGrade / gradedSec), ptr(top), coverage
}

func steadySegment(sec []rfSecond, hrSmooth, speedSmooth []float64, a, b int, start int64, runningAt float64) (RunFitnessSegment, bool) {
	both, speedSum, hrSum := 0.0, 0.0, 0.0
	var ts, hs, ss []float64
	minSpeed := math.Inf(1)
	speedEpoch, hrEpoch := "", ""
	for i := a; i < b; i++ {
		x := sec[i]
		if x.speedOK {
			if speedEpoch == "" {
				speedEpoch = x.speedEpoch
			} else if speedEpoch != x.speedEpoch {
				speedEpoch = "ambiguous"
			}
			ss = append(ss, speedSmooth[i])
			minSpeed = math.Min(minSpeed, speedSmooth[i])
		}
		if x.hrOK {
			if hrEpoch == "" {
				hrEpoch = x.hrEpoch
			} else if hrEpoch != x.hrEpoch {
				hrEpoch = "ambiguous"
			}
		}
		if x.speedOK && x.hrOK && !math.IsNaN(hrSmooth[i]) {
			both++
			speedSum += x.speed
			hrSum += hrSmooth[i]
			ts = append(ts, float64(i-a))
			hs = append(hs, hrSmooth[i])
		}
	}
	if both < 30 {
		return RunFitnessSegment{}, false
	}
	seg := RunFitnessSegment{Kind: "steady", Start: start + int64(a)*1e6, End: start + int64(b)*1e6, Seconds: float64(b - a), RunningAt: runningAt,
		Speed: speedSum / both, HR: hrSum / both, Coverage: both / float64(b-a), SpeedEpoch: speedEpoch, HREpoch: hrEpoch, MinSpeed: minSpeed}
	seg.HRTrend = slope(ts, hs) * 60
	seg.SpeedCV = cv(ss)
	seg.FlatSpeed, seg.Grade, seg.MaxAbsGrade, seg.GradeSupport = flatSpeed(sec, a, b)
	return seg, true
}

func slope(x, y []float64) float64 {
	if len(x) < 2 {
		return 0
	}
	mx, my := 0.0, 0.0
	for i := range x {
		mx += x[i]
		my += y[i]
	}
	mx /= float64(len(x))
	my /= float64(len(x))
	num, den := 0.0, 0.0
	for i := range x {
		num += (x[i] - mx) * (y[i] - my)
		den += (x[i] - mx) * (x[i] - mx)
	}
	if den == 0 {
		return 0
	}
	return num / den
}

func cv(xs []float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	m := 0.0
	for _, x := range xs {
		m += x
	}
	m /= float64(len(xs))
	if m <= 0 {
		return math.Inf(1)
	}
	v := 0.0
	for _, x := range xs {
		v += (x - m) * (x - m)
	}
	return math.Sqrt(v/float64(len(xs)-1)) / m
}

// cycleSegment recognises >=3 consecutive complete fast+recovery cycles that
// together last >=600 s in one usable interval and averages them as one block.
func cycleSegment(sec []rfSecond, hrSmooth, speedSmooth []float64, iv cellRange, start int64, runningBefore float64) (RunFitnessSegment, bool) {
	vals := []float64{}
	for i := iv.a; i < iv.b; i++ {
		if !math.IsNaN(speedSmooth[i]) {
			vals = append(vals, speedSmooth[i])
		}
	}
	if len(vals) < 600 {
		return RunFitnessSegment{}, false
	}
	sorted := append([]float64{}, vals...)
	sort.Float64s(sorted)
	p20, p80 := sorted[len(sorted)/5], sorted[len(sorted)*4/5]
	if p20 <= 0 || p80/p20 < 1.15 {
		return RunFitnessSegment{}, false
	}
	threshold := (p20 + p80) / 2
	type phase struct {
		a, b int
		fast bool
		min  float64
	}
	phases := []phase{}
	for i := iv.a; i < iv.b; i++ {
		v := speedSmooth[i]
		fast := !math.IsNaN(v) && v >= threshold
		if len(phases) == 0 || phases[len(phases)-1].fast != fast {
			phases = append(phases, phase{a: i, b: i + 1, fast: fast, min: math.Inf(1)})
		} else {
			phases[len(phases)-1].b = i + 1
		}
		p := &phases[len(phases)-1]
		if !math.IsNaN(v) {
			p.min = math.Min(p.min, v)
		} else {
			p.min = 0
		}
	}
	// Merge flicker shorter than 20 s into its neighbour.
	merged := []phase{}
	for _, p := range phases {
		if len(merged) > 0 && (p.b-p.a < 20 || merged[len(merged)-1].fast == p.fast) {
			last := &merged[len(merged)-1]
			last.b = p.b
			last.min = math.Min(last.min, p.min)
			continue
		}
		merged = append(merged, p)
	}
	bestA, bestB, bestCycles, walk := -1, -1, 0, false
	for i := 0; i+1 < len(merged); i++ {
		if !merged[i].fast {
			continue
		}
		cycles, j, walked := 0, i, false
		for j+1 < len(merged) && merged[j].fast && !merged[j+1].fast && merged[j].b-merged[j].a >= 20 && merged[j+1].b-merged[j+1].a >= 20 {
			cycles++
			if merged[j+1].min < rfMinRunningSpeed {
				walked = true
			}
			j += 2
		}
		if cycles >= 3 {
			a, b := merged[i].a, merged[j-1].b
			if b-a >= 600 && cycles > bestCycles {
				bestA, bestB, bestCycles, walk = a, b, cycles, walked
			}
		}
	}
	if bestA < 0 {
		return RunFitnessSegment{}, false
	}
	seg, ok := steadySegment(sec, hrSmooth, speedSmooth, bestA, bestB, start, runningBefore+float64(bestA-iv.a))
	if !ok {
		return RunFitnessSegment{}, false
	}
	seg.Kind, seg.Cycles, seg.WalkRecovery = "cycle_averaged", bestCycles, walk
	return seg, true
}

func bestWindow(sec []rfSecond, hrSmooth []float64, d int, maxGap float64, start int64) *RunFitnessBest {
	var best *RunFitnessBest
	run := 0
	sum := 0.0
	for i := range sec {
		ok := sec[i].speedOK && sec[i].speedGap <= maxGap && (i == 0 || run == 0 || sec[i].active == sec[i-1].active)
		if !ok {
			run, sum = 0, 0
			continue
		}
		run++
		sum += sec[i].speed
		if run > d {
			sum -= sec[i-d].speed
		}
		if run >= d {
			v := sum / float64(d)
			if best == nil || v > best.Speed+1e-12 {
				a := i + 1 - d
				gap := 0.0
				for j := a; j <= i; j++ {
					gap = math.Max(gap, sec[j].speedGap)
				}
				best = &RunFitnessBest{Duration: d, Start: start + int64(a)*1e6, End: start + int64(i+1)*1e6, Speed: v, MaxGap: gap}
			}
		}
	}
	if best != nil {
		a, b := int((best.Start-start)/1e6), int((best.End-start)/1e6)
		best.FlatSpeed, _, _, _ = flatSpeed(sec, a, b)
		sum, k := 0.0, 0.0
		for j := a; j < b; j++ {
			if !math.IsNaN(hrSmooth[j]) {
				sum += hrSmooth[j]
				k++
			}
		}
		if k >= .9*float64(b-a) {
			best.MeanHR = ptr(sum / k)
		}
	}
	return best
}

// longRunBlocks: the longest continuous running stretch; when it lasts at
// least 60 minutes, consecutive 15-minute blocks after its first 10 minutes.
func longRunBlocks(sec []rfSecond, hrSmooth, speedSmooth []float64, start int64) (float64, []RunFitnessBlock) {
	bestA, bestB, cur := 0, 0, -1
	for i := 0; i <= len(sec); i++ {
		ok := i < len(sec) && sec[i].speedOK && (cur < 0 || sec[i].active == sec[cur].active)
		if ok && cur < 0 {
			cur = i
		}
		if !ok && cur >= 0 {
			if i-cur > bestB-bestA {
				bestA, bestB = cur, i
			}
			cur = -1
			if i < len(sec) && sec[i].speedOK {
				cur = i
			}
		}
	}
	blocks := []RunFitnessBlock{}
	if bestB-bestA < rfLongRunSeconds {
		return float64(bestB - bestA), blocks
	}
	for a := bestA + rfLongRunSkip; a+rfBlockSeconds <= bestB; a += rfBlockSeconds {
		b := a + rfBlockSeconds
		both, speedSum, hrSum := 0.0, 0.0, 0.0
		var ss []float64
		epoch := ""
		for i := a; i < b; i++ {
			if sec[i].speedOK {
				ss = append(ss, speedSmooth[i])
				if epoch == "" {
					epoch = sec[i].speedEpoch
				} else if epoch != sec[i].speedEpoch {
					epoch = "ambiguous"
				}
			}
			if sec[i].speedOK && !math.IsNaN(hrSmooth[i]) {
				both++
				speedSum += sec[i].speed
				hrSum += hrSmooth[i]
			}
		}
		if both == 0 {
			continue
		}
		block := RunFitnessBlock{Start: start + int64(a)*1e6, End: start + int64(b)*1e6, Speed: speedSum / both, HR: hrSum / both, Coverage: both / float64(b-a), SpeedCV: cv(ss), Epoch: epoch}
		block.FlatSpeed, _, block.MaxGrade, _ = flatSpeed(sec, a, b)
		blocks = append(blocks, block)
	}
	return float64(bestB - bestA), blocks
}
