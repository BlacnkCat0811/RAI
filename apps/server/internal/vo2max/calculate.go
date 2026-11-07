package vo2max

import (
	"math"
	"sort"
	"time"
)

const (
	minWorkoutUs   = int64(25 * 60 * 1e6)
	warmupUs       = int64(10 * 60 * 1e6)
	windowUs       = int64(5 * 60 * 1e6)
	pauseAfterUs   = int64(3 * 60 * 1e6)
	minCoverage    = 0.95
	maxPauseFrac   = 0.02
	maxSpeedCV     = 0.05
	maxHRTrend     = 1.0
	maxEFDrift     = 0.05
	maxGrade       = 0.01
	minGradeCover  = 0.90
	maxDistanceErr = 0.05
)

type span struct{ a, b int64 }

func merge(sp []span) []span {
	if len(sp) == 0 {
		return nil
	}
	sort.Slice(sp, func(i, j int) bool {
		if sp[i].a == sp[j].a {
			return sp[i].b < sp[j].b
		}
		return sp[i].a < sp[j].a
	})
	out := []span{sp[0]}
	for _, x := range sp[1:] {
		last := &out[len(out)-1]
		if x.a <= last.b {
			if x.b > last.b {
				last.b = x.b
			}
		} else {
			out = append(out, x)
		}
	}
	return out
}

func unionLen(sp []span) int64 {
	var n int64
	for _, x := range merge(sp) {
		if x.b > x.a {
			n += x.b - x.a
		}
	}
	return n
}

func intersect(a, b []span) []span {
	ma, mb := merge(a), merge(b)
	var out []span
	i, j := 0, 0
	for i < len(ma) && j < len(mb) {
		lo, hi := ma[i].a, ma[i].b
		if mb[j].a > lo {
			lo = mb[j].a
		}
		if mb[j].b < hi {
			hi = mb[j].b
		}
		if hi > lo {
			out = append(out, span{lo, hi})
		}
		if ma[i].b < mb[j].b {
			i++
		} else {
			j++
		}
	}
	return out
}

func sampleSpans(samples []Sample, hr bool, a, b int64) []span {
	var out []span
	for _, s := range samples {
		if hr && s.HR == nil {
			continue
		}
		if !hr && s.Speed == nil {
			continue
		}
		lo, hi := s.Start, s.End
		if lo < a {
			lo = a
		}
		if hi > b {
			hi = b
		}
		if hi > lo {
			out = append(out, span{lo, hi})
		}
	}
	return out
}

func valueAt(samples []Sample, hr bool, t int64) (float64, bool) {
	for i := len(samples) - 1; i >= 0; i-- {
		s := samples[i]
		if t < s.Start || t >= s.End {
			continue
		}
		v := s.Speed
		if hr {
			v = s.HR
		}
		if v == nil || !finite(*v) {
			return 0, false
		}
		return *v, true
	}
	return 0, false
}

type stats struct {
	meanHR, meanSpeed, speedCV, hrTrend, efDrift, gradeCover float64
	commonSec                                                float64
}

