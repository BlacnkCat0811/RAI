package training

import (
	"math"
	"sort"
	"time"
)

func Generate(in Context) (Base, error) {
	out := Base{Sessions: []Session{}, Recommendations: []string{}}
	if in.Config.Validate(in.Timezone) != nil || in.Parameters != V1Parameters() {
		return out, ErrInvalid
	}
	loc, _ := time.LoadLocation(in.Timezone)
	start, _ := time.ParseInLocation("2006-01-02", in.Config.StartDate, loc)
	asOf, e := time.ParseInLocation("2006-01-02", in.AsOf, loc)
	if e != nil {
		return out, ErrInvalid
	}
	baseline, longest := 0.0, 0.0
	if in.Config.BaselineWeeklyM != nil {
		baseline = *in.Config.BaselineWeeklyM
	}
	if in.Config.PreviousLongM != nil {
		longest = *in.Config.PreviousLongM
	}
	var sum, observedLong float64
	count := 0
	for _, w := range in.Workouts {
		day, e := time.ParseInLocation("2006-01-02", w.Date, loc)
		if e != nil {
			return out, ErrInvalid
		}
		if day.Before(asOf.AddDate(0, 0, -27)) || day.After(asOf) || w.DistanceM == nil {
			continue
		}
		if !Finite(*w.DistanceM) || *w.DistanceM < 0 {
			return out, ErrInvalid
		}
		sum += *w.DistanceM
		observedLong = math.Max(observedLong, *w.DistanceM)
		count++
	}
	if count > 0 && sum/4 >= 3000 {
		if baseline == 0 {
			baseline = sum / 4
		} else {
			baseline = math.Min(baseline, sum/4)
		}
	}
	if observedLong > 0 {
		if longest == 0 {
			longest = observedLong
		} else {
			longest = math.Min(longest, observedLong)
		}
	}
	if baseline < 3000 {
		return out, ErrInsufficient
	}
	baseline = math.Min(baseline, 160000)
	if longest == 0 {
		longest = baseline * .25
		out.Recommendations = append(out.Recommendations, "cold_start_conservative")
	}
	p := in.Parameters
	var v2Readiness, acute, chronic, v60, hrMax, hrRest *float64
	v2 := in.ObservedModelBinding != nil && (in.ObservedModelBinding.Adapter == "schema2-v2" || in.ObservedModelBinding.Adapter == "schema3-v1")
	for _, s := range in.Signals {
		newAdapter := in.ObservedModelBinding != nil && in.ObservedModelBinding.Adapter == "schema3-v1"
		if s.Name == "readiness" && (newAdapter && s.Version != "v4" || !newAdapter && s.Version == "v4") {
			continue
		}
		// Only the v2 model stack feeds the plan; legacy signals were retired.
		if !v2 || s.Value == nil || !Finite(*s.Value) || (s.Version != "v2" && !(s.Name == "readiness" && (s.Version == "v3" || s.Version == "v4"))) {
			continue
		}
		// Load state values may be partial (imputed or short history);
		// readiness is consumed only when complete.
		if s.Quality != "complete" && !(s.Version == "v4" && s.Name == "readiness" && s.Mode == "estimated" && s.Quality == "estimated") && !(s.Quality == "partial" && s.Algorithm == "rai.load_state.exp_finite") {
			continue
		}
		switch {
		case s.Name == "readiness" && s.Algorithm == "rai.readiness.training":
			v2Readiness = s.Value
		case s.Name == "v60" && s.Algorithm == "rai.running_ability":
			v60 = s.Value
		case s.Name == "hr_max" && s.Algorithm == "hr_reference":
			hrMax = s.Value
		case s.Name == "hr_rest" && s.Algorithm == "hr_reference":
			hrRest = s.Value
		case s.Name == "acute" && s.Algorithm == "rai.load_state.exp_finite":
			acute = s.Value
		case s.Name == "chronic" && s.Algorithm == "rai.load_state.exp_finite":
			chronic = s.Value
		}
	}
	// Today's readiness (or an acute spike) reschedules only today's slot.
	lowToday := (v2Readiness != nil && *v2Readiness < p.LowReadiness) || (acute != nil && chronic != nil && *chronic > 0 && *acute > *chronic*1.5)
	if in.ObservedModelBinding != nil && in.ObservedModelBinding.Adapter == "schema3-v1" && v2Readiness != nil {
		// The continuous recovery scale uses a separate conservative threshold.
		// Estimated signals only take this downward path; they never add intensity.
		lowToday = *v2Readiness < 60 || (acute != nil && chronic != nil && *chronic > 0 && *acute > *chronic*1.5)
	}
	if lowToday {
		out.Recommendations = append(out.Recommendations, "load_only_advisory")
	}
	// Ramp guard: acute well above chronic holds next week's progression.
	ramp := v2 && acute != nil && chronic != nil && *chronic > 0 && *acute > *chronic*RampGuardRatio
	if ramp {
		out.Recommendations = append(out.Recommendations, "load_ramp_guard")
	}
	if v2Readiness == nil {
		out.Recommendations = append(out.Recommendations, "readiness_unavailable")
	}
	goalDay, _ := time.ParseInLocation("2006-01-02", in.Config.Goal.Date, loc)
	goalWeeks := 0
	for d := start; !d.After(goalDay); d = d.AddDate(0, 0, 7) {
		goalWeeks++
	}
	if in.Config.Goal.DistanceM > baseline*math.Pow(1+p.Progression, float64(goalWeeks-1))*.6 {
		out.Recommendations = append(out.Recommendations, "goal_risk")
	}
	if in.Config.Goal.TargetTimeS != nil {
		out.Recommendations = append(out.Recommendations, "target_time_not_optimized")
	}
	feedbackWeek := -1
	for _, f := range in.Feedback {
		if f.Status != "under_target" && f.Status != "over_target" && f.Status != "skipped" {
			continue
		}
		day, e := time.ParseInLocation("2006-01-02", f.Date, loc)
		if e == nil && !day.After(asOf) && !day.Before(asOf.AddDate(0, 0, -7)) {
			delta := 0
			for d := start; d.Before(asOf); d = d.AddDate(0, 0, 1) {
				delta++
			}
			feedbackWeek = delta / 7
			if feedbackWeek >= in.Config.Weeks {
				feedbackWeek = -1
			}
			break
		}
	}
	days := append([]int(nil), in.Config.AvailableDays...)
	sort.Ints(days)
	available := map[int]bool{}
	for _, d := range days {
		available[d] = true
	}
	longDay := in.Config.LongRunDay - 1
	hardDay := -1
	if baseline >= 20000 && len(days) >= 3 {
		for _, d := range days {
			n := d - 1
			distance := int(math.Abs(float64(n - longDay)))
			distance = min(distance, 7-distance)
			if distance >= p.HardSpacingDays {
				hardDay = n
				break
			}
		}
	}
	// build and longBuild carry the progression. A recovery week is cut from
	// them without replacing them, so the week after it returns to the level
	// reached before the cut and the plan keeps building instead of restarting
	// one deload lower every four weeks.
	build, longBuild := baseline, longest
	tempos := 0
	// The goal date is a day of the plan (Validate). The weeks before it taper:
	// volume comes down while the weekly quality session stays, so the runner
	// arrives rested but not detrained. A longer event tapers for longer.
	raceSlot := int(goalDay.Sub(start).Hours()/24 + .5)
	raceWeek, raceDay := raceSlot/7, raceSlot%7
	taper := []float64{}
	switch {
	case in.Config.Goal.DistanceM >= 30000:
		taper = []float64{.65, .8}
	case in.Config.Goal.DistanceM >= 15000:
		taper = []float64{.75}
	}
	for week := 0; week < in.Config.Weeks; week++ {
		reasons := []string{"weekly_load_constraint"}
		deload := week%4 == 3
		// phase: "" build, "taper" before the event week, "race" the event
		// week, "post" after it. None of the three moves the build level.
		phase, factor := "", 1.
		switch before := raceWeek - week; {
		case before < 0:
			phase, factor = "post", .5
		case before == 0:
			phase, factor = "race", .35
		case before <= len(taper):
			phase, factor = "taper", taper[before-1]
		}
		switch {
		case phase != "":
			deload = false
		case week == 0 || deload || week%4 == 0:
			// First week, the recovery week itself and the week that returns
			// from it hold the level already reached.
		case week == 1 && ramp:
			reasons = append(reasons, "load_ramp_guard")
		default:
			build *= 1 + p.Progression
			reasons = append(reasons, "weekly_progression")
		}
		if week == feedbackWeek {
			build *= p.FeedbackFactor
			reasons = append(reasons, "feedback_adjustment")
		}
		weekly, longCap := build, longBuild*(1+p.Progression)
		switch {
		case phase == "taper":
			weekly, longCap = build*factor, longBuild*factor
			reasons = append(reasons, "taper")
		case phase == "post":
			weekly, longCap = build*factor, 0
			reasons = append(reasons, "post_event_recovery")
		case phase == "race":
			weekly, longCap = build*factor, 0
			reasons = append(reasons, "taper")
		case deload:
			weekly, longCap = build*p.Deload, longBuild*p.Deload
			reasons = append(reasons, "recovery_week")
		case week > 0 && week%4 == 0:
			longCap = longBuild
		}
		longM := math.Floor(math.Min(math.Min(weekly*p.LongShare, longCap), math.Min(p.LongCeilingM, in.Config.Goal.DistanceM)))
		each := math.Min(p.MaxSessionM, math.Floor((weekly-longM)/float64(len(days)-1)))
		longM = math.Min(longM, math.Floor(each*float64(len(days)-1)*p.LongShare/(1-p.LongShare)))
		switch phase {
		case "post":
			// Easy running only, spread over every available day.
			each = math.Max(100, math.Floor(weekly/float64(len(days))))
		case "race":
			// The week's small budget goes to the available days before the
			// event; the event itself is not part of it.
			n := 0
			for _, d := range days {
				if d-1 < raceDay {
					n++
				}
			}
			each = math.Max(100, math.Min(each, math.Floor(weekly/float64(max(n, 1)))))
		case "":
			if !deload {
				build = longM + each*float64(len(days)-1)
				longBuild = longM
			}
		}
		for day := 0; day < 7; day++ {
			slot := week*7 + day
			s := Session{ID: StableID(in.PlanID, slot), Slot: slot, Date: start.AddDate(0, 0, slot).Format("2006-01-02"), Type: "REST", Origin: "generated", Status: "scheduled", Reasons: []string{"minimum_recovery"}}
			switch {
			case slot == raceSlot:
				// The event is scheduled on its date whether or not that is a
				// usual training day. Its pace comes only from a stated target.
				s.Type, s.DistanceM, s.Effort = "RACE", in.Config.Goal.DistanceM, 9
				s.Reasons = []string{"goal_event"}
				if t := in.Config.Goal.TargetTimeS; t != nil {
					if pace := math.Round(*t / (in.Config.Goal.DistanceM / 1000)); pace >= 120 && pace <= 1800 {
						s.PaceSPerKM = Ptr(pace)
					}
				}
			case slot == raceSlot+1:
				s.Reasons = []string{"post_event_recovery"}
			case phase == "race" || phase == "post":
				if available[day+1] {
					s.Type, s.DistanceM, s.Effort = "EASY", each, 3
					s.Reasons = append([]string{"aerobic_base"}, reasons...)
					if slot == raceSlot-1 || slot > raceSlot && slot <= raceSlot+7 {
						s.Type, s.Effort = "RECOVERY", 2
					}
				}
			case available[day+1]:
				s.Type = "EASY"
				s.DistanceM = each
				s.Effort = 3
				s.Reasons = append([]string{"aerobic_base"}, reasons...)
				if day == longDay {
					s.Type = "LONG"
					s.DistanceM = longM
					s.Reasons = append(s.Reasons, "long_run_progression")
				} else if day == hardDay {
					s.Type = "TEMPO"
					s.Effort = 6
					s.Reasons = append(s.Reasons, "goal_specific_tempo")
					// A tempo run is a block at tempo effort inside an easy run,
					// not the whole distance at tempo pace: 20 minutes at first,
					// five more with each later tempo session, 40 at most. The
					// pace and heart-rate targets describe the work block.
					work := min(2400, 1200+300*tempos)
					s.DurationS = float64(1200 + work)
					s.Steps = []Step{{Kind: "warmup", DurationS: 600, Effort: 2}, {Kind: "work", DurationS: work, Effort: 6}, {Kind: "cooldown", DurationS: 600, Effort: 2}}
					if week%2 == 1 {
						s.Type = "INTERVAL"
						s.Effort = 7
						s.DurationS = 2880
						s.Steps = []Step{{Kind: "warmup", DurationS: 600, Effort: 2}, {Kind: "repeat", DurationS: 300, Effort: 7, Repeats: 4, RecoveryS: 120}, {Kind: "cooldown", DurationS: 600, Effort: 2}}
						s.Reasons = append(s.Reasons, "goal_specific_interval")
					} else {
						tempos++
					}
				} else if day == (longDay+1)%7 || day == (hardDay+1)%7 && hardDay >= 0 {
					s.Type = "RECOVERY"
					s.Effort = 2
					s.Reasons = append(s.Reasons, "recovery_after_hard_session")
				}
			}
			if s.Type != "RACE" {
				setTargets(&s, v60, hrMax, hrRest)
			}
			if err := ValidateSession(s); err != nil {
				return Base{}, err
			}
			out.Sessions = append(out.Sessions, s)
		}
	}
	if lowToday {
		if e := rescheduleToday(out.Sessions, int(asOf.Sub(start).Hours()/24+.5), v60, hrMax, hrRest); e != nil {
			return Base{}, e
		}
	}
	return out, nil
}

