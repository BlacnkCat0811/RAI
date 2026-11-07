-- +goose Up
-- Model completion extends the typed input kinds; it must not remove the
-- pre-existing external-VO2 source selection kind used by P4/P5.
ALTER TABLE athlete_model_inputs DROP CONSTRAINT IF EXISTS athlete_model_inputs_kind_check;
ALTER TABLE athlete_model_inputs ADD CONSTRAINT athlete_model_inputs_kind_check CHECK(kind IN (
 'hr_reference','vo2max_source_selection','sleep_target','workout_effort',
 'workout_overlap_resolution','source_context_selection','revocation'));
ALTER TABLE athlete_model_inputs DROP CONSTRAINT IF EXISTS athlete_model_inputs_schema_version_check;
ALTER TABLE athlete_model_inputs ADD CONSTRAINT athlete_model_inputs_schema_version_check CHECK(schema_version IN (
 'rai.hr_reference/1','rai.hr_reference/2','rai.vo2max.source_selection/1',
 'rai.sleep_target/1','rai.workout_effort/1','rai.workout_overlap_resolution/1',
 'rai.source_context_selection/1'));
ALTER TABLE athlete_model_inputs DROP CONSTRAINT IF EXISTS model_input_kind_schema;
ALTER TABLE athlete_model_inputs ADD CONSTRAINT model_input_kind_schema CHECK (
 (kind='hr_reference' AND schema_version IN ('rai.hr_reference/1','rai.hr_reference/2')) OR
 (kind='vo2max_source_selection' AND schema_version='rai.vo2max.source_selection/1' AND
  COALESCE(jsonb_typeof(payload->'sourceEpoch')='string' AND payload->>'sourceEpoch' ~ '^sha256:[0-9a-f]{64}$' AND
  jsonb_typeof(payload->'revoked')='boolean' AND payload-'sourceEpoch'-'revoked'='{}',false)) OR
 (kind='sleep_target' AND schema_version='rai.sleep_target/1') OR
 (kind='workout_effort' AND schema_version='rai.workout_effort/1') OR
 (kind='workout_overlap_resolution' AND schema_version='rai.workout_overlap_resolution/1') OR
 (kind='source_context_selection' AND schema_version='rai.source_context_selection/1') OR
 (kind='revocation' AND schema_version IN ('rai.hr_reference/1','rai.hr_reference/2','rai.vo2max.source_selection/1','rai.sleep_target/1','rai.workout_effort/1','rai.workout_overlap_resolution/1','rai.source_context_selection/1'))
);
-- +goose Down
ALTER TABLE athlete_model_inputs DROP CONSTRAINT IF EXISTS model_input_kind_schema;
ALTER TABLE athlete_model_inputs DROP CONSTRAINT IF EXISTS athlete_model_inputs_schema_version_check;
ALTER TABLE athlete_model_inputs DROP CONSTRAINT IF EXISTS athlete_model_inputs_kind_check;
ALTER TABLE athlete_model_inputs ADD CONSTRAINT athlete_model_inputs_kind_check CHECK(kind IN ('hr_reference','sleep_target','workout_effort','workout_overlap_resolution','source_context_selection','revocation'));
ALTER TABLE athlete_model_inputs ADD CONSTRAINT athlete_model_inputs_schema_version_check CHECK(schema_version IN ('rai.hr_reference/1','rai.hr_reference/2','rai.sleep_target/1','rai.workout_effort/1','rai.workout_overlap_resolution/1','rai.source_context_selection/1'));
ALTER TABLE athlete_model_inputs ADD CONSTRAINT model_input_kind_schema CHECK (
 (kind='hr_reference' AND schema_version IN ('rai.hr_reference/1','rai.hr_reference/2')) OR
 (kind='sleep_target' AND schema_version='rai.sleep_target/1') OR
 (kind='workout_effort' AND schema_version='rai.workout_effort/1') OR
 (kind='workout_overlap_resolution' AND schema_version='rai.workout_overlap_resolution/1') OR
 (kind='source_context_selection' AND schema_version='rai.source_context_selection/1')
);
