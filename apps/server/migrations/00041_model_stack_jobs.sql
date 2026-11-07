-- +goose Up
-- Managed v2 stack scopes are independent of the legacy candidate triggers.
-- The product day scope fences all six slots and remains a publication unit.
-- +goose StatementBegin
CREATE FUNCTION rai_schedule_model_day(p_dataset uuid,p_stack text,p_day date,p_at timestamptz)
RETURNS uuid LANGUAGE plpgsql AS $$
DECLARE scope text; g bigint; jid uuid;
BEGIN
 scope:='model/'||p_stack||'/athlete/day/v2/'||p_day::text;
 PERFORM rai_analysis_dirty(p_dataset,scope,'analysis.model_stack_v2',p_at,p_at);
 SELECT generation INTO STRICT g FROM dirty_scopes WHERE dataset_id=p_dataset AND scope_key=scope;
 SELECT id INTO STRICT jid FROM jobs WHERE dataset_id=p_dataset AND scope_key=scope AND kind='analysis.model_stack_v2' AND target_generation=g;
 RETURN jid;
END $$;
-- +goose StatementEnd

-- Invalidate existing historical pointers in the maximum affected civil-day
-- interval. A fact transaction only schedules current work; historical rebuild
-- requires an explicit bounded operation.
-- +goose StatementBegin
CREATE FUNCTION rai_model_stack_invalidate(p_dataset uuid,p_from timestamptz,p_through timestamptz,p_workout text)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE m record; tz text; today date; day_prefix text; affected_from date; affected_through date;
BEGIN
 SELECT timezone INTO tz FROM datasets WHERE id=p_dataset AND retired_at IS NULL FOR UPDATE;
 IF NOT FOUND THEN RETURN; END IF;
 today:=(clock_timestamp() AT TIME ZONE tz)::date;
 affected_from:=(p_from AT TIME ZONE tz)::date;
 affected_through:=(p_through AT TIME ZONE tz)::date+214;
 FOR m IN SELECT DISTINCT stack_id FROM analysis_model_channels
  WHERE dataset_id=p_dataset AND extension_configs ? 'rai.model_manifest/1' ORDER BY stack_id
 LOOP
  day_prefix:='model/'||m.stack_id||'/athlete/day/v2/';
  UPDATE dirty_scopes SET generation=generation+1,dirty_from=LEAST(dirty_from,p_from),dirty_through=GREATEST(dirty_through,p_through)
   WHERE dataset_id=p_dataset AND scope_key >= day_prefix||affected_from::text
    AND scope_key < day_prefix||LEAST(today,affected_through+1)::text
    AND left(scope_key,length(day_prefix))=day_prefix;
  IF p_workout IS NOT NULL THEN
   UPDATE dirty_scopes SET generation=generation+1,dirty_from=LEAST(dirty_from,p_from),dirty_through=GREATEST(dirty_through,p_through)
    WHERE dataset_id=p_dataset AND scope_key='model/'||m.stack_id||'/workout/'||p_workout;
  END IF;
  IF today BETWEEN affected_from AND affected_through THEN
   PERFORM rai_schedule_model_day(p_dataset,m.stack_id,today,clock_timestamp());
  END IF;
 END LOOP;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION rai_model_stack_dirty_trigger() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE ds uuid; from_at timestamptz; through_at timestamptz; wid text;
