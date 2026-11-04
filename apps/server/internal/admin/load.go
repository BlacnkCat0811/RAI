package admin

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
	tl "rai/server/internal/trainingload"
	wf "rai/server/internal/workoutfeatures"
	"time"
)

// QueueTrainingLoadCandidate registers the existing candidate dependency graph
// and requests one feature->load->ledger->state pipeline in a dataset transaction.
func QueueTrainingLoadCandidate(ctx context.Context, s *postgres.Store, dataset, workout, operation, actor string) error {
	if !syncing.ValidID(dataset) || !syncing.ValidID(workout) || !syncing.ValidID(operation) || len(actor) < 1 || len(actor) > 200 {
		return errors.New("explicit dataset, workout, operation and actor required")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockAdministrativeDataset(ctx, tx, dataset); err != nil {
		return err
	}
	details := map[string]string{"workoutID": workout, "stackID": wf.StackID(), "configHash": wf.DefaultPolicy().Hash(), "action": "training_load_compute", "loadModel": tl.Algorithm + "/" + tl.Version, "stateModel": tl.StateAlgorithm + "/" + tl.Version, "loadConfigHash": tl.ConfigHash()}
	b, _ := json.Marshal(details)
	var previous []byte
	err = tx.QueryRow(ctx, `SELECT details FROM admin_operations WHERE operation_id=$1 AND operation_type='feature_candidate' AND subject_dataset=$2 AND actor=$3`, operation, dataset, actor).Scan(&previous)
	if err == nil {
		var p map[string]string
		_ = json.Unmarshal(previous, &p)
		pb, _ := json.Marshal(p)
		if string(pb) != string(b) {
			return errors.New("operation conflict")
		}
		return tx.Commit(ctx)
	}
	var from, through time.Time
	err = tx.QueryRow(ctx, `SELECT event_start,event_end FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2`, dataset, workout).Scan(&from, &through)
	if err != nil {
		return err
	}
	legacy := sha256.Sum256([]byte("legacy:workout.bundle/v2:training_state.bundle/v2"))
	_, err = tx.Exec(ctx, `INSERT INTO analysis_model_channels(dataset_id,channel,stack_id,config_sha256,assignment_version) VALUES($1,'active','legacy',$2,1) ON CONFLICT DO NOTHING`, dataset, legacy[:])
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO analysis_model_channels(dataset_id,channel,stack_id,config_sha256,assignment_version) VALUES($1,'candidate',$2,decode($3,'hex'),1) ON CONFLICT DO NOTHING`, dataset, wf.StackID(), wf.DefaultPolicy().Hash())
	if err != nil {
		return err
	}
	var matches bool
	if err = tx.QueryRow(ctx, `SELECT stack_id=$2 AND config_sha256=decode($3,'hex') FROM analysis_model_channels WHERE dataset_id=$1 AND channel='candidate'`, dataset, wf.StackID(), wf.DefaultPolicy().Hash()).Scan(&matches); err != nil {
		return err
	}
	if !matches {
		return errors.New("candidate assignment conflict")
	}
	// Explicit enrollment ends an external-only slot's M1 suppression.
	if _, err = tx.Exec(ctx, `UPDATE analysis_model_channels SET extension_configs=extension_configs-'rai.vo2max.external_only/v1' WHERE dataset_id=$1 AND channel='candidate' AND extension_configs ? 'rai.vo2max.external_only/v1'`, dataset); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO admin_operations(operation_id,operation_type,actor,status,subject_dataset,details) VALUES($1,'feature_candidate',$2,'committed',$3,$4)`, operation, actor, dataset, b)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO analysis_model_dependencies(dataset_id,upstream_scope,downstream_scope) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, dataset, wf.Scope(workout), tl.Scope(workout))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `SELECT rai_analysis_dirty($1,$2,$3,$4,$5)`, dataset, wf.Scope(workout), wf.JobKind, from, through)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RecomputeTrainingLoad explicitly revises only the selected workout dates;
// surrounding state reads at most 186 seed days from the immutable ledger.
// HR input version denotes the schema; each workout still resolves its own
// effective-dated input ID as of its feature run, with no parameter override.
func RecomputeTrainingLoad(ctx context.Context, s *postgres.Store, dataset, fromDate, throughDate, modelVersion, parameterVersion string) (int, error) {
	if !syncing.ValidID(dataset) || modelVersion != tl.Algorithm+"/"+tl.Version || parameterVersion != "rai.hr_reference/1" {
		return 0, errors.New("explicit supported dataset/model/parameter version required")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if err = lockAdministrativeDataset(ctx, tx, dataset); err != nil {
		return 0, err
	}
	var tz string
	if err = tx.QueryRow(ctx, `SELECT timezone FROM datasets WHERE id=$1`, dataset).Scan(&tz); err != nil {
		return 0, err
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return 0, err
	}
	from, err := time.ParseInLocation("2006-01-02", fromDate, loc)
	if err != nil {
		return 0, err
	}
	through, err := time.ParseInLocation("2006-01-02", throughDate, loc)
	if err != nil || through.Before(from) || through.After(from.AddDate(0, 0, 365)) {
		return 0, errors.New("bounded dates required")
	}
	var enabled bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM analysis_model_dependencies WHERE dataset_id=$1 AND downstream_scope LIKE 'model/%/workout_load/%')`, dataset).Scan(&enabled); err != nil {
		return 0, err
	}
	if !enabled {
		return 0, errors.New("explicit candidate compute required first")
	}
	rows, err := tx.Query(ctx, `SELECT workout_uuid::text,event_start,event_end FROM workouts WHERE dataset_id=$1 AND event_start<$3 AND event_end>$2 ORDER BY event_start,workout_uuid LIMIT 1001`, dataset, from, through.AddDate(0, 0, 1))
	if err != nil {
		return 0, err
	}
	type ref struct {
		id   string
		a, b time.Time
	}
	refs := []ref{}
	for rows.Next() {
		var r ref
		if err = rows.Scan(&r.id, &r.a, &r.b); err != nil {
			rows.Close()
			return 0, err
		}
		refs = append(refs, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	if len(refs) > 1000 {
		return 0, errors.New("recompute workout limit")
	}
	for _, r := range refs {
		if _, err = tx.Exec(ctx, `INSERT INTO analysis_model_dependencies(dataset_id,upstream_scope,downstream_scope) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, dataset, wf.Scope(r.id), tl.Scope(r.id)); err != nil {
			return 0, err
		}
		if _, err = tx.Exec(ctx, `SELECT rai_analysis_dirty($1,$2,$3,$4,$5)`, dataset, wf.Scope(r.id), wf.JobKind, r.a, r.b); err != nil {
			return 0, err
		}
	}
	if _, err = tx.Exec(ctx, `SELECT rai_analysis_dirty($1,$2,$3,$4,$5)`, dataset, tl.StateScope(), tl.StateJobKind, from, through.AddDate(0, 0, 1)); err != nil {
		return 0, err
	}
	return len(refs), tx.Commit(ctx)
}
