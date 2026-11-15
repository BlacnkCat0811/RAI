-- +goose Up
-- Preserve immutable v2 payloads; v3 patches share the existing transactional
-- receipts, invalidation and dataset lock. No sequence is a commit watermark.
ALTER TABLE athlete_model_inputs ADD COLUMN commit_order bigint NOT NULL DEFAULT 0;
CREATE UNIQUE INDEX model_input_commit_order ON athlete_model_inputs(dataset_id,commit_order) WHERE commit_order>0;
ALTER TABLE athlete_model_inputs DROP CONSTRAINT IF EXISTS athlete_model_inputs_schema_version_check;
ALTER TABLE athlete_model_inputs ADD CONSTRAINT athlete_model_inputs_schema_version_check CHECK(schema_version IN (
 'rai.hr_reference/1','rai.hr_reference/2','rai.hr_reference/3','rai.vo2max.source_selection/1',
 'rai.sleep_target/1','rai.workout_effort/1','rai.workout_overlap_resolution/1',
 'rai.source_context_selection/1','rai.trimp_parameters/1'));
-- A trimp parameter set is global and append-only, so revoking one input cannot
-- retract an already published parameter version. Keep it outside the
-- revocation branch on purpose.
ALTER TABLE athlete_model_inputs DROP CONSTRAINT IF EXISTS model_input_kind_schema;
ALTER TABLE athlete_model_inputs ADD CONSTRAINT model_input_kind_schema CHECK (
 (kind='hr_reference' AND schema_version IN ('rai.hr_reference/1','rai.hr_reference/2','rai.hr_reference/3')) OR
 (kind='vo2max_source_selection' AND schema_version='rai.vo2max.source_selection/1' AND
  COALESCE(jsonb_typeof(payload->'sourceEpoch')='string' AND payload->>'sourceEpoch' ~ '^sha256:[0-9a-f]{64}$' AND
  jsonb_typeof(payload->'revoked')='boolean' AND payload-'sourceEpoch'-'revoked'='{}',false)) OR
 (kind='sleep_target' AND schema_version='rai.sleep_target/1') OR
 (kind='workout_effort' AND schema_version='rai.workout_effort/1') OR
 (kind='workout_overlap_resolution' AND schema_version='rai.workout_overlap_resolution/1') OR
 (kind='source_context_selection' AND schema_version='rai.source_context_selection/1') OR
 (kind='trimp_parameters' AND schema_version='rai.trimp_parameters/1') OR
 (kind='revocation' AND schema_version IN ('rai.hr_reference/1','rai.hr_reference/2','rai.hr_reference/3','rai.vo2max.source_selection/1','rai.sleep_target/1','rai.workout_effort/1','rai.workout_overlap_resolution/1','rai.source_context_selection/1'))
);

-- +goose StatementBegin
CREATE FUNCTION rai_model_input_order_v3() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 PERFORM 1 FROM datasets WHERE id=NEW.dataset_id FOR UPDATE;
 SELECT COALESCE(max(commit_order),0)+1 INTO NEW.commit_order FROM athlete_model_inputs WHERE dataset_id=NEW.dataset_id;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER model_input_order_v3 BEFORE INSERT ON athlete_model_inputs FOR EACH ROW EXECUTE FUNCTION rai_model_input_order_v3();
-- Preserve the full installed commit implementation and its ACL, extending only
-- the admitted payload field set for the explicitly versioned HR input.
-- +goose StatementBegin
DO $migration$
DECLARE definition text;
BEGIN
 SELECT pg_get_functiondef('rai_commit_typed_model_input(uuid,uuid,uuid,text,timestamptz,timestamptz,uuid,uuid,text,bytea,jsonb,text)'::regprocedure) INTO definition;
 IF position('WHEN ''hr_reference'' THEN ARRAY[''maxHR'',''restingHR'',''measuredAt'',''restingMeasuredAt'',''method'',''unit'']' in definition)=0 THEN
  RAISE EXCEPTION 'unexpected_model_input_function';
 END IF;
 definition:=replace(definition,'WHEN ''hr_reference'' THEN ARRAY[''maxHR'',''restingHR'',''measuredAt'',''restingMeasuredAt'',''method'',''unit'']',
 'WHEN ''hr_reference'' THEN CASE WHEN p_schema=''rai.hr_reference/3'' THEN ARRAY[''maxHR'',''restingHR''] ELSE ARRAY[''maxHR'',''restingHR'',''measuredAt'',''restingMeasuredAt'',''method'',''unit''] END');
 EXECUTE definition;
END $migration$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'hr_reference_v3_down_refused: immutable input history'; END $$;
-- +goose StatementEnd
