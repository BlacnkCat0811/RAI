package postgres

import (
	"encoding/json"
	"testing"
	"time"

	am "rai/server/internal/athletemodel"
	"rai/server/internal/modelmanifest"
	"rai/server/internal/modelrelease"
	rc "rai/server/internal/recovery"
)

func TestRecoveryTypedReferenceSelection(t *testing.T) {
	at := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	makeRow := func(id string, age int, value float64) stackInput {
		b, _ := json.Marshal(map[string]any{"restingHR": value, "restingMeasuredAt": at.AddDate(0, 0, -100)})
		return stackInput{ID: id, Kind: "hr_reference", Hash: "synthetic-" + id, From: at.AddDate(0, 0, -age), Known: at, Payload: b}
	}
	old, recent := makeRow("old", 90, 60), makeRow("recent", 1, 50)
	r, conflict := recoveryRestingHR([]stackInput{old, recent}, at, at)
	if r == nil || r.ID != "recent" || conflict {
		t.Fatal("latest effective explicit RHR was not used")
	}
	revocation := stackInput{ID: "revocation", Known: at, Revokes: "recent"}
	r, conflict = recoveryRestingHR([]stackInput{old, recent, revocation}, at, at)
	if r == nil || r.ID != "old" || conflict {
		t.Fatal("revoked RHR was still used")
	}
	tie := makeRow("tie", 1, 55)
	_, conflict = recoveryRestingHR([]stackInput{recent, tie}, at, at)
	if !conflict {
		t.Fatal("conflicting effective references were silently selected")
	}
	future := makeRow("future", -1, 40)
	r, conflict = recoveryRestingHR([]stackInput{old, future}, at, at)
	if r == nil || r.ID != "old" || conflict {
		t.Fatal("future effective input leaked into the fallback")
	}
}

func TestRecoveryV4PublishedArtifactProjectsAndExpires(t *testing.T) {
	at := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	h, _ := modelmanifest.CurrentHash()
	release, ok := modelrelease.Lookup(modelmanifest.StackID(), h, "UTC")
	if !ok {
		t.Fatal("release missing")
	}
	score := 67.
	r := rc.V4Result{V2Result: rc.V2Result{Result: rc.Result{Dataset: "synthetic", Generation: 1, AsOf: at.UnixMicro(), Schema: rc.SchemaV4, ModelVersion: rc.ModelV4, Timezone: "UTC", ConfigHash: release.RecoveryConfig.Hash(), Score: &score, Status: "available", Quality: "estimated"}}, Mode: "estimated", ValidBefore: at.Add(12 * time.Hour).UnixMicro(), Components: map[string]rc.V3Component{"hrv": {State: "default_baseline", InputBasis: "default_baseline"}}}
	b, _ := json.Marshal(r)
	a := publishedModelArtifact{Payload: jsonbRoundTrip(t, b), ContentHash: rc.Hash(r), Schema: rc.SchemaV4, Algorithm: rc.Algorithm, Version: "v4", Generation: 1, ManifestSHA: h, PublishedAt: at}
	var out am.AthleteModelSnapshot
	projectOptimizedArtifacts(&out, map[string]publishedModelArtifact{"recovery": a}, "synthetic", "UTC", release, at.Add(time.Hour).UnixMicro())
	if out.PhysiologicalRecovery.Value == nil || out.PhysiologicalRecovery.Value.Score == nil || out.PhysiologicalRecovery.Envelope.Mode != "estimated" || out.PhysiologicalRecovery.Value.Components["hrv"].InputBasis != "default_baseline" {
		t.Fatal("new recovery payload failed validation or projection")
	}
	projectOptimizedArtifacts(&out, map[string]publishedModelArtifact{"recovery": a}, "synthetic", "UTC", release, r.ValidBefore)
	if out.PhysiologicalRecovery.Value.Score != nil || out.PhysiologicalRecovery.Envelope.Freshness != "expired" {
		t.Fatal("previous-day estimate did not expire")
	}
}
