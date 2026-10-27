-- +goose Up
-- Operational admission state only: never part of the fact revision/changefeed.
-- NULL device_id denotes the shared dataset bucket. At most three rows per
-- dataset plus three per enrolled device; request paths never become keys.
CREATE TABLE sync_request_budgets (
    dataset_id uuid NOT NULL REFERENCES datasets(id),
    device_id uuid,
    lane text NOT NULL CHECK (lane IN ('read', 'write', 'upload')),
    available_at timestamptz NOT NULL,
    FOREIGN KEY (dataset_id, device_id) REFERENCES devices(dataset_id, id),
    UNIQUE NULLS NOT DISTINCT (dataset_id, lane, device_id)
);

-- Deliberately no Down: downgrade requires an explicit admission-policy decision.
