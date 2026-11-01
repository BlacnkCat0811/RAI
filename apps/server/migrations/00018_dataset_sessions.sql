-- +goose Up
-- Expand only. Existing facts, receipts, deletion barriers and history are untouched.
ALTER TABLE datasets ADD COLUMN retired_at timestamptz;
CREATE TABLE admin_operations (
    operation_id uuid PRIMARY KEY,
    operation_type text NOT NULL CHECK (operation_type = 'new_dataset'),
    source_dataset uuid NOT NULL UNIQUE REFERENCES datasets(id),
    target_dataset uuid NOT NULL UNIQUE REFERENCES datasets(id),
    actor text NOT NULL CHECK (length(actor) BETWEEN 1 AND 200),
    created_at timestamptz NOT NULL DEFAULT now(),
    committed_at timestamptz NOT NULL DEFAULT now(),
    status text NOT NULL CHECK (status = 'committed'),
    CHECK (source_dataset <> target_dataset)
);
-- +goose Down
-- Intentionally irreversible: retirement and administrative lineage must survive.