func windowStats(w Workout, a, b int64) (stats, string) {
	hr := sampleSpans(w.Samples, true, a, b)
	sp := sampleSpans(w.Samples, false, a, b)
	dur := float64(b-a) / 1e6
	if float64(unionLen(hr))/1e6/dur < minCoverage {
		return stats{}, "hr_support"
	}
	if float64(unionLen(sp))/1e6/dur < minCoverage {
		return stats{}, "speed_support"
	}
	common := intersect(hr, sp)
	commonSec := float64(unionLen(common)) / 1e6
	if commonSec/dur < minCoverage {
		return stats{}, "common_support"
	}
	var sumHR, sumSp, sumSp2, sumT, sumT2, sumHT float64
	var n float64
	gradeSec := 0.0
	for _, seg := range common {
		breaks := []int64{seg.a, seg.b}
		for _, s := range w.Samples {
			if s.Start > seg.a && s.Start < seg.b {
				breaks = append(breaks, s.Start)
			}
			if s.End > seg.a && s.End < seg.b {
				breaks = append(breaks, s.End)
			}
		}
		sort.Slice(breaks, func(i, j int) bool { return breaks[i] < breaks[j] })
		for k := 0; k+1 < len(breaks); k++ {
			lo, hi := breaks[k], breaks[k+1]
			if hi <= lo {
				continue
			}
			mid := lo + (hi-lo)/2
			hv, ok := valueAt(w.Samples, true, mid)
			if !ok {
				continue
			}
			sv, ok := valueAt(w.Samples, false, mid)
			if !ok {
				continue
			}
			dt := float64(hi-lo) / 1e6
			tm := (float64(mid-a) / 1e6) / 60.0
			sumHR += hv * dt
			sumSp += sv * dt
			sumSp2 += sv * sv * dt
			sumT += tm * dt
			sumT2 += tm * tm * dt
			sumHT += tm * hv * dt
			n += dt
			if g, ok := gradeAt(w.Samples, mid); ok && math.Abs(g) <= maxGrade {
				gradeSec += dt
			}
		}
	}
	if n < commonSec*0.999 || n <= 0 {
		return stats{}, "common_support"
	}
	st := stats{commonSec: commonSec}
	st.meanHR = sumHR / n
	st.meanSpeed = sumSp / n
	variance := sumSp2/n - st.meanSpeed*st.meanSpeed
	if variance < 0 {
		variance = 0
	}
	if st.meanSpeed <= 0 {
		return stats{}, "speed_domain"
	}
	st.speedCV = math.Sqrt(variance) / st.meanSpeed
	st.gradeCover = gradeSec / commonSec
	den := sumT2 - sumT*sumT/n
	if den > 0 {
		st.hrTrend = (sumHT - sumT*sumHR/n) / den
	}
	// Efficiency drift: mean speed/HR in the two halves of the common support.
	mid := a + (b-a)/2
	var h1, s1, d1, h2, s2, d2 float64
	for _, seg := range common {
		lo, hi := seg.a, seg.b
		if lo < mid {
			x := hi
			if x > mid {
				x = mid
			}
			m := lo + (x-lo)/2
			if hv, ok := valueAt(w.Samples, true, m); ok {
				if sv, ok := valueAt(w.Samples, false, m); ok {
					dt := float64(x-lo) / 1e6
					h1 += hv * dt
					s1 += sv * dt
					d1 += dt
				}
			}
		}
		if hi > mid {
			x := lo
			if x < mid {
				x = mid
			}
			m := x + (hi-x)/2
			if hv, ok := valueAt(w.Samples, true, m); ok {
				if sv, ok := valueAt(w.Samples, false, m); ok {
					dt := float64(hi-x) / 1e6
					h2 += hv * dt
					s2 += sv * dt
					d2 += dt
				}
			}
		}
	}
	if d1 <= 0 || d2 <= 0 || h1 <= 0 || h2 <= 0 {
		return stats{}, "efficiency_drift"
	}
	ef1, ef2 := (s1/d1)/(h1/d1), (s2/d2)/(h2/d2)
	st.efDrift = math.Abs(ef2-ef1) / ((ef1 + ef2) / 2)
	return st, ""
}

func gradeAt(samples []Sample, t int64) (float64, bool) {
	for i := len(samples) - 1; i >= 0; i-- {
		s := samples[i]
		if t < s.Start || t >= s.End || s.Grade == nil || !finite(*s.Grade) {
			continue
		}
		return *s.Grade, true
	}
	return 0, false
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	y := append([]float64(nil), xs...)
	sort.Float64s(y)
	n := len(y)
	if n%2 == 1 {
		return y[n/2]
	}
	return (y[n/2-1] + y[n/2]) / 2
}

func pauseUnion(ps []Pause, a, b int64) []span {
	var out []span
	for _, p := range ps {
		lo, hi := p.Start, p.End
		if lo < a {
			lo = a
		}
		if hi > b {
			hi = b
		}
		if hi > lo {
			out = append(out, span{lo, hi})
		}
	}
	return out
}

