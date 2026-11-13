package training

import (
	"math"
	"slices"
	"sort"
	"strings"
	"time"
)

// training_plan/v4: a rolling, adaptive plan.
//
//   - Two modes. With a goal the weeks are phased backwards from the race
//     (pre, base, build, peak, taper, race, post); without one, or once the race
//     and its recovery are over, each week follows the runner's training.
//   - Three layers of adaptation: a week decision from last week's completed
//     volume, load and readiness; in-week rebalancing after missed key sessions
//     or extra volume; and today's readiness and session recovery.
//   - Time is the prescription and every generated run lasts at least
//     MinSessionS; a small budget means fewer sessions, never shorter ones.
//   - The past is frozen: days before AsOf come verbatim from the previous base
//     and a week decision, once taken in its week, is reused. Without a new
//     fact the output is byte-identical.

const layout = "2006-01-02"

type v4Goal struct {
	idx, week                         int
	cat                               string
	catM                              float64
	taper, peak, build, maxPrep, post int
	minPrep                           int
	taperF                            []float64
	longMax                           float64
	racePace                          *float64
	status                            GoalStatus
}

type v4Signals struct {
	readiness          *float64
	estimated          bool
	acute, chronic     *float64
	hrMax, hrRest      *float64
	paces              PaceTargets
	predictions        map[string]float64
	readyAfter         *float64
	readinessAvailable bool
}

type v4 struct {
	in           Context
	p            CoachParameters
	loc          *time.Location
	anchor       time.Time
	today, cur   int
	days         []int
	avail        [7]bool
	longDay      int
	runS         map[int]float64
	runN         map[int]int
	firstWeek    int
	hasRuns      bool
	focus        [3]float64
	focusRuns    int
	sig          v4Signals
	goal         *v4Goal
	prevSessions map[int]Session
	prevWeeks    map[int]WeekSummary
	locked       map[string]bool
	recs         []string
}

func generateV4(in Context) (Base, error) {
	out := Base{Sessions: []Session{}, Recommendations: []string{}}
	if in.Coach == nil || *in.Coach != V2Parameters() || in.Config.Validate(in.Timezone) != nil {
		return out, ErrInvalid
	}
	g, e := newV4(in)
	if e != nil {
		return out, e
	}
	return g.run()
}

func floorDiv(a, b int) int {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}
func round60(v float64) float64 { return math.Round(v/60) * 60 }
func floor60(v float64) float64 { return math.Floor(v/60) * 60 }

func (g *v4) idx(s string) (int, error) {
	d, e := time.ParseInLocation(layout, s, g.loc)
	if e != nil || d.Format(layout) != s {
		return 0, ErrInvalid
	}
	return int(math.Round(d.Sub(g.anchor).Hours() / 24)), nil
}
func (g *v4) date(i int) string { return g.anchor.AddDate(0, 0, i).Format(layout) }

func newV4(in Context) (*v4, error) {
	g := &v4{in: in, p: *in.Coach, runS: map[int]float64{}, runN: map[int]int{}, prevSessions: map[int]Session{}, prevWeeks: map[int]WeekSummary{}, locked: map[string]bool{}}
	g.loc, _ = time.LoadLocation(in.Timezone)
	g.anchor, _ = time.ParseInLocation(layout, in.Config.StartDate, g.loc)
	t, e := g.idx(in.AsOf)
	if e != nil {
		return nil, ErrInvalid
	}
	g.today = max(t, 0)
	g.cur = g.today / 7
	for _, d := range in.Config.AvailableDays {
		g.avail[d-1] = true
	}
	for d := 0; d < 7; d++ {
		if g.avail[d] {
			g.days = append(g.days, d)
		}
	}
	g.longDay = in.Config.LongRunDay - 1
	g.firstWeek = math.MaxInt
	for _, w := range in.Workouts {
		i, e := g.idx(w.Date)
		if e != nil {
			return nil, ErrInvalid
		}
		if i > g.today || i <= g.today-g.p.HistoryDays {
			continue
		}
		if w.DurationS != nil && (!Finite(*w.DurationS) || *w.DurationS < 0) {
			return nil, ErrInvalid
		}
		if w.DurationS != nil {
			g.runS[i] += *w.DurationS
		}
		g.runN[i]++
		g.hasRuns = true
		g.firstWeek = min(g.firstWeek, floorDiv(i, 7))
		if i > g.today-28 && w.FocusLow != nil && w.FocusHigh != nil && w.FocusAnaerobic != nil &&
			Finite(*w.FocusLow) && Finite(*w.FocusHigh) && Finite(*w.FocusAnaerobic) {
			g.focus[0] += max(*w.FocusLow, 0)
			g.focus[1] += max(*w.FocusHigh, 0)
			g.focus[2] += max(*w.FocusAnaerobic, 0)
			g.focusRuns++
		}
	}
	for _, s := range in.PreviousSessions {
		if s.Origin != "generated" || s.Slot < 0 || g.date(s.Slot) != s.Date || ValidateSession(s) != nil {
			continue
		}
		g.prevSessions[s.Slot] = s
	}
	for _, w := range in.PreviousWeeks {
		if w.Detail != "outline" {
			g.prevWeeks[w.Index] = w
		}
	}
	for _, o := range ActiveOverrides(in.Overrides) {
		if o.Status == "active" && o.Operation != "revoke" {
			g.locked[o.SessionID] = true
		}
	}
	g.parseSignals()
	if in.Config.HasGoal() {
		if e := g.buildGoal(); e != nil {
			return nil, e
		}
	}
	return g, nil
}

