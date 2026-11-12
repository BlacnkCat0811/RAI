package runfitness

import (
	"math"
	"slices"
	"sort"

	wf "rai/server/internal/workoutfeatures"
)

// Reference is the heart-rate reference in effect at the Workout's start.
type Reference struct {
	MaxHR     float64 `json:"maxHR"`
	RestHR    float64 `json:"restHR"`
	MaxBasis  string  `json:"maxBasis"`  // measured | age_estimated
	RestBasis string  `json:"restBasis"` // user | baseline
	Hash      string  `json:"hash"`
}

// Effort is a user-confirmed effort label. Labels change the model input only;
// they never modify HealthKit facts.
type Effort struct {
	Kind string `json:"kind"` // race | threshold_test | speed_test | sprint_test
	ID   string `json:"id"`
}

type SingleInput struct {
	WorkoutID         string
	Day               string
	Start, End        int64
	KnownAt           int64
	SourceEpoch       string
	Features          *wf.RunFitnessFeatures
	Reference         *Reference
	ReferenceConflict bool
	Effort            *Effort
}

type SegmentUse struct {
	Kind    string   `json:"kind"`
	Start   int64    `json:"startUs"`
	End     int64    `json:"endUs"`
	R       *float64 `json:"hrr"`
	A       *float64 `json:"a70MPS"`
	Weight  float64  `json:"weight"`
	Status  string   `json:"status"` // used | diagnostic | lower_bound | excluded
	Reason  string   `json:"reason,omitempty"`
	Domain  string   `json:"domain,omitempty"` // core | low_proxy | high_edge
	Seconds float64  `json:"seconds"`
}

type HRSingle struct {
	A               float64      `json:"a70MPS"`
	Score           float64      `json:"score"`
	RawScore        float64      `json:"rawScore"`
	Saturation      string       `json:"saturation,omitempty"`
	ScoreRange      Range        `json:"scoreSensitivityRange"`
	AbilityRange    Range        `json:"abilitySensitivityRange"`
	LogAHalfWidth   float64      `json:"logAHalfWidth"`
	NetSeconds      float64      `json:"netSupportedSeconds"`
	Quality         float64      `json:"quality"`
	Mode            string       `json:"gradeMode"` // adjusted | unadjusted | indoor
	ConditionWeight float64      `json:"conditionWeight"`
	Segments        []SegmentUse `json:"segments"`
}

type RaceSingle struct {
	Kind      string  `json:"kind"`
	DistanceM float64 `json:"distanceM"`
	Seconds   float64 `json:"elapsedSeconds"`
	Score     float64 `json:"score"`
	RawScore  float64 `json:"rawScore"`
	Quality   float64 `json:"quality"`
	Status    string  `json:"status"`
}

type Demonstrated struct {
	Duration int     `json:"durationSeconds"`
	Speed    float64 `json:"flatSpeedMPS"`
}

type Retention struct {
	Value float64 `json:"retention"`
	Drift float64 `json:"drift"`
}

type SingleResult struct {
	Schema        string         `json:"schemaVersion"`
	ConfigHash    string         `json:"configHash"`
	WorkoutID     string         `json:"workoutID"`
	Day           string         `json:"day"`
	Start         int64          `json:"startUs"`
	End           int64          `json:"endUs"`
	KnownAt       int64          `json:"knownAt"`
	SourceEpoch   string         `json:"sourceEpoch"`
	Status        string         `json:"status"` // supported | limited | insufficient_evidence | unavailable
	EvidenceTypes []string       `json:"evidenceTypes"`
	Reasons       []string       `json:"reasonCodes"`
	HR            *HRSingle      `json:"hrNormalized,omitempty"`
	Race          *RaceSingle    `json:"raceEquivalent,omitempty"`
	Demonstrated  []Demonstrated `json:"demonstrated"`
	Sprint90      *float64       `json:"demonstratedV90sMPS,omitempty"`
	SprintTest    *float64       `json:"sprintTestV90sMPS,omitempty"`
	Retention     *Retention     `json:"retention,omitempty"`
	RunningSecs   float64        `json:"runningSeconds"`
	PeakHR15      *float64       `json:"peakHR15,omitempty"`
	HRMaxSuggest  *float64       `json:"hrMaxSuggestion,omitempty"`
	Indoor        bool           `json:"indoor"`
	NoUpgrade     bool           `json:"noUpgrade"`
	ReferenceHash string         `json:"referenceHash,omitempty"`
	EffortKind    string         `json:"effortKind,omitempty"`
}

