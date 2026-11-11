package performance

import (
	"math"
	"testing"
)

func TestPerformanceV2DemonstratedIdentity(t *testing.T) {
	in := V2Input{Input: established(3, .9), Mode: ModeDemonstratedV2}
	r, err := CalculateV2(in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Schema != SchemaV2 || r.Version != VersionV2 || r.EvidenceMode != ModeDemonstratedV2 || r.Ability == nil {
		t.Fatalf("v2 demonstrated %+v", r)
	}
	v1 := mustCalculate(t, in.Input)
	if v1.Ability.V60 != r.Ability.V60 {
		t.Fatalf("v2 changed frozen ability math: %v %v", v1.Ability.V60, r.Ability.V60)
	}
}

func TestPerformanceV2MaximalAnnotations(t *testing.T) {
	base := Input{Calendar: fixtureEnd.Location(), Timezone: "UTC", DatasetID: "synthetic", Generation: 1, AsOf: fixtureEnd.UnixMicro(), View: "revised"}
	short := []int{1200, 1800}
	long := []int{3600, 5400}
	var ann []EffortAnnotation
	for i := 0; i < 8; i++ {
		ds := long
		if i%2 == 0 {
			ds = short
		}
		ref := fixtureRef(i, fixtureEnd.AddDate(0, 0, -20+i*2), 3, .9, ds...)
		base.Features = append(base.Features, ref)
		ann = append(ann, EffortAnnotation{ID: ref.Feature.WorkoutID, WorkoutID: ref.Feature.WorkoutID, Mode: ModeMaximalV2, UserConfirmed: true, AnnotatedAt: fixtureEnd.UnixMicro() - 1, KnownAt: fixtureEnd.UnixMicro() - 1})
	}
	r, err := CalculateV2(V2Input{Input: base, Mode: ModeMaximalV2, Annotations: ann})
	if err != nil {
		t.Fatal(err)
	}
	if r.EvidenceMode != ModeMaximalV2 || r.MaximalSupported == "" {
		t.Fatalf("maximal mode %+v", r)
	}
	if r.Confidence > .7 {
		t.Fatalf("maximal confidence cap exceeded: %v", r.Confidence)
	}
	// A high-HR workout without an annotation is not maximal evidence.
	r2, err := CalculateV2(V2Input{Input: base, Mode: ModeMaximalV2})
	if err != nil {
		t.Fatal(err)
	}
	if r2.Ability != nil || r2.MaximalSupported != "unavailable" {
		t.Fatalf("unannotated maximal must abstain: %+v", r2)
	}
	// Future-known and revoked annotations cannot backfill a past prediction.
	ann[0].KnownAt = fixtureEnd.UnixMicro() + 1
	ann[1].Revoked = true
	r3, err := CalculateV2(V2Input{Input: base, Mode: ModeMaximalV2, Annotations: ann})
	if err != nil {
		t.Fatal(err)
	}
	if r3.MaximalSupported != "supported" {
		t.Fatalf("remaining valid annotations should still support: %+v", r3)
	}
}

func TestV2ScopeCarriesStack(t *testing.T) {
	if got := ScopeFor("synthetic-v2"); got != "model/synthetic-v2/athlete/performance" {
		t.Fatalf("scope=%s", got)
	}
}

// Easy runs mixed with harder efforts: v1's 0.8 quantile over every workout
// leaves residuals that fail identification, while the v2 envelope fits the
// fastest three per duration and recovers the demonstrated curve.
func TestPerformanceV2EnvelopeIgnoresEasyRuns(t *testing.T) {
	in := Input{Calendar: fixtureEnd.Location(), Timezone: "UTC", DatasetID: "synthetic", Generation: 1, AsOf: fixtureEnd.UnixMicro(), View: "revised"}
	for i := 0; i < 24; i++ {
		v20 := 3.6
		if i%4 != 0 {
			v20 = 3.6 * .78 // three easy runs for every hard one
		}
		in.Features = append(in.Features, fixtureRef(i, fixtureEnd.AddDate(0, 0, -47+2*i), v20, .93))
	}
	r, err := CalculateV2(V2Input{Input: in, Mode: ModeDemonstratedV2})
	if err != nil || r.Ability == nil {
		t.Fatalf("v2 envelope: %v %v %s", err, r.ReasonCodes, r.Status)
	}
	want := 3.6 * 1200 * math.Pow(3, .93) / 3600
	if math.Abs(r.Ability.V60/want-1) > .01 {
		t.Fatalf("v60=%v want %v", r.Ability.V60, want)
	}
	if v1 := mustCalculate(t, in); v1.Ability != nil && math.Abs(v1.Ability.V60/want-1) <= .01 {
		t.Fatalf("fixture does not exercise the v1 limitation: %v", v1.Ability.V60)
	}
	below := 0
	for _, e := range r.Evidence {
		if e.State == "below_envelope" {
			below++
		}
	}
	if below == 0 {
		t.Fatal("easy runs must stay visible as below-envelope evidence")
	}
}
