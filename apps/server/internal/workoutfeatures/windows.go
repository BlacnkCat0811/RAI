package workoutfeatures

import "math"

// MaxGradeAdjusted is the steepest 100 m grade the grade-adjusted (v2)
// performance windows convert to flat-equivalent speed with the Minetti cost.
const MaxGradeAdjusted = .08

type prefix struct {
	gradeMeters, flatMeters                                   []float64
	modestMeters, gapMeters                                   []float64
	speed, speed2, hr, hrTime, moment, grade, gradeTime, flat []float64
	bad, hrBad                                                []int
}

func prefixes(t AnalysisTimeline) prefix {
	n := len(t.Cells) + 1
	p := prefix{modestMeters: make([]float64, n), gapMeters: make([]float64, n), speed: make([]float64, n), speed2: make([]float64, n), hr: make([]float64, n), hrTime: make([]float64, n), moment: make([]float64, n), grade: make([]float64, n), gradeTime: make([]float64, n), flat: make([]float64, n), gradeMeters: make([]float64, n), flatMeters: make([]float64, n), bad: make([]int, n), hrBad: make([]int, n)}
	for i, c := range t.Cells {
		j := i + 1
		p.speed[j] = p.speed[i] + c.speed
		p.gradeMeters[j] = p.gradeMeters[i] + c.gradedMeters
		p.flatMeters[j] = p.flatMeters[i] + c.flatMeters
		p.modestMeters[j] = p.modestMeters[i] + c.modestMeters
		p.gapMeters[j] = p.gapMeters[i] + c.gapMeters
		p.speed2[j] = p.speed2[i] + c.speed2
		p.hr[j] = p.hr[i] + c.hr
		p.hrTime[j] = p.hrTime[i] + c.hrTime
		p.moment[j] = p.moment[i] + c.hrMoment
		p.grade[j] = p.grade[i] + c.grade
		p.gradeTime[j] = p.gradeTime[i] + c.gradeTime
		p.flat[j] = p.flat[i] + c.flatTime
		p.bad[j] = p.bad[i]
		p.hrBad[j] = p.hrBad[i]
		if !c.continuous {
			p.bad[j]++
		}
		if i > 0 && (c.epoch != t.Cells[i-1].epoch || c.activeSegment != t.Cells[i-1].activeSegment) {
			p.bad[j]++
		}
		if c.hrTime < 1-1e-9 || c.hrEpoch == "ambiguous" || c.hrEpoch == "" {
			p.hrBad[j]++
		}
		if i > 0 && c.hrEpoch != t.Cells[i-1].hrEpoch {
			p.hrBad[j]++
		}
	}
	return p
}
func (p prefix) continuous(t AnalysisTimeline, a, b int) bool {
	if a < 0 || b > len(t.Cells) || b <= a {
		return false
	}
	// Exclude a source boundary at the start itself; it cannot join a prior cell.
	bad := p.bad[b] - p.bad[a]
	if a > 0 && (t.Cells[a].epoch != t.Cells[a-1].epoch || t.Cells[a].activeSegment != t.Cells[a-1].activeSegment) {
		bad--
	}
	return bad == 0
}
func qual(hard, soft []Reason) Qualification {
	status := "eligible"
	if len(soft) > 0 {
		status = "conditional"
	}
	if len(hard) > 0 {
		status = "ineligible"
	}
	return Qualification{status, reasonSet(hard, soft)}
}
func rangeWindow(t AnalysisTimeline, p prefix, a, d int, evidence string) Window {
	b := a + d
	distance := p.speed[b] - p.speed[a]
	return Window{Start: t.Start + int64(a)*1e6, End: t.Start + int64(b)*1e6, Duration: d, Distance: distance, MeanSpeed: distance / float64(d), Coverage: 1, Quality: "supported", SourceEpoch: t.Cells[a].epoch, Evidence: evidence}
}
func performanceQual(p prefix, a, b int, base []Reason) Qualification {
	hard := append([]Reason{}, base...)
	d := p.speed[b] - p.speed[a]
	if d <= 0 || (p.gradeMeters[b]-p.gradeMeters[a])/d < .9 {
		hard = append(hard, GradeUnknown)
	} else if (p.flatMeters[b]-p.flatMeters[a])/d < .9 {
		hard = append(hard, RouteAccuracyLow)
	}
	return qual(hard, []Reason{EffortUnconfirmed})
}