func hrr(hr float64, ref Reference) float64 { return (hr - ref.RestHR) / (ref.MaxHR - ref.RestHR) }

func addReason(xs []string, r ...string) []string {
	for _, x := range r {
		if x != "" && !slices.Contains(xs, x) {
			xs = append(xs, x)
		}
	}
	return xs
}

// conditionWeight applies the v1 weather policy (quality only, not a
// physiological correction).
func conditionWeight(f *wf.RunFitnessFeatures, indoor bool) (w float64, noUpgrade bool, reasons []string) {
	w = 1
	if indoor {
		return w, false, nil
	}
	if f.TemperatureC == nil && f.Humidity == nil {
		return .8, false, []string{"weather_missing"}
	}
	if t := f.TemperatureC; t != nil {
		if *t < 5 {
			w *= .7
			reasons = append(reasons, "cold_conditions")
		} else if *t > 25 {
			w *= .7
			reasons = append(reasons, "hot_conditions")
		}
		if *t < 0 || *t > 30 {
			noUpgrade = true
		}
	}
	if h := f.Humidity; h != nil && *h > .85 {
		w *= .8
		reasons = append(reasons, "humid_conditions")
	}
	return w, noUpgrade, reasons
}

type usedSegment struct {
	speed, hr, weight float64
}

// weightedMedianLog returns exp(weighted median of ln x) with a fixed
// first-crossing rule, so ties and input order cannot change the result.
func weightedMedianLog(xs, ws []float64) float64 {
	type p struct{ x, w float64 }
	ps := make([]p, len(xs))
	total := 0.0
	for i := range xs {
		ps[i] = p{math.Log(xs[i]), ws[i]}
		total += ws[i]
	}
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].x < ps[j].x })
	acc := 0.0
	for _, x := range ps {
		acc += x.w
		if acc >= total/2 {
			return math.Exp(x.x)
		}
	}
	return math.Exp(ps[len(ps)-1].x)
}

// aFor computes the single A for fixed segments under a perturbed reference.
func aFor(segs []usedSegment, rest, maxHR, speedFactor, beta float64) float64 {
	xs := make([]float64, len(segs))
	ws := make([]float64, len(segs))
	for i, s := range segs {
		r := (s.hr - rest) / (maxHR - rest)
		if r <= 0 {
			r = 1e-6
		}
		xs[i] = s.speed * speedFactor * math.Pow(RefIntensity/r, beta)
		ws[i] = s.weight
	}
	return weightedMedianLog(xs, ws)
}

func scoreVT(vT, bL float64) float64 {
	s, _, _ := ScoreFromTime(MarathonSeconds(vT, bL))
	return s
}

