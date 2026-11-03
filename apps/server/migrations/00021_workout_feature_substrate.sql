-- +goose Up
-- Additive M1 substrate: legacy publications and raw facts are unchanged.
CREATE TABLE analysis_artifacts (
 dataset_id uuid NOT NULL, run_id uuid NOT NULL,
 artifact_key text NOT NULL CHECK(artifact_key='workout_features'),
 schema_version text NOT NULL CHECK(schema_version='rai.workout_features/1'),
 content_sha256 bytea NOT NULL CHECK(octet_length(content_sha256)=32),
 payload jsonb NOT NULL CHECK(jsonb_typeof(payload)='object' AND octet_length(payload::text)<=131072),
 PRIMARY KEY(dataset_id,run_id,artifact_key),
 FOREIGN KEY(dataset_id,run_id) REFERENCES analysis_runs(dataset_id,id)
);
CREATE TRIGGER immutable_analysis_artifacts BEFORE UPDATE OR DELETE ON analysis_artifacts FOR EACH ROW EXECUTE FUNCTION rai_analysis_immutable();
-- +goose StatementBegin
CREATE FUNCTION rai_feature_artifact_insert() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE r analysis_runs%ROWTYPE;
BEGIN
 SELECT * INTO r FROM analysis_runs WHERE dataset_id=NEW.dataset_id AND id=NEW.run_id FOR SHARE;
 IF NOT FOUND OR r.status<>'RUNNING' OR r.algorithm_name<>'rai.workout_features' OR r.algorithm_version<>'v1'
 OR (NEW.payload->>'schemaVersion') IS DISTINCT FROM NEW.schema_version OR (NEW.payload->>'datasetID') IS DISTINCT FROM NEW.dataset_id::text
 OR (NEW.payload->>'generation')::bigint IS DISTINCT FROM r.target_generation THEN
  RAISE EXCEPTION 'feature_artifact_invalid_run' USING ERRCODE='RA041';
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER feature_artifact_insert BEFORE INSERT ON analysis_artifacts FOR EACH ROW EXECUTE FUNCTION rai_feature_artifact_insert();

CREATE TABLE analysis_model_channels (
 dataset_id uuid NOT NULL REFERENCES datasets(id),
 channel text NOT NULL CHECK(channel IN ('active','candidate')),
 stack_id text NOT NULL,
 config_sha256 bytea NOT NULL CHECK(octet_length(config_sha256)=32),
 assignment_version bigint NOT NULL CHECK(assignment_version>0),
 assigned_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(dataset_id,channel),
 -- M1 deliberately cannot activate a feature-only stack. A later reviewed
 -- migration/consumer contract must open the promotion gate.
 CHECK(channel<>'active' OR stack_id='legacy')
);
CREATE TABLE athlete_model_inputs (
 dataset_id uuid NOT NULL REFERENCES datasets(id), id uuid NOT NULL,
 kind text NOT NULL CHECK(kind='hr_reference'), subject_key text NOT NULL CHECK(subject_key='athlete'),
 effective_from timestamptz NOT NULL, effective_through timestamptz,
 supersedes_id uuid, schema_version text NOT NULL CHECK(schema_version='rai.hr_reference/1'),
 content_sha256 bytea NOT NULL CHECK(octet_length(content_sha256)=32),
 payload jsonb NOT NULL CHECK(jsonb_typeof(payload)='object' AND octet_length(payload::text)<=4096),
 provenance text NOT NULL CHECK(length(provenance) BETWEEN 1 AND 200),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(dataset_id,id), FOREIGN KEY(dataset_id,supersedes_id) REFERENCES athlete_model_inputs(dataset_id,id),
 CHECK(effective_through IS NULL OR effective_through>effective_from)
);
CREATE INDEX athlete_model_inputs_effective ON athlete_model_inputs(dataset_id,kind,subject_key,effective_from,id);
CREATE TRIGGER immutable_athlete_model_inputs BEFORE UPDATE OR DELETE ON athlete_model_inputs FOR EACH ROW EXECUTE FUNCTION rai_analysis_immutable();
-- Append-only HR-reference events: a superseding/revoking input is explicit.
-- +goose StatementBegin
CREATE FUNCTION rai_model_input_insert() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.supersedes_id=NEW.id OR (NEW.supersedes_id IS NOT NULL AND NOT EXISTS(
 SELECT 1 FROM athlete_model_inputs WHERE dataset_id=NEW.dataset_id AND id=NEW.supersedes_id AND kind=NEW.kind AND subject_key=NEW.subject_key)) THEN
  RAISE EXCEPTION 'model_input_supersedes_invalid' USING ERRCODE='RA042';
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER model_input_insert BEFORE INSERT ON athlete_model_inputs FOR EACH ROW EXECUTE FUNCTION rai_model_input_insert();

