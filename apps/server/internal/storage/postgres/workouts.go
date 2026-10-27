package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/syncing"
)

// All children and the full validated source payload share the parent fact's
// transaction, receipt, tombstone, ledger and dirty generation boundary.
func insertWorkout(ctx context.Context, tx pgx.Tx, dataset string, event syncing.Event, rev int64, canonical, hash []byte) error {
	w := event.Workout
	_, err := tx.Exec(ctx, `INSERT INTO workouts(dataset_id,workout_uuid,event_start,event_end,activity_type,duration_us,source_bundle_id,content_sha256,entity_revision,payload)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, dataset, event.UUID, time.UnixMicro(*event.StartUs).UTC(), time.UnixMicro(*event.EndUs).UTC(), *w.ActivityType, *w.DurationUs, event.Source.BundleID, hash, rev, canonical)
	if err != nil {
		return err
	}
	for _, statistic := range w.Statistics {
		body, err := json.Marshal(statistic)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO workout_statistics(dataset_id,workout_uuid,sample_type,payload) VALUES($1,$2,$3,$4)`, dataset, event.UUID, statistic.Type, body); err != nil {
			return err
		}
	}
	for ordinal, workoutEvent := range w.Events {
		body, err := json.Marshal(workoutEvent)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO workout_events(dataset_id,workout_uuid,ordinal,payload) VALUES($1,$2,$3,$4)`, dataset, event.UUID, ordinal, body); err != nil {
			return err
		}
	}
	for ordinal, activity := range w.Activities {
		body, err := json.Marshal(activity)
		if err != nil {
			return err
		}
		var end *time.Time
		if activity.EndUs != nil {
			stamp := time.UnixMicro(*activity.EndUs).UTC()
			end = &stamp
		}
		if _, err = tx.Exec(ctx, `INSERT INTO workout_activities(dataset_id,workout_uuid,ordinal,activity_uuid,event_start,event_end,duration_us,payload)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, dataset, event.UUID, ordinal, activity.UUID, time.UnixMicro(*activity.StartUs).UTC(), end, *activity.DurationUs, body); err != nil {
			return err
		}
	}
	return nil
}
