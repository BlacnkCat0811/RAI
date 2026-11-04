-- +goose Up
-- Independent external projection; active remains legacy. No estimated model.
ALTER TABLE analysis_artifacts DROP CONSTRAINT analysis_artifacts_check;
ALTER TABLE analysis_artifacts ADD CHECK(
 (artifact_key='vo2max_external_projection' AND schema_version='rai.vo2max.external_projection/1' AND octet_length(payload::text)<=131072) OR
 (artifact_key='workout_features' AND schema_version='rai.workout_features/1' AND octet_length(payload::text)<=131072) OR
 (artifact_key='hr_exposure' AND schema_version='rai.hr_exposure/1' AND octet_length(payload::text)<=2097152) OR
 (artifact_key='training_load' AND schema_version='rai.training_load/1' AND octet_length(payload::text)<=131072) OR
 (artifact_key='load_state' AND schema_version='rai.load_state/1' AND octet_length(payload::text)<=2097152) OR
 (artifact_key='performance_evidence' AND schema_version='rai.performance_evidence/1' AND octet_length(payload::text)<=2097152) OR
 (artifact_key='performance_curve' AND schema_version='rai.performance_curve/1' AND octet_length(payload::text)<=2097152) OR
 (artifact_key='running_ability' AND schema_version='rai.running_ability/1' AND octet_length(payload::text)<=2097152) OR
 (artifact_key='endurance' AND schema_version='rai.endurance/1' AND octet_length(payload::text)<=2097152) OR
 (artifact_key='performance_state' AND schema_version='rai.performance/1' AND octet_length(payload::text)<=2097152));
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rai_feature_artifact_insert() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE r analysis_runs%ROWTYPE; alg text;
BEGIN
 SELECT * INTO r FROM analysis_runs WHERE dataset_id=NEW.dataset_id AND id=NEW.run_id FOR SHARE;
 alg:=CASE NEW.artifact_key WHEN 'vo2max_external_projection' THEN 'rai.vo2max.external_projection' WHEN 'workout_features' THEN 'rai.workout_features' WHEN 'hr_exposure' THEN 'rai.workout_features'
 WHEN 'training_load' THEN 'rai.training_load.hrr_integral' WHEN 'load_state' THEN 'rai.load_state.exp_finite'
 WHEN 'performance_evidence' THEN 'rai.performance_curve' WHEN 'performance_curve' THEN 'rai.performance_curve'
 WHEN 'running_ability' THEN 'rai.performance_curve' WHEN 'endurance' THEN 'rai.performance_curve' WHEN 'performance_state' THEN 'rai.performance_curve' END;
 IF NOT FOUND OR r.status<>'RUNNING' OR r.algorithm_name IS DISTINCT FROM alg OR r.algorithm_version<>'v1'
 OR (NEW.payload->>'schemaVersion') IS DISTINCT FROM NEW.schema_version OR (NEW.payload->>'datasetID') IS DISTINCT FROM NEW.dataset_id::text
 OR COALESCE((NEW.payload->>'generation')::bigint,(NEW.payload->>'inputGeneration')::bigint) IS DISTINCT FROM r.target_generation THEN
 RAISE EXCEPTION 'feature_artifact_invalid_run' USING ERRCODE='RA041'; END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd

ALTER TABLE athlete_model_inputs DROP CONSTRAINT athlete_model_inputs_kind_check;
ALTER TABLE athlete_model_inputs DROP CONSTRAINT athlete_model_inputs_schema_version_check;
ALTER TABLE athlete_model_inputs ADD CONSTRAINT model_input_kind_schema CHECK (
 (kind='hr_reference' AND schema_version='rai.hr_reference/1') OR
 (kind='vo2max_source_selection' AND schema_version='rai.vo2max.source_selection/1' AND
 COALESCE(jsonb_typeof(payload->'sourceEpoch')='string' AND payload->>'sourceEpoch' ~ '^sha256:[0-9a-f]{64}$' AND
 jsonb_typeof(payload->'revoked')='boolean' AND payload-'sourceEpoch'-'revoked'='{}',false)));
