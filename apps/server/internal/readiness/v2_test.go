package readiness

import "testing"

func v2Fixture() ReadinessSnapshot {
	in := fixture()
	in.Config = DefaultV2Config("UTC")
	in.Recovery.Model = RecoveryModelV2
	for i := range in.Loads {
		in.Loads[i].Model = LoadModelV2
	}
	return in
}

func TestV2AllowlistAndIdentity(t *testing.T) {
	in := v2Fixture()
	r, err := CalculateV2(in)
	if err != nil {
		t.Fatal(err)
	}
	if r.ModelVersion != ModelV2 || r.Schema != SchemaV2 || r.Score == nil || *r.Score <= 0 || r.Status != "available" {
		t.Fatalf("v2 result %+v", r)
	}
	// Mixing v1 into an allowlist v2 calculation fails closed.
	in = v2Fixture()
	in.Loads[0].Model = LoadModel
	r, err = CalculateV2(in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Score != nil || r.Status != "source_conflict" {
		t.Fatalf("mixed load must fail closed: %+v", r)
	}
	found := false
	for _, x := range r.Reasons {
		if x == "load_model_not_allowed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("reasons %v", r.Reasons)
	}
	// Partial Recovery v2 never feeds a full readiness score.
	in = v2Fixture()
	in.Recovery.Status = "partial"
	in.Recovery.Quality = "partial"
	r, _ = CalculateV2(in)
	if r.Score != nil {
		t.Fatalf("partial recovery must not produce readiness: %+v", r)
	}
}

func TestV2NoWorkoutRefreshAndSupersede(t *testing.T) {
	in := v2Fixture()
	r, err := CalculateV2(in)
	if err != nil || r.Score == nil {
		t.Fatal(err, r.Score)
	}
	f, err := FreshenV2(r, in.AsOf+6*Hour)
	if err != nil || f.Score == nil {
		t.Fatal(err, f.Score)
	}
	if *f.Score <= 0 || *f.Score > *r.Score+0.01 {
		t.Fatalf("load-free refresh must remain a valid bounded score: %v %v", *r.Score, *f.Score)
	}
	before, err := FreshenV2(r, r.ValidBefore)
	if err != nil {
		t.Fatal(err)
	}
	if before.Score != nil || before.Status != "superseded" {
		t.Fatalf("must supersede at civil-day anchor boundary: %+v", before)
	}
}
