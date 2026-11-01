package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/analysis"
	"rai/server/internal/syncing"
)

// StartAnalysisRun stores a small lineage, not a copy of the health database.
func (s *Store) StartAnalysisRun(ctx context.Context, l Lease, in analysis.Snapshot) (string, error) {
	name, err := analysis.Algorithm(l.Kind)
	if err != nil {
		return "", err
	}
	hash, err := in.Hash()
	if err != nil {
		return "", analysis.Invalid()
	}
	lineage := map[string]any{"snapshotGeneration": in.Generation, "revision": in.Revision, "inputVersion": in.InputVersion}
	if in.Workout != nil {
		lineage["workoutID"] = in.Workout.ID
		lineage["deleted"] = in.Workout.Deleted
		lineage["facts"] = in.Workout.Lineage
		lineage["sampleCount"] = len(in.Workout.Observations)
		lineage["routePointCount"] = len(in.Workout.Route)
		if in.Workout.Parameters != nil {
			lineage["parameterVersion"] = in.Workout.Parameters.Version
		}
	}
	if in.State != nil {
		lineage["timezone"] = in.State.Timezone
		lineage["from"] = in.State.From
		lineage["through"] = in.State.Through
		lineage["upstreamRuns"] = in.State.Loads
	}
	b, err := json.Marshal(lineage)
	if err != nil {
		return "", analysis.Invalid()
	}
	id := syncing.NewID()
	r, err := s.Pool.Exec(ctx, `INSERT INTO analysis_runs(dataset_id,id,job_id,fencing_token,scope_key,target_generation,algorithm_name,algorithm_version,input_version,input_hash,input_from,input_through,lineage,status)
 SELECT $1,$2,$3,$4,$5,$6,$7,$13,$8,$9,$10,$11,$12,'RUNNING'
 WHERE EXISTS(SELECT 1 FROM jobs WHERE id=$3 AND dataset_id=$1 AND state='running' AND fencing_token=$4 AND lease_until>clock_timestamp())`, l.DatasetID, id, l.ID, l.FencingToken, l.ScopeKey, l.TargetGeneration, name, in.InputVersion, hash, in.From, in.Through, b, analysis.BundleVersion(l.Kind))
	if err != nil {
		return "", err
	}
	if r.RowsAffected() != 1 {
		return "", syncing.Fail(409, "lease_lost")
	}
	return id, nil
}
func validAnalysisLease(ctx context.Context, tx pgx.Tx, l Lease) error {
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT state='running' AND fencing_token=$3 AND lease_until>clock_timestamp() FROM jobs WHERE dataset_id=$1 AND id=$2 FOR UPDATE`, l.DatasetID, l.ID, l.FencingToken).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return syncing.Fail(409, "lease_lost")
	}
	return nil
}

// PublishAnalysis owns the only result/publication/clean/complete transaction.
func (s *Store) PublishAnalysis(ctx context.Context, l Lease, id string, in analysis.Snapshot, metrics []analysis.Metric) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := analysis.ValidateMetrics(metrics); err != nil {
		return "", err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL transaction_timeout='10s'`); err != nil {
		return "", err
	}

	var rev int64
	if err = tx.QueryRow(ctx, `SELECT commit_rev FROM datasets WHERE id=$1 FOR UPDATE`, l.DatasetID).Scan(&rev); err != nil {
		return "", err
	}
	if err = validAnalysisLease(ctx, tx, l); err != nil {
		return "", err
	}
	var leaseDeadline time.Time
	if err = tx.QueryRow(ctx, `SELECT lease_until FROM jobs WHERE id=$1`, l.ID).Scan(&leaseDeadline); err != nil {
		return "", err
	}
	var generation int64
	if err = tx.QueryRow(ctx, `SELECT generation FROM dirty_scopes WHERE dataset_id=$1 AND scope_key=$2 FOR UPDATE`, l.DatasetID, l.ScopeKey).Scan(&generation); err != nil {
		return "", err
	}
	status := "SUCCEEDED"
	if generation != l.TargetGeneration || in.Generation != l.TargetGeneration {
		status = "SUPERSEDED"
	}
	if status == "SUCCEEDED" {
		batch := &pgx.Batch{}
		for _, m := range metrics {
			b, e := json.Marshal(m.Details)
			if e != nil {
				return "", analysis.Invalid()
			}
			batch.Queue(`INSERT INTO analysis_results(dataset_id,run_id,day,metric,algorithm_name,algorithm_version,value,unit,quality,details) VALUES($1,$2,$3::date,$4,$5,$6,$7,$8,$9,$10)`, l.DatasetID, id, m.Day, m.Name, m.Algorithm, m.Version, m.Value, m.Unit, m.Quality, b)
		}
		if len(metrics) > 0 {
			r := tx.SendBatch(ctx, batch)
			for range metrics {
				if _, e := r.Exec(); e != nil {
					r.Close()
					return "", e
				}
			}
			if e := r.Close(); e != nil {
				return "", e
			}
		}
		// Deletions publish no workout pointer, but still propagate their former day.
		family := "workout"
		if l.Kind == "analysis.training_state" {
			family = "state"
		}
		if in.Workout != nil && in.Workout.Deleted {
			if _, err = tx.Exec(ctx, `DELETE FROM analysis_publications WHERE dataset_id=$1 AND scope_key=$2`, l.DatasetID, l.ScopeKey); err != nil {
				return "", err
			}
		} else {
			days := map[string]bool{}
			for _, m := range metrics {
				days[m.Day] = true
			}
			for day := range days {
				if _, err = tx.Exec(ctx, `INSERT INTO analysis_publications(dataset_id,scope_key,metric_family,day,published_run_id,generation)
    VALUES($1,$2,$3,$4::date,$5,$6) ON CONFLICT(dataset_id,scope_key,metric_family,day) DO UPDATE SET published_run_id=EXCLUDED.published_run_id,generation=EXCLUDED.generation,published_at=now()
    WHERE analysis_publications.generation<=EXCLUDED.generation`, l.DatasetID, l.ScopeKey, family, day, id, generation); err != nil {
					return "", err
				}
			}
		}
		if l.Kind == "analysis.training_state" {
			if _, err = tx.Exec(ctx, `UPDATE dirty_scopes SET dirty_through=GREATEST(dirty_through,clock_timestamp()) WHERE dataset_id=$1 AND scope_key=$2`, l.DatasetID, l.ScopeKey); err != nil {
				return "", err
			}
		}
		if l.Kind == "analysis.workout" {
			if _, err = tx.Exec(ctx, `SELECT rai_analysis_dirty($1,'athlete/default','analysis.training_state',$2,$3)`, l.DatasetID, in.From, in.Through); err != nil {
				return "", err
			}
		}
		if l.Kind == "analysis.sample" || l.Kind == "analysis.series" {
			for _, scope := range in.Dependencies {
				if _, err = tx.Exec(ctx, `SELECT rai_analysis_dirty($1,$2,'analysis.workout',w.event_start,w.event_end) FROM workouts w WHERE dataset_id=$1 AND 'workout:HKWorkoutTypeIdentifier:'||workout_uuid::text=$2`, l.DatasetID, scope); err != nil {
					return "", err
				}
			}
		}
		if l.Kind == "analysis.workout" || l.Kind == "analysis.training_state" {
			if err = dirtyTrainingPlans(ctx, tx, l.DatasetID); err != nil {
				return "", err
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE dirty_scopes SET clean_generation=$3 WHERE dataset_id=$1 AND scope_key=$2 AND generation=$3`, l.DatasetID, l.ScopeKey, generation); err != nil {
			return "", err
		}
	}
	r, err := tx.Exec(ctx, `UPDATE analysis_runs SET status=$5,finished_at=now() WHERE dataset_id=$1 AND id=$2 AND job_id=$3 AND fencing_token=$4 AND status='RUNNING'`, l.DatasetID, id, l.ID, l.FencingToken, status)
	if err != nil {
		return "", err
	}
	if r.RowsAffected() != 1 {
		return "", analysis.Invalid()
	}
	// Statement-end lease check uses wall time; a transaction must not publish on
	// an expired lease merely because PostgreSQL now() is fixed at BEGIN.
	r, err = tx.Exec(ctx, `UPDATE jobs SET state='succeeded',lease_until=NULL,last_error_code=$4 WHERE id=$1 AND dataset_id=$2 AND fencing_token=$3 AND state='running' AND lease_until>clock_timestamp()`, l.ID, l.DatasetID, l.FencingToken, map[bool]string{true: "superseded", false: "published"}[status == "SUPERSEDED"])
	if err != nil {
		return "", err
	}
	if r.RowsAffected() != 1 {
		return "", syncing.Fail(409, "lease_lost")
	}
	r, err = tx.Exec(ctx, `UPDATE job_attempts SET finished_at=now(),result_code=$3 WHERE job_id=$1 AND fencing_token=$2 AND finished_at IS NULL`, l.ID, l.FencingToken, status)
	if err != nil {
		return "", err
	}
	if r.RowsAffected() != 1 {
		return "", analysis.Invalid()
	}
	// Jobs already made obsolete never need an algorithm execution. Their IDs
	// (including IDs in historical durable receipts) remain queryable.
	if _, err = tx.Exec(ctx, `UPDATE jobs SET state='succeeded',last_error_code='superseded' WHERE dataset_id=$1 AND scope_key=$2 AND kind=$3 AND state='queued' AND target_generation<$4`, l.DatasetID, l.ScopeKey, l.Kind, generation); err != nil {
		return "", err
	}
	var leaseValid bool
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()<$1`, leaseDeadline).Scan(&leaseValid); err != nil {
		return "", err
	}
	if !leaseValid {
		return "", syncing.Fail(409, "lease_lost")
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return status, nil
}

func (s *Store) FailAnalysis(ctx context.Context, l Lease, id string, code string, retryable bool) error {
	allowed := map[string]bool{"analysis_invalid_input": true, "analysis_unsupported_algorithm": true, "analysis_input_limit": true, "analysis_dependency_pending": true, "analysis_storage_transient": true}
	if !allowed[code] {
		code = "analysis_invalid_input"
		retryable = false
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = validAnalysisLease(ctx, tx, l); err != nil {
		return err
	}
	if id != "" {
		if _, err = tx.Exec(ctx, `UPDATE analysis_runs SET status='FAILED',finished_at=now(),error_code=$5 WHERE dataset_id=$1 AND id=$2 AND job_id=$3 AND fencing_token=$4 AND status='RUNNING'`, l.DatasetID, id, l.ID, l.FencingToken, code); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE jobs SET state=CASE WHEN $4 AND attempts<8 THEN 'queued' ELSE 'failed' END,lease_until=NULL,run_after=now()+interval '5 seconds',last_error_code=$5 WHERE dataset_id=$1 AND id=$2 AND fencing_token=$3`, l.DatasetID, l.ID, l.FencingToken, retryable, code); err != nil {
		return err
	}
	r, err := tx.Exec(ctx, `UPDATE job_attempts SET finished_at=now(),result_code=$3 WHERE job_id=$1 AND fencing_token=$2 AND finished_at IS NULL`, l.ID, l.FencingToken, code)
	if err != nil {
		return err
	}
	if r.RowsAffected() != 1 {
		return errors.New("attempt missing")
	}
	return tx.Commit(ctx)
}

// AdvanceAnalysisDay only touches existing state scopes, using an absolute
// caller timestamp and explicit dataset calendar. No raw table/global digest.
func (s *Store) AdvanceAnalysisDay(ctx context.Context, at time.Time) error {
	rows, err := s.Pool.Query(ctx, `SELECT d.id::text FROM datasets d JOIN dirty_scopes x ON x.dataset_id=d.id AND x.scope_key='athlete/default'
 WHERE x.generation=x.clean_generation AND x.dirty_through < date_trunc('day',$1::timestamptz,d.timezone) LIMIT 20`, at)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		tx, e := s.begin(ctx)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `SELECT id FROM datasets WHERE id=$1 FOR UPDATE`, id)
		if e == nil {
			_, e = tx.Exec(ctx, `SELECT rai_analysis_dirty($1,'athlete/default','analysis.training_state',date_trunc('day',$2::timestamptz,d.timezone),$2) FROM datasets d JOIN dirty_scopes x ON x.dataset_id=d.id AND x.scope_key='athlete/default' WHERE d.id=$1 AND x.generation=x.clean_generation AND x.dirty_through<date_trunc('day',$2::timestamptz,d.timezone)`, id, at)
		}
		if e == nil {
			e = tx.Commit(ctx)
		} else {
			tx.Rollback(ctx)
		}
		if e != nil {
			return e
		}
	}
	return nil
}
