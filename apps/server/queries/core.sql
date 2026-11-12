-- name: Authenticate :one
SELECT v.dataset_id, v.id, v.can_collect, (to_jsonb(d)->>'retired_at' IS NOT NULL)::boolean AS dataset_retired FROM devices v JOIN datasets d ON d.id=v.dataset_id
WHERE v.token_sha256 = $1 AND v.revoked_at IS NULL;

-- name: Dataset :one
SELECT id, epoch, service_id, timezone, commit_rev FROM datasets WHERE id = $1;

-- name: Batch :one
SELECT envelope_sha256, envelope, state, receipt, page_id FROM sync_batches
WHERE dataset_id = $1 AND batch_id = $2;

-- name: Series :one
SELECT series_id, sample_type, sample_uuid, parent_start_us, parent_end_us,
    dataset_epoch, collector_epoch, state, sealed_chunks, total_points, manifest_sha256, chunk_count, receipt
FROM series_uploads WHERE dataset_id = $1 AND series_id = $2;

-- name: SeriesForUpdate :one
SELECT series_id, sample_type, sample_uuid, parent_start_us, parent_end_us,
    dataset_epoch, collector_epoch, state, sealed_chunks, total_points, manifest_sha256, chunk_count, receipt
FROM series_uploads WHERE dataset_id = $1 AND series_id = $2 FOR UPDATE;

-- name: SeriesByParent :one
SELECT series_id, sample_type, sample_uuid, parent_start_us, parent_end_us,
    dataset_epoch, collector_epoch, state, sealed_chunks, total_points, manifest_sha256, chunk_count, receipt
FROM series_uploads WHERE dataset_id = $1 AND sample_type = $2 AND sample_uuid = $3;

-- name: SeriesChunk :one
SELECT point_count, wire_sha256, points_sha256, receipt FROM series_chunks
WHERE dataset_id = $1 AND series_id = $2 AND chunk_index = $3;

-- name: Stream :one
SELECT id, collector_epoch, sample_type, query_fingerprint, last_client_seq, last_batch_id
FROM sync_streams WHERE dataset_id = $1 AND id = $2;

-- name: Job :one
SELECT id, kind, scope_key, target_generation, state, attempts, last_error_code
FROM jobs WHERE dataset_id = $1 AND id = $2;