ALTER TABLE admin_operations DROP CONSTRAINT IF EXISTS admin_operations_check;
ALTER TABLE admin_operations DROP CONSTRAINT IF EXISTS admin_operations_operation_type_check;
ALTER TABLE admin_operations DROP CONSTRAINT IF EXISTS admin_operations_check1;
ALTER TABLE admin_operations ADD CHECK(operation_type IN ('new_dataset','disaster_recovery','feature_candidate','vo2max_external_candidate','vo2max_source_selection'));
ALTER TABLE admin_operations ADD CHECK((operation_type IN ('feature_candidate','vo2max_external_candidate','vo2max_source_selection') AND subject_dataset IS NOT NULL AND source_dataset IS NULL AND target_dataset IS NULL) OR (operation_type IN ('new_dataset','disaster_recovery') AND target_dataset IS NOT NULL AND (operation_type='disaster_recovery' OR source_dataset IS NOT NULL)));
ALTER TABLE analysis_model_dependencies DROP CONSTRAINT IF EXISTS analysis_model_dependencies_check;
ALTER TABLE analysis_model_dependencies DROP CONSTRAINT IF EXISTS analysis_model_dependencies_upstream_scope_check;
ALTER TABLE analysis_model_dependencies DROP CONSTRAINT IF EXISTS analysis_model_dependencies_downstream_scope_check;
ALTER TABLE analysis_model_dependencies DROP CONSTRAINT IF EXISTS analysis_model_dependencies_check1;
ALTER TABLE analysis_model_dependencies DROP CONSTRAINT IF EXISTS analysis_model_dependencies_check2;
ALTER TABLE analysis_model_dependencies ADD CHECK(
 (upstream_scope ~ '^model/[^/]+/(workout|workout_load)/[0-9a-f-]{36}$' AND
 downstream_scope ~ '^model/[^/]+/(athlete/(load|performance|vo2|ability|endurance)|workout_load/[0-9a-f-]{36})$' AND
 upstream_scope<>downstream_scope AND (upstream_scope LIKE '%/workout/%' OR downstream_scope LIKE '%/athlete/%')) OR
 (upstream_scope IN ('external/vo2max/facts/v1','external/vo2max/selection/v1') AND downstream_scope='external/vo2max/v1/athlete/current'));
ALTER TABLE analysis_model_channels ADD CONSTRAINT vo2_external_entry CHECK (
 NOT extension_configs ? 'rai.vo2max.external_projection/v1' OR COALESCE(
 channel='candidate' AND jsonb_typeof(extension_configs->'rai.vo2max.external_projection/v1')='object' AND
 (extension_configs->'rai.vo2max.external_projection/v1')-'configHash'-'assignmentID'='{}' AND
 extension_configs->'rai.vo2max.external_projection/v1'->>'configHash' ~ '^[0-9a-f]{64}$' AND
 extension_configs->'rai.vo2max.external_projection/v1'->>'assignmentID' ~ '^[0-9a-f-]{36}$',false));
ALTER TABLE analysis_results ADD CONSTRAINT vo2_external_metric CHECK (
 metric<>'vo2max_external_value' OR (algorithm_name='rai.vo2max.external_projection' AND algorithm_version='v1' AND day='0001-01-01' AND unit='mL/kg/min' AND quality IN ('partial','insufficient')));
ALTER TABLE analysis_publications ADD CONSTRAINT vo2_external_family CHECK (
 metric_family<>'vo2max_external_candidate' OR (scope_key='external/vo2max/v1/athlete/current' AND day='0001-01-01'));

