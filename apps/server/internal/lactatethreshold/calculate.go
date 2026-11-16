package lactatethreshold

import (
	"math"
	"slices"
	"sort"
	"time"
)

// Calculate evaluates one day. It is deterministic: the same input (in any
// Workout order) gives the same result, and it never reads a clock or a
// previous publication.
func Calculate(in Input) Result {
	p := DefaultPolicy()
	in = canonical(in)
	out := Result{Schema: Schema, Algorithm: Algorithm, Version: Version, ConfigHash: ConfigHash(), DatasetID: in.DatasetID, Generation: in.Generation,
		Date: in.Date, AsOf: in.AsOf, View: in.View, Definition: DefinitionWorking, UpdateStatus: "none",
		HeartRate:   HeartRate{Basis: "collecting", Freshness: "expired", Reasons: []string{}},
		TrainingUse: TrainingUse{Reasons: []string{"hr_training_adapter_not_activated"}},
		Evidence:    []DayEvidence{}, Diagnostics: []WorkoutDiagnostic{}, Reasons: []string{"working_threshold_not_lab_measurement"}}
	out.InputHash = Hash(in)
	a := in.Anchor
	out.AnchorHash = in.FitnessInputHash
	// Overall status follows the pace anchor only (section 12.2).
	switch {
	case a != nil && a.Basis == "conflict":
		out.Status = "conflict"
	case a == nil || !(a.SpeedMPS > 0) || !finite(a.SpeedMPS):
		out.Status = "collecting"
		a = nil
		out.Reasons = add(out.Reasons, "pace_anchor_unavailable")
	case a.Basis == "prior_based":
		out.Status = "prior_based_proxy"
	case a.Basis == "field_calibrated":
		out.Status = "field_calibrated_proxy"
	default:
		out.Status = "estimated_proxy"
	}
	if a != nil && a.Basis != "conflict" {
		out.Pace = pace(p, *a)
		out.TrainingUse.PaceEligible = (a.Basis == "estimated" || a.Basis == "field_calibrated") && (a.Freshness == "fresh" || a.Freshness == "aging")
		if !out.TrainingUse.PaceEligible {
			out.TrainingUse.Reasons = add(out.TrainingUse.Reasons, "pace_not_eligible")
		}
	}

	var estimates []workoutEstimate
	reviewHRMax := false
	for _, w := range in.Workouts {
		age, ok := ageDays(in.Date, w.Day)
		if !ok || age < 0 || age >= p.WindowDays {
			continue
		}
		e, diag, review := evaluateWorkout(p, w, a, age)
		reviewHRMax = reviewHRMax || review
		if len(out.Diagnostics) < p.MaxDiagnostics {
			out.Diagnostics = append(out.Diagnostics, diag)
		}
		if e != nil {
			estimates = append(estimates, *e)
		}
	}
	if reviewHRMax {
		out.Reasons = add(out.Reasons, "hrmax_review_suggested")
	}
	heartRate(p, &out, estimates, in, a)
	if out.Status == "conflict" {
		// A pace conflict publishes no new automatic target.
		out.TrainingUse.PaceEligible = false
		out.TrainingUse.Reasons = add(out.TrainingUse.Reasons, "pace_conflict")
	}
	sort.Strings(out.Reasons)
	sort.Strings(out.TrainingUse.Reasons)
	sort.Strings(out.HeartRate.Reasons)
	return out
}

func canonical(in Input) Input {
	ws := slices.Clone(in.Workouts)
	sort.SliceStable(ws, func(i, j int) bool {
		if ws[i].Start != ws[j].Start {
			return ws[i].Start < ws[j].Start
		}
		return ws[i].ID < ws[j].ID
	})
	for i := range ws {
		win := slices.Clone(ws[i].Windows)
		sort.SliceStable(win, func(x, y int) bool { return win[x].Start < win[y].Start })
		ws[i].Windows = win
	}
	in.Workouts = ws
	if in.View == "" {
		in.View = "revised"
	}
	return in
}

