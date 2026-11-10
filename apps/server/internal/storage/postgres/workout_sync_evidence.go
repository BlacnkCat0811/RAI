package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	rd "rai/server/internal/readiness"
)

// workoutSyncEvidence reports the positive upload evidence readiness consumes:
// how many Workout ingests this dataset committed and when the newest one
// finished. A committed ingest is the whole criterion — the server never tries
// to prove that a set of Workouts is complete, because it cannot distinguish
// "the client uploaded three of five" from "there were three".
func workoutSyncEvidence(ctx context.Context, tx pgx.Tx, dataset string) (rd.Sync, error) {
	var s rd.Sync
	var last pgtype.Timestamptz
	if e := tx.QueryRow(ctx, `SELECT count(*), max(finished_at) FROM workout_ingests WHERE dataset_id=$1 AND state='committed'`, dataset).Scan(&s.CommittedWorkouts, &last); e != nil {
		return s, e
	}
	if last.Valid {
		s.LastCommitAt = last.Time.UnixMicro()
	}
	return s, nil
}
