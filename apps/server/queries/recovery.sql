-- name: RecoveryDailyFacts :many
SELECT sample_type,sample_uuid::text,event_start,event_end,value_numeric,value_category,COALESCE(unit,'') AS unit,
 source_bundle_id,encode(content_sha256,'hex') AS hash,entity_revision,first_received_at,metadata
 FROM health_samples WHERE dataset_id=$1 AND sample_type=ANY($2::text[])
 AND event_end>=$3 AND event_end<$4 ORDER BY sample_type,event_end,sample_uuid LIMIT 8193;

-- name: RecoveryDailyTombstones :many
SELECT t.sample_type,t.sample_uuid::text,t.deleted_rev,t.previous_start,t.previous_end,
 COALESCE((extract(epoch FROM (SELECT min(c.created_at) FROM change_events c WHERE c.dataset_id=t.dataset_id AND c.commit_rev=t.deleted_rev))*1000000)::bigint,1)::bigint AS known_at_us
 FROM entity_tombstones t WHERE t.dataset_id=$1 AND t.sample_type=ANY($2::text[])
 AND t.previous_end>=$3 AND t.previous_start<$4 ORDER BY t.sample_type,t.previous_end,t.sample_uuid LIMIT 8193;