func (g *v4) parseSignals() {
	adapter := ""
	if g.in.ObservedModelBinding != nil {
		adapter = g.in.ObservedModelBinding.Adapter
	}
	v2 := adapter == "schema2-v2" || adapter == "schema3-v1" || adapter == "schema4-v1"
	newAdapter := adapter == "schema3-v1" || adapter == "schema4-v1"
	g.sig.paces = PaceTargets{}
	g.sig.predictions = map[string]float64{}
	for _, s := range g.in.Signals {
		if s.Name == "readiness" && (newAdapter && s.Version != "v4" || !newAdapter && s.Version == "v4") {
			continue
		}
		if s.Algorithm == PaceAlgorithm && s.Version == "v1" && adapter == "schema4-v1" {
			if s.Value != nil && Finite(*s.Value) && s.Quality == "complete" {
				if zone, ok := strings.CutPrefix(s.Name, "pace_"); ok {
					g.sig.paces[zone] = *s.Value
				} else if d, ok := strings.CutPrefix(s.Name, "predict_"); ok && *s.Value > 0 {
					g.sig.predictions[d] = *s.Value
				}
			}
			continue
		}
		if s.Name == "ready_after" && s.Algorithm == SessionRecoveryAlgorithm {
			if newAdapter && s.Value != nil && Finite(*s.Value) {
				g.sig.readyAfter = s.Value
			}
			continue
		}
		if !v2 || s.Value == nil || !Finite(*s.Value) || (s.Version != "v2" && !(s.Name == "readiness" && (s.Version == "v3" || s.Version == "v4"))) {
			continue
		}
		estimated := s.Version == "v4" && s.Name == "readiness" && s.Mode == "estimated" && s.Quality == "estimated"
		if s.Quality != "complete" && !estimated && !(s.Quality == "partial" && s.Algorithm == "rai.load_state.exp_finite") {
			continue
		}
		switch {
		case s.Name == "readiness" && s.Algorithm == "rai.readiness.training":
			g.sig.readiness, g.sig.estimated = s.Value, estimated
		case s.Name == "hr_max" && s.Algorithm == "hr_reference":
			g.sig.hrMax = s.Value
		case s.Name == "hr_rest" && s.Algorithm == "hr_reference":
			g.sig.hrRest = s.Value
		case s.Name == "acute" && s.Algorithm == "rai.load_state.exp_finite":
			g.sig.acute = s.Value
		case s.Name == "chronic" && s.Algorithm == "rai.load_state.exp_finite":
			g.sig.chronic = s.Value
		}
	}
}

func (g *v4) acwr() float64 {
	if g.sig.acute == nil || g.sig.chronic == nil || *g.sig.chronic <= 0 {
		return 0
	}
	return *g.sig.acute / *g.sig.chronic
}

// Goal tables: taper, peak and build weeks, the longest and shortest useful
// preparation, post-race recovery weeks and the taper volume factors (index 0
// is the week right before the race week).
func (g *v4) buildGoal() error {
	c := g.in.Config.Goal
	i, e := g.idx(c.Date)
	if e != nil {
		return ErrInvalid
	}
	x := &v4Goal{idx: i, week: floorDiv(i, 7)}
	x.cat, x.catM = raceCategory(c.DistanceM)
	switch x.cat {
	case "5k":
		x.taper, x.peak, x.build, x.maxPrep, x.minPrep, x.post, x.taperF, x.longMax = 1, 2, 4, 12, 4, 1, []float64{.7}, g.p.LongMax5KS
	case "10k":
		x.taper, x.peak, x.build, x.maxPrep, x.minPrep, x.post, x.taperF, x.longMax = 1, 2, 5, 14, 4, 1, []float64{.7}, g.p.LongMax10KS
	case "half":
		x.taper, x.peak, x.build, x.maxPrep, x.minPrep, x.post, x.taperF, x.longMax = 1, 3, 6, 16, 6, 2, []float64{.7}, g.p.LongMaxHalfS
	default:
		x.taper, x.peak, x.build, x.maxPrep, x.minPrep, x.post, x.taperF, x.longMax = 2, 3, 8, 20, 8, 2, []float64{.6, .75}, g.p.LongMaxFullS
	}
	st := GoalStatus{Date: c.Date, DistanceM: c.DistanceM, Category: x.cat, DaysToRace: i - g.today, PrepStart: g.date(7 * max(0, x.week-x.maxPrep+1)), TargetTimeS: c.TargetTimeS, Reasons: []string{}}
	if x.week+1 < x.minPrep {
		st.Reasons = append(st.Reasons, "goal_short_notice")
	}
	var predicted *float64
	if p, ok := g.sig.predictions[x.cat]; ok {
		// Riegel scaling from the nearest predicted distance.
		predicted = Ptr(math.Round(p * math.Pow(c.DistanceM/x.catM, 1.06)))
		st.PredictedTimeS = predicted
	}
	km := c.DistanceM / 1000
	switch {
	case c.TargetTimeS == nil:
		st.Assessment = "finish"
		if predicted != nil {
			x.racePace = Ptr(*predicted / km)
		} else {
			st.Reasons = append(st.Reasons, "goal_prediction_unavailable")
		}
	case predicted == nil:
		st.Assessment = "unknown"
		st.Reasons = append(st.Reasons, "goal_prediction_unavailable")
	default:
		gap := (*predicted - *c.TargetTimeS) / *predicted
		st.GapRatio = Ptr(math.Round(gap*1000) / 1000)
		switch {
		case gap <= g.p.GoalOnTrack:
			st.Assessment = "on_track"
		case gap <= g.p.GoalAmbitious:
			st.Assessment = "ambitious"
		default:
			st.Assessment = "out_of_reach"
		}
		if gap <= g.p.GoalAmbitious {
			x.racePace = Ptr(*c.TargetTimeS / km)
		} else {
			x.racePace = Ptr(*predicted / km)
			st.Reasons = append(st.Reasons, "goal_pace_capped_to_ability")
		}
	}
	if x.racePace != nil {
		st.RacePaceSPerKM = Ptr(math.Round(*x.racePace))
	}
	x.status = st
	g.goal = x
	return nil
}

// phase of week w. "fitness" without a goal or after the race's recovery.
func (g *v4) phase(w int) string {
	x := g.goal
	if x == nil {
		return "fitness"
	}
	before := x.week - w
	switch {
	case before < 0:
		if -before <= x.post {
			return "post"
		}
		return "fitness"
	case before == 0:
		return "race"
	case before <= x.taper:
		return "taper"
	case before <= x.taper+x.peak:
		return "peak"
	case before <= x.taper+x.peak+x.build:
		return "build"
	case before < x.maxPrep:
		return "base"
	}
	return "pre"
}

func (g *v4) weekActual(w int) float64 {
	sum := 0.0
	for d := 0; d < 7; d++ {
		sum += g.runS[7*w+d]
	}
	return sum
}

