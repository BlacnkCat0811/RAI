package runfitness

import (
	"math"
	"slices"
	"sort"
	"time"
)

// StateInput is every single-run result whose Workout day lies in the 90 days
// up to the evaluation day and was known at evaluation time. The caller
// selects them; this package reads no clock.
type StateInput struct {
	DatasetID string `json:"datasetID"`
	Date      string `json:"date"`
	AsOf      int64  `json:"asOf"`
	// View "revised" (default) uses late-uploaded Workouts whose event lies
	// before the evaluation day; "as_known" also requires KnownAt <= AsOf.
	View         string         `json:"view"`
	Observations []SingleResult `json:"observations"`
}

type Dimension struct {
	Status     string   `json:"status"` // supported | estimated | prior_based | lower_bound_only | insufficient_evidence
	Basis      string   `json:"basis"`
	SpeedMPS   *float64 `json:"speedMPS"`
	PaceSecKm  *float64 `json:"paceSecondsPerKm"`
	Rating     *float64 `json:"rating"`
	Range      *Range   `json:"speedRange,omitempty"`
	LowerBound *float64 `json:"lowerBoundMPS,omitempty"`
	Zone       string   `json:"zone"`
	Hint       string   `json:"evidenceHint,omitempty"`
	Retention  *float64 `json:"retention,omitempty"`
	BL         *float64 `json:"bL,omitempty"`
	BLStatus   string   `json:"bLStatus,omitempty"`
}

type Dimensions struct {
	Endurance Dimension `json:"endurance"`
	Threshold Dimension `json:"threshold"`
	Speed     Dimension `json:"speed"`
	Sprint    Dimension `json:"sprint"`
}

type Overall struct {
	Score           *float64 `json:"score"`
	RawScore        *float64 `json:"rawScore"`
	Saturation      string   `json:"saturation,omitempty"`
	Range           *Range   `json:"range"`
	Basis           string   `json:"basis"` // calibrated | estimated | prior_based | conflict | none
	MarathonSeconds *float64 `json:"marathonEquivalentSeconds"`
}

type Params struct {
	A        *float64 `json:"a70MPS"`
	VT       *float64 `json:"vTMPS"`
	V6       *float64 `json:"v6MPS"`
	V90s     *float64 `json:"v90sMPS"`
	BL       *float64 `json:"bL"`
	BS       *float64 `json:"bS"`
	BLStatus string   `json:"bLStatus"`
	BSStatus string   `json:"bSStatus"`
}

type Support struct {
	IndependentDays int     `json:"independentDays"`
	SpanDays        int     `json:"spanDays"`
	Workouts        int     `json:"workouts"`
	Races           int     `json:"races"`
	ConditionShare  float64 `json:"conditionDownweightedShare"`
	EffectiveWeight float64 `json:"effectiveWeight"`
}

type Stability struct {
	ChangeUnconfirmed bool    `json:"changeUnconfirmed"`
	MaxScoreDelta     float64 `json:"looMaxScoreDelta"`
	MaxAnchorDelta    float64 `json:"looMaxAnchorDelta"`
}

type Preparation struct {
	LongRuns       int      `json:"longRuns90Min"`
	LongestSeconds float64  `json:"longestRunSeconds"`
	TrainingWeeks  int      `json:"effectiveTrainingWeeks"`
	Retention      *float64 `json:"latestRetention"`
}

type EvidenceRef struct {
	WorkoutID string  `json:"workoutID"`
	Day       string  `json:"day"`
	Type      string  `json:"type"`
	Dimension string  `json:"dimension"`
	Weight    float64 `json:"weight"`
	Value     float64 `json:"value"`
	Duration  float64 `json:"durationSeconds,omitempty"`
}

