package runfitness

import "math"

// Prediction is one target distance under flat, mild, well-paced conditions.
type Prediction struct {
	Distance       string   `json:"distance"` // 5k | 10k | half | marathon
	DistanceM      float64  `json:"distanceM"`
	Seconds        *float64 `json:"seconds"`
	Range          *Range   `json:"range"`
	Support        string   `json:"predictionSupport"` // direct | calibrated | prior_based | insufficient
	Status         string   `json:"status"`            // available | unsupported | conflict
	TargetEligible bool     `json:"targetEligible"`
	LongRunCovered *bool    `json:"longRunCovered,omitempty"`
	Conditions     string   `json:"conditions"`
	Reasons        []string `json:"reasonCodes"`
}

// RacePace is the average pace of a prediction; schedulable only when the
// prediction is direct or calibrated.
type RacePace struct {
	Distance    string   `json:"distance"`
	PaceSecKm   *float64 `json:"paceSecondsPerKm"`
	Range       *Range   `json:"paceRange"`
	Support     string   `json:"support"`
	Schedulable bool     `json:"schedulable"`
}

type Zone struct {
	Name        string   `json:"name"`
	Label       string   `json:"label"`
	FastPace    *float64 `json:"fastPaceSecondsPerKm"`
	SlowPace    *float64 `json:"slowPaceSecondsPerKm"`
	FastSpeed   *float64 `json:"fastSpeedMPS"`
	SlowSpeed   *float64 `json:"slowSpeedMPS"`
	FastRange   *Range   `json:"fastBoundRange,omitempty"`
	OpenEnded   bool     `json:"openEnded,omitempty"`
	Basis       string   `json:"basis"`
	Support     string   `json:"support"` // supported | estimated | limited | prior_based | insufficient_evidence | conflict
	Schedulable bool     `json:"schedulable"`
	Reasons     []string `json:"reasonCodes"`
}

type Zones struct {
	Zones      []Zone   `json:"zones"`
	ValidUntil string   `json:"validUntil,omitempty"`
	Reasons    []string `json:"reasonCodes"`
}

var targets = []struct {
	name     string
	meters   float64
	lo, hi   float64
	coverage float64
}{{"5k", 5000, 4000, 6000, 0}, {"10k", 10000, 8000, 12000, 0}, {"half", 21097.5, 18000, 24000, .6}, {"marathon", 42195, 38000, 46000, .5}}

func predictions(p Policy, c Curve, corners []Curve, obs []SingleResult, ageOf map[string]int, st *State, bLCal, vTCal, v6Direct bool) []Prediction {
	out := []Prediction{}
	for _, tg := range targets {
		pr := Prediction{Distance: tg.name, DistanceM: tg.meters, Status: "available", Support: "prior_based", Conditions: "flat_course_mild_weather_even_pacing_fuelled", Reasons: []string{}}
		sec, ok := c.TimeFor(tg.meters)
		if !ok {
			pr.Status, pr.Support = "unsupported", "insufficient"
			pr.Reasons = append(pr.Reasons, "outside_curve_domain")
			out = append(out, pr)
			continue
		}
		pr.Seconds = ptr(sec)
		var xs []float64
		for _, cc := range corners {
			if t, ok := cc.TimeFor(tg.meters); ok {
				xs = append(xs, t)
			}
		}
		pr.Range = spanOf(append(xs, sec))
		direct := false
		var latestRace *RaceSingle
		for _, o := range obs {
			if age, ok := ageOf[o.WorkoutID]; ok && age < p.RaceWindow && o.Race != nil && o.Race.DistanceM >= tg.lo && o.Race.DistanceM <= tg.hi {
				direct = true
				latestRace = o.Race
			}
		}
		switch {
		case direct:
			pr.Support = "direct"
		case tg.name == "5k" && (v6Direct || vTCal):
			pr.Support = "calibrated"
		case tg.name == "10k" && vTCal:
			pr.Support = "calibrated"
		case (tg.name == "half" || tg.name == "marathon") && bLCal:
			pr.Support = "calibrated"
		}
		if pr.Support == "prior_based" {
			switch tg.name {
			case "5k":
				pr.Reasons = append(pr.Reasons, "speed_shape_prior")
			case "10k":
				pr.Reasons = append(pr.Reasons, "threshold_prior")
			default:
				pr.Reasons = append(pr.Reasons, "endurance_prior")
			}
		}
		if latestRace != nil {
			implied := latestRace.Seconds * math.Pow(tg.meters/latestRace.DistanceM, c.BL)
			if math.Abs(implied/sec-1) > .05 {
				pr.Reasons = append(pr.Reasons, "race_prediction_conflict")
			}
		}
		if tg.coverage > 0 {
			covered := st.Preparation.LongestSeconds >= tg.coverage*sec
			pr.LongRunCovered = &covered
			if !covered {
				pr.Reasons = append(pr.Reasons, "long_run_coverage_low")
			}
		}
		switch tg.name {
		case "marathon":
			pr.TargetEligible = st.Preparation.LongRuns >= 2 && (bLCal || direct) && st.Status != "conflict"
		default:
			pr.TargetEligible = pr.Support == "direct" || pr.Support == "calibrated"
		}
		out = append(out, pr)
	}
	return out
}

