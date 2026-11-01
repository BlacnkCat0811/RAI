-- +goose Up
-- One Workout = one immutable bundle upload. The validated source document is
-- retained alongside the normalized facts so a future parser can reproduce the
-- projection without relying on the phone still having the HealthKit objects.
-- The receipt row is also the status endpoint's durable answer.
CREATE TABLE workout_imports (
    dataset_id uuid NOT NULL REFERENCES datasets(id),
    workout_uuid uuid NOT NULL,
    bundle_sha256 bytea NOT NULL CHECK (octet_length(bundle_sha256) = 32),
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload) = 'object'),
    receipt bytea NOT NULL,
    imported_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (dataset_id, workout_uuid),
    FOREIGN KEY (dataset_id, workout_uuid)
        REFERENCES workouts(dataset_id, workout_uuid) ON DELETE CASCADE
);

-- No Down: the raw source bundle and import receipt are immutable facts.