// Single computes one Workout's single-run fitness. It never reads long-term
// fitness and never uses evidence after this Workout.
func Single(in SingleInput) SingleResult {
	p := DefaultPolicy()
	out := SingleResult{Schema: SingleSchema, ConfigHash: ConfigHash(), WorkoutID: in.WorkoutID, Day: in.Day, Start: in.Start, End: in.End, KnownAt: in.KnownAt,
		SourceEpoch: in.SourceEpoch, Status: "insufficient_evidence", EvidenceTypes: []string{}, Reasons: []string{}, Demonstrated: []Demonstrated{}}
	f := in.Features
	if f == nil || f.Schema != wf.RunFitnessSchema {
		out.Status = "unavailable"
		out.Reasons = addReason(out.Reasons, "features_unavailable")
		return out
	}
	out.RunningSecs = f.RunningSeconds
	out.PeakHR15 = f.PeakHR15
	indoor := f.Indoor != nil && *f.Indoor
	out.Indoor = indoor
	if f.Indoor == nil {
		out.Reasons = addReason(out.Reasons, "environment_unknown")
	}
	cond, noUpgrade, condReasons := conditionWeight(f, indoor)
	out.NoUpgrade = noUpgrade
	out.Reasons = addReason(out.Reasons, condReasons...)
	if in.Effort != nil {
		out.EffortKind = in.Effort.Kind
	}
	distanceOK := f.DistanceCheck == "consistent"
	if f.DistanceCheck == "mismatch" {
		out.Reasons = addReason(out.Reasons, "distance_mismatch")
	}

	// Demonstrated lower bounds: outdoor, grade-known, cross-checked distance.
	if !indoor && distanceOK {
		for _, b := range f.Best {
			if b.FlatSpeed == nil {
				continue
			}
			switch b.Duration {
			case 180, 360, 720, 1200, 2400:
				out.Demonstrated = append(out.Demonstrated, Demonstrated{b.Duration, *b.FlatSpeed})
			case 90:
				if b.MaxGap <= 5 && (f.SampleSeconds == nil || *f.SampleSeconds <= 5) {
					v := *b.FlatSpeed
					out.Sprint90 = &v
					if in.Effort != nil && in.Effort.Kind == "sprint_test" {
						out.SprintTest = &v
					}
				}
			}
		}
	}

	// Heart-rate reference.
	ref := in.Reference
	switch {
	case in.ReferenceConflict:
		out.Reasons = addReason(out.Reasons, "hr_reference_conflict")
		ref = nil
	case ref == nil:
		out.Reasons = addReason(out.Reasons, "hr_reference_missing")
	case !(ref.MaxHR > ref.RestHR && ref.RestHR > 0 && ref.MaxHR <= 250):
		out.Reasons = addReason(out.Reasons, "hr_reference_invalid")
		ref = nil
	}
	if ref != nil {
		out.ReferenceHash = ref.Hash
		if ref.MaxBasis == "age_estimated" {
			out.Reasons = addReason(out.Reasons, "age_estimated_hrmax")
		}
		if f.PeakHR15 != nil && *f.PeakHR15 > ref.MaxHR {
			v := math.Round(*f.PeakHR15)
			out.HRMaxSuggest = &v
			out.Reasons = addReason(out.Reasons, "hrmax_review_suggested")
		}
		out.HR = singleHR(p, f, *ref, indoor, cond, distanceOK, &out)
		if out.HR != nil {
			out.EvidenceTypes = append(out.EvidenceTypes, "hr_normalized")
		}
		out.Retention = retention(p, f, *ref)
	}
	if len(f.Segments) == 0 && f.RunningSeconds > 0 {
		out.Reasons = addReason(out.Reasons, "steady_support_low")
	}

	// Confirmed race / test: distance and elapsed time, no heart rate needed.
	if in.Effort != nil && (in.Effort.Kind == "race" || in.Effort.Kind == "threshold_test" || in.Effort.Kind == "speed_test") {
		out.Race = raceSingle(p, f, in.Effort.Kind, indoor)
		if out.Race != nil {
			out.EvidenceTypes = append(out.EvidenceTypes, "race_equivalent")
		} else {
			out.Reasons = addReason(out.Reasons, "race_input_unsupported")
		}
	}
	if out.SprintTest != nil {
		out.EvidenceTypes = append(out.EvidenceTypes, "sprint_test")
	}
	switch {
	case out.HR != nil && out.HR.NetSeconds >= p.SupportedSeconds && singleReliable(out, f):
		out.Status = "supported"
	case out.HR != nil:
		out.Status = "limited"
	case out.Race != nil:
		out.Status = out.Race.Status
	}
	if out.HR == nil && ref != nil && !slices.Contains(out.Reasons, "steady_support_low") {
		out.Reasons = addReason(out.Reasons, "steady_support_low")
	}
	sort.Strings(out.Reasons)
	return out
}

func singleReliable(out SingleResult, f *wf.RunFitnessFeatures) bool {
	h := out.HR
	if h.Mode != "adjusted" || f.DistanceCheck != "consistent" || slices.Contains(out.Reasons, "age_estimated_hrmax") || h.ScoreRange.High-h.ScoreRange.Low > 20 {
		return false
	}
	for _, s := range h.Segments {
		if s.Status == "used" && (s.Kind == "cycle_averaged" || s.Domain == "low_proxy" || s.Domain == "high_edge") {
			return false
		}
	}
	return true
}

