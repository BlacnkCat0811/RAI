package analysis

import (
	"errors"
	ve "rai/server/internal/vo2maxexternal"
)

func CalculateCandidateVO2External(in *Snapshot) ([]Metric, error) {
	if in.ExternalInput == nil {
		return nil, Invalid()
	}
	r, err := ve.Calculate(*in.ExternalInput)
	if errors.Is(err, ve.ErrLimit) {
		return nil, &Failure{Code: "analysis_input_limit"}
	}
	if err != nil {
		return nil, Invalid()
	}
	in.ExternalArtifact = &r
	return ExternalVO2Metrics(r), nil
}
func ExternalVO2Metrics(r ve.Result) []Metric {
	return []Metric{{Day: WorkoutDay, Name: ve.Metric, Algorithm: ve.Algorithm, Version: "v1", Value: r.Value, Unit: ve.Unit, Quality: r.Quality, Details: map[string]any{"artifactKey": ve.ArtifactKey, "status": r.Status, "confidence": nil}}}
}
