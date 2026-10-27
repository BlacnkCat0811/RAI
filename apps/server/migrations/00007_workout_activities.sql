-- +goose Up
-- Additive projection only; never backfill source activities for v1 facts.
CREATE TABLE workout_activities (
    dataset_id uuid NOT NULL,
    workout_uuid uuid NOT NULL,
    ordinal integer NOT NULL CHECK (ordinal BETWEEN 0 AND 127),
    activity_uuid uuid NOT NULL,
    event_start timestamptz NOT NULL,
    event_end timestamptz CHECK (event_end >= event_start),
    duration_us bigint NOT NULL CHECK (duration_us >= 0),
    -- Bounded configuration, metadata, statistics and events, retained together.
    payload jsonb NOT NULL,
    PRIMARY KEY (dataset_id, workout_uuid, ordinal),
    UNIQUE (dataset_id, workout_uuid, activity_uuid),
    FOREIGN KEY (dataset_id, workout_uuid) REFERENCES workouts(dataset_id, workout_uuid) ON DELETE CASCADE
);

-- No Down: immutable facts cannot be dropped as a code rollback.
