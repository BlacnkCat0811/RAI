//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"rai/server/internal/analysis"
	"rai/server/internal/readiness"
	"rai/server/internal/syncing"
	"rai/server/internal/training"
)

func setTrainingActiveStack(t *testing.T, f *fixture, stack string, version int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.s.Pool.Exec(ctx, `INSERT INTO analysis_model_channels(dataset_id,channel,stack_id,config_sha256,assignment_version) VALUES($1,'active','legacy',decode(repeat('00',32),'hex'),1) ON CONFLICT(dataset_id,channel) DO NOTHING`, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}
	tx, err := f.s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL rai.model_assignment='on'`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE analysis_model_channels SET stack_id=$2,config_sha256=decode(repeat('ab',32),'hex'),assignment_version=$3 WHERE dataset_id=$1 AND channel='active'`, f.p.DatasetID, stack, version); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestTrainingPlanRecordsModelBindingAndRollbackAdapter(t *testing.T) {
	f := setup(t)
	setTrainingActiveStack(t, f, "legacy", 1)
	p1, err := f.s.CreateTrainingPlan(context.Background(), f.p, training.CreateRequest{CommandID: syncing.NewID(), PlanID: syncing.NewID(), Config: trainingConfig()})
	if err != nil {
		t.Fatal(err)
	}
	if p1.Config.ModelBinding == nil || p1.Config.ModelBinding.Adapter != "legacy-v1" || p1.Config.ModelBinding.Model != "rai.athlete_model/1" {
		t.Fatalf("legacy binding=%+v", p1.Config.ModelBinding)
	}
	setTrainingActiveStack(t, f, "synthetic-v2", 2)
	p2, err := f.s.CreateTrainingPlan(context.Background(), f.p, training.CreateRequest{CommandID: syncing.NewID(), PlanID: syncing.NewID(), Config: trainingConfig()})
	if err != nil {
		t.Fatal(err)
	}
	if p2.Config.ModelBinding == nil || p2.Config.ModelBinding.Adapter != "schema2-v2" || p2.Config.ModelBinding.Assignment != "synthetic-v2" || p2.Config.ModelBinding.ManifestSHA == "" || p2.Config.ModelBinding.AsOf == "" {
		t.Fatalf("v2 binding=%+v", p2.Config.ModelBinding)
	}
	setTrainingActiveStack(t, f, "legacy", 3)
	p3, err := f.s.CreateTrainingPlan(context.Background(), f.p, training.CreateRequest{CommandID: syncing.NewID(), PlanID: syncing.NewID(), Config: trainingConfig()})
	if err != nil {
		t.Fatal(err)
	}
	if p3.Config.ModelBinding == nil || p3.Config.ModelBinding.Adapter != "legacy-v1" {
		t.Fatalf("rollback binding=%+v", p3.Config.ModelBinding)
	}
	again, err := f.s.TrainingPlan(context.Background(), f.p, p2.ID)
	if err != nil || again.Config.ModelBinding == nil || again.Config.ModelBinding.Adapter != "schema2-v2" {
		t.Fatalf("historical plan binding rewritten: %+v %v", again.Config.ModelBinding, err)
	}
}