func singleHR(p Policy, f *wf.RunFitnessFeatures, ref Reference, indoor bool, cond float64, distanceOK bool, out *SingleResult) *HRSingle {
	h := &HRSingle{Segments: []SegmentUse{}, ConditionWeight: cond}
	var used []usedSegment
	unadjusted := false
	sourceQ := func(s wf.RunFitnessSegment) float64 {
		if s.HREpoch == "ambiguous" || s.SpeedEpoch == "ambiguous" {
			return 0
		}
		if s.HREpoch == "" || s.SpeedEpoch == "" {
			return .5
		}
		return 1
	}
	for _, s := range f.Segments {
		u := SegmentUse{Kind: s.Kind, Start: s.Start, End: s.End, Status: "excluded", Seconds: s.Seconds}
		r := hrr(s.HR, ref)
		u.R = ptr(r)
		exclude := func(reason string) { u.Reason = reason; h.Segments = append(h.Segments, u) }
		if s.Coverage < p.DiagnosticCover {
			exclude("coverage_low")
			continue
		}
		if s.Coverage < p.Coverage {
			u.Status = "diagnostic"
			exclude("coverage_diagnostic_only")
			continue
		}
		if f.DistanceCheck == "mismatch" && !indoor {
			exclude("distance_mismatch")
			continue
		}
		q := sourceQ(s)
		if q == 0 {
			exclude("source_conflict")
			continue
		}
		if r > 1 {
			exclude("hr_reference_exceeded")
			out.Reasons = addReason(out.Reasons, "hr_reference_exceeded")
			continue
		}
		if r > p.EdgeHRR {
			u.Status = "lower_bound"
			exclude("hrr_above_edge")
			continue
		}
		if r < p.LowHRR {
			exclude("hrr_below_low")
			continue
		}
		if s.Kind == "cycle_averaged" {
			if s.WalkRecovery {
				u.Status = "lower_bound"
				exclude("walk_recovery")
				continue
			}
			if r < p.CoreHRR[0] {
				exclude("hrr_below_low")
				continue
			}
		} else {
			if s.Speed < p.MinSpeed || s.MinSpeed < p.MinSpeed {
				exclude("not_continuous_running")
				continue
			}
			if s.SpeedCV > p.SpeedCV {
				exclude("speed_unsteady")
				continue
			}
			if math.Abs(s.HRTrend) > p.HRTrend {
				exclude("hr_not_steady")
				continue
			}
		}
		speed := s.Speed
		condQ := cond
		switch {
		case indoor:
			// Treadmill: grade treated as known (0 without a record).
		case s.FlatSpeed == nil:
			unadjusted = true
			condQ *= p.UnadjustedWeight
		case s.MaxAbsGrade != nil && *s.MaxAbsGrade > p.GradeMax || s.Grade != nil && math.Abs(*s.Grade) > p.GradeMax:
			exclude("grade_too_steep")
			continue
		case s.Grade != nil && math.Abs(*s.Grade) > p.GradeCore:
			exclude("grade_slope_only")
			continue
		default:
			speed = *s.FlatSpeed
		}
		switch {
		case s.Kind == "cycle_averaged":
			condQ *= p.CycleQuality
			u.Domain = "core"
			if r > p.CoreHRR[1] {
				u.Domain = "high_edge"
			}
		case r < p.CoreHRR[0]:
			condQ *= p.LowQuality
			u.Domain = "low_proxy"
		case r > p.CoreHRR[1]:
			condQ *= p.EdgeQuality
			u.Domain = "high_edge"
		default:
			u.Domain = "core"
		}
		steady := math.Max(.25, 1-s.SpeedCV/.10) * math.Max(.25, 1-math.Abs(s.HRTrend)/3)
		if s.Kind == "cycle_averaged" {
			steady = 1
		}
		drift := 1.0
		if s.RunningAt > p.DriftAfter {
			drift = p.DriftWeight
		}
		w := s.Coverage * q * steady * condQ * drift
		a := speed * math.Pow(RefIntensity/r, p.Beta)
		u.A, u.Weight, u.Status, u.Reason = ptr(a), w, "used", ""
		h.Segments = append(h.Segments, u)
		used = append(used, usedSegment{speed, s.HR, w})
		h.NetSeconds += s.Seconds
		for _, reason := range []string{map[string]string{"low_proxy": "low_intensity_proxy", "high_edge": "high_intensity_edge"}[u.Domain]} {
			out.Reasons = addReason(out.Reasons, reason)
		}
		if s.Kind == "cycle_averaged" {
			out.Reasons = addReason(out.Reasons, "cycle_averaged")
		}
	}
	if len(used) == 0 || h.NetSeconds < p.LimitedSeconds {
		if len(used) > 0 {
			out.Reasons = addReason(out.Reasons, "net_support_short")
		}
		return nil
	}
	h.Mode = "adjusted"
	if indoor {
		h.Mode = "indoor"
		out.Reasons = addReason(out.Reasons, "indoor_treadmill")
	} else if unadjusted {
		h.Mode = "unadjusted"
		out.Reasons = addReason(out.Reasons, "grade_unknown")
	}
	if !distanceOK && !indoor {
		out.Reasons = addReason(out.Reasons, "distance_cross_check_missing")
	}
	h.A = aFor(used, ref.RestHR, ref.MaxHR, 1, p.Beta)
	h.Score, h.RawScore, h.Saturation = ScoreA(h.A)
	maxSens := p.MaxSensitivity
	if ref.MaxBasis == "age_estimated" {
		maxSens = p.AgeMaxSensitive
	}
	speedSens := p.SpeedSensitivity
	if h.Mode == "unadjusted" || h.Mode == "indoor" {
		speedSens = p.UnknownGradeSens
	}
	var scores, logs, abilities []float64
	for _, dr := range []float64{-p.RestSensitivity, p.RestSensitivity} {
		for _, dm := range []float64{-maxSens, maxSens} {
			for _, ds := range []float64{1 - speedSens, 1 + speedSens} {
				a := aFor(used, ref.RestHR+dr, ref.MaxHR+dm, ds, p.Beta)
				s, _, _ := ScoreA(a)
				scores = append(scores, s)
				logs = append(logs, math.Log(a))
				for _, beta := range p.BetaRange {
					ab := aFor(used, ref.RestHR+dr, ref.MaxHR+dm, ds, beta)
					for _, rt := range p.RTRange {
						for _, bl := range p.BLRange {
							abilities = append(abilities, scoreVT(ab*math.Pow(rt/RefIntensity, beta), bl))
						}
					}
				}
			}
		}
	}
	h.ScoreRange = *spanOf(scores)
	lr := spanOf(logs)
	h.LogAHalfWidth = (lr.High - lr.Low) / 2
	h.AbilityRange = *spanOf(abilities)
	total, sumW := 0.0, 0.0
	for _, u := range used {
		total += u.weight
		sumW++
	}
	h.Quality = math.Min(1, total/sumW)
	if indoor {
		h.Quality *= p.IndoorWeight
	}
	return h
}