-- Fixed-scope invalidator. All mutation paths serialize on the dataset lock.
-- +goose StatementBegin
CREATE FUNCTION rai_vo2_external_dirty(p_dataset uuid) RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE entry jsonb; g bigint; selection_identity text; rev bigint;
BEGIN
 SELECT commit_rev INTO rev FROM public.datasets WHERE id=p_dataset FOR UPDATE;
 SELECT extension_configs->'rai.vo2max.external_projection/v1' INTO entry FROM public.analysis_model_channels WHERE dataset_id=p_dataset AND channel='candidate';
 DELETE FROM public.analysis_publications WHERE dataset_id=p_dataset AND scope_key='external/vo2max/v1/athlete/current';
 IF entry IS NULL THEN RETURN; END IF;
 SELECT COALESCE(string_agg(id::text||':'||encode(content_sha256,'hex'),',' ORDER BY id),'') INTO selection_identity FROM public.athlete_model_inputs WHERE dataset_id=p_dataset AND kind='vo2max_source_selection';
 -- Identity includes explicit controls, own assignment/config, revision and the
 -- monotonic external generation. Other model assignments are not inputs.
 INSERT INTO public.dirty_scopes(dataset_id,scope_key,generation,dirty_from,dirty_through)
 VALUES(p_dataset,'external/vo2max/v1/athlete/current',1,clock_timestamp(),clock_timestamp())
 ON CONFLICT(dataset_id,scope_key) DO UPDATE SET generation=dirty_scopes.generation+1,dirty_through=clock_timestamp() RETURNING generation INTO g;
 INSERT INTO public.jobs(id,dataset_id,kind,scope_key,target_generation,payload)
 VALUES(gen_random_uuid(),p_dataset,'analysis.vo2max_external_projection','external/vo2max/v1/athlete/current',g,
 jsonb_build_object('configHash',entry->>'configHash','assignmentID',entry->>'assignmentID','selectionIdentity',selection_identity,'knownRevision',rev));