// history averages the four weeks before w, counting only weeks since the
// first recorded run in the history window so a new user is not diluted by
// weeks before they started recording.
func (g *v4) history(w int) (avg, peak float64, ok bool) {
	n := 0
	for k := w - 4; k < w; k++ {
		if !g.hasRuns || k < g.firstWeek || 7*k > g.today {
			continue
		}
		a := g.weekActual(k)
		avg += a
		peak = max(peak, a)
		n++
	}
	if n == 0 {
		return 0, 0, false
	}
	return avg / float64(n), peak, true
}

func (g *v4) lastRun(at int) (int, bool) {
	for i := at; i > at-g.p.HistoryDays; i-- {
		if g.runN[i] > 0 {
			return i, true
		}
	}
	return 0, false
}

func (g *v4) loadFocus() string {
	total := g.focus[0] + g.focus[1] + g.focus[2]
	if g.focusRuns < g.p.FocusMinRuns || total <= 0 {
		return "load_focus_unavailable"
	}
	short := []float64{g.p.FocusLowMin - g.focus[0]/total, g.p.FocusHighMin - g.focus[1]/total, g.p.FocusAnaerobicMin - g.focus[2]/total}
	best, code := 0.0, "load_focus_balanced"
	for i, name := range []string{"load_focus_low_aerobic_shortage", "load_focus_high_aerobic_shortage", "load_focus_anaerobic_shortage"} {
		if short[i] > best {
			best, code = short[i], name
		}
	}
	return code
}

func (g *v4) sessionCount(s WeekSummary) int {
	if s.SingleSession {
		return 1
	}
	n := min(len(g.days), int(s.BudgetS/g.p.MinSessionS))
	if n < 2 {
		n = min(2, len(g.days))
	}
	return n
}

// decide takes the week decision for week w at day at. preview decisions
// (next week) assume the current week is completed as planned and ignore
// today's readiness and load ratio, which belong to the current week.
func (g *v4) decide(w int, prev *WeekSummary, at int, preview bool) WeekSummary {
	p := g.p
	s := WeekSummary{Index: w, Start: g.date(7 * w), Phase: g.phase(w), DecidedOn: g.date(at), ConfigRevision: g.in.ConfigRevision, Reasons: []string{}}
	avg, _, hist := g.history(w)
	level := 0.0
	if prev != nil {
		level = prev.LevelS
	}
	baseline := 0.0
	if g.in.Config.BaselineWeeklyS != nil {
		baseline = *g.in.Config.BaselineWeeklyS
	}
	last, ran := g.lastRun(at)
	gap := p.HistoryDays
	if ran {
		gap = at - last
	}
	// Return after a break: a share of the volume before it, by the length of
	// the break; six weeks or more start over from the cold-start week.
	returning := func() {
		s.State = "return"
		ref, n := 0.0, 0
		if ran {
			lw := floorDiv(last, 7)
			for k := lw - 3; k <= lw; k++ {
				if k >= g.firstWeek {
					ref += g.weekActual(k)
					n++
				}
			}
		}
		if n > 0 {
			ref /= float64(n)
		}
		f := 0.0
		switch {
		case gap < 21:
			f = .7
		case gap < 42:
			f = .5
		}
		level = max(ref*f, p.ColdStartWeekS)
	}
	streak := 0
	switch {
	case prev == nil || prev.State == "untracked":
		switch {
		case g.hasRuns && gap >= p.ReturnGapDays:
			returning()
		case hist && avg > 0:
			s.State, level = "start", avg
		case baseline > 0:
			s.State, level = "start", baseline
			s.Reasons = append(s.Reasons, "cold_start_user_baseline")
		default:
			s.State, level = "start", p.ColdStartWeekS
			s.Reasons = append(s.Reasons, "cold_start_conservative")
		}
	case g.hasRuns && !preview && gap >= p.ReturnGapDays && s.Phase != "race" && s.Phase != "post":
		// Only a break in recorded running is a break: with no runs in the
		// history window at all (nothing synced yet) the plan keeps its level.
		returning()
	case s.Phase == "taper" || s.Phase == "race" || s.Phase == "post":
		s.State = s.Phase
	default:
		planned := prev.PlannedS
		if planned <= 0 {
			planned = prev.BudgetS
		}
		actual := g.weekActual(w - 1)
		if preview {
			actual = max(actual, planned)
		}
		completion := 1.0
		if planned > 0 {
			completion = actual / planned
		}
		ratio := 0.0
		lowReady := false
		if !preview {
			ratio = g.acwr()
			lowReady = g.sig.readiness != nil && *g.sig.readiness < p.ReadinessWeekLow
		}
		switch {
		case prev.ProgressStreak >= p.ProgressStreak || ratio > p.ACWRRecover || lowReady:
			s.State = "recover"
			switch {
			case ratio > p.ACWRRecover:
				s.Reasons = append(s.Reasons, "load_ramp_guard")
			case lowReady:
				s.Reasons = append(s.Reasons, "readiness_week_low")
			default:
				s.Reasons = append(s.Reasons, "planned_recovery_week")
			}
		case completion < p.CompleteHold:
			s.State = "regress"
			if hist && avg > 0 {
				level = min(level, avg)
			}
		case completion < p.CompleteProgress || ratio > p.ACWRHold || prev.State == "recover":
			s.State = "hold"
			if ratio > p.ACWRHold {
				s.Reasons = append(s.Reasons, "load_ramp_guard")
			}
		case s.Phase == "peak":
			// The peak holds the volume reached; it does not build further.
			s.State = "hold"
			s.Reasons = append(s.Reasons, "phase_peak_hold")
		default:
			s.State = "progress"
			level = min(max(level, actual)*(1+p.Progression), level*(1+p.ProgressionCap))
			streak = prev.ProgressStreak + 1
		}
		if preview {
			s.Reasons = append(s.Reasons, "week_preview")
		}
	}
	s.ProgressStreak = streak
	if level <= 0 {
		level = p.ColdStartWeekS
	}
	level = min(level, p.MaxWeekS)
	s.LevelS = round60(level)
	peakRef := 0.0
	if prev != nil {
		peakRef = prev.PeakS
	}
	budget := s.LevelS
	switch s.State {
	case "recover":
		budget = s.LevelS * p.RecoveryFactor
	case "taper":
		ref := peakRef
		if ref == 0 {
			ref = s.LevelS
		}
		budget = ref * g.goal.taperF[g.goal.week-w-1]
	case "race":
		ref := peakRef
		if ref == 0 {
			ref = s.LevelS
		}
		budget = ref * .4
	case "post":
		budget = s.LevelS * .5
	}
	s.PeakS = peakRef
	if s.Phase == "base" || s.Phase == "build" || s.Phase == "peak" {
		s.PeakS = max(peakRef, budget)
	}
	// Floors: a very small history gets one 30-minute run a week until it
	// has been completed for FloorWeeksExit weeks in a row.
	s.LowBase = (hist && avg < p.LowBaseWeekS) || (!hist && baseline < p.LowBaseWeekS)
	if hist && avg > 0 && 2*p.MinSessionS > p.LowFloorFactor*avg && s.State != "return" {
		floorDone := 0
		if prev != nil && prev.SingleSession && g.weekActual(w-1) >= prev.PlannedS && prev.PlannedS > 0 {
			floorDone = prev.FloorWeeks + 1
		}
		s.FloorWeeks = floorDone
		if floorDone < p.FloorWeeksExit {
			s.SingleSession = true
		}
	}
	if s.SingleSession {
		budget = p.MinSessionS
		s.Reasons = append(s.Reasons, "low_base_floor")
	} else if budget < 2*p.MinSessionS {
		budget = 2 * p.MinSessionS
		s.Reasons = append(s.Reasons, "minimum_session_floor")
	}
	s.BudgetS = round60(min(budget, p.MaxWeekS))
	g.focusFor(w, &s)
	s.Reasons = append([]string{"week_state_" + s.State, "phase_" + s.Phase}, s.Reasons...)
	return s
}

