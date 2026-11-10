package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// The v3 workout package write path was removed, but applyEvent still uses
// these two helpers for the legacy workout event shape: an explicit workout
// event must not overwrite a sealed package, and a workout delete must remove
// the package row.
//
// The package tables arrive in schema 9. Normal sample uploads remain usable
// during an explicit upgrade window, so the older Workout path must not assume
// the table exists until readyz has approved the new schema.
func workoutPackageExists(ctx context.Context, tx pgx.Tx, dataset, id string) (bool, error) {
	var table pgtype.Text
	if err := tx.QueryRow(ctx, `SELECT to_regclass('workout_packages')::text`).Scan(&table); err != nil {
		return false, err
	}
	if !table.Valid {
		return false, nil
	}
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workout_packages WHERE dataset_id=$1 AND workout_uuid=$2)`, dataset, id).Scan(&exists)
	return exists, err
}

func deleteWorkoutPackage(ctx context.Context, tx pgx.Tx, dataset, id string) error {
	var table pgtype.Text
	if err := tx.QueryRow(ctx, `SELECT to_regclass('workout_packages')::text`).Scan(&table); err != nil {
		return err
	}
	if !table.Valid {
		return nil
	}
	_, err := tx.Exec(ctx, `DELETE FROM workout_packages WHERE dataset_id=$1 AND workout_uuid=$2`, dataset, id)
	return err
}
