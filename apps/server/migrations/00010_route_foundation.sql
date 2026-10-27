-- +goose Up
-- Preserve known collector-device provenance for new registrations. Older
-- rows may not contain enough information to backfill without guessing.
ALTER TABLE sync_streams ADD COLUMN device_id uuid;
ALTER TABLE sync_streams ADD CONSTRAINT sync_streams_device_fk FOREIGN KEY (dataset_id, device_id) REFERENCES devices(dataset_id, id);
ALTER TABLE sync_batches ADD COLUMN device_id uuid;
ALTER TABLE sync_batches ADD CONSTRAINT sync_batches_device_fk FOREIGN KEY (dataset_id, device_id) REFERENCES devices(dataset_id, id);
-- This is the collector that submitted the fact, not an assertion about the
-- physical HealthKit source (which may be another device).
ALTER TABLE health_samples ADD COLUMN ingest_device_id uuid;
ALTER TABLE health_samples ADD CONSTRAINT health_samples_ingest_device_fk FOREIGN KEY (dataset_id, ingest_device_id) REFERENCES devices(dataset_id, id);
ALTER TABLE workouts ADD COLUMN ingest_device_id uuid;
ALTER TABLE workouts ADD CONSTRAINT workouts_ingest_device_fk FOREIGN KEY (dataset_id, ingest_device_id) REFERENCES devices(dataset_id, id);
ALTER TABLE jobs ADD CONSTRAINT jobs_attempts_nonnegative CHECK (attempts >= 0);
ALTER TABLE jobs ADD CONSTRAINT jobs_fencing_nonnegative CHECK (fencing_token >= 0);

-- The already-locked collector lease determines the registering device. Keep
-- pre-10 application SQL usable for historical-schema upgrade fixtures.
-- +goose StatementBegin
CREATE FUNCTION bind_sync_stream_device() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    SELECT device_id INTO NEW.device_id FROM collector_leases
    WHERE dataset_id = NEW.dataset_id AND collector_epoch = NEW.collector_epoch;
    IF NEW.device_id IS NULL THEN RAISE EXCEPTION 'collector device unavailable'; END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER sync_stream_device BEFORE INSERT ON sync_streams FOR EACH ROW EXECUTE FUNCTION bind_sync_stream_device();

-- +goose StatementBegin
CREATE FUNCTION bind_sync_batch_device() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    SELECT coalesce(s.device_id, l.device_id) INTO NEW.device_id
    FROM sync_streams s LEFT JOIN collector_leases l
      ON l.dataset_id=s.dataset_id AND l.collector_epoch=s.collector_epoch
    WHERE s.dataset_id=NEW.dataset_id AND s.id=NEW.stream_id;
    IF NEW.device_id IS NULL THEN RAISE EXCEPTION 'collector device unavailable'; END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER sync_batch_device BEFORE INSERT ON sync_batches FOR EACH ROW EXECUTE FUNCTION bind_sync_batch_device();

-- +goose StatementBegin
CREATE FUNCTION bind_fact_ingest_device() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    SELECT device_id INTO NEW.ingest_device_id FROM collector_leases WHERE dataset_id=NEW.dataset_id;
    IF NEW.ingest_device_id IS NULL THEN RAISE EXCEPTION 'collector device unavailable'; END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER health_sample_ingest_device BEFORE INSERT ON health_samples FOR EACH ROW EXECUTE FUNCTION bind_fact_ingest_device();
CREATE TRIGGER workout_ingest_device BEFORE INSERT ON workouts FOR EACH ROW EXECUTE FUNCTION bind_fact_ingest_device();

-- Storage foundation only: route upload, seal, deletion barriers and publication
-- must be implemented together in the future protocol. Pending rows are not
-- published route facts.
-- Associations may arrive after an immutable sample. A sample can exist
-- without an association; each associated sample has one workout in P2.
CREATE TABLE workout_samples (
    dataset_id uuid NOT NULL,
    sample_type text NOT NULL,
    sample_uuid uuid NOT NULL,
    workout_uuid uuid NOT NULL,
    PRIMARY KEY (dataset_id, sample_type, sample_uuid),
    FOREIGN KEY (dataset_id, sample_type, sample_uuid)
        REFERENCES health_samples(dataset_id, sample_type, sample_uuid) ON DELETE CASCADE,
    FOREIGN KEY (dataset_id, workout_uuid)
        REFERENCES workouts(dataset_id, workout_uuid) ON DELETE CASCADE
);
CREATE INDEX workout_samples_workout ON workout_samples(dataset_id, workout_uuid, sample_type, sample_uuid);

CREATE TABLE workout_routes (
    dataset_id uuid NOT NULL,
    route_uuid uuid NOT NULL,
    workout_uuid uuid NOT NULL,
    source_bundle_id text NOT NULL CHECK (source_bundle_id <> ''),
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'sealed')),
    created_at timestamptz NOT NULL DEFAULT now(),
    sealed_at timestamptz,
    receipt bytea,
    PRIMARY KEY (dataset_id, route_uuid),
    FOREIGN KEY (dataset_id, workout_uuid) REFERENCES workouts(dataset_id, workout_uuid) ON DELETE CASCADE,
    CHECK ((state = 'sealed') = (sealed_at IS NOT NULL AND receipt IS NOT NULL))
);
CREATE INDEX workout_routes_workout ON workout_routes(dataset_id, workout_uuid, route_uuid);

-- The original validated canonical chunk and its digest remain the authority;
-- route_points are a query projection populated in the same chunk transaction.
CREATE TABLE route_chunks (
    dataset_id uuid NOT NULL,
    route_uuid uuid NOT NULL,
    chunk_index integer NOT NULL CHECK (chunk_index >= 0),
    point_count integer NOT NULL CHECK (point_count > 0),
    content_sha256 bytea NOT NULL CHECK (octet_length(content_sha256) = 32),
    points jsonb NOT NULL CHECK (jsonb_typeof(points) = 'array' AND jsonb_array_length(points) = point_count),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (dataset_id, route_uuid, chunk_index),
    FOREIGN KEY (dataset_id, route_uuid) REFERENCES workout_routes(dataset_id, route_uuid) ON DELETE CASCADE
);
CREATE TABLE route_points (
    dataset_id uuid NOT NULL,
    route_uuid uuid NOT NULL,
    chunk_index integer NOT NULL CHECK (chunk_index >= 0),
    sequence bigint NOT NULL CHECK (sequence >= 0),
    recorded_at timestamptz NOT NULL,
    latitude double precision NOT NULL CHECK (latitude BETWEEN -90 AND 90),
    longitude double precision NOT NULL CHECK (longitude BETWEEN -180 AND 180),
    altitude double precision CHECK (altitude > '-Infinity'::float8 AND altitude < 'Infinity'::float8),
    horizontal_accuracy double precision CHECK (horizontal_accuracy >= 0 AND horizontal_accuracy < 'Infinity'::float8),
    vertical_accuracy double precision CHECK (vertical_accuracy >= 0 AND vertical_accuracy < 'Infinity'::float8),
    speed double precision CHECK (speed >= 0 AND speed < 'Infinity'::float8),
    course double precision CHECK (course >= 0 AND course < 360),
    PRIMARY KEY (dataset_id, route_uuid, sequence),
    FOREIGN KEY (dataset_id, route_uuid, chunk_index) REFERENCES route_chunks(dataset_id, route_uuid, chunk_index) ON DELETE CASCADE
);
CREATE INDEX route_points_time ON route_points(dataset_id, route_uuid, recorded_at, sequence);

-- No Down: route facts and eventual receipts must not be destroyed by rollback.