func pauseBlocked(ps []Pause, a, b int64) bool {
	for _, p := range ps {
		if p.End <= p.Start {
			continue
		}
		if p.Start < b && p.End > a {
			return true
		}
		if p.End >= a-pauseAfterUs && p.End <= b {
			return true
		}
	}
	return false
}

// EvaluateWorkout evaluates one workout and returns at most one independent
// evidence row (median of qualified five-minute windows).
func EvaluateWorkout(w Workout, ref Reference) (WorkoutEvidence, error) {
	ev := WorkoutEvidence{Schema: Schema, Model: Model, ModelVersion: Version, WorkoutID: w.ID, DatasetID: w.DatasetID,
		Generation: w.Generation, Day: w.Day, Start: w.Start, End: w.End, SourceEpoch: ref.SourceEpoch,
		Context: w.ID, ReferenceHash: ref.Hash, ReferenceID: ref.ReferenceID, MaxHR: ref.MaxHR, RestHR: ref.RestHR,
		KnownAt: w.KnownAt, Reasons: []string{}}
	if len(w.Samples) > MaxSamples {
		return ev, ErrLimit
	}
	if !ref.Validate() {
		ev.Reasons = append(ev.Reasons, "hr_reference_missing")
		return ev, ErrInvalid
	}
	if w.ActivityType != 37 {
		ev.Reasons = append(ev.Reasons, "unsupported_activity")
		return ev, nil
	}
	if !w.Outdoor {
		ev.Reasons = append(ev.Reasons, "indoor")
		return ev, nil
	}
	if w.End <= w.Start || w.End-w.Start < minWorkoutUs {
		ev.Reasons = append(ev.Reasons, "duration_too_short")
		return ev, nil
	}
	if len(w.Samples) == 0 {
		ev.Reasons = append(ev.Reasons, "no_samples")
		return ev, nil
	}
	paused := unionLen(pauseUnion(w.Pauses, w.Start, w.End))
	pauseFrac := float64(paused) / float64(w.End-w.Start)
	ev.Support.PauseFraction = pauseFrac
	if pauseFrac > maxPauseFrac {
		ev.Reasons = append(ev.Reasons, "pause_contaminated")
		return ev, nil
	}
	envKnown := w.TemperatureC != nil && w.HumidityPct != nil
	ev.EnvironmentKnown = envKnown
	ev.Support.EnvironmentKnown = envKnown
	if w.TemperatureC != nil && (*w.TemperatureC < 5 || *w.TemperatureC > 25) {
		ev.Reasons = append(ev.Reasons, "environment_out_of_range")
		return ev, nil
	}
	if w.HumidityPct != nil && *w.HumidityPct > 85 {
		ev.Reasons = append(ev.Reasons, "environment_out_of_range")
		return ev, nil
	}
	if w.DistanceM == nil || w.RecordedDistanceM == nil || *w.DistanceM <= 0 || *w.RecordedDistanceM <= 0 {
		ev.Reasons = append(ev.Reasons, "distance_unverified")
		return ev, nil
	}
	de := math.Abs(*w.DistanceM-*w.RecordedDistanceM) / math.Max(*w.DistanceM, *w.RecordedDistanceM)
	ev.Support.DistanceError = de
	if de > maxDistanceErr {
		ev.Reasons = append(ev.Reasons, "distance_mismatch")
		return ev, nil
	}
	var vs, hrs, speeds []float64
	var bestHRCov, bestSpeedCov, bestGrade float64
	bestHRCov, bestSpeedCov = 1, 1
	bestGrade = 1
	reasons := map[string]int{}
	for ws := w.Start + warmupUs; ws+windowUs <= w.End; ws += windowUs {
		if pauseBlocked(w.Pauses, ws, ws+windowUs) {
			reasons["window_pause"]++
			continue
		}
		st, reason := windowStats(w, ws, ws+windowUs)
		if reason != "" {
			reasons[reason]++
			continue
		}
		if st.speedCV > maxSpeedCV {
			reasons["speed_variability"]++
			continue
		}
		if math.Abs(st.hrTrend) > maxHRTrend {
			reasons["hr_trend"]++
			continue
		}
		if st.efDrift > maxEFDrift {
			reasons["efficiency_drift"]++
			continue
		}
		if st.gradeCover < minGradeCover {
			reasons["grade"]++
			continue
		}
		vmin := st.meanSpeed * 60
		r := (st.meanHR - ref.RestHR) / (ref.MaxHR - ref.RestHR)
		if r < 0.55 || r > 0.85 {
			reasons["hr_ratio_domain"]++
			continue
		}
		if vmin < 134 || vmin > 300 {
			reasons["speed_domain"]++
			continue
		}
		v := 3.5 + 0.2*vmin/r
		if !finite(v) || v < 15 || v > 90 {
			reasons["outside_domain"]++
			continue
		}
		vs = append(vs, v)
		hrs = append(hrs, st.meanHR)
		speeds = append(speeds, st.meanSpeed)
		if st.commonSec/300 < bestHRCov {
			bestHRCov = st.commonSec / 300
		}
		_ = bestSpeedCov
		if st.gradeCover < bestGrade {
			bestGrade = st.gradeCover
		}
	}
	ev.Windows = len(vs)
	ev.Support.HRCoverage = bestHRCov
	ev.Support.SpeedCoverage = bestHRCov
	ev.Support.CommonSeconds = float64(len(vs)) * 300
	ev.Support.GradeCoverage = bestGrade
	if len(vs) < 3 {
		ev.Reasons = append(ev.Reasons, "insufficient_qualified_windows")
		for k, n := range reasons {
			ev.Reasons = append(ev.Reasons, k)
			_ = n
		}
		sort.Strings(ev.Reasons)
		return ev, nil
	}
	v := median(vs)
	hr := median(hrs)
	sp := median(speeds)
	ev.Qualified = true
	ev.CandidateV = ptr(v)
	ev.MedianHR = ptr(hr)
	ev.MedianSpeedMPS = ptr(sp)
	ev.Quality = 1
	if !envKnown && ev.Quality > 0.7 {
		ev.Quality = 0.7
	}
	ev.ContentHash = Hash(ev)
	return ev, nil
}

