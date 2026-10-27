-- +goose Up
CREATE TABLE datasets (
    id uuid PRIMARY KEY,
    epoch uuid NOT NULL,
    service_id uuid NOT NULL,
    timezone text NOT NULL DEFAULT 'Asia/Shanghai',
    commit_rev bigint NOT NULL DEFAULT 0 CHECK (commit_rev >= 0),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE devices (
    dataset_id uuid NOT NULL REFERENCES datasets(id),
    id uuid NOT NULL,
    token_sha256 bytea NOT NULL UNIQUE CHECK (octet_length(token_sha256) = 32),
    can_collect boolean NOT NULL DEFAULT false,
    revoked_at timestamptz,
    PRIMARY KEY (dataset_id, id)
);
CREATE TABLE collector_leases (
    dataset_id uuid PRIMARY KEY REFERENCES datasets(id),
    device_id uuid NOT NULL,
    collector_epoch uuid NOT NULL,
    FOREIGN KEY (dataset_id, device_id) REFERENCES devices(dataset_id, id),
    UNIQUE (dataset_id, collector_epoch)
);
CREATE TABLE sync_streams (
    dataset_id uuid NOT NULL REFERENCES datasets(id),
    id uuid NOT NULL,
    collector_epoch uuid NOT NULL,
    sample_type text NOT NULL,
    query_fingerprint text NOT NULL CHECK (query_fingerprint ~ '^sha256:[0-9a-f]{64}$'),
    last_client_seq bigint NOT NULL DEFAULT 0 CHECK (last_client_seq >= 0),
    last_batch_id uuid,
    PRIMARY KEY (dataset_id, id),
    UNIQUE (dataset_id, collector_epoch, sample_type, query_fingerprint),
    CHECK ((last_client_seq = 0) = (last_batch_id IS NULL))
);
CREATE TABLE sync_batches (
    dataset_id uuid NOT NULL,
    batch_id uuid NOT NULL,
    stream_id uuid NOT NULL,
    client_seq bigint NOT NULL CHECK (client_seq > 0),
    dataset_epoch uuid NOT NULL,
    envelope_sha256 bytea NOT NULL CHECK (octet_length(envelope_sha256) = 32),
    payload_sha256 bytea NOT NULL CHECK (octet_length(payload_sha256) = 32),
    envelope jsonb NOT NULL,
    event_count integer NOT NULL CHECK (event_count BETWEEN 0 AND 1000),
    state text NOT NULL DEFAULT 'registered' CHECK (state IN ('registered','committed')),
    receipt bytea,
    committed_at timestamptz,
    PRIMARY KEY (dataset_id, batch_id),
    UNIQUE (dataset_id, stream_id, client_seq),
    FOREIGN KEY (dataset_id, stream_id) REFERENCES sync_streams(dataset_id, id),
    CHECK ((state = 'committed') = (receipt IS NOT NULL AND committed_at IS NOT NULL))
);
CREATE TABLE health_samples (
    dataset_id uuid NOT NULL REFERENCES datasets(id),
    sample_type text NOT NULL,
    sample_uuid uuid NOT NULL,
    event_start timestamptz NOT NULL,
    event_end timestamptz NOT NULL CHECK (event_end >= event_start),
    value_numeric double precision,
    value_category integer,
    unit text,
    source_bundle_id text NOT NULL,
    content_sha256 bytea NOT NULL CHECK (octet_length(content_sha256) = 32),
    entity_revision bigint NOT NULL CHECK (entity_revision > 0),
    first_received_at timestamptz NOT NULL DEFAULT now(),
    available_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}',
    PRIMARY KEY (dataset_id, sample_type, sample_uuid),
    CHECK ((value_numeric IS NOT NULL) <> (value_category IS NOT NULL)),
    CHECK (value_numeric IS NULL OR (value_numeric > '-Infinity'::float8 AND value_numeric < 'Infinity'::float8))
);
CREATE INDEX health_samples_type_time ON health_samples(dataset_id, sample_type, event_start, sample_uuid)
    INCLUDE (event_end, value_numeric, unit);
CREATE TABLE entity_tombstones (
    dataset_id uuid NOT NULL REFERENCES datasets(id),
    sample_type text NOT NULL,
    sample_uuid uuid NOT NULL,
    deleted_rev bigint NOT NULL CHECK (deleted_rev > 0),
    previous_start timestamptz,
    previous_end timestamptz,
    reason text NOT NULL,
    PRIMARY KEY (dataset_id, sample_type, sample_uuid)
);
CREATE TABLE entity_changes (
    dataset_id uuid NOT NULL,
    batch_id uuid NOT NULL,
    ordinal integer NOT NULL,
    commit_rev bigint NOT NULL,
    sample_type text NOT NULL,
    sample_uuid uuid NOT NULL,
    operation text NOT NULL CHECK (operation IN ('upsert','delete')),
    previous_start timestamptz,
    previous_end timestamptz,
    new_start timestamptz,
    new_end timestamptz,
    PRIMARY KEY (dataset_id, batch_id, ordinal),
    FOREIGN KEY (dataset_id, batch_id) REFERENCES sync_batches(dataset_id, batch_id)
);
CREATE TABLE change_events (
    dataset_id uuid NOT NULL REFERENCES datasets(id),
    commit_rev bigint NOT NULL CHECK (commit_rev > 0),
    ordinal integer NOT NULL CHECK (ordinal >= 0),
    event_id uuid NOT NULL UNIQUE,
    entity_kind text NOT NULL,
    entity_key text NOT NULL,
    operation text NOT NULL,
    payload jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (dataset_id, commit_rev, ordinal)
);
CREATE TABLE dirty_scopes (
    dataset_id uuid NOT NULL REFERENCES datasets(id),
    scope_key text NOT NULL,
    generation bigint NOT NULL CHECK (generation > 0),
    dirty_from timestamptz NOT NULL,
    dirty_through timestamptz NOT NULL CHECK (dirty_through >= dirty_from),
    PRIMARY KEY (dataset_id, scope_key)
);
CREATE TABLE jobs (
    id uuid PRIMARY KEY,
    dataset_id uuid NOT NULL REFERENCES datasets(id),
    kind text NOT NULL,
    scope_key text NOT NULL,
    target_generation bigint NOT NULL,
    state text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued','running','succeeded','failed','repair_required')),
    run_after timestamptz NOT NULL DEFAULT now(),
    lease_until timestamptz,
    fencing_token bigint NOT NULL DEFAULT 0,
    attempts integer NOT NULL DEFAULT 0,
    payload jsonb NOT NULL DEFAULT '{}',
    last_error_code text,
    UNIQUE (dataset_id, kind, scope_key, target_generation),
    CHECK ((state = 'running') = (lease_until IS NOT NULL))
);
CREATE INDEX jobs_ready ON jobs(run_after, id) WHERE state = 'queued';
CREATE INDEX jobs_expired ON jobs(lease_until, id) WHERE state = 'running';
CREATE TABLE job_attempts (
    job_id uuid NOT NULL REFERENCES jobs(id),
    fencing_token bigint NOT NULL,
    started_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    result_code text,
    PRIMARY KEY (job_id, fencing_token)
);

-- Deliberately no Down: facts/receipts cannot be dropped as an application rollback.