BEGIN
 IF TG_TABLE_NAME='dirty_scopes' THEN
  IF TG_OP='UPDATE' AND NEW.generation=OLD.generation THEN RETURN NEW; END IF;
  IF NEW.scope_key LIKE 'workout:HKWorkoutTypeIdentifier:%' THEN
   wid:=right(NEW.scope_key,36);
  ELSIF NOT (NEW.scope_key LIKE 'daily/raw/%' OR NEW.scope_key IN
   ('external/vo2max/facts/v1','external/vo2max/selection/v1','readiness/load-coverage/v1')) THEN
   RETURN NEW;
  END IF;
  ds:=NEW.dataset_id;
  from_at:=NEW.dirty_from;
  through_at:=NEW.dirty_through;
 ELSIF TG_TABLE_NAME='athlete_model_inputs' THEN
  ds:=NEW.dataset_id;
  from_at:=LEAST(NEW.effective_from,clock_timestamp());
  through_at:=clock_timestamp();
  IF NEW.revokes_id IS NOT NULL THEN
   SELECT LEAST(effective_from,from_at) INTO from_at FROM athlete_model_inputs
    WHERE dataset_id=ds AND id=NEW.revokes_id;
  END IF;
 ELSIF TG_TABLE_NAME='health_samples' THEN
  IF TG_OP='DELETE' THEN
   IF OLD.sample_type NOT IN ('HKQuantityTypeIdentifierHeartRateVariabilitySDNN','HKQuantityTypeIdentifierRestingHeartRate','HKCategoryTypeIdentifierSleepAnalysis','HKQuantityTypeIdentifierVO2Max') THEN RETURN NULL; END IF;
   ds:=OLD.dataset_id; from_at:=OLD.event_start; through_at:=OLD.event_end;
  ELSE
   IF NEW.sample_type NOT IN ('HKQuantityTypeIdentifierHeartRateVariabilitySDNN','HKQuantityTypeIdentifierRestingHeartRate','HKCategoryTypeIdentifierSleepAnalysis','HKQuantityTypeIdentifierVO2Max') THEN RETURN NULL; END IF;
   ds:=NEW.dataset_id; from_at:=NEW.event_start; through_at:=NEW.event_end;
  END IF;
  from_at:=from_at-interval '1 day';
 ELSIF TG_TABLE_NAME='devices' THEN
  IF NEW.revoked_at IS NOT DISTINCT FROM OLD.revoked_at AND NEW.can_collect IS NOT DISTINCT FROM OLD.can_collect THEN RETURN NEW; END IF;
  ds:=NEW.dataset_id;
  from_at:=clock_timestamp()-interval '366 days';
  through_at:=clock_timestamp();
 ELSIF TG_TABLE_NAME='datasets' THEN
  IF NEW.timezone IS NOT DISTINCT FROM OLD.timezone AND NEW.epoch IS NOT DISTINCT FROM OLD.epoch
   AND NEW.retired_at IS NOT DISTINCT FROM OLD.retired_at THEN RETURN NEW; END IF;
  ds:=NEW.id;
  from_at:=clock_timestamp()-interval '366 days';
  through_at:=clock_timestamp();
 ELSE
  IF TG_OP='DELETE' THEN ds:=OLD.dataset_id; ELSE ds:=NEW.dataset_id; END IF;
  from_at:=clock_timestamp()-interval '366 days';
  through_at:=clock_timestamp();
 END IF;
 IF EXISTS(SELECT 1 FROM analysis_model_channels WHERE dataset_id=ds AND extension_configs ? 'rai.model_manifest/1') THEN
  PERFORM rai_model_stack_invalidate(ds,from_at,through_at,wid);
 END IF;
 RETURN NULL;
END $$;
-- +goose StatementEnd
CREATE TRIGGER model_stack_dirty_generation AFTER INSERT OR UPDATE ON dirty_scopes FOR EACH ROW EXECUTE FUNCTION rai_model_stack_dirty_trigger();
CREATE TRIGGER model_stack_dirty_input AFTER INSERT ON athlete_model_inputs FOR EACH ROW EXECUTE FUNCTION rai_model_stack_dirty_trigger();
CREATE TRIGGER model_stack_dirty_collector AFTER INSERT OR UPDATE OR DELETE ON collector_leases FOR EACH ROW EXECUTE FUNCTION rai_model_stack_dirty_trigger();
CREATE TRIGGER model_stack_dirty_dataset AFTER UPDATE ON datasets FOR EACH ROW EXECUTE FUNCTION rai_model_stack_dirty_trigger();
CREATE TRIGGER model_stack_daily_insert AFTER INSERT ON health_samples FOR EACH ROW
 WHEN (NEW.sample_type IN ('HKQuantityTypeIdentifierHeartRateVariabilitySDNN','HKQuantityTypeIdentifierRestingHeartRate','HKCategoryTypeIdentifierSleepAnalysis','HKQuantityTypeIdentifierVO2Max')) EXECUTE FUNCTION rai_model_stack_dirty_trigger();
CREATE TRIGGER model_stack_daily_delete AFTER DELETE ON health_samples FOR EACH ROW
 WHEN (OLD.sample_type IN ('HKQuantityTypeIdentifierHeartRateVariabilitySDNN','HKQuantityTypeIdentifierRestingHeartRate','HKCategoryTypeIdentifierSleepAnalysis','HKQuantityTypeIdentifierVO2Max')) EXECUTE FUNCTION rai_model_stack_dirty_trigger();