// retention compares the first and last comparable 15-minute blocks of a
// continuous run of at least 60 minutes.
func retention(p Policy, f *wf.RunFitnessFeatures, ref Reference) *Retention {
	if len(f.Blocks) < 2 {
		return nil
	}
	ok := func(b wf.RunFitnessBlock) bool {
		r := hrr(b.HR, ref)
		return b.FlatSpeed != nil && b.Coverage >= p.Coverage && b.Epoch != "" && b.Epoch != "ambiguous" && r >= p.CoreHRR[0] && r <= p.CoreHRR[1] &&
			b.MaxGrade != nil && *b.MaxGrade <= p.GradeMax
	}
	first, last := f.Blocks[0], f.Blocks[len(f.Blocks)-1]
	if !ok(first) || !ok(last) || first.Epoch != last.Epoch || math.Abs(*last.FlatSpeed / *first.FlatSpeed - 1) > .10 {
		return nil
	}
	early := *first.FlatSpeed / hrr(first.HR, ref)
	late := *last.FlatSpeed / hrr(last.HR, ref)
	v := late / early
	return &Retention{Value: v, Drift: 1 - v}
}

// raceSingle scores a confirmed race/test by its elapsed time under the fixed
// neutral prior curve; it does not read any long-term state.
func raceSingle(p Policy, f *wf.RunFitnessFeatures, kind string, indoor bool) *RaceSingle {
	if f.DistanceM == nil || *f.DistanceM <= 0 || f.Elapsed <= 0 || indoor {
		return nil
	}
	d, t := *f.DistanceM, f.Elapsed
	vT, ok := neutralVT(d/t, t)
	if !ok {
		return nil
	}
	q, status := 1.0, "supported"
	if f.DistanceCheck != "consistent" {
		q, status = .7, "limited"
	}
	s, raw, _ := ScoreFromTime(MarathonSeconds(vT, BL0))
	return &RaceSingle{Kind: kind, DistanceM: d, Seconds: t, Score: s, RawScore: raw, Quality: q, Status: status}
}

// neutralVT inverts the neutral two-segment curve at one performance.
func neutralVT(v, t float64) (float64, bool) {
	c6 := (1/BS0 - 1) * math.Log(360/TT)
	switch {
	case t >= TT:
		return v * math.Pow(t/TT, 1-1/BL0), true
	case t >= 360:
		z := math.Log(t/360) / math.Log(TT/360)
		return v / math.Exp((1-z)*c6), true
	case t >= 180:
		v6 := v * math.Pow(360/t, 1/BS0-1)
		return v6 / math.Exp(c6), true
	}
	return 0, false
}
