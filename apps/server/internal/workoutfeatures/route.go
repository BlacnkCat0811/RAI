package workoutfeatures

import (
	"math"
	"rai/server/internal/workoutdomain"
	"sort"
)

type RouteSupport struct {
	Summary RouteSummary
	Grades  []Span
}

func BuildRoute(s RawWorkoutSnapshot, p PauseTimeline) RouteSupport {
	out := RouteSupport{Summary: RouteSummary{State: Missing, SourceEpochs: []string{}, Reasons: []Reason{GradeUnknown}}}
	sources := map[string]Source{}
	for _, x := range s.Provenance {
		sources[x.Epoch] = x
	}
	type located struct {
		p        RoutePoint
		distance float64
	}
	type track struct {
		points []located
		epoch  string
	}
	tracks := []track{}
	epochs := []string{}
	edges := []Interval{}
	var distance, covered float64
	for _, r := range s.Routes {
		if r.Fact.State == Deleted || r.Fact.State == Unsupported {
			continue
		}
		if !sourceAllowed(sources[r.Source]) {
			out.Summary.Reasons = reasonSet(out.Summary.Reasons, []Reason{SourceUnknown})
			continue
		}
		if len(r.Points) < 2 {
			continue
		}
		t := track{epoch: r.Source}
		flush := func() {
			if len(t.points) > 1 {
				tracks = append(tracks, t)
			}
			t = track{epoch: r.Source}
		}
		for i := 1; i < len(r.Points); i++ {
			a, b := r.Points[i-1], r.Points[i]
			dt := float64(b.At-a.At) / 1e6
			d := workoutdomain.Haversine(a.Latitude, a.Longitude, b.Latitude, b.Longitude)
			accurate := func(x RoutePoint) bool {
				return finite(x.Latitude) && finite(x.Longitude) && math.Abs(x.Latitude) <= 90 && math.Abs(x.Longitude) <= 180 && x.HorizontalAccuracy != nil && finite(*x.HorizontalAccuracy) && *x.HorizontalAccuracy >= 0 && *x.HorizontalAccuracy <= s.Policy.HorizontalAccuracyM
			}
			if b.Sequence <= a.Sequence || dt <= 0 || dt > float64(s.Policy.MaxGapSeconds) || !accurate(a) || !accurate(b) || !finite(d) || d/dt > s.Policy.MaxSpeedMPS || containing(p.Active, a.At, b.At) < 0 {
				out.Summary.RejectedEdges++
				flush()
				continue
			}
			if len(t.points) == 0 {
				t.points = append(t.points, located{a, 0})
			}
			t.points = append(t.points, located{b, t.points[len(t.points)-1].distance + d})
			distance += d
			covered += dt
			edges = append(edges, Interval{a.At, b.At})
			epochs = append(epochs, r.Source)
		}
		flush()
	}
	// Multiple overlapping routes cannot be treated as independent distance.
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].Start == edges[j].Start {
			return edges[i].End < edges[j].End
		}
		return edges[i].Start < edges[j].Start
	})
	for i := 1; i < len(edges); i++ {
		if edges[i].Start < edges[i-1].End {
			out.Summary.State = Ambiguous
			out.Summary.Reasons = reasonSet(out.Summary.Reasons, []Reason{SourceConflict})
			return out
		}
	}
	if len(edges) == 0 {
		return out
	}
	out.Summary.State = Present
	out.Summary.Distance = ptr(distance)
	out.Summary.SourceEpochs = unique(epochs)
	if p.Timing.Active != nil && *p.Timing.Active > 0 {
		out.Summary.Coverage = covered / *p.Timing.Active
	}
	var gradeDistance, flatDistance, gradeSeconds float64
	median := func(xs []float64) float64 {
		sort.Float64s(xs)
		n := len(xs)
		if n%2 == 1 {
			return xs[n/2]
		}
		return (xs[n/2-1] + xs[n/2]) / 2
	}
	for _, t := range tracks {
		xs := t.points
		// Fixed non-overlapping 100m neighborhoods. Each raw point participates in
		// at most two median groups; stationary/dense input cannot create O(N²).
		cursor := 0
		for base := float64(0); base+100 <= xs[len(xs)-1].distance; base += 100 {
			for cursor < len(xs) && xs[cursor].distance < base {
				cursor++
			}
			left := cursor
			end := left
			var ah, bh, ad, bd []float64
			for end < len(xs) && xs[end].distance <= base+100 {
				x := xs[end]
				if x.p.Altitude != nil && x.p.VerticalAccuracy != nil && finite(*x.p.Altitude) && finite(*x.p.VerticalAccuracy) && *x.p.VerticalAccuracy >= 0 && *x.p.VerticalAccuracy <= s.Policy.VerticalAccuracyM {
					if x.distance <= base+25 {
						ah = append(ah, *x.p.Altitude)
						ad = append(ad, x.distance)
					}
					if x.distance >= base+75 {
						bh = append(bh, *x.p.Altitude)
						bd = append(bd, x.distance)
					}
				}
				end++
			}
			if len(ah) < 5 || len(bh) < 5 || end <= left {
				continue
			}
			delta := median(bd) - median(ad)
			if delta < 50 {
				continue
			}
			grade := (median(bh) - median(ah)) / delta
			a, b := xs[left].p.At, xs[end-1].p.At
			if b <= a {
				continue
			}
			out.Grades = append(out.Grades, Span{Start: a, End: b, V0: grade, V1: grade, Epoch: t.epoch})
			gradeDistance += 100
			if math.Abs(grade) <= 0.01 {
				flatDistance += 100
			}
			gradeSeconds += float64(b-a) / 1e6
		}
	}
	sort.Slice(out.Grades, func(i, j int) bool { return out.Grades[i].Start < out.Grades[j].Start })
	if gradeDistance > 0 {
		out.Summary.FlatDistanceFraction = ptr(flatDistance / gradeDistance)
		out.Summary.GradeCoverage = math.Min(1, gradeSeconds/covered)
		if out.Summary.GradeCoverage >= 0.9 {
			out.Summary.Reasons = []Reason{}
		}
	}
	if out.Summary.RejectedEdges > 0 {
		out.Summary.Reasons = reasonSet(out.Summary.Reasons, []Reason{RouteAccuracyLow})
	}
	return out
}