func pace(p Policy, a Anchor) *Pace {
	out := &Pace{SpeedMPS: a.SpeedMPS, PaceSecondsPerKm: 1000 / a.SpeedMPS, Basis: a.Basis, Freshness: a.Freshness, EvidenceDay: a.EvidenceDay,
		DefinitionSensitivity: [2]int{2400, 3600}}
	if a.SpeedLowMPS > 0 && a.SpeedHighMPS >= a.SpeedLowMPS {
		out.SpeedModelRange = &Range{Low: a.SpeedLowMPS, High: a.SpeedHighMPS}
	}
	low := a.SpeedMPS * (1 - p.TrainingMinWidth)
	if a.Speed60MPS > 0 && a.Speed60MPS <= a.SpeedMPS {
		out.Speed60MPS = ptr(a.Speed60MPS)
		low = math.Min(a.Speed60MPS, low)
	}
	out.TrainingSpeedRange = Range{Low: low, High: a.SpeedMPS}
	return out
}

// workoutEstimate is one Workout's heart-rate estimate at the working point.
type workoutEstimate struct {
	id, day, method string
	age             int
	bpm, support    float64 // support: u_i in [0,1]
	seconds         float64
	correction      float64 // largest slope-correction uncertainty (bpm)
	flatShare       float64
	envKnown        bool
	conflict        bool
	notSettled      bool
	anchorDependent bool
	slope           float64
}

type qualified struct {
	w        Window
	speed    float64
	x        float64
	q        float64
	flat     bool
	moderate bool
}

func evaluateWorkout(p Policy, w Workout, a *Anchor, age int) (*workoutEstimate, WorkoutDiagnostic, bool) {
	d := WorkoutDiagnostic{WorkoutID: w.ID, Day: w.Day, Status: "excluded", Reasons: []string{}}
	cond, envKnown, extreme := condition(w)
	review := false
	if w.Reference != nil {
		for _, x := range w.Windows {
			if x.Coverage >= p.MinCoverage && x.HR > w.Reference.MaxHR {
				review = true
			}
		}
	}
	if w.TimeTrial != nil {
		e, reasons := methodC(p, w, cond, envKnown, extreme, age)
		d.Reasons = append(d.Reasons, reasons...)
		if e != nil {
			d.Status, d.Method, d.BPM, d.SupportSeconds = "used", e.method, ptr(e.bpm), e.seconds
			sort.Strings(d.Reasons)
			return e, d, review
		}
		// A failed time trial still offers its steady windows to A/B below.
	}
	if a == nil {
		d.Reasons = add(d.Reasons, "pace_anchor_unavailable")
		return nil, d, review
	}
	if !prefilter(p, w, *a) {
		d.Reasons = add(d.Reasons, "threshold_candidate_absent")
		return nil, d, review
	}
	if extreme {
		d.Reasons = add(d.Reasons, "condition_extreme")
		return nil, d, review
	}
	if w.Indoor != nil && *w.Indoor {
		d.Reasons = add(d.Reasons, "indoor_speed_uncalibrated")
		return nil, d, review
	}
	if w.DistanceCheck == "mismatch" {
		d.Reasons = add(d.Reasons, "distance_mismatch")
		return nil, d, review
	}
	qs, reasons := qualify(p, w, *a, cond)
	d.Reasons = add(d.Reasons, reasons...)
	e := localEstimate(p, w, *a, qs, &d)
	if e == nil {
		sort.Strings(d.Reasons)
		return nil, d, review
	}
	e.age, e.envKnown = age, envKnown
	d.BPM, d.Method, d.SupportSeconds = ptr(e.bpm), e.method, e.seconds
	if e.slope != 0 {
		d.Slope = ptr(e.slope)
	}
	if a.Basis == "prior_based" {
		// Section 9.4.1: near a prior-only anchor the observed HR collapses to
		// the linear HRR prior; it is diagnostic, never the threshold HR.
		d.Status = "diagnostic"
		d.Reasons = add(d.Reasons, "anchor_prior_based_hr_circular")
		sort.Strings(d.Reasons)
		return nil, d, review
	}
	d.Status = "used"
	sort.Strings(d.Reasons)
	return e, d, review
}

// condition implements section 7.3.
func condition(w Workout) (q float64, known, extreme bool) {
	t, h := w.TemperatureC, w.Humidity
	if t != nil && (*t < 0 || *t > 30) {
		return 0, true, true
	}
	if t == nil || h == nil {
		return .8, false, false
	}
	q = 1
	if *t < 5 || *t > 25 {
		q = .7
	}
	if *h > 85 {
		q *= .8
	}
	return q, true, false
}

func validEpoch(e string) bool { return e != "" && e != "ambiguous" }

