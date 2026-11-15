package hrref

import "testing"

func TestV3OldManualRemainsUsableAndV2Unchanged(t *testing.T) {
	r := ref("old", m(190, base-180*day, base-180*day), m(50, base-180*day, base-180*day), base-180*day)
	in := Input{AsOf: base, WorkoutStart: base, SourceEpoch: "e1", References: []Reference{r}}
	old, err := Resolve(in)
	if err != nil || old.RestingHR != nil {
		t.Fatal("v2 behavior changed")
	}
	got, err := ResolveV3(V3Input{Input: in, Mode: "manual"})
	if err != nil || got.RestingHR == nil || got.RestingHR.Value != 50 || got.Status != "estimated" {
		t.Fatalf("old manual: %+v %v", got, err)
	}
	in.AsOf += 300 * day
	later, err := ResolveV3(V3Input{Input: in, Mode: "manual"})
	if err != nil || later.RestingHR == nil || later.RestingHR.Value != got.RestingHR.Value || later.Status != got.Status {
		t.Fatal("wall clock changed historical reference")
	}
}

func TestV3SparseAutomaticAndManualPriority(t *testing.T) {
	in := Input{AsOf: base, WorkoutStart: base, SourceEpoch: "e1", References: []Reference{ref("manual", m(190, base-day, base-day), m(55, base-day, base-day), base-day)}}
	for i := 0; i < 3; i++ {
		v := 50.0
		at := base - int64(i+10)*day
		in.DailyRHR = append(in.DailyRHR, DailyRHR{Date: LocalDate(timeAt(at)), Start: at, Value: &v, SourceEpoch: "e1"})
	}
	auto, err := ResolveV3(V3Input{Input: in, Mode: "automatic"})
	if err != nil || auto.Source != "baseline_sparse" || auto.Status != "estimated" || auto.RestingHR.Value != 50 {
		t.Fatalf("sparse: %+v %v", auto, err)
	}
	manual, err := ResolveV3(V3Input{Input: in, Mode: "manual"})
	if err != nil || manual.Source != "manual" || manual.RestingHR.Value != 55 {
		t.Fatalf("manual: %+v %v", manual, err)
	}
	in.DailyRHR = in.DailyRHR[:2]
	fallback, err := ResolveV3(V3Input{Input: in, Mode: "automatic"})
	if err != nil || fallback.Source != "manual_fallback" || fallback.Status != "estimated" {
		t.Fatalf("fallback: %+v %v", fallback, err)
	}
	in.References[0].RestingHR = nil
	missing, err := ResolveV3(V3Input{Input: in, Mode: "automatic"})
	if err != nil || missing.RestingHR != nil || missing.Status != "unavailable" {
		t.Fatalf("missing: %+v %v", missing, err)
	}
}
