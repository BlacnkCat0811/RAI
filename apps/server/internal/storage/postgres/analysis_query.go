package postgres

import (
	"context"
	"encoding/json"
	"time"

	"rai/server/internal/analysis"
	"rai/server/internal/syncing"
)

func (s *Store) CurrentWorkoutAnalysis(ctx context.Context, p syncing.Principal, id string) (analysis.Current, error) {
	if !syncing.ValidID(id) {
		return analysis.Current{}, syncing.Fail(422, "invalid_workout_id")
	}
	return s.currentAnalysis(ctx, p.DatasetID, "workout:"+syncing.WorkoutType+":"+id, "workout", id)
}
func (s *Store) CurrentTrainingState(ctx context.Context, p syncing.Principal) (analysis.Current, error) {
	return s.currentAnalysis(ctx, p.DatasetID, analysis.StateScope, "state", "")
}
func (s *Store) currentAnalysis(ctx context.Context, dataset, scope, family, workout string) (analysis.Current, error) {
	rows, err := s.Pool.Query(ctx, AnalysisCurrentSQL, dataset, scope, family, workout)
	if err != nil {
		return analysis.Current{}, err
	}
	defer rows.Close()
	var out analysis.Current
	for rows.Next() {
		var m analysis.Metric
		var b []byte
		var computed time.Time
		if err = rows.Scan(&out.Scope, &out.RunID, &out.Generation, &out.Algorithm, &out.AlgorithmVersion, &out.InputVersion, &out.InputHash, &out.PublishedAt, &computed, &m.Day, &m.Name, &m.Algorithm, &m.Version, &m.Value, &m.Unit, &m.Quality, &b); err != nil {
			return out, err
		}
		out.ComputedAt = computed
		if err = json.Unmarshal(b, &m.Details); err != nil {
			return out, err
		}
		if family == "workout" {
			m.Day = ""
		}
		out.Metrics = append(out.Metrics, m)
	}
	if err = rows.Err(); err != nil {
		return out, err
	}
	if out.RunID == "" {
		return out, syncing.Fail(404, "analysis_not_ready")
	}
	return out, nil
}

// AnalysisCurrentSQL is shared with actual EXPLAIN/ANALYZE acceptance tests.
const AnalysisCurrentSQL = `SELECT p.scope_key,p.published_run_id::text,p.generation,a.algorithm_name,a.algorithm_version,a.input_version,encode(a.input_hash,'hex'),p.published_at,
 r.computed_at,r.day::text,r.metric,r.algorithm_name,r.algorithm_version,r.value,r.unit,r.quality,r.details
 FROM analysis_publications p JOIN analysis_runs a ON a.dataset_id=p.dataset_id AND a.id=p.published_run_id AND a.status='SUCCEEDED'
 JOIN analysis_results r ON r.dataset_id=p.dataset_id AND r.run_id=p.published_run_id AND r.day=p.day
 JOIN dirty_scopes d ON d.dataset_id=p.dataset_id AND d.scope_key=p.scope_key
 JOIN datasets x ON x.id=p.dataset_id
 WHERE p.dataset_id=$1 AND p.scope_key=$2 AND p.metric_family=$3 AND d.generation=d.clean_generation
 AND (COALESCE((a.lineage->>'parameterVersion')::bigint,0) = COALESCE((SELECT max(version) FROM analysis_parameter_sets WHERE dataset_id=$1),0) OR $3='state')
 AND (($3='workout' AND p.generation=d.generation AND EXISTS(SELECT 1 FROM workouts WHERE dataset_id=$1 AND workout_uuid=NULLIF($4,'')::uuid))
 OR ($3='state' AND p.day=(clock_timestamp() AT TIME ZONE x.timezone)::date)) ORDER BY r.metric`
