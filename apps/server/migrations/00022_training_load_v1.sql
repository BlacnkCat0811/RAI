-- +goose Up
-- M2 companion artifacts use the same append-only run/artifact transaction.
ALTER TABLE analysis_artifacts DROP CONSTRAINT analysis_artifacts_artifact_key_check;
ALTER TABLE analysis_artifacts DROP CONSTRAINT analysis_artifacts_schema_version_check;
ALTER TABLE analysis_artifacts DROP CONSTRAINT analysis_artifacts_payload_check;
ALTER TABLE analysis_artifacts ADD CHECK(
 (artifact_key='workout_features' AND schema_version='rai.workout_features/1' AND octet_length(payload::text)<=131072) OR
 (artifact_key='hr_exposure' AND schema_version='rai.hr_exposure/1' AND octet_length(payload::text)<=2097152) OR
 (artifact_key='training_load' AND schema_version='rai.training_load/1' AND octet_length(payload::text)<=131072) OR
 (artifact_key='load_state' AND schema_version='rai.load_state/1' AND octet_length(payload::text)<=2097152));
ALTER TABLE analysis_artifacts ADD CHECK(jsonb_typeof(payload)='object');
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rai_feature_artifact_insert() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE r analysis_runs%ROWTYPE; alg text;
BEGIN
 SELECT * INTO r FROM analysis_runs WHERE dataset_id=NEW.dataset_id AND id=NEW.run_id FOR SHARE;
 alg:=CASE NEW.artifact_key WHEN 'workout_features' THEN 'rai.workout_features' WHEN 'hr_exposure' THEN 'rai.workout_features' WHEN 'training_load' THEN 'rai.training_load.hrr_integral' WHEN 'load_state' THEN 'rai.load_state.exp_finite' END;
 IF NOT FOUND OR r.status<>'RUNNING' OR r.algorithm_name IS DISTINCT FROM alg OR r.algorithm_version<>'v1'
 OR (NEW.payload->>'schemaVersion') IS DISTINCT FROM NEW.schema_version
 OR (NEW.payload->>'datasetID') IS DISTINCT FROM NEW.dataset_id::text
 OR COALESCE((NEW.payload->>'generation')::bigint,(NEW.payload->>'inputGeneration')::bigint) IS DISTINCT FROM r.target_generation THEN
 RAISE EXCEPTION 'feature_artifact_invalid_run' USING ERRCODE='RA041'; END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
-- A ledger row is immutable history; its current identity is the existing
-- candidate publication pointer, never a second mutable current flag.
CREATE TABLE analysis_load_ledger (
 dataset_id uuid NOT NULL, run_id uuid NOT NULL, local_date date NOT NULL,
 workout_uuid uuid NOT NULL, model_version text NOT NULL CHECK(model_version='rai.training_load.hrr_integral/v1'),
 parameter_id uuid, feature_run_id uuid NOT NULL,
 contribution double precision CHECK(contribution IS NULL OR (contribution>=0 AND contribution<'Infinity'::float8)),
 observed_contribution double precision CHECK(observed_contribution IS NULL OR (observed_contribution>=0 AND observed_contribution<'Infinity'::float8)),
 active_seconds double precision CHECK(active_seconds IS NULL OR (active_seconds>=0 AND active_seconds<=86400)),
 quality text NOT NULL CHECK(quality IN ('complete','partial','insufficient')),
 PRIMARY KEY(dataset_id,run_id,local_date),
 FOREIGN KEY(dataset_id,run_id) REFERENCES analysis_runs(dataset_id,id),
 FOREIGN KEY(dataset_id,feature_run_id) REFERENCES analysis_runs(dataset_id,id),
 FOREIGN KEY(dataset_id,parameter_id) REFERENCES athlete_model_inputs(dataset_id,id),
 CHECK((contribution IS NULL)=(quality='insufficient'))
);
CREATE INDEX analysis_load_ledger_day ON analysis_load_ledger(dataset_id,local_date,workout_uuid,run_id);
CREATE TRIGGER immutable_load_ledger BEFORE UPDATE OR DELETE ON analysis_load_ledger FOR EACH ROW EXECUTE FUNCTION rai_analysis_immutable();
-- +goose StatementBegin
CREATE FUNCTION rai_load_ledger_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NOT EXISTS(SELECT 1 FROM analysis_runs r JOIN analysis_artifacts a USING(dataset_id)
 WHERE r.dataset_id=NEW.dataset_id AND r.id=NEW.run_id AND r.status='RUNNING' AND r.algorithm_name='rai.training_load.hrr_integral'
 AND a.run_id=r.id AND a.artifact_key='training_load' AND a.payload->>'workoutID'=NEW.workout_uuid::text
 AND a.payload->>'featureArtifactID'=NEW.feature_run_id::text) THEN
 RAISE EXCEPTION 'load_ledger_invalid_run' USING ERRCODE='RA043'; END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER load_ledger_insert BEFORE INSERT ON analysis_load_ledger FOR EACH ROW EXECUTE FUNCTION rai_load_ledger_insert();