type weighted struct {
	ev Evidence
	w  float64
}

func dayKey(e Evidence) string {
	if e.Day != "" {
		return e.Day
	}
	return timeDay(e.At)
}

func timeDay(us int64) string {
	// Pure UTC civil date; loaders supply the athlete-local Day.
	days := us / DayUs
	t := days * 86400
	y, m, d := civilFromDays(days)
	_ = t
	return fmtDate(y, m, d)
}

func civilFromDays(z int64) (int, int, int) {
	z += 719468
	era := z / 146097
	if z < 0 {
		era = (z - 146096) / 146097
	}
	doe := z - era*146097
	yoe := (doe - doe/1460 + doe/36524 - doe/146096) / 365
	y := yoe + era*400
	doy := doe - (365*yoe + yoe/4 - yoe/100)
	mp := (5*doy + 2) / 153
	d := doy - (153*mp+2)/5 + 1
	m := mp + 3
	if mp >= 10 {
		m = mp - 9
		y++
	}
	return int(y), int(m), int(d)
}

func fmtDate(y, m, d int) string {
	// fixed-width ASCII
	b := make([]byte, 0, 10)
	b = appendInt(b, y, 4)
	b = append(b, '-')
	b = appendInt(b, m, 2)
	b = append(b, '-')
	b = appendInt(b, d, 2)
	return string(b)
}

func appendInt(b []byte, v, width int) []byte {
	if v == 0 {
		for i := 0; i < width-1; i++ {
			b = append(b, '0')
		}
		return append(b, '0')
	}
	var tmp [20]byte
	i := len(tmp)
	for v > 0 {
		i--
		tmp[i] = byte('0' + v%10)
		v /= 10
	}
	for len(tmp)-i < width {
		b = append(b, '0')
		width--
	}
	return append(b, tmp[i:]...)
}

