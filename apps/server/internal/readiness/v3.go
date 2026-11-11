package readiness

import (
	"math"
	"sort"
)

const (
	ModelV3         = "rai.readiness.training/v3"
	SchemaV3        = "rai.readiness.training/3"
	InputSchemaV3   = "rai.readiness.input/3"
	LoadModelV2     = "rai.training_load.hrr_integral/v2"
	RecoveryModelV2 = "rai.recovery.physiological/v2"
	PolicyV3        = "recorded-running;known-day-median;finite-tail-certified;allowlist-load-v2-recovery-v2;decayed-unknown-burden;habitual-burden-28d/v3.2"
	// UnknownBurdenMaxV3 bounds the decayed burden of unknown Workouts, in
	// typical training days. With a 36 h tau a typical unknown run is below it
	// after about 2.8 days, so old gaps no longer veto today's score.
	UnknownBurdenMaxV3 = .15
)

func ScopeV3For(stack string) string { return "model/" + stack + "/athlete/readiness/v3" }

func DefaultV3Config(tz string) Config {
	c := DefaultConfig(tz)
	c.Policy = PolicyV3
	c.UnknownBurdenMax = UnknownBurdenMaxV3
	c.HabitDays, c.HabitFloorDays, c.MultiplierFloor = 28, .5, .35
	return c
}

// habitualBurden is the median decayed burden at the same clock time on each
// of the previous HabitDays days, floored at HabitFloorDays typical training
// days so a return from a break is not compared with an empty habit. A daily
// runner therefore sits at a multiplier near 1 instead of being permanently
// penalized for training often.
func habitualBurden(terms []Term, at int64, c Config, typical float64) float64 {
	vals := make([]float64, 0, c.HabitDays)
	for k := 1; k <= c.HabitDays; k++ {
		vals = append(vals, DecayedBurden(terms, at-int64(k)*24*Hour, c))
	}
	sort.Float64s(vals)
	n := len(vals)
	h := vals[n/2]
	if n%2 == 0 {
		h = vals[n/2-1]/2 + vals[n/2]/2
	}
	return math.Max(h, c.HabitFloorDays*typical)
}

// evaluateV3 applies the habitual-burden multiplier: no penalty at or below
// the habit, halving for each habit above it, never below MultiplierFloor.
func evaluateV3(r *Result, at int64) {
	d := DecayedBurden(r.Terms, at, r.Config)
	h := *r.HabitualBurden
	r.Burden = ptr(d)
	r.BurdenRatio = ptr(d / h)
	r.Penalty = ptr(math.Max(r.Config.MultiplierFloor, math.Exp2(-math.Max(0, d/h-1))))
	r.Score = ptr(*r.RecoveryScore * *r.Penalty)
}

// knownLoad is the v3 knownness rule: a lineage-compatible v2 load with a
// value is known at any quality tier; its tier only affects confidence upstream.
func knownLoad(w WorkoutLoad) bool {
	return w.AU != nil && w.Compatible && !w.Conflict && w.Model == LoadModelV2
}

