package postgres

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	rd "rai/server/internal/readiness"
)

func verifiedWorkoutCompleteness(ctx context.Context, tx pgx.Tx, dataset, epoch, collector string) (rd.Coverage, bool, error) {
	var c rd.Coverage
	var factRevision int64
	err := tx.QueryRow(ctx, `SELECT encode(request_sha256,'hex'),window_from_us,known_cutoff_us,fact_revision,encode(sha256(convert_to(head_state::text,'UTF8')),'hex') FROM workout_sync_completeness WHERE dataset_id=$1 AND dataset_epoch::text=$2 AND collector_epoch::text=$3 AND stream_set_version=1 ORDER BY generation DESC LIMIT 1`, dataset, epoch, collector).Scan(&c.Identity, &c.From, &c.Through, &factRevision, &c.StreamHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, false, nil
	}
	if err != nil {
		return c, false, err
	}
	current, err := workoutCompletenessFactRevision(ctx, tx, dataset)
	if err != nil {
		return c, false, err
	}
	c.CaughtUp = factRevision == current
	c.Synthetic = false
	return c, true, nil
}
