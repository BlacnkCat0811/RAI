package postgres

import (
	"context"
	"errors"
	"rai/server/internal/analysis"
	"rai/server/internal/modelmanifest"

	"rai/server/internal/storage/postgres/dbgen"
	"rai/server/internal/syncing"

	"github.com/jackc/pgx/v5"
)

type Lease = analysis.Lease

func (s *Store) Claim(ctx context.Context, kinds []string) (Lease, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return Lease{}, err
	}
	defer tx.Rollback(ctx)
	r, err := dbgen.New(tx).ClaimJob(ctx, dbgen.ClaimJobParams{
		Kinds: kinds, ModelDayPrefix: "model/" + modelmanifest.StackID() + "/athlete/day/v2/",
	})
	if err != nil {
		return Lease{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO job_attempts(job_id,fencing_token) VALUES($1,$2)`, r.ID, r.FencingToken)
	if err != nil {
		return Lease{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Lease{}, err
	}
	return Lease{ID: r.ID, DatasetID: r.DatasetID, Kind: r.Kind, ScopeKey: r.ScopeKey, TargetGeneration: r.TargetGeneration, FencingToken: r.FencingToken, Payload: r.Payload}, nil
}
func (s *Store) Renew(ctx context.Context, l Lease) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = lockActiveDataset(ctx, tx, l.DatasetID); err != nil {
		return err
	}
	r, err := tx.Exec(ctx, `UPDATE jobs SET lease_until=now()+interval '60 seconds' WHERE id=$1 AND dataset_id=$2 AND state='running' AND fencing_token=$3 AND lease_until>clock_timestamp()`, l.ID, l.DatasetID, l.FencingToken)
	if err != nil {
		return err
	}
	if r.RowsAffected() != 1 {
		return syncing.Fail(409, "lease_lost")
	}
	return tx.Commit(ctx)
}

// Fail records a bounded retry without accepting arbitrary error text, which
// could contain health data or credentials. An expired or superseded lease
// cannot alter the current attempt.
func (s *Store) Fail(ctx context.Context, l Lease, retryable bool) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = lockActiveDataset(ctx, tx, l.DatasetID); err != nil {
		return err
	}
	var code string
	err = tx.QueryRow(ctx, `UPDATE jobs SET
		state=CASE WHEN $4 AND attempts < 8 THEN 'queued' ELSE 'failed' END,
		lease_until=NULL, run_after=now()+interval '5 seconds',
		last_error_code=CASE WHEN $4 THEN 'handler_retryable' ELSE 'handler_failed' END
		WHERE id=$1 AND dataset_id=$2 AND state='running' AND fencing_token=$3 AND lease_until>clock_timestamp()
		RETURNING last_error_code`, l.ID, l.DatasetID, l.FencingToken, retryable).Scan(&code)
	if errors.Is(err, pgx.ErrNoRows) {
		return syncing.Fail(409, "lease_lost")
	}
	if err != nil {
		return err
	}
	r, err := tx.Exec(ctx, `UPDATE job_attempts SET finished_at=now(),result_code=$3
		WHERE job_id=$1 AND fencing_token=$2 AND finished_at IS NULL`, l.ID, l.FencingToken, code)
	if err != nil {
		return err
	}
	if r.RowsAffected() != 1 {
		return errors.New("job attempt missing or already finished")
	}
	return tx.Commit(ctx)
}

// Complete is queue bookkeeping only. A future result publisher must also CAS
// its declared input generations and write the release/changefeed in this transaction.
func (s *Store) Complete(ctx context.Context, l Lease) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = lockActiveDataset(ctx, tx, l.DatasetID); err != nil {
		return err
	}
	var scope string
	var generation int64
	err = tx.QueryRow(ctx, `UPDATE jobs SET state='succeeded',lease_until=NULL WHERE id=$1 AND dataset_id=$2
		AND fencing_token=$3 AND state='running' AND lease_until>clock_timestamp() RETURNING scope_key,target_generation`, l.ID, l.DatasetID, l.FencingToken).Scan(&scope, &generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return syncing.Fail(409, "lease_lost")
	}
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE dirty_scopes SET clean_generation=$3 WHERE dataset_id=$1 AND scope_key=$2 AND generation=$3`, l.DatasetID, scope, generation)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE job_attempts SET finished_at=now(),result_code='succeeded' WHERE job_id=$1 AND fencing_token=$2`, l.ID, l.FencingToken)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Store) RequeueExpired(ctx context.Context) (int, error) {
	if err := s.retireUnassignedModelJobs(ctx); err != nil {
		return 0, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := dbgen.New(tx).RequeueExpired(ctx)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		_, err = tx.Exec(ctx, `UPDATE training_generation_runs SET status='FAILED',completed_at=now(),error_code='lease_expired' WHERE job_id=$1 AND fencing_token=$2 AND status='RUNNING'`, r.ID, r.FencingToken)
		if err != nil {
			return 0, err
		}
		_, err = tx.Exec(ctx, `UPDATE analysis_runs SET status='FAILED',finished_at=now(),error_code='lease_expired' WHERE job_id=$1 AND fencing_token=$2 AND status='RUNNING'`, r.ID, r.FencingToken)
		if err != nil {
			return 0, err
		}
		_, err = tx.Exec(ctx, `UPDATE job_attempts SET finished_at=now(),result_code='lease_expired' WHERE job_id=$1 AND fencing_token=$2`, r.ID, r.FencingToken)
		if err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(rows), nil
}

// Separate transactions keep retirement and expiry from locking two datasets
// in opposite orders when several workers recover their queues concurrently.
func (s *Store) retireUnassignedModelJobs(ctx context.Context) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Retire only queued jobs whose stack no longer has a live assignment.
	// A different active/candidate stack may still have a compatible worker
	// during a rolling upgrade. Keep its work and all terminal audit history.
	if _, err := tx.Exec(ctx, `WITH dataset AS MATERIALIZED (
 SELECT d.id FROM datasets d WHERE d.retired_at IS NULL AND EXISTS (
  SELECT 1 FROM jobs j WHERE j.dataset_id=d.id AND j.kind='analysis.model_stack_v2' AND j.state='queued'
   AND NOT EXISTS(SELECT 1 FROM analysis_model_channels c WHERE c.dataset_id=j.dataset_id
    AND c.channel IN ('active','candidate') AND left(j.scope_key,length('model/'||c.stack_id||'/athlete/day/v2/'))='model/'||c.stack_id||'/athlete/day/v2/'))
 ORDER BY d.id FOR UPDATE OF d SKIP LOCKED LIMIT 1
 ), retired AS (
 SELECT j.id FROM jobs j WHERE j.dataset_id IN (SELECT id FROM dataset) AND j.kind='analysis.model_stack_v2' AND j.state='queued'
  AND NOT EXISTS(SELECT 1 FROM analysis_model_channels c WHERE c.dataset_id=j.dataset_id
   AND c.channel IN ('active','candidate') AND left(j.scope_key,length('model/'||c.stack_id||'/athlete/day/v2/'))='model/'||c.stack_id||'/athlete/day/v2/')
 ORDER BY j.id FOR UPDATE OF j SKIP LOCKED LIMIT 100
 ) UPDATE jobs j SET state='failed',lease_until=NULL,last_error_code='retired_model_stack' FROM retired r WHERE j.id=r.id`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
