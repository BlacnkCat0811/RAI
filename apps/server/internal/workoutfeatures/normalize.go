package workoutfeatures

import (
	"bytes"
	"encoding/json"
	"sort"
)

func canonicalRaw(b json.RawMessage) (json.RawMessage, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var v any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if d.Decode(&v) != nil {
		return nil, failure("feature_invalid_input")
	}
	out, e := json.Marshal(v)
	return out, e
}
func unique[T any](xs []T) []T {
	type item struct {
		key   string
		value T
	}
	a := make([]item, 0, len(xs))
	for _, x := range xs {
		b, _ := json.Marshal(x)
		a = append(a, item{string(b), x})
	}
	sort.Slice(a, func(i, j int) bool { return a[i].key < a[j].key })
	out := make([]T, 0, len(a))
	last := ""
	for i, x := range a {
		if i == 0 || x.key != last {
			out = append(out, x.value)
		}
		last = x.key
	}
	return out
}
func reasonSet(rs ...[]Reason) []Reason {
	a := []Reason{}
	for _, r := range rs {
		a = append(a, r...)
	}
	return unique(a)
}
func sourceAllowed(s Source) bool {
	return s.BundleID != "" && (s.Classification == "apple_watch" || s.Classification == "associated_apple_watch")
}
func NewSource(bundle, classification string, descriptor json.RawMessage) Source {
	b, _ := canonicalRaw(descriptor)
	s := Source{BundleID: bundle, Classification: classification, Descriptor: b}
	s.Epoch = digest(s)
	return s
}

// Normalize returns a deep copy; it never edits source slices or metadata.
func Normalize(in RawWorkoutSnapshot) (RawWorkoutSnapshot, error) {
	total := len(in.Quantities)
	routes := 0
	details := len(in.Events) + len(in.Activities) + len(in.Statistics)
	metadata := len(in.Metadata)
	for _, e := range in.Enrichments {
		metadata += len(e.Metadata)
	}
	for _, s := range in.Series {
		total += len(s.Points)
		for _, q := range s.Points {
			metadata += len(q.Metadata)
		}
	}
	for _, r := range in.Routes {
		routes += len(r.Points)
	}
	for _, a := range in.Activities {
		details += len(a.Events) + len(a.Statistics)
		metadata += len(a.Metadata) + len(a.Configuration)
		for _, e := range a.Events {
			metadata += len(e.Metadata)
		}
	}
	for _, e := range in.Events {
		metadata += len(e.Metadata)
	}
	for _, q := range in.Quantities {
		metadata += len(q.Metadata)
	}
	for _, s := range in.Provenance {
		metadata += len(s.Descriptor)
	}
	if in.End >= in.Start && in.End-in.Start > MaxSeconds*1e6 {
		return RawWorkoutSnapshot{}, failure("input_limit")
	}
	if total > MaxPoints || routes > MaxPoints || details > MaxDetails || metadata > MaxMetadataBytes || len(in.References) > MaxPoints+MaxDetails || len(in.Parameters) > 256 || len(in.Provenance) > MaxDetails || len(in.Series) > MaxDetails || len(in.Routes) > MaxDetails || len(in.Availability) > MaxPoints+MaxDetails || len(in.Enrichments) > MaxDetails {
		return RawWorkoutSnapshot{}, failure("input_limit")
	}
	if in.Policy != DefaultPolicy() || in.AlgorithmVersion != Algorithm+"/"+Version || in.DatasetID == "" || in.WorkoutID == "" || in.Generation < 1 || in.End < in.Start || in.End-in.Start > MaxSeconds*1e6 || in.DurationUs < 0 || in.DurationUs > in.End-in.Start {
		return RawWorkoutSnapshot{}, failure("feature_invalid_input")
	}
	if in.Distance != nil && (!finite(*in.Distance) || *in.Distance < 0) {
		return RawWorkoutSnapshot{}, failure("feature_invalid_input")
	}
	b, e := json.Marshal(in)
	if e != nil {
		return RawWorkoutSnapshot{}, failure("feature_invalid_input")
	}
	var n RawWorkoutSnapshot
	if json.Unmarshal(b, &n) != nil {
		return n, failure("feature_invalid_input")
	}
	fix := func(p *json.RawMessage) {
		if e != nil {
			return
		}
		*p, e = canonicalRaw(*p)
	}
	fix(&n.Metadata)
	for i := range n.Enrichments {
		fix(&n.Enrichments[i].Metadata)
	}
	for i := range n.Series {
		for j := range n.Series[i].Points {
			fix(&n.Series[i].Points[j].Metadata)
		}
	}
	for i := range n.Provenance {
		fix(&n.Provenance[i].Descriptor)
	}
	for i := range n.Events {
		fix(&n.Events[i].Metadata)
	}
	for i := range n.Activities {
		a := &n.Activities[i]
		fix(&a.Metadata)
		fix(&a.Configuration)
		for j := range a.Events {
			fix(&a.Events[j].Metadata)
		}
		a.Events = unique(a.Events)
		a.Statistics = unique(a.Statistics)
	}
	for i := range n.Quantities {
		fix(&n.Quantities[i].Metadata)
	}
	if e != nil {
		return n, failure("feature_invalid_input")
	}
	sourceEpochs := map[string]string{}
	for _, src := range n.Provenance {
		key := digest(src)
		if old, ok := sourceEpochs[src.Epoch]; ok && old != key {
			return n, failure("feature_invalid_input")
		}
		sourceEpochs[src.Epoch] = key
	}
	n.Enrichments = unique(n.Enrichments)
	n.Quantities = unique(n.Quantities)
	n.Events = unique(n.Events)
	n.Activities = unique(n.Activities)
	n.Statistics = unique(n.Statistics)
	n.Provenance = unique(n.Provenance)
	n.Parameters = unique(n.Parameters)
	n.References = unique(n.References)
	for i := range n.Series {
		n.Series[i].Points = unique(n.Series[i].Points)
	}
	n.Series = unique(n.Series)
	for i := range n.Routes {
		n.Routes[i].Points = unique(n.Routes[i].Points)
		sort.Slice(n.Routes[i].Points, func(a, b int) bool { return n.Routes[i].Points[a].Sequence < n.Routes[i].Points[b].Sequence })
	}
	n.Routes = unique(n.Routes)
	return n, nil
}
func (s RawWorkoutSnapshot) Hash() string { return digest(s) }
func resolveParameter(s RawWorkoutSnapshot) (*Parameter, []Reason) {
	superseded := map[string]bool{}
	eligible := []Parameter{}
	for _, p := range s.Parameters {
		if p.RecordedAt <= s.AsOf && p.EffectiveFrom <= s.Start && (p.EffectiveThrough == nil || s.Start < *p.EffectiveThrough) {
			eligible = append(eligible, p)
			if p.Supersedes != "" {
				superseded[p.Supersedes] = true
			}
		}
	}
	var selected *Parameter
	for i := range eligible {
		p := eligible[i]
		if superseded[p.ID] {
			continue
		}
		if selected != nil {
			return nil, []Reason{ParameterConflict}
		}
		selected = &p
	}
	if selected == nil || selected.Revoked {
		return nil, []Reason{ParameterMissing}
	}
	if selected.ID == "" || selected.Hash == "" || selected.Provenance == "" || !finite(selected.RestHR) || !finite(selected.MaxHR) || selected.RestHR <= 0 || selected.MaxHR <= selected.RestHR {
		return nil, []Reason{ParameterConflict}
	}
	return selected, nil
}