// prefilter: a continuous stretch of at least 15 minutes whose median speed is
// at least 0.90 vT, or whose median HR is at least 75% HRR (section 7.2 step 7).
func prefilter(p Policy, w Workout, a Anchor) bool {
	var group []Window
	check := func() bool {
		secs := 0.0
		var speeds, hrs []float64
		for _, x := range group {
			secs += x.Seconds
			speeds = append(speeds, x.Speed)
			hrs = append(hrs, x.HR)
		}
		if secs < p.PrefilterSeconds {
			return false
		}
		if median(speeds) >= p.PrefilterSpeed*a.SpeedMPS {
			return true
		}
		r := w.Reference
		return r != nil && r.MaxHR > r.RestHR && median(hrs) >= r.RestHR+p.PrefilterHRR*(r.MaxHR-r.RestHR)
	}
	for i, x := range w.Windows {
		if x.Kind != "steady" {
			if check() {
				return true
			}
			group = nil
			continue
		}
		if len(group) > 0 && w.Windows[i-1].End != x.Start {
			if check() {
				return true
			}
			group = nil
		}
		group = append(group, x)
	}
	return check()
}

func qualify(p Policy, w Workout, a Anchor, cond float64) ([]qualified, []string) {
	var out []qualified
	reasons := []string{}
	distQ := 1.0
	switch w.DistanceCheck {
	case "consistent":
	default:
		distQ = p.UncheckedDistance
	}
	for i, x := range w.Windows {
		if x.Kind != "steady" || !validEpoch(x.HREpoch) || !validEpoch(x.SpeedEpoch) {
			continue
		}
		if x.RunningAt < p.WarmupSeconds {
			continue
		}
		if x.RunningAt+x.Seconds > p.LateSeconds {
			reasons = add(reasons, "late_run_drift_diagnostic")
			continue
		}
		if x.Coverage < p.MinCoverage || x.SpeedCV > p.MaxSpeedCV {
			continue
		}
		// Stage settling: the previous adjacent window belongs to the same
		// stage, so this window starts at least 3 minutes after any change
		// and the stage lasts at least 6 minutes.
		if i == 0 || w.Windows[i-1].End != x.Start || w.Windows[i-1].Kind != "steady" ||
			math.Abs(math.Log(x.Speed/w.Windows[i-1].Speed)) > math.Log(p.StageChange) {
			continue
		}
		gradeKnown := x.MaxAbsGrade != nil && x.GradeSupport >= p.MinGradeSupport
		if gradeKnown && *x.MaxAbsGrade > p.MaxGrade {
			reasons = add(reasons, "grade_too_steep")
			continue
		}
		gq := 1.0
		speed := x.Speed
		if gradeKnown && x.FlatSpeed != nil {
			speed = *x.FlatSpeed
		}
		if !gradeKnown {
			if w.DistanceCheck != "consistent" {
				reasons = add(reasons, "grade_unknown_unchecked")
				continue
			}
			gq = p.UnknownGrade
			reasons = add(reasons, "grade_unknown")
		}
		if !(speed > 0) || !finite(speed) || !finite(x.HR) || x.HR <= 0 {
			continue
		}
		maxTrend := p.MaxTrend
		if speed >= p.NearTrendFrom*a.SpeedMPS {
			maxTrend = p.MaxTrendNear
		}
		if math.Abs(x.HRTrend) > maxTrend {
			reasons = add(reasons, "hr_drift_high")
			continue
		}
		q := qualified{w: x, speed: speed, x: math.Log(speed / a.SpeedMPS), q: x.Coverage * cond * distQ * gq, flat: gradeKnown && w.DistanceCheck == "consistent"}
		if math.Abs(x.HRTrend) > p.MaxTrend {
			q.moderate = true
			reasons = add(reasons, "hr_drift_moderate")
		}
		out = append(out, q)
	}
	return out, reasons
}

func slopePrior(p Policy, ref *Reference) float64 {
	if ref != nil && ref.MaxHR > ref.RestHR {
		return p.SlopeHRR * (ref.MaxHR - ref.RestHR)
	}
	return p.SlopeDefault
}

