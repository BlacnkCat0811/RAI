package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/modelmanifest"
	"rai/server/internal/readiness"
	"rai/server/internal/syncing"
	"rai/server/internal/training"
	tl "rai/server/internal/trainingload"
)

// Only current, clean publications supply features; raw rows filter identity and
// running activity, but no raw measurements are read by the coach.
// The aggregate LATERAL lookup is parameterized by each selected Workout.
// Historical publication/dirty rows are reached by identity, not scanned first.
const trainingWorkoutsSelect = `SELECT w.workout_uuid::text,p.published_run_id::text,p.generation,
 (w.event_start AT TIME ZONE $2)::date::text,p.duration,p.distance,p.load,p.metrics
 FROM workouts w JOIN LATERAL (
 SELECT p.published_run_id,p.generation,
 max(r.value) FILTER(WHERE r.metric='duration' AND r.quality IN ('available','partial') AND r.algorithm_name='workout.features') duration,
 max(r.value) FILTER(WHERE r.metric='distance' AND r.quality IN ('available','partial') AND r.algorithm_name='workout.features') distance,
 max(r.value) FILTER(WHERE r.metric='workout_load' AND r.quality IN ('available','partial') AND r.algorithm_name='training_load.trimp') load,
 jsonb_agg(jsonb_build_object('runID',p.published_run_id::text,'generation',p.generation,'day',(w.event_start AT TIME ZONE $2)::date::text,'metric',r.metric,'algorithm',r.algorithm_name,'algorithmVersion',r.algorithm_version,'quality',r.quality,'value',r.value) ORDER BY r.metric) metrics
 FROM analysis_publications p
 JOIN analysis_runs a ON a.dataset_id=p.dataset_id AND a.id=p.published_run_id AND a.status='SUCCEEDED'
 JOIN analysis_results r ON r.dataset_id=p.dataset_id AND r.run_id=p.published_run_id AND ((r.algorithm_name='workout.features' AND r.algorithm_version IN ('v1','v2','v3')) OR (r.algorithm_name='training_load.trimp' AND r.algorithm_version='v1')) AND r.metric IN ('duration','distance','workout_load')
 JOIN dirty_scopes d ON d.dataset_id=p.dataset_id AND d.scope_key=p.scope_key AND d.generation=d.clean_generation AND d.generation=p.generation
 WHERE p.dataset_id=$1 AND p.metric_family='workout' AND p.scope_key='workout:HKWorkoutTypeIdentifier:'||w.workout_uuid::text
 AND COALESCE((a.lineage->>'parameterVersion')::bigint,0)=COALESCE((SELECT max(version) FROM analysis_parameter_sets WHERE dataset_id=$1),0)
 GROUP BY p.published_run_id,p.generation
 ) p ON true WHERE w.dataset_id=$1 AND w.activity_type=37 `

const TrainingWorkoutsSQL = trainingWorkoutsSelect + `AND w.event_start >= $3 AND w.event_start < $4
 ORDER BY w.event_start,w.workout_uuid LIMIT 4097`
const TrainingWorkoutSQL = trainingWorkoutsSelect + `AND w.workout_uuid=$3::uuid`

// TrainingWindow resolves local calendar dates before SQL. AddDate handles DST
// and offset changes; subtracting 28*24h would choose different local dates.
func TrainingWindow(zone, day string) (time.Time, time.Time, error) {
	loc, e := time.LoadLocation(zone)
	if e != nil {
		return time.Time{}, time.Time{}, training.ErrInvalid
	}
	end, e := time.ParseInLocation("2006-01-02", day, loc)
	if e != nil || end.Format("2006-01-02") != day {
		return time.Time{}, time.Time{}, training.ErrInvalid
	}
	return end.AddDate(0, 0, -27).UTC(), end.AddDate(0, 0, 1).UTC(), nil
}

