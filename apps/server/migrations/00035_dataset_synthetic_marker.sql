-- +goose Up
-- Synthetic evidence is only valid for a dataset created and marked by the
-- isolated test harness. A CLI flag or gate file alone cannot make a real
-- dataset promotable.
ALTER TABLE datasets ADD COLUMN synthetic_test boolean NOT NULL DEFAULT false;
-- +goose Down
ALTER TABLE datasets DROP COLUMN synthetic_test;