// localEstimate runs methods A (near point, direct + corrected tier) and B
// (within-workout interpolation) for one Workout.
func localEstimate(p Policy, w Workout, a Anchor, qs []qualified, d *WorkoutDiagnostic) *workoutEstimate {
	b0 := slopePrior(p, w.Reference)
	bHat, hB, okB, secondsB := interpolation(p, qs, d)
	slope := b0
	if okB {
		slope = bHat
	}
	var ys, ws []float64
	seconds, qsum, flat, correction := 0.0, 0.0, 0.0, 0.0
	direct := true
	for _, q := range qs {
		ax := math.Abs(q.x)
		if ax > math.Log(p.CorrectedBand)+1e-12 {
			continue
		}
		tier, y := 1.0, q.w.HR
		if ax > math.Log(p.DirectBand)+1e-12 {
			tier = p.CorrectedWeight
			y = q.w.HR - slope*q.x
			correction = math.Max(correction, ax*(p.SlopeRange[1]-p.SlopeRange[0])/2*b0)
			direct = false
		}
		ys = append(ys, y)
		ws = append(ws, q.q*q.w.Seconds*tier)
		seconds += q.w.Seconds
		qsum += q.q * q.w.Seconds
		if q.flat {
			flat += q.w.Seconds
		}
	}
	if seconds >= p.MinNearSeconds {
		hA := weightedMedian(ys, ws)
		e := &workoutEstimate{id: w.ID, day: w.Day, method: "near_anchor_observed", bpm: hA, seconds: seconds, correction: correction,
			flatShare: flat / seconds, anchorDependent: true, slope: 0}
		if !direct {
			e.method = "near_anchor_corrected"
			e.slope = slope
		}
		e.support = (qsum / seconds) * math.Min(seconds/60/p.SupportMinutes, 1)
		if okB && math.Abs(hA-hB) > p.WithinConflict {
			e.conflict = true
			d.Reasons = add(d.Reasons, "within_workout_conflict")
		}
		return e
	}
	if len(ys) > 0 {
		d.Reasons = add(d.Reasons, "near_threshold_support_low")
	}
	if okB {
		qsumB, flatB := 0.0, 0.0
		for _, q := range qs {
			if q.speed >= p.InterpDomain[0]*a.SpeedMPS && q.speed <= p.InterpDomain[1]*a.SpeedMPS {
				qsumB += q.q * q.w.Seconds
				if q.flat {
					flatB += q.w.Seconds
				}
			}
		}
		e := &workoutEstimate{id: w.ID, day: w.Day, method: "local_interpolation", bpm: hB, seconds: secondsB, flatShare: flatB / secondsB,
			anchorDependent: true, slope: bHat}
		e.support = (qsumB / secondsB) * math.Min(secondsB/60/p.SupportMinutes, 1)
		return e
	}
	d.Reasons = add(d.Reasons, "near_threshold_support_missing")
	return nil
}

// interpolation is method B: a Huber line through at least two stable stages
// that bracket the anchor inside [0.90, 1.08] vT.
func interpolation(p Policy, qs []qualified, d *WorkoutDiagnostic) (slope, at float64, ok bool, seconds float64) {
	var in []qualified
	for _, q := range qs {
		r := math.Exp(q.x)
		if r >= p.InterpDomain[0] && r <= p.InterpDomain[1] {
			in = append(in, q)
		}
	}
	if len(in) < 2 {
		return 0, 0, false, 0
	}
	stages := 1
	below, above := 0.0, 0.0
	var sb, wb, sa, wa float64
	for i, q := range in {
		seconds += q.w.Seconds
		if i > 0 && (in[i-1].w.End != q.w.Start || math.Abs(q.x-in[i-1].x) > math.Log(p.StageChange)) {
			stages++
		}
		switch {
		case q.x < 0:
			below += q.w.Seconds
			sb += q.speed * q.w.Seconds
			wb += q.w.Seconds
		case q.x > 0:
			above += q.w.Seconds
			sa += q.speed * q.w.Seconds
			wa += q.w.Seconds
		}
	}
	if stages < 2 || seconds < p.MinInterpSeconds || below == 0 || above == 0 {
		return 0, 0, false, 0
	}
	vt := math.Exp(-in[0].x) * in[0].speed
	if sa/wa-sb/wb < p.MinInterpSpread*vt {
		return 0, 0, false, 0
	}
	xs, ys, ws := make([]float64, len(in)), make([]float64, len(in)), make([]float64, len(in))
	for i, q := range in {
		xs[i], ys[i], ws[i] = q.x, q.w.HR, q.q*q.w.Seconds
	}
	a, b, fitOK := huberLine(p, xs, ys, ws)
	if !fitOK || b < p.SlopeDomain[0] || b > p.SlopeDomain[1] {
		d.Reasons = add(d.Reasons, "local_fit_unidentified")
		return 0, 0, false, 0
	}
	return b, a, true, seconds
}