ALTER TABLE analysis_model_dependencies DROP CONSTRAINT analysis_model_dependencies_upstream_scope_check;
ALTER TABLE analysis_model_dependencies DROP CONSTRAINT analysis_model_dependencies_downstream_scope_check;
ALTER TABLE analysis_model_dependencies ADD CHECK(upstream_scope ~ '^model/[^/]+/(workout|workout_load)/[0-9a-f-]{36}$');
ALTER TABLE analysis_model_dependencies ADD CHECK(downstream_scope ~ '^model/[^/]+/(athlete/(load|performance|vo2|ability|endurance)|workout_load/[0-9a-f-]{36})$');
ALTER TABLE analysis_model_dependencies ADD CHECK(upstream_scope<>downstream_scope AND
 (upstream_scope LIKE '%/workout/%' OR downstream_scope LIKE '%/athlete/%'));
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rai_feature_downstream_dirty(p_dataset uuid,p_scope text,p_from timestamptz,p_through timestamptz)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE d record; enabled boolean;
BEGIN
 SELECT EXISTS(SELECT 1 FROM analysis_model_dependencies WHERE dataset_id=p_dataset AND downstream_scope LIKE 'model/%/workout_load/%') INTO enabled;
 FOR d IN SELECT downstream_scope FROM analysis_model_dependencies WHERE dataset_id=p_dataset AND upstream_scope=p_scope ORDER BY downstream_scope LOOP
  IF d.downstream_scope LIKE 'model/%/workout_load/%' THEN
   PERFORM rai_analysis_dirty(p_dataset,d.downstream_scope,'analysis.training_load',p_from,p_through);
  ELSIF enabled AND d.downstream_scope LIKE 'model/%/athlete/load' THEN
   PERFORM rai_analysis_dirty(p_dataset,d.downstream_scope,'analysis.load_state',p_from,p_through);
  ELSE
   INSERT INTO dirty_scopes(dataset_id,scope_key,generation,dirty_from,dirty_through)
   VALUES(p_dataset,d.downstream_scope,1,p_from,p_through)
   ON CONFLICT(dataset_id,scope_key) DO UPDATE SET generation=dirty_scopes.generation+1,
    dirty_from=CASE WHEN dirty_scopes.generation=dirty_scopes.clean_generation THEN EXCLUDED.dirty_from ELSE LEAST(dirty_scopes.dirty_from,EXCLUDED.dirty_from) END,
    dirty_through=CASE WHEN dirty_scopes.generation=dirty_scopes.clean_generation THEN EXCLUDED.dirty_through ELSE GREATEST(dirty_scopes.dirty_through,EXCLUDED.dirty_through) END;
  END IF;
  IF enabled AND d.downstream_scope LIKE 'model/%/athlete/load' THEN
   DELETE FROM analysis_publications p USING datasets x WHERE x.id=p_dataset AND p.dataset_id=p_dataset AND p.scope_key=d.downstream_scope
   AND p.day>=(p_from AT TIME ZONE x.timezone)::date AND p.day<=((p_through-interval '1 microsecond') AT TIME ZONE x.timezone)::date+186;
  ELSE
   DELETE FROM analysis_publications WHERE dataset_id=p_dataset AND scope_key=d.downstream_scope;
  END IF;
 END LOOP;
END $$;
-- +goose StatementEnd
-- The existing candidate channel enables features. Explicit M2 registration
-- adds graph edges. Later facts inherit M2 only within that enabled dataset.
-- +goose StatementBegin
CREATE FUNCTION rai_load_dependency() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE load_scope text; n integer;
BEGIN
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
CREATE TRIGGER load_dependency AFTER INSERT OR UPDATE ON dirty_scopes FOR EACH ROW EXECUTE FUNCTION rai_load_dependency();
REVOKE ALL ON analysis_load_ledger FROM PUBLIC;
REVOKE ALL ON FUNCTION rai_load_dependency(),rai_load_ledger_insert() FROM PUBLIC;
-- No destructive Down: immutable model/input/ledger history is retained.
