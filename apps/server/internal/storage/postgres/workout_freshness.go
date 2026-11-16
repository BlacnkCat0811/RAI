package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"rai/server/internal/syncing"
	"rai/server/internal/workoutdetail"
)

// loadWorkoutFreshnessData fills exactly the fields buildFreshness reads, with
// SQL aggregates instead of decoding every sample, series point and route
// point. The detail page polls freshness while it is open, so this read must
// stay independent of the Workout's size. The result equals the full loader's
// freshness (integration test TestWorkoutFreshnessMatchesFullLoader).
func (s *Store) loadWorkoutFreshnessData(ctx context.Context, p syncing.Principal, id string) (*workoutDetailData, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	d := &workoutDetailData{ID: id, SampleCounts: map[string]int{}, SeriesCounts: map[string]int{}, LastObservedByType: map[string]time.Time{}}
	err = tx.QueryRow(ctx, `SELECT event_start,event_end,first_received_at FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2`, p.DatasetID, id).
		Scan(&d.Start, &d.End, &d.ReceivedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, syncing.Fail(404, "workout_not_found")
	}
	if err != nil {
		return nil, err
	}
	if err = tx.QueryRow(ctx, `SELECT count(DISTINCT ec.batch_id),COALESCE(max(b.committed_at),'epoch'::timestamptz)
		FROM entity_changes ec JOIN sync_batches b ON b.dataset_id=ec.dataset_id AND b.batch_id=ec.batch_id
		WHERE ec.dataset_id=$1 AND ec.sample_type=$2 AND ec.sample_uuid=$3`, p.DatasetID, syncing.WorkoutType, id).Scan(&d.Batches, &d.LastBatchAt); err != nil {
		return nil, err
	}
	// A statistic counts when it carries an average, sum or maximum.
	rows, err := tx.Query(ctx, `SELECT sample_type FROM workout_statistics WHERE dataset_id=$1 AND workout_uuid=$2
		AND (payload->'average' IS NOT NULL AND payload->'average'<>'null'::jsonb
		  OR payload->'sum' IS NOT NULL AND payload->'sum'<>'null'::jsonb
		  OR payload->'maximum' IS NOT NULL AND payload->'maximum'<>'null'::jsonb)`, p.DatasetID, id)
	if err != nil {
		return nil, err
	}
	kinds, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	for _, kind := range kinds {
		d.Statistics = append(d.Statistics, syncing.WorkoutStatistic{Type: kind, Sum: &syncing.Quantity{Value: 1}})
	}
	type agg struct {
		kind  string
		count int
		last  *time.Time
	}
	collect := func(sql string, counts map[string]int) error {
		rows, err := tx.Query(ctx, sql, p.DatasetID, id)
		if err != nil {
			return err
		}
		xs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (agg, error) {
			var a agg
			return a, r.Scan(&a.kind, &a.count, &a.last)
		})
		if err != nil {
			return err
		}
		for _, a := range xs {
			counts[a.kind] += a.count
			if a.last != nil && a.last.UTC().After(d.LastObservedByType[a.kind]) {
				d.LastObservedByType[a.kind] = a.last.UTC()
			}
		}
		return nil
	}
	if err = collect(`SELECT h.sample_type,count(*)::int,max(h.event_start)
		FROM workout_samples a JOIN health_samples h USING(dataset_id,sample_type,sample_uuid)
		LEFT JOIN series_uploads u USING(dataset_id,sample_type,sample_uuid)
		WHERE a.dataset_id=$1 AND a.workout_uuid=$2 AND h.value_numeric IS NOT NULL
		  AND (u.series_id IS NULL OR u.state<>'sealed')
		GROUP BY 1`, d.SampleCounts); err != nil {
		return nil, err
	}
	// Series: point counts are stored per chunk; the newest observation is the
	// later of the latest point start and the seal time, as in the full loader.
	if err = collect(`SELECT u.sample_type,sum(c.point_count)::int,
		GREATEST(max(u.sealed_at),max('epoch'::timestamptz+(SELECT max((e->>'startUs')::bigint) FROM jsonb_array_elements(c.points) e)*interval '1 microsecond'))
		FROM workout_samples a
		JOIN series_uploads u USING(dataset_id,sample_type,sample_uuid)
		JOIN series_chunks c USING(dataset_id,series_id)
		WHERE a.dataset_id=$1 AND a.workout_uuid=$2 AND u.state='sealed'
		GROUP BY 1`, d.SeriesCounts); err != nil {
		return nil, err
	}
	var elevation int
	var routeSealed *time.Time
	if err = tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE p.altitude IS NOT NULL AND p.recorded_at BETWEEN w.event_start AND w.event_end)::int,max(r.sealed_at)
		FROM workout_routes r JOIN route_points p ON p.dataset_id=r.dataset_id AND p.route_uuid=r.route_uuid
		JOIN workouts w ON w.dataset_id=r.dataset_id AND w.workout_uuid=r.workout_uuid
		WHERE r.dataset_id=$1 AND r.workout_uuid=$2 AND r.state='sealed'`, p.DatasetID, id).Scan(&elevation, &routeSealed); err != nil {
		return nil, err
	}
	alt := 0.0
	for i := 0; i < elevation; i++ {
		d.Route = append(d.Route, workoutdetail.RouteSample{AltitudeM: &alt})
	}
	var seriesSealed, enriched *time.Time
	if err = tx.QueryRow(ctx, `SELECT (SELECT max(u.sealed_at) FROM workout_samples a JOIN series_uploads u USING(dataset_id,sample_type,sample_uuid)
		 WHERE a.dataset_id=$1 AND a.workout_uuid=$2 AND u.state='sealed'
		 AND EXISTS(SELECT 1 FROM series_chunks c WHERE c.dataset_id=u.dataset_id AND c.series_id=u.series_id)),
		(SELECT 'epoch'::timestamptz+max(observed_at_us)*interval '1 microsecond' FROM workout_enrichments WHERE dataset_id=$1 AND workout_uuid=$2)`, p.DatasetID, id).Scan(&seriesSealed, &enriched); err != nil {
		return nil, err
	}
	d.LastFactAt = d.ReceivedAt
	for _, t := range []*time.Time{routeSealed, seriesSealed, enriched} {
		if t != nil {
			d.LastFactAt = maxTime(d.LastFactAt, t.UTC())
		}
	}
	if err = loadPublishedWorkoutLoad(ctx, tx, p, id, d); err != nil {
		return nil, err
	}
	return d, tx.Commit(ctx)
}

// ReferenceWorkoutFreshness builds freshness from the full detail snapshot.
// It is the slow reference the lightweight read is verified against.
func (s *Store) ReferenceWorkoutFreshness(ctx context.Context, p syncing.Principal, id string) (workoutdetail.Freshness, error) {
	d, err := s.loadWorkoutDetailData(ctx, p, id, false)
	if err != nil {
		return workoutdetail.Freshness{}, err
	}
	return d.buildFreshness(), nil
}
