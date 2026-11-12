package workoutfeatures

import (
	"fmt"
	"math"
	"testing"
)

func speedProfile(s *RawWorkoutSnapshot, stepMS int64, speed func(sec float64) float64) {
	removeKind(s, Speed)
	for a := s.Start; a <= s.End; a += stepMS * 1000 {
		sec := float64(a-s.Start) / 1e6
		s.Quantities = append(s.Quantities, Quantity{Fact: FactRef{Kind: "quantity", ID: fmt.Sprint("speed", a), State: Present}, Type: Speed, Start: a, End: a, Value: speed(sec), Unit: unit(Speed), Source: s.Provenance[0].Epoch, Support: InstantPoint})
	}
}

func TestRunFitnessSegmentsAreDeterministic180sWindowsAfterWarmup(t *testing.T) {
	for _, c := range []struct{ seconds, want int }{{1800, 9}, {300, 1}, {180, 0}} {
		f := compute(t, synthetic(c.seconds))
		rf := f.RunFitness
		if rf == nil || rf.Schema != RunFitnessSchema || len(rf.Segments) != c.want {
			t.Fatalf("%ds: %d segments", c.seconds, len(rf.Segments))
		}
		for i, s := range rf.Segments {
			if s.Kind != "steady" || s.End-s.Start != 180e6 || s.Start != f.Timing.Start+int64(120+180*i)*1e6 {
				t.Fatalf("segment %d %+v", i, s)
			}
			if math.Abs(s.HR-150) > 1e-6 || s.FlatSpeed == nil || math.Abs(*s.FlatSpeed-3) > .01 || s.Coverage < .99 || s.SpeedCV > .01 {
				t.Fatalf("segment values %+v", s)
			}
		}
		if rf.DistanceCheck != "consistent" {
			t.Fatal(rf.DistanceCheck)
		}
	}
}

func TestRunFitnessCycleAveragedIntervals(t *testing.T) {
	for _, walk := range []bool{false, true} {
		s := synthetic(3000)
		recovery := 2.5
		if walk {
			recovery = 1.0
		}
		speedProfile(&s, 1000, func(sec float64) float64 {
			if sec < 600 {
				return 3
			}
			if int(sec-600)/120%2 == 0 {
				return 4.5
			}
			return recovery
		})
		rf := compute(t, s).RunFitness
		var cycles *RunFitnessSegment
		for i := range rf.Segments {
			if rf.Segments[i].Kind == "cycle_averaged" {
				cycles = &rf.Segments[i]
			}
		}
		if cycles == nil || cycles.Cycles < 3 || cycles.End-cycles.Start < 600e6 || cycles.WalkRecovery != walk {
			t.Fatalf("walk=%v cycle segment %+v", walk, cycles)
		}
		for _, x := range rf.Segments {
			if x.Kind == "steady" && x.Start < cycles.End && x.End > cycles.Start {
				t.Fatal("steady window overlaps the cycle block")
			}
		}
	}
}

func TestRunFitnessSprintNeedsNativeFiveSecondSpeed(t *testing.T) {
	for _, c := range []struct {
		stepMS int64
		want   bool
	}{{2500, true}, {5000, true}, {10000, false}} {
		s := synthetic(1200)
		speedProfile(&s, c.stepMS, func(sec float64) float64 {
			if sec >= 600 && sec < 700 {
				return 5.5
			}
			return 3
		})
		rf := compute(t, s).RunFitness
		var best *RunFitnessBest
		for i := range rf.Best {
			if rf.Best[i].Duration == 90 {
				best = &rf.Best[i]
			}
		}
		if (best != nil) != c.want {
			t.Fatalf("step %dms: v90 present=%v", c.stepMS, best != nil)
		}
		if best != nil && (best.Speed < 5.3 || best.MaxGap > 5) {
			t.Fatalf("step %dms: %+v", c.stepMS, best)
		}
	}
}

func TestRunFitnessPeakHRAndLongRunBlocks(t *testing.T) {
	s := synthetic(4500)
	for i := range s.Quantities {
		q := &s.Quantities[i]
		if q.Type == HR && q.Start >= s.Start+1000e6 && q.Start < s.Start+1030e6 {
			q.Value = 196
		}
	}
	rf := compute(t, s).RunFitness
	if rf.PeakHR15 == nil || *rf.PeakHR15 < 190 {
		t.Fatalf("peak %v", rf.PeakHR15)
	}
	if rf.LongestRun < 4400 || len(rf.Blocks) != 4 {
		t.Fatalf("blocks %v %d", rf.LongestRun, len(rf.Blocks))
	}
}