END $$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION rai_vo2_external_dirty(uuid) FROM PUBLIC;
-- +goose StatementBegin
CREATE FUNCTION rai_vo2_external_invalidate() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE dataset uuid;
BEGIN
 IF TG_OP='DELETE' THEN dataset:=OLD.dataset_id; ELSE dataset:=NEW.dataset_id; END IF;
 IF TG_TABLE_NAME='health_samples' OR TG_TABLE_NAME='entity_tombstones' THEN
  IF TG_OP='DELETE' THEN IF OLD.sample_type<>'HKQuantityTypeIdentifierVO2Max' THEN RETURN OLD; END IF;
  ELSE IF NEW.sample_type<>'HKQuantityTypeIdentifierVO2Max' THEN RETURN NEW; END IF; END IF;
 ELSIF TG_TABLE_NAME='dirty_scopes' THEN
  IF NEW.scope_key<>'external/vo2max/facts/v1' OR (TG_OP='UPDATE' AND NEW.generation=OLD.generation) THEN RETURN NEW; END IF;
 ELSIF TG_TABLE_NAME='athlete_model_inputs' THEN
  IF NEW.kind<>'vo2max_source_selection' THEN RETURN NEW; END IF;
 ELSIF TG_TABLE_NAME='analysis_model_channels' THEN
  IF TG_OP='DELETE' AND NOT OLD.extension_configs ? 'rai.vo2max.external_projection/v1' THEN RETURN OLD; END IF;
  IF TG_OP='UPDATE' AND NEW.extension_configs->'rai.vo2max.external_projection/v1' IS NOT DISTINCT FROM OLD.extension_configs->'rai.vo2max.external_projection/v1' THEN RETURN NEW; END IF;
  IF TG_OP='INSERT' AND NOT NEW.extension_configs ? 'rai.vo2max.external_projection/v1' THEN RETURN NEW; END IF;
 END IF;
 PERFORM public.rai_vo2_external_dirty(dataset);
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION rai_vo2_external_invalidate() FROM PUBLIC;
CREATE TRIGGER vo2_external_fact AFTER INSERT OR DELETE ON health_samples FOR EACH ROW EXECUTE FUNCTION rai_vo2_external_invalidate();
CREATE TRIGGER vo2_external_tombstone AFTER INSERT ON entity_tombstones FOR EACH ROW EXECUTE FUNCTION rai_vo2_external_invalidate();
CREATE TRIGGER vo2_external_observation AFTER INSERT OR UPDATE ON dirty_scopes FOR EACH ROW EXECUTE FUNCTION rai_vo2_external_invalidate();
CREATE TRIGGER vo2_external_selection AFTER INSERT ON athlete_model_inputs FOR EACH ROW EXECUTE FUNCTION rai_vo2_external_invalidate();
CREATE TRIGGER vo2_external_assignment AFTER INSERT OR UPDATE OR DELETE ON analysis_model_channels FOR EACH ROW EXECUTE FUNCTION rai_vo2_external_invalidate();
-- Identity changes do not preserve a candidate from the previous collector.
-- +goose StatementBegin
CREATE FUNCTION rai_vo2_external_session() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF TG_TABLE_NAME='datasets' THEN
  IF NEW.commit_rev IS DISTINCT FROM OLD.commit_rev THEN
   UPDATE public.jobs SET payload=jsonb_set(payload,'{knownRevision}',to_jsonb(NEW.commit_rev)) WHERE dataset_id=NEW.id AND kind='analysis.vo2max_external_projection' AND state='queued' AND target_generation=(SELECT generation FROM public.dirty_scopes WHERE dataset_id=NEW.id AND scope_key='external/vo2max/v1/athlete/current');
  END IF;
  IF NEW.epoch IS DISTINCT FROM OLD.epoch OR NEW.retired_at IS DISTINCT FROM OLD.retired_at THEN PERFORM public.rai_vo2_external_dirty(NEW.id); END IF;
 ELSE
  IF TG_OP='INSERT' OR NEW.collector_epoch IS DISTINCT FROM OLD.collector_epoch THEN PERFORM public.rai_vo2_external_dirty(NEW.dataset_id); END IF;
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION rai_vo2_external_session() FROM PUBLIC;
CREATE TRIGGER vo2_external_dataset AFTER UPDATE ON datasets FOR EACH ROW EXECUTE FUNCTION rai_vo2_external_session();
CREATE TRIGGER vo2_external_collector AFTER INSERT OR UPDATE ON collector_leases FOR EACH ROW EXECUTE FUNCTION rai_vo2_external_session();

-- These support the measured bounded window and latest historical locator.
CREATE INDEX vo2_external_window ON health_samples(dataset_id,event_end DESC,sample_uuid) WHERE sample_type='HKQuantityTypeIdentifierVO2Max';
CREATE INDEX vo2_external_epoch_end ON health_samples(dataset_id,(metadata->'RAI:vo2maxFactV1'->>'sourceEpochID'),event_end DESC) WHERE sample_type='HKQuantityTypeIdentifierVO2Max';
CREATE INDEX vo2_external_withdrawal ON entity_tombstones(dataset_id,previous_end DESC) WHERE sample_type='HKQuantityTypeIdentifierVO2Max';
CREATE INDEX vo2_external_unknown_delete ON entity_tombstones(dataset_id,deleted_rev DESC) WHERE sample_type='HKQuantityTypeIdentifierVO2Max' AND previous_end IS NULL;
-- No Down: preserve immutable analysis/control history.

CREATE INDEX vo2_external_sync ON health_samples(dataset_id,source_bundle_id,(metadata->'RAI:vo2maxFactV1'->'typedMetadata'->'HKMetadataKeySyncIdentifier')) WHERE sample_type='HKQuantityTypeIdentifierVO2Max';
CREATE INDEX vo2_external_withdrawal_epoch ON entity_tombstones(dataset_id,(provenance_snapshot->>'sourceEpochID'),previous_end DESC) WHERE sample_type='HKQuantityTypeIdentifierVO2Max';

