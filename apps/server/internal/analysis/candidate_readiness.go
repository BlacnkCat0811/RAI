package analysis

import (
	"errors"
	rd "rai/server/internal/readiness"
)

func CalculateCandidateReadiness(in *Snapshot) ([]Metric, error) {
	if in.ReadinessInput == nil {
		return nil, Invalid()
	}
	r, e := rd.Calculate(*in.ReadinessInput)
	if errors.Is(e, rd.ErrLimit) {
		return nil, &Failure{Code: "analysis_input_limit"}
	}
	if e != nil {
		return nil, Invalid()
	}
	in.ReadinessArtifact = &r
	return ReadinessMetrics(r), nil
}
func ReadinessMetrics(r rd.Result) []Metric {
	q := "insufficient"
	if r.Score != nil {
		q = "available"
	}
	return []Metric{{Day: r.Date, Name: "training_readiness", Algorithm: rd.Algorithm, Version: "v1", Value: r.Score, Unit: "score_0_100", Quality: q, Details: map[string]any{"artifactKey": rd.Key, "status": r.Status, "confidence": r.Confidence, "confidenceMeaning": r.ConfidenceMeaning, "asOf": r.AsOf, "candidateOnly": true}}}
}
