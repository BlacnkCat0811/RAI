package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"rai/server/internal/hrref"
	te "rai/server/internal/trainingeffect"
	tl "rai/server/internal/trainingload"
	vo2 "rai/server/internal/vo2max"
	wf "rai/server/internal/workoutfeatures"
)

// workoutCacheKey marks the day-release value carrying per-Workout cache
// updates. It is written to model_workout_results, never published.
const workoutCacheKey = "workout_caches"

// workoutCacheRow is one cached calculation: the input key and its JSON value
// (JSON null when the calculation produced no result).
type workoutCacheRow struct {
	id, key string
	value   []byte
}

type workoutCaches struct{ vo2, effect []workoutCacheRow }

// stackVO2Key covers every input of stackVO2Evidence: the Workout's facts (fact
// generation and content hash identify the raw snapshot; first receipt is its
// known-at), its features, the HR reference and the calendar.
func stackVO2Key(w stackWorkout, dose tl.Reference, ref hrref.Snapshot, loc *time.Location) string {
	estimated := ref.Evidence != nil && (ref.Evidence.MaxHR.Status == "estimated" || ref.Evidence.RestingHR.Status == "estimated")
	return featureHash(struct {
		Policy                    string
		Workout, FactHash         string
		FactGeneration, Known     int64
		Feature                   string
		Dose                      tl.Reference
		MaxMeasured, RestMeasured int64
		Estimated                 bool
		Zone                      string
	}{"vo2-evidence/1:" + vo2.Version, w.id, w.factHash, w.factGeneration, w.known.UnixMicro(), w.feature.Evidence.InputHash,
		dose, ref.HRMax.MeasuredAt, ref.RestingHR.MeasuredAt, estimated, loc.String()})
}

// cachedVO2Evidence decodes a cached evaluation; ok=false means unusable.
func cachedVO2Evidence(raw []byte) (*vo2.Evidence, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	if string(raw) == "null" {
		return nil, true
	}
	var v vo2.Evidence
	if json.Unmarshal(raw, &v) != nil || v.WorkoutID == "" {
		return nil, false
	}
	return &v, true
}

// stackEffectKey covers every input of te.Calculate. The effort points are
// identified by the facts they are derived from and the effort policy
// instead of being hashed point by point.
func stackEffectKey(w stackWorkout, in te.Input) string {
	in.Effort = nil
	var schema, policy string
	if w.feature.Effort != nil {
		schema, policy = w.feature.Effort.Schema, w.feature.Effort.Policy
	}
	return featureHash(struct {
		Policy                     string
		Input                      te.Input
		FactHash                   string
		FactGeneration             int64
		Feature                    string
		EffortSchema, EffortPolicy string
		HasEffort                  bool
	}{te.Policy, in, w.factHash, w.factGeneration, w.feature.Evidence.InputHash, schema, policy, w.feature.Effort != nil})
}

// loadEffortPoints restores the effort points of cached features that were
// read without them, in one query.
func loadEffortPoints(ctx context.Context, tx pgx.Tx, dataset, stack string, workouts []*stackWorkout) error {
	byID := map[string]*stackWorkout{}
	ids := make([]string, 0, len(workouts))
	for _, w := range workouts {
		if w.pointsOmitted && w.feature.Effort != nil {
			byID[w.id] = w
			ids = append(ids, w.id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT workout_uuid::text,fact_generation,feature->'effort'->'points' FROM model_workout_results
 WHERE dataset_id=$1 AND stack_id=$2 AND workout_uuid=ANY($3::uuid[])`, dataset, stack, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var gen int64
		var raw []byte
		if err := rows.Scan(&id, &gen, &raw); err != nil {
			return err
		}
		w := byID[id]
		if w == nil || gen != w.factGeneration {
			continue
		}
		effort := *w.feature.Effort
		effort.Points = nil
		if len(raw) > 0 && string(raw) != "null" {
			if err := json.Unmarshal(raw, &effort.Points); err != nil {
				return err
			}
		}
		w.feature.Effort = &effort
		w.pointsOmitted = false
	}
	return rows.Err()
}

// writeWorkoutCaches stores the cache updates of a day release.
func writeWorkoutCaches(ctx context.Context, tx pgx.Tx, dataset, stack string, c workoutCaches) error {
	for _, set := range []struct {
		rows []workoutCacheRow
		sql  string
	}{
		{c.vo2, `UPDATE model_workout_results r SET vo2_key=v.k,vo2_evidence=v.j FROM unnest($3::uuid[],$4::text[],$5::jsonb[]) v(id,k,j)
 WHERE r.dataset_id=$1 AND r.stack_id=$2 AND r.workout_uuid=v.id`},
		{c.effect, `UPDATE model_workout_results r SET effect_key=v.k,effect=v.j FROM unnest($3::uuid[],$4::text[],$5::jsonb[]) v(id,k,j)
 WHERE r.dataset_id=$1 AND r.stack_id=$2 AND r.workout_uuid=v.id`},
	} {
		if len(set.rows) == 0 {
			continue
		}
		ids, keys, values := make([]string, len(set.rows)), make([]string, len(set.rows)), make([]string, len(set.rows))
		for i, r := range set.rows {
			ids[i], keys[i], values[i] = r.id, r.key, string(r.value)
		}
		if _, err := tx.Exec(ctx, set.sql, dataset, stack, ids, keys, values); err != nil {
			return err
		}
	}
	return nil
}

// restoreArtifactEffort fills the effort points of a feature artifact built
// from a cached feature that was read without them.
func restoreArtifactEffort(ctx context.Context, tx pgx.Tx, dataset, stack string, f *wf.WorkoutFeatures) error {
	if f.Effort == nil || f.Effort.Points != nil {
		return nil
	}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT feature->'effort'->'points' FROM model_workout_results WHERE dataset_id=$1 AND stack_id=$2 AND workout_uuid=$3`,
		dataset, stack, f.WorkoutID).Scan(&raw)
	if err == pgx.ErrNoRows || len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if err != nil {
		return err
	}
	effort := *f.Effort
	if err := json.Unmarshal(raw, &effort.Points); err != nil {
		return err
	}
	f.Effort = &effort
	return nil
}
