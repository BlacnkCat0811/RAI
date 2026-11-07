package athletemodel

import (
	"strings"
	"testing"
	"time"
)

func gate() PromotionGate {
	h := strings.Repeat("a", 64)
	return PromotionGate{Model: "rai.vo2max.submax", Version: "v1", ManifestSHA: h, ConfigSHA: h, FeatureSchema: "rai.workout_features/2", SourcePolicy: "recorded-running;hr-reference-v2/v1", InputRegistry: "rai.model_input_registry/1", MappingVersion: MappingVersion2, ConsumerSchema: Schema2, DatasetID: "d", SyntheticDataset: true, EngineeringReport: h, DeviceDataReport: h, ApplicabilityReport: h, UXPrivacyReport: h, RollbackReport: h, Authorization: h, Reviewer: "reviewer", ExpiresAt: "2030-01-01T00:00:00Z"}
}
func TestPromotionGateClosesOnMissingEvidenceAndRealDataset(t *testing.T) {
	now := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	g := gate()
	if err := g.Validate(now, g.ManifestSHA, true); err != nil {
		t.Fatal(err)
	}
	g.DeviceDataReport = ""
	if err := g.Validate(now, g.ManifestSHA, true); err == nil {
		t.Fatal("missing device report accepted")
	}
	g = gate()
	if err := g.Validate(now, g.ManifestSHA, false); err == nil {
		t.Fatal("synthetic gate authorized real dataset")
	}
	g = gate()
	g.ExpiresAt = "2020-01-01T00:00:00Z"
	if err := g.Validate(now, g.ManifestSHA, true); err == nil {
		t.Fatal("expired gate accepted")
	}
}
