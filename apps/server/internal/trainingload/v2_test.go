package trainingload

import "testing"

func v2Reference() *Reference {
	return &Reference{ID: "synthetic-reference", Hash: Hash("parameter"), RestHR: 50, MaxHR: 200, MeasuredAt: 1, RecordedAt: 1, Provenance: "explicit synthetic reference"}
}

func TestLoadV2IdentityAndReference(t *testing.T) {
	base := input(t, fixture(3600, 140))
	r, err := CalculateTrainingLoadV2(V2Input{WorkoutInput: base, Reference: v2Reference()}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if r.Schema != SchemaV2 || r.ModelVersion != VersionV2 || r.Model != AlgorithmV2 || r.Load == nil {
		t.Fatalf("identity %+v", r)
	}
	if r.Evidence["referenceID"] != "synthetic-reference" || r.Evidence["referencePolicy"] != "hr_reference/2" {
		t.Fatalf("reference evidence %+v", r.Evidence)
	}
	// v1 output for the same facts stays byte-for-byte on its own identity.
	v1, err := CalculateTrainingLoad(base, 1)
	if err != nil {
		t.Fatal(err)
	}
	if v1.Schema != Schema || v1.ModelVersion != Version {
		t.Fatalf("v1 changed: %+v", v1)
	}
	if *v1.Load != *r.Load {
		t.Fatalf("v2 changed the frozen dose kernel: %v %v", *v1.Load, *r.Load)
	}
}

func TestLoadV2MissingReferenceAndOverlap(t *testing.T) {
	base := input(t, fixture(3600, 140))
	r, err := CalculateTrainingLoadV2(V2Input{WorkoutInput: base}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if r.Load != nil || r.Status != "insufficient_data" {
		t.Fatalf("missing reference must not score: %+v", r)
	}
	r, err = CalculateTrainingLoadV2(V2Input{WorkoutInput: base, Reference: v2Reference(), OverlapUnknown: true}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if r.Load != nil || r.Status != "partial" {
		t.Fatalf("unresolved overlap must not score: %+v", r)
	}
}

func TestLoadV2ReferenceConflict(t *testing.T) {
	base := input(t, fixture(3600, 140))
	r, err := CalculateTrainingLoadV2(V2Input{WorkoutInput: base, ReferenceConflict: true}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "source_conflict" || r.Load != nil {
		t.Fatalf("conflict %+v", r)
	}
}

func TestV2ScopesCarryStack(t *testing.T) {
	if got := ScopeStateV2For("synthetic-v2"); got != "model/synthetic-v2/athlete/load/v2" {
		t.Fatalf("state scope=%s", got)
	}
	if got := ScopeV2For("w", "synthetic-v2"); got != "model/synthetic-v2/workout_load/w/v2" {
		t.Fatalf("workout scope=%s", got)
	}
}