func TestTrainingContextUsesV2ReadinessSignal(t *testing.T) {
	f := setup(t)
	setTrainingActiveStack(t, f, "synthetic-v2", 2)
	p, err := f.s.CreateTrainingPlan(context.Background(), f.p, training.CreateRequest{CommandID: syncing.NewID(), PlanID: syncing.NewID(), Config: trainingConfig()})
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	day := now.Format("2006-01-02")
	end := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, loc).UnixMicro()
	nowUs := now.UnixMicro()
	score := 82.0
	cfg := readiness.DefaultV2Config("Asia/Shanghai")
	result := readiness.Result{Date: day, Schema: readiness.SchemaV2, Dataset: f.p.DatasetID, Generation: 1, ModelVersion: readiness.ModelV2, ConfigHash: cfg.Hash(), Config: cfg, InputHash: "x", CalendarHash: "x", AsOf: nowUs, EvaluationAt: nowUs, CapturedAt: nowUs, ValidBefore: end, Status: "available", Quality: "complete", Score: &score, Coverage: readiness.Coverage{Through: nowUs, CaughtUp: true}}
	seedV2Artifact(t, f, readiness.ScopeV2For("synthetic-v2"), "training_readiness", readiness.SchemaV2, readiness.Algorithm, "v2", "model_product_v2", day, result)
	in, err := f.s.LoadTraining(context.Background(), analysis.Lease{DatasetID: f.p.DatasetID, ScopeKey: "plan:" + p.ID}, now)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range in.Context.Signals {
		if s.Name == "readiness" && s.Algorithm == "rai.readiness.training" {
			found = true
			if s.Value == nil || *s.Value != score {
				t.Fatalf("v2 readiness value=%v", s.Value)
			}
		}
	}
	if !found {
		t.Fatalf("v2 readiness signal missing: %+v", in.Context.Signals)
	}
}

func TestTrainingContextIgnoresV2ReadinessOnLegacyAdapter(t *testing.T) {
	f := setup(t)
	setTrainingActiveStack(t, f, "legacy", 1)
	p, err := f.s.CreateTrainingPlan(context.Background(), f.p, training.CreateRequest{CommandID: syncing.NewID(), PlanID: syncing.NewID(), Config: trainingConfig()})
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	day := now.Format("2006-01-02")
	end := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, loc).UnixMicro()
	score := 90.0
	cfg := readiness.DefaultV2Config("Asia/Shanghai")
	result := readiness.Result{Date: day, Schema: readiness.SchemaV2, Dataset: f.p.DatasetID, Generation: 1, ModelVersion: readiness.ModelV2, ConfigHash: cfg.Hash(), Config: cfg, AsOf: now.UnixMicro(), ValidBefore: end, Status: "available", Quality: "complete", Score: &score}
	seedV2Artifact(t, f, readiness.ScopeV2For("synthetic-v2"), "training_readiness", readiness.SchemaV2, readiness.Algorithm, "v2", "model_product_v2", day, result)
	in, err := f.s.LoadTraining(context.Background(), analysis.Lease{DatasetID: f.p.DatasetID, ScopeKey: "plan:" + p.ID}, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range in.Context.Signals {
		if s.Name == "readiness" && s.Algorithm == "rai.readiness.training" {
			t.Fatal("legacy plan consumed v2 readiness")
		}
	}
}

func TestTrainingContextDoesNotUseExpiredV2Readiness(t *testing.T) {
	f := setup(t)
	setTrainingActiveStack(t, f, "synthetic-v2", 2)
	p, err := f.s.CreateTrainingPlan(context.Background(), f.p, training.CreateRequest{CommandID: syncing.NewID(), PlanID: syncing.NewID(), Config: trainingConfig()})
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	day := now.Format("2006-01-02")
	score := 90.0
	cfg := readiness.DefaultV2Config("Asia/Shanghai")
	result := readiness.Result{Date: day, Schema: readiness.SchemaV2, Dataset: f.p.DatasetID, Generation: 1, ModelVersion: readiness.ModelV2, ConfigHash: cfg.Hash(), Config: cfg, AsOf: now.Add(-2 * time.Hour).UnixMicro(), ValidBefore: now.Add(-time.Hour).UnixMicro(), Status: "available", Quality: "complete", Score: &score}
	seedV2Artifact(t, f, readiness.ScopeV2For("synthetic-v2"), "training_readiness", readiness.SchemaV2, readiness.Algorithm, "v2", "model_product_v2", day, result)
	in, err := f.s.LoadTraining(context.Background(), analysis.Lease{DatasetID: f.p.DatasetID, ScopeKey: "plan:" + p.ID}, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range in.Context.Signals {
		if s.Name == "readiness" && s.Algorithm == "rai.readiness.training" {
			if s.Value != nil {
				t.Fatalf("expired v2 readiness yielded %v", *s.Value)
			}
			return
		}
	}
	t.Fatal("expired v2 readiness signal not represented")
}
