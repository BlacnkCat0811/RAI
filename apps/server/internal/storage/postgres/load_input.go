package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"rai/server/internal/analysis"
	"rai/server/internal/syncing"
	tl "rai/server/internal/trainingload"
	wf "rai/server/internal/workoutfeatures"
	"strings"
	"time"
)

func (s *Store) loadTrainingLoadInput(ctx context.Context, tx pgx.Tx, l Lease, in *analysis.Snapshot, tz string) error {
	id := strings.TrimPrefix(l.ScopeKey, "model/"+wf.StackID()+"/workout_load/")
	if !syncing.ValidID(id) {
		return analysis.Invalid()
	}
	in.InputVersion = "rai.training_load.input/1"
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return analysis.Invalid()
	}
	input := &tl.WorkoutInput{Timezone: tz, Calendar: loc}
	in.LoadInput = input
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2)`, l.DatasetID, id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		input.Deleted = true
		input.Feature.DatasetID = l.DatasetID
		input.Feature.WorkoutID = id
		return nil
	}
	var fb, eb []byte
	var fh, eh string
	err = tx.QueryRow(ctx, `SELECT f.payload,e.payload,encode(f.content_sha256,'hex'),encode(e.content_sha256,'hex'),p.published_run_id::text
 FROM analysis_publications p JOIN dirty_scopes d USING(dataset_id,scope_key)
 JOIN analysis_runs r ON r.dataset_id=p.dataset_id AND r.id=p.published_run_id AND r.status='SUCCEEDED'
 JOIN analysis_artifacts f ON f.dataset_id=p.dataset_id AND f.run_id=p.published_run_id AND f.artifact_key='workout_features'
 JOIN analysis_artifacts e ON e.dataset_id=p.dataset_id AND e.run_id=p.published_run_id AND e.artifact_key='hr_exposure'
 WHERE p.dataset_id=$1 AND p.scope_key=$2 AND d.generation=d.clean_generation AND p.generation=d.generation LIMIT 1`, l.DatasetID, wf.Scope(id)).Scan(&fb, &eb, &fh, &eh, &input.FeatureRunID)
	if errors.Is(err, pgx.ErrNoRows) {
		return &analysis.Failure{Code: "analysis_dependency_pending", Retryable: true}
	}
	if err != nil {
		return err
	}
	if json.Unmarshal(fb, &input.Feature) != nil || json.Unmarshal(eb, &input.Exposure) != nil {
		return analysis.Invalid()
	}
	if tl.Hash(input.Feature) != fh || tl.Hash(input.Exposure) != eh {
		return analysis.Invalid()
	}
	in.From = time.UnixMicro(input.Feature.Timing.Start).UTC()
	in.Through = time.UnixMicro(input.Feature.Timing.End).UTC()
	input.CalendarIdentity = tl.CalendarIdentity(loc, in.From, in.Through)
	return nil
}

// Only the bounded immutable load ledger is read. No HR/sample/history parsing.
func (s *Store) loadFiniteStateInput(ctx context.Context, tx pgx.Tx, l Lease, in *analysis.Snapshot, asOf time.Time, tz string) error {
	if l.ScopeKey != tl.StateScope() {
		return analysis.Invalid()
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return analysis.Invalid()
	}
	date := func(t time.Time) time.Time {
		t = t.In(loc)
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	}
	from := date(in.From)
	through := date(in.Through.Add(-time.Microsecond)).AddDate(0, 0, 186)
	today := date(asOf)
	if through.After(today) {
		through = today
	}
	if from.After(today) {
		return analysis.Invalid()
	}
	if through.Before(from) {
		through = from
	}
	if through.After(from.AddDate(0, 0, tl.MaxRecomputeDays-1)) {
		return &analysis.Failure{Code: "analysis_input_limit"}
	}
	a := &tl.StateInput{Calendar: loc, CalendarIdentity: tl.CalendarIdentity(loc, from.AddDate(0, 0, -186), through.AddDate(0, 0, 1)), DatasetID: l.DatasetID, Generation: in.Generation, Timezone: tz, From: from.Format("2006-01-02"), Through: through.Format("2006-01-02"), HistoryFrom: from.Format("2006-01-02"), Loads: []tl.LedgerLoad{}}
	var first *time.Time
	if err = tx.QueryRow(ctx, `SELECT min(local_date) FROM analysis_load_ledger WHERE dataset_id=$1`, l.DatasetID).Scan(&first); err != nil {
		return err
	}
	if first != nil {
		a.HistoryFrom = first.Format("2006-01-02")
	}
	begin, end := from.AddDate(0, 0, -186), through.AddDate(0, 0, 1)
	// Every known running Workout in the input range must have a fresh load.
	// Missing load is pending; a completed insufficient load is a ledger NULL.
	var pending bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workouts w LEFT JOIN dirty_scopes d ON d.dataset_id=w.dataset_id AND d.scope_key=$2||w.workout_uuid::text
 LEFT JOIN analysis_publications p ON p.dataset_id=d.dataset_id AND p.scope_key=d.scope_key AND p.generation=d.generation
 WHERE w.dataset_id=$1 AND w.event_start<$4 AND w.event_end>$3 AND w.payload->'workout'->>'activityType'='37'
 AND (d.generation IS NULL OR d.generation<>d.clean_generation OR p.published_run_id IS NULL))`, l.DatasetID, "model/"+wf.StackID()+"/workout_load/", begin, end).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return &analysis.Failure{Code: "analysis_dependency_pending", Retryable: true}
	}
	rows, err := tx.Query(ctx, `SELECT x.workout_uuid::text,x.run_id::text,x.local_date::text,x.contribution,x.quality FROM analysis_load_ledger x
 JOIN analysis_publications p ON p.dataset_id=x.dataset_id AND p.scope_key=$2||x.workout_uuid::text AND p.published_run_id=x.run_id AND p.day=x.local_date
 JOIN dirty_scopes d ON d.dataset_id=p.dataset_id AND d.scope_key=p.scope_key AND d.generation=d.clean_generation AND d.generation=p.generation
 JOIN workouts w ON w.dataset_id=x.dataset_id AND w.workout_uuid=x.workout_uuid
 WHERE w.payload->'workout'->>'activityType'='37' AND x.dataset_id=$1 AND x.local_date>=$3::date AND x.local_date<$4::date ORDER BY x.local_date,x.workout_uuid LIMIT 10001`, l.DatasetID, "model/"+wf.StackID()+"/workout_load/", begin.Format("2006-01-02"), end.Format("2006-01-02"))
	if err != nil {
		return err
	}
	for rows.Next() {
		var x tl.LedgerLoad
		if err = rows.Scan(&x.WorkoutID, &x.RunID, &x.Day, &x.Load, &x.Quality); err != nil {
			rows.Close()
			return err
		}
		a.Loads = append(a.Loads, x)
		if len(a.Loads) > 10000 {
			rows.Close()
			return &analysis.Failure{Code: "analysis_input_limit"}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	in.LoadStateInput = a
	in.InputVersion = "rai.load_state.input/1"
	in.From = from.UTC()
	in.Through = end.Add(-time.Microsecond).UTC()
	return nil
}
