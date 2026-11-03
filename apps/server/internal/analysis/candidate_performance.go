package analysis

import pf "rai/server/internal/performance"

func CalculateCandidatePerformance(in *Snapshot) ([]Metric, error) {
	if in.PerformanceInput == nil {
		return nil, Invalid()
	}
	r, err := pf.Calculate(*in.PerformanceInput)
	if err != nil {
		if _, ok := err.(*pf.LimitError); ok {
			return nil, &Failure{Code: "analysis_input_limit"}
		}
		return nil, Invalid()
	}
	in.PerformanceArtifact = &r
	return PerformanceMetrics(r), nil
}
func PerformanceMetrics(r pf.Result) []Metric {
	var v60, score, retention *float64
	if r.Ability != nil {
		v60 = Ptr(r.Ability.V60)
		score = Ptr(r.Ability.Score)
		retention = Ptr(r.Ability.Retention)
	}
	ms := []Metric{}
	for _, m := range []struct {
		name, model, unit string
		value             *float64
	}{{"supported_v60", pf.CurveModel, "m/s", v60}, {"running_ability", pf.AbilityModel, "point", score}, {"endurance_retention", pf.EnduranceModel, "1", retention}} {
		q := "partial"
		if m.value == nil {
			q = "insufficient"
		}
		ms = append(ms, Metric{Day: WorkoutDay, Name: m.name, Algorithm: m.model, Version: pf.Version, Value: m.value, Unit: m.unit, Quality: q, Details: map[string]any{"artifactKey": pf.ArtifactKey, "evidenceMode": r.EvidenceMode, "status": r.Status, "confidence": r.Confidence}})
	}
	return ms
}