func (g *v4) cat() string {
	if g.goal == nil {
		return ""
	}
	return g.goal.cat
}

// focusFor chooses the week's quality sessions and whether an easy run gets
// strides. Race phases follow the goal; fitness weeks fill the largest gap of
// the 28-day load focus.
func (g *v4) focusFor(w int, s *WeekSummary) {
	cat := g.cat()
	short := cat == "5k" || cat == "10k"
	var list []string
	switch s.Phase {
	case "race", "post":
	case "taper":
		list = []string{"race_pace"}
	case "base":
		list, s.Strides = []string{"tempo"}, true
	case "build":
		if short {
			list, s.Strides = []string{"vo2max", "threshold"}, true
		} else {
			list = []string{"threshold", "tempo"}
		}
	case "peak":
		if short {
			list = []string{"race_pace", "vo2max"}
		} else {
			list = []string{"race_pace", "threshold"}
		}
	default:
		s.Focus = g.loadFocus()
		switch s.Focus {
		case "load_focus_low_aerobic_shortage":
		case "load_focus_unavailable":
			rot := []string{"tempo", "threshold", "vo2max"}
			list = []string{rot[w%3], rot[(w+1)%3]}
		default:
			list = []string{"threshold", "vo2max"}
			if w%2 == 1 {
				list = []string{"vo2max", "threshold"}
			}
			s.Strides = s.Focus == "load_focus_anaerobic_shortage"
		}
	}
	n := g.sessionCount(*s)
	h := 0
	switch {
	case s.LowBase || s.State == "recover" || s.State == "return" || s.SingleSession || n < 2 || len(list) == 0:
	case s.Phase == "taper" || s.Phase == "base":
		h = 1
	case s.BudgetS >= g.p.TwoHardWeekS && len(g.days) >= g.p.TwoHardDays && n >= 4:
		h = 2
	default:
		h = 1
	}
	if h == 1 && len(list) == 2 && s.Phase == "build" && w%2 == 1 {
		list = []string{list[1], list[0]}
	}
	s.HardFocus = append([]string(nil), list[:min(h, len(list))]...)
	if s.LowBase || s.State == "recover" || s.State == "return" || s.SingleSession {
		s.Strides = false
	}
}

func (g *v4) longMax() float64 {
	if g.goal != nil && g.phase(g.cur) != "fitness" && g.phase(g.cur) != "pre" {
		return g.goal.longMax
	}
	return g.p.LongMaxS
}

func circular(a, b int) int {
	d := a - b
	if d < 0 {
		d = -d
	}
	return min(d, 7-d)
}

// chooseDays returns n available days in priority order: the long-run day
// first, then the day farthest from those already chosen (earliest on ties).
func (g *v4) chooseDays(n int) []int {
	out := []int{}
	if n <= 0 {
		return out
	}
	if g.avail[g.longDay] {
		out = append(out, g.longDay)
	}
	for len(out) < n {
		best, bestD := -1, -1
		for _, d := range g.days {
			if slices.Contains(out, d) {
				continue
			}
			dist := 7
			for _, c := range out {
				dist = min(dist, circular(c, d))
			}
			if dist > bestD {
				best, bestD = d, dist
			}
		}
		if best < 0 {
			break
		}
		out = append(out, best)
	}
	return out
}

func (g *v4) newSession(i int, typ, focus string, dur float64, effort int, steps []Step, reasons ...string) Session {
	s := Session{ID: StableID(g.in.PlanID, i), Slot: i, Date: g.date(i), Type: typ, Origin: "generated", Status: "scheduled", DurationS: dur, Effort: effort, Steps: steps, Focus: focus, Purpose: focusPurpose[focus], Reasons: append([]string{}, reasons...)}
	if typ == "REST" {
		s.DurationS, s.Effort, s.Focus, s.Purpose = 0, 0, "", ""
	}
	return s
}

func (g *v4) rest(i int, reasons ...string) Session {
	if len(reasons) == 0 {
		reasons = []string{"minimum_recovery"}
	}
	return g.newSession(i, "REST", "", 0, 0, nil, reasons...)
}

func (g *v4) easy(i int, dur float64, focus string, reasons ...string) Session {
	switch focus {
	case "recovery":
		return g.newSession(i, "RECOVERY", focus, dur, 2, nil, append([]string{"recovery_after_hard_session"}, reasons...)...)
	case "strides":
		return g.newSession(i, "EASY", focus, dur, 4, stridesSteps(dur), append([]string{"aerobic_base", "strides_neuromuscular"}, reasons...)...)
	}
	return g.newSession(i, "EASY", "aerobic_base", dur, 3, nil, append([]string{"aerobic_base"}, reasons...)...)
}

