package postgres

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"rai/server/internal/syncing"
	"rai/server/internal/workoutdetail"
)

// MaxDistributionWindow bounds one statistics request (a calendar month plus
// slack); MaxDistributionWorkouts bounds its work.
const (
	MaxDistributionWindow   = 45 * 24 * time.Hour
	MaxDistributionWorkouts = 200
)

type WorkoutDistanceItem = workoutdetail.WorkoutDistanceItem

// distributionCache keeps computed distributions in process memory. The key
// holds every input of the calculation (fact generation and hash, enrichment
// state, the HR reference values and the policy), so an entry cannot go stale;
// GET requests never write to the database.
var distributionCache = struct {
	sync.Mutex
	m map[string]WorkoutDistanceItem
}{m: map[string]WorkoutDistanceItem{}}

const distributionCacheMax = 4096

// WorkoutDistanceDistributions returns the distance distribution of every
// running Workout that starts in [from, to), computed from the same facts and
// HR reference as the detail page, in one read-only snapshot.
func (s *Store) WorkoutDistanceDistributions(ctx context.Context, p syncing.Principal, from, to time.Time) ([]WorkoutDistanceItem, error) {
	if !to.After(from) || to.Sub(from) > MaxDistributionWindow {
		return nil, syncing.Fail(422, "invalid_statistics_window")
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT w.workout_uuid::text,w.event_start,encode(w.content_sha256,'hex'),COALESCE(d.generation,0),
 (SELECT count(*)::text||':'||COALESCE(max(e.observed_at_us),0)::text FROM workout_enrichments e WHERE e.dataset_id=w.dataset_id AND e.workout_uuid=w.workout_uuid)
 FROM workouts w LEFT JOIN dirty_scopes d ON d.dataset_id=w.dataset_id AND d.scope_key='workout:HKWorkoutTypeIdentifier:'||w.workout_uuid::text
 WHERE w.dataset_id=$1 AND w.activity_type=37 AND w.event_start>=$2 AND w.event_start<$3
 ORDER BY w.event_start,w.workout_uuid LIMIT $4`, p.DatasetID, from, to, MaxDistributionWorkouts+1)
	if err != nil {
		return nil, err
	}
	type target struct {
		id, hash, enriched string
		start              time.Time
		generation         int64
	}
	targets, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (target, error) {
		var x target
		return x, r.Scan(&x.id, &x.start, &x.hash, &x.generation, &x.enriched)
	})
	if err != nil {
		return nil, err
	}
	if len(targets) > MaxDistributionWorkouts {
		return nil, syncing.Fail(422, "statistics_window_too_large")
	}
	out := make([]WorkoutDistanceItem, 0, len(targets))
	for _, x := range targets {
		// The same HR reference precedence as the detail page: the published
		// load's reference evidence, overridden by an explicit HR reference
		// with a resting value effective at the Workout start.
		refs := &workoutDetailData{ID: x.id}
		if err := loadModelQualification(ctx, tx, p, x.id, refs); err != nil {
			return nil, err
		}
		rest, maxHR := refs.HistoricalRestHR, refs.HistoricalMaxHR
		ref, err := hrReferenceAt(ctx, tx, p.DatasetID, x.start)
		if err != nil {
			return nil, err
		}
		if ref != nil && ref.RestingHR != nil {
			rest, maxHR = *ref.RestingHR, ref.MaxHR
		}
		key := p.DatasetID + "|" + x.id + "|" + x.hash + "|" + strconv.FormatInt(x.generation, 10) + "|" + x.enriched + "|" +
			strconv.FormatFloat(rest, 'g', -1, 64) + "|" + strconv.FormatFloat(maxHR, 'g', -1, 64) + "|distance-zones/v1"
		distributionCache.Lock()
		item, ok := distributionCache.m[key]
		distributionCache.Unlock()
		if !ok {
			item = WorkoutDistanceItem{WorkoutID: x.id, Status: "failed"}
			d, err := loadWorkoutDetailTx(ctx, tx, p, x.id, false)
			var se *syncing.Error
			switch {
			case err == nil:
				d.HistoricalRestHR, d.HistoricalMaxHR = rest, maxHR
				_, events := d.measured()
				item.Distribution = workoutdetail.BuildDistanceDistribution(d.Start.UnixMicro(), d.End.UnixMicro(), d.distanceMeters(), d.Observations, events, rest, maxHR)
				item.Status = "unavailable"
				if item.Distribution != nil {
					item.Status = "available"
				}
			case errors.As(err, &se) && se.Status < 500:
				// This Workout's own facts cannot be read (too large, invalid).
			default:
				return nil, err
			}
			distributionCache.Lock()
			if len(distributionCache.m) >= distributionCacheMax {
				distributionCache.m = map[string]WorkoutDistanceItem{}
			}
			distributionCache.m[key] = item
			distributionCache.Unlock()
		}
		out = append(out, item)
	}
	return out, tx.Commit(ctx)
}
