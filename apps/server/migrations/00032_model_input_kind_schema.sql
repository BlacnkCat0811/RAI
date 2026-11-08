-- +goose Up
-- The pre-existing named constraint from the v1 external-VO2 migration still
-- bound hr_reference to schema/1. Replace it explicitly; do not relax the
-- table's independent kind/schema checks.
ALTER TABLE athlete_model_inputs DROP CONSTRAINT IF EXISTS model_input_kind_schema;
ALTER TABLE athlete_model_inputs ADD CONSTRAINT model_input_kind_schema CHECK (
 (kind='hr_reference' AND schema_version IN ('rai.hr_reference/1','rai.hr_reference/2')) OR
 (kind='vo2max_source_selection' AND schema_version='rai.vo2max.source_selection/1') OR
 (kind='sleep_target' AND schema_version='rai.sleep_target/1') OR
 (kind='workout_effort' AND schema_version='rai.workout_effort/1') OR
 (kind='workout_overlap_resolution' AND schema_version='rai.workout_overlap_resolution/1') OR
 (kind='source_context_selection' AND schema_version='rai.source_context_selection/1') OR
 (kind='revocation' AND schema_version IN ('rai.hr_reference/1','rai.hr_reference/2','rai.vo2max.source_selection/1','rai.sleep_target/1','rai.workout_effort/1','rai.workout_overlap_resolution/1','rai.source_context_selection/1'))
);
-- +goose Down
ALTER TABLE athlete_model_inputs DROP CONSTRAINT IF EXISTS model_input_kind_schema;
ALTER TABLE athlete_model_inputs ADD CONSTRAINT model_input_kind_schema CHECK (
 (kind='hr_reference' AND schema_version='rai.hr_reference/1') OR
 (kind='vo2max_source_selection' AND schema_version='rai.vo2max.source_selection/1')
);
