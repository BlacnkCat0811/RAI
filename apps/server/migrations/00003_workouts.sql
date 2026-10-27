-- +goose Up
CREATE TABLE workouts (
    dataset_id uuid NOT NULL REFERENCES datasets(id),
    workout_uuid uuid NOT NULL,
    event_start timestamptz NOT NULL,
    event_end timestamptz NOT NULL CHECK (event_end >= event_start),
    activity_type bigint NOT NULL CHECK (activity_type BETWEEN 1 AND 2147483647),
    duration_us bigint NOT NULL CHECK (duration_us >= 0),
    source_bundle_id text NOT NULL,
    content_sha256 bytea NOT NULL CHECK (octet_length(content_sha256) = 32),
    entity_revision bigint NOT NULL CHECK (entity_revision > 0),
    first_received_at timestamptz NOT NULL DEFAULT now(),
    available_at timestamptz NOT NULL DEFAULT now(),
    -- Complete validated event: includes optional totals, metadata and child data.
    payload jsonb NOT NULL,
    PRIMARY KEY (dataset_id, workout_uuid)
);
CREATE INDEX workouts_time ON workouts(dataset_id, event_start DESC, workout_uuid DESC);
CREATE TABLE workout_statistics (
    dataset_id uuid NOT NULL,
    workout_uuid uuid NOT NULL,
    sample_type text NOT NULL,
    payload jsonb NOT NULL,
    PRIMARY KEY (dataset_id, workout_uuid, sample_type),
    FOREIGN KEY (dataset_id, workout_uuid) REFERENCES workouts(dataset_id, workout_uuid) ON DELETE CASCADE
);
CREATE TABLE workout_events (
    dataset_id uuid NOT NULL,
    workout_uuid uuid NOT NULL,
    ordinal integer NOT NULL CHECK (ordinal BETWEEN 0 AND 255),
    payload jsonb NOT NULL,
    PRIMARY KEY (dataset_id, workout_uuid, ordinal),
    FOREIGN KEY (dataset_id, workout_uuid) REFERENCES workouts(dataset_id, workout_uuid) ON DELETE CASCADE
);

-- No Down: immutable facts cannot be dropped as a code rollback.
