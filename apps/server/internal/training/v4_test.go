package training

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"slices"
	"testing"
	"time"
)

const v4Plan = "00000000-0000-4000-8000-0000000000a4"

func coachPtr() *CoachParameters { c := V3Parameters(); return &c }

func day(base string, add int) string {
	d, _ := time.Parse("2006-01-02", base)
	return d.AddDate(0, 0, add).Format("2006-01-02")
}

// history returns runs on the given ISO weekdays for the given number of weeks
// before anchor, each lasting minutes.
func history(anchor string, weeks int, weekdays []int, minutes float64) []Workout {
	out := []Workout{}
	for w := weeks; w >= 1; w-- {
		for _, wd := range weekdays {
			d := day(anchor, -7*w+wd-1)
			out = append(out, Workout{ID: fmt.Sprintf("h-%s", d), Date: d, DurationS: Ptr(minutes * 60)})
		}
	}
	return out
}

func v4Context(asOf string) Context {
	return Context{PlanID: v4Plan, Timezone: "Asia/Shanghai", AsOf: asOf, ConfigRevision: 1,
		Config:   Config{Version: ConfigVersionV2, StartDate: "2026-11-16", AvailableDays: []int{2, 4, 6, 7}, LongRunDay: 7},
		Coach:    coachPtr(),
		Workouts: history("2026-11-16", 8, []int{2, 4, 6, 7}, 50), Signals: []Signal{}, Feedback: []Feedback{}}
}

func checkV4(t *testing.T, b Base) {
	t.Helper()
	seen := map[string]bool{}
	for _, s := range b.Sessions {
		if e := ValidateSession(s); e != nil {
			t.Fatalf("invalid session %+v", s)
		}
		if s.Type != "REST" && s.Type != "RACE" && s.DurationS < 1800 {
			t.Fatalf("session below 30 minutes: %s %s %.0f", s.Date, s.Type, s.DurationS)
		}
		if s.ID != StableID(v4Plan, s.Slot) || seen[s.ID] {
			t.Fatalf("session identity %+v", s)
		}
		seen[s.ID] = true
	}
	if !slices.IsSortedFunc(b.Sessions, func(a, c Session) int { return a.Slot - c.Slot }) {
		t.Fatal("sessions out of order")
	}
}

func byDate(b Base, d string) Session {
	for _, s := range b.Sessions {
		if s.Date == d {
			return s
		}
	}
	return Session{}
}

func week(b Base, detail string) WeekSummary {
	for _, w := range b.Weeks {
		if w.Detail == detail {
			return w
		}
	}
	return WeekSummary{}
}

// next feeds a generation's output back as the next day's previous base.
func next(in Context, b Base, asOf string) Context {
	in.AsOf = asOf
	in.PreviousSessions = b.Sessions
	in.PreviousWeeks = b.Weeks
	return in
}

// simulate rolls a plan forward day by day. run decides how long the runner
// ran on each day given that day's planned session.
func simulate(t *testing.T, in Context, days int, run func(i int, s Session) float64) []Base {
	t.Helper()
	out := []Base{}
	for i := 0; i < days; i++ {
		asOf := day(in.Config.StartDate, i)
		in.AsOf = asOf
		b, e := Generate(in)
		if e != nil {
			t.Fatalf("day %d: %v", i, e)
		}
		checkV4(t, b)
		again, _ := Generate(in)
		if !reflect.DeepEqual(b, again) {
			t.Fatalf("day %d: not deterministic", i)
		}
		if len(out) > 0 {
			prev := out[len(out)-1]
			for _, s := range prev.Sessions {
				if s.Date < asOf && s.Date >= b.WindowStart {
					x := byDate(b, s.Date)
					x.Outcome, s.Outcome = "", ""
					if !reflect.DeepEqual(x, s) {
						t.Fatalf("day %d: past day %s changed\n%+v\n%+v", i, s.Date, s, x)
					}
				}
			}
		}
		out = append(out, b)
		if m := run(i, byDate(b, asOf)); m > 0 {
			in.Workouts = append(in.Workouts, Workout{ID: fmt.Sprintf("w-%s", asOf), Date: asOf, DurationS: Ptr(m)})
		}
		in = next(in, b, day(in.Config.StartDate, i+1))
	}
	return out
}

