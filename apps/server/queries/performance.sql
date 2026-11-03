-- name: PerformanceFeatureRefs :many
SELECT w.workout_uuid::text AS workout_id, f.payload, encode(f.content_sha256,'hex') AS content_hash,
 r.id::text AS run_id,r.finished_at AS known_at,w.first_received_at
FROM workouts w
JOIN dirty_scopes d ON d.dataset_id=w.dataset_id AND d.scope_key=$2::text||w.workout_uuid::text
JOIN analysis_publications p ON p.dataset_id=d.dataset_id AND p.scope_key=d.scope_key AND p.metric_family='workout_model' AND p.generation=d.generation
JOIN analysis_runs r ON r.dataset_id=p.dataset_id AND r.id=p.published_run_id AND r.status='SUCCEEDED'
JOIN analysis_artifacts f ON f.dataset_id=r.dataset_id AND f.run_id=r.id AND f.artifact_key='workout_features'
WHERE w.dataset_id=$1 AND w.event_end>=$3 AND w.event_end<=$4 AND d.generation=d.clean_generation AND w.first_received_at-w.event_end<=interval '90 days'
ORDER BY w.workout_uuid LIMIT 97;

-- name: CurrentCandidatePerformance :one
SELECT a.payload,encode(a.content_sha256,'hex') AS content_hash,r.id::text AS run_id
FROM analysis_model_channels c
JOIN analysis_publications p ON p.dataset_id=c.dataset_id AND p.scope_key=$2 AND p.metric_family='performance_candidate'
JOIN dirty_scopes d ON d.dataset_id=p.dataset_id AND d.scope_key=p.scope_key AND d.generation=d.clean_generation AND p.generation=d.generation
JOIN analysis_runs r ON r.dataset_id=p.dataset_id AND r.id=p.published_run_id AND r.status='SUCCEEDED'
JOIN analysis_artifacts a ON a.dataset_id=r.dataset_id AND a.run_id=r.id AND a.artifact_key='performance_state'
JOIN datasets x ON x.id=c.dataset_id AND x.retired_at IS NULL
WHERE c.dataset_id=$1 AND c.channel='candidate' AND c.stack_id=$3 AND c.config_sha256=decode($4::text,'hex')
AND c.extension_configs->>'rai.performance_curve/v1'=$5::text;

-- name: HistoricalPerformance :one
SELECT a.payload,encode(a.content_sha256,'hex') AS content_hash,r.id::text AS run_id
FROM analysis_runs r JOIN analysis_artifacts a ON a.dataset_id=r.dataset_id AND a.run_id=r.id AND a.artifact_key='performance_state'
WHERE r.dataset_id=$1 AND r.id=$2 AND r.algorithm_name='rai.performance_curve' AND r.algorithm_version='v1' AND r.status='SUCCEEDED';

-- name: ComparablePerformanceHistory :one
SELECT a.payload,encode(a.content_sha256,'hex') AS content_hash
FROM analysis_runs r JOIN analysis_artifacts a ON a.dataset_id=r.dataset_id AND a.run_id=r.id AND a.artifact_key='performance_state'
WHERE r.dataset_id=$1 AND r.scope_key=$2 AND r.algorithm_name='rai.performance_curve' AND r.status='SUCCEEDED'
AND r.started_at>=$3 AND r.started_at<$4 ORDER BY r.started_at DESC,r.id DESC LIMIT 1;
