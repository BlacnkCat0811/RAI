package analysis

import (
	"errors"
	rc "rai/server/internal/recovery"
)

func CalculateCandidateRecovery(in *Snapshot) ([]Metric, error) {
	if in.RecoveryInput == nil {
		return nil, Invalid()
	}
	r, e := rc.Calculate(*in.RecoveryInput)
	if errors.Is(e, rc.ErrLimit) {
		return nil, &Failure{Code: "analysis_input_limit"}
	}
	if e != nil {
		return nil, Invalid()
	}
	in.RecoveryArtifact = &r
	return RecoveryMetrics(r.Recovery), nil
}
func RecoveryMetrics(r rc.Result) []Metric {
	q := r.Quality
	if r.Score == nil {
		q = "insufficient"
	}
	if q == "complete" {
		q = "available"
	}
	return []Metric{{Day: r.Date, Name: "physiological_recovery", Algorithm: rc.Algorithm, Version: "v1", Value: r.Score, Unit: "score_0_100", Quality: q, Details: map[string]any{"artifactKey": "physiological_recovery", "status": r.Status, "confidence": r.Confidence, "confidenceMeaning": r.ConfidenceMeaning, "candidateOnly": true}}}
}
