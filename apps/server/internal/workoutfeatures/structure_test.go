package workoutfeatures

import (
	"context"
	"reflect"
	"testing"
)

func TestSharedWindowIndexPreservesExtractedFeatures(t *testing.T) {
	for _, withoutSpeed := range []bool{false, true} {
		raw := synthetic(4200)
		if withoutSpeed {
			removeKind(&raw, Speed)
		}
		s, e := Normalize(raw)
		if e != nil {
			t.Fatal(e)
		}
		timeline := BuildTimeline(s)
		f, e := ComputeNormalized(context.Background(), s)
		if e != nil {
			t.Fatal(e)
		}
		parameter, _ := resolveParameter(s)
		if !reflect.DeepEqual(f.Windows, ExtractWindows(timeline, f.Distance, f.Environment, f.Evidence.InputHash)) || !reflect.DeepEqual(f.Steady, ExtractSteady(s, timeline, f.Environment, parameter, f.Evidence.InputHash)) || !reflect.DeepEqual(f.LongRun, ExtractLongRun(timeline)) {
			t.Fatal("shared prefix/empty-speed optimization changed evidence")
		}
		if f.Structure == nil || f.Structure.Schema != StructureSchema {
			t.Fatal("missing structure contract")
		}
		if withoutSpeed && len(f.Structure.Slices) != 0 {
			t.Fatal("invented speed structure")
		}
	}
}
