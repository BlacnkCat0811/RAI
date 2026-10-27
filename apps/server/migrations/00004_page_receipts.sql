-- +goose Up
-- No historical envelope is reinterpreted as a verified page. Pre-v4 batches
-- retain their original receipts and NULL page linkage.
CREATE TABLE sync_pages (
    dataset_id uuid NOT NULL,
    page_id uuid NOT NULL,
    stream_id uuid NOT NULL,
    dataset_epoch uuid NOT NULL,
    collector_epoch uuid NOT NULL,
    query_fingerprint text NOT NULL,
    fragment_count integer NOT NULL CHECK (fragment_count BETWEEN 1 AND 10000),
    first_client_seq bigint NOT NULL CHECK (first_client_seq > 0),
    committed_fragments integer NOT NULL DEFAULT 0,
    manifest_sha256 bytea NOT NULL CHECK (octet_length(manifest_sha256) = 32),
    receipt bytea,
    sealed_at timestamptz,
    PRIMARY KEY (dataset_id, page_id),
    FOREIGN KEY (dataset_id, stream_id) REFERENCES sync_streams(dataset_id, id),
    CHECK (committed_fragments BETWEEN 0 AND fragment_count),
    CHECK ((committed_fragments = fragment_count) = (receipt IS NOT NULL)),
    CHECK ((receipt IS NULL) = (sealed_at IS NULL))
);
-- One unfinished page per stream prevents another page consuming its next seq.
CREATE UNIQUE INDEX sync_pages_open ON sync_pages(dataset_id, stream_id) WHERE receipt IS NULL;
ALTER TABLE sync_batches ADD COLUMN page_id uuid, ADD COLUMN fragment_index integer;
ALTER TABLE sync_batches ADD CONSTRAINT sync_batches_page_fk
    FOREIGN KEY (dataset_id, page_id) REFERENCES sync_pages(dataset_id, page_id);
ALTER TABLE sync_batches ADD CONSTRAINT sync_batches_page_fields
    CHECK ((page_id IS NULL) = (fragment_index IS NULL) AND (fragment_index IS NULL OR fragment_index BETWEEN 0 AND 9999));
CREATE UNIQUE INDEX sync_batches_page_fragment ON sync_batches(dataset_id, page_id, fragment_index) WHERE page_id IS NOT NULL;

-- Reserve legacy page identities without interpreting their completeness.
-- This migration-only index build is not part of normal sync or fact scanning.
CREATE INDEX sync_batches_legacy_page ON sync_batches(dataset_id, (envelope->>'pageID')) WHERE page_id IS NULL;

-- Deliberately no Down: page and batch receipts are durable protocol records.
