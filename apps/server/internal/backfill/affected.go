package backfill

import (
	"sort"
)

// Model names used by affected-range closure. They are stable planning names,
// not artifact keys.
const (
	ModelLoad      = "load"
	ModelTrend     = "trend"
	ModelReference = "reference"
	ModelRecovery  = "recovery"
	ModelReadiness = "readiness"
	ModelRHR       = "rhr"
	ModelVO2       = "vo2"
	ModelAbility   = "ability"
	ModelFeatures  = "features"
)

// FactKind identifies the changed input family passed to AffectedRanges.
type FactKind string

const (
	FactWorkoutLoad FactKind = "workout_load"
	FactRHR         FactKind = "rhr"
	FactVO2         FactKind = "vo2"
	FactAbility     FactKind = "ability"
	FactSleep       FactKind = "sleep"
	FactDaily       FactKind = "daily"
	FactOverlap     FactKind = "overlap"
	FactSource      FactKind = "source"
	FactAnnotation  FactKind = "annotation"
)

// FactChange is one changed fact. Supply either Day for a single day or
// From/Through for an inclusive span. If From/Through are present they take
// precedence over Day.
type FactChange struct {
	Kind    FactKind `json:"kind"`
	Day     string   `json:"day,omitempty"`
	From    string   `json:"from,omitempty"`
	Through string   `json:"through,omitempty"`
}

// Range is one inclusive derived-output range that must be recomputed.
type Range struct {
	Model   string `json:"model"`
	From    string `json:"from"`
	Through string `json:"through"`
	Reason  string `json:"reason"`
}

