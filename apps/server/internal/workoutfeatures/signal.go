package workoutfeatures

import (
	"fmt"
	"math"
	"rai/server/internal/workoutdomain"
	"sort"
)

// Span is an ephemeral analysis primitive, never persisted as a duplicate trace.
type Span struct {
	Start, End    int64
	V0, V1        float64
	Epoch         string
	Reconstructed bool
	ActiveSegment int
	SupportGap    float64
}

func (s Span) value(at int64) float64 {
	return s.V0 + (s.V1-s.V0)*float64(at-s.Start)/float64(s.End-s.Start)
}
func (s Span) integral(a, b int64) float64 {
	return (s.value(a) + s.value(b)) * 0.5 * float64(b-a) / 1e6
}

type Signal struct {
	Spans   []Span
	Summary SignalSummary
}

func unit(kind string) string {
	switch kind {
	case HR:
		return "count/min"
	case Speed:
		return "m/s"
	case Steps:
		return "count"
	case Power:
		return "W"
	case GCT:
		return "ms"
	case Stride, Vertical:
		return "m"
	}
	return ""
}
func BuildSignal(s RawWorkoutSnapshot, p PauseTimeline, kind string) Signal {
	out := Signal{Summary: SignalSummary{State: Missing, SourceResolution: "no_supported_source", SourceEpochs: []string{}, Reasons: []Reason{}}}
	sources := map[string]Source{}
	for _, x := range s.Provenance {
		sources[x.Epoch] = x
	}
	sealed := map[string]bool{}
	deleted := map[string]bool{}
	for _, q := range s.Quantities {
		if q.Fact.State == Deleted {
			deleted[q.Fact.ID] = true
		}
	}
	qs := []Quantity{}
	for _, series := range s.Series {
		if series.Type != kind || series.Fact.State == Deleted || series.Fact.State == Unsupported || deleted[series.ParentID] {
			continue
		}
		sealed[series.ParentID] = true
		for _, q := range series.Points {
			q.Type = series.Type
			q.Source = series.Source
			q.Support = SealedSeries
			qs = append(qs, q)
		}
	}
	condensed := []Quantity{}
	for _, q := range s.Quantities {
		if q.Type != kind || (q.Fact.State == Deleted || q.Fact.State == Missing) || sealed[q.Fact.ID] {
			continue
		}
		if q.Support == CondensedParent {
			condensed = append(condensed, q)
			continue
		}
		qs = append(qs, q)
	}
	// Physical support deduplication is independent of fact UUID/series chunking.
	type reading struct {
		Start, End int64
		Value      float64
		Epoch      string
		Support    TimeSupport
		Bad        bool
	}
	readings := []reading{}
	reasons := []Reason{}
	for _, q := range qs {
		bad := !finite(q.Value) || q.Value < 0 || q.Unit != unit(kind) || q.End < q.Start || q.Support != InstantPoint && q.Support != IntervalObservation && q.Support != SealedSeries
		if !sourceAllowed(sources[q.Source]) {
			reasons = append(reasons, SourceUnknown)
			bad = true
		}
		if q.Fact.State == Ambiguous {
			reasons = append(reasons, SourceConflict)
		}
		if q.Fact.State == Ambiguous || q.Fact.State == Unsupported {
			reasons = append(reasons, UnsupportedInput)
			bad = true
		}
		if kind == HR && (q.Value < 30 || q.Value >= s.Policy.HRSpikeBPM) || kind == Speed && (q.Value < 0.5 || q.Value > s.Policy.MaxSpeedMPS) {
			reasons = append(reasons, SignalOutlier)
			bad = true
		}
		if bad {
			reasons = append(reasons, InvalidObservation)
		}
		readings = append(readings, reading{q.Start, q.End, q.Value, q.Source, q.Support, bad})
	}
	readings = unique(readings)
	sort.Slice(readings, func(i, j int) bool {
		a, b := readings[i], readings[j]
		if a.Epoch != b.Epoch {
			return a.Epoch < b.Epoch
		}
		if a.Start != b.Start {
			return a.Start < b.Start
		}
		if a.End != b.End {
			return a.End < b.End
		}
		return a.Value < b.Value
	})
	spans := []Span{}
	add := func(a, b int64, v0, v1 float64, epoch string, reconstructed bool) {
		if b <= a {
			return
		}
		original := Span{Start: a, End: b, V0: v0, V1: v1, Epoch: epoch, Reconstructed: reconstructed}
		if reconstructed {
			original.SupportGap = float64(b-a) / 1e6
		}
		i := sort.Search(len(p.Active), func(i int) bool { return p.Active[i].End > a })
		for ; i < len(p.Active) && p.Active[i].Start < b; i++ {
			x := p.Active[i]
			lo, hi := max(a, x.Start, s.Start), min(b, x.End, s.End)
			if lo >= hi {
				continue
			}
			spans = append(spans, Span{Start: lo, End: hi, V0: original.value(lo), V1: original.value(hi), Epoch: epoch, Reconstructed: reconstructed, ActiveSegment: i, SupportGap: original.SupportGap})
		}
	}
	for i, q := range readings {
		if q.Bad {
			continue
		}
		if q.End > q.Start {
			v := q.Value
			if kind == Steps {
				v = workoutdomain.CadenceProxy(q.Value, float64(q.End-q.Start)/1e6)
			}
			add(q.Start, q.End, v, v, q.Epoch, false)
			continue
		}
		// Contradictory points at the same timestamp are barriers, including their
		// adjacent interpolation edges; no averaging or skipping over the conflict.
		conflict := func(j int) bool {
			return j >= 0 && j < len(readings) && readings[j].Epoch == q.Epoch && readings[j].Start == q.Start && readings[j].End == q.Start && (readings[j].Value != q.Value || readings[j].Bad)
		}
		if conflict(i-1) || conflict(i+1) {
			reasons = append(reasons, SourceConflict)
			continue
		}
		if kind == Steps || i+1 >= len(readings) {
			continue
		}
		next := readings[i+1]
		if next.Bad || next.End != next.Start || next.Epoch != q.Epoch || next.Start <= q.Start || next.Start-q.Start > int64(s.Policy.MaxGapSeconds)*1e6 {
			continue
		}
		if i+2 < len(readings) && readings[i+2].Epoch == q.Epoch && readings[i+2].Start == next.Start && readings[i+2].End == next.Start && readings[i+2].Value != next.Value {
			reasons = append(reasons, SourceConflict)
			continue
		}
		dt := float64(next.Start-q.Start) / 1e6
		if kind == HR && dt <= 5 && math.Abs(next.Value-q.Value) > 30 || kind == Speed && dt <= 5 && math.Abs(next.Value-q.Value)/dt > 3 {
			reasons = append(reasons, SignalOutlier)
			continue
		}
		if containing(p.Active, q.Start, next.Start) < 0 {
			continue
		}
		add(q.Start, next.Start, q.Value, next.Value, q.Epoch, true)
	}
	type edge struct {
		at    int64
		key   string
		delta int
		span  Span
	}
	edges := make([]edge, 0, len(spans)*2)
	for _, x := range spans {
		key := fmt.Sprintf("%s:%d:%t:%.17g", x.Epoch, x.ActiveSegment, x.Reconstructed, x.V0)
		if x.V1 != x.V0 {
			key += fmt.Sprintf(":%d:%d:%.17g", x.Start, x.End, x.V1)
		}
		edges = append(edges, edge{x.Start, key, 1, x}, edge{x.End, key, -1, x})
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].at < edges[j].at })
	type live struct {
		count int
		span  Span
	}
	active := map[string]live{}
	var last int64
	ambiguous := float64(0)
	for i := 0; i < len(edges); {
		at := edges[i].at
		if at > last && len(active) == 1 {
			for _, v := range active {
				x := v.span
				out.Spans = append(out.Spans, Span{Start: last, End: at, V0: x.value(last), V1: x.value(at), Epoch: x.Epoch, Reconstructed: x.Reconstructed, ActiveSegment: x.ActiveSegment, SupportGap: x.SupportGap})
			}
		} else if at > last && len(active) > 1 {
			ambiguous += float64(at-last) / 1e6
			reasons = append(reasons, SourceConflict)
		}
		for i < len(edges) && edges[i].at == at {
			e := edges[i]
			v := active[e.key]
			v.count += e.delta
			if e.delta > 0 {
				v.span = e.span
			}
			if v.count == 0 {
				delete(active, e.key)
			} else {
				active[e.key] = v
			}
			i++
		}
		last = at
	}
	var covered, observed, area float64
	epochs := []string{}
	for _, x := range out.Spans {
		d := float64(x.End-x.Start) / 1e6
		covered += d
		out.Summary.MaxGap = math.Max(out.Summary.MaxGap, x.SupportGap)
		if !x.Reconstructed {
			observed += d
		}
		area += x.integral(x.Start, x.End)
		epochs = append(epochs, x.Epoch)
	}
	activeSeconds := float64(0)
	if p.Timing.Active != nil {
		activeSeconds = *p.Timing.Active
	}
	out.Summary.AmbiguousSeconds = ambiguous
	out.Summary.SourceEpochs = unique(epochs)
	if len(out.Summary.SourceEpochs) == 1 {
		out.Summary.SourceResolution = "sole_supported_source"
	} else if len(out.Summary.SourceEpochs) > 1 {
		out.Summary.SourceResolution = "non_overlapping_source_epochs"
	}
	if ambiguous > 0 {
		out.Summary.SourceResolution = "conflicting_support_excluded"
	}
	out.Summary.Reasons = reasonSet(reasons)
	if activeSeconds > 0 {
		out.Summary.ObservedCoverage = observed / activeSeconds
		out.Summary.ReconstructedCoverage = covered / activeSeconds
		out.Summary.ExcludedSeconds = math.Max(0, activeSeconds-covered)
		if covered > 0 {
			out.Summary.InterpolatedFraction = (covered - observed) / covered
		}
	}
	for _, a := range p.Active {
		previous := a.Start
		i := sort.Search(len(out.Spans), func(i int) bool { return out.Spans[i].End > a.Start })
		for ; i < len(out.Spans) && out.Spans[i].Start < a.End; i++ {
			x := out.Spans[i]
			out.Summary.MaxGap = math.Max(out.Summary.MaxGap, float64(x.Start-previous)/1e6)
			previous = x.End
		}
		out.Summary.MaxGap = math.Max(out.Summary.MaxGap, float64(a.End-previous)/1e6)
	}
	if covered > 0 {
		out.Summary.Mean = ptr(area / covered)
		out.Summary.State = Present
	} else if ambiguous > 0 {
		out.Summary.State = Ambiguous
	} else if len(condensed) > 0 {
		out.Summary.State = Condensed
	}
	return out
}
