package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"rai/server/internal/modelmanifest"
	"rai/server/internal/modelrelease"
	"rai/server/internal/readiness"
	rf "rai/server/internal/runfitness"
	sr "rai/server/internal/sessionrecovery"
	"rai/server/internal/syncing"
	"rai/server/internal/training"
	tl "rai/server/internal/trainingload"

	"github.com/jackc/pgx/v5"
)

// Only current, clean publications supply features; raw rows filter identity and
// running activity, but no raw measurements are read by the coach.
// Workout facts for the plan come from the active model stack's per-Workout
// results: active duration and preferred distance from the cached features,
// load from the latest v2 day release.
const trainingWorkoutsSelect = `SELECT w.workout_uuid::text,COALESCE(r.feature->'evidence'->>'inputHash',''),r.fact_generation,
 (w.event_start AT TIME ZONE $2)::date::text,(r.feature->'timing'->>'activeSeconds')::float8,
 (r.feature->'distance'->>'preferredDistanceM')::float8,(r.load->>'load')::float8,
 jsonb_build_array(jsonb_build_object('runID',COALESCE(r.feature->'evidence'->>'inputHash',''),'generation',r.fact_generation,
  'day',(w.event_start AT TIME ZONE $2)::date::text,'metric','workout_load','algorithm',COALESCE(r.load->>'model',''),
  'algorithmVersion',COALESCE(r.load->>'modelVersion',''),'quality',COALESCE(r.load->>'quality','insufficient'),'value',(r.load->>'load')::float8)),
 (r.load->'loadFocus'->'buckets'->>'lowAerobicAU')::float8,(r.load->'loadFocus'->'buckets'->>'highAerobicAU')::float8,
 (r.load->'loadFocus'->'buckets'->>'anaerobicProxyAU')::float8
 FROM workouts w
 JOIN model_workout_results r ON r.dataset_id=w.dataset_id AND r.workout_uuid=w.workout_uuid
 JOIN analysis_model_channels c ON c.dataset_id=r.dataset_id AND c.stack_id=r.stack_id AND c.channel='active'
 WHERE w.dataset_id=$1 AND w.activity_type=37 `

const TrainingWorkoutsSQL = trainingWorkoutsSelect + `AND w.event_start >= $3 AND w.event_start < $4
 ORDER BY w.event_start,w.workout_uuid LIMIT 4097`
const TrainingWorkoutSQL = trainingWorkoutsSelect + `AND w.workout_uuid=$3::uuid`

// TrainingWindow resolves local calendar dates before SQL. AddDate handles DST
// and offset changes; subtracting 28*24h would choose different local dates.
func TrainingWindow(zone, day string) (time.Time, time.Time, error) {
	return trainingWindowDays(zone, day, 28)
}

// trainingWindowDays is the window of the given number of local days ending
// with day. v3 plans read 28 days; v4 plans read their history parameter.
func trainingWindowDays(zone, day string, days int) (time.Time, time.Time, error) {
	loc, e := time.LoadLocation(zone)
	if e != nil {
		return time.Time{}, time.Time{}, training.ErrInvalid
	}
	end, e := time.ParseInLocation("2006-01-02", day, loc)
	if e != nil || end.Format("2006-01-02") != day {
		return time.Time{}, time.Time{}, training.ErrInvalid
	}
	return end.AddDate(0, 0, 1-days).UTC(), end.AddDate(0, 0, 1).UTC(), nil
}