type State struct {
	Schema              string        `json:"schemaVersion"`
	Algorithm           string        `json:"algorithm"`
	Version             string        `json:"algorithmVersion"`
	ConfigHash          string        `json:"configHash"`
	RatingPolicy        string        `json:"ratingPolicy"`
	DatasetID           string        `json:"datasetID"`
	Generation          int64         `json:"generation"`
	Date                string        `json:"date"`
	AsOf                int64         `json:"asOf"`
	Status              string        `json:"status"`    // initial | preliminary | established | race_only | insufficient_evidence | conflict
	Freshness           string        `json:"freshness"` // fresh | aging | stale | race_only | expired
	LatestEvidenceDay   string        `json:"latestEvidenceDay,omitempty"`
	EvidenceAt          *int64        `json:"evidenceAt,omitempty"`
	ValidUntil          string        `json:"validUntil,omitempty"`
	Overall             Overall       `json:"overall"`
	ReferenceDailyScore *float64      `json:"referenceDailyScore"`
	Params              Params        `json:"params"`
	Dimensions          Dimensions    `json:"dimensions"`
	Predictions         []Prediction  `json:"predictions"`
	Zones               Zones         `json:"zones"`
	RacePaces           []RacePace    `json:"racePaces"`
	Preparation         Preparation   `json:"preparation"`
	Support             Support       `json:"support"`
	Stability           Stability     `json:"stability"`
	Reasons             []string      `json:"reasonCodes"`
	Evidence            []EvidenceRef `json:"evidence"`
	Observations        []SingleBrief `json:"observations"`
	// Singles are the full single-run results of Workouts on this day.
	Singles   []SingleResult `json:"singles"`
	InputHash string         `json:"inputHash"`
}

// SingleBrief is the trend point of one Workout's single result.
type SingleBrief struct {
	WorkoutID string   `json:"workoutID"`
	Day       string   `json:"day"`
	Score     *float64 `json:"score"`
	Range     *Range   `json:"range,omitempty"`
	Type      string   `json:"type"`
	Status    string   `json:"status"`
}

func dayIndex(day string) (int, bool) {
	t, err := time.Parse(time.DateOnly, day)
	if err != nil {
		return 0, false
	}
	return int(t.Unix() / 86400), true
}

// evidenceItem is one fit observation with provenance.
type evidenceItem struct {
	o          fitObs
	restricted bool
	race       bool
	sprint     bool
}

type estimate struct {
	fit   FitResult
	ok    bool
	items []evidenceItem
}

func (e estimate) overallRaw() float64 {
	_, raw, _ := ScoreFromTime(MarathonSeconds(e.fit.Curve.VT, e.fit.Curve.BL))
	return raw
}

// runFit applies the "no upgrade from restricted evidence" rule: indoor,
// unknown-grade and extreme-weather observations may establish a value but
// never raise one supported by unrestricted evidence.
func runFit(p Policy, items []evidenceItem) estimate {
	var all, base []fitObs
	hasBase := false
	for _, it := range items {
		all = append(all, it.o)
		if !it.restricted {
			base = append(base, it.o)
			if it.o.kind == obsA || it.o.kind == obsRace {
				hasBase = true
			}
		}
	}
	full, err := Fit(p, all)
	if err != nil {
		return estimate{items: items}
	}
	if hasBase && len(base) < len(all) {
		if b, err := Fit(p, base); err == nil && full.A > b.A {
			return estimate{fit: b, ok: true, items: items}
		}
	}
	return estimate{fit: full, ok: true, items: items}
}