func (g *v4) level(s WeekSummary) int {
	l := int(s.BudgetS/3600) - 2
	switch s.Phase {
	case "peak":
		l++
	case "taper":
		l -= 2
	}
	return clampLevel(l)
}

func (g *v4) hard(i int, focus string, lvl int, reasons ...string) Session {
	spec := hardSpec(focus, g.cat(), lvl)
	code := "quality_" + focus
	return g.newSession(i, spec.Type, focus, spec.DurationS, spec.Effort, spec.Steps, append([]string{code}, reasons...)...)
}

// template lays out a decided week before any in-week adaptation.
func (g *v4) template(w int, s WeekSummary) []Session {
	out := make([]Session, 7)
	for d := range 7 {
		reason := "minimum_recovery"
		if g.avail[d] {
			reason = "session_count_by_budget"
		}
		out[d] = g.rest(7*w+d, reason)
	}
	wr := "week_state_" + s.State
	switch {
	case s.Phase == "race" && g.goal != nil:
		g.raceWeek(w, s, out)
		return out
	case s.Phase == "post" || s.State == "return":
		n := g.sessionCount(s)
		each := floor60(max(g.p.MinSessionS, min(s.BudgetS/float64(n), g.p.MaxEasyS)))
		focus := "aerobic_base"
		if s.Phase == "post" && g.goal != nil && w == g.goal.week+1 {
			focus = "recovery"
		}
		for _, d := range g.chooseDays(n) {
			x := g.easy(7*w+d, each, focus, wr)
			if s.Phase == "post" {
				x.Reasons = append(x.Reasons, "post_event_recovery")
			}
			out[d] = x
		}
		return out
	}
	p := g.p
	B := s.BudgetS
	n := g.sessionCount(s)
	order := g.chooseDays(n)
	hasLong := n >= 2 && B*p.LongShare >= p.LongMinS
	longDay := -1
	if hasLong && slices.Contains(order, g.longDay) {
		longDay = g.longDay
	} else {
		hasLong = false
	}
	// Quality days: away from the long run and from each other.
	cands := []int{}
	for _, d := range order {
		if d != longDay {
			cands = append(cands, d)
		}
	}
	sort.SliceStable(cands, func(a, b int) bool {
		if longDay < 0 {
			return cands[a] < cands[b]
		}
		da, db := circular(cands[a], longDay), circular(cands[b], longDay)
		if da != db {
			return da > db
		}
		return cands[a] < cands[b]
	})
	hardDays := []int{}
	for _, d := range cands {
		if len(hardDays) == len(s.HardFocus) {
			break
		}
		ok := longDay < 0 || circular(d, longDay) >= p.HardSpacingDays
		for _, h := range hardDays {
			ok = ok && circular(d, h) >= p.HardSpacingDays
		}
		if ok {
			hardDays = append(hardDays, d)
		}
	}
	sort.Ints(hardDays)
	lvl := g.level(s)
	used := 0.0
	key := map[int]bool{}
	for i, d := range hardDays {
		x := g.hard(7*w+d, s.HardFocus[i], lvl, wr)
		out[d] = x
		used += x.DurationS
		key[d] = true
	}
	if hasLong {
		key[longDay] = true
	}
	recovery := []int{}
	for _, d := range order {
		if key[d] {
			continue
		}
		for k := range key {
			if (k+1)%7 == d {
				recovery = append(recovery, d)
				break
			}
		}
	}
	easyDays := []int{}
	for _, d := range order {
		if !key[d] && !slices.Contains(recovery, d) {
			easyDays = append(easyDays, d)
		}
	}
	long := 0.0
	if hasLong {
		long = floor60(min(max(B*p.LongShare, p.LongMinS), g.longMax()))
	}
	rem := B - used - long - float64(len(recovery))*p.RecoverySessionS
	for len(easyDays) > 0 && rem/float64(len(easyDays)) < p.MinSessionS {
		easyDays = easyDays[:len(easyDays)-1]
	}
	if len(easyDays) == 0 && hasLong && rem > 0 {
		long = floor60(min(long+rem, g.longMax()))
	}
	if hasLong {
		focus := "long_aerobic"
		var steps []Step
		if s.Phase == "peak" && (g.cat() == "half" || g.cat() == "marathon") {
			if st := longRacePaceSteps(long, g.cat(), lvl); st != nil {
				focus, steps = "long_race_pace", st
			}
		}
		effort := 4
		if focus == "long_race_pace" {
			effort = 5
		}
		out[longDay] = g.newSession(7*w+longDay, "LONG", focus, long, effort, steps, "aerobic_base", "long_run_progression", wr)
	}
	for _, d := range recovery {
		out[d] = g.easy(7*w+d, p.RecoverySessionS, "recovery", wr)
	}
	if len(easyDays) > 0 {
		each := floor60(min(rem/float64(len(easyDays)), p.MaxEasyS))
		sort.Ints(easyDays)
		for i, d := range easyDays {
			focus := "aerobic_base"
			if s.Strides && i == 0 {
				focus = "strides"
			}
			out[d] = g.easy(7*w+d, each, focus, wr)
		}
	}
	if len(hardDays) < len(s.HardFocus) {
		g.recs = append(g.recs, "quality_session_not_placed")
	}
	return out
}