func asPlanned(_ int, s Session) float64 {
	if s.Type == "REST" {
		return 0
	}
	if s.Type == "RACE" {
		return 6000
	}
	return s.DurationS
}

func TestV4ConfigValidation(t *testing.T) {
	ok := v4Context("2026-11-16").Config
	if ok.Validate("Asia/Shanghai") != nil {
		t.Fatal("valid no-goal config rejected")
	}
	cases := map[string]func(c *Config){
		"tuesday":      func(c *Config) { c.StartDate = "2026-11-17" },
		"weeks":        func(c *Config) { c.Weeks = 8 },
		"one day":      func(c *Config) { c.AvailableDays = []int{7} },
		"long off":     func(c *Config) { c.LongRunDay = 1 },
		"half goal":    func(c *Config) { c.Goal.Date = "2027-01-01" },
		"goal past":    func(c *Config) { c.Goal = Goal{Type: "running", Date: "2026-11-01", DistanceM: 5000} },
		"goal far":     func(c *Config) { c.Goal = Goal{Type: "running", Date: "2029-01-01", DistanceM: 5000} },
		"meters field": func(c *Config) { c.BaselineWeeklyM = Ptr(30000) },
		"version":      func(c *Config) { c.Version = "training_config/v9" },
	}
	for name, f := range cases {
		c := ok
		c.AvailableDays = slices.Clone(ok.AvailableDays)
		f(&c)
		if c.Validate("Asia/Shanghai") == nil {
			t.Fatal(name)
		}
	}
	c := ok
	c.Goal = Goal{Type: "running", Date: "2027-03-21", DistanceM: 21097.5, TargetTimeS: Ptr(6300)}
	c.AvailableDays = []int{1, 2, 3, 4, 5, 6, 7}
	if c.Validate("Asia/Shanghai") != nil {
		t.Fatal("valid goal config rejected")
	}
	// A v2 config without a goal leaves the goal out of its JSON.
	b, _ := json.Marshal(ok)
	if string(b) != `{"version":"training_config/v2","startDate":"2026-11-16","availableDays":[2,4,6,7],"longRunDay":7}` {
		t.Fatal(string(b))
	}
}

func TestV4RejectsForeignParameters(t *testing.T) {
	in := v4Context("2026-11-16")
	in.Coach.MinSessionS = 900
	if _, e := Generate(in); e != ErrInvalid {
		t.Fatal("altered parameters accepted")
	}
	in = v4Context("2026-11-16")
	in.Coach = nil
	if _, e := Generate(in); e != ErrInvalid {
		t.Fatal("missing parameters accepted")
	}
	v3 := syntheticContext()
	v3.Coach = coachPtr()
	if _, e := Generate(v3); e != ErrInvalid {
		t.Fatal("v3 accepted v4 parameters")
	}
}

func TestV4FitnessWeekLayout(t *testing.T) {
	b, e := Generate(v4Context("2026-11-16"))
	if e != nil {
		t.Fatal(e)
	}
	checkV4(t, b)
	if b.Mode != "fitness" || b.GoalStatus != nil || b.WindowStart != "2026-11-16" || len(b.Sessions) != 14 {
		t.Fatalf("mode %s window %s n=%d", b.Mode, b.WindowStart, len(b.Sessions))
	}
	cur := week(b, "current")
	// 4 x 50 min a week of history: the first week starts at that level.
	if cur.State != "start" || cur.BudgetS != 12000 || cur.LowBase {
		t.Fatalf("%+v", cur)
	}
	sun := byDate(b, "2026-11-22")
	if sun.Type != "LONG" || sun.DurationS != 3600 {
		t.Fatalf("long %+v", sun)
	}
	hard := 0
	for _, s := range b.Sessions[:7] {
		if s.Type == "TEMPO" || s.Type == "INTERVAL" {
			hard++
		}
		if !slices.Contains([]string{"2026-11-17", "2026-11-19", "2026-11-21", "2026-11-22"}, s.Date) && s.Type != "REST" {
			t.Fatalf("run on unavailable day %+v", s)
		}
	}
	// 200 minutes over four days: two quality sessions, spaced from each
	// other and from the long run.
	if hard != 2 || byDate(b, "2026-11-17").Type == "EASY" || byDate(b, "2026-11-19").Type == "EASY" {
		t.Fatalf("hard sessions %d", hard)
	}
	if !slices.Contains(b.Recommendations, "readiness_unavailable") || !slices.Contains(b.Recommendations, "mode_fitness") {
		t.Fatal(b.Recommendations)
	}
}

