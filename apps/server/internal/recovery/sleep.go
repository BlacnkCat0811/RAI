package recovery

import "sort"

type episode struct {
	epoch      string
	start, end int64
	facts      []Fact
}
type edge struct {
	at              int64
	category, delta int
}

// sleepDaily sweeps derived union segments. Awake wins over asleep;
// overlapping stages count once and are diagnostic only.
func sleepDaily(fs []Fact, d Day, c Config, asOf int64) SleepSummary {
	out := SleepSummary{Component: missing("sleep")}
	if asOf >= d.End {
		asOf = d.End - 1
	}
	epochs := map[string][]Fact{}
	seen := map[string]bool{}
	for _, f := range fs {
		if f.Type == Sleep && f.End < d.End && !seen[f.SemanticHash] {
			epochs[f.Epoch] = append(epochs[f.Epoch], f)
			seen[f.SemanticHash] = true
		}
	}
	episodes := []episode{}
	for epoch, rows := range epochs {
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].Start == rows[j].Start {
				if rows[i].End == rows[j].End {
					return rows[i].SemanticHash < rows[j].SemanticHash
				}
				return rows[i].End < rows[j].End
			}
			return rows[i].Start < rows[j].Start
		})
		var e episode
		for _, f := range rows {
			if len(e.facts) > 0 && f.Start > e.end+c.EpisodeGapUs {
				episodes = append(episodes, e)
				e = episode{}
			}
			if len(e.facts) == 0 {
				e = episode{epoch: epoch, start: f.Start, end: f.End}
			}
			if f.End > e.end {
				e.end = f.End
			}
			e.facts = append(e.facts, f)
		}
		if len(e.facts) > 0 {
			episodes = append(episodes, e)
		}
	}
	sort.Slice(episodes, func(i, j int) bool {
		if episodes[i].end == episodes[j].end {
			return episodes[i].epoch < episodes[j].epoch
		}
		return episodes[i].end < episodes[j].end
	})
	valid := []SleepSummary{}
	source := map[string]bool{}
	for _, e := range episodes {
		if e.end < d.Start || e.end >= d.End {
			continue
		}
		out.EpisodeCount++
		if e.end+c.EpisodeGapUs > asOf {
			out.Reasons = append(out.Reasons, "sleep_pending_episode")
			continue
		}
		if e.end-e.start > c.MaxEpisodeUs {
			out.Reasons = append(out.Reasons, "sleep_overlap_uncertain")
			out.Quality = "source_conflict"
			continue
		}
		edges := []edge{}
		for _, f := range e.facts {
			edges = append(edges, edge{f.Start, *f.Category, 1}, edge{f.End, *f.Category, -1})
		}
		sort.Slice(edges, func(i, j int) bool { return edges[i].at < edges[j].at })
		counts := [6]int{}
		asleep, awake, unknown := int64(0), int64(0), int64(0)
		overlap := 0
		for i := 0; i < len(edges); {
			at := edges[i].at
			for i < len(edges) && edges[i].at == at {
				counts[edges[i].category] += edges[i].delta
				i++
			}
			if i == len(edges) {
				break
			}
			dt := edges[i].at - at
			stages := 0
			for _, k := range []int{1, 3, 4, 5} {
				if counts[k] > 0 {
					stages++
				}
			}
			if stages > 1 || stages > 0 && counts[2] > 0 {
				overlap++
			}
			if counts[2] > 0 {
				awake += dt
			} else if stages > 0 {
				asleep += dt
			} else {
				unknown += dt
			}
		}
		if asleep <= c.NapMaxUs {
			out.NapCount++
			continue
		}
		source[e.epoch] = true
		s := SleepSummary{Component: Component{Value: ptr(float64(asleep) / float64(Hour)), Epoch: e.epoch, Count: 1, LastEvent: &e.end, Quality: "complete", Reasons: []string{}}, AwakeHours: ptr(float64(awake) / float64(Hour)), UnknownHours: ptr(float64(unknown) / float64(Hour)), Continuity: ptr(float64(asleep) / float64(asleep+awake)), Coverage: ptr(float64(asleep+awake) / float64(e.end-e.start)), EpisodeStart: &e.start, EpisodeEnd: &e.end, StageOverlapSegments: overlap}
		if unknown > 0 {
			s.Reasons = append(s.Reasons, "sleep_coverage_partial")
		}
		for _, f := range e.facts {
			s.ContextIncomplete = s.ContextIncomplete || f.ContextIncomplete
		}
		if overlap > 0 {
			s.Reasons = append(s.Reasons, "sleep_overlap_uncertain")
		}
		if *s.Coverage < c.SleepCoverageMinimum {
			s.Value = nil
			s.Quality = "insufficient"
			s.Reasons = append(s.Reasons, "sleep_coverage_incomplete")
		}
		valid = append(valid, s)
	}
	if len(source) > 1 {
		out.Quality = "source_conflict"
		out.Reasons = []string{"sleep_source_conflict"}
		return out
	}
	if out.Quality == "source_conflict" {
		out.Reasons = reasons(out.Reasons...)
		return out
	}
	if len(valid) == 0 {
		return out
	}
	sort.Slice(valid, func(i, j int) bool {
		a, b := valid[i], valid[j]
		if a.Value == nil {
			return false
		}
		if b.Value == nil {
			return true
		}
		if *a.Value == *b.Value {
			return *a.EpisodeEnd > *b.EpisodeEnd
		}
		return *a.Value > *b.Value
	})
	chosen := valid[0]
	chosen.EpisodeCount = out.EpisodeCount
	chosen.NapCount = out.NapCount
	return chosen
}
