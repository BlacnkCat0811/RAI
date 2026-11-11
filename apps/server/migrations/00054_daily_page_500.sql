-- +goose Up
-- Daily pages grow from 128 to 500 changes (handshake dailyMaxChanges). The
-- coverage row stores the latest page's counts, so its bounds follow. Expand
-- only: every existing row already satisfies the wider checks.
ALTER TABLE daily_health_coverage
    DROP CONSTRAINT daily_health_coverage_sample_count_check,
    ADD CONSTRAINT daily_health_coverage_sample_count_check CHECK (sample_count BETWEEN 0 AND 500),
    DROP CONSTRAINT daily_health_coverage_deleted_count_check,
    ADD CONSTRAINT daily_health_coverage_deleted_count_check CHECK (deleted_count BETWEEN 0 AND 500);

-- +goose Down
-- No-op: the wider checks accept everything an older binary writes, and
-- narrowing them would reject coverage rows of committed 500-change pages.