// gapQual is the grade-adjusted counterpart of performanceQual: hills up to
// MaxGradeAdjusted are converted, not excluded; only unknown or steeper terrain
// over more than a tenth of the distance disqualifies the window.
func gapQual(p prefix, a, b int, base []Reason) Qualification {
	hard := append([]Reason{}, base...)
	d := p.speed[b] - p.speed[a]
	if d <= 0 || (p.gradeMeters[b]-p.gradeMeters[a])/d < .9 {
		hard = append(hard, GradeUnknown)
	} else if (p.modestMeters[b]-p.modestMeters[a])/d < .9 {
		hard = append(hard, RouteAccuracyLow)
	}
	return qual(hard, []Reason{EffortUnconfirmed})
}

// gapDistance is the flat-equivalent distance: graded metres within
// MaxGradeAdjusted are converted, the remainder counts as run.
func gapDistance(p prefix, a, b int) float64 {
	d := p.speed[b] - p.speed[a]
	return p.gapMeters[b] - p.gapMeters[a] + d - (p.modestMeters[b] - p.modestMeters[a])
}

func ExtractWindows(t AnalysisTimeline, check DistanceChoice, env Environment, evidence string) []WindowCandidates {
	return extractWindows(t, check, env, evidence, prefixes(t))
}
func extractWindows(t AnalysisTimeline, check DistanceChoice, env Environment, evidence string, p prefix) []WindowCandidates {
	nativeBad := make([]int, len(t.Cells)+1)
	for i, c := range t.Cells {
		nativeBad[i+1] = nativeBad[i]
		if c.speedGap > 5 {
			nativeBad[i+1]++
		}
	}
	out := []WindowCandidates{}
	base := []Reason{}
	if env.Indoor == nil {
		base = append(base, EnvironmentUnknown)
	} else if *env.Indoor {
		base = append(base, IndoorUnsupported)
	}
	if check.RouteDifferenceRatio != nil && *check.RouteDifferenceRatio > .05 {
		base = append(base, DistanceMismatch)
	}
	if check.Preferred == nil || *check.Preferred <= 0 {
		base = append(base, DistanceMissing)
	}
	if check.DifferenceRatio == nil {
		base = append(base, DistanceMissing)
	} else if *check.DifferenceRatio > 0.05 {
		base = append(base, DistanceMismatch)
	}
	for _, d := range []int{180, 300, 600, 1200, 1800, 2400, 3600, 5400} {
		c := WindowCandidates{Duration: d}
		search := func(backup bool) *Window {
			var best *Window
			for a := 0; a+d <= len(t.Cells); a++ {
				b := a + d
				if !p.continuous(t, a, b) || nativeBad[b] != nativeBad[a] {
					continue
				}
				start, end := t.Start+int64(a)*1e6, t.Start+int64(b)*1e6
				if backup && c.Best != nil && start < c.Best.End && end > c.Best.Start {
					continue
				}
				distance := p.speed[b] - p.speed[a]
				good := len(base) == 0 && (p.gradeMeters[b]-p.gradeMeters[a])/distance >= .9 && (p.flatMeters[b]-p.flatMeters[a])/distance >= .9
				bestGood := best != nil && best.Qualification.Status != "ineligible"
				if best != nil && !(good && !bestGood || good == bestGood && distance > best.Distance) {
					continue
				}
				w := rangeWindow(t, p, a, d, evidence)
				w.Qualification = performanceQual(p, a, b, base)
				best = &w
			}
			return best
		}
		c.Best = search(false)
		if c.Best != nil {
			c.Backup = search(true)
		}
		if d >= 1200 {
			// Grade-adjusted best (v2 running ability): the fastest
			// flat-equivalent window, preferring qualifying windows.
			var best *Window
			bestGood := false
			for a := 0; a+d <= len(t.Cells); a++ {
				b := a + d
				if !p.continuous(t, a, b) || nativeBad[b] != nativeBad[a] || p.speed[b]-p.speed[a] <= 0 {
					continue
				}
				q := gapQual(p, a, b, base)
				good := q.Status != "ineligible"
				gap := gapDistance(p, a, b)
				if best != nil && !(good && !bestGood || good == bestGood && gap > *best.GAPDistance) {
					continue
				}
				w := rangeWindow(t, p, a, d, evidence)
				w.Qualification = q
				w.GAPDistance, w.GAPSpeed = ptr(gap), ptr(gap/float64(d))
				best, bestGood = &w, good
			}
			c.GAP = best
		}
		out = append(out, c)
	}
	return out
}
func ExtractSteady(s RawWorkoutSnapshot, t AnalysisTimeline, env Environment, parameter *Parameter, evidence string) []SteadySegment {
	return extractSteady(s, t, env, parameter, evidence, prefixes(t))
}
func extractSteady(s RawWorkoutSnapshot, t AnalysisTimeline, env Environment, parameter *Parameter, evidence string, p prefix) []SteadySegment {
	out := []SteadySegment{}
	for a := 600; a+300 <= len(t.Cells); a += 300 {
		b := a + 300
		if !p.continuous(t, a, b) {
			continue
		}
		w := rangeWindow(t, p, a, 300, evidence)
		seg := SteadySegment{Window: w, Environment: env}
		mean := w.MeanSpeed
		seg.SpeedCV = math.Sqrt(math.Max(0, (p.speed2[b]-p.speed2[a])/300-mean*mean)) / mean
		hard, soft := []Reason{}, []Reason{}
		covered := p.hrTime[b] - p.hrTime[a]
		seg.HRCoverage = covered / 300
		hrBad := p.hrBad[b] - p.hrBad[a]
		if a > 0 && t.Cells[a].hrEpoch != t.Cells[a-1].hrEpoch {
			hrBad--
		}
		if covered > 0 {
			seg.HR = ptr((p.hr[b] - p.hr[a]) / covered)
		}
		if covered >= 300-1e-6 && hrBad == 0 { // least-squares slope on native linear support
			moment := p.moment[b] - p.moment[a] - float64(a)*(p.hr[b]-p.hr[a])
			slope := (moment - 150*(p.hr[b]-p.hr[a])) / (300 * 300 * 300 / 12)
			seg.HRTrend = ptr(slope * 60)
		}
		if seg.HRCoverage < 0.95 || seg.HRTrend == nil {
			hard = append(hard, HRCoverageLow)
		} else if math.Abs(*seg.HRTrend) > 1 {
			hard = append(hard, SteadySupportLow)
		}
		if seg.SpeedCV > 0.05 || mean*60 < 134 || mean*60 > 300 {
			hard = append(hard, SteadySupportLow)
		}
		gradeTime := p.gradeTime[b] - p.gradeTime[a]
		seg.GradeCoverage = gradeTime / 300
		if gradeTime > 0 {
			seg.Grade = ptr((p.grade[b] - p.grade[a]) / gradeTime)
		}
		if gradeTime/300 < .9 || (p.gradeMeters[b]-p.gradeMeters[a])/w.Distance < .9 {
			hard = append(hard, GradeUnknown)
		} else if (p.flatMeters[b]-p.flatMeters[a])/w.Distance < .9 {
			hard = append(hard, RouteAccuracyLow)
		}
		if env.Indoor == nil {
			hard = append(hard, EnvironmentUnknown)
		} else if *env.Indoor {
			hard = append(hard, IndoorUnsupported)
		}
		if env.TemperatureC == nil || env.Humidity == nil {
			soft = append(soft, EnvironmentUnknown)
		}
		if parameter == nil {
			hard = append(hard, ParameterMissing)
		} else if seg.HR != nil {
			r := (*seg.HR - parameter.RestHR) / (parameter.MaxHR - parameter.RestHR)
			if r < 0.55 || r > 0.85 {
				hard = append(hard, SteadySupportLow)
			}
		}
		if t.Cells[a].activeSegment > 0 && w.Start-t.Pause.Active[t.Cells[a].activeSegment].Start < 180e6 {
			hard = append(hard, PauseContaminated)
		}
		if t.Pause.Timing.Paused != nil && s.End > s.Start && *t.Pause.Timing.Paused/t.Pause.Timing.Elapsed > 0.02 {
			hard = append(hard, PauseContaminated)
		}
		// Same elapsed halves and common full HR/speed support are needed for drift.
		mid := a + 150
		if hrBad == 0 {
			h1 := (p.hr[mid] - p.hr[a]) / 150
			h2 := (p.hr[b] - p.hr[mid]) / 150
			v1 := (p.speed[mid] - p.speed[a]) / 150
			v2 := (p.speed[b] - p.speed[mid]) / 150
			if h1 > 0 && h2 > 0 && math.Abs((v2/h2)/(v1/h1)-1) > 0.05 {
				hard = append(hard, SteadySupportLow)
			}
		}
		seg.Qualification = qual(hard, soft)
		seg.Window.Qualification = seg.Qualification
		out = append(out, seg)
	}
	return out
}
func ExtractLongRun(t AnalysisTimeline) LongRun { return extractLongRun(t, prefixes(t)) }
func extractLongRun(t AnalysisTimeline, p prefix) LongRun {
	out := LongRun{}
	start, bestStart, bestEnd := 0, 0, 0
	for i, c := range t.Cells {
		if !c.continuous {
			start = i + 1
			continue
		}
		if i > start && (c.epoch != t.Cells[i-1].epoch || c.activeSegment != t.Cells[i-1].activeSegment) {
			start = i
		}
		if i+1-start > bestEnd-bestStart {
			bestStart, bestEnd = start, i+1
		}
	}
	out.LongestContinuousDuration = float64(bestEnd - bestStart)
	if bestEnd-bestStart < 3600 {
		return out
	}
	a := bestStart + 600
	mid := a + (bestEnd-a)/2
	half := func(a, b int) *Half {
		d := float64(b - a)
		h := &Half{Start: t.Start + int64(a)*1e6, End: t.Start + int64(b)*1e6}
		mean := (p.speed[b] - p.speed[a]) / d
		h.Speed = ptr(mean)
		distance := p.speed[b] - p.speed[a]
		h.GradeCoverage = (p.gradeMeters[b] - p.gradeMeters[a]) / distance
		h.FlatDistanceFraction = (p.flatMeters[b] - p.flatMeters[a]) / distance
		h.SourceEpoch = t.Cells[a].epoch
		h.SpeedCV = ptr(math.Sqrt(math.Max(0, (p.speed2[b]-p.speed2[a])/d-mean*mean)) / mean)
		covered := p.hrTime[b] - p.hrTime[a]
		h.HRCoverage = covered / d
		bad := p.hrBad[b] - p.hrBad[a]
		if a > 0 && t.Cells[a].hrEpoch != t.Cells[a-1].hrEpoch {
			bad--
		}
		if covered/d >= 0.9 && bad == 0 {
			h.HR = ptr((p.hr[b] - p.hr[a]) / covered)
		}
		return h
	}
	out.First = half(a, mid)
	out.Second = half(mid, bestEnd)
	if t.Cells[a].hrEpoch != t.Cells[bestEnd-1].hrEpoch || p.hrBad[bestEnd]-p.hrBad[a] > 0 {
		out.First.HR = nil
		out.Second.HR = nil
	}
	if out.First.SpeedCV == nil || *out.First.SpeedCV > 0.1 || *out.Second.SpeedCV > 0.1 {
		out.First = nil
		out.Second = nil
	}
	return out
}
