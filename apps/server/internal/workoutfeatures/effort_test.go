package workoutfeatures

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"testing"
)

// nativeSpeed replaces speed (and power/steps) with Apple-Watch-like 2.56 s
// interval samples following speed(sec).
func nativeSpeed(s *RawWorkoutSnapshot, speed func(sec float64) float64) {
	removeKind(s, Speed)
	removeKind(s, Power)
	removeKind(s, Steps)
	const step = int64(2_560_000)
	for a := s.Start; a+step <= s.End; a += step {
		v := speed(float64(a-s.Start) / 1e6)
		for k, value := range map[string]float64{Speed: v, Power: 70 * v, Steps: 2.56 * (150 + 6*v) / 60} {
			s.Quantities = append(s.Quantities, Quantity{Fact: FactRef{Kind: "quantity", ID: fmt.Sprint(k, a), State: Present}, Type: k, Start: a, End: a + step, Value: value, Unit: unit(k), Source: s.Provenance[0].Epoch, Support: IntervalObservation})
		}
	}
}

func TestEffortPointsKeepNativeResolutionAndContext(t *testing.T) {
	s := synthetic(1800)
	nativeSpeed(&s, func(sec float64) float64 {
		switch {
		case sec >= 600 && sec < 607.68:
			return 7
		case sec >= 1000 && sec < 1500:
			return 2
		}
		return 3
	})
	f := compute(t, s)
	e := f.Effort
	if e == nil || e.Schema != EffortSchema || e.Policy != EffortPolicy || e.MedianSpeed == nil || *e.MedianSpeed != 3 {
		t.Fatalf("effort contract %+v", e)
	}
	if e.Indoor == nil || *e.Indoor || e.MedianPower == nil || math.Abs(*e.MedianPower-210) > 1e-9 || e.MedianCadence == nil || math.Abs(*e.MedianCadence-168) > 1e-6 {
		t.Fatalf("medians %v %v %v", e.MedianSpeed, e.MedianPower, e.MedianCadence)
	}
	sprint := 0
	for i, p := range e.Points {
		if i > 0 && p.Start < e.Points[i-1].End {
			t.Fatal("unordered points")
		}
		if p.End-p.Start != 2_560_000 || !p.Supported {
			t.Fatalf("native span lost %+v", p)
		}
		if p.Speed == 7 {
			sprint++
			if p.Power == nil || *p.Power != 490 || p.Cadence == nil || math.Abs(*p.Cadence-192) > 1e-6 || p.HR == nil || p.Grade == nil || p.GAPSpeed == nil || *p.GAPSpeed != 7 {
				t.Fatalf("sprint point %+v", p)
			}
		}
		mid := float64(p.Start-s.Start) / 1e6
		if mid > 1040 && mid < 1460 {
			t.Fatalf("slow block outside the 30 s context kept at %v s", mid)
		}
	}
	if sprint != 3 || e.TotalPoints <= len(e.Points) {
		t.Fatalf("sprint points %d, retained %d of %d", sprint, len(e.Points), e.TotalPoints)
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var back EffortFeatures
	if err := json.Unmarshal(b, &back); err != nil || !reflect.DeepEqual(&back, e) {
		t.Fatalf("round trip %v", err)
	}
	if err := json.Unmarshal([]byte(`[1,2,null,null,null,null,null,null,true]`), &EffortPoint{}); err == nil {
		t.Fatal("null speed accepted")
	}
}

func TestEffortWithoutSpeedIsEmptyNotInvented(t *testing.T) {
	s := synthetic(600)
	removeKind(&s, Speed)
	e := compute(t, s).Effort
	if e == nil || len(e.Points) != 0 || e.MedianSpeed != nil {
		t.Fatalf("%+v", e)
	}
}

func TestEffortArtifactFitsAFourHourNativeRun(t *testing.T) {
	s := synthetic(4 * 3600)
	nativeSpeed(&s, func(sec float64) float64 { return 3 + .37*math.Sin(sec/97) })
	f := compute(t, s)
	b, _, err := f.MarshalArtifact()
	if err != nil || len(b) > MaxArtifactBytes {
		t.Fatalf("artifact %d bytes %v", len(b), err)
	}
	t.Logf("4 h run: %d effort points of %d, artifact %d bytes", len(f.Effort.Points), f.Effort.TotalPoints, len(b))
}