// CalculateState rebuilds one evaluation day from its bounded evidence.
func CalculateState(in StateInput) State {
	p := DefaultPolicy()
	out := State{Schema: Schema, Algorithm: Algorithm, Version: Version, ConfigHash: ConfigHash(), RatingPolicy: RatingPolicy, DatasetID: in.DatasetID,
		Date: in.Date, AsOf: in.AsOf, Status: "insufficient_evidence", Freshness: "expired", Reasons: []string{}, Evidence: []EvidenceRef{}, Observations: []SingleBrief{},
		Predictions: []Prediction{}, RacePaces: []RacePace{}, Singles: []SingleResult{}, Overall: Overall{Basis: "none"}, InputHash: Hash(in)}
	out.Dimensions = Dimensions{
		Endurance: Dimension{Status: "insufficient_evidence", Zone: "Z2", Hint: "steady_aerobic_run"},
		Threshold: Dimension{Status: "insufficient_evidence", Zone: "Z4", Hint: "threshold_test_20_40min"},
		Speed:     Dimension{Status: "insufficient_evidence", Zone: "Z5", Hint: "speed_test_3_12min"},
		Sprint:    Dimension{Status: "insufficient_evidence", Zone: "Z6", Hint: "sprint_repeats_90s"},
	}
	out.Zones = emptyZones()
	today, ok := dayIndex(in.Date)
	if !ok {
		out.Reasons = []string{"invalid_date"}
		return out
	}
	obs := append([]SingleResult(nil), in.Observations...)
	sort.SliceStable(obs, func(i, j int) bool {
		if obs[i].Start != obs[j].Start {
			return obs[i].Start < obs[j].Start
		}
		return obs[i].WorkoutID < obs[j].WorkoutID
	})
	// Same Workout delivered twice keeps one result (the later known one).
	seen := map[string]int{}
	dedup := obs[:0]
	for _, o := range obs {
		if i, ok := seen[o.WorkoutID]; ok {
			if o.KnownAt >= dedup[i].KnownAt {
				dedup[i] = o
			}
			continue
		}
		seen[o.WorkoutID] = len(dedup)
		dedup = append(dedup, o)
	}
	obs = dedup

	var items []evidenceItem
	// Per local day: the day's budget is its minute-weighted quality times
	// min(total minutes/20, 1), capped at 1, shared by minutes. Splitting one
	// run into two records on the same day cannot add support.
	dayMinutes, dayQuality := map[string]float64{}, map[string]float64{}
	ageOf := map[string]int{}
	for _, o := range obs {
		d, ok := dayIndex(o.Day)
		if !ok || d > today || in.View == "as_known" && o.KnownAt > in.AsOf {
			continue
		}
		ageOf[o.WorkoutID] = today - d
		if o.HR != nil && today-d < p.DailyWindow {
			dayMinutes[o.Day] += o.HR.NetSeconds / 60
			dayQuality[o.Day] += o.HR.Quality * o.HR.NetSeconds / 60
		}
	}
	dailyW := func(age int) float64 {
		return math.Exp(-float64(age)/p.DailyTau) * math.Min(1, float64(p.DailyWindow-age)/float64(p.DailyTaper))
	}
	raceW := func(age int) float64 {
		return math.Exp(-float64(age)/p.RaceTau) * math.Min(1, float64(p.RaceWindow-age)/float64(p.RaceTaper))
	}
	var latestDaily, latestRace = -1, -1
	var latestEnd int64
	hrDays := map[string]bool{}
	var retentions []float64
	condDown, condTotal := 0.0, 0.0
	type sprintObs struct {
		day   string
		v, w  float64
		test  bool
		works string
	}
	var sprints []sprintObs
	for _, o := range obs {
		age, ok := ageOf[o.WorkoutID]
		if !ok {
			continue
		}
		shared := o.HR != nil && o.Race != nil
		if o.HR != nil && age < p.DailyWindow {
			total := dayMinutes[o.Day]
			budget := math.Min(1, dayQuality[o.Day]/total*math.Min(total/p.MinutesFull, 1))
			w := budget * (o.HR.NetSeconds / 60 / total) * dailyW(age)
			if shared {
				w *= .5
			}
			if w > 0 {
				items = append(items, evidenceItem{o: fitObs{kind: obsA, x: math.Log(o.HR.A), w: w, g: [3]float64{1, 0, 0}, day: o.Day, workout: o.WorkoutID},
					restricted: o.Indoor || o.NoUpgrade || o.HR.Mode == "unadjusted"})
				out.Evidence = append(out.Evidence, EvidenceRef{o.WorkoutID, o.Day, "hr_normalized", "endurance", w, o.HR.A, o.HR.NetSeconds})
				hrDays[o.Day] = true
				latestDaily = max(latestDaily, today-age)
				latestEnd = max(latestEnd, o.End)
				condTotal += w
				if o.HR.ConditionWeight < 1 {
					condDown += w
				}
			}
		}
		if o.Race != nil && age < p.RaceWindow {
			w := math.Min(p.RaceBudget, p.RaceBudget*o.Race.Quality) * raceW(age)
			if shared {
				w *= .5
			}
			g, h := speedObs(p, o.Race.Seconds, p.BL0)
			items = append(items, evidenceItem{o: fitObs{kind: obsRace, x: math.Log(o.Race.DistanceM / o.Race.Seconds), sigma: p.SigmaRace, w: w, g: g, h: h,
				day: o.Day, workout: o.WorkoutID, duration: o.Race.Seconds, bLDependent: o.Race.Seconds > TT}, race: true})
			out.Evidence = append(out.Evidence, EvidenceRef{o.WorkoutID, o.Day, o.Race.Kind, raceDimension(o.Race.Seconds), w, o.Race.DistanceM / o.Race.Seconds, o.Race.Seconds})
			latestRace = max(latestRace, today-age)
			latestEnd = max(latestEnd, o.End)
		}
		if age < p.DailyWindow && o.Race == nil {
			for _, d := range o.Demonstrated {
				if d.Duration < 360 {
					continue
				}
				g, h := speedObs(p, float64(d.Duration), p.BL0)
				items = append(items, evidenceItem{o: fitObs{kind: obsLower, x: math.Log(d.Speed), sigma: p.SigmaRace, w: dailyW(age), g: g, h: h,
					day: o.Day, workout: o.WorkoutID, duration: float64(d.Duration), bLDependent: float64(d.Duration) > TT}, restricted: o.NoUpgrade})
			}
		}
		if o.SprintTest != nil && age < p.RaceWindow {
			sprints = append(sprints, sprintObs{o.Day, *o.SprintTest, p.RaceBudget * raceW(age), true, o.WorkoutID})
		} else if o.Sprint90 != nil && age < p.DailyWindow {
			sprints = append(sprints, sprintObs{o.Day, *o.Sprint90, dailyW(age), false, o.WorkoutID})
		}
		if o.Retention != nil && age < p.DailyWindow {
			retentions = append(retentions, o.Retention.Value)
		}
		if age == 0 {
			out.Singles = append(out.Singles, o)
		}
		if age < p.DailyWindow {
			b := SingleBrief{WorkoutID: o.WorkoutID, Day: o.Day, Status: o.Status, Type: "none"}
			if o.HR != nil {
				b.Score, b.Range, b.Type = ptr(o.HR.Score), ptr(o.HR.ScoreRange), "hr_normalized"
			} else if o.Race != nil {
				b.Score, b.Type = ptr(o.Race.Score), "race_equivalent"
			}
			out.Observations = append(out.Observations, b)
		}
	}
	// Support counts.
	days := make([]string, 0, len(hrDays))
	for d := range hrDays {
		days = append(days, d)
	}
	sort.Strings(days)
	out.Support.IndependentDays = len(days)
	if len(days) > 0 {
		a, _ := dayIndex(days[0])
		b, _ := dayIndex(days[len(days)-1])
		out.Support.SpanDays = b - a
	}
	for _, it := range items {
		if it.o.kind == obsA {
			out.Support.Workouts++
			out.Support.EffectiveWeight += it.o.w
		}
		if it.race {
			out.Support.Races++
		}
	}
	if condTotal > 0 {
		out.Support.ConditionShare = condDown / condTotal
	}
	switch {
	case len(days) >= 8 && out.Support.SpanDays >= 21:
		out.Status = "established"
	case len(days) >= 3 && out.Support.SpanDays >= 7:
		out.Status = "preliminary"
	case len(days) >= 1:
		out.Status = "initial"
	case latestRace >= 0:
		out.Status = "race_only"
	}
	switch {
	case latestDaily >= 0 && today-latestDaily <= 14:
		out.Freshness = "fresh"
	case latestDaily >= 0 && today-latestDaily <= 28:
		out.Freshness = "aging"
	case latestDaily >= 0:
		out.Freshness = "stale"
	case latestRace >= 0:
		out.Freshness = "race_only"
	}
	latest := max(latestDaily, latestRace)
	if latest >= 0 {
		out.LatestEvidenceDay = time.Unix(int64(latest)*86400, 0).UTC().Format(time.DateOnly)
		out.ValidUntil = time.Unix(int64(latest+p.DailyWindow-1)*86400, 0).UTC().Format(time.DateOnly)
		if latestDaily < 0 {
			out.ValidUntil = time.Unix(int64(latest+p.RaceWindow-1)*86400, 0).UTC().Format(time.DateOnly)
		}
		out.EvidenceAt = ptr(latestEnd)
	}
	if len(retentions) > 0 {
		out.Preparation.Retention = ptr(retentions[len(retentions)-1])
	}
	out.Preparation = preparation(p, obs, ageOf, out.Preparation.Retention)

	// Sprint: independent robust location of v90s observations.
	var sprintVals, sprintWs []float64
	sprintDays := map[string]bool{}
	sprintTest := false
	for _, s := range sprints {
		sprintVals = append(sprintVals, math.Log(s.v))
		sprintWs = append(sprintWs, s.w)
		sprintDays[s.day] = true
		sprintTest = sprintTest || s.test
		out.Evidence = append(out.Evidence, EvidenceRef{s.works, s.day, map[bool]string{true: "sprint_test", false: "demonstrated_90s"}[s.test], "sprint", s.w, s.v, 90})
	}
	if len(sprintVals) > 0 {
		v := math.Exp(huberLocation(p, sprintVals, sprintWs))
		d := &out.Dimensions.Sprint
		switch {
		case sprintTest:
			d.Status, d.Basis = "supported", "sprint_test"
		case len(sprintDays) >= 2:
			d.Status, d.Basis = "estimated", "repeated_short_efforts"
		default:
			d.Status, d.Basis = "lower_bound_only", "single_day_short_effort"
		}
		if d.Status == "lower_bound_only" {
			d.LowerBound = ptr(v)
		} else {
			d.SpeedMPS, d.PaceSecKm, d.Rating = ptr(v), ptr(Pace(v)), ptr(Rating(v, SprintRef))
			out.Params.V90s = ptr(v)
		}
	}

	est := runFit(p, items)
	if !est.ok {
		if len(items) > 0 {
			out.Reasons = append(out.Reasons, "fit_unidentified")
		}
		finishReasons(&out)
		return out
	}
	// Leave-one-day-out sensitivity on established models.
	if out.Status == "established" {
		evDays := map[string]bool{}
		for _, it := range items {
			evDays[it.o.day] = true
		}
		keys := make([]string, 0, len(evDays))
		for d := range evDays {
			keys = append(keys, d)
		}
		sort.Strings(keys)
		full := est.overallRaw()
		latestDay := ""
		var latestDelta, latestAnchor float64
		for _, d := range keys {
			var rest []evidenceItem
			for _, it := range items {
				if it.o.day != d {
					rest = append(rest, it)
				}
			}
			e := runFit(p, rest)
			if !e.ok {
				continue
			}
			delta := math.Abs(full - e.overallRaw())
			anchor := math.Max(math.Abs(est.fit.A/e.fit.A-1), math.Abs(est.fit.Curve.VT/e.fit.Curve.VT-1))
			out.Stability.MaxScoreDelta = math.Max(out.Stability.MaxScoreDelta, delta)
			out.Stability.MaxAnchorDelta = math.Max(out.Stability.MaxAnchorDelta, anchor)
			latestDay, latestDelta, latestAnchor = d, delta, anchor
		}
		if latestDay != "" && (latestDelta > p.ChangeScore || latestAnchor > p.ChangeAnchor) {
			supportedRace := false
			for _, o := range obs {
				if o.Day == latestDay && o.Race != nil && o.Race.Status == "supported" {
					supportedRace = true
				}
			}
			if !supportedRace {
				out.Stability.ChangeUnconfirmed = true
				out.Reasons = append(out.Reasons, "change_unconfirmed")
				for i := range items {
					if items[i].o.day == latestDay && items[i].o.kind == obsA {
						items[i].o.w *= .5
					}
				}
				if e := runFit(p, items); e.ok {
					est = e
				}
			}
		}
	}
	applyEstimate(p, &out, est, obs, ageOf)
	finishReasons(&out)
	return out
}

