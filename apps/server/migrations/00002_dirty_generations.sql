-- +goose Up
-- Keep generation history after successful analysis. Deleting a clean scope row
-- would reuse generation 1 and collide with immutable historical job identities.
ALTER TABLE dirty_scopes ADD COLUMN clean_generation bigint NOT NULL DEFAULT 0
    CHECK (clean_generation >= 0 AND clean_generation <= generation);
