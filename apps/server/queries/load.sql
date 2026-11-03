-- name: CurrentCandidateLoadArtifact :one
SELECT a.payload,encode(a.content_sha256,'hex') AS content_hash,p.published_run_id::text AS run_id
FROM analysis_model_channels c
JOIN analysis_publications p ON p.dataset_id=c.dataset_id AND p.scope_key=$2 AND p.day=$3::date
JOIN dirty_scopes d ON d.dataset_id=p.dataset_id AND d.scope_key=p.scope_key AND d.generation=d.clean_generation AND p.generation<=d.clean_generation
JOIN analysis_runs r ON r.dataset_id=p.dataset_id AND r.id=p.published_run_id AND r.status='SUCCEEDED'
JOIN analysis_artifacts a ON a.dataset_id=r.dataset_id AND a.run_id=r.id AND a.artifact_key=$4
JOIN datasets x ON x.id=c.dataset_id AND x.retired_at IS NULL
WHERE c.dataset_id=$1 AND c.channel='candidate' AND c.stack_id=$5 AND c.config_sha256=decode($6::text,'hex');