func raceDimension(t float64) string {
	switch {
	case t <= 720:
		return "speed"
	case t <= 5400:
		return "threshold"
	}
	return "endurance"
}

// huberLocation is the robust weighted location used for the sprint anchor
// (and, by test, the closed form of the joint fit without race evidence).
func huberLocation(p Policy, xs, ws []float64) float64 {
	s := robustScale(p, xs, ws)
	mu := weightedMedian(xs, ws)
	for i := 0; i < p.MaxIterations; i++ {
		m, b := 0.0, 0.0
		for k, x := range xs {
			u := (x - mu) / s
			c := ws[k] * huberWeight(u, p.HuberDelta) / (s * s)
			m += c * 1 * 1
			b += c * 1 * (x - 0)
		}
		next := b / m
		d := math.Abs(next - mu)
		mu = next
		if d <= p.Tolerance {
			break
		}
	}
	return mu
}

func preparation(p Policy, obs []SingleResult, ageOf map[string]int, retention *float64) Preparation {
	out := Preparation{Retention: retention}
	weeks := map[int]bool{}
	for _, o := range obs {
		age, ok := ageOf[o.WorkoutID]
		if !ok || age >= p.PreparationDays {
			continue
		}
		if o.RunningSecs >= 5400 {
			out.LongRuns++
		}
		out.LongestSeconds = math.Max(out.LongestSeconds, o.RunningSecs)
		if o.HR != nil || o.Race != nil {
			weeks[age/7] = true
		}
	}
	out.TrainingWeeks = len(weeks)
	return out
}

