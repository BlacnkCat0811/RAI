package hrref

import "sort"

type Interval struct {
	Start int64 `json:"startUs"`
	End   int64 `json:"endUs"`
}

type Workout struct {
	ID            string     `json:"id"`
	DatasetID     string     `json:"datasetID"`
	ActivityType  int64      `json:"activityType"`
	Start         int64      `json:"startUs"`
	End           int64      `json:"endUs"`
	Active        []Interval `json:"activeIntervals"`
	ActiveCertain bool       `json:"activeCertain"`
	Deleted       bool       `json:"deleted"`
}

type Overlap struct {
	First   string `json:"firstWorkoutID"`
	Second  string `json:"secondWorkoutID"`
	Start   int64  `json:"startUs"`
	End     int64  `json:"endUs"`
	Seconds int64  `json:"overlapSeconds"`
	Certain bool   `json:"certain"`
}

func intervals(w Workout) []Interval {
	if w.Deleted || w.ActivityType != 37 || w.End <= w.Start {
		return nil
	}
	if w.ActiveCertain && len(w.Active) > 0 {
		out := append([]Interval{}, w.Active...)
		sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
		return out
	}
	return []Interval{{w.Start, w.End}}
}

// Detect returns every strictly positive intersection between locatable active
// intervals of distinct running workouts. Touching endpoints do not overlap.
// The result is independent of input ordering.
func Detect(ws []Workout) []Overlap {
	sorted := append([]Workout{}, ws...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	out := []Overlap{}
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[i].DatasetID != "" && sorted[j].DatasetID != "" && sorted[i].DatasetID != sorted[j].DatasetID {
				continue
			}
			for _, a := range intervals(sorted[i]) {
				for _, b := range intervals(sorted[j]) {
					start, end := max64(a.Start, b.Start), min64(a.End, b.End)
					if end > start {
						certain := sorted[i].ActiveCertain && sorted[j].ActiveCertain
						out = append(out, Overlap{First: sorted[i].ID, Second: sorted[j].ID, Start: start, End: end, Seconds: (end - start) / 1_000_000, Certain: certain})
					}
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Start != out[j].Start {
			return out[i].Start < out[j].Start
		}
		if out[i].First != out[j].First {
			return out[i].First < out[j].First
		}
		return out[i].Second < out[j].Second
	})
	// Merge adjacent identical pair segments produced by touching source intervals.
	merged := out[:0]
	for _, o := range out {
		if len(merged) > 0 {
			p := &merged[len(merged)-1]
			if p.First == o.First && p.Second == o.Second && p.End == o.Start {
				p.End = o.End
				p.Seconds = (p.End - p.Start) / 1_000_000
				continue
			}
		}
		merged = append(merged, o)
	}
	return merged
}

// Covered returns the union duration of locatable active coverage and whether
// any interval was uncertain (whole-event fallback) or overlapped. The value is
// a non-overlap lower bound when unresolved is true.
func Covered(ws []Workout) (seconds int64, unresolved bool) {
	type span struct{ s, e int64 }
	spans := []span{}
	ids := map[string]bool{}
	for _, w := range ws {
		if w.Deleted || w.ActivityType != 37 {
			continue
		}
		if ids[w.ID] {
			unresolved = true
		}
		ids[w.ID] = true
		if !w.ActiveCertain || len(w.Active) == 0 {
			unresolved = true
		}
		for _, a := range intervals(w) {
			if a.End > a.Start {
				spans = append(spans, span{a.Start, a.End})
			}
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].s < spans[j].s })
	var cur *span
	for _, s := range spans {
		if cur == nil {
			x := s
			cur = &x
			continue
		}
		if s.s <= cur.e {
			if s.e > cur.e {
				cur.e = s.e
			}
		} else {
			seconds += (cur.e - cur.s) / 1_000_000
			x := s
			cur = &x
		}
	}
	if cur != nil {
		seconds += (cur.e - cur.s) / 1_000_000
	}
	return seconds, unresolved || len(Detect(ws)) > 0
}

type Resolution struct {
	ID          string `json:"id"`
	DatasetID   string `json:"datasetID"`
	PrimaryID   string `json:"primaryWorkoutID"`
	DuplicateID string `json:"duplicateWorkoutID"`
	KnownAt     int64  `json:"knownAtUs"`
	Revoked     bool   `json:"revoked"`
	Reason      string `json:"reason"`
}

type ResolutionResult struct {
	Suppressed []string `json:"suppressedWorkoutIDs"`
	Conflicts  []string `json:"conflictingWorkoutIDs"`
	Reasons    []string `json:"reasonCodes"`
}

// ApplyResolutions only affects derived inclusion. It never changes workout
// facts, intervals or deletion state. Conflicting resolutions fail closed.
func ApplyResolutions(ws []Workout, rs []Resolution, asOf int64) ResolutionResult {
	out := ResolutionResult{Suppressed: []string{}, Conflicts: []string{}, Reasons: []string{}}
	byID := map[string]bool{}
	for _, w := range ws {
		byID[w.ID] = true
	}
	pair := map[string][]Resolution{}
	for _, r := range rs {
		if r.Revoked || r.KnownAt > asOf || r.PrimaryID == "" || r.DuplicateID == "" || r.PrimaryID == r.DuplicateID {
			continue
		}
		if !byID[r.PrimaryID] || !byID[r.DuplicateID] {
			out.Reasons = append(out.Reasons, "resolution_subject_missing")
			continue
		}
		key := r.PrimaryID + "|" + r.DuplicateID
		pair[key] = append(pair[key], r)
	}
	dup := map[string]bool{}
	conflict := map[string]bool{}
	for _, rs := range pair {
		if len(rs) > 1 {
			for _, r := range rs {
				conflict[r.PrimaryID], conflict[r.DuplicateID] = true, true
			}
			continue
		}
		dup[rs[0].DuplicateID] = true
	}
	for id := range dup {
		if !conflict[id] {
			out.Suppressed = append(out.Suppressed, id)
		}
	}
	for id := range conflict {
		out.Conflicts = append(out.Conflicts, id)
	}
	if len(out.Conflicts) > 0 {
		out.Reasons = append(out.Reasons, "overlap_resolution_conflict")
	}
	sort.Strings(out.Suppressed)
	sort.Strings(out.Conflicts)
	out.Reasons = dedupe(out.Reasons)
	return out
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
