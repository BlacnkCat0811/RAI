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
	v2 := in.ObservedModelBinding != nil && in.ObservedModelBinding.Adapter == "schema2-v2"
	for _, s := range in.Signals {
		// Only the v2 model stack feeds the plan; legacy signals were retired.
		if !v2 || s.Value == nil || !Finite(*s.Value) || s.Version != "v2" {
			continue
		}
		// Load state values may be partial (imputed or short history);
		// readiness is consumed only when complete.
		if s.Quality != "complete" && !(s.Quality == "partial" && s.Algorithm == "rai.load_state.exp_finite") {
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
	weekly := baseline
	for week := 0; week < in.Config.Weeks; week++ {
		reasons := []string{"weekly_load_constraint"}
		if week > 0 && !(week == 1 && ramp) {
			weekly *= 1 + p.Progression
			reasons = append(reasons, "weekly_progression")
		} else if week == 1 {
			reasons = append(reasons, "load_ramp_guard")
		}
		if week%4 == 3 {
			weekly /= 1 + p.Progression
			weekly *= p.Deload
			reasons = append(reasons, "recovery_week")
		}
		if week == feedbackWeek {
			weekly *= p.FeedbackFactor
			reasons = append(reasons, "feedback_adjustment")
		}
		longM := math.Floor(math.Min(math.Min(weekly*p.LongShare, longest*(1+p.Progression)), math.Min(p.LongCeilingM, in.Config.Goal.DistanceM)))
		each := math.Min(p.MaxSessionM, math.Floor((weekly-longM)/float64(len(days)-1)))
		longM = math.Min(longM, math.Floor(each*float64(len(days)-1)*p.LongShare/(1-p.LongShare)))
		weekly = longM + each*float64(len(days)-1)
		longest = longM
		for day := 0; day < 7; day++ {
			slot := week*7 + day
			s := Session{ID: StableID(in.PlanID, slot), Slot: slot, Date: start.AddDate(0, 0, slot).Format("2006-01-02"), Type: "REST", Origin: "generated", Status: "scheduled", Reasons: []string{"minimum_recovery"}}
			if available[day+1] {
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
					if week%2 == 1 {
						s.Type = "INTERVAL"
						s.Effort = 7
						s.DurationS = 2880
						s.Steps = []Step{{Kind: "warmup", DurationS: 600, Effort: 2}, {Kind: "repeat", DurationS: 300, Effort: 7, Repeats: 4, RecoveryS: 120}, {Kind: "cooldown", DurationS: 600, Effort: 2}}
						s.Reasons = append(s.Reasons, "goal_specific_interval")
					}
				} else if day == (longDay+1)%7 || day == (hardDay+1)%7 && hardDay >= 0 {
					s.Type = "RECOVERY"
					s.Effort = 2
					s.Reasons = append(s.Reasons, "recovery_after_hard_session")
				}
			}
			setTargets(&s, v60, hrMax, hrRest)
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
