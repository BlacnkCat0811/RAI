package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/syncing"
	"rai/server/internal/training"
)

// Only current, clean publications supply features; raw rows filter identity and
// running activity, but no raw measurements are read by the coach.
const TrainingWorkoutsSQL = `SELECT w.workout_uuid::text,p.published_run_id::text,p.generation,
 (w.event_start AT TIME ZONE $2)::date::text,
 max(r.value) FILTER(WHERE r.metric='duration' AND r.quality IN ('available','partial') AND r.algorithm_name='workout.features'),
 max(r.value) FILTER(WHERE r.metric='distance' AND r.quality IN ('available','partial') AND r.algorithm_name='workout.features'),
 max(r.value) FILTER(WHERE r.metric='workout_load' AND r.quality IN ('available','partial') AND r.algorithm_name='training_load.trimp'),
 jsonb_agg(jsonb_build_object('runID',p.published_run_id::text,'generation',p.generation,'day',(w.event_start AT TIME ZONE $2)::date::text,'metric',r.metric,'algorithm',r.algorithm_name,'algorithmVersion',r.algorithm_version,'quality',r.quality,'value',r.value) ORDER BY r.metric)
 FROM analysis_publications p JOIN analysis_runs a ON a.dataset_id=p.dataset_id AND a.id=p.published_run_id AND a.status='SUCCEEDED'
 JOIN analysis_results r ON r.dataset_id=p.dataset_id AND r.run_id=p.published_run_id AND r.algorithm_version='v1' AND r.metric IN ('duration','distance','workout_load')
 JOIN dirty_scopes d ON d.dataset_id=p.dataset_id AND d.scope_key=p.scope_key AND d.generation=d.clean_generation AND d.generation=p.generation
 JOIN workouts w ON w.dataset_id=p.dataset_id AND p.scope_key='workout:HKWorkoutTypeIdentifier:'||w.workout_uuid::text
 WHERE p.dataset_id=$1 AND p.metric_family='workout' AND w.activity_type=37
 AND (($3='' AND (w.event_start AT TIME ZONE $2)::date BETWEEN $4::date-27 AND $4::date) OR w.workout_uuid=NULLIF($3,'')::uuid)
 AND COALESCE((a.lineage->>'parameterVersion')::bigint,0)=COALESCE((SELECT max(version) FROM analysis_parameter_sets WHERE dataset_id=$1),0)
 GROUP BY w.workout_uuid,p.published_run_id,p.generation,w.event_start ORDER BY w.event_start,w.workout_uuid LIMIT 4097`

func loadTrainingWorkouts(ctx context.Context, q trainingDB, dataset, zone, day, id string) ([]training.Workout, error) {
	if day == "" {
		day = "2000-01-01"
	}
	rows, e := q.Query(ctx, TrainingWorkoutsSQL, dataset, zone, id, day)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []training.Workout{}
	for rows.Next() {
		var w training.Workout
		var mb []byte
		if e = rows.Scan(&w.ID, &w.RunID, &w.Generation, &w.Date, &w.DurationS, &w.DistanceM, &w.Load, &mb); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(mb, &w.Metrics); e != nil {
			return nil, e
		}
		out = append(out, w)
	}
	if len(out) > 4096 {
		return nil, syncing.Fail(422, "training_input_limit")
	}
	return out, rows.Err()
}
func (s *Store) LoadTraining(ctx context.Context, l Lease, at time.Time) (training.Input, error) {
	var out training.Input
	tx, e := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	p, e := loadPlan(ctx, tx, l.DatasetID, trainingPlanID(l))
	if e != nil {
		return out, e
	}
	out.Plan = p
	out.Generation = p.Generation
	loc, e := time.LoadLocation(p.Timezone)
	if e != nil {
		return out, training.ErrInvalid
	}
	c := training.Context{PlanID: p.ID, Timezone: p.Timezone, AsOf: at.In(loc).Format("2006-01-02"), ConfigRevision: p.ConfigRevision, Config: p.Config, Signals: []training.Signal{}, Feedback: []training.Feedback{}}
	var b []byte
	if e = tx.QueryRow(ctx, `SELECT parameters FROM training_parameter_sets WHERE version=$1`, training.ParameterVersion).Scan(&b); e != nil {
		return out, e
	}
	if e = json.Unmarshal(b, &c.Parameters); e != nil {
		return out, e
	}
	c.Workouts, e = loadTrainingWorkouts(ctx, tx, l.DatasetID, p.Timezone, c.AsOf, "")
	if e != nil {
		return out, e
	}
	out.Workouts = map[string]training.Workout{}
	for _, w := range c.Workouts {
		out.Workouts[w.ID] = w
	}
	rows, e := tx.Query(ctx, `SELECT p.published_run_id::text,p.generation,r.day::text,r.metric,r.algorithm_name,r.algorithm_version,r.quality,r.value
 FROM analysis_publications p JOIN analysis_runs a ON a.dataset_id=p.dataset_id AND a.id=p.published_run_id AND a.status='SUCCEEDED'
 JOIN analysis_results r ON r.dataset_id=p.dataset_id AND r.run_id=p.published_run_id AND r.day=p.day
 JOIN dirty_scopes d ON d.dataset_id=p.dataset_id AND d.scope_key=p.scope_key AND d.generation=d.clean_generation
 WHERE p.dataset_id=$1 AND p.metric_family='state' AND p.day=$2::date AND r.details->>'timezone'=$3 ORDER BY r.metric`, l.DatasetID, c.AsOf, p.Timezone)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var sig training.Signal
		if e = rows.Scan(&sig.RunID, &sig.Generation, &sig.Day, &sig.Name, &sig.Algorithm, &sig.Version, &sig.Quality, &sig.Value); e != nil {
			rows.Close()
			return out, e
		}
		c.Signals = append(c.Signals, sig)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	rows, e = tx.Query(ctx, `SELECT id::text FROM training_executions WHERE dataset_id=$1 AND plan_id=$2 AND status IN ('completed','skipped') ORDER BY id LIMIT 4097`, l.DatasetID, p.ID)
	if e != nil {
		return out, e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return out, e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	if len(ids) > 4096 {
		return out, syncing.Fail(422, "training_input_limit")
	}
	for _, id := range ids {
		ex, e := loadExecution(ctx, tx, l.DatasetID, id)
		if e != nil {
			return out, e
		}
		out.Executions = append(out.Executions, ex)
		var w *training.Workout
		if ex.WorkoutID != "" {
			x, ok := out.Workouts[ex.WorkoutID]
			if !ok {
				ws, e := loadTrainingWorkouts(ctx, tx, l.DatasetID, p.Timezone, c.AsOf, ex.WorkoutID)
				if e != nil {
					return out, e
				}
				if len(ws) > 0 {
					x = ws[0]
					out.Workouts[x.ID] = x
					ok = true
				}
			}
			if ok {
				w = &x
			}
		}
		f := training.Compare(ex, w)
		if ex.Status == "skipped" || w != nil {
			c.Feedback = append(c.Feedback, f)
		}
	}
	out.Context = c
	return out, tx.Commit(ctx)
}