func TestV4ColdStartAndLowBase(t *testing.T) {
	in := v4Context("2026-11-16")
	in.Workouts = nil
	b, e := Generate(in)
	if e != nil {
		t.Fatal(e)
	}
	checkV4(t, b)
	cur := week(b, "current")
	if cur.BudgetS != 3600 || len(cur.HardFocus) != 0 || !slices.Contains(cur.Reasons, "cold_start_conservative") {
		t.Fatalf("%+v", cur)
	}
	runs := 0
	for _, s := range b.Sessions[:7] {
		if s.Type != "REST" {
			runs++
			if s.DurationS != 1800 || s.Type != "EASY" {
				t.Fatalf("%+v", s)
			}
		}
	}
	if runs != 2 {
		t.Fatal(runs)
	}
	// A user-given baseline replaces the cold-start week.
	in.Config.BaselineWeeklyS = Ptr(9000)
	b, _ = Generate(in)
	if cur = week(b, "current"); cur.BudgetS != 9000 || !cur.LowBase == false && len(cur.HardFocus) != 1 {
		t.Fatalf("%+v", cur)
	}
	// No synced runs yet is not a break: next week previews from the
	// baseline instead of a return week.
	if nx := week(b, "next"); nx.State == "return" || nx.BudgetS < 9000 {
		t.Fatalf("preview %+v", nx)
	}
	// 20 minutes a week: one 30-minute run, never a shorter one.
	in = v4Context("2026-11-16")
	in.Workouts = history("2026-11-16", 4, []int{6}, 20)
	b, _ = Generate(in)
	checkV4(t, b)
	if cur = week(b, "current"); !cur.SingleSession || cur.BudgetS != 1800 {
		t.Fatalf("%+v", cur)
	}
}

func TestV4RollingSimulationFloorsFreezingAndProgress(t *testing.T) {
	bases := simulate(t, v4Context("2026-11-16"), 9*7, asPlanned)
	states := []string{}
	for i := 0; i < len(bases); i += 7 {
		states = append(states, week(bases[i], "current").State)
	}
	// Completed weeks progress three times, then a recovery week.
	want := []string{"start", "progress", "progress", "progress", "recover", "hold", "progress", "progress", "progress"}
	if !reflect.DeepEqual(states, want) {
		t.Fatalf("states %v", states)
	}
	b0, b3 := week(bases[7], "current"), week(bases[28], "current")
	if b3.BudgetS >= b0.BudgetS*1.25 || b3.LevelS <= b0.LevelS {
		t.Fatalf("progression %v -> %v", b0, b3)
	}
	// A week decision is frozen within its week.
	for i := 1; i < 7; i++ {
		a, b := week(bases[7], "current"), week(bases[7+i], "current")
		if a.BudgetS != b.BudgetS || a.State != b.State || a.DecidedOn != b.DecidedOn {
			t.Fatalf("week decision changed on day %d", i)
		}
	}
	// The window keeps four past weeks plus this and next week.
	last := bases[len(bases)-1]
	if len(last.Sessions) != 6*7 || len(last.Weeks) != 6 {
		t.Fatalf("window %d sessions %d weeks", len(last.Sessions), len(last.Weeks))
	}
}

func TestV4MissingWeekRegressesAndBreakReturns(t *testing.T) {
	// The runner runs only the long run: completion falls under 60 %.
	bases := simulate(t, v4Context("2026-11-16"), 14, func(i int, s Session) float64 {
		if s.Type == "LONG" {
			return s.DurationS
		}
		return 0
	})
	if s := week(bases[7], "current"); s.State != "regress" {
		t.Fatalf("%+v", s)
	}
	// Two weeks without running: a return week at 70 % of the level before.
	bases = simulate(t, v4Context("2026-11-16"), 22, func(i int, s Session) float64 {
		if i >= 6 {
			return 0
		}
		return asPlanned(i, s)
	})
	s := week(bases[21], "current")
	if s.State != "return" || len(s.HardFocus) != 0 {
		t.Fatalf("%+v", s)
	}
	for _, x := range bases[21].Sessions {
		if x.Date >= "2026-12-07" && x.Date < "2026-12-14" && (x.Type == "TEMPO" || x.Type == "INTERVAL" || x.Type == "LONG") {
			t.Fatalf("hard work in a return week %+v", x)
		}
	}
}

