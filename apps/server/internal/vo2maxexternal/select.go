package vo2maxexternal

import (
	"encoding/json"
	"math"
	"sort"
)

// SelectionAt resolves explicit supersession only within the effective interval.
// Future recorded controls cannot backfill an earlier capture or evaluation.
func SelectionAt(in ExternalVO2Snapshot) (*Selection, bool) {
	active := map[string]Selection{}
	for _, s := range in.Selections {
		if s.RecordedAt <= in.CapturedAt && s.RecordedAt <= in.AsOf && s.EffectiveFrom <= in.AsOf && (s.EffectiveThrough == nil || in.AsOf < *s.EffectiveThrough) {
			active[s.ID] = s
		}
	}
	superseded := map[string]bool{}
	for _, s := range active {
		if s.Supersedes != "" {
			superseded[s.Supersedes] = true
		}
	}
	remaining := []Selection{}
	for _, s := range active {
		if !superseded[s.ID] {
			remaining = append(remaining, s)
		}
	}
	if len(remaining) != 1 || remaining[0].Revoked {
		return nil, len(remaining) > 1
	}
	return &remaining[0], false
}

type decision struct {
	status, reason string
	when           bool
}

// decide is the executable, ordered status decision table. Technical failures
// have already returned errors and can never become successful null states.
func decide(rows []decision) (string, string) {
	for _, r := range rows {
		if r.when {
			return r.status, r.reason
		}
	}
	return "available", "external_fact_selected"
}
func Calculate(input ExternalVO2Snapshot) (Result, error) {
	in := Canonical(input)
	if len(in.Facts)+len(in.Tombstones) > MaxRefs || len(in.Selections) > 128 {
		return Result{}, ErrLimit
	}
	if !in.Config.Valid() || in.Generation <= 0 || in.AsOf > in.CapturedAt || (in.View != "revised" && in.View != "as_known") {
		return Result{}, ErrInvalid
	}
	if in.View == "as_known" && !in.HistoricalCapable {
		return Result{}, ErrHistorical
	}
	epochs := map[string]bool{}
	ids := map[string]bool{}
	r := Result{Schema: Schema, Dataset: in.Dataset, DatasetEpoch: in.DatasetEpoch, Collector: in.Collector, Unit: Unit, Method: "external_estimate", Provider: "unknown", Quality: "insufficient", ModelVersion: Model, InputSchema: InputSchema, ConfigHash: in.Config.Hash(), Generation: in.Generation, AsOf: in.AsOf, CalculatedAt: in.CapturedAt, EvaluationAt: in.AsOf, KnownThroughRevision: in.KnownThroughRevision, View: in.View, Assignment: in.Assignment, Coverage: in.Coverage, FactRefs: [][2]string{}, TombstoneRefs: [][2]string{}, SelectionRefs: [][2]string{}, Reasons: []string{}, Excluded: map[string]int{}}
	visible := []Fact{}
	for _, f := range in.Facts {
		if ids[f.ID] || f.ID == "" || f.Hash == "" || math.IsNaN(f.Value) || math.IsInf(f.Value, 0) {
			return Result{}, ErrInvalid
		}
		ids[f.ID] = true
		r.FactRefs = append(r.FactRefs, [2]string{f.ID, f.Hash})
		if f.Epoch != "" {
			epochs[f.Epoch] = true
		}
		if f.KnownRevision > in.KnownThroughRevision {
			r.Excluded["future_known"]++
			r.Reasons = append(r.Reasons, "future_known_input")
			continue
		}
		visible = append(visible, f)
	}
	for _, t := range in.Tombstones {
		if ids[t.ID] {
			return Result{}, ErrInvalid
		}
		ids[t.ID] = true
		r.TombstoneRefs = append(r.TombstoneRefs, [2]string{t.ID, t.Hash})
		if t.Epoch != "" {
			epochs[t.Epoch] = true
		}
	}
	if len(epochs) > MaxEpochs {
		return Result{}, ErrLimit
	}
	for _, s := range in.Selections {
		r.SelectionRefs = append(r.SelectionRefs, [2]string{s.ID, s.Hash})
	}
	sel, overlap := SelectionAt(in)
	revoked := false
	if sel != nil {
		r.SelectionID = &sel.ID
		r.SourceEpoch = &sel.Epoch
		for _, t := range in.Tombstones {
			if t.Epoch == sel.Epoch && t.Origin == Apple && t.Revision <= in.KnownThroughRevision && t.End != nil && *t.End <= in.AsOf {
				revoked = true
			}
		}
	}
	candidates := []Fact{}
	future := 0
	conflict := false
	syncContents := map[string]string{}
	for _, f := range visible {
		if !f.Supported {
			r.Excluded["unsupported"]++
			r.Reasons = append(r.Reasons, "unsupported_metadata")
			continue
		}
		if f.End > in.AsOf {
			r.Excluded["future_event"]++
			r.Reasons = append(r.Reasons, "future_dated_input")
			if sel != nil && f.Epoch == sel.Epoch && f.Origin == Apple {
				future++
			}
			continue
		}
		if f.Origin != Apple {
			r.Excluded[f.Origin]++
			if f.Origin == "unclassified" {
				r.Reasons = append(r.Reasons, "source_unknown")
			} else if f.Origin == "user_entered" {
				r.Reasons = append(r.Reasons, "user_entered")
			}
			continue
		}
		if sel != nil && f.Epoch == sel.Epoch {
			candidates = append(candidates, f)
		}
	}
	// Cross-epoch sync conflict is relevant only when the selected epoch has a
	// member of that exact source-scoped key. No source is silently preferred.
	selectedKeys := map[string]bool{}
	for _, f := range candidates {
		if f.SyncKey != "" {
			selectedKeys[f.SyncKey] = true
		}
	}
	for _, f := range visible {
		if f.End > in.AsOf || !f.Supported || !selectedKeys[f.SyncKey] {
			continue
		}
		if prior, ok := syncContents[f.SyncKey]; ok && prior != f.SemanticHash {
			conflict = true
		}
		syncContents[f.SyncKey] = f.SemanticHash
	}
	latest := int64(math.MinInt64)
	ties := []Fact{}
	for _, f := range candidates {
		if f.End > latest {
			latest = f.End
			ties = []Fact{f}
		} else if f.End == latest {
			ties = append(ties, f)
		}
	}
	if len(ties) > MaxTies {
		return Result{}, ErrLimit
	}
	invalid := false
	if len(ties) > 0 {
		f := ties[0]
		origin := Apple
		r.Origin = &origin
		r.Provider = "apple_health"
		// ID order determines canonical evidence serialization only, never a winner.
		for _, other := range ties {
			if other.Value != f.Value || other.SupportHash != f.SupportHash {
				conflict = true
			}
			if other.Start != f.Start {
				r.Reasons = append(r.Reasons, "interval_context_differs")
				if other.Start > f.Start {
					f.Start = other.Start
				}
			}
			if other.Value <= 0 {
				invalid = true
			}
		}
		r.Selected = &Selected{End: f.End, Start: f.Start, TestType: f.TestType, Calibrated: f.Calibrated, EquivalentCount: len(ties)}
		if f.Calibrated == nil {
			r.Reasons = append(r.Reasons, "provider_calibration_unknown")
		} else if !*f.Calibrated {
			r.Reasons = append(r.Reasons, "provider_calibration_insufficient")
		}
	}
	// A deleted current-domain sample must not fall back to an expired predecessor.
	deleted := revoked && (len(ties) == 0 || in.AsOf-latest >= in.Config.ExpireUs)
	age := int64(0)
	if len(ties) > 0 {
		age = in.AsOf - latest
	}
	complete := in.Coverage.Complete && in.Coverage.BootstrapStart != nil && *in.Coverage.BootstrapStart <= in.AsOf-in.Config.ExpireUs && in.Coverage.QueryCompletedAt != nil && *in.Coverage.QueryCompletedAt <= in.CapturedAt
	status, reason := decide([]decision{
		{"sync_incomplete", "coverage_incomplete", !complete},
		{"no_sample_observed", "no_visible_sample", len(visible) == 0 && !revoked},
		{"source_selection_required", "source_selection_required", sel == nil || overlap || len(ties) == 0 && !revoked && future == 0},
		{"conflicting_data", "source_conflict", conflict},
		{"invalid_input", "invalid_input", invalid || len(ties) == 0 && future > 0},
		{"deleted", "sample_deleted", deleted},
		{"expired", "sample_expired", len(ties) > 0 && age >= in.Config.ExpireUs},
		{"stale", "sample_stale", len(ties) > 0 && age >= in.Config.FreshUs},
	})
	r.ModelIdentity = Model
	r.DatasetCommitRevision = in.KnownThroughRevision
	r.SourceLineage = Lineage{OriginRule: in.Config.OriginRule, SourceEpoch: r.SourceEpoch, SelectionInputID: r.SelectionID}
	r.Evidence = Evidence{FactCount: len(in.Facts), TombstoneCount: len(in.Tombstones), SelectionCount: len(in.Selections), Laboratory: "not_available"}
	r.Status = status
	r.Reasons = append(r.Reasons, reason)
	if revoked {
		r.Reasons = append(r.Reasons, "sample_deleted")
		if status == "available" || status == "stale" {
			r.Reasons = append(r.Reasons, "withdrawal_fallback")
		}
	}
	if status == "available" || status == "stale" {
		v := ties[0].Value
		r.Value = &v
		r.Quality = "partial"
	}
	sort.Strings(r.Reasons)
	r.Reasons = unique(r.Reasons)
	b, e := json.Marshal(r)
	if e != nil {
		return Result{}, ErrInvalid
	}
	if len(b) > MaxArtifactBytes {
		return Result{}, ErrLimit
	}
	return r, nil
}
func unique(ss []string) []string {
	out := []string{}
	for _, s := range ss {
		if len(out) == 0 || out[len(out)-1] != s {
			out = append(out, s)
		}
	}
	return out
}
