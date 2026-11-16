//go:build integration

package integration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	am "rai/server/internal/athletemodel"
	"rai/server/internal/modelmanifest"
)

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

// TestModelReleasePromoteAndRollback promotes the current stack on a
// synthetic dataset and rolls back to the previous assignment (here: none).
func TestModelReleasePromoteAndRollback(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	runStackDay(t, f, nil, stackDayOptions{})
	if _, err := f.s.ReadAthleteProduct(ctx, f.p, time.Now()); !errors.Is(err, am.ErrNotActivated) {
		t.Fatalf("candidate must not be readable: %v", err)
	}
	hash, _ := modelmanifest.CurrentHash()
	m := modelmanifest.Current()
	digest := strings.Repeat("a", 64)
	gate := am.PromotionGate{Model: m.VO2Max.ModelID, Version: m.VO2Max.Version, ManifestSHA: hash, ConfigSHA: hash,
		FeatureSchema: m.Features.Schema, SourcePolicy: m.VO2Max.SourcePolicy, InputRegistry: m.InputRegistry, MappingVersion: m.MappingVersion,
		ConsumerSchema: m.ProductSchema, DatasetID: f.p.DatasetID, SyntheticDataset: true, EngineeringReport: digest, DeviceDataReport: digest,
		ApplicabilityReport: digest, UXPrivacyReport: digest, RollbackReport: digest, Authorization: digest, Reviewer: "synthetic-test", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)}
	if _, err := f.s.PromoteModelStack(ctx, f.p.DatasetID, "synthetic-test", modelmanifest.StackID(), gate, hash); err != nil {
		t.Fatal(err)
	}
	current, err := f.s.ReadAthleteProduct(ctx, f.p, time.Now())
	if err != nil || current.SchemaVersion != am.Schema4 || current.Assignment.StackID != modelmanifest.StackID() || current.RunningFitness == nil || current.RunningFitness.Envelope.TechnicalError != nil {
		t.Fatalf("promotion: %v", err)
	}
	if _, err := f.s.RollbackModelStack(ctx, f.p.DatasetID, "synthetic-test", hash); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ReadAthleteProduct(ctx, f.p, time.Now()); !errors.Is(err, am.ErrNotActivated) {
		t.Fatalf("rollback to no assignment: %v", err)
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
	// The previous registered release is active (before auto-activation or
	// after a rollback) and has no clean result: the server is switching
	// models, which an App update cannot fix.
	const previous = "be2ffee8ea215e14c0a0cb052d7c1b94ba351b431981ae75e073fe5eb77c6641"
	setModelChannel(t, f, "active", "rai-v2-"+previous[:16], previous)
	s, err = f.s.ReadAthleteProductV4(ctx, f.p, time.Now())
	if err != nil || s.TrainingReadiness.Envelope.ModelStatus != "model_switching" || s.TrainingReadiness.Envelope.PrimaryReason != "model_switching" {
		t.Fatalf("switching: %+v %v", s.TrainingReadiness.Envelope, err)
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