func TestV4MissedKeySessionMovesOnce(t *testing.T) {
	// 4 x 35 minutes: no long run yet and one quality session on Tuesday.
	in := v4Context("2026-11-16")
	in.Workouts = history("2026-11-16", 8, []int{2, 4, 6, 7}, 35)
	b, _ := Generate(in)
	// Find the week's quality day and miss it.
	var q Session
	for _, s := range b.Sessions[:7] {
		if s.Type == "TEMPO" || s.Type == "INTERVAL" {
			q = s
		}
	}
	if q.ID == "" {
		t.Fatal("no quality session")
	}
	qi := slices.IndexFunc(b.Sessions, func(s Session) bool { return s.ID == q.ID })
	in = next(in, b, day(q.Date, 1))
	moved, e := Generate(in)
	if e != nil {
		t.Fatal(e)
	}
	checkV4(t, moved)
	if moved.Sessions[qi].Outcome != "missed" {
		t.Fatalf("%+v", moved.Sessions[qi])
	}
	n := 0
	for _, s := range moved.Sessions[:7] {
		if s.ChangedFrom != nil && s.ChangedFrom.Reason == "missed_key_session_moved" {
			n++
			if s.Date <= q.Date || s.Focus != q.Focus || s.ChangedFrom.Date != q.Date || s.ChangedFrom.Type != "EASY" {
				t.Fatalf("%+v", s)
			}
		}
	}
	if n != 1 || !slices.Contains(week(moved, "current").Reasons, "missed_key_session_moved") {
		t.Fatalf("moved %d %v", n, week(moved, "current").Reasons)
	}
	again, _ := Generate(in)
	if !reflect.DeepEqual(moved, again) {
		t.Fatal("not stable")
	}
}

func TestV4ExtraVolumeIsRebalanced(t *testing.T) {
	b, _ := Generate(v4Context("2026-11-16"))
	in := next(v4Context("2026-11-16"), b, "2026-11-18")
	// A 100-minute run on the planned Tuesday, far beyond its prescription.
	in.Workouts = append(in.Workouts, Workout{ID: "x", Date: "2026-11-17", DurationS: Ptr(6000)})
	out, e := Generate(in)
	if e != nil {
		t.Fatal(e)
	}
	checkV4(t, out)
	if !slices.Contains(week(out, "current").Reasons, "extra_volume_rebalanced") {
		t.Fatal(week(out, "current").Reasons)
	}
	for _, s := range out.Sessions[:7] {
		if s.Date > "2026-11-17" && s.Type == "EASY" && s.DurationS < 1800 {
			t.Fatal("cut below floor")
		}
	}
}

func TestV4LowReadinessMovesTodaysQuality(t *testing.T) {
	b, _ := Generate(v4Context("2026-11-16"))
	var q Session
	for _, s := range b.Sessions[:7] {
		if s.Type == "TEMPO" || s.Type == "INTERVAL" {
			q = s
		}
	}
	in := next(v4Context("2026-11-16"), b, q.Date)
	in.ObservedModelBinding = &ModelBinding{Adapter: "schema4-v1"}
	in.Signals = []Signal{{Name: "readiness", Algorithm: "rai.readiness.training", Version: "v4", Quality: "complete", Mode: "complete", Value: Ptr(45)}}
	out, e := Generate(in)
	if e != nil {
		t.Fatal(e)
	}
	checkV4(t, out)
	today := byDate(out, q.Date)
	if today.Type != "RECOVERY" || today.ChangedFrom == nil || today.ChangedFrom.Type != q.Type {
		t.Fatalf("%+v", today)
	}
	// Very low complete readiness: rest instead.
	in.Signals[0].Value = Ptr(30)
	out, _ = Generate(in)
	if s := byDate(out, q.Date); s.Type != "REST" || !slices.Contains(s.Reasons, "readiness_rest") {
		t.Fatalf("%+v", s)
	}
	// Estimated readiness only downshifts; it never imposes rest.
	in.Signals[0] = Signal{Name: "readiness", Algorithm: "rai.readiness.training", Version: "v4", Quality: "estimated", Mode: "estimated", Value: Ptr(30)}
	out, _ = Generate(in)
	if s := byDate(out, q.Date); s.Type != "RECOVERY" {
		t.Fatalf("%+v", s)
	}
	// Next day the displaced session is still owed and moves again.
	in.Signals = nil
	in = next(in, out, day(q.Date, 1))
	later, e := Generate(in)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	for _, s := range later.Sessions[:7] {
		if s.Date > q.Date && s.Focus == q.Focus && s.ChangedFrom != nil {
			found = true
		}
	}
	if !found && !slices.Contains(week(later, "current").Reasons, "missed_key_session_dropped") {
		t.Fatal("displaced session lost")
	}
}