// raceWeek: the race on its date, a short sharpening session three to four
// days before, easy 30-minute runs on other days before the eve, and rest or
// short recovery runs after.
func (g *v4) raceWeek(w int, s WeekSummary, out []Session) {
	x := g.goal
	rd := x.idx - 7*w
	c := g.in.Config.Goal
	race := g.newSession(x.idx, "RACE", "goal_event", 0, 9, nil, "goal_event")
	race.DistanceM = c.DistanceM
	if c.TargetTimeS != nil {
		race.DurationS = math.Round(*c.TargetTimeS)
		if pace := math.Round(*c.TargetTimeS / (c.DistanceM / 1000)); pace >= 120 && pace <= 1800 {
			race.PaceSPerKM = Ptr(pace)
		}
	} else if x.status.PredictedTimeS != nil && *x.status.PredictedTimeS <= 21600 {
		race.DurationS = *x.status.PredictedTimeS
	}
	out[rd] = race
	budget := int(s.BudgetS / g.p.MinSessionS)
	used := 0
	wake := -1
	for _, d := range []int{rd - 3, rd - 4, rd - 2, rd - 5} {
		if d >= 0 && g.avail[d] {
			wake = d
			break
		}
	}
	if wake >= 0 && budget > 0 {
		out[wake] = g.hard(7*w+wake, "race_wakeup", 0, "taper")
		used++
	}
	for d := 0; d < rd-1 && used < budget; d++ {
		if g.avail[d] && d != wake {
			out[d] = g.easy(7*w+d, g.p.MinSessionS, "aerobic_base", "taper")
			used++
		}
	}
	if rd-1 >= 0 {
		out[rd-1] = g.rest(7*w+rd-1, "pre_race_rest")
	}
	for d := rd + 1; d < 7; d++ {
		if d >= rd+2 && g.avail[d] && (x.cat == "5k" || x.cat == "10k") {
			out[d] = g.easy(7*w+d, g.p.MinSessionS, "recovery", "post_event_recovery")
		} else {
			out[d] = g.rest(7*w+d, "post_event_recovery")
		}
	}
}

func isKey(t string) bool { return t == "LONG" || t == "TEMPO" || t == "INTERVAL" || t == "RACE" }

func (g *v4) done(i int, s Session) bool {
	if g.runN[i] == 0 {
		return false
	}
	return s.DurationS <= 0 || g.runS[i] >= g.p.DoneRatio*s.DurationS
}

// keyDays lists the days of week w (as day offsets, previous-week days
// negative) that carry a key session in the merged plan: a done key session in
// the past, any key session still ahead, user-added or user-moved key work.
func (g *v4) keyDays(w int, week []Session, skip int) []int {
	all := append([]Session(nil), week...)
	for _, d := range []int{-2, -1} {
		if s, ok := g.prevSessions[7*w+d]; ok {
			all = append(all, s)
		}
	}
	merged, e := ResolveEffectiveVersion(Base{Sessions: all, Recommendations: []string{}}, g.in.Overrides, MergeVersion)
	if e != nil {
		merged = Base{Sessions: all}
	}
	out := []int{}
	for _, s := range merged.Sessions {
		i, e := g.idx(s.Date)
		if e != nil || floorDiv(i, 7) < w-1 || floorDiv(i, 7) > w || i-7*w == skip || !isKey(s.Type) || s.Status != "scheduled" {
			continue
		}
		if i < g.today && !g.done(i, s) {
			continue
		}
		out = append(out, i-7*w)
	}
	return out
}

func (g *v4) open(i int) bool { return i > g.today || (i == g.today && g.runN[i] == 0) }

// place finds the earliest open EASY day of week w at or after from that keeps
// the spacing to every other key day. -1 when there is none.
func (g *v4) place(w int, week []Session, from int, skip int) int {
	keys := g.keyDays(w, week, skip)
	for d := max(from, 0); d < 7; d++ {
		i := 7*w + d
		s := week[d]
		if !g.open(i) || s.Type != "EASY" || g.locked[s.ID] || s.ChangedFrom != nil {
			continue
		}
		ok := true
		for _, k := range keys {
			if k != d && int(math.Abs(float64(k-d))) < g.p.HardSpacingDays {
				ok = false
			}
		}
		if ok {
			return d
		}
	}
	return -1
}

func (g *v4) moveInto(week []Session, d int, content Session, origin, reason string) {
	old := week[d]
	moved := content
	moved.ID, moved.Slot, moved.Date = old.ID, old.Slot, old.Date
	moved.Steps = append([]Step(nil), content.Steps...)
	moved.Reasons = append(append([]string(nil), content.Reasons...), reason)
	moved.ChangedFrom = &ChangedFrom{Date: origin, Type: old.Type, Focus: old.Focus, DurationS: old.DurationS, Reason: reason}
	moved.PaceSPerKM, moved.TargetHR, moved.DistanceM, moved.Outcome = nil, nil, 0, ""
	week[d] = moved
}

