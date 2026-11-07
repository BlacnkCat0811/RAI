package athletemodel

import (
	"testing"

	tl "rai/server/internal/trainingload"
	vo2 "rai/server/internal/vo2max"
)

func TestRAIVO2ProjectionIsIndependentAndExperimental(t *testing.T) {
	v, width := 52.0, .25
	r := vo2.Result{Schema: vo2.Schema, Model: vo2.Model, ModelVersion: vo2.Version, DatasetID: "d", Generation: 1, Status: "established", Quality: "available", Freshness: "current", Experimental: true, Unit: vo2.Unit, Value: &v, Confidence: .5, Workouts: 5, Stability: "stable", Sensitivity: vo2.Sensitivity{Range: &vo2.Range{Low: 46, High: 59}, Width: &width, Corners: 8}}
	m := ProjectRAIVO2(r, 10, 20)
	if m.Value == nil || m.Value.Value == nil || *m.Value.Value != 52 || !m.Value.Experimental || m.Value.Source != "rai_owned_submax" {
		t.Fatal(m)
	}
	if m.Envelope.MappingVersion != MappingVersion2 || m.Envelope.ModelVersion != "rai.vo2max.submax/v1" {
		t.Fatal(m.Envelope)
	}
	if m.Envelope.State != Available {
		t.Fatal(m.Envelope.State, m.Envelope.ModelStatus)
	}
	r.Experimental = false
	m = ProjectRAIVO2(r, 10, 20)
	if m.Envelope.ModelStatus != "technical_failure" {
		t.Fatal("non-experimental RAI VO2 accepted")
	}
}

func TestProjectedResponseStateIsTruthful(t *testing.T) {
	s := NewSnapshotV2(1, 1, "UTC", "2026-01-01", Assignment{Channel: "active", StackID: "s", Version: 1})
	SetProjectedResponseState(&s)
	if s.ResponseState != "unavailable_response" {
		t.Fatalf("empty=%s", s.ResponseState)
	}
	s.TrainingLoad.Envelope.State = Available
	SetProjectedResponseState(&s)
	if s.ResponseState != "partial_response" {
		t.Fatalf("partial=%s", s.ResponseState)
	}
	for _, e := range []*Envelope{&s.TrainingLoad.Envelope, &s.RunningAbility.Envelope, &s.ExternalVO2Max.Envelope, &s.PhysiologicalRecovery.Envelope, &s.TrainingReadiness.Envelope, &s.RAIVO2Max.Envelope} {
		e.State = Available
	}
	SetProjectedResponseState(&s)
	if s.ResponseState != "complete_response" {
		t.Fatalf("complete=%s", s.ResponseState)
	}
}

func TestProjectLoadV2UsesV2Identity(t *testing.T) {
	acute := 12.0
	m := ProjectLoadV2(&tl.DailyState{Day: "2026-01-01", Acute: tl.KernelValue{Value: &acute}, Quality: "complete"}, 1, 2, 3)
	if m.Envelope.ModelVersion != tl.AlgorithmV2+"/"+tl.VersionV2 || m.Envelope.PayloadVersion != tl.SchemaV2 {
		t.Fatalf("v2 load identity not preserved: %+v", m.Envelope)
	}
	if m.Value == nil || m.Value.StateModelVersion != tl.StateAlgorithmV2+"/"+tl.VersionV2 {
		t.Fatalf("state identity: %+v", m.Value)
	}
}