func loadTrainingWorkouts(ctx context.Context, q trainingDB, dataset, zone, day, id string, days int) ([]training.Workout, error) {
	var rows pgx.Rows
	var e error
	if id != "" {
		rows, e = q.Query(ctx, TrainingWorkoutSQL, dataset, zone, id)
	} else {
		start, end, err := trainingWindowDays(zone, day, days)
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
		if e = rows.Scan(&w.ID, &w.RunID, &w.Generation, &w.Date, &w.DurationS, &w.DistanceM, &w.Load, &mb, &w.FocusLow, &w.FocusHigh, &w.FocusAnaerobic); e != nil {
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
	var a publishedModelArtifact
	scope, family := modelmanifest.DayScope(stack, day), "model_product_v2/training_readiness"
	err := q.QueryRow(ctx, `SELECT a.payload,encode(a.content_sha256,'hex'),a.schema_version,r.algorithm_name,r.algorithm_version,r.target_generation,r.id::text,r.lineage->>'manifestSHA256'
FROM analysis_publications p JOIN analysis_runs r ON r.dataset_id=p.dataset_id AND r.id=p.published_run_id AND r.status='SUCCEEDED'
JOIN analysis_published_artifacts a ON a.dataset_id=r.dataset_id AND a.run_id=r.id AND a.artifact_key='training_readiness'
JOIN dirty_scopes d ON d.dataset_id=p.dataset_id AND d.scope_key=p.scope_key AND d.generation=p.generation AND d.clean_generation=p.generation
WHERE p.dataset_id=$1 AND p.scope_key=$2 AND p.metric_family=$3 AND p.day=$4::date`, dataset, scope, family, day).Scan(&a.Payload, &a.ContentHash, &a.Schema, &a.Algorithm, &a.Version, &a.Generation, &a.RunID, &a.ManifestSHA)
	if errors.Is(err, pgx.ErrNoRows) {
		return sig, false, nil
	}
	if err != nil {
		return sig, false, err
	}
	if a.Schema == readiness.SchemaV4 {
		release, ok := modelrelease.Lookup(stack, a.ManifestSHA, tz)
		var r readiness.V4Result
		if !ok || !validHistoryArtifactForRelease("trainingReadiness", dataset, tz, a, release) || json.Unmarshal(a.Payload, &r) != nil || r.Date != day {
			return sig, false, syncing.Fail(503, "training_readiness_invalid")
		}
		fresh, e := readiness.FreshenV4(r, at.UnixMicro())
		if e != nil {
			return sig, false, syncing.Fail(503, "training_readiness_invalid")
		}
		sig = training.Signal{RunID: a.RunID, Generation: a.Generation, Day: r.Date, Name: "readiness", Algorithm: a.Algorithm, Version: a.Version, Quality: r.Quality, Mode: fresh.Mode, EvidenceAt: fresh.EvidenceAt, ValidBefore: &fresh.ValidBefore, Confidence: &fresh.Confidence}
		if fresh.Score != nil && (fresh.Mode == "complete" || fresh.Mode == "estimated") {
			sig.Value = fresh.Score
		}
		if fresh.Range != nil {
			sig.SensitivityLow = &fresh.Range.Low
			sig.SensitivityHigh = &fresh.Range.High
		}
		return sig, true, nil
	}
	var r readiness.Result
	release, supported := modelrelease.Lookup(stack, a.ManifestSHA, tz)
	if !supported {
		return sig, false, nil
	}
	if !validHistoryArtifactForRelease("trainingReadiness", dataset, tz, a, release) || json.Unmarshal(a.Payload, &r) != nil || r.Date != day {
		return sig, false, syncing.Fail(503, "training_readiness_invalid")
	}
	sig = training.Signal{RunID: a.RunID, Generation: a.Generation, Day: r.Date, Name: "readiness", Algorithm: a.Algorithm, Version: a.Version, Quality: r.Quality}
	if r.Score != nil && r.Status == "available" && r.Quality == "complete" && r.AsOf <= at.UnixMicro() && at.UnixMicro() < r.ValidBefore {
		fresh, err := readiness.FreshenV3ForConfig(r, at.UnixMicro(), release.ReadinessConfig)
		if err != nil {
			return sig, false, syncing.Fail(503, "training_readiness_invalid")
		}
		sig.Value = fresh.Score
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
	if e = tx.QueryRow(ctx, `SELECT parameters FROM training_parameter_sets WHERE version=$1`, training.ParameterVersionFor(p.Config)).Scan(&b); e != nil {
		return out, e
	}
	window := 28
	if p.Config.V2() {
		var coach training.CoachParameters
		if e = json.Unmarshal(b, &coach); e != nil {
			return out, e
		}
		c.Coach = &coach
		window = coach.HistoryDays
		if e = loadTrainingPrevious(ctx, tx, l.DatasetID, p, &c); e != nil {
			return out, e
		}
	} else if e = json.Unmarshal(b, &c.Parameters); e != nil {
		return out, e
	}
	c.Workouts, e = loadTrainingWorkouts(ctx, tx, l.DatasetID, p.Timezone, c.AsOf, "", window)
	if e != nil {
		return out, e
	}
	out.Workouts = map[string]training.Workout{}
	for _, w := range c.Workouts {
		out.Workouts[w.ID] = w
	}
	binding, e := loadTrainingModelBinding(ctx, tx, l.DatasetID)
	if e != nil {
		return out, e
	}
	c.ObservedModelBinding = &binding
	if binding.Adapter == "schema2-v2" || binding.Adapter == "schema3-v1" || binding.Adapter == "schema4-v1" {
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
		signals, err = loadTrainingTargetSignals(ctx, tx, l.DatasetID, binding.Assignment, p.Timezone, c.AsOf, at)
		if err != nil {
			return out, err
		}
		c.Signals = append(c.Signals, signals...)
		if p.Config.V2() && binding.Adapter != "schema2-v2" {
			sig, ok, err := loadTrainingSessionRecoverySignal(ctx, tx, l.DatasetID, binding.Assignment, p.Timezone, c.AsOf, at)
			if err != nil {
				return out, err
			}
			if ok {
				c.Signals = append(c.Signals, sig)
			}
		}
	}
	rows, e := tx.Query(ctx, `SELECT id::text FROM training_executions WHERE dataset_id=$1 AND plan_id=$2 AND status IN ('completed','skipped') ORDER BY id LIMIT 4097`, l.DatasetID, p.ID)
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
				ws, e := loadTrainingWorkouts(ctx, tx, l.DatasetID, p.Timezone, c.AsOf, ex.WorkoutID, 0)
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
	scope := modelmanifest.DayScope(stack, day)
	var a publishedModelArtifact
	err := q.QueryRow(ctx, `SELECT a.payload,encode(a.content_sha256,'hex'),a.schema_version,r.algorithm_name,r.algorithm_version,r.target_generation,r.id::text,r.lineage->>'manifestSHA256'
 FROM analysis_publications p
 JOIN analysis_runs r ON r.dataset_id=p.dataset_id AND r.id=p.published_run_id AND r.scope_key=p.scope_key AND r.target_generation=p.generation
 JOIN analysis_published_artifacts a ON a.dataset_id=r.dataset_id AND a.run_id=r.id AND a.artifact_key='load_state'
 JOIN dirty_scopes d ON d.dataset_id=p.dataset_id AND d.scope_key=p.scope_key AND d.generation=p.generation AND d.clean_generation=p.generation
 WHERE p.dataset_id=$1 AND p.scope_key=$2 AND p.day=$3::date`, dataset, scope, day).Scan(&a.Payload, &a.ContentHash, &a.Schema, &a.Algorithm, &a.Version, &a.Generation, &a.RunID, &a.ManifestSHA)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state tl.StateResult
	if !validHistoryArtifactForStack("trainingLoad", dataset, tz, stack, a) || json.Unmarshal(a.Payload, &state) != nil {
		return nil, syncing.Fail(503, "training_load_invalid")
	}
	for _, x := range state.Days {
		if x.Day == day && (x.Quality == "complete" || x.Quality == "partial") && x.Acute.Value != nil && x.Chronic.Value != nil {
			acute := training.Signal{RunID: a.RunID, Generation: a.Generation, Day: day, Algorithm: a.Algorithm, Version: a.Version, Quality: x.Quality, Name: "acute", Value: x.Acute.Value}
			chronic := acute
			chronic.Name, chronic.Value = "chronic", x.Chronic.Value
			return []training.Signal{acute, chronic}, nil
		}
	}
	return nil, nil
}

// loadTrainingTargetSignals supplies session targets: the schedulable
// running-fitness zone paces of the newest clean day state (the plan's frozen
// PacePrescriptionSnapshot) and the HR reference effective at the plan day.
// A zone that may not be scheduled emits nothing; sessions then carry only
// an HR target, never a pace from another zone or a retired v60 formula.
func loadTrainingTargetSignals(ctx context.Context, q trainingDB, dataset, stack, tz, day string, at time.Time) ([]training.Signal, error) {
	out := []training.Signal{}
	scope := modelmanifest.DayScope(stack, day)
	var a publishedModelArtifact
	err := q.QueryRow(ctx, `SELECT a.payload,encode(a.content_sha256,'hex'),a.schema_version,r.algorithm_name,r.algorithm_version,r.target_generation,r.id::text,r.lineage->>'manifestSHA256'
 FROM analysis_publications p
 JOIN analysis_runs r ON r.dataset_id=p.dataset_id AND r.id=p.published_run_id AND r.scope_key=p.scope_key AND r.target_generation=p.generation
 JOIN analysis_published_artifacts a ON a.dataset_id=r.dataset_id AND a.run_id=r.id AND a.artifact_key=$4
 JOIN dirty_scopes d ON d.dataset_id=p.dataset_id AND d.scope_key=p.scope_key AND d.generation=p.generation AND d.clean_generation=p.generation
 WHERE p.dataset_id=$1 AND p.scope_key=$2 AND p.day<=$3::date ORDER BY p.day DESC LIMIT 1`, dataset, scope, day, rf.ArtifactKey).Scan(&a.Payload, &a.ContentHash, &a.Schema, &a.Algorithm, &a.Version, &a.Generation, &a.RunID, &a.ManifestSHA)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err == nil && validHistoryArtifactForStack("runningFitness", dataset, tz, stack, a) {
		var r rf.State
		if json.Unmarshal(a.Payload, &r) == nil {
			for _, s := range trainingPaceSignals(rf.Freshen(r, day)) {
				s.RunID, s.Generation, s.Day = a.RunID, a.Generation, day
				out = append(out, s)
			}
		}
	}
	ref, err := hrReferenceAt(ctx, q, dataset, at)
	if err != nil {
		return nil, err
	}
	if ref != nil {
		out = append(out, training.Signal{RunID: ref.ID, Day: day, Name: "hr_max", Algorithm: "hr_reference", Version: "v2", Quality: "complete", Value: &ref.MaxHR})
		if ref.RestingHR != nil {
			out = append(out, training.Signal{RunID: ref.ID, Day: day, Name: "hr_rest", Algorithm: "hr_reference", Version: "v2", Quality: "complete", Value: ref.RestingHR})
		}
	}
	return out, nil
}

// trainingPaceSignals maps schedulable zones to target paces: the midpoint of
// the zone (for Z5 the slow end of its estimated range is already the fast
// bound) and the marathon race pace when it may be scheduled.
func trainingPaceSignals(r rf.State) []training.Signal {
	var out []training.Signal
	add := func(name string, pace float64, support string) {
		v := pace
		out = append(out, training.Signal{Name: "pace_" + name, Algorithm: training.PaceAlgorithm, Version: rf.Version, Quality: "complete", Mode: support, Value: &v})
	}
	for _, z := range r.Zones.Zones {
		if !z.Schedulable || z.FastPace == nil || z.SlowPace == nil {
			continue
		}
		switch z.Name {
		case "Z1", "Z2", "Z3", "Z4", "Z5":
			add(strings.ToLower(z.Name), (*z.FastPace+*z.SlowPace)/2, z.Support)
		}
	}
	for _, p := range r.RacePaces {
		if p.Distance == "marathon" && p.Schedulable && p.PaceSecKm != nil {
			add("marathon", *p.PaceSecKm, p.Support)
		}
	}
	// Race predictions eligible as targets (direct or calibrated support)
	// let a v4 race plan assess its goal; v3 ignores them.
	for _, p := range r.Predictions {
		if p.TargetEligible && p.Status == "available" && p.Seconds != nil && *p.Seconds > 0 {
			add("predict_"+p.Distance, *p.Seconds, p.Support)
		}
	}
	return out
}

// loadTrainingPrevious supplies a v4 plan with its current base: the days
// before AsOf (frozen) and the week decisions already taken, plus the user's
// active overrides, which adaptation must leave in place.
func loadTrainingPrevious(ctx context.Context, q trainingDB, dataset string, p training.Plan, c *training.Context) error {
	var b []byte
	err := q.QueryRow(ctx, `SELECT b.base FROM training_plans p JOIN training_base_versions b ON b.dataset_id=p.dataset_id AND b.plan_id=p.id AND b.id=p.current_base_id
 WHERE p.dataset_id=$1 AND p.id=$2`, dataset, p.ID).Scan(&b)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var base training.Base
	if err = json.Unmarshal(b, &base); err != nil {
		return err
	}
	loc, err := time.LoadLocation(p.Timezone)
	if err != nil {
		return training.ErrInvalid
	}
	asOf, err := time.ParseInLocation("2006-01-02", c.AsOf, loc)
	if err != nil {
		return training.ErrInvalid
	}
	from := asOf.AddDate(0, 0, -42).Format("2006-01-02")
	for _, s := range base.Sessions {
		if s.Date >= from && s.Date < c.AsOf {
			c.PreviousSessions = append(c.PreviousSessions, s)
		}
	}
	for _, w := range base.Weeks {
		if w.Detail != "outline" {
			c.PreviousWeeks = append(c.PreviousWeeks, w)
		}
	}
	os, err := loadOverrides(ctx, q, dataset, p.ID, p.OverrideRevision)
	if err != nil {
		return err
	}
	for _, o := range training.ActiveOverrides(os) {
		if o.Status == "active" && o.Operation != "revoke" {
			c.Overrides = append(c.Overrides, o)
		}
	}
	return nil
}

// loadTrainingSessionRecoverySignal reads today's session-recovery release
// of the active stack and emits its readyAfter instant (unix microseconds).
func loadTrainingSessionRecoverySignal(ctx context.Context, q trainingDB, dataset, stack, tz, day string, at time.Time) (training.Signal, bool, error) {
	var sig training.Signal
	scope := modelmanifest.DayScope(stack, day)
	var a publishedModelArtifact
	err := q.QueryRow(ctx, `SELECT a.payload,encode(a.content_sha256,'hex'),a.schema_version,r.algorithm_name,r.algorithm_version,r.target_generation,r.id::text,r.lineage->>'manifestSHA256'
 FROM analysis_publications p
 JOIN analysis_runs r ON r.dataset_id=p.dataset_id AND r.id=p.published_run_id AND r.scope_key=p.scope_key AND r.target_generation=p.generation
 JOIN analysis_published_artifacts a ON a.dataset_id=r.dataset_id AND a.run_id=r.id AND a.artifact_key=$4
 JOIN dirty_scopes d ON d.dataset_id=p.dataset_id AND d.scope_key=p.scope_key AND d.generation=p.generation AND d.clean_generation=p.generation
 WHERE p.dataset_id=$1 AND p.scope_key=$2 AND p.day=$3::date`, dataset, scope, day, sr.Key).Scan(&a.Payload, &a.ContentHash, &a.Schema, &a.Algorithm, &a.Version, &a.Generation, &a.RunID, &a.ManifestSHA)
	if errors.Is(err, pgx.ErrNoRows) {
		return sig, false, nil
	}
	if err != nil {
		return sig, false, err
	}
	var r sr.Result
	if !validHistoryArtifactForStack("sessionRecovery", dataset, tz, stack, a) || json.Unmarshal(a.Payload, &r) != nil {
		return sig, false, nil
	}
	fresh, err := sr.Freshen(r, at.UnixMicro())
	if err != nil || fresh.ReadyAfter == nil {
		return sig, false, nil
	}
	v := float64(*fresh.ReadyAfter)
	return training.Signal{RunID: a.RunID, Generation: a.Generation, Day: day, Name: "ready_after", Algorithm: training.SessionRecoveryAlgorithm, Version: a.Version, Quality: fresh.Status, Mode: fresh.Mode, Value: &v}, true, nil
}
