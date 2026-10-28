package training

func Compare(e Execution, w *Workout) Feedback {
	f := Feedback{ExecutionID: e.ID, ExecutionRevision: e.Revision, Version: FeedbackVersion, Date: e.Snapshot.Date, Status: "completed", Quality: "insufficient", Duration: Comparison{Planned: e.Snapshot.DurationS}, Distance: Comparison{Planned: e.Snapshot.DistanceM}, Reasons: []string{}}
	if e.Status == "skipped" {
		f.Status = "skipped"
		f.Quality = "available"
		f.Reasons = append(f.Reasons, "explicit_skip")
		return f
	}
	if w == nil {
		f.Reasons = append(f.Reasons, "analysis_pending")
		return f
	}
	f.SourceRunID = w.RunID
	f.ActualLoad = w.Load
	compare := func(c *Comparison, actual *float64) int {
		c.Actual = actual
		if actual == nil || c.Planned <= 0 || !Finite(*actual) {
			return 0
		}
		c.Delta = Ptr(*actual - c.Planned)
		c.Ratio = Ptr(*actual / c.Planned)
		if *c.Ratio < .9 {
			return -1
		}
		if *c.Ratio > 1.1 {
			return 1
		}
		return 2
	}
	a, b := compare(&f.Duration, w.DurationS), compare(&f.Distance, w.DistanceM)
	if a == 0 && b == 0 {
		f.Reasons = append(f.Reasons, "no_comparable_target")
		return f
	}
	f.Quality = "partial"
	f.Status = "on_target"
	if a == -1 || b == -1 {
		f.Status = "under_target"
	}
	if a == 1 || b == 1 {
		f.Status = "over_target"
	}
	if a*b == -1 {
		f.Status = "mixed"
		f.Reasons = append(f.Reasons, "duration_distance_disagree")
	}
	return f
}
