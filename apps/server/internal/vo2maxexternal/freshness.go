package vo2maxexternal

// Freshen changes a returned copy only; persisted artifacts remain immutable.
func Freshen(r Result, evaluationAt int64) (Result, error) {
	if evaluationAt < r.AsOf {
		return Result{}, ErrInvalid
	}
	r.EvaluationAt = evaluationAt
	if (r.Status == "available" || r.Status == "stale") && r.Selected != nil {
		age := evaluationAt - r.Selected.End
		switch {
		case age >= 90*DayUs:
			r.Status = "expired"
			r.Value = nil
			r.Quality = "insufficient"
			r.Reasons = append(append([]string{}, r.Reasons...), "sample_expired")
		case age >= 30*DayUs:
			r.Status = "stale"
		}
	}
	return r, nil
}
