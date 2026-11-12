//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"rai/server/internal/analysis"
	am "rai/server/internal/athletemodel"
	"rai/server/internal/modelmanifest"
	"rai/server/internal/modelrelease"
	rd "rai/server/internal/readiness"
	rc "rai/server/internal/recovery"
	"rai/server/internal/syncing"
	"rai/server/internal/training"
	tl "rai/server/internal/trainingload"
	vo2 "rai/server/internal/vo2max"
	ve "rai/server/internal/vo2maxexternal"
)

const retainedManifest = "654b005210f66d0e023e6044df1768684915e411c86ea9df89035018743afc6c"
const retainedStack = "rai-v2-654b005210f66d0e"

func setModelChannel(t *testing.T, f *fixture, channel, stack, manifest string) {
	t.Helper()
	ctx := context.Background()
	tx, err := f.s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL rai.model_assignment='on'`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO analysis_model_channels(dataset_id,channel,stack_id,config_sha256,assignment_version,extension_configs)
 VALUES($1,$2,$3,decode($4,'hex'),1,jsonb_build_object('rai.model_manifest/1',$4::text))
 ON CONFLICT(dataset_id,channel) DO UPDATE SET stack_id=EXCLUDED.stack_id,config_sha256=EXCLUDED.config_sha256,extension_configs=EXCLUDED.extension_configs`, f.p.DatasetID, channel, stack, manifest); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

