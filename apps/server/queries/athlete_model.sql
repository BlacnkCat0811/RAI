-- name: AthleteModelCurrentArtifacts :many
-- Six bounded current slots (M2 has separate workout/state artifacts). Missing
-- artifacts remain visible as technical failures instead of disappearing in JOINs.
SELECT s.metric::text AS metric,COALESCE(p.published_run_id::text,'')::text AS run_id,
 COALESCE(p.scope_key,'')::text AS scope_key,COALESCE(p.generation,0)::bigint AS generation,
 COALESCE(r.algorithm_name,'')::text AS algorithm,COALESCE(r.algorithm_version,'')::text AS version,
 COALESCE(r.status,'')::text AS run_status,COALESCE(r.target_generation,0)::bigint AS run_generation,
 COALESCE((extract(epoch FROM r.started_at)*1000000)::bigint,0)::bigint AS started_us,
 COALESCE((extract(epoch FROM p.published_at)*1000000)::bigint,0)::bigint AS updated_us,
 COALESCE((extract(epoch FROM latest.event_end)*1000000)::bigint,0)::bigint AS event_end_us,
 COALESCE(x.generation=x.clean_generation AND p.generation=x.generation,false)::boolean AS clean,
 COALESCE(a.schema_version,'')::text AS artifact_schema,a.payload,
 COALESCE(encode(a.content_sha256,'hex'),'')::text AS content_hash,COALESCE(r.lineage,'{}'::jsonb)::jsonb AS lineage
FROM (SELECT value->>'metric' AS metric,value->>'key' AS key,value->>'scope' AS scope,value->>'family' AS family FROM jsonb_array_elements($2::jsonb)) s
LEFT JOIN LATERAL (
 SELECT w.workout_uuid,w.event_end FROM workouts w WHERE s.metric='loadWorkout' AND w.dataset_id=$1
 AND w.event_end<=$4::timestamptz AND w.payload->'workout'->>'activityType'='37'
 ORDER BY w.event_end DESC,w.workout_uuid DESC LIMIT 1
) latest ON true
LEFT JOIN LATERAL (
 SELECT p.* FROM analysis_publications p
 WHERE p.dataset_id=$1 AND p.metric_family=s.family AND p.day<=$3::date
 AND ((s.metric='loadWorkout' AND p.scope_key=s.scope||latest.workout_uuid::text) OR
 (s.metric<>'loadWorkout' AND p.scope_key=s.scope AND (s.metric NOT IN ('loadState','recovery') OR p.day=$3::date)))
 ORDER BY p.day DESC,p.published_at DESC,p.scope_key LIMIT 1
) p ON true
LEFT JOIN dirty_scopes x ON x.dataset_id=p.dataset_id AND x.scope_key=p.scope_key
LEFT JOIN analysis_runs r ON r.dataset_id=p.dataset_id AND r.id=p.published_run_id
LEFT JOIN analysis_artifacts a ON a.dataset_id=p.dataset_id AND a.run_id=p.published_run_id AND a.artifact_key=s.key
ORDER BY s.metric;

-- name: AthleteModelValidateRefs :many
-- Identity/hash lookups only: no health values, raw payloads, or history scans.
SELECT f.metric::text AS metric,COALESCE(bool_and(COALESCE(CASE WHEN f.kind='fact' THEN h.content_sha256 IS NOT NULL
 ELSE p.published_run_id::text=f.run AND x.generation=x.clean_generation AND p.generation=x.generation END,false)),false)::boolean AS current,
 bool_and(CASE WHEN f.kind='fact' THEN h.content_sha256 IS NULL OR encode(h.content_sha256,'hex')=f.hash
 ELSE p.published_run_id IS NULL OR (a.content_sha256 IS NOT NULL AND (f.hash='' OR encode(a.content_sha256,'hex')=f.hash)) END)::boolean AS hash_ok
FROM (SELECT value->>'metric' AS metric,value->>'kind' AS kind,value->>'id' AS id,value->>'type' AS type,value->>'scope' AS scope,value->>'run' AS run,value->>'key' AS key,value->>'hash' AS hash FROM jsonb_array_elements($2::jsonb)) f
LEFT JOIN health_samples h ON f.kind='fact' AND h.dataset_id=$1 AND h.sample_type=f.type AND h.sample_uuid=NULLIF(f.id,'')::uuid
LEFT JOIN analysis_publications p ON f.kind='model' AND p.dataset_id=$1 AND p.scope_key=f.scope AND p.published_run_id::text=f.run
LEFT JOIN dirty_scopes x ON x.dataset_id=p.dataset_id AND x.scope_key=p.scope_key
LEFT JOIN analysis_artifacts a ON a.dataset_id=$1 AND a.run_id=NULLIF(f.run,'')::uuid AND a.artifact_key=f.key
GROUP BY f.metric ORDER BY f.metric;
