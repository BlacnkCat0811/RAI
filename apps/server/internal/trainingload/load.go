package trainingload

import (
	"math"
	wf "rai/server/internal/workoutfeatures"
	"sort"
	"time"
)

// Quality tiers of the bounded (v2 product) policy. Quality keeps its frozen
// complete/partial/insufficient vocabulary; the tier says why.
const (
	TierComplete    = "complete"
	TierEstimated   = "estimated"
	TierApproximate = "approximate"
	TierUnknown     = "unknown"

	// A trace at or above this coverage, with no gap longer than
	// CompleteMaxGap, is fit for downstream use at full weight. The few seconds
	// before the first and after the last Watch sample are scaled at the
	// observed mean intensity, as before.
	CompleteCoverage = .98
	CompleteMaxGap   = 30.0
	// Below complete but still mostly observed: gap-scaled, confidence equals
	// coverage, and downstream consumers treat it as known.
	EstimatedCoverage = .80
	EstimatedMaxGap   = 180.0
)

// policy separates the frozen v1 candidate gates from the bounded v2 product
// semantics. v1 output stays byte-identical; only v2 clamps and tiers.
type policy struct{ bounded bool }

var (
	strictPolicy  = policy{}
	boundedPolicy = policy{bounded: true}
)

// CalculateTrainingLoad consumes the unified M1 feature and versioned companion.
// It does not access raw samples, filter HR, infer parameters, or read a clock.
// This is the frozen v1 candidate: zero tolerance above HRmax, below-rest is an
// input error, coverage below .95 or a gap above 30 s is unknown.
func CalculateTrainingLoad(in WorkoutInput, generation int64) (Result, error) {
	return calculateLoad(in, generation, strictPolicy)
}

// ClampedIntegral integrates one linear HRR segment with r bounded to [0,1].
// Time with r>1 is dosed at r=1 and time with r<0 contributes nothing; both are
// returned so the result can state how much of the trace left the reference.
func ClampedIntegral(a, b, seconds float64) (load, above, below float64) {
	if seconds <= 0 {
		return 0, 0, 0
	}
	cuts := []float64{0, 1}
	if a != b {
		for _, edge := range []float64{0, 1} {
			if f := (edge - a) / (b - a); f > 0 && f < 1 {
				cuts = append(cuts, f)
			}
		}
	}
	sort.Float64s(cuts)
	for i := 0; i+1 < len(cuts); i++ {
		f0, f1 := cuts[i], cuts[i+1]
		if f1 <= f0 {
			continue
		}
		ra, rb := a+(b-a)*f0, a+(b-a)*f1
		dt := seconds * (f1 - f0)
		switch mid := (ra + rb) / 2; {
		case mid > 1:
			load += LinearIntegral(1, 1, dt)
			above += dt
		case mid < 0:
			below += dt
		default:
			load += LinearIntegral(math.Max(0, math.Min(1, ra)), math.Max(0, math.Min(1, rb)), dt)
		}
	}
	return load, above, below
}