CREATE TRIGGER model_stack_dirty_daily_coverage AFTER INSERT OR UPDATE ON daily_health_coverage FOR EACH ROW EXECUTE FUNCTION rai_model_stack_dirty_trigger();
CREATE TRIGGER model_stack_dirty_device AFTER UPDATE ON devices FOR EACH ROW EXECUTE FUNCTION rai_model_stack_dirty_trigger();
CREATE TRIGGER model_stack_dirty_proof AFTER INSERT ON workout_sync_completeness FOR EACH ROW EXECUTE FUNCTION rai_model_stack_dirty_trigger();

-- A no-Workout day still advances once per stack and local day. The dataset
-- row lock makes this idempotent across workers.
-- +goose StatementBegin
CREATE FUNCTION rai_model_stack_tick(p_dataset uuid,p_at timestamptz)
RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE m record; day date; scope text;
BEGIN
 SELECT (p_at AT TIME ZONE timezone)::date INTO day FROM datasets WHERE id=p_dataset AND retired_at IS NULL FOR UPDATE;
 IF NOT FOUND THEN RETURN; END IF;
 FOR m IN SELECT DISTINCT stack_id FROM analysis_model_channels
  WHERE dataset_id=p_dataset AND extension_configs ? 'rai.model_manifest/1' ORDER BY stack_id
 LOOP
  scope:='model/'||m.stack_id||'/athlete/day/v2/'||day::text;
  IF NOT EXISTS (SELECT 1 FROM dirty_scopes WHERE dataset_id=p_dataset AND scope_key=scope) THEN
   PERFORM rai_schedule_model_day(p_dataset,m.stack_id,day,p_at);
  END IF;
 END LOOP;
END $$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION rai_model_stack_tick(uuid,timestamptz),
 rai_schedule_model_day(uuid,text,date,timestamptz),rai_model_stack_invalidate(uuid,timestamptz,timestamptz,text),
 rai_model_stack_dirty_trigger() FROM PUBLIC;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='rai_worker') THEN
  GRANT EXECUTE ON FUNCTION rai_model_stack_tick(uuid,timestamptz) TO rai_worker;
 END IF;
END $$;
-- +goose StatementEnd

-- External-only slot metadata cannot implicitly enroll M1.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rai_feature_dependency() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE c record; id text; scope text;
BEGIN
 IF TG_OP='UPDATE' AND NEW.generation=OLD.generation THEN RETURN NEW; END IF;
 IF NEW.scope_key LIKE 'workout:HKWorkoutTypeIdentifier:%' THEN
  id:=substring(NEW.scope_key from length('workout:HKWorkoutTypeIdentifier:')+1);
  FOR c IN SELECT DISTINCT stack_id FROM analysis_model_channels WHERE dataset_id=NEW.dataset_id AND channel='candidate' AND NOT (extension_configs ? 'rai.model_manifest/1') AND NOT (extension_configs @> '{"rai.vo2max.external_only/v1":true}') LOOP
   scope:='model/'||c.stack_id||'/workout/'||id;
   PERFORM rai_analysis_dirty(NEW.dataset_id,scope,'analysis.workout_features',NEW.dirty_from,NEW.dirty_through);
  END LOOP;
 ELSIF NEW.scope_key LIKE 'model/%/workout/%' AND NEW.scope_key !~ '^model/rai-v2-[0-9a-f]{16}/' THEN
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
   FOR c IN SELECT stack_id FROM analysis_model_channels WHERE dataset_id=NEW.dataset_id AND channel='candidate' AND NOT (extension_configs ? 'rai.model_manifest/1') AND NOT (extension_configs @> '{"rai.vo2max.external_only/v1":true}') LOOP
    PERFORM rai_analysis_dirty(NEW.dataset_id,'model/'||c.stack_id||'/workout/'||w.workout_uuid,'analysis.workout_features',w.event_start,w.event_end);
   END LOOP;
  END LOOP;
 ELSE
  SELECT workout_uuid,event_start,event_end INTO w FROM workouts WHERE dataset_id=NEW.dataset_id AND workout_uuid=NEW.workout_uuid;
  IF FOUND THEN
   FOR c IN SELECT stack_id FROM analysis_model_channels WHERE dataset_id=NEW.dataset_id AND channel='candidate' AND NOT (extension_configs ? 'rai.model_manifest/1') AND NOT (extension_configs @> '{"rai.vo2max.external_only/v1":true}') LOOP
    PERFORM rai_analysis_dirty(NEW.dataset_id,'model/'||c.stack_id||'/workout/'||w.workout_uuid,'analysis.workout_features',w.event_start,w.event_end);
   END LOOP;
  END IF;
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd


