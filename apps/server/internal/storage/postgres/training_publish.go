package postgres

import (
	"context"
	"errors"
	"time"

	"rai/server/internal/syncing"
	"rai/server/internal/training"

	"github.com/jackc/pgx/v5"
)

func (s *Store) StartTrainingRun(ctx context.Context, l Lease, in training.Input) (string, error) {
	hash, e := training.Hash(in.Context)
	if e != nil {
		return "", e
	}
	id := syncing.NewID()
	tx, e := s.begin(ctx)
	if e != nil {
		return "", e
	}
	defer tx.Rollback(ctx)
	if e = trainingLock(ctx, tx, l.DatasetID); e != nil {
		return "", e
	}
	r, e := tx.Exec(ctx, `INSERT INTO training_generation_runs(dataset_id,id,plan_id,job_id,fencing_token,generation,config_revision,generator,parameter_version,input_hash,context,status)
 SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'RUNNING' WHERE EXISTS(SELECT 1 FROM jobs WHERE dataset_id=$1 AND id=$4 AND fencing_token=$5 AND state='running' AND lease_until>clock_timestamp())`, l.DatasetID, id, in.Plan.ID, l.ID, l.FencingToken, l.TargetGeneration, in.Plan.ConfigRevision, training.GeneratorFor(in.Plan.Config), training.ParameterVersionFor(in.Plan.Config), hash, trainingJSON(in.Context))
	if e != nil {
		return "", e
	}
	if r.RowsAffected() != 1 {
		return "", syncing.Fail(409, "lease_lost")
	}
	return id, tx.Commit(ctx)
}
func (s *Store) PublishTraining(ctx context.Context, l Lease, id string, in training.Input, base training.Base) (string, error) {
	tx, e := s.begin(ctx)
	if e != nil {
		return "", e
	}
	defer tx.Rollback(ctx)
	if e = trainingLock(ctx, tx, l.DatasetID); e != nil {
		return "", e
	}
	if e = validAnalysisLease(ctx, tx, l); e != nil {
		return "", e
	}
	var deadline time.Time
	if e = tx.QueryRow(ctx, `SELECT lease_until FROM jobs WHERE id=$1`, l.ID).Scan(&deadline); e != nil {
		return "", e
	}
	p, e := loadPlan(ctx, tx, l.DatasetID, trainingPlanID(l))
	if e != nil {
		return "", e
	}
	status := "SUCCEEDED"
	if !p.Active || p.Generation != l.TargetGeneration || in.Generation != l.TargetGeneration || p.ConfigRevision != in.Plan.ConfigRevision {
		status = "SUPERSEDED"
	}
	if status == "SUCCEEDED" && in.Context.ObservedModelBinding != nil {
		binding, err := loadTrainingModelBinding(ctx, tx, l.DatasetID)
		if err != nil {
			return "", err
		}
		snapshot := in.Context.ObservedModelBinding
		if binding.Assignment != snapshot.Assignment || binding.AssignmentVersion != snapshot.AssignmentVersion || binding.ManifestSHA != snapshot.ManifestSHA {
			status = "SUPERSEDED"
			if _, err := tx.Exec(ctx, `SELECT rai_training_dirty($1,$2)`, l.DatasetID, p.ID); err != nil {
				return "", err
			}
		}
	}
	// Bind the published result to the immutable run's exact context and lease.
	hash, e := training.Hash(in.Context)
	if e != nil {
		return "", e
	}
	var valid bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM training_generation_runs WHERE dataset_id=$1 AND id=$2 AND plan_id=$3 AND job_id=$4 AND fencing_token=$5 AND generation=$6 AND input_hash=$7 AND status='RUNNING')`, l.DatasetID, id, p.ID, l.ID, l.FencingToken, l.TargetGeneration, hash).Scan(&valid); e != nil {
		return "", e
	}
	if !valid {
		return "", training.ErrInvalid
	}
	if status == "SUCCEEDED" {
		// Re-evaluate pure output before persisting so alternate callers cannot
		// publish an unrelated base under the recorded generator/context hash.
		expected, e := training.Generate(in.Context)
		if e != nil {
			return "", e
		}
		eh, e := training.Hash(expected)
		if e != nil {
			return "", e
		}
		bh, e := training.Hash(base)
		if e != nil {
			return "", e
		}
		if string(eh) != string(bh) {
			return "", training.ErrInvalid
		}
		if _, e = tx.Exec(ctx, `INSERT INTO training_sessions(dataset_id,plan_id,id,origin,slot)
 SELECT $1,$2,(v->>'sessionID')::uuid,'generated',(v->>'slot')::integer FROM jsonb_array_elements($3::jsonb) v ON CONFLICT DO NOTHING`, l.DatasetID, p.ID, trainingJSON(base.Sessions)); e != nil {
			return "", e
		}

		var number int64
		if e = tx.QueryRow(ctx, `SELECT base_number+1 FROM training_plans WHERE dataset_id=$1 AND id=$2 FOR UPDATE`, l.DatasetID, p.ID).Scan(&number); e != nil {
			return "", e
		}
		baseID := syncing.NewID()
		if _, e = tx.Exec(ctx, `INSERT INTO training_base_versions(dataset_id,plan_id,id,number,run_id,base) VALUES($1,$2,$3,$4,$5,$6)`, l.DatasetID, p.ID, baseID, number, id, trainingJSON(base)); e != nil {
			return "", e
		}
		if _, e = materialize(ctx, tx, l.DatasetID, p, baseID, base); e != nil {
			return "", e
		}
		if _, e = tx.Exec(ctx, `UPDATE training_plans SET current_base_id=$3,base_number=$4 WHERE dataset_id=$1 AND id=$2`, l.DatasetID, p.ID, baseID, number); e != nil {
			return "", e
		}
		for _, ex := range in.Executions {
			var w *training.Workout
			if x, ok := in.Workouts[ex.WorkoutID]; ok {
				w = &x
			}
			if e = writeTrainingFeedback(ctx, tx, l.DatasetID, ex, w); e != nil {
				return "", e
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE dirty_scopes SET clean_generation=$3 WHERE dataset_id=$1 AND scope_key=$2 AND generation=$3`, l.DatasetID, l.ScopeKey, l.TargetGeneration); e != nil {
			return "", e
		}
	}
	r, e := tx.Exec(ctx, `UPDATE training_generation_runs SET status=$3,completed_at=now() WHERE dataset_id=$1 AND id=$2 AND status='RUNNING'`, l.DatasetID, id, status)
	if e != nil {
		return "", e
	}
	if r.RowsAffected() != 1 {
		return "", training.ErrInvalid
	}
	r, e = tx.Exec(ctx, `UPDATE jobs SET state='succeeded',lease_until=NULL,last_error_code=$4 WHERE dataset_id=$1 AND id=$2 AND fencing_token=$3 AND state='running' AND lease_until>clock_timestamp()`, l.DatasetID, l.ID, l.FencingToken, status)
	if e != nil {
		return "", e
	}
	if r.RowsAffected() != 1 {
		return "", syncing.Fail(409, "lease_lost")
	}
	r, e = tx.Exec(ctx, `UPDATE job_attempts SET finished_at=now(),result_code=$3 WHERE job_id=$1 AND fencing_token=$2 AND finished_at IS NULL`, l.ID, l.FencingToken, status)
	if e != nil {
		return "", e
	}
	if r.RowsAffected() != 1 {
		return "", training.ErrInvalid
	}
	if _, e = tx.Exec(ctx, `UPDATE jobs SET state='succeeded',last_error_code='superseded' WHERE dataset_id=$1 AND scope_key=$2 AND kind='training.plan' AND state='queued' AND target_generation<=$3`, l.DatasetID, l.ScopeKey, l.TargetGeneration); e != nil {
		return "", e
	}
	if e = tx.QueryRow(ctx, `SELECT clock_timestamp()<$1`, deadline).Scan(&valid); e != nil {
		return "", e
	}
	if !valid {
		return "", syncing.Fail(409, "lease_lost")
	}
	return status, tx.Commit(ctx)
}
func (s *Store) FailTraining(ctx context.Context, l Lease, id string, err error) error {
	code, retry := "training_storage_transient", true
	if errors.Is(err, training.ErrInsufficient) {
		code = "insufficient_context"
		retry = false
	}
	if errors.Is(err, training.ErrInvalid) {
		code = "invalid_training_input"
		retry = false
	}
	var se *syncing.Error
	if errors.As(err, &se) && (se.Code == "training_input_limit" || se.Code == "training_snapshot_limit") {
		code = se.Code
		retry = false
	}
	tx, e := s.begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = validAnalysisLease(ctx, tx, l); e != nil {
		return e
	}
	if id != "" {
		if _, e = tx.Exec(ctx, `UPDATE training_generation_runs SET status='FAILED',completed_at=now(),error_code=$3 WHERE dataset_id=$1 AND id=$2 AND status='RUNNING'`, l.DatasetID, id, code); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(ctx, `UPDATE jobs SET state=CASE WHEN $4 AND attempts<8 THEN 'queued' ELSE 'failed' END,lease_until=NULL,run_after=now()+interval '5 seconds',last_error_code=$5 WHERE dataset_id=$1 AND id=$2 AND fencing_token=$3`, l.DatasetID, l.ID, l.FencingToken, retry, code); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE job_attempts SET finished_at=now(),result_code=$3 WHERE job_id=$1 AND fencing_token=$2 AND finished_at IS NULL`, l.ID, l.FencingToken, code); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// Called once per published analysis bundle under its dataset lock.
func dirtyTrainingPlans(ctx context.Context, tx pgx.Tx, dataset string) error {
	_, e := tx.Exec(ctx, `SELECT rai_training_dirty(dataset_id,id) FROM training_plans WHERE dataset_id=$1 AND active`, dataset)
	return e
}

// TrainingVersions reports the exact first Effective snapshot published by this
// run, even if a subsequent user edit has already advanced the current pointer.
func (s *Store) TrainingVersions(ctx context.Context, l Lease, run string) (string, string, int64, error) {
	var base, effective string
	var revision int64
	e := s.Pool.QueryRow(ctx, `SELECT b.id::text,e.id::text,e.override_revision FROM training_base_versions b JOIN LATERAL
 (SELECT id,override_revision FROM training_effective_versions e WHERE e.dataset_id=b.dataset_id AND e.plan_id=b.plan_id AND e.base_id=b.id ORDER BY number LIMIT 1) e ON true
 WHERE b.dataset_id=$1 AND b.plan_id=$2 AND b.run_id=$3`, l.DatasetID, trainingPlanID(l), run).Scan(&base, &effective, &revision)
	if e != nil {
		return "", "", 0, e
	}
	os, e := loadOverrides(ctx, s.Pool, l.DatasetID, trainingPlanID(l), revision)
	if e != nil {
		return "", "", 0, e
	}
	var count int64
	for _, o := range training.ActiveOverrides(os) {
		if o.Status == "active" && o.Operation != "revoke" {
			count++
		}
	}
	return base, effective, count, nil
}