func capSameDay(evs []Evidence, w []float64) {
	sum := map[string]float64{}
	for i, e := range evs {
		sum[dayKey(e)] += w[i]
	}
	for i := range w {
		if s := sum[dayKey(evs[i])]; s > 1 {
			w[i] /= s
		}
	}
}

// waterFill caps every weight at 1/3 of the total and redistributes excess.
func waterFill(w []float64) {
	const capFrac = 1.0 / 3.0
	total := 0.0
	for _, x := range w {
		total += x
	}
	if total <= 0 {
		return
	}
	fixed := make([]bool, len(w))
	for {
		capv := total * capFrac
		var excess float64
		var free float64
		for i, x := range w {
			if !fixed[i] && x > capv+1e-12 {
				excess += x - capv
				w[i] = capv
				fixed[i] = true
			} else if !fixed[i] {
				free += x
			}
		}
		if excess <= 1e-12 || free <= 0 {
			break
		}
		for i := range w {
			if !fixed[i] {
				w[i] += excess * (w[i] / free)
			}
		}
	}
	sum := 0.0
	for _, x := range w {
		sum += x
	}
	if sum > 0 {
		for i := range w {
			w[i] /= sum
		}
	}
}

func weightedLogMedian(evs []Evidence, w []float64) float64 {
	order := make([]int, len(evs))
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(i, j int) bool {
		a, b := evs[order[i]], evs[order[j]]
		if a.V == b.V {
			return a.WorkoutID < b.WorkoutID
		}
		return a.V < b.V
	})
	total := 0.0
	for _, v := range w {
		total += v
	}
	cumulative := 0.0
	for _, i := range order {
		cumulative += w[i]
		if cumulative >= total/2 {
			return math.Log(evs[i].V)
		}
	}
	return math.Log(evs[order[len(order)-1]].V)
}

func fitWithWeights(evs []Evidence, w []float64) (target, qAgg, nEff float64, weights []float64, reasons []string) {
	n := len(evs)
	y := make([]float64, n)
	for i, e := range evs {
		y[i] = math.Log(e.V)
	}
	m := weightedLogMedian(evs, w)
	dev := make([]float64, n)
	for i := range y {
		dev[i] = math.Abs(y[i] - m)
	}
	type pair struct {
		d float64
		i int
	}
	ps := make([]pair, n)
	for i := range dev {
		ps[i] = pair{dev[i], i}
	}
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].d == ps[j].d {
			return evs[ps[i].i].WorkoutID < evs[ps[j].i].WorkoutID
		}
		return ps[i].d < ps[j].d
	})
	cum, mad := 0.0, ps[n-1].d
	for _, p := range ps {
		cum += w[p.i]
		if cum >= 0.5 {
			mad = p.d
			break
		}
	}
	scale := math.Max(1.4826*mad, 0.04)
	sum, sumw, qsum, sumw2 := 0.0, 0.0, 0.0, 0.0
	for i := range y {
		yy := y[i]
		if yy < m-2*scale {
			yy = m - 2*scale
		}
		if yy > m+2*scale {
			yy = m + 2*scale
		}
		sum += w[i] * yy
		sumw += w[i]
		qsum += w[i] * evs[i].Q
		sumw2 += w[i] * w[i]
	}
	if sumw <= 0 {
		return 0, 0, 0, nil, []string{"no_weight"}
	}
	return math.Exp(sum / sumw), qsum / sumw, sumw * sumw / sumw2, w, nil
}

func fitAt(evs []Evidence, asOf int64) (target, qAgg, nEff float64, weights []float64, reasons []string) {
	w := make([]float64, len(evs))
	for i, e := range evs {
		age := float64(asOf-e.At) / float64(DayUs)
		if age < 0 {
			age = 0
		}
		w[i] = e.Q * math.Exp(-age/28)
	}
	capSameDay(evs, w)
	waterFill(w)
	return fitWithWeights(evs, w)
}

