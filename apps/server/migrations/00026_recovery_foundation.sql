-- +goose Up
-- M5B candidate artifacts only. No raw schema, raw lifecycle or legacy changes.
ALTER TABLE analysis_artifacts DROP CONSTRAINT analysis_artifacts_check;
ALTER TABLE analysis_artifacts ADD CHECK(
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
 alg:=CASE NEW.artifact_key WHEN 'daily_health_features' THEN 'rai.recovery.physiological' WHEN 'daily_health_baseline' THEN 'rai.recovery.physiological' WHEN 'physiological_recovery' THEN 'rai.recovery.physiological' WHEN 'vo2max_external_projection' THEN 'rai.vo2max.external_projection' WHEN 'workout_features' THEN 'rai.workout_features' WHEN 'hr_exposure' THEN 'rai.workout_features'
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


ALTER TABLE analysis_model_channels ADD CONSTRAINT recovery_candidate_entry CHECK (
 NOT extension_configs ? 'rai.recovery.physiological/v1' OR COALESCE(channel='candidate' AND
 jsonb_typeof(extension_configs->'rai.recovery.physiological/v1')='object' AND
 extension_configs->'rai.recovery.physiological/v1'->>'configHash' ~ '^[0-9a-f]{64}$' AND
 extension_configs->'rai.recovery.physiological/v1'->>'assignmentID' ~ '^[0-9a-f-]{36}$' AND
 length(extension_configs->'rai.recovery.physiological/v1'->>'timezone') BETWEEN 1 AND 128 AND
 extension_configs->'rai.recovery.physiological/v1'->>'scopePrefix' ~ '^daily/recovery/v1/[0-9a-f]{16}/$',false));
ALTER TABLE analysis_results ADD CONSTRAINT physiological_recovery_metric CHECK (
 metric<>'physiological_recovery' OR (algorithm_name='rai.recovery.physiological' AND algorithm_version='v1' AND unit='score_0_100' AND (value IS NULL OR value BETWEEN 0 AND 100)));
ALTER TABLE analysis_publications ADD CONSTRAINT physiological_recovery_family CHECK (
 metric_family<>'physiological_recovery_candidate' OR scope_key ~ '^daily/recovery/v1/[0-9a-f]{16}/[0-9]{4}-[0-9]{2}-[0-9]{2}$');
ALTER TABLE admin_operations DROP CONSTRAINT admin_operations_operation_type_check;
ALTER TABLE admin_operations DROP CONSTRAINT admin_operations_check;
ALTER TABLE admin_operations ADD CHECK(operation_type IN ('new_dataset','disaster_recovery','feature_candidate','vo2max_external_candidate','vo2max_source_selection','recovery_candidate'));
ALTER TABLE admin_operations ADD CHECK((operation_type IN ('feature_candidate','vo2max_external_candidate','vo2max_source_selection','recovery_candidate') AND subject_dataset IS NOT NULL AND source_dataset IS NULL AND target_dataset IS NULL) OR (operation_type IN ('new_dataset','disaster_recovery') AND target_dataset IS NOT NULL AND (operation_type='disaster_recovery' OR source_dataset IS NOT NULL)));
ALTER TABLE analysis_model_dependencies DROP CONSTRAINT analysis_model_dependencies_check;
ALTER TABLE analysis_model_dependencies ADD CHECK(
 (upstream_scope ~ '^model/[^/]+/(workout|workout_load)/[0-9a-f-]{36}$' AND
 downstream_scope ~ '^model/[^/]+/(athlete/(load|performance|vo2|ability|endurance)|workout_load/[0-9a-f-]{36})$' AND
 upstream_scope<>downstream_scope AND (upstream_scope LIKE '%/workout/%' OR downstream_scope LIKE '%/athlete/%')) OR
 (upstream_scope IN ('external/vo2max/facts/v1','external/vo2max/selection/v1') AND downstream_scope='external/vo2max/v1/athlete/current') OR
 (upstream_scope ~ '^daily/(raw|features|baseline|recovery)/' AND downstream_scope ~ '^daily/(features|baseline|recovery|readiness)/' AND upstream_scope<>downstream_scope));

-- Existing output days are bounded recompute targets; no nonexistent lifetime
-- days are materialized. New days are created by explicit admin/day advancement.
-- +goose StatementBegin
CREATE FUNCTION rai_recovery_dirty_day(p_dataset uuid,p_day date) RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE e jsonb; scope text; tz text; feature text; baseline text; readiness text;
BEGIN
 PERFORM 1 FROM public.datasets WHERE id=p_dataset FOR UPDATE;
 SELECT extension_configs->'rai.recovery.physiological/v1' INTO e FROM public.analysis_model_channels WHERE dataset_id=p_dataset AND channel='candidate';
 IF e IS NULL THEN RETURN; END IF;
 tz:=e->>'timezone'; scope:=(e->>'scopePrefix')||p_day::text;
 feature:=replace(scope,'/recovery/','/features/');baseline:=replace(scope,'/recovery/','/baseline/');readiness:=replace(scope,'/recovery/','/readiness/');
 INSERT INTO public.analysis_model_dependencies(dataset_id,upstream_scope,downstream_scope)
 VALUES(p_dataset,replace(scope,'/recovery/','/raw/'),feature),(p_dataset,feature,scope),(p_dataset,baseline,scope),(p_dataset,scope,readiness) ON CONFLICT DO NOTHING;
 INSERT INTO public.analysis_model_dependencies(dataset_id,upstream_scope,downstream_scope)
 SELECT p_dataset,replace(e->>'scopePrefix','/recovery/','/features/')||(p_day-n)::text,baseline FROM generate_series(1,28) n ON CONFLICT DO NOTHING;
 -- Durable future dependency dirtiness, deliberately without a readiness job.
 INSERT INTO public.dirty_scopes(dataset_id,scope_key,generation,dirty_from,dirty_through)
 VALUES(p_dataset,readiness,1,p_day::timestamp AT TIME ZONE tz,(p_day+1)::timestamp AT TIME ZONE tz)
 ON CONFLICT(dataset_id,scope_key) DO UPDATE SET generation=dirty_scopes.generation+1;

 DELETE FROM public.analysis_publications WHERE dataset_id=p_dataset AND scope_key=scope AND metric_family='physiological_recovery_candidate';
 PERFORM public.rai_analysis_dirty(p_dataset,scope,'analysis.physiological_recovery',p_day::timestamp AT TIME ZONE tz,(p_day+1)::timestamp AT TIME ZONE tz);
END $$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION rai_recovery_dirty_day(uuid,date) FROM PUBLIC;

-- +goose StatementBegin
CREATE FUNCTION rai_recovery_invalidate() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE ds uuid; e jsonb; tz text; a date; b date; r record; first_at timestamptz; last_at timestamptz; kind text; all_days boolean:=false;
BEGIN
 -- Other health types never enter this graph or even read candidate state.
 IF TG_TABLE_NAME IN ('health_samples','entity_tombstones') THEN
  IF TG_OP='DELETE' THEN kind:=OLD.sample_type; ELSE kind:=NEW.sample_type; END IF;
  IF kind NOT IN ('HKQuantityTypeIdentifierHeartRateVariabilitySDNN','HKQuantityTypeIdentifierRestingHeartRate','HKCategoryTypeIdentifierSleepAnalysis') THEN RETURN NULL; END IF;
 END IF;
 IF TG_TABLE_NAME='datasets' THEN
  IF NEW.timezone IS NOT DISTINCT FROM OLD.timezone AND NEW.epoch IS NOT DISTINCT FROM OLD.epoch AND NEW.retired_at IS NOT DISTINCT FROM OLD.retired_at THEN RETURN NEW; END IF;
  ds:=NEW.id; all_days:=true;
 ELSE
  IF TG_OP='DELETE' THEN ds:=OLD.dataset_id; ELSE ds:=NEW.dataset_id; END IF;
 END IF;
 SELECT extension_configs->'rai.recovery.physiological/v1' INTO e FROM public.analysis_model_channels WHERE dataset_id=ds AND channel='candidate';
 IF TG_TABLE_NAME='analysis_model_channels' THEN
  IF TG_OP='UPDATE' AND NEW.extension_configs->'rai.recovery.physiological/v1' IS NOT DISTINCT FROM OLD.extension_configs->'rai.recovery.physiological/v1' THEN RETURN NEW; END IF;
  DELETE FROM public.analysis_publications WHERE dataset_id=ds AND metric_family='physiological_recovery_candidate';
  IF e IS NULL THEN RETURN NULL; END IF;
  all_days:=true;
 END IF;
 IF e IS NULL THEN RETURN NULL; END IF;
 PERFORM 1 FROM public.datasets WHERE id=ds FOR UPDATE;
 tz:=e->>'timezone';
 IF TG_TABLE_NAME='health_samples' OR TG_TABLE_NAME='entity_tombstones' THEN
  IF TG_TABLE_NAME='entity_tombstones' THEN kind:=NEW.sample_type;first_at:=NEW.previous_start;last_at:=NEW.previous_end;
  ELSIF TG_OP='DELETE' THEN kind:=OLD.sample_type;first_at:=OLD.event_start;last_at:=OLD.event_end;
  ELSE kind:=NEW.sample_type;first_at:=NEW.event_start;last_at:=NEW.event_end; END IF;
  IF kind NOT IN ('HKQuantityTypeIdentifierHeartRateVariabilitySDNN','HKQuantityTypeIdentifierRestingHeartRate','HKCategoryTypeIdentifierSleepAnalysis') THEN RETURN NULL; END IF;
  IF first_at IS NULL THEN
   -- Unseen UUIDs cannot occur in prior input lineage. Keep the raw barrier;
   -- do not invent an event day or enqueue lifetime history.
   RETURN NULL;
  ELSE
   a:=(last_at AT TIME ZONE tz)::date;b:=a+28;
   IF kind='HKCategoryTypeIdentifierSleepAnalysis' THEN
    IF last_at-first_at<=interval '24 hours' THEN a:=(first_at AT TIME ZONE tz)::date-1; ELSE a:=a-1; END IF;
    b:=b+2;
   END IF;
  END IF;
 ELSIF TG_TABLE_NAME='daily_health_coverage' THEN
  SELECT sample_type INTO kind FROM public.sync_streams WHERE dataset_id=ds AND id=NEW.stream_id;
  IF kind NOT IN ('HKQuantityTypeIdentifierHeartRateVariabilitySDNN','HKQuantityTypeIdentifierRestingHeartRate','HKCategoryTypeIdentifierSleepAnalysis') THEN RETURN NULL; END IF;
  -- A new observation fences computations, but does not enqueue all history.
  -- Existing requested days only, whose 28-day evidence range intersects it.
  a:=(to_timestamp(NEW.last_discovery_at_us/1000000.0) AT TIME ZONE tz)::date;b:=a;
  IF TG_OP='UPDATE' AND (NEW.bootstrap_complete IS DISTINCT FROM OLD.bootstrap_complete OR NEW.has_more IS DISTINCT FROM OLD.has_more) THEN DELETE FROM public.analysis_publications WHERE dataset_id=ds AND metric_family='physiological_recovery_candidate'; END IF;
 ELSIF TG_TABLE_NAME='collector_leases' THEN
  IF TG_OP='UPDATE' AND NEW.collector_epoch IS NOT DISTINCT FROM OLD.collector_epoch THEN RETURN NEW; END IF;all_days:=true;
 ELSIF TG_TABLE_NAME='devices' THEN
  IF NEW.revoked_at IS NOT DISTINCT FROM OLD.revoked_at AND NEW.can_collect IS NOT DISTINCT FROM OLD.can_collect THEN RETURN NEW; END IF;all_days:=true;
 END IF;
 IF all_days THEN
  DELETE FROM public.analysis_publications WHERE dataset_id=ds AND metric_family='physiological_recovery_candidate';
  -- Identity changes withdraw old current pointers. Historical revised work is
  -- explicit; automatic recompute is limited to the live 30-day context.
  a:=(clock_timestamp() AT TIME ZONE tz)::date-30;b:=(clock_timestamp() AT TIME ZONE tz)::date;
 END IF;
 FOR r IN SELECT right(scope_key,10)::date AS day FROM public.dirty_scopes WHERE dataset_id=ds AND scope_key LIKE (e->>'scopePrefix')||'%' AND right(scope_key,10)::date BETWEEN a AND b ORDER BY scope_key LOOP
  PERFORM public.rai_recovery_dirty_day(ds,r.day);
 END LOOP;
 RETURN NULL;
END $$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION rai_recovery_invalidate() FROM PUBLIC;
CREATE TRIGGER recovery_fact AFTER INSERT OR DELETE ON health_samples FOR EACH ROW EXECUTE FUNCTION rai_recovery_invalidate();
CREATE TRIGGER recovery_delete AFTER INSERT ON entity_tombstones FOR EACH ROW EXECUTE FUNCTION rai_recovery_invalidate();
CREATE TRIGGER recovery_coverage AFTER INSERT OR UPDATE ON daily_health_coverage FOR EACH ROW EXECUTE FUNCTION rai_recovery_invalidate();
CREATE TRIGGER recovery_assignment AFTER INSERT OR UPDATE OR DELETE ON analysis_model_channels FOR EACH ROW EXECUTE FUNCTION rai_recovery_invalidate();
CREATE TRIGGER recovery_dataset AFTER UPDATE ON datasets FOR EACH ROW EXECUTE FUNCTION rai_recovery_invalidate();
CREATE TRIGGER recovery_collector AFTER INSERT OR UPDATE ON collector_leases FOR EACH ROW EXECUTE FUNCTION rai_recovery_invalidate();
CREATE TRIGGER recovery_device AFTER UPDATE ON devices FOR EACH ROW EXECUTE FUNCTION rai_recovery_invalidate();

-- Bounded range scans, including a two-day episode context. Long raw intervals
-- are not truncated; the pure model rejects ambiguous >24h episodes.
CREATE INDEX recovery_daily_window ON health_samples(dataset_id,sample_type,event_end,sample_uuid)
 WHERE sample_type IN ('HKQuantityTypeIdentifierHeartRateVariabilitySDNN','HKQuantityTypeIdentifierRestingHeartRate','HKCategoryTypeIdentifierSleepAnalysis');
CREATE INDEX recovery_daily_deleted ON entity_tombstones(dataset_id,sample_type,previous_end)
 WHERE sample_type IN ('HKQuantityTypeIdentifierHeartRateVariabilitySDNN','HKQuantityTypeIdentifierRestingHeartRate','HKCategoryTypeIdentifierSleepAnalysis');
-- No Down: preserve immutable model evidence.