func TestV4SessionRecoveryKeepsQualityOffLoadedDays(t *testing.T) {
	b, _ := Generate(v4Context("2026-11-16"))
	var q Session
	for _, s := range b.Sessions[:7] {
		if s.Type == "TEMPO" || s.Type == "INTERVAL" {
			q = s
		}
	}
	in := next(v4Context("2026-11-16"), b, "2026-11-16")
	in.ObservedModelBinding = &ModelBinding{Adapter: "schema4-v1"}
	loc, _ := time.LoadLocation("Asia/Shanghai")
	ready, _ := time.ParseInLocation("2006-01-02", day(q.Date, 1), loc)
	in.Signals = []Signal{{Name: "ready_after", Algorithm: SessionRecoveryAlgorithm, Version: "v1", Quality: "complete", Value: Ptr(float64(ready.UnixMicro()))}}
	out, e := Generate(in)
	if e != nil {
		t.Fatal(e)
	}
	checkV4(t, out)
	if s := byDate(out, q.Date); s.Type == "TEMPO" || s.Type == "INTERVAL" || !slices.Contains(s.Reasons, "session_recovery_pending") {
		t.Fatalf("%+v", s)
	}
}

func raceContext(asOf, date string, km float64) Context {
	in := v4Context(asOf)
	in.Config.Goal = Goal{Type: "running", Date: date, DistanceM: km * 1000}
	in.Config.AvailableDays = []int{2, 3, 4, 6, 7}
	in.Workouts = history("2026-11-16", 8, []int{2, 3, 4, 6, 7}, 55)
	return in
}

func TestV4RacePlanPhasesTaperAndRace(t *testing.T) {
	// A half marathon 12 weeks out on a Sunday.
	in := raceContext("2026-11-16", "2027-02-07", 21.0975)
	b, e := Generate(in)
	if e != nil {
		t.Fatal(e)
	}
	checkV4(t, b)
	if b.Mode != "race" || b.GoalStatus == nil || b.GoalStatus.Category != "half" || b.GoalStatus.Assessment != "finish" {
		t.Fatalf("%+v", b.GoalStatus)
	}
	phases := []string{}
	for _, w := range b.Weeks {
		phases = append(phases, w.Phase)
	}
	want := []string{"base", "build", "build", "build", "build", "build", "build", "peak", "peak", "peak", "taper", "race", "post", "post"}
	if !reflect.DeepEqual(phases, want) {
		t.Fatalf("%v", phases)
	}
	bases := simulate(t, in, 15*7, asPlanned)
	race := byDate(bases[11*7], "2027-02-07")
	if race.Type != "RACE" || race.DistanceM != 21097.5 {
		t.Fatalf("%+v", race)
	}
	if eve := byDate(bases[11*7], "2027-02-06"); eve.Type != "REST" {
		t.Fatalf("eve %+v", eve)
	}
	peak, taper, raceWeek := week(bases[9*7], "current"), week(bases[10*7], "current"), week(bases[11*7], "current")
	if w8 := week(bases[8*7], "current"); w8.State != "hold" || w8.LevelS != peak.LevelS {
		t.Fatalf("peak weeks must hold: %v %v", w8.LevelS, peak.LevelS)
	}
	if taper.BudgetS >= peak.BudgetS || raceWeek.BudgetS >= taper.BudgetS || taper.State != "taper" {
		t.Fatalf("taper %v %v %v", peak.BudgetS, taper.BudgetS, raceWeek.BudgetS)
	}
	// Peak long runs carry a race-pace block.
	if l := byDate(bases[9*7], "2027-01-24"); l.Type != "LONG" || l.Focus != "long_race_pace" || len(l.Steps) != 3 {
		t.Fatalf("%+v", l)
	}
	// After the race and its recovery the plan returns to fitness mode.
	after := bases[len(bases)-1]
	if after.Mode != "fitness" || !after.GoalStatus.Completed {
		t.Fatalf("%s %+v", after.Mode, after.GoalStatus)
	}
}