// eligibleAt freezes both event and known-at times before isolating outliers.
func eligibleAt(evs []Evidence, cutoff int64) ([]Evidence, bool) {
	visible := make([]Evidence, 0, len(evs))
	for _, e := range evs {
		if e.At >= cutoff-42*DayUs && e.At <= cutoff && e.KnownAt <= cutoff {
			visible = append(visible, e)
		}
	}
	if len(visible) < 3 {
		return visible, false
	}
	order := append([]Evidence(nil), visible...)
	sort.Slice(order, func(i, j int) bool {
		if order[i].V == order[j].V {
			return order[i].WorkoutID < order[j].WorkoutID
		}
		return order[i].V < order[j].V
	})
	byID := make(map[string]int, len(order))
	for i, e := range order {
		byID[e.WorkoutID] = i
	}
	accepted := make([]Evidence, 0, len(visible))
	n := len(order) - 1
	for _, e := range visible {
		i := byID[e.WorkoutID]
		index := func(j int) int {
			if j >= i {
				return j + 1
			}
			return j
		}
		center := (order[index((n-1)/2)].V + order[index(n/2)].V) * .5
		if math.Abs(e.V/center-1) <= .20 {
			accepted = append(accepted, e)
			continue
		}
		for _, o := range visible {
			if o.WorkoutID != e.WorkoutID && abs64(o.At-e.At) <= 14*DayUs && math.Abs(o.V/e.V-1) <= .10 {
				accepted = append(accepted, e)
				break
			}
		}
	}
	return accepted, len(accepted) != len(visible)
}

func supportedTarget(pool []Evidence, at int64) bool {
	if len(pool) < 3 {
		return false
	}
	first, last := pool[0].At, pool[0].At
	for _, e := range pool {
		first = min(first, e.At)
		last = max(last, e.At)
	}
	return last-first >= 7*DayUs && at-last <= 14*DayUs
}

// Freshen ages the frozen estimate without changing its center or support.
func Freshen(anchor Result, at int64) Result {
	r := anchor
	if r.EvidenceAt == nil || at < r.AsOf {
		return r
	}
	age := at - *r.EvidenceAt
	r.RecentDays = int(age / DayUs)
	switch {
	case age > 42*DayUs:
		r.Status, r.Freshness, r.Value, r.Confidence = "expired", "expired", nil, 0
	case age > 28*DayUs:
		r.Status, r.Freshness, r.Value, r.Confidence = "stale", "stale", nil, 0
	case age > 14*DayUs:
		r.Freshness = "aging"
	}
	r.Confidence = min(r.Confidence, r.Confidence*math.Exp(-float64(at-r.AsOf)/float64(28*DayUs)))
	return r
}

