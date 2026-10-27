-- +goose Up
-- Two reusable operational slots per dataset, independent of business revisions.
-- A random token fences expired requests; inactive rows are reused, never appended.
CREATE TABLE sync_upload_slots (
    dataset_id uuid NOT NULL REFERENCES datasets(id),
    slot smallint NOT NULL CHECK (slot IN (0, 1)),
    device_id uuid,
    token uuid,
    expires_at timestamptz NOT NULL DEFAULT '-infinity',
    PRIMARY KEY (dataset_id, slot),
    FOREIGN KEY (dataset_id, device_id) REFERENCES devices(dataset_id, id),
    CHECK ((device_id IS NULL) = (token IS NULL))
);

-- Deliberately no Down: old binaries must not bypass the upload fence.
