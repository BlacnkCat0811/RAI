-- +goose Up
-- Expose the append-only external-VO2 source selection through the same typed
-- input receipt path as other personal inputs. The table constraint already
-- pins the payload shape; repeat the checks here so a direct function call
-- cannot bypass them through a stale schema argument.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rai_commit_typed_model_input(
 p_dataset uuid,p_device uuid,p_id uuid,p_kind text,p_from timestamptz,
 p_through timestamptz,p_supersedes uuid,p_revokes uuid,p_schema text,
 p_hash bytea,p_payload jsonb,p_provenance text
) RETURNS jsonb LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE
 old_input athlete_model_inputs%ROWTYPE;
 target athlete_model_inputs%ROWTYPE;
 old_receipt jsonb;
 receipt jsonb;
 job_ids jsonb;
 now_at timestamptz;
 allowed text[];
 workout_id uuid;
BEGIN
 PERFORM 1 FROM datasets WHERE id=p_dataset AND retired_at IS NULL FOR UPDATE;
 IF NOT FOUND OR NOT EXISTS (
  SELECT 1 FROM devices WHERE dataset_id=p_dataset AND id=p_device AND revoked_at IS NULL AND can_collect
 ) THEN
  RAISE EXCEPTION 'model_input_not_found' USING ERRCODE='RA080';
 END IF;
 now_at:=clock_timestamp();
 IF p_kind='revocation' THEN
  SELECT * INTO target FROM athlete_model_inputs
  WHERE dataset_id=p_dataset AND id=p_revokes AND kind<>'revocation' AND subject_key='athlete';
  IF NOT FOUND THEN
   RAISE EXCEPTION 'model_input_not_found' USING ERRCODE='RA080';
  END IF;
  SELECT * INTO old_input FROM athlete_model_inputs
   WHERE dataset_id=p_dataset AND kind='revocation' AND revokes_id=p_revokes ORDER BY created_at,id LIMIT 1;
  IF FOUND THEN
   IF COALESCE(old_input.payload->>'reason','') IS DISTINCT FROM COALESCE(p_payload->>'reason','') THEN
    RAISE EXCEPTION 'model_input_conflict' USING ERRCODE='RA081';
   END IF;
   SELECT r.receipt INTO STRICT old_receipt FROM model_input_receipts r WHERE dataset_id=p_dataset AND input_id=old_input.id;
   RETURN old_receipt;
  END IF;
  p_schema:=target.schema_version;
  p_payload:=jsonb_strip_nulls(jsonb_build_object('targetSchema',p_schema,'reason',p_payload->>'reason'));
  p_from:=now_at;
  p_through:=NULL;
  p_supersedes:=NULL;
  p_provenance:='api-revocation';
  p_hash:=sha256(convert_to(jsonb_build_array(p_kind,p_revokes,p_payload)::text,'UTF8'));
 ELSE
  IF p_revokes IS NOT NULL THEN
   RAISE EXCEPTION 'model_input_invalid' USING ERRCODE='RA082';
  END IF;
  SELECT * INTO old_input FROM athlete_model_inputs WHERE dataset_id=p_dataset AND id=p_id;
  IF FOUND THEN
   IF old_input.kind=p_kind AND old_input.schema_version=p_schema AND old_input.subject_key='athlete'
    AND old_input.effective_from=p_from AND old_input.effective_through IS NOT DISTINCT FROM p_through
    AND old_input.supersedes_id IS NOT DISTINCT FROM p_supersedes AND old_input.revokes_id IS NULL
    AND old_input.provenance=p_provenance AND old_input.payload=p_payload THEN
    SELECT r.receipt INTO STRICT old_receipt FROM model_input_receipts r WHERE dataset_id=p_dataset AND input_id=p_id;
    RETURN old_receipt;
   END IF;
   RAISE EXCEPTION 'model_input_conflict' USING ERRCODE='RA081';
  END IF;
 END IF;

 allowed:=CASE p_kind
  WHEN 'hr_reference' THEN ARRAY['maxHR','restingHR','measuredAt','restingMeasuredAt','method','unit']
  WHEN 'sleep_target' THEN ARRAY['hours']
  WHEN 'workout_effort' THEN ARRAY['workoutID','mode','userConfirmed','independentlyVerified']
  WHEN 'workout_overlap_resolution' THEN ARRAY['primaryWorkoutID','duplicateWorkoutID']
  WHEN 'source_context_selection' THEN ARRAY['sourceEpoch']
  WHEN 'vo2max_source_selection' THEN ARRAY['sourceEpoch','revoked']
  WHEN 'revocation' THEN ARRAY['targetSchema','reason']
 END;
 IF allowed IS NULL OR p_payload IS NULL OR jsonb_typeof(p_payload)<>'object' OR
  EXISTS(SELECT 1 FROM jsonb_object_keys(p_payload) AS k WHERE NOT k=ANY(allowed)) THEN
  RAISE EXCEPTION 'model_input_invalid' USING ERRCODE='RA082';
 END IF;

 IF p_kind='vo2max_source_selection' THEN
  IF p_schema<>'rai.vo2max.source_selection/1'
   OR p_payload->>'sourceEpoch' !~ '^sha256:[0-9a-f]{64}$'
   OR jsonb_typeof(p_payload->'revoked')<>'boolean' THEN
   RAISE EXCEPTION 'model_input_invalid' USING ERRCODE='RA082';
  END IF;
  IF (SELECT count(*) FROM athlete_model_inputs WHERE dataset_id=p_dataset AND kind='vo2max_source_selection')>=128 THEN
   RAISE EXCEPTION 'model_input_invalid' USING ERRCODE='RA082';
  END IF;
 END IF;

 IF p_kind='workout_effort' THEN
  IF p_payload->>'mode' IS DISTINCT FROM 'maximal_supported' OR p_payload->'userConfirmed' IS DISTINCT FROM 'true'::jsonb
   OR p_payload->'independentlyVerified'='true'::jsonb THEN
   RAISE EXCEPTION 'model_input_invalid' USING ERRCODE='RA082';
  END IF;
 END IF;
 IF p_kind IN ('workout_effort','workout_overlap_resolution') THEN
  FOREACH workout_id IN ARRAY CASE p_kind
    WHEN 'workout_effort' THEN ARRAY[(p_payload->>'workoutID')::uuid]
    ELSE ARRAY[(p_payload->>'primaryWorkoutID')::uuid,(p_payload->>'duplicateWorkoutID')::uuid] END
  LOOP
   IF NOT EXISTS (SELECT 1 FROM workouts WHERE dataset_id=p_dataset AND workout_uuid=workout_id AND activity_type=37) OR
    EXISTS (SELECT 1 FROM entity_tombstones WHERE dataset_id=p_dataset AND sample_type='HKWorkoutTypeIdentifier' AND sample_uuid=workout_id) THEN
    RAISE EXCEPTION 'model_input_subject_not_found' USING ERRCODE='RA080';
   END IF;
  END LOOP;
 END IF;
 IF p_supersedes IS NOT NULL THEN
  SELECT * INTO target FROM athlete_model_inputs WHERE dataset_id=p_dataset AND id=p_supersedes AND kind=p_kind AND subject_key='athlete';
  IF NOT FOUND OR p_supersedes=p_id
   OR (target.payload->>'workoutID') IS DISTINCT FROM (p_payload->>'workoutID')
   OR (target.payload->>'duplicateWorkoutID') IS DISTINCT FROM (p_payload->>'duplicateWorkoutID') THEN
   RAISE EXCEPTION 'model_input_conflict' USING ERRCODE='RA081';
  END IF;
 END IF;

 INSERT INTO athlete_model_inputs(dataset_id,id,kind,subject_key,effective_from,effective_through,
  supersedes_id,schema_version,content_sha256,payload,provenance,revokes_id,input_schema,created_at)
 VALUES(p_dataset,p_id,p_kind,'athlete',p_from,p_through,p_supersedes,p_schema,p_hash,p_payload,p_provenance,p_revokes,p_schema,now_at);

 -- Mark every existing v2 publication scope for this dataset as dirty.
 -- Recalculation is only scheduled by a registered model stack.
 UPDATE dirty_scopes d SET generation=generation+1,dirty_from=LEAST(d.dirty_from,p_from),dirty_through=GREATEST(d.dirty_through,now_at)
 WHERE d.dataset_id=p_dataset AND EXISTS (
  SELECT 1 FROM analysis_model_channels m WHERE m.dataset_id=d.dataset_id AND m.stack_id<>'legacy' AND NOT (m.extension_configs ? 'rai.model_manifest/1')
   AND left(d.scope_key,length(m.stack_id)+15)='model/'||m.stack_id||'/athlete/'
 );
 SELECT COALESCE(jsonb_agg(DISTINCT j.id),'[]'::jsonb) INTO job_ids
 FROM datasets ds
 JOIN analysis_model_channels m ON m.dataset_id=ds.id AND m.extension_configs ? 'rai.model_manifest/1'
 JOIN dirty_scopes d ON d.dataset_id=m.dataset_id AND d.scope_key=
  'model/'||m.stack_id||'/athlete/day/v2/'||(now_at AT TIME ZONE ds.timezone)::date::text
 JOIN jobs j ON j.dataset_id=d.dataset_id AND j.scope_key=d.scope_key AND j.target_generation=d.generation
 WHERE ds.id=p_dataset AND j.kind='analysis.model_stack_v2' AND d.dirty_through>=now_at;
 receipt:=jsonb_build_object('inputID',p_id,'schemaVersion',p_schema,'contentSHA256',encode(p_hash,'hex'),
  'acceptedAtUs',(extract(epoch FROM now_at)*1000000)::bigint,'analysisStatus','pending','jobIDs',job_ids);
 INSERT INTO model_input_receipts(dataset_id,input_id,content_sha256,receipt) VALUES(p_dataset,p_id,p_hash,receipt);
 RETURN receipt;
