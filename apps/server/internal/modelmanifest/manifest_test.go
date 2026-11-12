package modelmanifest

import (
	"strings"
	"testing"

	te "rai/server/internal/trainingeffect"
)

func TestCurrentManifestIsCanonicalAndVersioned(t *testing.T) {
	m := Current()
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	h1, err := Hash(m)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := Hash(Current())
	if err != nil || h1 != h2 || len(h1) != 64 {
		t.Fatalf("unstable hash %q %q %v", h1, h2, err)
	}
	if m.Features.Version != "v1" || m.Features.JobKind != StackJobKind || m.Load.Version != "v2" || m.VO2Max.ModelID != "rai.vo2max.submax" || m.VO2Max.Version != "v1" {
		t.Fatalf("identities %+v %+v", m.Features, m.VO2Max)
	}
	if m.Readiness.Version != "v4" || m.Readiness.Schema != "rai.readiness.training/4" || m.Recovery.Version != "v4" || m.ProductSchema != ProductSchema4 {
		t.Fatal("readiness v4 / schema4 boundary missing")
	}
	if m.RunningAbility.ModelID != "rai.running_fitness" || m.RunningAbility.ArtifactKey != "running_fitness" || m.AbilityEstimate != nil {
		t.Fatal("retired running ability still in the current manifest")
	}
	if m.TrainingEffect == nil || m.TrainingEffect.ModelID != "rai.training_effect" || m.TrainingEffect.ConfigHash != te.ConfigHash() {
		t.Fatal("training effect component missing from the current manifest")
	}
	s3 := schema3Manifest()
	if h, _ := Hash(s3); h == h1 || s3.Validate() != nil {
		t.Fatal("schema3 manifest must remain valid and distinct")
	}
	for _, d := range m.Diagnostics {
		if !strings.HasPrefix(d.Schema, "rai.") || d.Version != "v1" {
			t.Fatalf("diagnostic %+v", d)
		}
	}
}

func TestManifestRejectsDuplicateAndUnvalidated(t *testing.T) {
	m := Current()
	m.Diagnostics = append(m.Diagnostics, m.Diagnostics[0])
	if err := m.Validate(); err == nil {
		t.Fatal("duplicate accepted")
	}
	m = Current()
	m.PhysiologicallyValidated = true
	if err := m.Validate(); err == nil {
		t.Fatal("unvalidated claim accepted")
	}
}