-- name: ClaimJob :one
WITH dataset AS MATERIALIZED (
 SELECT d.id FROM datasets d WHERE d.retired_at IS NULL AND EXISTS
 (SELECT 1 FROM jobs WHERE dataset_id=d.id AND state='queued' AND run_after<=now() AND kind=ANY(sqlc.arg(kinds)::text[])
 AND (kind<>'analysis.model_stack_v2' OR (left(scope_key,length(sqlc.arg(model_day_prefix)::text))=sqlc.arg(model_day_prefix)::text
 AND EXISTS(SELECT 1 FROM analysis_model_channels c WHERE c.dataset_id=jobs.dataset_id AND c.stack_id=split_part(jobs.scope_key,'/',2) AND c.channel IN ('active','candidate')))))
 ORDER BY d.id FOR UPDATE OF d SKIP LOCKED LIMIT 1
), candidate AS (
    SELECT id FROM jobs candidate
    WHERE dataset_id IN (SELECT id FROM dataset) AND state = 'queued' AND run_after <= now() AND kind = ANY(sqlc.arg(kinds)::text[])
    AND (kind<>'analysis.model_stack_v2' OR (left(scope_key,length(sqlc.arg(model_day_prefix)::text))=sqlc.arg(model_day_prefix)::text
     AND EXISTS(SELECT 1 FROM analysis_model_channels c WHERE c.dataset_id=candidate.dataset_id AND c.stack_id=split_part(candidate.scope_key,'/',2) AND c.channel IN ('active','candidate'))))
    AND NOT EXISTS (SELECT 1 FROM jobs newer WHERE newer.dataset_id=candidate.dataset_id AND newer.kind=candidate.kind AND newer.scope_key=candidate.scope_key AND newer.target_generation>candidate.target_generation AND newer.state IN ('queued','running'))
    ORDER BY CASE kind WHEN 'analysis.workout' THEN 0 WHEN 'analysis.training_state' THEN 2 WHEN 'training.plan' THEN 3 ELSE 1 END, run_after, id FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE jobs j SET state = 'running', lease_until = now() + interval '60 seconds',
    fencing_token = fencing_token + 1, attempts = attempts + 1
FROM candidate c WHERE j.id = c.id
RETURNING j.id, j.dataset_id, j.kind, j.scope_key, j.target_generation, j.fencing_token, j.payload;

-- name: RequeueExpired :many
WITH dataset AS MATERIALIZED (
 SELECT d.id FROM datasets d WHERE d.retired_at IS NULL AND EXISTS
 (SELECT 1 FROM jobs WHERE dataset_id=d.id AND state='running' AND lease_until<=now())
 ORDER BY d.id FOR UPDATE OF d SKIP LOCKED LIMIT 1
), expired AS (
    SELECT id FROM jobs WHERE dataset_id IN (SELECT id FROM dataset) AND state = 'running' AND lease_until <= now()
    ORDER BY lease_until, id FOR UPDATE SKIP LOCKED LIMIT 20
)
UPDATE jobs j SET state = CASE WHEN attempts >= 8 THEN 'failed' ELSE 'queued' END,
    lease_until = NULL, run_after = now() + interval '5 seconds', last_error_code = 'lease_expired'
FROM expired e WHERE j.id = e.id RETURNING j.id, j.fencing_token;

-- name: Page :one
SELECT page_id, stream_id, dataset_epoch, collector_epoch, query_fingerprint,
    fragment_count, first_client_seq, committed_fragments, manifest_sha256, receipt
FROM sync_pages WHERE dataset_id = $1 AND page_id = $2;

-- name: BatchSequenceOccupied :one
SELECT EXISTS(SELECT 1 FROM sync_batches WHERE dataset_id=$1 AND stream_id=$2 AND client_seq=$3);

-- name: LegacyPageExists :one
SELECT EXISTS(SELECT 1 FROM sync_batches WHERE dataset_id=sqlc.arg(dataset_id) AND page_id IS NULL AND envelope->>'pageID'=sqlc.arg(page_id)::text);

-- name: OpenPageExists :one
SELECT EXISTS(SELECT 1 FROM sync_pages WHERE dataset_id=$1 AND stream_id=$2 AND receipt IS NULL);

-- name: TombstoneExists :one
SELECT EXISTS(SELECT 1 FROM entity_tombstones WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3);

-- name: SampleIdentity :one
SELECT event_start,event_end,content_sha256 FROM health_samples WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3;

-- name: WorkoutIdentity :one
SELECT event_start,event_end,content_sha256 FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2;

-- name: BudgetClock :one
SELECT clock_timestamp()::timestamptz AS admitted_at;

-- name: TryRequestBudget :one
INSERT INTO sync_request_budgets(dataset_id, device_id, lane, available_at)
VALUES (sqlc.arg(dataset_id), sqlc.narg(device_id), sqlc.arg(lane),
    sqlc.arg(admitted_at)::timestamptz + sqlc.arg(interval_us)::bigint * interval '1 microsecond')
ON CONFLICT (dataset_id, lane, device_id) DO UPDATE
SET available_at = GREATEST(sync_request_budgets.available_at, sqlc.arg(admitted_at)::timestamptz)
    + sqlc.arg(interval_us)::bigint * interval '1 microsecond'
WHERE sync_request_budgets.available_at <= sqlc.arg(admitted_at)::timestamptz
    + (sqlc.arg(burst)::integer - 1) * sqlc.arg(interval_us)::bigint * interval '1 microsecond'
RETURNING available_at;

-- name: RequestBudgetTime :one
SELECT available_at FROM sync_request_budgets
WHERE dataset_id = sqlc.arg(dataset_id) AND lane = sqlc.arg(lane)
    AND (device_id = sqlc.narg(device_id)::uuid OR (device_id IS NULL AND sqlc.narg(device_id)::uuid IS NULL));

-- name: EnsureUploadSlots :exec
INSERT INTO sync_upload_slots(dataset_id, slot) VALUES ($1, 0), ($1, 1)
ON CONFLICT (dataset_id, slot) DO NOTHING;

-- name: LockUploadSlots :many
SELECT slot, device_id, token, expires_at FROM sync_upload_slots
WHERE dataset_id = $1 ORDER BY slot FOR UPDATE;

-- name: AssignUploadSlot :one
UPDATE sync_upload_slots SET device_id = sqlc.arg(device_id), token = sqlc.arg(token),
    expires_at = clock_timestamp() + interval '30 seconds'
WHERE dataset_id = sqlc.arg(dataset_id) AND slot = sqlc.arg(slot)
RETURNING expires_at;

-- name: ReleaseUploadSlot :exec
UPDATE sync_upload_slots SET device_id = NULL, token = NULL, expires_at = '-infinity'
WHERE dataset_id = sqlc.arg(dataset_id) AND device_id = sqlc.arg(device_id) AND token = sqlc.arg(token);

-- name: LockUploadLease :one
SELECT slot FROM sync_upload_slots
WHERE dataset_id = sqlc.arg(dataset_id) AND device_id = sqlc.arg(device_id)
    AND token = sqlc.arg(token) FOR UPDATE;

-- name: UploadLeaseValid :one
SELECT EXISTS(SELECT 1 FROM sync_upload_slots
WHERE dataset_id = sqlc.arg(dataset_id) AND device_id = sqlc.arg(device_id)
    AND token = sqlc.arg(token) AND expires_at > clock_timestamp());