END $$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION rai_commit_typed_model_input(uuid,uuid,uuid,text,timestamptz,timestamptz,uuid,uuid,text,bytea,jsonb,text) FROM PUBLIC;

-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='rai_api') THEN
  GRANT EXECUTE ON FUNCTION rai_commit_typed_model_input(uuid,uuid,uuid,text,timestamptz,timestamptz,uuid,uuid,text,bytea,jsonb,text) TO rai_api;
 END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rai_commit_typed_model_input(
 p_dataset uuid,p_device uuid,p_id uuid,p_kind text,p_from timestamptz,
 p_through timestamptz,p_supersedes uuid,p_revokes uuid,p_schema text,
 p_hash bytea,p_payload jsonb,p_provenance text
) RETURNS jsonb LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE
 old_input athlete_model_inputs%ROWTYPE;
 target athlete_model_inputs%ROWTYPE;
 old_receipt jsonb;
 receipt jsonb;
 job_ids jsonb;
 now_at timestamptz;
 allowed text[];
 workout_id uuid;
BEGIN
 PERFORM 1 FROM datasets WHERE id=p_dataset AND retired_at IS NULL FOR UPDATE;
 IF NOT FOUND OR NOT EXISTS (
  SELECT 1 FROM devices WHERE dataset_id=p_dataset AND id=p_device AND revoked_at IS NULL AND can_collect
 ) THEN
  RAISE EXCEPTION 'model_input_not_found' USING ERRCODE='RA080';
 END IF;
 now_at:=clock_timestamp();
 IF p_kind='revocation' THEN
  SELECT * INTO target FROM athlete_model_inputs
  WHERE dataset_id=p_dataset AND id=p_revokes AND kind<>'revocation' AND subject_key='athlete';
  IF NOT FOUND THEN
   RAISE EXCEPTION 'model_input_not_found' USING ERRCODE='RA080';
  END IF;
  SELECT * INTO old_input FROM athlete_model_inputs
   WHERE dataset_id=p_dataset AND kind='revocation' AND revokes_id=p_revokes ORDER BY created_at,id LIMIT 1;
  IF FOUND THEN
   IF COALESCE(old_input.payload->>'reason','') IS DISTINCT FROM COALESCE(p_payload->>'reason','') THEN
    RAISE EXCEPTION 'model_input_conflict' USING ERRCODE='RA081';
   END IF;
   SELECT r.receipt INTO STRICT old_receipt FROM model_input_receipts r WHERE dataset_id=p_dataset AND input_id=old_input.id;
   RETURN old_receipt;
  END IF;
  p_schema:=target.schema_version;
  p_payload:=jsonb_strip_nulls(jsonb_build_object('targetSchema',p_schema,'reason',p_payload->>'reason'));
  p_from:=now_at;
  p_through:=NULL;
  p_supersedes:=NULL;
  p_provenance:='api-revocation';
  p_hash:=sha256(convert_to(jsonb_build_array(p_kind,p_revokes,p_payload)::text,'UTF8'));
 ELSE
  IF p_revokes IS NOT NULL THEN
   RAISE EXCEPTION 'model_input_invalid' USING ERRCODE='RA082';
  END IF;
  SELECT * INTO old_input FROM athlete_model_inputs WHERE dataset_id=p_dataset AND id=p_id;
  IF FOUND THEN
   IF old_input.kind=p_kind AND old_input.schema_version=p_schema AND old_input.subject_key='athlete'
    AND old_input.effective_from=p_from AND old_input.effective_through IS NOT DISTINCT FROM p_through
    AND old_input.supersedes_id IS NOT DISTINCT FROM p_supersedes AND old_input.revokes_id IS NULL
    AND old_input.provenance=p_provenance AND old_input.payload=p_payload THEN
    SELECT r.receipt INTO STRICT old_receipt FROM model_input_receipts r WHERE dataset_id=p_dataset AND input_id=p_id;
    RETURN old_receipt;
   END IF;
   RAISE EXCEPTION 'model_input_conflict' USING ERRCODE='RA081';
  END IF;
 END IF;

 allowed:=CASE p_kind
  WHEN 'hr_reference' THEN ARRAY['maxHR','restingHR','measuredAt','restingMeasuredAt','method','unit']
  WHEN 'sleep_target' THEN ARRAY['hours']
  WHEN 'workout_effort' THEN ARRAY['workoutID','mode','userConfirmed','independentlyVerified']
  WHEN 'workout_overlap_resolution' THEN ARRAY['primaryWorkoutID','duplicateWorkoutID']
  WHEN 'source_context_selection' THEN ARRAY['sourceEpoch']
  WHEN 'revocation' THEN ARRAY['targetSchema','reason']
 END;
 IF allowed IS NULL OR p_payload IS NULL OR jsonb_typeof(p_payload)<>'object' OR
  EXISTS(SELECT 1 FROM jsonb_object_keys(p_payload) AS k WHERE NOT k=ANY(allowed)) THEN
  RAISE EXCEPTION 'model_input_invalid' USING ERRCODE='RA082';
 END IF;

 IF p_kind='workout_effort' THEN
  IF p_payload->>'mode' IS DISTINCT FROM 'maximal_supported' OR p_payload->'userConfirmed' IS DISTINCT FROM 'true'::jsonb
   OR p_payload->'independentlyVerified'='true'::jsonb THEN
   RAISE EXCEPTION 'model_input_invalid' USING ERRCODE='RA082';
  END IF;
 END IF;
 IF p_kind IN ('workout_effort','workout_overlap_resolution') THEN
  FOREACH workout_id IN ARRAY CASE p_kind
    WHEN 'workout_effort' THEN ARRAY[(p_payload->>'workoutID')::uuid]
    ELSE ARRAY[(p_payload->>'primaryWorkoutID')::uuid,(p_payload->>'duplicateWorkoutID')::uuid] END
  LOOP
   IF NOT EXISTS (SELECT 1 FROM workouts WHERE dataset_id=p_dataset AND workout_uuid=workout_id AND activity_type=37) OR
    EXISTS (SELECT 1 FROM entity_tombstones WHERE dataset_id=p_dataset AND sample_type='HKWorkoutTypeIdentifier' AND sample_uuid=workout_id) THEN
    RAISE EXCEPTION 'model_input_subject_not_found' USING ERRCODE='RA080';
   END IF;
  END LOOP;
 END IF;
 IF p_supersedes IS NOT NULL THEN
  SELECT * INTO target FROM athlete_model_inputs WHERE dataset_id=p_dataset AND id=p_supersedes AND kind=p_kind AND subject_key='athlete';
  IF NOT FOUND OR p_supersedes=p_id
   OR (target.payload->>'workoutID') IS DISTINCT FROM (p_payload->>'workoutID')
   OR (target.payload->>'duplicateWorkoutID') IS DISTINCT FROM (p_payload->>'duplicateWorkoutID') THEN
   RAISE EXCEPTION 'model_input_conflict' USING ERRCODE='RA081';
  END IF;
 END IF;

 INSERT INTO athlete_model_inputs(dataset_id,id,kind,subject_key,effective_from,effective_through,
  supersedes_id,schema_version,content_sha256,payload,provenance,revokes_id,input_schema,created_at)
 VALUES(p_dataset,p_id,p_kind,'athlete',p_from,p_through,p_supersedes,p_schema,p_hash,p_payload,p_provenance,p_revokes,p_schema,now_at);

 UPDATE dirty_scopes d SET generation=generation+1,dirty_from=LEAST(d.dirty_from,p_from),dirty_through=GREATEST(d.dirty_through,now_at)
 WHERE d.dataset_id=p_dataset AND EXISTS (
  SELECT 1 FROM analysis_model_channels m WHERE m.dataset_id=d.dataset_id AND m.stack_id<>'legacy' AND NOT (m.extension_configs ? 'rai.model_manifest/1')
   AND left(d.scope_key,length(m.stack_id)+15)='model/'||m.stack_id||'/athlete/'
 );
 SELECT COALESCE(jsonb_agg(DISTINCT j.id),'[]'::jsonb) INTO job_ids
 FROM datasets ds
 JOIN analysis_model_channels m ON m.dataset_id=ds.id AND m.extension_configs ? 'rai.model_manifest/1'
 JOIN dirty_scopes d ON d.dataset_id=m.dataset_id AND d.scope_key=
  'model/'||m.stack_id||'/athlete/day/v2/'||(now_at AT TIME ZONE ds.timezone)::date::text
 JOIN jobs j ON j.dataset_id=d.dataset_id AND j.scope_key=d.scope_key AND j.target_generation=d.generation
 WHERE ds.id=p_dataset AND j.kind='analysis.model_stack_v2' AND d.dirty_through>=now_at;
 receipt:=jsonb_build_object('inputID',p_id,'schemaVersion',p_schema,'contentSHA256',encode(p_hash,'hex'),
  'acceptedAtUs',(extract(epoch FROM now_at)*1000000)::bigint,'analysisStatus','pending','jobIDs',job_ids);
 INSERT INTO model_input_receipts(dataset_id,input_id,content_sha256,receipt) VALUES(p_dataset,p_id,p_hash,receipt);
 RETURN receipt;
END $$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION rai_commit_typed_model_input(uuid,uuid,uuid,text,timestamptz,timestamptz,uuid,uuid,text,bytea,jsonb,text) FROM PUBLIC;

-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='rai_api') THEN
  GRANT EXECUTE ON FUNCTION rai_commit_typed_model_input(uuid,uuid,uuid,text,timestamptz,timestamptz,uuid,uuid,text,bytea,jsonb,text) TO rai_api;
 END IF;
END $$;
-- +goose StatementEnd
