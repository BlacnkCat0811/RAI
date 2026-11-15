package hrref

// PolicyV3 is deliberately separate from Resolve: published v2 computations
// must remain reproducible until a new stack explicitly opts into this policy.
const PolicyV3 = "rai.hr_reference.policy/3"

type V3Input struct {
	Input
	Mode string // automatic or manual; callers map legacy explicit values to manual.
}

type V3Result struct {
	Snapshot
	Policy string `json:"policyVersion"`
	Status string `json:"status"`
	Source string `json:"source"`
}

// ResolveV3 keeps explicit settings usable, and distinguishes sparse automatic
// evidence from a standard baseline. All ages are relative to the Workout,
// never to the wall clock. Invalid references and conflicts fail closed.
func ResolveV3(in V3Input) (V3Result, error) {
	out := V3Result{Policy: PolicyV3, Status: "unavailable", Source: "unavailable"}
	if in.Mode != "automatic" && in.Mode != "manual" {
		return out, ErrInvalid
	}
	for _, r := range in.References {
		if err := Validate(r, in.AsOf); err != nil {
			return out, err
		}
	}
	baseInput := in.Input
	baseInput.DailyRHR = nil
	baseInput.References = nil
	for _, r := range in.References {
		if r.HRMax != nil {
			r.RestingHR = nil
			baseInput.References = append(baseInput.References, r)
		}
	}

	s, err := Resolve(baseInput)
	out.Snapshot = s
	if err != nil {
		return out, err
	}
	var candidates []Reference
	for _, r := range in.References {
		if effective(r, in.WorkoutStart) && r.RestingHR != nil && r.RestingHR.MeasuredAt <= in.WorkoutStart && (in.SourceEpoch == "" || r.SourceEpoch == in.SourceEpoch) {
			candidates = append(candidates, r)
		}
	}
	manual := pick(candidates, false)
	if len(candidates) > 0 && manual == nil {
		out.RestingHR = nil
		out.Reasons = dedupe(append(out.Reasons, "rhr_reference_conflict"))
		return out, ErrConflict
	}
	if in.Mode == "manual" {
		out.RestingHR = manual
		out.Source = "manual"
	} else {
		auto := in.Input
		auto.References = nil
		byDate := map[string]DailyRHR{}
		for _, d := range auto.DailyRHR {
			old, ok := byDate[d.Date]
			if !ok || d.Start > old.Start || d.Start == old.Start && d.Hash > old.Hash {
				byDate[d.Date] = d
			}
		}
		auto.DailyRHR = nil
		for _, d := range byDate {
			auto.DailyRHR = append(auto.DailyRHR, d)
		}
		baseline, reason := baselineRHR(auto)
		if baseline == nil && (reason == "rhr_baseline_insufficient" || reason == "rhr_baseline_recent_insufficient") {
			baseline, reason = baselineWithMinimum(auto, 3, 0)
			if baseline != nil {
				out.Reasons = append(out.Reasons, "rhr_sparse_baseline")
				out.Source = "baseline_sparse"
			}
		} else if baseline != nil {
			out.Source = "baseline_28d"
		}
		out.RestingHR = baseline
		if baseline == nil && manual != nil && reason != "rhr_baseline_invalid" {
			out.RestingHR = manual
			out.Source = "manual_fallback"
			out.Reasons = append(out.Reasons, "rhr_manual_fallback")
		}
	}
	// Replace v2 baseline failures with diagnostics for the selected v3 path.
	reasons := out.Reasons[:0]
	for _, reason := range out.Reasons {
		if reason != "rhr_baseline_insufficient" && reason != "rhr_baseline_recent_insufficient" && reason != "rhr_missing" {
			reasons = append(reasons, reason)
		}
	}
	out.Reasons = reasons
	if out.RestingHR == nil {
		out.Source = "unavailable"
		out.Reasons = append(out.Reasons, "rhr_observations_insufficient")
	} else if out.HRMax != nil && out.HRMax.Value <= out.RestingHR.Value {
		out.RestingHR = nil
		out.Source = "unavailable"
		out.Reasons = append(out.Reasons, "reference_conflict")
		return out, ErrConflict
	} else {
		out.Status = "complete"
		if !out.RestingHR.Baseline && !within(Measurement{MeasuredAt: out.RestingHR.MeasuredAt}, in.WorkoutStart, RHRDays) {
			out.Reasons = append(out.Reasons, "rhr_manual_review_due")
			out.Status = "estimated"
		}
		if out.Source == "manual_fallback" || out.Source == "baseline_sparse" {
			out.Status = "estimated"
		}
	}
	if out.HRMax == nil {
		out.Status = "unavailable"
	}
	out.Reasons = dedupe(out.Reasons)
	return out, nil
}
