package trainingload

import (
	"math"
	wf "rai/server/internal/workoutfeatures"
	"sort"
	"time"
)

// CalculateTrainingLoad consumes the unified M1 feature and versioned companion.
// It does not access raw samples, filter HR, infer parameters, or read a clock.
func CalculateTrainingLoad(in WorkoutInput, generation int64) (Result, error) {
	f, e := in.Feature, in.Exposure
	out := Result{Schema: Schema, DatasetID: f.DatasetID, WorkoutID: f.WorkoutID, InputGeneration: generation, Model: Algorithm, ModelVersion: Version, ConfigHash: ConfigHash(), Scope: ScopeName, Status: "insufficient_data", Unit: Unit, Method: "unavailable", Quality: "insufficient", ActiveSeconds: f.Timing.Active, HRReference: e.Parameter, FeatureArtifactID: in.FeatureRunID, FeatureVersion: f.Schema, FeatureGeneration: f.Generation, ReasonCodes: []string{}, Days: []Contribution{}, Evidence: map[string]string{"featureInputHash": f.Evidence.InputHash, "exposureSchema": e.Schema, "exposureHash": Hash(e), "calendarIdentity": in.CalendarIdentity}}
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
	for _, r := range f.Qualification.Load.Reasons {
		if r == wf.MissingHR || r == wf.HRCoverageLow {
			continue
		}
		out.ReasonCodes = append(out.ReasonCodes, string(r))
		bad = true
	}
	if f.HeartRate.AmbiguousSeconds > 0 {
		out.ReasonCodes = append(out.ReasonCodes, "source_conflict")
		bad = true
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
		if !finite(r) || r > 1 {
			out.ReasonCodes = append(out.ReasonCodes, "parameter_conflict")
			finalize()
			return out, nil
		}
		v := active / 60 * Intensity(r)
		out.Load = ptr(v)
		out.EstimatedLoad = ptr(v)
		out.Method = "summary_hr_approximation"
		out.Status = "available"
		out.Quality = "partial"
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
		if s.HR0 > p.MaxHR || s.HR1 > p.MaxHR {
			out.ConflictSeconds += float64(s.End-s.Start) / 1e6
			continue
		}
		out.ValidHRSeconds += float64(s.End-s.Start) / 1e6
		split(s.Start, s.End, func(d string, a, b int64) {
			value := func(at int64) float64 { return s.HR0 + (s.HR1-s.HR0)*float64(at-s.Start)/float64(s.End-s.Start) }
			v := LinearIntegral((value(a)-p.RestHR)/(p.MaxHR-p.RestHR), (value(b)-p.RestHR)/(p.MaxHR-p.RestHR), float64(b-a)/1e6)
			total += v
			validByDay[d] += float64(b-a) / 1e6
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
	if out.Coverage < .95-1e-12 || out.MaxGap > 30 {
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