// methodC: mean HR over the last 20 minutes of a confirmed 30 minute time
// trial (definition field_tt30_last20). It does not depend on the anchor.
func methodC(p Policy, w Workout, cond float64, envKnown, extreme bool, age int) (*workoutEstimate, []string) {
	tt := *w.TimeTrial
	if float64(tt.End-tt.Start)/1e6 < p.TTMinSeconds {
		return nil, []string{"effort_segment_invalid"}
	}
	if extreme {
		return nil, []string{"condition_extreme"}
	}
	from := tt.End - int64(p.TTTailSeconds*1e6)
	var hs, ts, ws []float64
	sum, wsum, seconds := 0.0, 0.0, 0.0
	for _, x := range w.Windows {
		if x.Start < from || x.Start < tt.Start || x.End > tt.End || x.Coverage < p.MinCoverage || !validEpoch(x.HREpoch) || !finite(x.HR) || x.HR <= 0 {
			continue
		}
		wt := x.Seconds * x.Coverage
		sum += x.HR * wt
		wsum += wt
		seconds += x.Seconds
		hs = append(hs, x.HR)
		ts = append(ts, float64(x.Start+x.End)/2e6/60)
		ws = append(ws, wt)
	}
	if seconds < p.TTMinTailSupport {
		return nil, []string{"time_trial_hr_support_low"}
	}
	e := &workoutEstimate{id: w.ID, day: w.Day, method: "field_time_trial", age: age, bpm: sum / wsum, seconds: seconds, support: 1,
		flatShare: 1, envKnown: envKnown}
	reasons := []string{}
	if cond < 1 {
		e.support = cond
	}
	if len(hs) >= 2 && math.Abs(weightedSlope(ts, hs, ws)) > p.TTMaxTrend {
		e.notSettled = true
		reasons = append(reasons, "tt_hr_not_settled")
	}
	return e, reasons
}

type dayAgg struct {
	day         string
	age         int
	bpm, u, w   float64
	seconds     float64
	method      string
	ids         []string
	correction  float64
	flat        float64
	envKnown    bool
	conflict    bool
	notSettled  bool
	field       bool
	anchorBased bool
}

