package athletemodel

import "testing"

func TestDowngradeKeepsUnchangedMetricsForOlderApps(t *testing.T) {
	s := NewSnapshotV4(1, 1, "UTC", "2026-01-01", Assignment{Channel: "active", StackID: "s", Version: 1})
	score := 70.0
	s.PhysiologicalRecovery.Envelope.State, s.PhysiologicalRecovery.Envelope.Mode = Available, "estimated"
	s.PhysiologicalRecovery.Value = &RecoveryValue{Score: &score}
	s.TrainingReadiness.Envelope.State, s.TrainingReadiness.Envelope.Mode = Available, "complete"
	s.TrainingReadiness.Value = &ReadinessValue{Score: &score}
	s.TrainingLoad.Envelope.State = Available
	s.TrainingLoad.Value = &TrainingLoadValue{}

	v3 := Downgrade(s, Schema3)
	if v3.SchemaVersion != Schema3 || v3.RunningFitness != nil || v3.SessionRecovery == nil || v3.LoadFocus == nil {
		t.Fatalf("schema3 projection %+v", v3.SchemaVersion)
	}
	if v3.PhysiologicalRecovery.Value == nil || v3.TrainingReadiness.Value == nil || v3.TrainingLoad.Value == nil {
		t.Fatal("schema3 must keep every metric it can express")
	}

	v2 := Downgrade(s, Schema2)
	if v2.SchemaVersion != Schema2 || v2.RunningFitness != nil || v2.SessionRecovery != nil || v2.LoadFocus != nil {
		t.Fatal("schema2 must drop slots it does not know")
	}
	if v2.TrainingLoad.Value == nil || v2.TrainingReadiness.Value == nil {
		t.Fatal("complete metrics must survive the schema2 projection")
	}
	if v2.PhysiologicalRecovery.Value != nil || v2.PhysiologicalRecovery.Envelope.ModelStatus != "model_update_required" {
		t.Fatal("an estimated score cannot be shown as a schema2 complete score")
	}
	if v2.ResponseState != "partial_response" {
		t.Fatalf("response state %s", v2.ResponseState)
	}
	if same := Downgrade(s, Schema4); same.SchemaVersion != Schema4 || same.RunningFitness == nil {
		t.Fatal("schema4 is served unchanged")
	}
}

func TestModelSwitchingIsAServerReasonNotAnAppUpdate(t *testing.T) {
	codes := MapReasons("model_switching", nil)
	if len(codes) != 1 || codes[0] != "model_switching" {
		t.Fatalf("codes %v", codes)
	}
	if PrimaryReason([]ProductReasonCode{"model_update_required", "model_switching"}) != "model_switching" {
		t.Fatal("a server-side switch outranks an App update")
	}
}
