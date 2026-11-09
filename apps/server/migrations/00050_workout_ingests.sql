-- +goose Up
-- One row is the per-UUID acceptance identity, the exact decompressed payload
-- and the parse work item. dataset_id deliberately has no foreign key: the
-- acceptance transaction must never take a datasets row lock, so a first
-- request cannot queue behind a running fact import.
CREATE TABLE workout_ingests (
 dataset_id uuid NOT NULL,
 workout_uuid uuid NOT NULL,
 raw_json bytea,
 payload_bytes bigint NOT NULL CHECK (payload_bytes >= 0),
 device_id uuid NOT NULL,
 dataset_epoch uuid NOT NULL,
 collector_epoch uuid NOT NULL,
 state text NOT NULL CHECK (state IN ('accepted','committed','rejected')),
 accepted_at timestamptz NOT NULL,
 commit_revision bigint,
 commit_receipt jsonb,
 finished_at timestamptz,
 error_code text,
 attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
 next_attempt_at timestamptz NOT NULL,
 lease_until timestamptz,
 PRIMARY KEY (dataset_id, workout_uuid),
 CHECK ((state = 'accepted' AND finished_at IS NULL AND commit_revision IS NULL) OR
        (state = 'committed' AND finished_at IS NOT NULL AND commit_revision IS NOT NULL) OR
        (state = 'rejected' AND finished_at IS NOT NULL))
);
-- The worker claims due accepted rows only; committed/rejected history never
-- participates in the queue scan.
CREATE INDEX workout_ingests_due ON workout_ingests(next_attempt_at, dataset_id, workout_uuid)
 WHERE state = 'accepted';
CREATE INDEX workout_ingests_backlog ON workout_ingests(dataset_id)
 WHERE state = 'accepted';

-- +goose Down
DROP TABLE workout_ingests;