func heartRate(p Policy, out *Result, es []workoutEstimate, in Input, a *Anchor) {
	hr := &out.HeartRate
	if len(es) == 0 {
		switch {
		case a != nil && a.Basis == "prior_based":
			hr.Reasons = add(hr.Reasons, "anchor_prior_based_hr_circular")
		default:
			hr.Reasons = add(hr.Reasons, "near_threshold_support_missing")
		}
		return
	}
	byDay := map[string][]workoutEstimate{}
	for _, e := range es {
		byDay[e.day] = append(byDay[e.day], e)
	}
	var days []dayAgg
	for day, xs := range byDay {
		var fields, locals []workoutEstimate
		for _, e := range xs {
			if e.method == "field_time_trial" {
				fields = append(fields, e)
			} else {
				locals = append(locals, e)
			}
		}
		use := locals
		g := dayAgg{day: day, age: xs[0].age}
		if len(fields) > 0 {
			use, g.field = fields, true
		}
		var vs, us []float64
		su, flatW, envW := 0.0, 0.0, 0.0
		for _, e := range use {
			vs = append(vs, e.bpm)
			us = append(us, math.Max(e.support, 1e-9))
			su += e.support
			g.seconds += e.seconds
			g.ids = append(g.ids, e.id)
			g.correction = math.Max(g.correction, e.correction)
			g.conflict = g.conflict || e.conflict
			g.notSettled = g.notSettled || e.notSettled
			g.anchorBased = g.anchorBased || e.anchorDependent
			flatW += e.flatShare * e.support
			if e.envKnown {
				envW += e.support
			}
		}
		g.bpm = weightedMedian(vs, us)
		g.u = math.Min(1, su)
		if su > 0 {
			g.flat, g.envKnown = flatW/su, envW/su >= .5
		}
		g.method = use[0].method
		for _, e := range use[1:] {
			if e.method != g.method {
				g.method = "mixed"
			}
		}
		sort.Strings(g.ids)
		g.w = g.u * math.Exp(-float64(g.age)/p.TauDays) * math.Min(1, float64(p.WindowDays-g.age)/p.TaperDays)
		days = append(days, g)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].day < days[j].day })
	vs, ws := make([]float64, len(days)), make([]float64, len(days))
	for i, g := range days {
		vs[i], ws[i] = g.bpm, g.w
	}
	center, ok := huberLocation(p, vs, ws)
	if !ok {
		hr.Basis = "conflict"
		hr.Reasons = add(hr.Reasons, "hr_aggregation_unconverged")
		return
	}
	ref := latestReference(in.Workouts)
	if ref != nil && center <= ref.RestHR {
		hr.Reasons = add(hr.Reasons, "hr_below_resting")
		return
	}
	// Range: floor, day dispersion, slope correction and anchor edges.
	half := p.ErrorFloor
	if len(days) >= 2 {
		half = math.Max(half, 1.4826*weightedMAD(vs, ws))
	}
	anchorBased := false
	for _, g := range days {
		half = math.Max(half, g.correction)
		anchorBased = anchorBased || g.anchorBased
	}
	partial := false
	if anchorBased && a != nil && a.SpeedLowMPS > 0 && a.SpeedHighMPS > 0 {
		edge := math.Max(math.Abs(math.Log(a.SpeedLowMPS/a.SpeedMPS)), math.Abs(math.Log(a.SpeedHighMPS/a.SpeedMPS)))
		if edge > math.Log(p.CorrectedBand) {
			partial = true
			hr.Reasons = add(hr.Reasons, "anchor_range_unsupported")
		}
		half = math.Max(half, edge*p.SlopeRange[1]*slopePrior(p, ref))
	}
	// Leave-one-day-out on the HR aggregation only (plan R6).
	loo := 0.0
	if len(days) >= 2 {
		for i := range days {
			xs := slices.Delete(slices.Clone(vs), i, i+1)
			yw := slices.Delete(slices.Clone(ws), i, i+1)
			if c, ok := huberLocation(p, xs, yw); ok {
				loo = math.Max(loo, math.Abs(c-center))
			} else {
				partial = true
			}
		}
	}
	sumW, sumW2, seconds := 0.0, 0.0, 0.0
	latest := days[len(days)-1]
	flatW, envW := 0.0, 0.0
	conflict, notSettled := false, false
	for _, g := range days {
		sumW += g.w
		sumW2 += g.w * g.w
		seconds += g.seconds
		flatW += g.flat * g.w
		if g.envKnown {
			envW += g.w
		}
		conflict = conflict || g.conflict
		notSettled = notSettled || g.notSettled
		out.Evidence = append(out.Evidence, DayEvidence{Day: g.day, AgeDays: g.age, Method: g.method, BPM: round1(g.bpm), Weight: round4(g.w),
			Support: round4(g.u), SupportSeconds: g.seconds, WorkoutIDs: g.ids})
	}
	neff := 0.0
	if sumW2 > 0 {
		neff = sumW * sumW / sumW2
	}
	hr.BPM = ptr(round1(center))
	hr.ModelRange = &Range{Low: round1(center - half), High: round1(center + half)}
	hr.RangePartiallyIdentified = partial
	hr.IndependentDays, hr.EffectiveDays, hr.SupportSeconds = len(days), round4(neff), seconds
	hr.EvidenceDay, hr.LOOMaxDelta = latest.day, round1(loo)
	hr.Freshness = "aging"
	if latest.age <= p.FreshDays {
		hr.Freshness = "fresh"
	}
	hr.Method, hr.Definition = latest.method, DefinitionWorking
	if latest.field {
		hr.Definition = DefinitionFieldTT
	}
	if ref != nil && ref.MaxHR > ref.RestHR {
		hr.ObservedHRR = ptr(round4((center - ref.RestHR) / (ref.MaxHR - ref.RestHR)))
	}

	// Method C against A/B (section 9.4.2).
	var fieldV, localV, fieldW, localW []float64
	for _, g := range days {
		if g.field {
			fieldV, fieldW = append(fieldV, g.bpm), append(fieldW, g.w)
		} else {
			localV, localW = append(localV, g.bpm), append(localW, g.w)
		}
	}
	disagree := false
	pathTwo := false
	if len(fieldV) > 0 && len(localV) > 0 {
		f, l := weightedMedian(fieldV, fieldW), weightedMedian(localV, localW)
		if math.Abs(f-l) > p.DefinitionDiff {
			disagree = true
			hr.Reasons = add(hr.Reasons, "definition_disagreement")
		} else {
			pathTwo = true
		}
	}
	span := 0
	if first, ok := ageDays(latest.day, days[0].day); ok {
		span = first
	}
	pathOne := len(days) >= p.SupportedDays && span >= p.SupportedSpan && seconds >= p.SupportedSeconds && neff >= p.SupportedNeff
	width := 2 * half
	switch {
	case len(days) >= 2 && width > p.ConflictWidth:
		hr.Basis = "conflict"
		hr.Reasons = add(hr.Reasons, "hr_days_disagree")
	case (pathOne || pathTwo) && !disagree && latest.age < p.SupportedRecent && width <= p.SupportedWidth && loo <= p.SupportedLOO && !partial && !conflict && !notSettled &&
		flatW >= p.KnownFlatShare*sumW && envW >= p.KnownEnvShare*sumW:
		hr.Basis = "supported"
	default:
		hr.Basis = "preliminary"
		switch {
		case !(pathOne || pathTwo):
			hr.Reasons = add(hr.Reasons, "independent_days_insufficient")
		case flatW < p.KnownFlatShare*sumW:
			hr.Reasons = add(hr.Reasons, "known_flat_share_low")
		case envW < p.KnownEnvShare*sumW:
			hr.Reasons = add(hr.Reasons, "conditions_not_comparable")
		}
		if conflict {
			hr.Reasons = add(hr.Reasons, "within_workout_conflict")
		}
	}
	// Change protection (section 10.3): a recent day that moves the estimate by
	// more than 5 bpm needs a second agreeing day at least 48 h apart.
	out.UpdateStatus = "confirmed"
	if len(days) >= 2 && latest.age < p.ChangeRecentDays {
		rest := slices.Delete(slices.Clone(vs), len(vs)-1, len(vs))
		rw := slices.Delete(slices.Clone(ws), len(ws)-1, len(ws))
		if base, ok := huberLocation(p, rest, rw); ok && math.Abs(center-base) > p.ChangeBPM {
			confirmed := false
			for _, g := range days[:len(days)-1] {
				apart, _ := ageDays(latest.day, g.day)
				if g.age < p.ChangeRecentDays && apart >= p.ConfirmMinDays && math.Abs(g.bpm-latest.bpm) <= p.ChangeBPM &&
					math.Signbit(g.bpm-base) == math.Signbit(latest.bpm-base) {
					confirmed = true
				}
			}
			if !confirmed {
				out.UpdateStatus = "pending_confirmation"
				hr.Reasons = add(hr.Reasons, "change_unconfirmed")
			}
		}
	}
	if hr.Basis == "conflict" {
		out.UpdateStatus = "none"
	}
}

func latestReference(ws []Workout) *Reference {
	for i := len(ws) - 1; i >= 0; i-- {
		if r := ws[i].Reference; r != nil && r.MaxHR > r.RestHR {
			return r
		}
	}
	return nil
}

func ageDays(evaluation, day string) (int, bool) {
	e, err1 := time.Parse(time.DateOnly, evaluation)
	d, err2 := time.Parse(time.DateOnly, day)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return int(math.Round(e.Sub(d).Hours() / 24)), true
}

// Freshen re-evaluates the heart-rate time status of a published result for a
// later reading day. It never changes the estimate, its range or its basis.
func Freshen(r Result, day string) Result {
	p := DefaultPolicy()
	h := r.HeartRate
	if h.BPM == nil || h.EvidenceDay == "" {
		return r
	}
	age, ok := ageDays(day, h.EvidenceDay)
	if !ok || age < 0 {
		return r
	}
	switch {
	case age <= p.FreshDays:
		h.Freshness = "fresh"
	case age < p.WindowDays:
		h.Freshness = "aging"
	default:
		h.Freshness = "expired"
		h.Reasons = add(slices.Clone(h.Reasons), "hr_evidence_expired")
		sort.Strings(h.Reasons)
	}
	r.HeartRate = h
	return r
}