func racePaces(ps []Prediction) []RacePace {
	out := []RacePace{}
	for _, p := range ps {
		rp := RacePace{Distance: p.Distance, Support: p.Support}
		if p.Seconds != nil {
			rp.PaceSecKm = ptr(*p.Seconds / p.DistanceM * 1000)
			if p.Range != nil {
				rp.Range = &Range{p.Range.Low / p.DistanceM * 1000, p.Range.High / p.DistanceM * 1000}
			}
			rp.Schedulable = p.Support == "direct" || p.Support == "calibrated"
		}
		out = append(out, rp)
	}
	return out
}

var zoneLabels = []string{"recovery", "aerobic_endurance", "aerobic_power", "threshold", "anaerobic_endurance", "anaerobic_power"}

func emptyZones() Zones {
	z := Zones{Reasons: []string{}}
	for i, l := range zoneLabels {
		z.Zones = append(z.Zones, Zone{Name: "Z" + string(rune('1'+i)), Label: l, Support: "insufficient_evidence", Reasons: []string{}})
	}
	return z
}

// buildZones derives the six pace zones from long-term physical anchors.
func buildZones(p Policy, st *State, a float64, c Curve, bSCal, v6Direct bool) Zones {
	z := emptyZones()
	z.ValidUntil = st.ValidUntil
	V := func(r float64) float64 { return a * math.Pow(r/RefIntensity, p.Beta) }
	bS := impliedBS(c)
	b := []float64{V(.40), V(.55), V(.70), .95 * c.VT, 1.03 * c.VT, c.V6 * math.Pow(.5, 1/bS-1)}
	var b6 *float64 = st.Params.V90s
	fresh := st.Freshness == "fresh" || st.Freshness == "aging"
	set := func(i int, lo, hi float64, open bool) {
		zz := &z.Zones[i]
		zz.SlowSpeed, zz.SlowPace = ptr(lo), ptr(Pace(lo))
		if !open {
			zz.FastSpeed, zz.FastPace = ptr(hi), ptr(Pace(hi))
		}
		zz.OpenEnded = open
	}
	// Z1/Z2 rely on A.
	for i := 0; i < 2; i++ {
		set(i, b[i], b[i+1], false)
		z.Zones[i].Basis = "a70"
		z.Zones[i].Support = map[bool]string{true: "supported", false: "limited"}[st.Status == "established"]
		z.Zones[i].Schedulable = fresh && (st.Status == "established" || st.Status == "preliminary")
		if st.Status == "race_only" {
			z.Zones[i].Support, z.Zones[i].Schedulable = "prior_based", false
		}
	}
	vtStable := st.Status == "established" && !st.Stability.ChangeUnconfirmed && fresh
	if b[3] <= b[2] {
		z.Zones[2].Support = "conflict"
		z.Zones[2].Reasons = append(z.Zones[2].Reasons, "zone_anchor_conflict")
		z.Reasons = append(z.Reasons, "zone_anchor_conflict")
	} else {
		set(2, b[2], b[3], false)
		z.Zones[2].Basis = "a70_vT"
		z.Zones[2].Support = st.Dimensions.Threshold.Status
		z.Zones[2].Schedulable = vtStable
	}
	set(3, b[3], b[4], false)
	z.Zones[3].Basis = "vT"
	z.Zones[3].Support = st.Dimensions.Threshold.Status
	z.Zones[3].Schedulable = vtStable
	if st.Dimensions.Threshold.Status != "supported" {
		z.Zones[3].Reasons = append(z.Zones[3].Reasons, "threshold_prior")
	}
	// Z5: three support levels.
	z5 := &z.Zones[4]
	z5.Basis = "v6_short_shape"
	open5 := b6 == nil
	if b[5] <= b[4] {
		z5.Support = "conflict"
		z5.Reasons = append(z5.Reasons, "zone_anchor_conflict")
		z.Reasons = append(z.Reasons, "zone_anchor_conflict")
	} else {
		set(4, b[4], b[5], false)
		var lows []float64
		for _, bs := range p.BSRange {
			lows = append(lows, c.V6*math.Pow(.5, 1/bs-1))
		}
		z5.FastRange = spanOf(lows)
		switch {
		case v6Direct && bSCal:
			z5.Support, z5.Schedulable = "supported", fresh
		case v6Direct:
			z5.Support, z5.Schedulable = "estimated", fresh
			// Automatic use takes the slow end of the range.
			slow := math.Min(b[5], z5.FastRange.Low)
			z5.FastSpeed, z5.FastPace = ptr(slow), ptr(Pace(slow))
		default:
			z5.Support, z5.Schedulable = "prior_based", false
			z5.Reasons = append(z5.Reasons, "speed_shape_prior")
		}
	}
	z6 := &z.Zones[5]
	z6.Basis = "v90s"
	if b6 == nil {
		z6.Reasons = append(z6.Reasons, "sprint_evidence_missing")
		if z5.SlowSpeed != nil {
			z5.Reasons = append(z5.Reasons, "upper_bound_open")
		}
	} else if *b6 <= b[5] {
		z6.Support = "conflict"
		z6.Reasons = append(z6.Reasons, "zone_anchor_conflict")
		z.Reasons = append(z.Reasons, "zone_anchor_conflict")
	} else {
		set(5, b[5], *b6, false)
		z6.Support = st.Dimensions.Sprint.Status
		z6.Schedulable = false // never a maximal sprint prescription
	}
	_ = open5
	if !fresh {
		z.Reasons = append(z.Reasons, "evidence_not_fresh")
	}
	return z
}