// Retained publications are synthetic, with the exact frozen release configs.
// They go through the real immutable-artifact trigger and active-only view.
func publishRetainedDay(t *testing.T, f *fixture, today time.Time, read func(string, any)) {
	t.Helper()
	r, ok := modelrelease.Lookup(retainedStack, retainedManifest, "Asia/Shanghai")
	if !ok {
		t.Fatal("retained release missing")
	}
	var load tl.StateResult
	var recovery rc.V2Result
	var readiness rd.Result
	var native vo2.Result
	var external ve.Result
	read("load_state", &load)
	read("physiological_recovery", &recovery)
	read("training_readiness", &readiness)
	read(vo2.ArtifactKey, &native)
	read(ve.ArtifactKey, &external)
	load.Generation, load.ConfigHash = 1, r.LoadConfigHash
	// The retained release's v60 ability artifact: an audit row the current
	// code no longer reads or serves.
	ability := map[string]any{"schemaVersion": "rai.performance/2", "datasetID": f.p.DatasetID, "generation": 1, "configHash": r.AbilityConfigHash, "status": "available"}
	recovery.Generation, recovery.ConfigHash = 1, r.RecoveryConfig.Hash()
	recovery.Schema, recovery.ModelVersion = rc.SchemaV2, rc.ModelV2
	readiness.Generation, readiness.Config, readiness.ConfigHash = 1, r.ReadinessConfig, r.ReadinessConfig.Hash()
	readiness.Schema, readiness.ModelVersion = rd.SchemaV3, rd.ModelV3
	readiness.HabitualBurden = nil // This release uses the pre-habitual formula.
	score, baseline := 80.0, 10.0
	readiness.Score, readiness.RecoveryScore, readiness.Baseline.Value = &score, &score, &baseline
	readiness.Terms, readiness.Reasons = []rd.Term{}, []string{}
	readiness.Status, readiness.Quality = "available", "complete"
	readiness.ValidBefore = today.AddDate(0, 0, 1).UnixMicro()
	native.Generation, external.Generation = 1, 1
	ctx := context.Background()
	tx, err := f.s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	day, scope, job := today.Format(time.DateOnly), modelmanifest.DayScope(retainedStack, today.Format(time.DateOnly)), syncing.NewID()
	if _, err = tx.Exec(ctx, `INSERT INTO dirty_scopes(dataset_id,scope_key,generation,clean_generation,dirty_from,dirty_through) VALUES($1,$2,1,1,$3,$3)`, f.p.DatasetID, scope, today); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO jobs(id,dataset_id,kind,scope_key,target_generation,state) VALUES($1,$2,'analysis.model_stack_v2',$3,1,'succeeded')`, job, f.p.DatasetID, scope); err != nil {
		t.Fatal(err)
	}
	for _, a := range []struct {
		key, schema, algorithm, version string
		value                           any
	}{
		{"load_state", tl.StateSchemaV2, tl.StateAlgorithmV2, "v2", load},
		{"performance_state_v2", "rai.performance/2", "rai.performance_curve", "v2", ability},
		{"physiological_recovery", rc.SchemaV2, rc.Algorithm, "v2", recovery},
		{"training_readiness", rd.SchemaV3, rd.Algorithm, "v3", readiness},
		{vo2.ArtifactKey, vo2.Schema, vo2.Model, "v1", native},
		{ve.ArtifactKey, ve.Schema, ve.Algorithm, "v1", external},
	} {
		run := syncing.NewID()
		payload, err := json.Marshal(a.value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO analysis_runs(dataset_id,id,job_id,fencing_token,scope_key,target_generation,algorithm_name,algorithm_version,input_version,input_hash,input_from,input_through,lineage,status)
 VALUES($1,$2,$3,1,$4,1,$5,$6,'rai.model_stack.input/2',decode(repeat('00',32),'hex'),$7,$7,jsonb_build_object('manifestSHA256',$8::text),'RUNNING')`, f.p.DatasetID, run, job, scope, a.algorithm, a.version, today, retainedManifest); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO analysis_artifacts(dataset_id,run_id,artifact_key,schema_version,content_sha256,payload) VALUES($1,$2,$3,$4,decode($5,'hex'),$6::jsonb)`, f.p.DatasetID, run, a.key, a.schema, tl.Hash(a.value), payload); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `UPDATE analysis_runs SET status='SUCCEEDED',finished_at=now() WHERE dataset_id=$1 AND id=$2`, f.p.DatasetID, run); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO analysis_publications(dataset_id,scope_key,metric_family,day,published_run_id,generation) VALUES($1,$2,$3,$4,$5,1)`, f.p.DatasetID, scope, "model_product_v2/"+a.key, day, run); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestModelReleaseUpgradeReadsRetainedResultsAndRollsBack(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	today, read := runStackDay(t, f, nil, stackDayOptions{})
	if _, err := f.s.ReadAthleteProduct(ctx, f.p, time.Now()); !errors.Is(err, am.ErrNotActivated) {
		t.Fatalf("unassigned: %v", err)
	}
	publishRetainedDay(t, f, today, read)
	setModelChannel(t, f, "active", retainedStack, retainedManifest)
	monday := today.AddDate(0, 0, -(int(today.Weekday())+6)%7)
	plan, err := f.s.CreateTrainingPlan(ctx, f.p, training.CreateRequest{CommandID: syncing.NewID(), PlanID: syncing.NewID(), Config: training.Config{
		StartDate: monday.Format(time.DateOnly), Weeks: 4, AvailableDays: []int{2, 4, 7}, LongRunDay: 7,
		Goal: training.Goal{Type: "running", Date: monday.AddDate(0, 0, 26).Format(time.DateOnly), DistanceM: 5000}}})
	if err != nil {
		t.Fatal(err)
	}
	assertRetained := func() {
		t.Helper()
		s, err := f.s.ReadAthleteProduct(ctx, f.p, time.Now())
		if err != nil || s.Assignment.StackID != retainedStack {
			t.Fatalf("retained read: %v", err)
		}
		for _, e := range []am.Envelope{s.TrainingLoad.Envelope, s.PhysiologicalRecovery.Envelope, s.TrainingReadiness.Envelope, s.ExternalVO2Max.Envelope, s.RAIVO2Max.Envelope} {
			if e.ModelVersion == "" || e.TechnicalError != nil || e.ModelStatus == "not_product_activated" {
				t.Fatalf("retained slot rejected: %+v", e)
			}
		}
		if s.RunningAbility.Value != nil || s.RunningAbility.Envelope.ModelStatus != "model_update_required" {
			t.Fatalf("retired v60 ability served from a retained release: %+v", s.RunningAbility.Envelope)
		}
		if s.TrainingReadiness.Value == nil || s.TrainingReadiness.Value.Score == nil || *s.TrainingReadiness.Value.Score != 80 {
			t.Fatal("retained readiness formula not preserved")
		}
		input, err := f.s.LoadTraining(ctx, analysis.Lease{DatasetID: f.p.DatasetID, ScopeKey: "plan:" + plan.ID}, time.Now())
		if err != nil {
			t.Fatalf("retained training inputs: %v", err)
		}
		found := false
		for _, signal := range input.Context.Signals {
			if signal.Name == "readiness" && signal.Value != nil && *signal.Value == 80 {
				found = true
			}
		}
		if !found {
			t.Fatal("retained readiness missing from training inputs")
		}
		for _, metric := range []string{"trainingLoad", "physiologicalRecovery", "trainingReadiness", "externalVO2Max", "raiVO2Max"} {
			p, err := f.s.ReadAthleteHistory(ctx, f.p, metric, today.Format(time.DateOnly), today.Format(time.DateOnly), 30, "", "revised")
			if err != nil || len(p.Points) != 1 || p.ManifestSHA != retainedManifest {
				t.Fatalf("retained history %s: %v", metric, err)
			}
		}
		if _, err := f.s.ReadAthleteHistory(ctx, f.p, "runningAbility", today.Format(time.DateOnly), today.Format(time.DateOnly), 30, "", "revised"); !errors.Is(err, am.ErrUnsupportedRelease) {
			t.Fatalf("retired ability history: %v", err)
		}
	}
	assertRetained() // Current candidate must not replace the active old release.
	hash, _ := modelmanifest.CurrentHash()
	m := modelmanifest.Current()
	digest := strings.Repeat("a", 64)
	gate := am.PromotionGate{Model: m.VO2Max.ModelID, Version: m.VO2Max.Version, ManifestSHA: hash, ConfigSHA: hash,
		FeatureSchema: m.Features.Schema, SourcePolicy: m.VO2Max.SourcePolicy, InputRegistry: m.InputRegistry, MappingVersion: m.MappingVersion,
		ConsumerSchema: m.ProductSchema, DatasetID: f.p.DatasetID, SyntheticDataset: true, EngineeringReport: digest, DeviceDataReport: digest,
		ApplicabilityReport: digest, UXPrivacyReport: digest, RollbackReport: digest, Authorization: digest, Reviewer: "synthetic-test", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)}
	if _, err := f.s.Pool.Exec(ctx, `SELECT rai_schedule_model_day($1,$2,$3::date,clock_timestamp())`, f.p.DatasetID, modelmanifest.StackID(), today.Format(time.DateOnly)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.PromoteModelStack(ctx, f.p.DatasetID, "synthetic-test", modelmanifest.StackID(), gate, hash); !errors.Is(err, am.ErrGateClosed) {
		t.Fatalf("unready candidate promoted: %v", err)
	}
	assertRetained()
	if processStackJobs(t, f) == 0 {
		t.Fatal("candidate did not finish")
	}
	if _, err := f.s.PromoteModelStack(ctx, f.p.DatasetID, "synthetic-test", modelmanifest.StackID(), gate, hash); err != nil {
		t.Fatal(err)
	}
	current, err := f.s.ReadAthleteProductV4(ctx, f.p, time.Now())
	if err != nil || current.Assignment.StackID != modelmanifest.StackID() || current.RunningFitness == nil || current.RunningFitness.Envelope.TechnicalError != nil {
		t.Fatalf("promotion: %v", err)
	}
	if old, err := f.s.ReadAthleteProductV3(ctx, f.p, time.Now()); err != nil || old.TrainingReadiness.Envelope.ModelStatus != "model_update_required" {
		t.Fatalf("schema3 client must be told to update: %v", err)
	}
	if _, err := f.s.LoadTraining(ctx, analysis.Lease{DatasetID: f.p.DatasetID, ScopeKey: "plan:" + plan.ID}, time.Now()); err != nil {
		t.Fatalf("current training inputs: %v", err)
	}
	if _, err := f.s.RollbackModelStack(ctx, f.p.DatasetID, "synthetic-test", hash); err != nil {
		t.Fatal(err)
	}
	assertRetained()
	if _, err := f.s.Pool.Exec(ctx, `UPDATE dirty_scopes SET generation=generation+1 WHERE dataset_id=$1 AND scope_key=$2`, f.p.DatasetID, modelmanifest.DayScope(retainedStack, today.Format(time.DateOnly))); err != nil {
		t.Fatal(err)
	}
	s, err := f.s.ReadAthleteProduct(ctx, f.p, time.Now())
	if err != nil || s.TrainingReadiness.Value != nil || s.TrainingReadiness.Envelope.ModelStatus != "model_update_required" {
		t.Fatalf("dirty retained release: %+v %v", s.TrainingReadiness.Envelope, err)
	}
}