func loadTrainingWorkouts(ctx context.Context, q trainingDB, dataset, zone, day, id string) ([]training.Workout, error) {
	var rows pgx.Rows
	var e error
	if id != "" {
		rows, e = q.Query(ctx, TrainingWorkoutSQL, dataset, zone, id)
	} else {
		start, end, err := TrainingWindow(zone, day)
		if err != nil {
			return nil, err
		}
		rows, e = q.Query(ctx, TrainingWorkoutsSQL, dataset, zone, start, end)
	}
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
func loadTrainingReadinessSignal(ctx context.Context, q trainingDB, dataset, stack, tz, day string, at time.Time) (training.Signal, bool, error) {
	var sig training.Signal
	var payload []byte
	var hash, algorithm, version, runID string
	var generation int64
	scope, family := readiness.ScopeV2For(stack), "model_product_v2"
	if stack == modelmanifest.StackID() {
		scope, family = modelmanifest.DayScope(stack, day), "model_product_v2/training_readiness"
	}
	err := q.QueryRow(ctx, `SELECT a.payload,encode(a.content_sha256,'hex'),r.algorithm_name,r.algorithm_version,r.target_generation,r.id::text
FROM analysis_publications p JOIN analysis_runs r ON r.dataset_id=p.dataset_id AND r.id=p.published_run_id AND r.status='SUCCEEDED'
JOIN analysis_published_artifacts a ON a.dataset_id=r.dataset_id AND a.run_id=r.id AND a.artifact_key='training_readiness'
JOIN dirty_scopes d ON d.dataset_id=p.dataset_id AND d.scope_key=p.scope_key AND d.generation=p.generation AND d.clean_generation=p.generation
WHERE p.dataset_id=$1 AND p.scope_key=$2 AND p.metric_family=$3 AND p.day=$4::date`, dataset, scope, family, day).Scan(&payload, &hash, &algorithm, &version, &generation, &runID)
	if errors.Is(err, pgx.ErrNoRows) {
		return sig, false, nil
	}
	if err != nil {
		return sig, false, err
	}
	var r readiness.Result
	if json.Unmarshal(payload, &r) != nil || readiness.Hash(r) != hash || r.Dataset != dataset || r.Date != day || r.Schema != readiness.SchemaV2 || r.ModelVersion != readiness.ModelV2 || r.Generation != generation || r.ConfigHash != readiness.DefaultV2Config(tz).Hash() {
		return sig, false, syncing.Fail(503, "training_readiness_invalid")
	}
	sig = training.Signal{RunID: runID, Generation: generation, Day: r.Date, Name: "readiness", Algorithm: algorithm, Version: version, Quality: r.Quality}
	if r.Score != nil && r.Status == "available" && r.Quality == "complete" && r.AsOf <= at.UnixMicro() && at.UnixMicro() < r.ValidBefore {
		sig.Value = r.Score
	}
	return sig, true, nil
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
	binding, e := loadTrainingModelBinding(ctx, tx, l.DatasetID)
	if e != nil {
		return out, e
	}
	c.ObservedModelBinding = &binding
	if binding.Adapter == "schema2-v2" {
		kept := c.Signals[:0]
		for _, old := range c.Signals {
			if old.Name != "readiness" && old.Name != "fitness" && old.Name != "fatigue" {
				kept = append(kept, old)
			}
		}
		c.Signals = kept
		sig, ok, err := loadTrainingReadinessSignal(ctx, tx, l.DatasetID, binding.Assignment, p.Timezone, c.AsOf, at)
		if err != nil {
			return out, err
		}
		if ok {
			c.Signals = append(c.Signals, sig)
		}
		signals, err := loadTrainingLoadSignals(ctx, tx, l.DatasetID, binding.Assignment, p.Timezone, c.AsOf)
		if err != nil {
			return out, err
		}
		c.Signals = append(c.Signals, signals...)
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

func loadTrainingLoadSignals(ctx context.Context, q trainingDB, dataset, stack, tz, day string) ([]training.Signal, error) {
	scope := tl.ScopeStateV2For(stack)
	if stack == modelmanifest.StackID() {
		scope = modelmanifest.DayScope(stack, day)
	}
	var a publishedModelArtifact
	err := q.QueryRow(ctx, `SELECT a.payload,encode(a.content_sha256,'hex'),a.schema_version,r.algorithm_name,r.algorithm_version,r.target_generation,r.id::text
 FROM analysis_publications p
 JOIN analysis_runs r ON r.dataset_id=p.dataset_id AND r.id=p.published_run_id AND r.scope_key=p.scope_key AND r.target_generation=p.generation
 JOIN analysis_published_artifacts a ON a.dataset_id=r.dataset_id AND a.run_id=r.id AND a.artifact_key='load_state'
 JOIN dirty_scopes d ON d.dataset_id=p.dataset_id AND d.scope_key=p.scope_key AND d.generation=p.generation AND d.clean_generation=p.generation
 WHERE p.dataset_id=$1 AND p.scope_key=$2 AND p.day=$3::date`, dataset, scope, day).Scan(&a.Payload, &a.ContentHash, &a.Schema, &a.Algorithm, &a.Version, &a.Generation, &a.RunID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state tl.StateResult
	if !validHistoryArtifact("trainingLoad", dataset, tz, a) || json.Unmarshal(a.Payload, &state) != nil {
		return nil, syncing.Fail(503, "training_load_invalid")
	}
	for _, x := range state.Days {
		if x.Day == day && x.Quality == "complete" && x.Acute.Value != nil && x.Chronic.Value != nil {
			acute := training.Signal{RunID: a.RunID, Generation: a.Generation, Day: day, Algorithm: a.Algorithm, Version: a.Version, Quality: x.Quality, Name: "acute", Value: x.Acute.Value}
			chronic := acute
			chronic.Name, chronic.Value = "chronic", x.Chronic.Value
			return []training.Signal{acute, chronic}, nil
		}
	}
	return nil, nil
}
