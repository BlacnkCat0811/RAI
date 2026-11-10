package readiness

import (
	"math"
	"sort"
)

func Validate(in ReadinessSnapshot) error {
	c := in.Config
	if len(in.Loads) > MaxLoads {
		return ErrLimit
	}
	if c != DefaultConfig(c.Timezone) || c.Timezone == "" || in.Dataset == "" || in.Assignment == "" || in.Generation < 1 || in.AsOf <= 0 || in.CapturedAt < in.AsOf || len(in.Days) != c.BaselineDays+1 {
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
		if r.Model != RecoveryModel || r.RunID == "" || r.Hash == "" || r.Generation < 1 || r.AsOf > in.AsOf || !finite(r.Confidence) || r.Confidence < 0 || r.Confidence > 1 || !finite(r.Coverage) || r.Coverage < 0 || r.Coverage > 1 || r.Score != nil && (!finite(*r.Score) || *r.Score < 0 || *r.Score > 100) {
			return ErrInvalid
		}
	}
	return nil
}
func LoadBaseline(in ReadinessSnapshot) Baseline {
	c := in.Config
	b := Baseline{State: "collecting"}
	vals := []float64{}
	first, last := -1, -1
	for i, d := range in.Days[:len(in.Days)-1] {
		// Every civil day that ended at or before AsOf is judgeable; a day without
		// a committed Workout is a known zero. The server never infers a missing
		// upload, so incompleteness is only ever reported per Workout.
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
				if w.AU == nil || v.AU == nil || v.Quality != "complete" || w.Quality != "complete" || !w.Compatible || w.Model != LoadModel {
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
func Calculate(input ReadinessSnapshot) (Result, error) {
	in := Canonical(input)
	if e := Validate(in); e != nil {
		return Result{}, e
	}
	c := in.Config
	r := Result{Date: in.Days[len(in.Days)-1].Date, Schema: Schema, Dataset: in.Dataset, Generation: in.Generation, ModelVersion: Model, ConfigHash: c.Hash(), Config: c, InputHash: InputHash(in), CalendarHash: Hash(in.Days), AsOf: in.AsOf, EvaluationAt: in.AsOf, CapturedAt: in.CapturedAt, ValidBefore: in.Days[len(in.Days)-1].End, Status: "insufficient_data", Quality: "insufficient", Recovery: in.Recovery, Sync: in.Sync, Baseline: LoadBaseline(in), ConfidenceMeaning: "evidence_support_not_probability", Terms: []Term{}, Refs: []Ref{}, Reasons: []string{}, CandidateOnly: true, Scope: "recorded_running_training_readiness"}
	r.TailBound = TailUpperBound(c)
	r.MaxScoreTailError = 100 * math.Ln2 * r.TailBound / c.BaselineFloor
	conflict := false
	for _, w := range in.Loads {
		r.Refs = append(r.Refs, Ref{ID: w.ID, RunID: w.RunID, Hash: w.Hash, FactHash: w.FactHash, End: w.End, ParameterID: w.ParameterID})
		if w.End > in.AsOf {
			r.ValidBefore = min(r.ValidBefore, w.End)
			continue
		}
		if w.End < in.AsOf-c.LookbackUs {
			continue
		}
		if w.Conflict || !w.Compatible && w.AU != nil {
			conflict = true
		}
		if w.AU == nil || !w.Compatible || w.Model != LoadModel || w.Quality != "complete" {
			r.UnknownLoads++
		} else {
			r.KnownLoads++
			r.Terms = append(r.Terms, Term{w.End, *w.AU})
		}
	}
	if r.UnknownLoads == 0 {
		r.Burden = ptr(DecayedBurden(r.Terms, in.AsOf, c))
	} else {
		r.Reasons = append(r.Reasons, "recent_load_unknown")
	}
	if conflict {
		r.Reasons = append(r.Reasons, "load_parameter_conflict")
		r.Status = "source_conflict"
	}
	if r.Baseline.Value == nil {
		r.Reasons = append(r.Reasons, "load_baseline_insufficient")
		if !conflict {
			r.Status = "collecting"
		}
	}
	if in.Recovery == nil {
		r.Reasons = append(r.Reasons, "recovery_missing")
		r.Status = "insufficient_data"
	} else {
		e := in.Recovery
		switch {
		case in.AsOf > e.FreshUntil:
			r.Reasons = append(r.Reasons, "recovery_stale")
			r.Status = "stale"
		case e.Status == "source_conflict":
			r.Reasons = append(r.Reasons, "recovery_source_conflict")
			r.Status = "source_conflict"
		case e.Status == "partial":
			r.Reasons = append(r.Reasons, "recovery_partial")
			r.Status = "insufficient_data"
		case e.Status != "available" || e.Quality != "complete" || e.Score == nil:
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
		evaluate(&r, in.AsOf)
	}
	sort.Strings(r.Reasons)
	return r, nil
}
func evaluate(r *Result, at int64) {
	d := DecayedBurden(r.Terms, at, r.Config)
	r.Burden = ptr(d)
	b := *r.Baseline.Value
	r.BurdenRatio = ptr(d / b)
	r.Penalty = ptr(math.Exp(-math.Ln2 * d / b))
	v, _ := Score(*r.RecoveryScore, d, b)
	r.Score = ptr(v)
}

// Freshen is a projection. It never modifies the persisted anchor or its input
// identity, and cannot cross a civil day or a known future-workout boundary.
func Freshen(anchor Result, at int64) (Result, error) {
	if at < anchor.AsOf || anchor.Config != DefaultConfig(anchor.Config.Timezone) {
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
		r.Status = "insufficient_data"
	} else if r.Recovery == nil || at > r.Recovery.FreshUntil {
		r.Reasons = append(r.Reasons, "recovery_stale")
		r.Status = "stale"
	} else {
		evaluate(&r, at)
		r.Confidence = math.Min(r.Confidence, r.Recovery.Confidence*math.Exp(-float64(at-r.AsOf)/float64(72*Hour)))
		return r, nil
	}
	r.Score = nil
	r.Penalty = nil
	r.Confidence = 0
	r.Quality = "insufficient"
	sort.Strings(r.Reasons)
	return r, nil
}
