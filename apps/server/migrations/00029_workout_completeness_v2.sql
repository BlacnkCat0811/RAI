-- +goose Up
-- UA1-R3B: completeness v2 (workout-sync-completeness/v2) adds the frozen
-- source-policy inventory identity to the same immutable control-plane lineage.
-- v1 rows keep protocol_version 1 and leave the policy columns null, so v1
-- semantics are never reinterpreted. Exclusions live inside the immutable
-- manifest row; no duplicate raw Workout table is created.
ALTER TABLE workout_sync_completeness
 ADD COLUMN protocol_version integer NOT NULL DEFAULT 1,
 ADD COLUMN source_policy_version text,
 ADD COLUMN source_policy_sha256 text,
 ADD COLUMN classification_version integer,
 ADD COLUMN inventory_summary jsonb,
 ADD COLUMN exclusions jsonb;
ALTER TABLE workout_sync_completeness
 ADD CONSTRAINT workout_completeness_protocol_version CHECK(protocol_version IN (1,2)),
 ADD CONSTRAINT workout_completeness_v1_identity CHECK(protocol_version<>1 OR (
  source_policy_version IS NULL AND source_policy_sha256 IS NULL AND
  classification_version IS NULL AND inventory_summary IS NULL AND exclusions IS NULL)),
 ADD CONSTRAINT workout_completeness_v2_identity CHECK(protocol_version<>2 OR (
  source_policy_version IS NOT NULL AND length(source_policy_version) BETWEEN 1 AND 128 AND
  source_policy_sha256 ~ '^[0-9a-f]{64}$' AND
  classification_version IS NOT NULL AND classification_version>0 AND
  inventory_summary IS NOT NULL AND jsonb_typeof(inventory_summary)='object' AND octet_length(inventory_summary::text)<=4096 AND
  exclusions IS NOT NULL AND jsonb_typeof(exclusions)='array' AND jsonb_array_length(exclusions)<=256 AND octet_length(exclusions::text)<=1048576));

-- +goose Down
ALTER TABLE workout_sync_completeness DROP CONSTRAINT workout_completeness_v2_identity;
ALTER TABLE workout_sync_completeness DROP CONSTRAINT workout_completeness_v1_identity;
ALTER TABLE workout_sync_completeness DROP CONSTRAINT workout_completeness_protocol_version;
ALTER TABLE workout_sync_completeness
 DROP COLUMN exclusions,
 DROP COLUMN inventory_summary,
 DROP COLUMN classification_version,
 DROP COLUMN source_policy_sha256,
 DROP COLUMN source_policy_version,
 DROP COLUMN protocol_version;
