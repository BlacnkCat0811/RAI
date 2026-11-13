package training

import (
	"fmt"
	"math"
	"slices"
)

// Workout library of training_plan/v4. A focus names the session within its
// type; levels 0..4 grow with weekly volume and the phase of a race plan.
// Every generated running session lasts at least MinSessionS, including the
// warm-up and cool-down of a structured one.

type workoutSpec struct {
	Type      string
	Effort    int
	DurationS float64
	Steps     []Step
}

var focusPurpose = map[string]string{
	"recovery": "promote_recovery", "aerobic_base": "aerobic_base", "strides": "neuromuscular_speed",
	"long_aerobic": "endurance", "long_race_pace": "race_specific_endurance", "tempo": "aerobic_capacity",
	"threshold": "lactate_threshold", "vo2max": "vo2max", "race_pace": "race_specific_speed", "race_wakeup": "race_sharpening",
	"goal_event": "goal_event",
}

// Heart-rate targets as a fraction of the heart-rate reserve and the pace
// zone of each focus. Race-pace work uses the race pace instead of a zone.
var focusTargets = map[string]struct {
	zone string
	hrr  float64
}{
	"recovery": {"z1", .62}, "aerobic_base": {"z2", .70}, "strides": {"z2", .70}, "long_aerobic": {"z2", .72},
	"long_race_pace": {"z2", .72}, "tempo": {"z3", .80}, "threshold": {"z4", .86}, "vo2max": {"z5", .92},
	"race_pace": {"", .86}, "race_wakeup": {"", .84},
}

func clampLevel(l int) int { return max(0, min(4, l)) }

// hardSpec builds a quality session. cat is the race category ("" without a
// goal); it shapes the race-pace work only.
func hardSpec(focus, cat string, lvl int) workoutSpec {
	lvl = clampLevel(lvl)
	warm := Step{Kind: "warmup", DurationS: 600, Effort: 2}
	cool := Step{Kind: "cooldown", DurationS: 600, Effort: 2}
	build := func(typ string, effort int, main ...Step) workoutSpec {
		steps := append(append([]Step{warm}, main...), cool)
		total := 0
		for _, s := range steps {
			if s.Kind == "repeat" {
				total += s.Repeats * (s.DurationS + s.RecoveryS)
			} else {
				total += s.DurationS
			}
		}
		return workoutSpec{Type: typ, Effort: effort, DurationS: float64(total), Steps: steps}
	}
	switch focus {
	case "tempo":
		work := []int{1200, 1500, 1800, 2100, 2400}[lvl]
		return build("TEMPO", 6, Step{Kind: "work", DurationS: work, Effort: 6})
	case "threshold":
		k := []int{3, 3, 4, 4, 5}[lvl]
		return build("TEMPO", 7, Step{Kind: "repeat", DurationS: 480, Effort: 7, Repeats: k, RecoveryS: 120})
	case "vo2max":
		v := [][2]int{{4, 180}, {5, 180}, {4, 240}, {5, 240}, {6, 240}}[lvl]
		return build("INTERVAL", 8, Step{Kind: "repeat", DurationS: v[1], Effort: 8, Repeats: v[0], RecoveryS: v[1]})
	case "race_wakeup":
		return build("INTERVAL", 6, Step{Kind: "repeat", DurationS: 120, Effort: 7, Repeats: 3, RecoveryS: 120})
	case "race_pace":
		switch cat {
		case "5k":
			k := []int{3, 4, 4, 5, 6}[lvl]
			return build("INTERVAL", 7, Step{Kind: "repeat", DurationS: 300, Effort: 7, Repeats: k, RecoveryS: 120})
		case "10k":
			k := []int{3, 3, 4, 4, 5}[lvl]
			return build("INTERVAL", 7, Step{Kind: "repeat", DurationS: 480, Effort: 7, Repeats: k, RecoveryS: 120})
		case "half":
			k := []int{2, 2, 3, 3, 3}[lvl]
			return build("TEMPO", 7, Step{Kind: "repeat", DurationS: 900, Effort: 7, Repeats: k, RecoveryS: 180})
		default:
			work := []int{1800, 2100, 2400, 2700, 3000}[lvl]
			return build("TEMPO", 6, Step{Kind: "work", DurationS: work, Effort: 6})
		}
	}
	return build("TEMPO", 6, Step{Kind: "work", DurationS: 1200, Effort: 6})
}

// stridesSteps puts 8 x 20 s fast strides with 60 s easy jogging at the end
// of an easy run of the given duration.
func stridesSteps(dur float64) []Step {
	d := int(dur)
	return []Step{
		{Kind: "warmup", DurationS: d - 8*80 - 300, Effort: 3},
		{Kind: "repeat", DurationS: 20, Effort: 8, Repeats: 8, RecoveryS: 60},
		{Kind: "cooldown", DurationS: 300, Effort: 2},
	}
}

// longRacePaceSteps places a race-pace block in the middle of a long run.
// Nil when the run is too short to hold a useful block.
func longRacePaceSteps(dur float64, cat string, lvl int) []Step {
	works := []int{900, 1200, 1500, 1800, 2400}
	if cat == "marathon" {
		works = []int{1200, 1800, 2400, 3000, 3600}
	}
	d := int(dur)
	work := min(works[clampLevel(lvl)], d-1800)
	if work < 600 {
		return nil
	}
	wu := (d - work) / 2
	cd := d - work - wu
	if wu > 7200 || cd > 7200 {
		return nil
	}
	return []Step{{Kind: "warmup", DurationS: wu, Effort: 3}, {Kind: "work", DurationS: work, Effort: 6}, {Kind: "cooldown", DurationS: cd, Effort: 3}}
}

// raceCategory maps a goal distance to the nearest prediction distance.
func raceCategory(m float64) (string, float64) {
	switch {
	case m <= 7000:
		return "5k", 5000
	case m <= 14000:
		return "10k", 10000
	case m <= 28000:
		return "half", 21097.5
	}
	return "marathon", 42195
}

// targetsV4 adds the pace and heart-rate target of a session's focus. Paces
// come only from schedulable running-fitness zones or the race pace of the
// goal assessment; a missing one is recorded, never replaced by another zone.
func targetsV4(s *Session, paces PaceTargets, racePace *float64, hrMax, hrRest *float64) {
	t, ok := focusTargets[s.Focus]
	if !ok {
		return
	}
	var pace *float64
	if t.zone == "" {
		if racePace != nil {
			pace = Ptr(math.Round(*racePace))
		}
	} else if v, ok := paces[t.zone]; ok && Finite(v) && v >= 120 && v <= 1800 {
		pace = Ptr(math.Round(v))
	}
	if pace != nil && *pace >= 120 && *pace <= 1800 {
		s.PaceSPerKM = pace
		if len(s.Steps) == 0 && s.DurationS > 0 {
			s.DistanceM = math.Round(s.DurationS / *pace * 100) * 10
		}
	} else if !slices.Contains(s.Reasons, "pace_zone_unavailable_hr_only") {
		s.Reasons = append(s.Reasons, "pace_zone_unavailable_hr_only")
	}
	if s.Focus == "long_race_pace" && racePace != nil {
		p := int(math.Round(*racePace))
		s.Notes = fmt.Sprintf("中段主训练按比赛配速 %d:%02d/公里，其余轻松跑。", p/60, p%60)
	}
	if hrMax != nil && hrRest != nil && Finite(*hrMax) && Finite(*hrRest) && *hrMax > *hrRest && *hrRest > 0 {
		s.TargetHR = Ptr(math.Round(*hrRest + t.hrr*(*hrMax-*hrRest)))
	}
}
