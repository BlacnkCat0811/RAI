-- +goose Up
-- Expand-only. Daily facts share health_samples, entity_tombstones,
-- sync_streams, sync_batches, entity_changes, change_events and dirty_scopes.
CREATE TABLE daily_health_coverage (
    dataset_id uuid NOT NULL,
    stream_id uuid NOT NULL,
    bootstrap_start_us bigint NOT NULL,
    last_discovery_at_us bigint NOT NULL,
    last_sync_at timestamptz NOT NULL DEFAULT now(),
    last_fact_at_us bigint,
    bootstrap_complete boolean NOT NULL,
    has_more boolean NOT NULL,
    sample_count integer NOT NULL CHECK (sample_count BETWEEN 0 AND 128),
    deleted_count integer NOT NULL CHECK (deleted_count BETWEEN 0 AND 128),
    source_context jsonb NOT NULL,
    PRIMARY KEY(dataset_id, stream_id),
    FOREIGN KEY(dataset_id, stream_id) REFERENCES sync_streams(dataset_id, id)
);
-- No Down: do not discard facts or synchronization evidence.