func TestV4GoalAssessmentUsesPredictionAndCapsPace(t *testing.T) {
	in := raceContext("2026-11-16", "2027-01-17", 10)
	in.Config.Goal.TargetTimeS = Ptr(2700)
	in.ObservedModelBinding = &ModelBinding{Adapter: "schema4-v1"}
	pred := func(v float64) []Signal {
		return []Signal{{Name: "predict_10k", Algorithm: PaceAlgorithm, Version: "v1", Quality: "complete", Value: Ptr(v)},
			{Name: "pace_z4", Algorithm: PaceAlgorithm, Version: "v1", Quality: "complete", Value: Ptr(290)}}
	}
	for _, c := range []struct {
		pred   float64
		want   string
		paceKM float64
	}{{2750, "on_track", 270}, {2900, "ambitious", 270}, {3200, "out_of_reach", 320}} {
		in.Signals = pred(c.pred)
		b, e := Generate(in)
		if e != nil {
			t.Fatal(e)
		}
		if b.GoalStatus.Assessment != c.want || *b.GoalStatus.RacePaceSPerKM != c.paceKM {
			t.Fatalf("%v: %+v", c, b.GoalStatus)
		}
	}
	in.Signals = nil
	b, _ := Generate(in)
	if b.GoalStatus.Assessment != "unknown" || b.GoalStatus.RacePaceSPerKM != nil {
		t.Fatalf("%+v", b.GoalStatus)
	}
}

func TestV4ShortNoticeAndPrepNotStarted(t *testing.T) {
	b, e := Generate(raceContext("2026-11-16", "2026-12-06", 42.195))
	if e != nil {
		t.Fatal(e)
	}
	checkV4(t, b)
	if !slices.Contains(b.GoalStatus.Reasons, "goal_short_notice") {
		t.Fatal(b.GoalStatus.Reasons)
	}
	b, _ = Generate(raceContext("2026-11-16", "2027-10-10", 42.195))
	if b.GoalStatus.Phase != "pre" || !slices.Contains(b.GoalStatus.Reasons, "goal_prep_not_started") || b.Mode != "race" {
		t.Fatalf("%+v", b.GoalStatus)
	}
}

func TestV4FloorPropertyAcrossConfigurations(t *testing.T) {
	daySets := [][]int{{6, 7}, {2, 7}, {1, 3, 5}, {2, 4, 6, 7}, {1, 2, 3, 4, 5, 6}, {1, 2, 3, 4, 5, 6, 7}}
	levels := []float64{0, 15, 30, 60, 120}
	goals := []Goal{{}, {Type: "running", Date: "2026-12-13", DistanceM: 5000}, {Type: "running", Date: "2027-01-31", DistanceM: 42195}}
	for _, ds := range daySets {
		for _, lv := range levels {
			for _, gl := range goals {
				in := v4Context("2026-11-16")
				in.Config.AvailableDays, in.Config.LongRunDay, in.Config.Goal = ds, ds[len(ds)-1], gl
				in.Workouts = nil
				if lv > 0 {
					in.Workouts = history("2026-11-16", 6, ds, lv)
				}
				simulate(t, in, 5*7, func(i int, s Session) float64 {
					if i%5 == 3 {
						return 0
					}
					return asPlanned(i, s) * 1.3
				})
			}
		}
	}
}

func TestV4MergeDropsOverridesThatLeftTheWindow(t *testing.T) {
	b, _ := Generate(v4Context("2026-11-16"))
	s := b.Sessions[1]
	o := Override{ID: "00000000-0000-4000-8000-0000000000b1", SessionID: s.ID, Operation: "edit", Path: "notes", Value: json.RawMessage(`"x"`), Revision: 1, Anchor: &s}
	b.WindowStart = "2026-12-01"
	b.Sessions = b.Sessions[7:]
	m, e := ResolveEffective(b, []Override{o})
	if e != nil {
		t.Fatal(e)
	}
	for _, x := range m.Sessions {
		if x.ID == s.ID {
			t.Fatal("aged-out session resurrected")
		}
	}
}

