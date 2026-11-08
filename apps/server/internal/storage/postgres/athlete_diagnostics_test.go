package postgres

import (
	"encoding/json"
	"testing"
	"time"

	diag "rai/server/internal/diagnostics"
	pf "rai/server/internal/performance"
	tl "rai/server/internal/trainingload"
)

// jsonbRoundTrip re-encodes a stored payload with map key ordering, which is
// what reading an analysis_artifacts.payload jsonb column does. The projection
// must not depend on the original Go field order for its content hash.
func jsonbRoundTrip(t *testing.T, payload []byte) []byte {
	t.Helper()
	var generic any
	if err := json.Unmarshal(payload, &generic); err != nil {
		t.Fatal(err)
	}
	normalized, err := json.Marshal(generic)
	if err != nil {
		t.Fatal(err)
	}
	return normalized
}

func TestResearchProductItemHashesTypedResultAfterJSONBReorder(t *testing.T) {
	identity := diag.CriticalSpeedIdentity()
	dataset, stack, generation := "6a8a5d8a-0000-4000-8000-000000000001", "synthetic-v2", int64(7)
	cs, dPrime, r2 := 3.1, 210.0, 0.97
	result := diag.CriticalSpeedResult{
		Header: diag.Header{Identity: identity, Status: diag.StatusOK, Reasons: diag.ReasonCodes{}, Support: diag.Support{Level: diag.SupportModerate, Fraction: .7, Count: 4}},
		AsOf:   time.Now().UnixMicro(), WindowDays: 90, WorkoutCount: 4, DistinctDurations: 3,
		CSMPS: &cs, DPrimeMeters: &dPrime, R2: &r2,
	}
	envelope := researchEnvelope{Identity: identity, Scope: "model/" + stack + "/athlete/diagnostic/v1/critical_speed", AsOf: result.AsOf, Status: diag.StatusOK, Reasons: result.Reasons, Result: result}
	envelope.SchemaVersion, envelope.DatasetID, envelope.Generation = identity.Schema, dataset, generation
	payload, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	item, err := researchProductItem("critical_speed", "model_research_v1/critical_speed", identity.Schema, identity.Model, identity.Version,
		tl.Hash(envelope), "2026-11-08", generation, time.Now(), jsonbRoundTrip(t, payload), dataset, stack)
	if err != nil || item.Status != diag.ProductAvailable || item.CriticalSpeed == nil || *item.CriticalSpeed.CSMPS != cs {
		t.Fatalf("critical speed projection item=%+v err=%v", item, err)
	}
}

func TestResearchProductItemAcceptsUnavailableEnvelopeWithoutResult(t *testing.T) {
	identity := diag.LoadSettlingIdentity()
	dataset, stack, generation := "6a8a5d8a-0000-4000-8000-000000000002", "synthetic-v2", int64(8)
	envelope := unavailableResearch(identity, "model/"+stack+"/athlete/diagnostic/v1/load_settling", time.Now().UnixMicro(), diag.ReasonInsufficientHistory)
	envelope.SchemaVersion, envelope.DatasetID, envelope.Generation = identity.Schema, dataset, generation
	payload, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	item, err := researchProductItem("load_settling", "model_research_v1/load_settling", identity.Schema, identity.Model, identity.Version,
		tl.Hash(envelope), "2026-11-08", generation, time.Now(), jsonbRoundTrip(t, payload), dataset, stack)
	if err != nil || item.Status != diag.ProductInsufficient || item.LoadSettling != nil {
		t.Fatalf("unavailable projection item=%+v err=%v", item, err)
	}
}

func TestResearchProductItemRejectsContentHashMismatch(t *testing.T) {
	identity := diag.AerobicEfficiencyIdentity()
	dataset, stack, generation := "6a8a5d8a-0000-4000-8000-000000000003", "synthetic-v2", int64(9)
	envelope := unavailableResearch(identity, "model/"+stack+"/athlete/diagnostic/v1/aerobic_efficiency", time.Now().UnixMicro(), diag.ReasonInsufficientHistory)
	envelope.SchemaVersion, envelope.DatasetID, envelope.Generation = identity.Schema, dataset, generation
	payload, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = researchProductItem("aerobic_efficiency", "model_research_v1/aerobic_efficiency", identity.Schema, identity.Model, identity.Version,
		tl.Hash("tampered"), "2026-11-08", generation, time.Now(), payload, dataset, stack); err == nil {
		t.Fatal("content hash mismatch was accepted")
	}
}

func TestResearchProductItemMaximalHashesTypedPerformanceResult(t *testing.T) {
	dataset, generation := "6a8a5d8a-0000-4000-8000-000000000004", int64(10)
	result := pf.Result{Schema: pf.AbilitySchemaV2, DatasetID: dataset, Generation: generation, Model: pf.AbilityModel,
		Version: pf.VersionV2, ConfigHash: pf.ConfigHashV2(), AsOf: time.Now().UnixMicro(), View: "revised",
		EvidenceMode: pf.ModeMaximalV2, Status: "available", AnchorDay: "2026-11-08",
		Ability: &pf.Ability{V60: 3.1, Gamma: 1, Retention: .8, Score: 61}}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	item, err := researchProductItem("running_ability_maximal_v2", "model_research_v1/running_ability_maximal_v2", pf.AbilitySchemaV2,
		pf.CurveModel, pf.VersionV2, pf.Hash(result), "2026-11-08", generation, time.Now(), jsonbRoundTrip(t, payload), dataset, "synthetic-v2")
	if err != nil || item.Status != diag.ProductAvailable || item.Maximal == nil || item.Maximal.Score == nil || *item.Maximal.Score != 61 {
		t.Fatalf("maximal projection item=%+v err=%v", item, err)
	}
}
