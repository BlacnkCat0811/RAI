package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"rai/server/internal/analysis"
	pf "rai/server/internal/performance"
	"rai/server/internal/storage/postgres/dbgen"
	wf "rai/server/internal/workoutfeatures"
)

func (s *Store) loadPerformanceInput(ctx context.Context, tx pgx.Tx, l Lease, in *analysis.Snapshot, asOf time.Time, tz string) error {
	if l.ScopeKey != pf.Scope() {
		return analysis.Invalid()
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return analysis.Invalid()
	}
	a := &pf.Input{Calendar: loc, Timezone: tz, DatasetID: l.DatasetID, Generation: in.Generation, AsOf: asOf.UnixMicro(), View: "revised", Features: []pf.FeatureRef{}}
	in.PerformanceInput = a
	in.InputVersion = "rai.performance.input/1"
	var enabled bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM analysis_model_channels WHERE dataset_id=$1 AND channel='candidate' AND extension_configs->>'rai.performance_curve/v1'=$2)`, l.DatasetID, pf.ConfigHash()).Scan(&enabled); err != nil {
		return err
	}
	if !enabled {
		return analysis.Invalid()
	}
	// Locate the newest M1-qualified performance in a bounded live window. The
	// 90d evidence horizon and 13d smoothing seed are anchored to evidence days;
	// daily refresh is only a projection of the saved result, never a new target.
	var anchor *time.Time
	err = tx.QueryRow(ctx, `SELECT max(w.event_end) FROM workouts w
 JOIN analysis_publications p ON p.dataset_id=w.dataset_id AND p.scope_key=$2||w.workout_uuid::text AND p.metric_family='workout_model'
 JOIN dirty_scopes d ON d.dataset_id=p.dataset_id AND d.scope_key=p.scope_key AND d.generation=d.clean_generation AND d.generation=p.generation
 JOIN analysis_artifacts f ON f.dataset_id=p.dataset_id AND f.run_id=p.published_run_id AND f.artifact_key='workout_features'
 WHERE w.dataset_id=$1 AND w.event_end>=$3 AND w.event_end<=$4 AND f.payload->'qualification'->'performance'->>'status' IN ('eligible','conditional')`, l.DatasetID, "model/"+wf.StackID()+"/workout/", asOf.AddDate(0, 0, -90), asOf).Scan(&anchor)
	if err != nil {
		return err
	}
	h := asOf.In(loc)
	if anchor != nil {
		h = anchor.In(loc)
	}
	h = time.Date(h.Year(), h.Month(), h.Day(), 0, 0, 0, 0, loc)
	begin := h.AddDate(0, 0, -102)
	load := func(begin time.Time) error {
		// A known workout with unavailable features is pending, never silently absent.
		var pending bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workouts w LEFT JOIN dirty_scopes d ON d.dataset_id=w.dataset_id AND d.scope_key=$2||w.workout_uuid::text
 LEFT JOIN analysis_publications p ON p.dataset_id=d.dataset_id AND p.scope_key=d.scope_key AND p.metric_family='workout_model' AND p.generation=d.generation
 WHERE w.dataset_id=$1 AND w.event_end>=$3 AND w.event_end<=$4 AND w.payload->'workout'->>'activityType'='37' AND w.first_received_at-w.event_end<=interval '90 days'
 AND (d.generation IS NULL OR d.generation<>d.clean_generation OR p.published_run_id IS NULL))`, l.DatasetID, "model/"+wf.StackID()+"/workout/", begin, asOf).Scan(&pending)
		if err != nil {
			return err
		}
		if pending {
			return &analysis.Failure{Code: "analysis_dependency_pending", Retryable: true}
		}
		rows, err := dbgen.New(tx).PerformanceFeatureRefs(ctx, dbgen.PerformanceFeatureRefsParams{DatasetID: l.DatasetID, Column2: "model/" + wf.StackID() + "/workout/", EventEnd: pgtype.Timestamptz{Time: begin, Valid: true}, EventEnd_2: pgtype.Timestamptz{Time: asOf, Valid: true}})
		if err != nil {
			return err
		}
		if len(rows) > pf.MaxWorkouts {
			return &analysis.Failure{Code: "analysis_input_limit"}
		}
		for _, row := range rows {
			var ref pf.FeatureRef
			ref.RunID = row.RunID
			ref.KnownAt = row.KnownAt.Time.UnixMicro()
			ref.FirstReceivedAt = row.FirstReceivedAt.Time.UnixMicro()
			if json.Unmarshal(row.Payload, &ref.Feature) != nil || pf.Hash(ref.Feature) != row.ContentHash || ref.Feature.WorkoutID != row.WorkoutID {
				return analysis.Invalid()
			}
			a.Features = append(a.Features, ref)
		}
		return nil
	}
	if err = load(begin); err != nil {
		return err
	}
	accepted, anchorErr := pf.EvidenceAnchor(*a)
	if anchorErr != nil {
		return analysis.Invalid()
	}
	if accepted > 0 {
		acceptedDay := time.UnixMicro(accepted).In(loc)
		acceptedDay = time.Date(acceptedDay.Year(), acceptedDay.Month(), acceptedDay.Day(), 0, 0, 0, 0, loc)
		corrected := acceptedDay.AddDate(0, 0, -102)
		if corrected.Before(begin) {
			begin = corrected
			a.Features = nil
			if err = load(begin); err != nil {
				return err
			}
		}
	}
	in.From = begin.UTC()
	in.Through = asOf.UTC()
	return nil
}