func calculateLoad(in WorkoutInput, generation int64, p0 policy) (Result, error) {
	f, e := in.Feature, in.Exposure
	out := Result{Schema: Schema, DatasetID: f.DatasetID, WorkoutID: f.WorkoutID, InputGeneration: generation, Model: Algorithm, ModelVersion: Version, ConfigHash: ConfigHash(), Scope: ScopeName, Status: "insufficient_data", Unit: Unit, Method: "unavailable", Quality: "insufficient", ActiveSeconds: f.Timing.Active, HRReference: e.Parameter, FeatureArtifactID: in.FeatureRunID, FeatureVersion: f.Schema, FeatureGeneration: f.Generation, ReasonCodes: []string{}, Days: []Contribution{}, Evidence: map[string]string{"featureInputHash": f.Evidence.InputHash, "exposureSchema": e.Schema, "exposureHash": Hash(e), "calendarIdentity": in.CalendarIdentity}}
	if p0.bounded {
		out.QualityTier = TierUnknown
	}
	if in.Deleted {
		out.Status = "deleted"
		out.ReasonCodes = append(out.ReasonCodes, "workout_deleted")
		return out, nil
	}
	loc, err := resolvedCalendar(in.Timezone, in.Calendar)
	if err != nil {
		return out, invalid()
	}
	if f.Schema != wf.Schema || e.Schema != wf.ExposureSchema || e.InputHash != f.Evidence.InputHash || e.DatasetID != f.DatasetID || e.WorkoutID != f.WorkoutID || e.Generation != f.Generation || f.Timing.End < f.Timing.Start || len(e.Segments) > wf.MaxExposureSegments {
		return out, invalid()
	}
	out.External.DistanceM = f.Distance.Preferred
	if f.Timing.Active != nil {
		out.External.ActiveMinutes = ptr(*f.Timing.Active / 60)
	}
	days := map[string]*Contribution{}
	validByDay := map[string]float64{}
	split := func(a, b int64, fn func(string, int64, int64)) {
		for a < b {
			at := time.UnixMicro(a).In(loc)
			next := day(at).AddDate(0, 0, 1).UnixMicro()
			end := min(b, next)
			fn(at.Format("2006-01-02"), a, end)
			a = end
		}
	}
	get := func(d string) *Contribution {
		if days[d] == nil {
			days[d] = &Contribution{Day: d, Quality: "insufficient"}
			if f.Timing.Active != nil {
				days[d].ActiveSeconds = ptr(0)
			}
		}
		return days[d]
	}
	intervals := e.Active
	if f.Timing.Active == nil {
		intervals = []wf.Interval{{Start: f.Timing.Start, End: f.Timing.End}}
	}
	active := 0.0
	var prev int64
	for _, a := range intervals {
		if a.Start < a.End && a.Start >= f.Timing.Start && a.End <= f.Timing.End && (prev == 0 || a.Start >= prev) {
			prev = a.End
		} else {
			return out, invalid()
		}
		split(a.Start, a.End, func(d string, a, b int64) {
			dt := float64(b-a) / 1e6
			x := get(d)
			if x.ActiveSeconds != nil {
				*x.ActiveSeconds += dt
			}
			active += dt
		})
	}
	// A recorded workout with no localizable active contribution still leaves
	// an unknown ledger entry, rather than a successful empty dependency.
	if len(days) == 0 {
		get(time.UnixMicro(f.Timing.Start).In(loc).Format("2006-01-02"))
	}
	finalize := func() {
		for _, d := range days {
			out.Days = append(out.Days, *d)
		}
		sort.Slice(out.Days, func(i, j int) bool { return out.Days[i].Day < out.Days[j].Day })
	}
	out.MaxGap = f.HeartRate.MaxGap
	bad := false
	if f.Timing.Active == nil || *f.Timing.Active <= 0 {
		out.ReasonCodes = append(out.ReasonCodes, "active_time_uncertain")
		bad = true
	} else if math.Abs(active-*f.Timing.Active) > 1e-6 {
		return out, invalid()
	}
	if f.ActivityType != 37 {
		out.ReasonCodes = append(out.ReasonCodes, "unsupported_input")
		bad = true
	}
	if e.Parameter == nil {
		for _, r := range e.ParameterReasons {
			out.ReasonCodes = append(out.ReasonCodes, string(r))
		}
		if len(e.ParameterReasons) == 0 {
			out.ReasonCodes = append(out.ReasonCodes, "parameter_missing")
		}
		bad = true
	}
	// Keep every more conservative M1 gate. Summary may replace only absent HR
	// time support, never an observed but failing trace or an outlier/source gate.
	// A resolved hr_reference/2 dose reference supersedes the feature-level
	// parameter, so its absence is not an additional gate here.
	//
	// The bounded policy measures coverage itself: outlier-excluded and
	// source-ambiguous time is simply unobserved, and a feature-level HRmax
	// conflict is resolved by clamping below, so neither discards the trace.
	for _, r := range f.Qualification.Load.Reasons {
		if r == wf.MissingHR || r == wf.HRCoverageLow || r == wf.ParameterMissing && e.Parameter != nil {
			continue
		}
		if p0.bounded && (r == wf.SignalOutlier || r == wf.ParameterConflict && e.Parameter != nil) {
			continue
		}
		out.ReasonCodes = append(out.ReasonCodes, string(r))
		bad = true
	}
	if f.HeartRate.AmbiguousSeconds > 0 {
		if p0.bounded {
			out.ReasonCodes = append(out.ReasonCodes, "source_conflict_excluded")
		} else {
			out.ReasonCodes = append(out.ReasonCodes, "source_conflict")
			bad = true
		}
	}
	if f.Timing.Active != nil && *f.Timing.Active < 180 {
		out.ReasonCodes = append(out.ReasonCodes, "short_session_hr_lag")
	}
	if bad {
		finalize()
		return out, nil
	}
	p := e.Parameter
	if !finite(p.RestHR) || !finite(p.MaxHR) || p.RestHR <= 0 || p.MaxHR <= p.RestHR || p.ID == "" || p.Hash == "" || p.Provenance == "" || p.Revoked || p.EffectiveFrom > f.Timing.Start || p.EffectiveThrough != nil && f.Timing.Start >= *p.EffectiveThrough {
		return out, invalid()
	}
	if len(e.Segments) == 0 {
		if e.SummaryHR == nil || e.SummarySemantics != "whole_elapsed_equals_active" {
			out.ReasonCodes = append(out.ReasonCodes, "missing_hr")
			finalize()
			return out, nil
		}
		r := (*e.SummaryHR - p.RestHR) / (p.MaxHR - p.RestHR)
		if !finite(r) {
			return out, invalid()
		}
		if r < 0 {
			if !p0.bounded {
				return out, invalid()
			}
			out.ReasonCodes = append(out.ReasonCodes, "hr_below_reference_rest")
			out.BelowRestSeconds = active
			r = 0
		}
		if r > 1 {
			if !p0.bounded {
				out.ReasonCodes = append(out.ReasonCodes, "parameter_conflict")
				finalize()
				return out, nil
			}
			out.ReasonCodes = append(out.ReasonCodes, "hr_above_reference_max")
			out.AboveMaxSeconds = active
			r = 1
		}
		v := active / 60 * Intensity(r)
		out.Load = ptr(v)
		out.EstimatedLoad = ptr(v)
		out.Method = "summary_hr_approximation"
		out.Status = "available"
		out.Quality = "partial"
		if p0.bounded {
			out.QualityTier = TierApproximate
		}
		out.Confidence = .35
		out.Evidence["assumption"] = "whole active interval represented by summary mean HR; nonlinear exposure unknown"
		for _, d := range days {
			d.Load = ptr(*d.ActiveSeconds / 60 * Intensity(r))
			d.Quality = "partial"
		}
		finalize()
		return out, nil
	}
	total := 0.0
	prev = 0
	for _, s := range e.Segments {
		if s.Start >= s.End || s.Start < prev || !finite(s.HR0) || !finite(s.HR1) || s.Source < 0 || s.Source >= len(e.SourceEpochs) || e.SourceEpochs[s.Source] == "" {
			return out, invalid()
		}
		prev = s.End
		contained := false
		for _, a := range e.Active {
			if a.Start <= s.Start && s.End <= a.End {
				contained = true
				break
			}
		}
		if !contained {
			return out, invalid()
		}
		if !p0.bounded {
			if s.HR0 < p.RestHR || s.HR1 < p.RestHR {
				return out, invalid()
			}
			if s.HR0 > p.MaxHR || s.HR1 > p.MaxHR {
				out.ConflictSeconds += float64(s.End-s.Start) / 1e6
				continue
			}
		}
		out.ValidHRSeconds += float64(s.End-s.Start) / 1e6
		split(s.Start, s.End, func(d string, a, b int64) {
			value := func(at int64) float64 { return s.HR0 + (s.HR1-s.HR0)*float64(at-s.Start)/float64(s.End-s.Start) }
			ra, rb, seconds := (value(a)-p.RestHR)/(p.MaxHR-p.RestHR), (value(b)-p.RestHR)/(p.MaxHR-p.RestHR), float64(b-a)/1e6
			var v float64
			if p0.bounded {
				var above, below float64
				v, above, below = ClampedIntegral(ra, rb, seconds)
				out.AboveMaxSeconds += above
				out.BelowRestSeconds += below
			} else {
				v = LinearIntegral(ra, rb, seconds)
			}
			total += v
			validByDay[d] += seconds
			x := get(d)
			if x.Observed == nil {
				x.Observed = ptr(0)
			}
			*x.Observed += v
		})
	}
	out.Coverage = out.ValidHRSeconds / active
	out.ObservedLoad = ptr(total)
	if out.ConflictSeconds > 0 {
		out.ReasonCodes = append(out.ReasonCodes, "parameter_conflict")
		out.Evidence["conflictPolicy"] = "zero tolerance; conflicting segments excluded, no clamp or scaling"
		finalize()
		return out, nil
	}
	if out.AboveMaxSeconds > 0 {
		out.ReasonCodes = append(out.ReasonCodes, "hr_above_reference_max")
		out.Evidence["boundPolicy"] = "HRR clamped to [0,1]; time above the HRmax reference dosed at HRR 1"
	}
	if out.BelowRestSeconds > 0 {
		out.ReasonCodes = append(out.ReasonCodes, "hr_below_reference_rest")
		out.Evidence["boundPolicy"] = "HRR clamped to [0,1]; time above the HRmax reference dosed at HRR 1"
	}
	tier := TierUnknown
	if p0.bounded {
		switch {
		case out.Coverage >= CompleteCoverage-1e-12 && out.MaxGap <= CompleteMaxGap:
			tier = TierComplete
		case out.Coverage >= EstimatedCoverage-1e-12 && out.MaxGap <= EstimatedMaxGap:
			tier = TierEstimated
		}
	} else if out.Coverage >= .95-1e-12 && out.MaxGap <= 30 {
		tier = TierComplete
	}
	if tier == TierUnknown {
		out.ReasonCodes = append(out.ReasonCodes, "hr_coverage_low")
		out.Evidence["observedLoad"] = "lower bound only; missing exposure not imputed"
		finalize()
		return out, nil
	}
	scale := 1.0
	out.Quality = "complete"
	out.Method = "hrr_integral"
	out.Confidence = 1
	if active-out.ValidHRSeconds > 1e-6 {
		scale = active / out.ValidHRSeconds
		out.Quality = "partial"
		out.Method = "hrr_integral_gap_scaled"
		out.Confidence = out.Coverage
		out.Evidence["assumption"] = "missing time has the same mean intensity as observed time"
		out.EstimatedLoad = ptr(total * scale)
	}
	if p0.bounded {
		out.QualityTier = tier
		if tier == TierComplete {
			out.Quality = "complete"
		} else {
			out.Quality = "partial"
		}
	}
	out.ScaleFactor = ptr(scale)
	out.Load = ptr(total * scale)
	out.Status = "available"
	for _, d := range days {
		v := 0.0
		if d.Observed != nil {
			v = *d.Observed
		}
		d.Load = ptr(v + (*d.ActiveSeconds-validByDay[d.Day])*total/out.ValidHRSeconds)
		d.Quality = out.Quality
	}
	finalize()
	return out, nil
}
