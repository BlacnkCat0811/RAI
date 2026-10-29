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
	fitness, fatigue, readiness := (*float64)(nil), (*float64)(nil), (*float64)(nil)
	for _, s := range in.Signals {
		supported := s.Version == "v1" || (s.Algorithm == "fitness_fatigue" && s.Version == "v2")
		if !supported || s.Value == nil || (s.Quality != "available" && s.Quality != "partial") || !Finite(*s.Value) {
			continue
		}
		switch s.Name {
		case "fitness":
			if s.Algorithm == "fitness_fatigue" {
				fitness = s.Value
			}
		case "fatigue":
			if s.Algorithm == "fitness_fatigue" {
				fatigue = s.Value
			}
		case "readiness":
			if s.Algorithm == "readiness" {
				readiness = s.Value
			}
		}
	}
	low := (readiness != nil && *readiness < p.LowReadiness) || (fitness != nil && fatigue != nil && *fitness > 0 && *fatigue > *fitness*1.5)
	if low {
		out.Recommendations = append(out.Recommendations, "load_only_advisory")
	}
	if readiness == nil {
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
		if week > 0 {
			weekly *= 1 + p.Progression
			reasons = append(reasons, "weekly_progression")
		}
		if week%4 == 3 {
			weekly /= 1 + p.Progression
			weekly *= p.Deload
			reasons = append(reasons, "recovery_week")
		}
		if week == 0 && low {
			weekly *= p.ReadinessFactor
			reasons = append(reasons, "readiness_adjustment")
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
					if low && week == 0 {
						s.Type = "RECOVERY"
						s.Effort = 2
						s.DurationS = 0
						s.Steps = nil
						s.Reasons = append(s.Reasons, "readiness_adjustment")
					}
				} else if day == (longDay+1)%7 || day == (hardDay+1)%7 && hardDay >= 0 {
					s.Type = "RECOVERY"
					s.Effort = 2
					s.Reasons = append(s.Reasons, "recovery_after_hard_session")
				}
			}
			if err := ValidateSession(s); err != nil {
				return Base{}, err
			}
			out.Sessions = append(out.Sessions, s)
		}
	}
	return out, nil
}