func hardSession(t string) bool { return t == "TEMPO" || t == "INTERVAL" || t == "LONG" }

// rescheduleToday turns a hard session planned for today into recovery. Its
// content moves to the next EASY day of the same week whose following day is
// not hard; that EASY day's distance becomes today's recovery run. Slot ids,
// indices and dates stay put. Without such a day the hard content is dropped.
func rescheduleToday(sessions []Session, today int, v60, hrMax, hrRest *float64) error {
	if today < 0 || today >= len(sessions) || !hardSession(sessions[today].Type) {
		return nil
	}
	t := &sessions[today]
	recovery := t.DistanceM
	for j := today + 1; j < len(sessions) && j/7 == today/7; j++ {
		c := &sessions[j]
		if c.Type != "EASY" || (j+1 < len(sessions) && hardSession(sessions[j+1].Type)) {
			continue
		}
		recovery = c.DistanceM
		moved := *t
		moved.ID, moved.Slot, moved.Date = c.ID, c.Slot, c.Date
		moved.PaceSPerKM, moved.TargetHR = nil, nil
		moved.Reasons = append(append([]string(nil), t.Reasons...), "daily_readiness_reschedule")
		*c = moved
		setTargets(c, v60, hrMax, hrRest)
		if e := ValidateSession(*c); e != nil {
			return e
		}
		break
	}
	t.Type, t.Effort, t.DurationS, t.Steps, t.DistanceM = "RECOVERY", 2, 0, nil, recovery
	t.PaceSPerKM, t.TargetHR = nil, nil
	t.Reasons = append(append([]string(nil), t.Reasons...), "daily_readiness_reschedule")
	setTargets(t, v60, hrMax, hrRest)
	return ValidateSession(*t)
}

