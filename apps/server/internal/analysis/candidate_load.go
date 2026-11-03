package analysis

import tl "rai/server/internal/trainingload"

func CalculateCandidateLoad(in *Snapshot, kind string) ([]Metric, error) {
	ms := []Metric{}
	if kind == tl.JobKind {
		if in.LoadInput == nil {
			return nil, Invalid()
		}
		r, err := tl.CalculateTrainingLoad(*in.LoadInput, in.Generation)
		if err != nil {
			return nil, Invalid()
		}
		in.LoadArtifact = &r
		for _, d := range r.Days {
			q := d.Quality
			if q == "complete" {
				q = "available"
			}
			ms = append(ms, Metric{Day: d.Day, Name: "recorded_running_load", Algorithm: tl.Algorithm, Version: tl.Version, Value: d.Load, Unit: tl.Unit, Quality: q, Details: map[string]any{"artifactKey": tl.ArtifactKey, "method": r.Method}})
		}
		return ms, nil
	}
	if kind != tl.StateJobKind || in.LoadStateInput == nil {
		return nil, Invalid()
	}
	r, err := tl.CalculateLoadState(*in.LoadStateInput)
	if err != nil {
		return nil, Invalid()
	}
	in.LoadStateArtifact = &r
	for _, d := range r.Days {
		for _, m := range []struct {
			name, unit string
			value      *float64
		}{{"acute_load", tl.Unit + "/day", d.Acute.Value}, {"chronic_load", tl.Unit + "/day", d.Chronic.Value}, {"balance", tl.Unit + "/day", d.Balance}, {"load_ratio", "1", d.Ratio}, {"load_trend7", tl.Unit + "/day²", d.Trend7}} {
			q := d.Quality
			if q == "complete" {
				q = "available"
			}
			if m.value == nil {
				q = "insufficient"
			} else if q == "insufficient" {
				q = "partial"
			}
			ms = append(ms, Metric{Day: d.Day, Name: m.name, Algorithm: tl.StateAlgorithm, Version: tl.Version, Value: m.value, Unit: m.unit, Quality: q, Details: map[string]any{"artifactKey": tl.StateKey}})
		}
	}
	return ms, nil
}