func identifiedShapes(p Policy, items []evidenceItem, obs []SingleResult, ageOf map[string]int) (bL, bS bool, vTCal bool, v6Direct bool) {
	type perf struct {
		day string
		t   float64
	}
	var long, short []perf
	for _, o := range obs {
		age, ok := ageOf[o.WorkoutID]
		if !ok || o.Race == nil || age >= p.RaceWindow {
			continue
		}
		t := o.Race.Seconds
		if t >= TT {
			long = append(long, perf{o.Day, t})
		}
		if t >= 180 && t <= 720 {
			short = append(short, perf{o.Day, t})
			v6Direct = true
		}
		if t >= 720 && t <= 5400 {
			vTCal = true
		}
	}
	pairs := func(xs []perf, ratio float64, needLong bool) bool {
		for i := range xs {
			for j := range xs {
				if xs[i].day == xs[j].day || xs[j].t/xs[i].t < ratio {
					continue
				}
				if !needLong || math.Max(xs[i].t, xs[j].t) >= 4500 {
					return true
				}
			}
		}
		return false
	}
	return pairs(long, 1.5, true), pairs(short, 2, false), vTCal, v6Direct
}

func applyEstimate(p Policy, out *State, est estimate, obs []SingleResult, ageOf map[string]int) {
	f := est.fit
	c := f.Curve
	bLCal, bSCal, vTCal, v6Direct := identifiedShapes(p, est.items, obs, ageOf)
	out.Params = Params{A: ptr(f.A), VT: ptr(c.VT), V6: ptr(c.V6), V90s: out.Params.V90s, BL: ptr(c.BL), BS: ptr(impliedBS(c)),
		BLStatus: map[bool]string{true: "calibrated", false: "prior_based"}[bLCal], BSStatus: map[bool]string{true: "calibrated", false: "prior_based"}[bSCal]}
	if !c.Valid(p) {
		out.Status = "conflict"
		out.Overall.Basis = "conflict"
		out.Reasons = append(out.Reasons, "curve_conflict")
		return
	}
	if bLCal && (c.BL <= p.BLDomain[0]+1e-9 || c.BL >= p.BLDomain[1]-1e-9) {
		out.Reasons = append(out.Reasons, "shape_at_boundary")
	}
	hasHR := out.Support.IndependentDays > 0
	// Dimensions.
	e := &out.Dimensions.Endurance
	e.SpeedMPS, e.PaceSecKm, e.Rating = ptr(f.A), ptr(Pace(f.A)), ptr(Rating(f.A, ARef))
	e.Retention, e.BL, e.BLStatus = out.Preparation.Retention, ptr(c.BL), out.Params.BLStatus
	switch {
	case out.Support.IndependentDays >= 3:
		e.Status, e.Basis = "supported", "daily_hr_evidence"
	case hasHR:
		e.Status, e.Basis = "estimated", "daily_hr_evidence"
	default:
		e.Status, e.Basis = "prior_based", "race_derived_prior"
		e.Rating = nil
	}
	t := &out.Dimensions.Threshold
	t.SpeedMPS, t.PaceSecKm, t.Rating = ptr(c.VT), ptr(Pace(c.VT)), ptr(Rating(c.VT, VTRef))
	t.Status, t.Basis = "estimated", "hrr_prior"
	if vTCal {
		t.Status, t.Basis = "supported", "race_or_test"
	}
	s := &out.Dimensions.Speed
	switch {
	case v6Direct && bSCal:
		s.Status, s.Basis = "supported", "speed_test_calibrated"
	case v6Direct:
		s.Status, s.Basis = "estimated", "speed_test_shape_prior"
	default:
		s.Status, s.Basis = "insufficient_evidence", "speed_shape_prior"
		for _, o := range obs {
			if age, ok := ageOf[o.WorkoutID]; ok && age < p.DailyWindow {
				for _, d := range o.Demonstrated {
					if d.Duration >= 180 && d.Duration <= 720 && (s.LowerBound == nil || d.Speed > *s.LowerBound) {
						s.LowerBound = ptr(d.Speed)
					}
				}
			}
		}
		if s.LowerBound != nil {
			s.Status = "lower_bound_only"
		}
	}
	if s.Status == "supported" || s.Status == "estimated" {
		s.SpeedMPS, s.PaceSecKm, s.Rating = ptr(c.V6), ptr(Pace(c.V6)), ptr(Rating(c.V6, V6Ref))
	}
	out.ReferenceDailyScore = ptr(Rating(f.A, ARef))

	// Overall and ranges from deterministic corners.
	tm := MarathonSeconds(c.VT, c.BL)
	score, raw, sat := ScoreFromTime(tm)
	out.Overall = Overall{Score: ptr(score), RawScore: ptr(raw), Saturation: sat, MarathonSeconds: ptr(tm)}
	switch {
	case bLCal && vTCal:
		out.Overall.Basis = "calibrated"
	case vTCal || out.Status == "established":
		out.Overall.Basis = "estimated"
	default:
		out.Overall.Basis = "prior_based"
	}
	if !bLCal {
		out.Reasons = append(out.Reasons, "endurance_prior")
	}
	if !vTCal {
		out.Reasons = append(out.Reasons, "threshold_prior")
	}
	if !v6Direct {
		out.Reasons = append(out.Reasons, "speed_evidence_missing")
	}
	if out.Params.V90s == nil {
		out.Reasons = append(out.Reasons, "sprint_evidence_missing")
	}
	corners := cornerCurves(p, est, out, bLCal)
	var overall []float64
	for _, cc := range corners {
		s, _, _ := ScoreFromTime(MarathonSeconds(cc.VT, cc.BL))
		overall = append(overall, s)
	}
	out.Overall.Range = spanOf(append(overall, score))
	if r := cornerRange(corners, func(c Curve) float64 { return c.VT }); r != nil {
		t.Range = r
	}
	if s.SpeedMPS != nil {
		s.Range = cornerRange(corners, func(c Curve) float64 { return c.V6 })
	}
	out.Predictions = predictions(p, c, corners, obs, ageOf, out, bLCal, vTCal, v6Direct)
	out.RacePaces = racePaces(out.Predictions)
	out.Zones = buildZones(p, out, f.A, c, bSCal, v6Direct)
}