-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rai_readiness_edges() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE scope text;
BEGIN
 IF NEW.artifact_key<>'training_readiness' OR NEW.schema_version<>'rai.readiness.training/1' THEN RETURN NEW; END IF;
 SELECT scope_key INTO scope FROM public.analysis_runs WHERE dataset_id=NEW.dataset_id AND id=NEW.run_id AND algorithm_name='rai.readiness.training' AND status='RUNNING';
 IF scope IS NULL THEN RAISE EXCEPTION 'readiness_invalid_run' USING ERRCODE='RA041'; END IF;
 INSERT INTO public.analysis_model_dependencies(dataset_id,upstream_scope,downstream_scope)
 SELECT NEW.dataset_id,'model/'||m.stack_id||'/workout_load/'||(f->>'workoutID'),scope FROM jsonb_array_elements(NEW.payload->'inputRefs') f JOIN public.analysis_model_channels m ON m.dataset_id=NEW.dataset_id AND m.channel='candidate' ON CONFLICT DO NOTHING;
 INSERT INTO public.analysis_model_dependencies(dataset_id,upstream_scope,downstream_scope)
 VALUES(NEW.dataset_id,'daily/recovery/v1/'||split_part(scope,'/',4)||'/'||(NEW.payload->>'date'),scope),(NEW.dataset_id,'readiness/load-coverage/v1',scope) ON CONFLICT DO NOTHING;
 RETURN NEW;
END $$;
-- +goose StatementEnd

-- Only the managed manifest may travel with an active or previous channel.
-- Legacy candidate-only extension settings retain their existing constraints.
ALTER TABLE analysis_model_channels DROP CONSTRAINT analysis_model_channels_check1;
ALTER TABLE analysis_model_channels ADD CONSTRAINT analysis_model_channels_extension_shape CHECK(
 jsonb_typeof(extension_configs)='object' AND octet_length(extension_configs::text)<=4096
 AND (channel='candidate' OR extension_configs='{}' OR
  (stack_id ~ '^rai-v2-[0-9a-f]{16}$' AND extension_configs=jsonb_build_object('rai.model_manifest/1',encode(config_sha256,'hex')))));

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rai_load_dependency() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE load_scope text; n integer;
BEGIN
 IF NEW.scope_key ~ '^model/rai-v2-[0-9a-f]{16}/' THEN RETURN NEW; END IF;
 IF TG_OP='UPDATE' AND NEW.generation=OLD.generation THEN RETURN NEW; END IF;
 IF NEW.scope_key LIKE 'model/%/workout_load/%' THEN
  DELETE FROM analysis_publications WHERE dataset_id=NEW.dataset_id AND scope_key=NEW.scope_key;
  INSERT INTO analysis_model_dependencies(dataset_id,upstream_scope,downstream_scope)
  VALUES(NEW.dataset_id,NEW.scope_key,split_part(NEW.scope_key,'/workout_load/',1)||'/athlete/load') ON CONFLICT DO NOTHING;
  PERFORM rai_feature_downstream_dirty(NEW.dataset_id,NEW.scope_key,NEW.dirty_from,NEW.dirty_through);
 ELSIF NEW.scope_key LIKE 'model/%/workout/%' AND EXISTS(SELECT 1 FROM analysis_model_dependencies WHERE dataset_id=NEW.dataset_id AND downstream_scope LIKE 'model/%/workout_load/%') THEN
  load_scope:=replace(NEW.scope_key,'/workout/','/workout_load/');
  INSERT INTO analysis_model_dependencies(dataset_id,upstream_scope,downstream_scope) VALUES(NEW.dataset_id,NEW.scope_key,load_scope) ON CONFLICT DO NOTHING;
  GET DIAGNOSTICS n=ROW_COUNT;
  IF n>0 THEN PERFORM rai_analysis_dirty(NEW.dataset_id,load_scope,'analysis.training_load',NEW.dirty_from,NEW.dirty_through); END IF;
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