// Both revisions must match the frozen rows the database will actually load.
func TestV4MigrationParametersMatch(t *testing.T) {
	for _, tc := range []struct {
		file string
		want CoachParameters
	}{
		{"00060_training_plan_v4.sql", V2Parameters()},
		{"00061_training_baseline_parameters.sql", V3Parameters()},
	} {
		t.Run(tc.want.Version, func(t *testing.T) {
			b, e := os.ReadFile("../../migrations/" + tc.file)
			if e != nil {
				t.Fatal(e)
			}
			m := regexp.MustCompile(`'` + regexp.QuoteMeta(tc.want.Version) + `','(\{[^']*\})'`).FindSubmatch(b)
			if m == nil {
				t.Fatal("parameters not found")
			}
			dec := json.NewDecoder(bytes.NewReader(m[1]))
			dec.DisallowUnknownFields()
			var c CoachParameters
			if e = dec.Decode(&c); e != nil || c != tc.want {
				t.Fatalf("migration parameters differ: %v", e)
			}
		})
	}
}

// Captured from cce810c, before the baseline change. These hashes pin both the
// old JSON context and complete generated output, including return-after-break.
func TestV4FrozenV2Replay(t *testing.T) {
	for _, tc := range []struct{ name, input, output string }{
		{"steady", "6961f8570381e7fca2c85a74961ec16159a5f535b1ee432c9257c723010bca8c", "fbafdf214ac774961b730adf3878106d2533a5b58a1250f9bec68ff6c9549883"},
		{"irregular", "fbbd0d79f91c6b80afd40210c838acffba72a4b1a2c75b951dab43004f76b489", "4ac1d7b84e253def6b83075633a461e2726424810cf8cbe964268f6800f501cd"},
		{"return", "7fa1848a39c49bfc34638828546e2d0e5e3d8a6cea056d0fd9c94f298780de87", "b053c52fb24cfaaeacdb946dd6fca25f976318eda8e1369ed7cda574ad4df955"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := v4Context("2026-11-16")
			p := V2Parameters()
			in.Coach = &p
			if tc.name == "irregular" {
				in.Workouts = append(history("2026-11-16", 1, []int{2, 4}, 50), history("2026-11-16", 8, []int{7}, 40)...)
			}
			if tc.name == "return" {
				in.Workouts = history("2026-10-26", 4, []int{2, 4, 7}, 50)
			}
			b, e := Generate(in)
			if e != nil {
				t.Fatal(e)
			}
			ih, _ := Hash(in)
			oh, _ := Hash(b)
			if fmt.Sprintf("%x", ih) != tc.input || fmt.Sprintf("%x", oh) != tc.output {
				t.Fatalf("frozen replay changed: input=%x output=%x", ih, oh)
			}
		})
	}
}

func TestV4ActiveWeekBaseline(t *testing.T) {
	for _, tc := range []struct {
		name  string
		weeks map[int]float64
		want  float64
	}{
		{"even median ignores breaks and partial oldest week", map[int]float64{-9: 30000, -8: 6000, -6: 9000, -3: 12000, -1: 24000}, 10500},
		{"odd median", map[int]float64{-8: 6000, -3: 12000, -1: 24000}, 12000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := v4Context("2026-11-16")
			in.Workouts = nil
			for w, seconds := range tc.weeks {
				in.Workouts = append(in.Workouts, Workout{ID: fmt.Sprint(w), Date: day(in.Config.StartDate, w*7+1), DurationS: Ptr(seconds)})
			}
			// A run in this week must not inflate the baseline from full weeks.
			in.Workouts = append(in.Workouts, Workout{ID: "today", Date: in.AsOf, DurationS: Ptr(30000)})
			out, e := Generate(in)
			if e != nil {
				t.Fatal(e)
			}
			if got := week(out, "current").LevelS; got != tc.want {
				t.Fatalf("baseline %.0f, want %.0f", got, tc.want)
			}
		})
	}
	in := v4Context("2026-11-16")
	in.Workouts = history("2026-11-02", 1, []int{2, 4, 7}, 60)
	b, e := Generate(in)
	if e != nil {
		t.Fatal(e)
	}
	if w := week(b, "current"); w.State != "return" || w.LevelS != 7560 {
		t.Fatalf("return must use 70%% of the active-week median: %+v", w)
	}
}
