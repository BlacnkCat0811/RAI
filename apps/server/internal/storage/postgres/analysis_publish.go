package postgres

import (
	"context"
	"errors"
	"time"

	"rai/server/internal/modelmanifest"
	"rai/server/internal/syncing"

	"github.com/jackc/pgx/v5"
)

func validAnalysisLease(ctx context.Context, tx pgx.Tx, l Lease) error {
	if _, err := lockActiveDataset(ctx, tx, l.DatasetID); err != nil {
		return err
	}
	var ok bool
	if err := tx.QueryRow(ctx, `SELECT state='running' AND fencing_token=$3 AND lease_until>clock_timestamp() FROM jobs WHERE dataset_id=$1 AND id=$2 FOR UPDATE`, l.DatasetID, l.ID, l.FencingToken).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return syncing.Fail(409, "lease_lost")
	}
	return nil
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
	if l.Kind == modelmanifest.StackJobKind {
		backfillID := modelStackBackfillID(l)
		if backfillID != "" {
			if _, err := tx.Exec(ctx, `SELECT rai_backfill_progress($1,$2,false)`, l.DatasetID, backfillID); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

// AdvanceAnalysisDay only touches existing state scopes, using an absolute
// caller timestamp and explicit dataset calendar. No raw table/global digest.
func (s *Store) AdvanceAnalysisDay(ctx context.Context, at time.Time) error {
	// Only the v2 model stack advances by day; the legacy training_state and
	// candidate day advances were retired with schema 53.
	return s.advanceModelStackDay(ctx, at)
}