// cornerCurves propagates A (fit and reference sensitivity), the threshold
// and short-shape relations, and bL (prior range unless calibrated).
func cornerCurves(p Policy, est estimate, out *State, bLCal bool) []Curve {
	f := est.fit
	var halves []float64
	for _, o := range out.Observations {
		if o.Range != nil && o.Score != nil {
			// score units -> log A: d score / d ln A = 55/ln 2.
			halves = append(halves, (o.Range.High-o.Range.Low)/2*math.Ln2/(50*BL0))
		}
	}
	meas := 0.0
	if len(halves) > 0 {
		sort.Float64s(halves)
		meas = halves[len(halves)/2]
	}
	hA := math.Sqrt(math.Max(f.Cov[0][0], 0) + meas*meas)
	hT := math.Sqrt(math.Max(f.Cov[1][1], 0))
	h6 := math.Sqrt(math.Max(f.Cov[2][2], 0))
	bls := []float64{p.BLRange[0], p.BLRange[1]}
	if bLCal {
		bls = []float64{math.Max(p.BLDomain[0], f.BL-.02), math.Min(p.BLDomain[1], f.BL+.02)}
	} else {
		bls = []float64{math.Min(bls[0], f.BL), math.Max(bls[1], f.BL)}
	}
	var out2 []Curve
	for _, sa := range []float64{-1, 1} {
		for _, st := range []float64{-1, 1} {
			for _, s6 := range []float64{-1, 1} {
				for _, bl := range bls {
					lnA := f.Theta[0] + sa*hA
					lnvT := lnA + cT(p) + f.Theta[1] + st*hT
					lnv6 := lnvT + c6(p.BS0) + f.Theta[2] + s6*h6
					c := Curve{VT: math.Exp(lnvT), V6: math.Exp(lnv6), BL: bl}
					if c.Valid(p) {
						out2 = append(out2, c)
					}
				}
			}
		}
	}
	return out2
}

