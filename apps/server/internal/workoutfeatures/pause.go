package workoutfeatures

import (
	"math"
	"sort"
)

type PauseTimeline struct {
	Active     []Interval
	Boundaries []int64
	Timing     Timing
	Reasons    []Reason
}

func BuildPause(s RawWorkoutSnapshot) PauseTimeline {
	out := PauseTimeline{Timing: Timing{Start: s.Start, End: s.End, Elapsed: float64(s.End-s.Start) / 1e6, SourceDuration: float64(s.DurationUs) / 1e6, State: Present}}
	events := append([]Event{}, s.Events...)
	for _, a := range s.Activities {
		events = append(events, a.Events...)
	}
	// Parent and activity copies of the same physical event are one transition.
	type transition struct{ Type, At, End int64 }
	es := []transition{}
	for _, e := range events {
		es = append(es, transition{e.Type, e.Start, e.End})
	}
	es = unique(es)
	sort.Slice(es, func(i, j int) bool {
		if es[i].At == es[j].At {
			return es[i].Type < es[j].Type
		}
		return es[i].At < es[j].At
	})
	manual, motion, everManual, everMotion := false, false, false, false
	last := s.Start
	uncertain := false
	for i := 0; i < len(es); {
		at := es[i].At
		j := i
		flags := map[int64]bool{}
		for j < len(es) && es[j].At == at {
			flags[es[j].Type] = true
			if es[j].At < s.Start || es[j].End > s.End || es[j].End < es[j].At {
				uncertain = true
			}
			j++
		}
		if at < s.Start || at > s.End {
			uncertain = true
			i = j
			continue
		}
		if flags[1] && flags[2] || flags[5] && flags[6] {
			uncertain = true
		}
		if at > last && !manual && !motion {
			out.Active = append(out.Active, Interval{last, at})
		}
		before := manual || motion
		for _, e := range es[i:j] {
			switch e.Type {
			case 1:
				manual = true
				everManual = true
			case 2:
				if !everManual {
					uncertain = true
				}
				manual = false
			case 5:
				motion = true
				everMotion = true
			case 6:
				if !everMotion {
					uncertain = true
				}
				motion = false
			case 3, 4, 7, 8: // lap/marker/segment/request carry no state transition
			default:
				uncertain = true
			}
		}
		if before != (manual || motion) {
			out.Boundaries = append(out.Boundaries, at)
		}
		last = at
		i = j
	}
	if manual || motion {
		uncertain = true
	}
	if last < s.End && !manual && !motion {
		out.Active = append(out.Active, Interval{last, s.End})
	}
	// Adjacent active intervals separated only by a lap/request are continuous.
	merged := []Interval{}
	for _, x := range out.Active {
		if len(merged) > 0 && merged[len(merged)-1].End == x.Start {
			merged[len(merged)-1].End = x.End
		} else {
			merged = append(merged, x)
		}
	}
	out.Active = merged
	var active float64
	for _, x := range out.Active {
		d := float64(x.End-x.Start) / 1e6
		active += d
		out.Timing.LongestContinuous = math.Max(out.Timing.LongestContinuous, d)
	}
	if math.Abs(active-out.Timing.SourceDuration) > math.Max(2, 0.005*out.Timing.Elapsed) {
		uncertain = true
	}
	if uncertain {
		out.Timing.State = Ambiguous
		out.Reasons = []Reason{ActiveTimeUncertain}
		out.Active = nil
		out.Timing.LongestContinuous = 0
		return out
	}
	out.Timing.Active = ptr(active)
	out.Timing.Paused = ptr(out.Timing.Elapsed - active)
	return out
}
func containing(xs []Interval, a, b int64) int {
	i := sort.Search(len(xs), func(i int) bool { return xs[i].End > a })
	if i < len(xs) && xs[i].Start <= a && b <= xs[i].End {
		return i
	}
	return -1
}
