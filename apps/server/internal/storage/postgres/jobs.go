package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/storage/postgres/dbgen"
	"rai/server/internal/syncing"
)

type Lease = dbgen.ClaimJobRow

func (s *Store) Claim(ctx context.Context, kinds []string) (Lease, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return Lease{}, err
	}
	defer tx.Rollback(ctx)
	r, err := dbgen.New(tx).ClaimJob(ctx, kinds)
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
	return r, nil
}
func (s *Store) Renew(ctx context.Context, l Lease) error {
	r, err := s.Pool.Exec(ctx, `UPDATE jobs SET lease_until=now()+interval '60 seconds'
		WHERE id=$1 AND dataset_id=$2 AND state='running' AND fencing_token=$3 AND lease_until>now()`, l.ID, l.DatasetID, l.FencingToken)
	if err != nil {
		return err
	}
	if r.RowsAffected() != 1 {
		return syncing.Fail(409, "lease_lost")
	}
	return nil
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
	var code string
	err = tx.QueryRow(ctx, `UPDATE jobs SET
		state=CASE WHEN $4 AND attempts < 8 THEN 'queued' ELSE 'failed' END,
		lease_until=NULL, run_after=now()+interval '5 seconds',
		last_error_code=CASE WHEN $4 THEN 'handler_retryable' ELSE 'handler_failed' END
		WHERE id=$1 AND dataset_id=$2 AND state='running' AND fencing_token=$3 AND lease_until>now()
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
	var rev int64
	if err = tx.QueryRow(ctx, `SELECT commit_rev FROM datasets WHERE id=$1 FOR UPDATE`, l.DatasetID).Scan(&rev); err != nil {
		return err
	}
	var scope string
	var generation int64
	err = tx.QueryRow(ctx, `UPDATE jobs SET state='succeeded',lease_until=NULL WHERE id=$1 AND dataset_id=$2
		AND fencing_token=$3 AND state='running' AND lease_until>now() RETURNING scope_key,target_generation`, l.ID, l.DatasetID, l.FencingToken).Scan(&scope, &generation)
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