-- Explicit future dependency edges. No unimplemented model jobs/handlers are
-- created: M2/M3/M4 may bind their own registered handler later.
CREATE TABLE analysis_model_dependencies (
 dataset_id uuid NOT NULL REFERENCES datasets(id), upstream_scope text NOT NULL, downstream_scope text NOT NULL,
 PRIMARY KEY(dataset_id,upstream_scope,downstream_scope),
 CHECK(upstream_scope LIKE 'model/%/workout/%'),
 CHECK(downstream_scope ~ '^model/[^/]+/athlete/(load|performance|vo2|ability|endurance)$')
);
-- +goose StatementBegin
CREATE FUNCTION rai_feature_downstream_dirty(p_dataset uuid,p_scope text,p_from timestamptz,p_through timestamptz)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE d record;
BEGIN
 FOR d IN SELECT downstream_scope FROM analysis_model_dependencies WHERE dataset_id=p_dataset AND upstream_scope=p_scope ORDER BY downstream_scope LOOP
  INSERT INTO dirty_scopes(dataset_id,scope_key,generation,dirty_from,dirty_through)
  VALUES(p_dataset,d.downstream_scope,1,p_from,p_through)
  ON CONFLICT(dataset_id,scope_key) DO UPDATE SET generation=dirty_scopes.generation+1,
   dirty_from=LEAST(dirty_scopes.dirty_from,EXCLUDED.dirty_from),dirty_through=GREATEST(dirty_scopes.dirty_through,EXCLUDED.dirty_through);
  DELETE FROM analysis_publications WHERE dataset_id=p_dataset AND scope_key=d.downstream_scope;
 END LOOP;
END $$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION rai_feature_dependency() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE c record; id text; scope text;
BEGIN
 IF TG_OP='UPDATE' AND NEW.generation=OLD.generation THEN RETURN NEW; END IF;
 IF NEW.scope_key LIKE 'workout:HKWorkoutTypeIdentifier:%' THEN
  id:=substring(NEW.scope_key from length('workout:HKWorkoutTypeIdentifier:')+1);
  FOR c IN SELECT DISTINCT stack_id FROM analysis_model_channels WHERE dataset_id=NEW.dataset_id AND channel='candidate' LOOP
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
CREATE TRIGGER feature_dependency AFTER INSERT OR UPDATE ON dirty_scopes FOR EACH ROW EXECUTE FUNCTION rai_feature_dependency();
REVOKE ALL ON analysis_artifacts,analysis_model_channels,athlete_model_inputs,analysis_model_dependencies FROM PUBLIC;
REVOKE ALL ON FUNCTION rai_feature_downstream_dirty(uuid,text,timestamptz,timestamptz) FROM PUBLIC;
REVOKE ALL ON FUNCTION rai_feature_dependency(),rai_feature_artifact_insert(),rai_model_input_insert() FROM PUBLIC;
-- No Down: preserve immutable artifact/input/run history on binary rollback.
-- Administrative rollback is explicitly rejected by the existing checksum gate.

-- Extend the existing administrative audit without weakening old operations.
ALTER TABLE admin_operations DROP CONSTRAINT admin_operations_operation_type_check;
ALTER TABLE admin_operations DROP CONSTRAINT admin_operations_source_check;
ALTER TABLE admin_operations ALTER COLUMN target_dataset DROP NOT NULL;
ALTER TABLE admin_operations ADD COLUMN subject_dataset uuid REFERENCES datasets(id);
ALTER TABLE admin_operations ADD COLUMN details jsonb NOT NULL DEFAULT '{}';
ALTER TABLE admin_operations ADD CHECK(operation_type IN ('new_dataset','disaster_recovery','feature_candidate'));
ALTER TABLE admin_operations ADD CHECK((operation_type='feature_candidate' AND subject_dataset IS NOT NULL AND source_dataset IS NULL AND target_dataset IS NULL) OR (operation_type IN ('new_dataset','disaster_recovery') AND target_dataset IS NOT NULL AND (operation_type='disaster_recovery' OR source_dataset IS NOT NULL)));

-- Staging registrations change candidate availability without changing the
-- legacy protocol's revision/receipt/job identities. No pending points are used.
-- +goose StatementBegin
CREATE FUNCTION rai_feature_staged() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE w record; c record;
BEGIN
 IF TG_TABLE_NAME='series_uploads' THEN
  FOR w IN SELECT x.workout_uuid,x.event_start,x.event_end FROM workout_samples a JOIN workouts x USING(dataset_id,workout_uuid)
   WHERE a.dataset_id=NEW.dataset_id AND a.sample_type=NEW.sample_type AND a.sample_uuid=NEW.sample_uuid LOOP
   FOR c IN SELECT stack_id FROM analysis_model_channels WHERE dataset_id=NEW.dataset_id AND channel='candidate' LOOP
    PERFORM rai_analysis_dirty(NEW.dataset_id,'model/'||c.stack_id||'/workout/'||w.workout_uuid,'analysis.workout_features',w.event_start,w.event_end);
   END LOOP;
  END LOOP;
 ELSE
  SELECT workout_uuid,event_start,event_end INTO w FROM workouts WHERE dataset_id=NEW.dataset_id AND workout_uuid=NEW.workout_uuid;
  IF FOUND THEN
   FOR c IN SELECT stack_id FROM analysis_model_channels WHERE dataset_id=NEW.dataset_id AND channel='candidate' LOOP
    PERFORM rai_analysis_dirty(NEW.dataset_id,'model/'||c.stack_id||'/workout/'||w.workout_uuid,'analysis.workout_features',w.event_start,w.event_end);
   END LOOP;
  END IF;
 END IF;
 RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER feature_series_staged AFTER INSERT ON series_uploads FOR EACH ROW EXECUTE FUNCTION rai_feature_staged();
CREATE TRIGGER feature_route_staged AFTER INSERT ON workout_routes FOR EACH ROW EXECUTE FUNCTION rai_feature_staged();
REVOKE ALL ON FUNCTION rai_feature_staged() FROM PUBLIC;
