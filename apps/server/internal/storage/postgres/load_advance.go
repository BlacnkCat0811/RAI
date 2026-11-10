package postgres

import (
	"context"
	tl "rai/server/internal/trainingload"
	"time"
)

func (s *Store) advanceCandidateLoadDay(ctx context.Context, at time.Time) error {
	rows, err := s.Pool.Query(ctx, `SELECT d.id::text FROM datasets d JOIN dirty_scopes x ON x.dataset_id=d.id AND x.scope_key=$2
 WHERE d.retired_at IS NULL AND x.generation=x.clean_generation AND EXISTS(SELECT 1 FROM analysis_model_dependencies m WHERE m.dataset_id=d.id AND m.downstream_scope LIKE 'model/%/workout_load/%')
 AND (SELECT max(p.day) FROM analysis_publications p WHERE p.dataset_id=d.id AND p.scope_key=$2)<( $1::timestamptz AT TIME ZONE d.timezone)::date LIMIT 20`, at, tl.StateScope())
	if err != nil {
		return err
	}
	ids := []string{}
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
		_, e = lockActiveDataset(ctx, tx, id)
		// Bespoke: the dirty range starts at the next unpublished load day from
		// analysis_publications, so it cannot use one caller-supplied range.
		if e == nil {
			_, e = tx.Exec(ctx, `SELECT rai_analysis_dirty(d.id,$2,$3,((max(p.day)+1)::timestamp AT TIME ZONE d.timezone),$4) FROM datasets d JOIN dirty_scopes x ON x.dataset_id=d.id AND x.scope_key=$2 AND x.generation=x.clean_generation JOIN analysis_publications p ON p.dataset_id=d.id AND p.scope_key=$2 WHERE d.id=$1 GROUP BY d.id HAVING max(p.day)<($4::timestamptz AT TIME ZONE d.timezone)::date`, id, tl.StateScope(), tl.StateJobKind, at)
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