// RampGuardRatio: acute load this far above chronic holds the next week's
// progression (an ACWR-style guard on the v2 load state).
const RampGuardRatio = 1.3

// Target intensities per session type: speed as a fraction of the 60-minute
// ability v60 (pace zones E/T/I), and heart rate as a fraction of heart-rate
// reserve. Rest days get none.
var sessionTargets = map[string]struct{ speed, hrr float64 }{
	"EASY": {.74, .70}, "LONG": {.76, .72}, "RECOVERY": {.68, .62}, "TEMPO": {.97, .85}, "INTERVAL": {1.08, .90},
}

// setTargets adds a pace target from the running ability and a heart-rate
// target from the HR reference when they exist; it never invents either.
func setTargets(s *Session, v60, hrMax, hrRest *float64) {
	t, ok := sessionTargets[s.Type]
	if !ok {
		return
	}
	if v60 != nil && Finite(*v60) && *v60 > 0 {
		s.PaceSPerKM = Ptr(math.Round(1000 / (*v60 * t.speed)))
	}
	if hrMax != nil && hrRest != nil && Finite(*hrMax) && Finite(*hrRest) && *hrMax > *hrRest && *hrRest > 0 {
		s.TargetHR = Ptr(math.Round(*hrRest + t.hrr*(*hrMax-*hrRest)))
	}
}