func ValidateV3(in ReadinessSnapshot) error {
	c := in.Config
	if len(in.Loads) > MaxLoads {
		return ErrLimit
	}
	if c != DefaultV3Config(c.Timezone) || c.Timezone == "" || in.Dataset == "" || in.Assignment == "" || in.Generation < 1 || in.AsOf <= 0 || in.CapturedAt < in.AsOf || len(in.Days) != c.BaselineDays+1 {
		return ErrInvalid
	}
	for i, d := range in.Days {
		if len(d.Date) != 10 || d.End-d.Start < 22*Hour || d.End-d.Start > 26*Hour || i > 0 && (d.Start != in.Days[i-1].End || d.Date <= in.Days[i-1].Date) {
			return ErrInvalid
		}
	}
	today := in.Days[len(in.Days)-1]
	if in.AsOf < today.Start || in.AsOf >= today.End {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, l := range in.Loads {
		if l.ID == "" || seen[l.ID] || l.End <= 0 || l.AU != nil && (!finite(*l.AU) || *l.AU < 0 || *l.AU > c.MaxWorkoutAU) {
			return ErrInvalid
		}
		seen[l.ID] = true
		ds := map[string]bool{}
		sum := 0.
		all := true
		for _, d := range l.Days {
			if ds[d.Day] || len(d.Day) != 10 || d.AU != nil && (!finite(*d.AU) || *d.AU < 0) {
				return ErrInvalid
			}
			ds[d.Day] = true
			if d.AU == nil {
				all = false
			} else {
				sum += *d.AU
			}
		}
		if l.AU != nil && (len(l.Days) == 0 || !all || math.Abs(sum-*l.AU) > 1e-7*math.Max(1, *l.AU)) {
			return ErrInvalid
		}
	}
	if r := in.Recovery; r != nil {
		if r.RunID == "" || r.Hash == "" || r.Generation < 1 || r.AsOf > in.AsOf || !finite(r.Confidence) || r.Confidence < 0 || r.Confidence > 1 || !finite(r.Coverage) || r.Coverage < 0 || r.Coverage > 1 || r.Score != nil && (!finite(*r.Score) || *r.Score < 0 || *r.Score > 100) {
			return ErrInvalid
		}
	}
	return nil
}

func LoadBaselineV3(in ReadinessSnapshot) Baseline {
	c := in.Config
	b := Baseline{State: "collecting"}
	vals := []float64{}
	first, last := -1, -1
	for i, d := range in.Days[:len(in.Days)-1] {
		// Every civil day that ended at or before AsOf is judgeable: a day with no
		// committed Workout is a known zero, not an unknown. The server never
		// infers that a Workout was missing, so absence of evidence is only
		// reported per Workout through its own load quality.
		known := d.End <= in.AsOf
		sum := 0.
		for _, w := range in.Loads {
			if w.End > in.AsOf {
				continue
			}
			for _, v := range w.Days {
				if v.Day != d.Date {
					continue
				}
				if !knownLoad(w) || v.AU == nil {
					known = false
				} else {
					sum += *v.AU
				}
			}
		}
		if !known {
			b.UnknownDays++
			continue
		}
		if sum == 0 {
			b.KnownZeroDays++
			continue
		}
		vals = append(vals, sum)
		if first < 0 {
			first = i
		}
		last = i
	}
	b.TrainingDays = len(vals)
	if first >= 0 {
		b.SpanDays = last - first
		b.LatestAgeDays = c.BaselineDays - last
	}
	if len(vals) < c.MinimumDays || b.SpanDays < c.MinimumSpanDays || b.LatestAgeDays > c.BaselineFreshDays {
		return b
	}
	sort.Float64s(vals)
	n := len(vals)
	v := vals[n/2]
	if n%2 == 0 {
		v = vals[n/2-1]/2 + vals[n/2]/2
	}
	b.Median = ptr(v)
	b.Value = ptr(math.Max(c.BaselineFloor, v))
	b.FloorApplied = v < c.BaselineFloor
	b.State = "available"
	return b
}

func CalculateV3(input ReadinessSnapshot) (Result, error) {
	in := Canonical(input)
	if e := ValidateV3(in); e != nil {
		return Result{}, e
	}
	c := in.Config
	r := Result{Date: in.Days[len(in.Days)-1].Date, Schema: SchemaV3, Dataset: in.Dataset, Generation: in.Generation, ModelVersion: ModelV3, ConfigHash: c.Hash(), Config: c, InputHash: InputHash(in), CalendarHash: Hash(in.Days), AsOf: in.AsOf, EvaluationAt: in.AsOf, CapturedAt: in.CapturedAt, ValidBefore: in.Days[len(in.Days)-1].End, Status: "insufficient_data", Quality: "insufficient", Recovery: in.Recovery, Sync: in.Sync, Baseline: LoadBaselineV3(in), ConfidenceMeaning: "evidence_support_not_probability", Terms: []Term{}, Refs: []Ref{}, Reasons: []string{}, CandidateOnly: true, Scope: "recorded_running_training_readiness"}
	r.TailBound = TailUpperBound(c)
	r.MaxScoreTailError = 100 * math.Ln2 * r.TailBound / c.BaselineFloor
	conflict, materialConflict := false, false
	badModel := false
	var unknown []Term
	for _, w := range in.Loads {
		r.Refs = append(r.Refs, Ref{ID: w.ID, RunID: w.RunID, Hash: w.Hash, FactHash: w.FactHash, End: w.End, ParameterID: w.ParameterID})
		if w.End > in.AsOf {
			r.ValidBefore = min(r.ValidBefore, w.End)
			continue
		}
		if w.End < in.AsOf-c.LookbackUs {
			continue
		}
		if w.Model != LoadModelV2 {
			badModel = true
			continue
		}
		if knownLoad(w) {
			r.KnownLoads++
			r.Terms = append(r.Terms, Term{End: w.End, AU: *w.AU})
			continue
		}
		r.UnknownLoads++
		unknown = append(unknown, Term{End: w.End, Imputed: true})
		conflict = conflict || w.Conflict || !w.Compatible && w.AU != nil
	}
	if badModel {
		r.Reasons = append(r.Reasons, "load_model_not_allowed")
		r.Status = "source_conflict"
	}
	if len(unknown) == 0 {
		r.Burden = ptr(DecayedBurden(r.Terms, in.AsOf, c))
	} else if typical, ok := typicalTrainingLoad(r); ok {
		// Unknown Workouts are valued at the typical training-day load. Only
		// their decayed share of today's burden decides whether that guess is
		// small enough to keep the score.
		for i := range unknown {
			unknown[i].AU = typical
		}
		known := DecayedBurden(r.Terms, in.AsOf, c)
		u := DecayedBurden(unknown, in.AsOf, c)
		// Measured in typical training days, like burdenInTypicalDays, so the
		// imputed part can move the load multiplier by at most 2^-UnknownBurdenMax.
		scale := typical
		if r.Baseline.Value != nil {
			scale = *r.Baseline.Value
		}
		ratio := u / scale
		r.UnknownBurden = ptr(ratio)
		if ratio <= c.UnknownBurdenMax {
			r.Terms = append(r.Terms, unknown...)
			r.Burden = ptr(known + u)
		} else {
			r.Reasons = append(r.Reasons, "recent_load_unknown")
			materialConflict = conflict
		}
	} else {
		r.Reasons = append(r.Reasons, "recent_load_unknown")
		materialConflict = conflict
	}
	if materialConflict {
		r.Reasons = append(r.Reasons, "load_parameter_conflict")
		r.Status = "source_conflict"
	}
	if r.Baseline.Value == nil {
		r.Reasons = append(r.Reasons, "load_baseline_insufficient")
		if !materialConflict {
			r.Status = "collecting"
		}
	}
	if in.Recovery == nil {
		r.Reasons = append(r.Reasons, "recovery_missing")
		r.Status = "insufficient_data"
	} else {
		e := in.Recovery
		switch {
		case e.Model != RecoveryModelV2:
			r.Reasons = append(r.Reasons, "recovery_model_not_allowed")
			r.Status = "source_conflict"
		case in.AsOf > e.FreshUntil:
			r.Reasons = append(r.Reasons, "recovery_stale")
			r.Status = "stale"
		case e.Status == "source_conflict":
			r.Reasons = append(r.Reasons, "recovery_source_conflict")
			r.Status = "source_conflict"
		case e.Status == "partial" || e.Quality != "complete" || e.Score == nil:
			r.Reasons = append(r.Reasons, "recovery_partial_or_missing")
			r.Status = "insufficient_data"
		case e.Status != "available":
			r.Reasons = append(r.Reasons, "recovery_missing")
			r.Status = "insufficient_data"
		default:
			r.RecoveryScore = ptr(*e.Score)
			r.RecoveryContribution = ptr(*e.Score)
		}
	}
	if len(r.Reasons) == 0 {
		r.Status = "available"
		r.Quality = "complete"
		r.LoadSupport = math.Min(1, float64(r.Baseline.TrainingDays)/float64(c.MinimumDays)) * math.Min(1, float64(r.Baseline.SpanDays)/float64(c.MinimumSpanDays))
		r.Confidence = math.Min(c.ConfidenceCap, math.Min(in.Recovery.Confidence, r.LoadSupport))
		if r.UnknownBurden != nil {
			r.Confidence *= 1 - math.Min(1, *r.UnknownBurden)
		}
		r.HabitualBurden = ptr(habitualBurden(r.Terms, in.AsOf, c, *r.Baseline.Value))
		evaluateV3(&r, in.AsOf)
	}
	sort.Strings(r.Reasons)
	return r, nil
}

func FreshenV3(anchor Result, at int64) (Result, error) {
	return FreshenV3ForConfig(anchor, at, DefaultV3Config(anchor.Config.Timezone))
}

// FreshenV3ForConfig projects a validated publication using its frozen release
// configuration. The caller supplies a trusted release descriptor; this does
// not allow calculating new results with a retired policy.
func FreshenV3ForConfig(anchor Result, at int64, expected Config) (Result, error) {
	if at < anchor.AsOf || anchor.ModelVersion != ModelV3 || anchor.Config != expected {
		return Result{}, ErrInvalid
	}
	r := anchor
	r.Reasons = append([]string{}, anchor.Reasons...)
	r.EvaluationAt = at
	if r.Score == nil {
		return r, nil
	}
	if at >= r.ValidBefore {
		r.Reasons = append(r.Reasons, "candidate_superseded")
		r.Score = nil
		r.Burden = nil
		r.BurdenRatio = nil
		r.Penalty = nil
		r.Quality = "insufficient"
		r.Status = "superseded"
		return r, nil
	}
	if r.HabitualBurden == nil {
		evaluate(&r, at)
		return r, nil
	}
	evaluateV3(&r, at)
	return r, nil
}

// typicalTrainingLoad is the baseline median training-day load, or, while the
// baseline is still collecting, the median of the known recent Workout loads.
func typicalTrainingLoad(r Result) (float64, bool) {
	if r.Baseline.Median != nil && *r.Baseline.Median > 0 {
		return *r.Baseline.Median, true
	}
	vals := []float64{}
	for _, t := range r.Terms {
		if t.AU > 0 {
			vals = append(vals, t.AU)
		}
	}
	if len(vals) == 0 {
		return 0, false
	}
	sort.Float64s(vals)
	n := len(vals)
	if n%2 == 1 {
		return vals[n/2], true
	}
	return vals[n/2-1]/2 + vals[n/2]/2, true
}
