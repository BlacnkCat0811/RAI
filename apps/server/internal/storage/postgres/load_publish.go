package postgres

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"rai/server/internal/analysis"
	tl "rai/server/internal/trainingload"
	wf "rai/server/internal/workoutfeatures"
)

func insertModelArtifact(ctx context.Context, tx pgx.Tx, dataset, run, key, schema string, value any, limit int) error {
	b, err := json.Marshal(value)
	if err != nil {
		return analysis.Invalid()
	}
	if len(b) > limit {
		return &analysis.Failure{Code: "analysis_input_limit"}
	}
	hash, _ := hex.DecodeString(tl.Hash(value))
	_, err = tx.Exec(ctx, `INSERT INTO analysis_artifacts(dataset_id,run_id,artifact_key,schema_version,content_sha256,payload) VALUES($1,$2,$3,$4,$5,$6)`, dataset, run, key, schema, hash, json.RawMessage(b))
	return err
}
func writeExposureArtifact(ctx context.Context, tx pgx.Tx, l Lease, id string, in analysis.Snapshot) error {
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM analysis_model_dependencies WHERE dataset_id=$1 AND upstream_scope=$2 AND downstream_scope LIKE 'model/%/workout_load/%')`, l.DatasetID, l.ScopeKey).Scan(&enabled); err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	if in.ExposureArtifact == nil || in.FeatureArtifact == nil || in.ExposureArtifact.InputHash != in.FeatureArtifact.Evidence.InputHash {
		return analysis.Invalid()
	}
	return insertModelArtifact(ctx, tx, l.DatasetID, id, wf.ExposureKey, wf.ExposureSchema, in.ExposureArtifact, wf.MaxExposureBytes)
}
func writeLoadArtifacts(ctx context.Context, tx pgx.Tx, l Lease, id string, in analysis.Snapshot) error {
	if l.Kind == tl.StateJobKind {
		if in.LoadStateArtifact == nil || in.LoadStateInput == nil || in.LoadStateArtifact.Generation != l.TargetGeneration || in.LoadStateArtifact.DatasetID != l.DatasetID || in.LoadStateArtifact.ConfigHash != tl.ConfigHash() {
			return analysis.Invalid()
		}
		return insertModelArtifact(ctx, tx, l.DatasetID, id, tl.StateKey, tl.StateSchema, in.LoadStateArtifact, 2<<20)
	}
	r := in.LoadArtifact
	if r == nil || in.LoadInput == nil || r.InputGeneration != l.TargetGeneration || r.DatasetID != l.DatasetID || r.ConfigHash != tl.ConfigHash() || r.FeatureArtifactID != in.LoadInput.FeatureRunID || r.Model != tl.Algorithm || r.ModelVersion != tl.Version {
		return analysis.Invalid()
	}
	if err := insertModelArtifact(ctx, tx, l.DatasetID, id, tl.ArtifactKey, tl.Schema, r, 128<<10); err != nil {
		return err
	}
	if in.LoadInput.Deleted {
		return nil
	}
	var parameter *string
	if r.HRReference != nil {
		parameter = &r.HRReference.ID
	}
	for _, d := range r.Days {
		if _, err := tx.Exec(ctx, `INSERT INTO analysis_load_ledger(dataset_id,run_id,local_date,workout_uuid,model_version,parameter_id,feature_run_id,contribution,observed_contribution,active_seconds,quality) VALUES($1,$2,$3::date,$4,$5,$6,$7,$8,$9,$10,$11)`, l.DatasetID, id, d.Day, r.WorkoutID, tl.Algorithm+"/"+tl.Version, parameter, r.FeatureArtifactID, d.Load, d.Observed, d.ActiveSeconds, d.Quality); err != nil {
			return err
		}
	}
	return nil
}
