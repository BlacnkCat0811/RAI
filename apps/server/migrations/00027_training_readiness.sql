-- +goose Up
-- M6 candidate only. No mutable readiness current table or raw semantics change.
ALTER TABLE analysis_artifacts DROP CONSTRAINT analysis_artifacts_check;
ALTER TABLE analysis_artifacts ADD CHECK(
 (artifact_key='training_readiness' AND schema_version='rai.readiness.training/1' AND octet_length(payload::text)<=2097152) OR
 ((artifact_key='daily_health_features' AND schema_version='rai.daily_health_features/1' OR artifact_key='daily_health_baseline' AND schema_version='rai.daily_health_baseline/1' OR artifact_key='physiological_recovery' AND schema_version='rai.recovery.physiological/1') AND octet_length(payload::text)<=2097152) OR
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
 alg:=CASE NEW.artifact_key WHEN 'training_readiness' THEN 'rai.readiness.training' WHEN 'daily_health_features' THEN 'rai.recovery.physiological' WHEN 'daily_health_baseline' THEN 'rai.recovery.physiological' WHEN 'physiological_recovery' THEN 'rai.recovery.physiological' WHEN 'vo2max_external_projection' THEN 'rai.vo2max.external_projection' WHEN 'workout_features' THEN 'rai.workout_features' WHEN 'hr_exposure' THEN 'rai.workout_features'
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



ALTER TABLE analysis_model_channels ADD CONSTRAINT readiness_candidate_entry CHECK (
 NOT extension_configs ? 'rai.readiness.training/v1' OR COALESCE(channel='candidate' AND
 jsonb_typeof(extension_configs->'rai.readiness.training/v1')='object' AND
 extension_configs->'rai.readiness.training/v1'->>'configHash' ~ '^[0-9a-f]{64}$' AND
 extension_configs->'rai.readiness.training/v1'->>'assignmentID' ~ '^[0-9a-f-]{36}$' AND
 length(extension_configs->'rai.readiness.training/v1'->>'timezone') BETWEEN 1 AND 128 AND
 extension_configs->'rai.readiness.training/v1'->>'scope' ~ '^instant/readiness/v1/[0-9a-f]{16}/current$',false));
ALTER TABLE analysis_results ADD CONSTRAINT training_readiness_metric CHECK (
 metric<>'training_readiness' OR (algorithm_name='rai.readiness.training' AND algorithm_version='v1' AND unit='score_0_100' AND (value IS NULL OR value BETWEEN 0 AND 100)));
ALTER TABLE analysis_publications ADD CONSTRAINT training_readiness_family CHECK (
 metric_family<>'training_readiness_candidate' OR scope_key ~ '^instant/readiness/v1/[0-9a-f]{16}/current$');
ALTER TABLE admin_operations DROP CONSTRAINT admin_operations_operation_type_check;
ALTER TABLE admin_operations DROP CONSTRAINT admin_operations_check;
ALTER TABLE admin_operations ADD CHECK(operation_type IN ('new_dataset','disaster_recovery','feature_candidate','vo2max_external_candidate','vo2max_source_selection','recovery_candidate','readiness_candidate'));
ALTER TABLE admin_operations ADD CHECK((operation_type IN ('feature_candidate','vo2max_external_candidate','vo2max_source_selection','recovery_candidate','readiness_candidate') AND subject_dataset IS NOT NULL AND source_dataset IS NULL AND target_dataset IS NULL) OR (operation_type IN ('new_dataset','disaster_recovery') AND target_dataset IS NOT NULL AND (operation_type='disaster_recovery' OR source_dataset IS NOT NULL)));
ALTER TABLE analysis_model_dependencies DROP CONSTRAINT analysis_model_dependencies_check;
ALTER TABLE analysis_model_dependencies ADD CHECK(
 (downstream_scope ~ '^instant/readiness/v1/[0-9a-f]{16}/current$' AND (upstream_scope ~ '^model/[^/]+/workout_load/[0-9a-f-]{36}$' OR upstream_scope ~ '^daily/recovery/v1/[0-9a-f]{16}/[0-9]{4}-[0-9]{2}-[0-9]{2}$' OR upstream_scope='readiness/load-coverage/v1')) OR
 (upstream_scope ~ '^model/[^/]+/(workout|workout_load)/[0-9a-f-]{36}$' AND
 downstream_scope ~ '^model/[^/]+/(athlete/(load|performance|vo2|ability|endurance)|workout_load/[0-9a-f-]{36})$' AND
 upstream_scope<>downstream_scope AND (upstream_scope LIKE '%/workout/%' OR downstream_scope LIKE '%/athlete/%')) OR
 (upstream_scope IN ('external/vo2max/facts/v1','external/vo2max/selection/v1') AND downstream_scope='external/vo2max/v1/athlete/current') OR
 (upstream_scope ~ '^daily/(raw|features|baseline|recovery)/' AND downstream_scope ~ '^daily/(features|baseline|recovery|readiness)/' AND upstream_scope<>downstream_scope));


-- +goose StatementBegin
CREATE FUNCTION rai_readiness_dirty(p_dataset uuid) RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE e jsonb; tz text; start_at timestamptz; end_at timestamptz;
BEGIN
 PERFORM 1 FROM public.datasets WHERE id=p_dataset FOR UPDATE;
 SELECT extension_configs->'rai.readiness.training/v1' INTO e FROM public.analysis_model_channels WHERE dataset_id=p_dataset AND channel='candidate';
 DELETE FROM public.analysis_publications WHERE dataset_id=p_dataset AND metric_family='training_readiness_candidate';
 IF e IS NULL THEN RETURN; END IF;
 tz:=e->>'timezone';
 start_at:=date_trunc('day',clock_timestamp(),tz);end_at:=( (clock_timestamp() AT TIME ZONE tz)::date+1)::timestamp AT TIME ZONE tz;
 PERFORM public.rai_analysis_dirty(p_dataset,e->>'scope','analysis.training_readiness',start_at,end_at);
END $$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION rai_readiness_dirty(uuid) FROM PUBLIC;

-- Every relevant write invalidates in its own dataset transaction. Pointer
-- deletion chains Recovery withdrawal to Readiness; new raw Workouts fence even
-- when their M2 artifact has not yet been produced. No analysis of raw signals.
-- +goose StatementBegin
CREATE FUNCTION rai_readiness_invalidate() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE ds uuid; e jsonb; tz text; finish timestamptz; scope text; family text; relevant boolean:=false;
BEGIN
 IF TG_TABLE_NAME='datasets' THEN
  IF NEW.timezone IS NOT DISTINCT FROM OLD.timezone AND NEW.epoch IS NOT DISTINCT FROM OLD.epoch AND NEW.retired_at IS NOT DISTINCT FROM OLD.retired_at THEN RETURN NEW; END IF;
  ds:=NEW.id;
 ELSE
  IF TG_OP='DELETE' THEN ds:=OLD.dataset_id; ELSE ds:=NEW.dataset_id; END IF;
 END IF;
 IF TG_TABLE_NAME='analysis_publications' THEN
  IF TG_OP='DELETE' THEN family:=OLD.metric_family;scope:=OLD.scope_key; ELSE family:=NEW.metric_family;scope:=NEW.scope_key; END IF;
  IF family NOT IN ('physiological_recovery_candidate','workout_load_candidate') THEN RETURN NULL; END IF;
 END IF;
 IF TG_TABLE_NAME='dirty_scopes' THEN
  IF NEW.scope_key NOT LIKE 'model/%/workout_load/%' OR TG_OP='UPDATE' AND NEW.generation=OLD.generation THEN RETURN NEW; END IF;
 END IF;
 IF TG_TABLE_NAME='sync_streams' THEN
  IF TG_OP='DELETE' THEN IF OLD.sample_type<>'HKWorkoutTypeIdentifier' THEN RETURN NULL; END IF;
  ELSE IF NEW.sample_type<>'HKWorkoutTypeIdentifier' THEN RETURN NULL; END IF; END IF;
 END IF;
 SELECT extension_configs->'rai.readiness.training/v1' INTO e FROM public.analysis_model_channels WHERE dataset_id=ds AND channel='candidate';
 IF TG_TABLE_NAME='analysis_model_channels' THEN
  IF TG_OP='UPDATE' AND NEW.extension_configs->'rai.readiness.training/v1' IS NOT DISTINCT FROM OLD.extension_configs->'rai.readiness.training/v1' AND NEW.stack_id IS NOT DISTINCT FROM OLD.stack_id AND NEW.config_sha256 IS NOT DISTINCT FROM OLD.config_sha256 AND NEW.assignment_version IS NOT DISTINCT FROM OLD.assignment_version THEN RETURN NEW; END IF;
  DELETE FROM public.analysis_publications WHERE dataset_id=ds AND metric_family='training_readiness_candidate';
 END IF;
 IF e IS NULL THEN RETURN NULL; END IF;
 tz:=e->>'timezone';
 IF TG_TABLE_NAME='workouts' THEN
  IF TG_OP='DELETE' THEN finish:=OLD.event_end;relevant:=OLD.payload->'workout'->>'activityType'='37'; ELSE finish:=NEW.event_end;relevant:=NEW.payload->'workout'->>'activityType'='37'; END IF;
  IF NOT relevant THEN RETURN NULL; END IF;
 ELSIF TG_TABLE_NAME='analysis_publications' THEN
  IF family='physiological_recovery_candidate' THEN
   -- instant and daily share the timezone hash; Recovery must be today's day.
   relevant:=scope='daily/recovery/v1/'||split_part(e->>'scope','/',4)||'/'||(clock_timestamp() AT TIME ZONE tz)::date::text;
   IF NOT relevant THEN RETURN NULL; END IF;
  ELSE
   SELECT event_end INTO finish FROM public.workouts WHERE dataset_id=ds AND workout_uuid=right(scope,36)::uuid;
   -- The raw delete trigger handles a disappeared Workout as well.
   IF finish IS NULL THEN RETURN NULL; END IF;
  END IF;
 ELSIF TG_TABLE_NAME='dirty_scopes' THEN
  SELECT event_end INTO finish FROM public.workouts WHERE dataset_id=ds AND workout_uuid=right(NEW.scope_key,36)::uuid;
  IF finish IS NULL THEN RETURN NEW; END IF;
 ELSIF TG_TABLE_NAME='collector_leases' THEN
  IF TG_OP='UPDATE' AND NEW.collector_epoch IS NOT DISTINCT FROM OLD.collector_epoch THEN RETURN NEW; END IF;
 ELSIF TG_TABLE_NAME='devices' THEN
  IF NEW.revoked_at IS NOT DISTINCT FROM OLD.revoked_at AND NEW.can_collect IS NOT DISTINCT FROM OLD.can_collect THEN RETURN NEW; END IF;
 END IF;
 IF finish IS NOT NULL AND (finish<clock_timestamp()-interval '180 days' OR finish>=((clock_timestamp() AT TIME ZONE tz)::date+1)::timestamp AT TIME ZONE tz) THEN RETURN NULL; END IF;
 PERFORM public.rai_readiness_dirty(ds);
 RETURN NULL;
END $$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION rai_readiness_invalidate() FROM PUBLIC;
CREATE TRIGGER readiness_workout AFTER INSERT OR DELETE ON workouts FOR EACH ROW EXECUTE FUNCTION rai_readiness_invalidate();
CREATE TRIGGER readiness_upstream AFTER INSERT OR UPDATE OR DELETE ON analysis_publications FOR EACH ROW EXECUTE FUNCTION rai_readiness_invalidate();
CREATE TRIGGER readiness_load_revision AFTER INSERT OR UPDATE ON dirty_scopes FOR EACH ROW EXECUTE FUNCTION rai_readiness_invalidate();
CREATE TRIGGER readiness_assignment AFTER INSERT OR UPDATE OR DELETE ON analysis_model_channels FOR EACH ROW EXECUTE FUNCTION rai_readiness_invalidate();
CREATE TRIGGER readiness_dataset AFTER UPDATE ON datasets FOR EACH ROW EXECUTE FUNCTION rai_readiness_invalidate();
CREATE TRIGGER readiness_collector AFTER INSERT OR UPDATE ON collector_leases FOR EACH ROW EXECUTE FUNCTION rai_readiness_invalidate();
CREATE TRIGGER readiness_device AFTER UPDATE ON devices FOR EACH ROW EXECUTE FUNCTION rai_readiness_invalidate();
CREATE TRIGGER readiness_stream AFTER INSERT OR UPDATE OR DELETE ON sync_streams FOR EACH ROW EXECUTE FUNCTION rai_readiness_invalidate();
CREATE INDEX readiness_workout_end ON workouts(dataset_id,event_end,workout_uuid) WHERE payload->'workout'->>'activityType'='37';
-- No destructive Down: retain immutable candidate evidence.

-- Fixed artifact-derived edges; the worker receives no general graph-write role.
-- +goose StatementBegin
CREATE FUNCTION rai_readiness_edges() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE scope text;
BEGIN
 IF NEW.artifact_key<>'training_readiness' THEN RETURN NEW; END IF;
 SELECT scope_key INTO scope FROM public.analysis_runs WHERE dataset_id=NEW.dataset_id AND id=NEW.run_id AND algorithm_name='rai.readiness.training' AND status='RUNNING';
 IF scope IS NULL THEN RAISE EXCEPTION 'readiness_invalid_run' USING ERRCODE='RA041'; END IF;
 INSERT INTO public.analysis_model_dependencies(dataset_id,upstream_scope,downstream_scope)
 SELECT NEW.dataset_id,'model/'||m.stack_id||'/workout_load/'||(f->>'workoutID'),scope FROM jsonb_array_elements(NEW.payload->'inputRefs') f JOIN public.analysis_model_channels m ON m.dataset_id=NEW.dataset_id AND m.channel='candidate' ON CONFLICT DO NOTHING;
 INSERT INTO public.analysis_model_dependencies(dataset_id,upstream_scope,downstream_scope)
 VALUES(NEW.dataset_id,'daily/recovery/v1/'||split_part(scope,'/',4)||'/'||(NEW.payload->>'date'),scope),(NEW.dataset_id,'readiness/load-coverage/v1',scope) ON CONFLICT DO NOTHING;
 RETURN NEW;
END $$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION rai_readiness_edges() FROM PUBLIC;
CREATE TRIGGER readiness_edges AFTER INSERT ON analysis_artifacts FOR EACH ROW EXECUTE FUNCTION rai_readiness_edges();

-- Preserve all earlier model branches; activate only separately enrolled M6 edges.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rai_feature_downstream_dirty(p_dataset uuid,p_scope text,p_from timestamptz,p_through timestamptz)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE d record; load_enabled boolean; performance_enabled boolean;
BEGIN
 SELECT EXISTS(SELECT 1 FROM analysis_model_dependencies WHERE dataset_id=p_dataset AND downstream_scope LIKE 'model/%/workout_load/%') INTO load_enabled;
 SELECT EXISTS(SELECT 1 FROM analysis_model_channels WHERE dataset_id=p_dataset AND channel='candidate' AND extension_configs ? 'rai.performance_curve/v1') INTO performance_enabled;
 FOR d IN SELECT downstream_scope FROM analysis_model_dependencies WHERE dataset_id=p_dataset AND upstream_scope=p_scope ORDER BY downstream_scope LOOP
  IF d.downstream_scope LIKE 'instant/readiness/v1/%/current' THEN
   -- An actual persisted M6 dependency always receives a runnable generation,
   -- including an anchor ref that aged just past the finite window boundary.
   PERFORM rai_readiness_dirty(p_dataset);CONTINUE;
  ELSIF d.downstream_scope LIKE 'model/%/workout_load/%' THEN
   PERFORM rai_analysis_dirty(p_dataset,d.downstream_scope,'analysis.training_load',p_from,p_through);
  ELSIF load_enabled AND d.downstream_scope LIKE 'model/%/athlete/load' THEN
   PERFORM rai_analysis_dirty(p_dataset,d.downstream_scope,'analysis.load_state',p_from,p_through);
  ELSIF performance_enabled AND d.downstream_scope LIKE 'model/%/athlete/performance' THEN
   -- Changes outside the live 90-day window cannot revise current M3. Historical
   -- artifacts remain immutable and an explicit bounded rebuild is available.
   IF (p_through<clock_timestamp()-interval '90 days' OR p_from>clock_timestamp()) AND NOT EXISTS(
    SELECT 1 FROM analysis_publications p JOIN analysis_runs r ON r.dataset_id=p.dataset_id AND r.id=p.published_run_id,
    LATERAL jsonb_array_elements(COALESCE(r.lineage->'featureRefs','[]')) ref
    WHERE p.dataset_id=p_dataset AND p.scope_key=d.downstream_scope AND ref->>'workoutID'=split_part(p_scope,'/workout/',2)) THEN CONTINUE; END IF;
   PERFORM rai_analysis_dirty(p_dataset,d.downstream_scope,'analysis.performance',p_from,p_through);
  ELSE
   INSERT INTO dirty_scopes(dataset_id,scope_key,generation,dirty_from,dirty_through)
   VALUES(p_dataset,d.downstream_scope,1,p_from,p_through)
   ON CONFLICT(dataset_id,scope_key) DO UPDATE SET generation=dirty_scopes.generation+1,
    dirty_from=CASE WHEN dirty_scopes.generation=dirty_scopes.clean_generation THEN EXCLUDED.dirty_from ELSE LEAST(dirty_scopes.dirty_from,EXCLUDED.dirty_from) END,
    dirty_through=CASE WHEN dirty_scopes.generation=dirty_scopes.clean_generation THEN EXCLUDED.dirty_through ELSE GREATEST(dirty_scopes.dirty_through,EXCLUDED.dirty_through) END;
  END IF;
  IF load_enabled AND d.downstream_scope LIKE 'model/%/athlete/load' THEN
   DELETE FROM analysis_publications p USING datasets x WHERE x.id=p_dataset AND p.dataset_id=p_dataset AND p.scope_key=d.downstream_scope
   AND p.day>=(p_from AT TIME ZONE x.timezone)::date AND p.day<=((p_through-interval '1 microsecond') AT TIME ZONE x.timezone)::date+186;
  ELSE
   DELETE FROM analysis_publications WHERE dataset_id=p_dataset AND scope_key=d.downstream_scope;
  END IF;
 END LOOP;
END $$;
-- +goose StatementEnd
