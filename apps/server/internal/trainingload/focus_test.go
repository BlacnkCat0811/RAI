package trainingload

import (
	"math"
	wf "rai/server/internal/workoutfeatures"
	"testing"
	"time"
)

func TestFocusConservationAndUnknowns(t *testing.T) {
	s := watchTrace(3600, steady(160))
	in := input(t, s)
	ref := reference(50, 190)
	r, e := CalculateTrainingLoadV2(V2Input{WorkoutInput: in, Reference: ref}, 1)
	if e != nil {
		t.Fatal(e)
	}
	f, e := ClassifyLoad(r, in.Exposure, in.Feature, ref, nil, time.UTC)
	if e != nil || math.Abs(f.Buckets.Total()-*r.Load) > 1e-8 || f.Buckets.Anaerobic != 0 || f.Buckets.High <= 0 || f.Buckets.Unclassified <= 0 {
		t.Fatal("steady high HR is not anaerobic; scaled exposure is unknown", e, f)
	}
	r.QualityTier = TierApproximate
	f, e = ClassifyLoad(r, in.Exposure, in.Feature, ref, nil, time.UTC)
	if e != nil || f.Buckets.Unclassified != *r.Load {
		t.Fatal("mean-HR approximation has no intensity structure", e)
	}
	r.Load = nil
	f, e = ClassifyLoad(r, in.Exposure, in.Feature, ref, nil, time.UTC)
	if e != nil {
		t.Fatal(e)
	}
	agg := AggregateFocus("d", "s", 1, s.End, time.UnixMicro(s.Start).UTC(), []WorkoutFocus{f})
	if agg.Windows[0].UnknownWorkouts != 1 || agg.Windows[0].ClassifiedCoverage != nil {
		t.Fatal("unknown vanished", agg)
	}
}
func TestFocusShortRepetitionsRequirePreWorkoutReference(t *testing.T) {
	in := input(t, fixture(600, 170))
	ref := v2Reference()
	r, e := CalculateTrainingLoadV2(V2Input{WorkoutInput: in, Reference: ref}, 1)
	if e != nil {
		t.Fatal(e)
	}
	start := in.Feature.Timing.Start
	in.Feature.Structure = &wf.StructureFeatures{Schema: wf.StructureSchema}
	for i := int64(0); i < 60; i++ {
		speed, hr := 2., 130.
		if (i >= 6 && i < 12) || (i >= 18 && i < 24) || (i >= 30 && i < 36) {
			speed = 4.
			hr = 170
		}
		in.Feature.Structure.Slices = append(in.Feature.Structure.Slices, wf.StructureSlice{Start: start + i*10e6, End: start + (i+1)*10e6, Speed: speed, HR: hr})
	}
	pre := &AbilityReference{V60: 3, At: start - 1, Hash: "prior", Mode: "demonstrated"}
	f, e := ClassifyLoad(r, in.Exposure, in.Feature, ref, pre, time.UTC)
	if e != nil || f.Buckets.Anaerobic <= 0 {
		t.Fatal("repetition not recognized", e, f)
	}
	pre.At = start
	f, e = ClassifyLoad(r, in.Exposure, in.Feature, ref, pre, time.UTC)
	if e != nil || f.Buckets.Anaerobic != 0 {
		t.Fatal("future capability leaked into classification", e)
	}
}
func TestFocusSplitsLocalMidnightDST(t *testing.T) {
	loc, e := time.LoadLocation("America/New_York")
	if e != nil {
		t.Fatal(e)
	}
	s := fixture(7200, 140)
	start := time.Date(2026, 3, 7, 23, 0, 0, 0, loc).UnixMicro()
	delta := start - s.Start
	s.Start += delta
	s.End += delta
	for i := range s.Quantities {
		s.Quantities[i].Start += delta
		s.Quantities[i].End += delta
	}
	for i := range s.Parameters {
		s.Parameters[i].EffectiveFrom = 1
	}
	in := input(t, s)
	in.Calendar = loc
	in.Timezone = loc.String()
	r, e := CalculateTrainingLoadV2(V2Input{WorkoutInput: in, Reference: v2Reference()}, 1)
	if e != nil {
		t.Fatal(e)
	}
	f, e := ClassifyLoad(r, in.Exposure, in.Feature, v2Reference(), nil, loc)
	if e != nil || len(f.Days) != 2 || math.Abs(f.Days[0].Buckets.Total()+f.Days[1].Buckets.Total()-*r.Load) > 1e-8 {
		t.Fatal("local split differs from ledger", e, f)
	}
}
