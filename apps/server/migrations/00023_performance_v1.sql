-- +goose Up
-- M3 keeps every model in the candidate channel. The generic extension manifest
-- binds additional model configurations without altering the frozen M1 policy.
ALTER TABLE analysis_model_channels ADD COLUMN extension_configs jsonb NOT NULL DEFAULT '{}'
 CHECK(jsonb_typeof(extension_configs)='object' AND octet_length(extension_configs::text)<=4096 AND (channel='candidate' OR extension_configs='{}'));
ALTER TABLE analysis_artifacts DROP CONSTRAINT analysis_artifacts_check;
ALTER TABLE analysis_artifacts ADD CHECK(
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
 alg:=CASE NEW.artifact_key WHEN 'workout_features' THEN 'rai.workout_features' WHEN 'hr_exposure' THEN 'rai.workout_features'
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
-- M3's evidence/curve/endurance/ability are one atomic scope and publication.
-- Unbound M1 future scopes remain placeholders, never successful dummy jobs.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rai_feature_downstream_dirty(p_dataset uuid,p_scope text,p_from timestamptz,p_through timestamptz)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE d record; load_enabled boolean; performance_enabled boolean;
BEGIN
 SELECT EXISTS(SELECT 1 FROM analysis_model_dependencies WHERE dataset_id=p_dataset AND downstream_scope LIKE 'model/%/workout_load/%') INTO load_enabled;
 SELECT EXISTS(SELECT 1 FROM analysis_model_channels WHERE dataset_id=p_dataset AND channel='candidate' AND extension_configs ? 'rai.performance_curve/v1') INTO performance_enabled;
 FOR d IN SELECT downstream_scope FROM analysis_model_dependencies WHERE dataset_id=p_dataset AND upstream_scope=p_scope ORDER BY downstream_scope LOOP
  IF d.downstream_scope LIKE 'model/%/workout_load/%' THEN
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
-- Supports the bounded feature lookup; no raw sample query or evidence ledger.
CREATE INDEX workouts_performance_end ON workouts(dataset_id,event_end,workout_uuid);
CREATE INDEX analysis_performance_history ON analysis_runs(dataset_id,scope_key,started_at DESC,id DESC)
 WHERE algorithm_name='rai.performance_curve' AND status='SUCCEEDED';
-- No Down: retain immutable runs and artifacts. Binary rollback keeps legacy active.
