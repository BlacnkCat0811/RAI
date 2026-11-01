-- +goose Up
-- RQ-016: on a fresh imported Workout without autovacuum statistics, PG18
-- selected health_samples_type_time for 26,382 UUID lookups (1.36M shared
-- hits, 2212ms). This covering identity path measured 109ms on the same data.
-- UUID second avoids scanning a type/time range for each member. Facts remain
-- immutable; the additional index trades bounded write/storage cost for reads.
CREATE INDEX health_samples_analysis_identity
    ON health_samples(dataset_id, sample_uuid, sample_type)
    INCLUDE (event_start, event_end, value_numeric, unit, content_sha256);

-- +goose Down
DROP INDEX health_samples_analysis_identity;
