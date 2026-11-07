package postgres

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	rd "rai/server/internal/readiness"
	"rai/server/internal/syncing"
)

func verifiedWorkoutCompleteness(ctx context.Context, tx pgx.Tx, dataset, epoch, collector string) (rd.Coverage, bool, error) {
	return verifiedWorkoutCompletenessProtocol(ctx, tx, dataset, epoch, collector, true)
}

func verifiedWorkoutCompletenessV2(ctx context.Context, tx pgx.Tx, dataset, epoch, collector string) (rd.Coverage, bool, error) {
	return verifiedWorkoutCompletenessProtocol(ctx, tx, dataset, epoch, collector, false)
}

func verifiedWorkoutCompletenessProtocol(ctx context.Context, tx pgx.Tx, dataset, epoch, collector string, legacy bool) (rd.Coverage, bool, error) {
	var c rd.Coverage
	var factRevision int64
	err := tx.QueryRow(ctx, `SELECT encode(request_sha256,'hex'),window_from_us,known_cutoff_us,fact_revision,encode(sha256(request_bytes),'hex') FROM workout_sync_completeness WHERE dataset_id=$1 AND dataset_epoch::text=$2 AND collector_epoch::text=$3 AND stream_set_version=1 AND ((protocol_version=$4 AND source_policy_version=$5 AND source_policy_sha256=$6) OR ($7 AND protocol_version=1)) ORDER BY (protocol_version=$4) DESC, generation DESC LIMIT 1`, dataset, epoch, collector, syncing.WorkoutCompletenessV2Version, syncing.WorkoutSourcePolicyVersion, mustPolicyHash(), legacy).Scan(&c.Identity, &c.From, &c.Through, &factRevision, &c.StreamHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, false, nil
	}
	if err != nil {
		return c, false, fmt.Errorf("coverage proof query: %w", err)
	}
	current, err := workoutCompletenessFactRevision(ctx, tx, dataset)
	if err != nil {
		return c, false, err
	}
	c.CaughtUp = factRevision == current
	c.Synthetic = false
	return c, true, nil
}

func mustPolicyHash() string {
	return syncing.WorkoutSourcePolicySHA256
}
