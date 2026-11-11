package athletemodel

import (
	"bytes"
	"encoding/json"
	"os"
	rd "rai/server/internal/readiness"
	rc "rai/server/internal/recovery"
	ra "rai/server/internal/runningability"
	sr "rai/server/internal/sessionrecovery"
	tl "rai/server/internal/trainingload"
	"testing"
)

func TestOptimizedSharedContract(t *testing.T) {
	s := NewSnapshotV3(1794369600000000, 1, "Asia/Shanghai", "2026-11-11", Assignment{Channel: "active", StackID: "synthetic-v3", Version: 1})
	at := s.AsOf
	score := 70.
	evidence := at - 24*sr.Hour
	recovery := rc.V3Result{V2Result: rc.V2Result{Result: rc.Result{ModelVersion: rc.ModelV3, Schema: rc.SchemaV3, Status: "available", Quality: "estimated", Score: &score, BaselineState: "established", Confidence: .3, AsOf: at}}, Mode: "estimated", BaselineNights: 28, TrendNights: 6, EvidenceAt: &evidence, ValidBefore: at + 12*sr.Hour, MissingComponents: []string{"hrv"}, Components: map[string]rc.V3Component{"hrv": {State: "missing_after_sync", Trend: number(50), TrendNights: 6, BaselineNights: 28, EvidenceAt: &evidence, Source: "synthetic-watch"}}}
	s.PhysiologicalRecovery = ProjectRecoveryV3(recovery, at, at)
	s.TrainingReadiness = ProjectReadinessV4(rd.V4Result{Result: rd.Result{ModelVersion: rd.ModelV4, Schema: rd.SchemaV4, AsOf: at, Status: "available", Quality: "estimated", Score: &score, ValidBefore: at + 12*sr.Hour}, Mode: "estimated", PhysiologicalCap: &score, SessionCap: number(80), Range: &rd.ScoreRange{Low: 70, High: 72}, EvidenceAt: &evidence, WeeklyCap: number(100)}, at, at)
	session := ProjectSessionRecovery(sr.Result{Schema: sr.Schema, AsOf: at, Status: "available", Mode: "estimated", RemainingHours: number(20), Range: &sr.Range{Low: 18, High: 20}, Scope: "recorded_running_next_hard_session", ValidBefore: at + 12*sr.Hour}, at, at)
	s.SessionRecovery = &session
	focus := ProjectFocus(tl.FocusResult{AsOf: at, Windows: []tl.FocusWindow{{WindowDays: 7, Buckets: tl.Buckets{Low: 100, High: 30, Unclassified: 20}, UnknownWorkouts: 1, ClassifiedCoverage: number(130. / 150), Denominator: "known_load", Method: "hrr_proxy"}}}, at, at)
	s.LoadFocus = &focus
	s.RunningAbility = ProjectAbilityEstimate(ra.Result{AsOf: at, SelectedMode: "submax_estimated", Demonstrated: ra.Branch{Mode: "demonstrated", Status: "collecting"}, Maximal: ra.Branch{Mode: "maximal_supported", Status: "collecting"}, Submax: ra.Branch{V60: number(3.3), Score: number(57), Range: &ra.Range{Low: 3.1, High: 3.6}, Mode: "submax_estimated", Status: "limited", EvidenceAt: evidence, Confidence: .35}, Progress: ra.Progress{Workouts: 4, SpanDays: 12, ShortWorkouts: 4, TargetDays: 3}}, at, at)
	b, e := json.MarshalIndent(s, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	b = append(b, '\n')
	for _, path := range []string{"../../../../contracts/fixtures/model-optimization-v3.json", "../../../../ios/RAI/Tests/ProductCoreTests/Fixtures/ModelOptimizationV3.json"} {
		if os.Getenv("RAI_UPDATE_M7_GOLDENS") == "1" {
			if e := os.WriteFile(path, b, 0644); e != nil {
				t.Fatal(e)
			}
		}
		want, e := os.ReadFile(path)
		if e != nil || !bytes.Equal(b, want) {
			t.Fatal("shared v3 fixture differs", e)
		}
	}
	if s.RunningAbility.Value.Gamma != nil || s.PhysiologicalRecovery.Envelope.Mode != "estimated" {
		t.Fatal("estimate manufactured full evidence")
	}
}