-- External-only slot metadata cannot implicitly enroll M1.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rai_feature_dependency() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE c record; id text; scope text;
BEGIN
 IF TG_OP='UPDATE' AND NEW.generation=OLD.generation THEN RETURN NEW; END IF;
 IF NEW.scope_key LIKE 'workout:HKWorkoutTypeIdentifier:%' THEN
  id:=substring(NEW.scope_key from length('workout:HKWorkoutTypeIdentifier:')+1);
  FOR c IN SELECT DISTINCT stack_id FROM analysis_model_channels WHERE dataset_id=NEW.dataset_id AND channel='candidate' AND NOT (extension_configs @> '{"rai.vo2max.external_only/v1":true}') LOOP
   scope:='model/'||c.stack_id||'/workout/'||id;
   PERFORM rai_analysis_dirty(NEW.dataset_id,scope,'analysis.workout_features',NEW.dirty_from,NEW.dirty_through);
  END LOOP;
 ELSIF NEW.scope_key LIKE 'model/%/workout/%' THEN
  INSERT INTO analysis_model_dependencies(dataset_id,upstream_scope,downstream_scope)
  SELECT NEW.dataset_id,NEW.scope_key,split_part(NEW.scope_key,'/workout/',1)||'/athlete/'||k
  FROM unnest(ARRAY['load','performance','vo2','ability','endurance']) k ON CONFLICT DO NOTHING;
  -- Immediate removal of a current reference, including child deletes, in the
  -- fact transaction; immutable historical artifacts remain inspectable.
  DELETE FROM analysis_publications WHERE dataset_id=NEW.dataset_id AND scope_key=NEW.scope_key;
  PERFORM rai_feature_downstream_dirty(NEW.dataset_id,NEW.scope_key,NEW.dirty_from,NEW.dirty_through);
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd

-- External-only slot metadata cannot implicitly enroll M1.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rai_feature_staged() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE w record; c record;
BEGIN
 IF TG_TABLE_NAME='series_uploads' THEN
  FOR w IN SELECT x.workout_uuid,x.event_start,x.event_end FROM workout_samples a JOIN workouts x USING(dataset_id,workout_uuid)
   WHERE a.dataset_id=NEW.dataset_id AND a.sample_type=NEW.sample_type AND a.sample_uuid=NEW.sample_uuid LOOP
   FOR c IN SELECT stack_id FROM analysis_model_channels WHERE dataset_id=NEW.dataset_id AND channel='candidate' AND NOT (extension_configs @> '{"rai.vo2max.external_only/v1":true}') LOOP
    PERFORM rai_analysis_dirty(NEW.dataset_id,'model/'||c.stack_id||'/workout/'||w.workout_uuid,'analysis.workout_features',w.event_start,w.event_end);
   END LOOP;
  END LOOP;
 ELSE
  SELECT workout_uuid,event_start,event_end INTO w FROM workouts WHERE dataset_id=NEW.dataset_id AND workout_uuid=NEW.workout_uuid;
  IF FOUND THEN
   FOR c IN SELECT stack_id FROM analysis_model_channels WHERE dataset_id=NEW.dataset_id AND channel='candidate' AND NOT (extension_configs @> '{"rai.vo2max.external_only/v1":true}') LOOP
    PERFORM rai_analysis_dirty(NEW.dataset_id,'model/'||c.stack_id||'/workout/'||w.workout_uuid,'analysis.workout_features',w.event_start,w.event_end);
   END LOOP;
  END IF;
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION rai_vo2_external_device() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
BEGIN
 IF (NEW.revoked_at IS DISTINCT FROM OLD.revoked_at OR NEW.can_collect IS DISTINCT FROM OLD.can_collect) AND EXISTS(SELECT 1 FROM public.collector_leases WHERE dataset_id=NEW.dataset_id AND device_id=NEW.id) THEN
 PERFORM public.rai_vo2_external_dirty(NEW.dataset_id); END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION rai_vo2_external_device() FROM PUBLIC;
CREATE TRIGGER vo2_external_device AFTER UPDATE ON devices FOR EACH ROW EXECUTE FUNCTION rai_vo2_external_device();
