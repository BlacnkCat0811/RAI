-- +goose Up
-- Protocol 3 receipts are append-only per bundle hash. The original design kept
-- one row per Workout, so an older exact bundle retry could no longer return its
-- original receipt and a later bundle could overwrite the earlier source bytes.
-- Keep existing rows, widen the identity to include the exact bundle digest,
-- and expose the newest receipt through a deterministic index.
ALTER TABLE workout_imports DROP CONSTRAINT workout_imports_pkey;
ALTER TABLE workout_imports DROP COLUMN updated_at;
ALTER TABLE workout_imports
    ADD PRIMARY KEY (dataset_id, workout_uuid, bundle_sha256);

CREATE INDEX workout_imports_latest
    ON workout_imports (dataset_id, workout_uuid, imported_at DESC, bundle_sha256);

-- No Down: historical bundle receipts and source documents are immutable facts.