// adapt applies the in-week and day layers to the current week w. tmpl is the
// week's template, which supplies the content of a key session that an
// earlier day layer displaced from a day that is now in the past.
func (g *v4) adapt(w int, week, tmpl []Session, s *WeekSummary) {
	p := g.p
	// 1. Missed key sessions move to a later open easy day of this week. A key
	// session is identified by the day it was first planned on (its origin); it
	// is satisfied when any copy of it was run.
	type missed struct {
		origin  string
		content Session
	}
	satisfied := map[string]bool{}
	contents := map[string]Session{}
	origins := []string{}
	clean := func(x Session) Session {
		c := x
		c.ChangedFrom, c.Outcome = nil, ""
		c.Steps = append([]Step(nil), x.Steps...)
		c.Reasons = slices.DeleteFunc(append([]string(nil), x.Reasons...), func(r string) bool {
			return r == "missed_key_session_moved" || r == "daily_readiness_reschedule" || r == "session_recovery_reschedule"
		})
		return c
	}
	for d := 0; d < 7; d++ {
		i := 7*w + d
		x := week[d]
		if i >= g.today || g.locked[x.ID] {
			continue
		}
		var content Session
		origin := x.Date
		switch {
		case isKey(x.Type) && x.Type != "RACE":
			content = clean(x)
			if x.ChangedFrom != nil && x.ChangedFrom.Date != "" {
				origin = x.ChangedFrom.Date
			}
		case x.ChangedFrom != nil && isKey(x.ChangedFrom.Type) && x.ChangedFrom.Type != "RACE" &&
			(x.ChangedFrom.Reason == "daily_readiness_reschedule" || x.ChangedFrom.Reason == "session_recovery_reschedule") &&
			tmpl[d].Type == x.ChangedFrom.Type:
			// Displaced here by a day layer; its run on this day does not count.
			content = clean(tmpl[d])
			if _, seen := contents[origin]; !seen {
				origins = append(origins, origin)
				contents[origin] = content
			}
			continue
		default:
			continue
		}
		if _, seen := contents[origin]; !seen {
			origins = append(origins, origin)
			contents[origin] = content
		}
		satisfied[origin] = satisfied[origin] || g.done(i, x)
	}
	todo := []missed{}
	for _, o := range origins {
		if !satisfied[o] {
			todo = append(todo, missed{o, contents[o]})
		}
	}
	sort.SliceStable(todo, func(a, b int) bool {
		la, lb := todo[a].content.Type == "LONG", todo[b].content.Type == "LONG"
		if la != lb {
			return la
		}
		return todo[a].origin < todo[b].origin
	})
	for _, m := range todo {
		if d := g.place(w, week, g.today-7*w, -1); d >= 0 {
			g.moveInto(week, d, m.content, m.origin, "missed_key_session_moved")
			s.Reasons = append(s.Reasons, "missed_key_session_moved")
		} else {
			s.Reasons = append(s.Reasons, "missed_key_session_dropped")
		}
	}
	// 2. Extra volume comes out of the remaining easy runs.
	planned, actual := 0.0, 0.0
	for d := 0; d < 7; d++ {
		i := 7*w + d
		if g.open(i) {
			continue
		}
		actual += g.runS[i]
		if week[d].Type != "REST" && week[d].Type != "RACE" {
			planned += week[d].DurationS
		}
	}
	if need := actual - planned; need > p.ExtraVolumeS {
		remaining := 0.0
		for d := 0; d < 7; d++ {
			if g.open(7*w+d) && week[d].Type != "REST" && week[d].Type != "RACE" {
				remaining += week[d].DurationS
			}
		}
		changed := false
		for d := 6; d >= 0 && need > 0; d-- {
			x := week[d]
			if !g.open(7*w+d) || x.Type != "EASY" || g.locked[x.ID] || x.ChangedFrom != nil {
				continue
			}
			cut := floor60(min(x.DurationS-p.MinSessionS, need))
			if cut <= 0 {
				continue
			}
			nx := g.easy(x.Slot, x.DurationS-cut, x.Focus, "week_state_"+s.State, "extra_volume_rebalanced")
			nx.ChangedFrom = &ChangedFrom{Date: x.Date, Type: x.Type, Focus: x.Focus, DurationS: x.DurationS, Reason: "extra_volume_rebalanced"}
			week[d] = nx
			need -= cut
			remaining -= cut
			changed = true
		}
		if actual+remaining > s.BudgetS*p.OverBudget {
			for d := 6; d >= 0; d-- {
				x := week[d]
				if g.open(7*w+d) && x.Type == "EASY" && !g.locked[x.ID] && (x.ChangedFrom == nil || x.ChangedFrom.Reason == "extra_volume_rebalanced") {
					r := g.rest(x.Slot, "extra_volume_rebalanced")
					r.ChangedFrom = &ChangedFrom{Date: x.Date, Type: x.Type, Focus: x.Focus, DurationS: x.DurationS, Reason: "extra_volume_rebalanced"}
					if x.ChangedFrom != nil {
						r.ChangedFrom.DurationS = x.ChangedFrom.DurationS
					}
					week[d] = r
					changed = true
					break
				}
			}
		}
		if changed {
			s.Reasons = append(s.Reasons, "extra_volume_rebalanced")
		}
	}
	// 3. Today: low readiness or an acute load spike turns today's quality
	// into a recovery run (or rest when complete readiness is very low).
	td := g.today - 7*w
	if td >= 0 && td < 7 && g.open(g.today) && !g.locked[week[td].ID] {
		x := week[td]
		ratio := g.acwr()
		low := (g.sig.readiness != nil && *g.sig.readiness < p.ReadinessLow) || ratio > p.ACWRRecover
		rest := g.sig.readiness != nil && !g.sig.estimated && *g.sig.readiness < p.ReadinessRest
		if (rest && x.Type != "REST" && x.Type != "RACE") || (low && isKey(x.Type) && x.Type != "RACE") {
			if isKey(x.Type) {
				if d := g.place(w, week, td+1, td); d >= 0 {
					c := x
					c.ChangedFrom = nil
					g.moveInto(week, d, c, x.Date, "daily_readiness_reschedule")
				}
			}
			var nx Session
			if rest {
				nx = g.rest(x.Slot, "readiness_rest")
			} else {
				nx = g.easy(x.Slot, p.RecoverySessionS, "recovery", "daily_readiness_reschedule")
			}
			nx.ChangedFrom = &ChangedFrom{Date: x.Date, Type: x.Type, Focus: x.Focus, DurationS: x.DurationS, Reason: "daily_readiness_reschedule"}
			week[td] = nx
			s.Reasons = append(s.Reasons, "daily_readiness_reschedule")
		}
	}
	// 4. Session recovery: no quality session before the estimated ready day.
	if g.sig.readyAfter != nil {
		ready := time.UnixMicro(int64(*g.sig.readyAfter)).In(g.loc).Format(layout)
		ri, e := g.idx(ready)
		if e == nil && ri > g.today {
			for d := 0; d < 7; d++ {
				i := 7*w + d
				x := week[d]
				if i >= ri || !g.open(i) || g.locked[x.ID] || (x.Type != "TEMPO" && x.Type != "INTERVAL") {
					continue
				}
				if t := g.place(w, week, ri-7*w, d); t >= 0 {
					c := x
					c.ChangedFrom = nil
					g.moveInto(week, t, c, x.Date, "session_recovery_reschedule")
				}
				nx := g.easy(x.Slot, max(p.MinSessionS, floor60(x.DurationS*.75)), "aerobic_base", "session_recovery_pending")
				nx.ChangedFrom = &ChangedFrom{Date: x.Date, Type: x.Type, Focus: x.Focus, DurationS: x.DurationS, Reason: "session_recovery_reschedule"}
				week[d] = nx
				s.Reasons = append(s.Reasons, "session_recovery_pending")
			}
		}
	}
	s.Reasons = dedupe(s.Reasons)
}