func TestModelReleasePendingFailureAndUnknownNeverClaimUnactivated(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	today, _ := runStackDay(t, f, nil, stackDayOptions{})
	hash, _ := modelmanifest.CurrentHash()
	stack, scope := modelmanifest.StackID(), modelmanifest.DayScope(modelmanifest.StackID(), today.Format(time.DateOnly))
	setModelChannel(t, f, "active", stack, hash)
	if _, err := f.s.Pool.Exec(ctx, `SELECT rai_schedule_model_day($1,$2,$3::date,clock_timestamp())`, f.p.DatasetID, stack, today.Format(time.DateOnly)); err != nil {
		t.Fatal(err)
	}
	s, err := f.s.ReadAthleteProductV4(ctx, f.p, time.Now())
	if err != nil || s.TrainingReadiness.Envelope.ModelStatus != "pending" || s.RunningAbility.Value != nil || s.RunningFitness == nil || s.RunningFitness.Envelope.ModelStatus != "pending" {
		t.Fatalf("pending: %+v %v", s.TrainingReadiness.Envelope, err)
	}
	if _, err := f.s.Pool.Exec(ctx, `UPDATE jobs SET state='failed',last_error_code='analysis_invalid_input' WHERE dataset_id=$1 AND scope_key=$2 AND state='queued'`, f.p.DatasetID, scope); err != nil {
		t.Fatal(err)
	}
	s, err = f.s.ReadAthleteProductV4(ctx, f.p, time.Now())
	if err != nil || s.TrainingReadiness.Envelope.ModelStatus != "technical_failure" {
		t.Fatalf("failure: %+v %v", s.TrainingReadiness.Envelope, err)
	}
	unknown := strings.Repeat("b", 64)
	setModelChannel(t, f, "active", "rai-v2-"+unknown[:16], unknown)
	s, err = f.s.ReadAthleteProductV4(ctx, f.p, time.Now())
	if err != nil || s.TrainingReadiness.Envelope.ModelStatus != "unsupported" {
		t.Fatalf("unsupported: %+v %v", s.TrainingReadiness.Envelope, err)
	}
	if _, err = f.s.ReadAthleteHistory(ctx, f.p, "trainingReadiness", "", "", 30, "", "revised"); !errors.Is(err, am.ErrUnsupportedRelease) {
		t.Fatalf("unsupported history: %v", err)
	}
}
