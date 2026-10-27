-- +goose Up
-- Durable staging for a Workout whose complete source object cannot fit the
-- inline single-line limit. No Workout fact exists until seal; chunks are
-- immutable and the package row is the only completeness marker.
CREATE TABLE workout_packages (
    dataset_id uuid NOT NULL REFERENCES datasets(id),
    workout_uuid uuid NOT NULL,
    dataset_epoch uuid NOT NULL,
    collector_epoch uuid NOT NULL,
    event_start timestamptz NOT NULL,
    event_end timestamptz NOT NULL CHECK (event_end >= event_start),
    -- Canonical core Event bytes without statistics/events/activities.
    core bytea NOT NULL,
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'sealed')),
    committed_chunks integer NOT NULL DEFAULT 0 CHECK (committed_chunks BETWEEN 0 AND 100),
    total_details bigint NOT NULL DEFAULT 0 CHECK (total_details BETWEEN 0 AND 20000),
    committed_activities integer NOT NULL DEFAULT 0 CHECK (committed_activities BETWEEN 0 AND 128),
    committed_statistics integer NOT NULL DEFAULT 0 CHECK (committed_statistics BETWEEN 0 AND 128),
    committed_events integer NOT NULL DEFAULT 0 CHECK (committed_events BETWEEN 0 AND 256),
    last_statistic_type text NOT NULL DEFAULT '',
    manifest_sha256 bytea NOT NULL CHECK (octet_length(manifest_sha256) = 32),
    chunk_count integer CHECK (chunk_count BETWEEN 1 AND 100),
    receipt bytea,
    sealed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (dataset_id, workout_uuid),
    CHECK ((state = 'sealed') = (receipt IS NOT NULL AND sealed_at IS NOT NULL AND chunk_count IS NOT NULL)),
    CHECK (chunk_count IS NULL OR chunk_count = committed_chunks)
);

CREATE TABLE workout_package_chunks (
    dataset_id uuid NOT NULL,
    workout_uuid uuid NOT NULL,
    chunk_index integer NOT NULL CHECK (chunk_index BETWEEN 0 AND 99),
    detail_count integer NOT NULL CHECK (detail_count BETWEEN 1 AND 1000),
    wire_sha256 bytea NOT NULL CHECK (octet_length(wire_sha256) = 32),
    details_sha256 bytea NOT NULL CHECK (octet_length(details_sha256) = 32),
    -- Canonical detail array bytes, never reordered or reserialized by storage.
    details bytea NOT NULL,
    receipt bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (dataset_id, workout_uuid, chunk_index),
    FOREIGN KEY (dataset_id, workout_uuid) REFERENCES workout_packages(dataset_id, workout_uuid) ON DELETE CASCADE
);

-- A Workout can be deleted through the normal fact path or an authorized
-- maintenance statement; either way its pending/sealed package staging must
-- not orphan. The API role already holds DELETE on workout_packages.
-- +goose StatementBegin
CREATE FUNCTION rai_delete_workout_packages() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM workout_packages WHERE dataset_id=OLD.dataset_id AND workout_uuid=OLD.workout_uuid;
    RETURN OLD;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER delete_workout_packages AFTER DELETE ON workouts
    FOR EACH ROW EXECUTE FUNCTION rai_delete_workout_packages();

-- No Down: immutable staged detail and sealed receipts cannot be dropped as an
-- application rollback.