func cornerRange(cs []Curve, fn func(Curve) float64) *Range {
	var xs []float64
	for _, c := range cs {
		xs = append(xs, fn(c))
	}
	return spanOf(xs)
}

func finishReasons(out *State) {
	out.Reasons = append(out.Reasons, "freshness_"+out.Freshness)
	slices.Sort(out.Reasons)
	out.Reasons = slices.Compact(out.Reasons)
}

// Freshen re-evaluates evidence freshness for a read on a later local day
// than the publication. It never changes values: stale or expired evidence
// only stops automatic pace binding.
func Freshen(s State, day string) State {
	if s.LatestEvidenceDay == "" || day <= s.Date {
		return s
	}
	p := DefaultPolicy()
	today, ok1 := dayIndex(day)
	latest, ok2 := dayIndex(s.LatestEvidenceDay)
	if !ok1 || !ok2 {
		return s
	}
	age := today - latest
	dailyStatus := s.Status != "race_only" && s.Freshness != "race_only"
	switch {
	case dailyStatus && age <= 14:
		s.Freshness = "fresh"
	case dailyStatus && age <= 28:
		s.Freshness = "aging"
	case dailyStatus && age < p.DailyWindow:
		s.Freshness = "stale"
	case !dailyStatus && age < p.RaceWindow:
		s.Freshness = "race_only"
	default:
		s.Freshness = "expired"
	}
	if s.Freshness == "stale" || s.Freshness == "expired" {
		zones := make([]Zone, len(s.Zones.Zones))
		copy(zones, s.Zones.Zones)
		for i := range zones {
			zones[i].Schedulable = false
		}
		s.Zones.Zones = zones
		s.Zones.Reasons = append(append([]string{}, s.Zones.Reasons...), "evidence_not_fresh")
		paces := make([]RacePace, len(s.RacePaces))
		copy(paces, s.RacePaces)
		for i := range paces {
			paces[i].Schedulable = false
		}
		s.RacePaces = paces
	}
	reasons := []string{}
	for _, r := range s.Reasons {
		if len(r) < 10 || r[:10] != "freshness_" {
			reasons = append(reasons, r)
		}
	}
	s.Reasons = append(reasons, "freshness_"+s.Freshness)
	slices.Sort(s.Reasons)
	return s
}