func dedupe(xs []string) []string {
	out := []string{}
	for _, x := range xs {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

func (g *v4) plannedS(week []Session) float64 {
	sum := 0.0
	for _, s := range week {
		if s.Type != "REST" && s.Type != "RACE" {
			sum += s.DurationS
		}
	}
	return sum
}

func (g *v4) run() (Base, error) {
	p := g.p
	out := Base{Sessions: []Session{}, Recommendations: []string{}, AsOf: g.in.AsOf, Mode: "fitness"}
	ws := max(0, g.cur-p.WindowWeeks)
	out.WindowStart = g.date(7 * ws)
	var prev *WeekSummary
	weeks := []WeekSummary{}
	for w := ws; w <= g.cur+1; w++ {
		var s WeekSummary
		switch {
		case w < g.cur:
			if x, ok := g.prevWeeks[w]; ok {
				s = x
			} else {
				s = WeekSummary{Index: w, Start: g.date(7 * w), Phase: g.phase(w), State: "untracked", Reasons: []string{"week_state_untracked"}}
				if prev != nil {
					s.LevelS, s.PeakS = prev.LevelS, prev.PeakS
				}
			}
		case w == g.cur:
			if x, ok := g.prevWeeks[w]; ok && x.DecidedOn >= g.date(7*w) && x.ConfigRevision == g.in.ConfigRevision && x.Detail != "next" {
				s = x
				s.Reasons = slices.DeleteFunc(append([]string(nil), x.Reasons...), func(r string) bool {
					switch r {
					case "missed_key_session_moved", "missed_key_session_dropped", "extra_volume_rebalanced", "daily_readiness_reschedule", "session_recovery_pending":
						return true
					}
					return false
				})
			} else {
				s = g.decide(w, prev, g.today, false)
			}
		default:
			s = g.decide(w, prev, g.today, true)
		}
		tmpl := g.template(w, s)
		for d := range tmpl {
			// The eve of the race is a rest day even when it closes the week
			// before the race week.
			if i := 7*w + d; g.goal != nil && i == g.goal.idx-1 && tmpl[d].Type != "REST" {
				tmpl[d] = g.rest(i, "pre_race_rest")
			}
		}
		week := append([]Session(nil), tmpl...)
		for d := 0; d < 7; d++ {
			i := 7*w + d
			if i >= g.today {
				break
			}
			if x, ok := g.prevSessions[i]; ok {
				week[d] = x
			} else {
				week[d] = g.rest(i, "plan_not_started")
			}
		}
		switch {
		case w < g.cur:
			s.Detail = "past"
		case w == g.cur:
			s.Detail = "current"
			g.adapt(w, week, tmpl, &s)
		default:
			s.Detail = "next"
		}
		for d := range week {
			x := &week[d]
			i := 7*w + d
			if i >= g.today && x.Type != "REST" && x.Type != "RACE" && x.PaceSPerKM == nil && x.TargetHR == nil {
				var rp *float64
				if g.goal != nil {
					rp = g.goal.racePace
				}
				targetsV4(x, g.sig.paces, rp, g.sig.hrMax, g.sig.hrRest)
			}
			x.Outcome = ""
			if i < g.today || (i == g.today && g.runN[i] > 0) {
				switch {
				case x.Type == "REST" && g.runN[i] > 0:
					x.Outcome = "extra"
				case x.Type == "REST":
				case g.runN[i] > 0 && (x.DurationS <= 0 || g.runS[i] >= .8*x.DurationS):
					x.Outcome = "done"
				case g.runN[i] > 0:
					x.Outcome = "partial"
				case i < g.today:
					x.Outcome = "missed"
				}
			}
			if e := g.validate(*x); e != nil {
				return Base{}, e
			}
		}
		if w >= g.cur || s.PlannedS == 0 {
			s.PlannedS = g.plannedS(week)
		}
		if w <= g.cur {
			s.ActualS = Ptr(g.weekActual(w))
		}
		out.Sessions = append(out.Sessions, week...)
		weeks = append(weeks, s)
		prev = &weeks[len(weeks)-1]
	}
	// Outline weeks of a race plan: phase and projected volume to the end of
	// the post-race recovery.
	if g.goal != nil {
		last := min(g.goal.week+g.goal.post, g.cur+60)
		level, peak, streak := prev.LevelS, prev.PeakS, prev.ProgressStreak
		for w := g.cur + 2; w <= last; w++ {
			ph := g.phase(w)
			s := WeekSummary{Index: w, Start: g.date(7 * w), Detail: "outline", Phase: ph, State: "projected", Reasons: []string{"phase_" + ph}}
			budget := level
			switch ph {
			case "taper":
				budget = max(peak, level) * g.goal.taperF[g.goal.week-w-1]
			case "race":
				budget = max(peak, level) * .4
			case "post":
				budget = level * .5
			case "peak":
				streak = 0
			default:
				if streak >= p.ProgressStreak {
					budget, streak = level*p.RecoveryFactor, 0
				} else {
					level = min(level*(1+p.Progression), p.MaxWeekS)
					budget, streak = level, streak+1
				}
			}
			if ph == "base" || ph == "build" || ph == "peak" {
				peak = max(peak, budget)
			}
			s.LevelS, s.BudgetS, s.PeakS = round60(level), round60(max(budget, 2*p.MinSessionS)), round60(peak)
			s.PlannedS = s.BudgetS
			g.focusFor(w, &s)
			weeks = append(weeks, s)
		}
	}
	out.Weeks = weeks
	// Mode, goal status and plan-level explanations.
	curPhase := g.phase(g.cur)
	if g.goal != nil {
		st := g.goal.status
		st.Phase = curPhase
		if curPhase == "fitness" {
			st.Completed = true
			st.Reasons = append(st.Reasons, "goal_completed_auto_fitness")
		} else {
			out.Mode = "race"
			if curPhase == "pre" {
				st.Reasons = append(st.Reasons, "goal_prep_not_started")
			}
		}
		out.GoalStatus = &st
	}
	out.Recommendations = append(out.Recommendations, "mode_"+out.Mode)
	for _, s := range weeks {
		if s.Detail == "current" {
			out.Recommendations = append(out.Recommendations, s.Reasons...)
		}
	}
	if g.sig.readiness == nil {
		out.Recommendations = append(out.Recommendations, "readiness_unavailable")
	} else if g.sig.estimated {
		out.Recommendations = append(out.Recommendations, "load_only_advisory")
	}
	out.Recommendations = dedupe(append(out.Recommendations, g.recs...))
	return out, nil
}

func (g *v4) validate(s Session) error {
	if e := ValidateSession(s); e != nil {
		return e
	}
	if s.Origin == "generated" && s.Type != "REST" && s.Type != "RACE" && s.DurationS < g.p.MinSessionS {
		return ErrInvalid
	}
	return nil
}