// AffectedRanges is the pure dependency-closure operator. It maps a changed
// input fact to the inclusive output ranges that can depend on it. The windows
// are model-specific; no single global 180-day window is applied.
//
// Covered closures:
//   - workout_load: load d..d+179, trend d..d+186, readiness through d+207.
//   - rhr: reference d+1..d+28, then load through d+207, recovery baseline
//     d..d+28, and readiness load closure through d+207. This is the
//     rhr -> reference -> load/readiness chain, not a fixed 180-day tail.
//   - vo2: pool/smoothing d..d+47 plus held current/lastKnown reference
//     through d+74.
//   - ability: pool/smoothing d..d+102 plus held current/lastKnown reference
//     through d+129.
//   - sleep/daily: signal neighborhood d-1..d+2, 28-day baseline d..d+28 and
//     the two-day persistent-deviation tail d..d+30; readiness follows recovery.
//   - overlap/source/annotation: the fact neighborhood plus the corresponding
//     model windows and held-reference closures.
//
// Invalid kinds or dates return nil. The result is sorted deterministically and
// contains no scalar health values.
func AffectedRanges(change FactChange) []Range {
	from, through, ok := change.span()
	if !ok {
		return nil
	}
	var out []Range
	switch change.Kind {
	case FactWorkoutLoad:
		out = []Range{
			{Model: ModelLoad, From: from, Through: addDays(through, 179), Reason: "load_180d_tail"},
			{Model: ModelTrend, From: from, Through: addDays(through, 186), Reason: "trend_7d_tail"},
			{Model: ModelReadiness, From: from, Through: addDays(through, 207), Reason: "workout_load_readiness_closure"},
		}
	case FactRHR:
		out = []Range{
			{Model: ModelReference, From: addDays(from, 1), Through: addDays(through, 28), Reason: "rhr_reference_28d"},
			{Model: ModelLoad, From: addDays(from, 1), Through: addDays(through, 207), Reason: "rhr_load_closure_180d"},
			{Model: ModelRecovery, From: from, Through: addDays(through, 28), Reason: "rhr_recovery_baseline_28d"},
			{Model: ModelReadiness, From: from, Through: addDays(through, 207), Reason: "rhr_readiness_load_closure"},
		}
	case FactVO2:
		out = []Range{
			{Model: ModelVO2, From: from, Through: addDays(through, 47), Reason: "vo2_pool_42d_smoothing_6d"},
			{Model: ModelVO2, From: from, Through: addDays(through, 74), Reason: "vo2_held_reference_27d"},
		}
	case FactAbility:
		out = []Range{
			{Model: ModelAbility, From: from, Through: addDays(through, 102), Reason: "ability_pool_90d_smoothing_13d"},
			{Model: ModelAbility, From: from, Through: addDays(through, 129), Reason: "ability_held_reference_27d"},
		}
	case FactSleep, FactDaily:
		prefix := "sleep"
		if change.Kind == FactDaily {
			prefix = "daily"
		}
		out = []Range{
			{Model: ModelRecovery, From: addDays(from, -1), Through: addDays(through, 2), Reason: prefix + "_signal_neighborhood"},
			{Model: ModelRecovery, From: from, Through: addDays(through, 28), Reason: prefix + "_baseline_28d"},
			{Model: ModelRecovery, From: addDays(through, 29), Through: addDays(through, 30), Reason: prefix + "_persistent_tail_2d"},
			{Model: ModelReadiness, From: from, Through: addDays(through, 30), Reason: prefix + "_readiness_closure"},
		}
	case FactOverlap:
		out = []Range{
			{Model: ModelFeatures, From: addDays(from, -1), Through: addDays(through, 1), Reason: "overlap_neighborhood"},
			{Model: ModelLoad, From: from, Through: addDays(through, 179), Reason: "overlap_load_closure"},
			{Model: ModelTrend, From: from, Through: addDays(through, 186), Reason: "overlap_trend_closure"},
			{Model: ModelVO2, From: from, Through: addDays(through, 47), Reason: "overlap_vo2_closure"},
			{Model: ModelVO2, From: from, Through: addDays(through, 74), Reason: "overlap_vo2_held_reference"},
			{Model: ModelAbility, From: from, Through: addDays(through, 102), Reason: "overlap_ability_closure"},
			{Model: ModelAbility, From: from, Through: addDays(through, 129), Reason: "overlap_ability_held_reference"},
			{Model: ModelReadiness, From: from, Through: addDays(through, 207), Reason: "overlap_readiness_closure"},
		}
	case FactSource:
		out = []Range{
			{Model: ModelFeatures, From: addDays(from, -1), Through: addDays(through, 1), Reason: "source_neighborhood"},
			{Model: ModelLoad, From: from, Through: addDays(through, 179), Reason: "source_load_closure"},
			{Model: ModelTrend, From: from, Through: addDays(through, 186), Reason: "source_trend_closure"},
			{Model: ModelVO2, From: from, Through: addDays(through, 47), Reason: "source_vo2_closure"},
			{Model: ModelVO2, From: from, Through: addDays(through, 74), Reason: "source_vo2_held_reference"},
			{Model: ModelAbility, From: from, Through: addDays(through, 102), Reason: "source_ability_closure"},
			{Model: ModelAbility, From: from, Through: addDays(through, 129), Reason: "source_ability_held_reference"},
			{Model: ModelRecovery, From: addDays(from, -1), Through: addDays(through, 30), Reason: "source_recovery_closure"},
			{Model: ModelReadiness, From: addDays(from, -1), Through: addDays(through, 207), Reason: "source_readiness_closure"},
		}
	case FactAnnotation:
		out = []Range{
			{Model: ModelAbility, From: from, Through: addDays(through, 102), Reason: "annotation_ability_closure"},
			{Model: ModelAbility, From: from, Through: addDays(through, 129), Reason: "annotation_ability_held_reference"},
		}
	default:
		return nil
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Model != out[j].Model {
			return out[i].Model < out[j].Model
		}
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		if out[i].Through != out[j].Through {
			return out[i].Through < out[j].Through
		}
		return out[i].Reason < out[j].Reason
	})
	return out
}

func (c FactChange) span() (string, string, bool) {
	from, through := c.From, c.Through
	if from == "" && through == "" {
		from, through = c.Day, c.Day
	}
	if from == "" {
		from = through
	}
	if through == "" {
		through = from
	}
	start, err := parseDay(from)
	if err != nil {
		return "", "", false
	}
	end, err := parseDay(through)
	if err != nil || end.Before(start) {
		return "", "", false
	}
	return from, through, true
}