func Aggregate(in AggregateInput) (Result, error) {
	r := Result{Schema: Schema, Model: Model, ModelVersion: Version, DatasetID: in.DatasetID, Generation: in.Generation,
		AsOf: in.AsOf, ComputedAt: in.ComputedAt, ReferenceHash: in.ReferenceHash, SourceEpoch: in.SourceEpoch,
		Status: "collecting", Quality: "insufficient", Freshness: "current", Experimental: true, Unit: Unit,
		ConfidenceMeaning: "evidence_support_not_probability", PhysiologicallyValidated: false,
		Sensitivity: Sensitivity{Corners: 0}, LOO: LOO{}, Reasons: []string{}}
	if r.ComputedAt == 0 {
		r.ComputedAt = in.AsOf
	}
	if in.AsOf <= 0 {
		return r, ErrInvalid
	}
	if len(in.Evidence) > MaxWorkouts {
		return r, ErrLimit
	}
	loc := time.UTC
	if in.Timezone != "" {
		var err error
		loc, err = time.LoadLocation(in.Timezone)
		if err != nil {
			return r, ErrInvalid
		}
	}
	byID := make(map[string]Evidence, len(in.Evidence))
	for _, e := range in.Evidence {
		if e.WorkoutID == "" || e.At <= 0 || !finite(e.V) || e.V <= 0 || !finite(e.Q) || e.Q <= 0 || e.Q > 1 || e.KnownAt > in.AsOf || e.At > in.AsOf {
			continue
		}
		if in.ReferenceHash == "" || e.ReferenceHash != in.ReferenceHash || in.SourceEpoch == "" || e.SourceEpoch != in.SourceEpoch {
			continue
		}
		if previous, exists := byID[e.WorkoutID]; exists && previous != e {
			return r, ErrInvalid
		}
		byID[e.WorkoutID] = e
	}
	pool := make([]Evidence, 0, len(byID))
	for _, e := range byID {
		pool = append(pool, e)
	}
	sort.Slice(pool, func(i, j int) bool {
		if pool[i].At == pool[j].At {
			return pool[i].WorkoutID < pool[j].WorkoutID
		}
		return pool[i].At < pool[j].At
	})
	if len(pool) == 0 {
		r.Reasons = append(r.Reasons, "no_evidence")
		return r, nil
	}

	latest := pool[len(pool)-1]
	r.EvidenceAt = &latest.At
	r.RecentDays = int((in.AsOf - latest.At) / DayUs)
	if in.AsOf-latest.At > 28*DayUs {
		return Freshen(r, in.AsOf), nil
	}
	anchor := time.UnixMicro(latest.At).In(loc)
	anchor = time.Date(anchor.Year(), anchor.Month(), anchor.Day(), 0, 0, 0, 0, loc)
	end := min(in.AsOf, anchor.AddDate(0, 0, 1).UnixMicro()-1)
	all := pool
	pool, excluded := eligibleAt(all, end)
	if excluded {
		r.Reasons = append(r.Reasons, "outlier_unconfirmed")
	}
	r.Workouts = len(pool)
	if !supportedTarget(pool, end) {
		r.Reasons = append(r.Reasons, "insufficient_independent_workouts")
		return r, nil
	}
	latest = pool[len(pool)-1]
	r.EvidenceAt = &latest.At
	r.SpanDays = int((latest.At - pool[0].At) / DayUs)
	r.RecentDays = int((in.AsOf - latest.At) / DayUs)
	anchor = time.UnixMicro(latest.At).In(loc)
	anchor = time.Date(anchor.Year(), anchor.Month(), anchor.Day(), 0, 0, 0, 0, loc)
	_, qAgg, nEff, _, _ := fitAt(pool, end)
	var logs []float64
	var sw float64
	validDays := 0
	for k := 6; k >= 0; k-- {
		cutoff := min(in.AsOf, anchor.AddDate(0, 0, 1-k).UnixMicro()-1)
		daily, _ := eligibleAt(all, cutoff)
		if !supportedTarget(daily, cutoff) {
			continue
		}
		t, _, _, _, _ := fitAt(daily, cutoff)
		if t <= 0 || !finite(t) {
			continue
		}
		wt := math.Exp(-float64(k) / 7)
		logs = append(logs, math.Log(t)*wt)
		sw += wt
		validDays++
	}
	if len(pool) == 3 {
		r.LOO.Reasons = []string{"loo_support_insufficient"}
	}
	if validDays < 3 || sw <= 0 {
		r.Reasons = append(r.Reasons, "insufficient_target_days")
		r.Workouts = len(pool)
		r.Status = "collecting"
		return r, nil
	}
	sum := 0.0
	for _, x := range logs {
		sum += x
	}
	targetLog := sum / sw
	target := math.Exp(targetLog)
	r.TargetLog = ptr(targetLog)
	r.Value = ptr(target)
	r.QAggregate = qAgg
	r.NEff = nEff
	switch {
	case len(pool) >= 5 && r.SpanDays >= 14:
		r.Status = "established"
	case len(pool) >= 3:
		r.Status = "preliminary"
	default:
		r.Status = "collecting"
	}
	// Confidence
	conf := math.Min(0.6, qAgg*math.Min(1, nEff/10)*math.Exp(-float64(in.AsOf-latest.At)/float64(28*DayUs)))
	for _, e := range pool {
		if !e.EnvironmentKnown {
			conf = math.Min(conf, 0.5)
			r.Reasons = append(r.Reasons, "environment_unknown")
			break
		}
	}
	if r.Status == "preliminary" {
		conf = math.Min(conf, 0.4)
	}
	r.Confidence = conf
	if r.Status == "established" {
		r.Quality = "available"
	} else {
		r.Quality = "partial"
	}
	// Sensitivity: 8 corners, every corner must be defined.
	var corners []float64
	unbounded := false
	for _, rest := range []float64{latest.RestHR - 5, latest.RestHR + 5} {
		for _, max := range []float64{latest.MaxHR - 10, latest.MaxHR + 10} {
			for _, coef := range []float64{0.18, 0.22} {
				if max <= rest || rest < 30 || max >= 220 {
					unbounded = true
					continue
				}
				adj := make([]Evidence, len(pool))
				ok := true
				for i, e := range pool {
					rr := (e.HRMean - rest) / (max - rest)
					if rr <= 0 || !finite(rr) {
						ok = false
						break
					}
					v := 3.5 + coef*(e.SpeedMPS*60)/rr
					if !finite(v) || v < 15 || v > 90 {
						ok = false
						break
					}
					e.V = v
					adj[i] = e
				}
				if !ok {
					unbounded = true
					continue
				}
				t, _, _, _, _ := fitAt(adj, end)
				if t <= 0 || !finite(t) {
					unbounded = true
					continue
				}
				corners = append(corners, t)
			}
		}
	}
	r.Sensitivity.Corners = len(corners)
	if unbounded || len(corners) != 8 {
		r.Sensitivity.Unbounded = true
		r.Sensitivity.Reasons = append(r.Sensitivity.Reasons, "sensitivity_unbounded")
		r.CandidateOnly = true
	} else {
		lo, hi := corners[0], corners[0]
		for _, v := range corners[1:] {
			if v < lo {
				lo = v
			}
			if v > hi {
				hi = v
			}
		}
		r.Sensitivity.Range = &Range{lo, hi}
		width := (hi - lo) / target
		r.Sensitivity.Width = &width
		if width > 0.30 {
			r.Sensitivity.Reasons = append(r.Sensitivity.Reasons, "sensitivity_width_exceeded")
			r.CandidateOnly = true
		}
	}
	// LOO refit.
	if len(pool) <= 3 {
		r.LOO.SupportOK = false
		r.Confidence = math.Min(r.Confidence, 0.4)
		r.CandidateOnly = true
	} else {
		var vals []float64
		for i := range pool {
			sub := make([]Evidence, 0, len(pool)-1)
			for j, e := range pool {
				if i != j {
					sub = append(sub, e)
				}
			}
			if len(sub) < 3 {
				r.LOO.SupportOK = false
				break
			}
			t, _, _, _, _ := fitAt(sub, end)
			if t <= 0 || !finite(t) {
				r.LOO.SupportOK = false
				break
			}
			vals = append(vals, t)
		}
		r.LOO.Folds = len(vals)
		if len(vals) == len(pool) && len(vals) > 0 {
			lo, hi := vals[0], vals[0]
			for _, v := range vals[1:] {
				if v < lo {
					lo = v
				}
				if v > hi {
					hi = v
				}
			}
			r.LOO.Range = &Range{lo, hi}
			r.LOO.SupportOK = true
			if (hi-lo)/target > 0.10 {
				r.Stability = "sensitive"
			} else {
				r.Stability = "stable"
			}
		} else {
			r.LOO.Reasons = append(r.LOO.Reasons, "loo_support_insufficient")
			r.Confidence = math.Min(r.Confidence, 0.4)
			r.CandidateOnly = true
		}
	}
	if r.Sensitivity.Unbounded || r.Sensitivity.Width != nil && *r.Sensitivity.Width > .30 {
		r.Value = nil
	}
	if r.CandidateOnly && r.Quality == "available" {
		r.Quality = "partial"
	}
	// Aggregate support summary.
	r.Support.EnvironmentKnown = true
	for _, e := range pool {
		if !e.EnvironmentKnown {
			r.Support.EnvironmentKnown = false
		}
	}
	r.Evidence = append([]Evidence(nil), pool...)
	return Freshen(r, in.AsOf), nil
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
