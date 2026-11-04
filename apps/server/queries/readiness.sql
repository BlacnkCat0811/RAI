-- name: ReadinessWorkouts :many
-- Only identities, completion time and the published M2 artifact; no HR/pace.
SELECT w.workout_uuid::text,w.event_start,w.event_end,encode(w.content_sha256,'hex') AS fact_hash,
 COALESCE(p.published_run_id::text,'')::text AS load_run,
 a.payload,COALESCE(encode(a.content_sha256,'hex'),'')::text AS load_hash
FROM workouts w
LEFT JOIN dirty_scopes d ON d.dataset_id=w.dataset_id AND d.scope_key=$2||w.workout_uuid::text
LEFT JOIN analysis_publications p ON p.dataset_id=d.dataset_id AND p.scope_key=d.scope_key AND p.metric_family='workout_load_candidate'
 AND p.generation=d.generation AND d.clean_generation=d.generation
LEFT JOIN analysis_runs r ON r.dataset_id=p.dataset_id AND r.id=p.published_run_id AND r.status='SUCCEEDED'
LEFT JOIN analysis_artifacts a ON a.dataset_id=r.dataset_id AND a.run_id=r.id AND a.artifact_key='training_load'
WHERE w.dataset_id=$1 AND w.event_end>=$3 AND w.event_end<$4
 AND w.payload->'workout'->>'activityType'='37'
ORDER BY w.event_end,w.workout_uuid,p.day LIMIT 12289;
