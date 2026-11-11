package modelmanifest

import (
	"strings"
	"testing"
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
	if m.Readiness.Version != "v4" || m.Readiness.Schema != "rai.readiness.training/4" || m.Recovery.Version != "v3" || m.ProductSchema != "rai.athlete_model/3" {
		t.Fatal("readiness v3 boundary missing")
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
