-- +goose Up
-- Quantity-series detail is a child of one already committed quantity sample.
-- Chunks are immutable staging rows; only the sealed row is the visible,
-- completeness-marked series. Parent deletion cascades detail so a late chunk
-- cannot resurrect a deleted HealthKit UUID.
CREATE TABLE series_uploads (
    dataset_id uuid NOT NULL,
    series_id uuid NOT NULL,
    sample_type text NOT NULL,
    sample_uuid uuid NOT NULL,
    parent_start_us bigint NOT NULL,
    parent_end_us bigint NOT NULL CHECK (parent_end_us >= parent_start_us),
    dataset_epoch uuid NOT NULL,
    collector_epoch uuid NOT NULL,
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'sealed')),
    sealed_chunks integer NOT NULL DEFAULT 0 CHECK (sealed_chunks BETWEEN 0 AND 1000),
    total_points bigint NOT NULL DEFAULT 0 CHECK (total_points BETWEEN 0 AND 1000000),
    manifest_sha256 bytea NOT NULL CHECK (octet_length(manifest_sha256) = 32),
    chunk_count integer CHECK (chunk_count BETWEEN 1 AND 1000),
    receipt bytea,
    sealed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (dataset_id, series_id),
    UNIQUE (dataset_id, sample_type, sample_uuid),
    FOREIGN KEY (dataset_id, sample_type, sample_uuid)
        REFERENCES health_samples(dataset_id, sample_type, sample_uuid) ON DELETE CASCADE,
    CHECK ((state = 'sealed') = (receipt IS NOT NULL AND sealed_at IS NOT NULL AND chunk_count IS NOT NULL)),
    CHECK (chunk_count IS NULL OR chunk_count = sealed_chunks)
);

CREATE TABLE series_chunks (
    dataset_id uuid NOT NULL,
    series_id uuid NOT NULL,
    chunk_index integer NOT NULL CHECK (chunk_index BETWEEN 0 AND 999),
    point_count integer NOT NULL CHECK (point_count BETWEEN 1 AND 5000),
    min_start_us bigint NOT NULL,
    max_end_us bigint NOT NULL CHECK (max_end_us >= min_start_us),
    wire_sha256 bytea NOT NULL CHECK (octet_length(wire_sha256) = 32),
    points_sha256 bytea NOT NULL CHECK (octet_length(points_sha256) = 32),
    -- Bounded canonical point array; source order is preserved and never sorted.
    points jsonb NOT NULL CHECK (jsonb_typeof(points) = 'array'),
    receipt bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (dataset_id, series_id, chunk_index),
    FOREIGN KEY (dataset_id, series_id) REFERENCES series_uploads(dataset_id, series_id) ON DELETE CASCADE
);

-- No Down: immutable chunk bytes and completeness receipts cannot be dropped
-- as an application rollback.
