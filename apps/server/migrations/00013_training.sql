-- +goose Up
CREATE TABLE training_parameter_sets (
 version text PRIMARY KEY,
 parameters jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO training_parameter_sets VALUES ('coach_parameters/v1','{"maxSessionM":30000,"version":"coach_parameters/v1","progression":0.05,"deload":0.8,"longShare":0.35,"longCeilingM":30000,"lowReadiness":50,"readinessFactor":0.8,"feedbackFactor":0.9,"hardSpacingDays":2,"maxHardPerWeek":1}');
CREATE TABLE training_plans (
 dataset_id uuid NOT NULL REFERENCES datasets(id),
 id uuid NOT NULL,
 timezone text NOT NULL,
 active boolean NOT NULL DEFAULT true,
 config_revision bigint NOT NULL DEFAULT 1 CHECK(config_revision>0),
 override_revision bigint NOT NULL DEFAULT 0 CHECK(override_revision>=0),
 base_number bigint NOT NULL DEFAULT 0 CHECK(base_number>=0),
 effective_number bigint NOT NULL DEFAULT 0 CHECK(effective_number>=0),
 current_base_id uuid,
 current_effective_id uuid,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(dataset_id,id)
);
CREATE UNIQUE INDEX training_current_plan ON training_plans(dataset_id) WHERE active;
CREATE TABLE training_configs (
 dataset_id uuid NOT NULL, plan_id uuid NOT NULL, revision bigint NOT NULL CHECK(revision>0),
 config jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(dataset_id,plan_id,revision), FOREIGN KEY(dataset_id,plan_id) REFERENCES training_plans(dataset_id,id)
);
CREATE TABLE training_sessions (
 dataset_id uuid NOT NULL, plan_id uuid NOT NULL, id uuid NOT NULL,
 origin text NOT NULL CHECK(origin IN ('generated','user_added')), slot integer,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(dataset_id,plan_id,id), UNIQUE(dataset_id,plan_id,slot),
 FOREIGN KEY(dataset_id,plan_id) REFERENCES training_plans(dataset_id,id),
 CHECK((origin='generated' AND slot>=0 AND slot<112) OR (origin='user_added' AND slot IS NULL))
);
CREATE TABLE training_generation_runs (
 dataset_id uuid NOT NULL, id uuid NOT NULL, plan_id uuid NOT NULL, job_id uuid NOT NULL,
 fencing_token bigint NOT NULL, generation bigint NOT NULL CHECK(generation>0), config_revision bigint NOT NULL,
 generator text NOT NULL, parameter_version text NOT NULL REFERENCES training_parameter_sets(version),
 input_hash bytea NOT NULL CHECK(octet_length(input_hash)=32), context jsonb NOT NULL,
 status text NOT NULL CHECK(status IN ('RUNNING','SUCCEEDED','SUPERSEDED','FAILED')), error_code text,
 started_at timestamptz NOT NULL DEFAULT now(), completed_at timestamptz,
 PRIMARY KEY(dataset_id,id), UNIQUE(dataset_id,plan_id,id),
 FOREIGN KEY(dataset_id,plan_id,config_revision) REFERENCES training_configs(dataset_id,plan_id,revision),
 FOREIGN KEY(dataset_id,job_id) REFERENCES jobs(dataset_id,id),
 CHECK((status='RUNNING')=(completed_at IS NULL))
);
CREATE TABLE training_base_versions (
 dataset_id uuid NOT NULL, plan_id uuid NOT NULL, id uuid NOT NULL, number bigint NOT NULL CHECK(number>0),
 run_id uuid NOT NULL, base jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(dataset_id,plan_id,id), UNIQUE(dataset_id,plan_id,number), UNIQUE(dataset_id,run_id),
 FOREIGN KEY(dataset_id,plan_id,run_id) REFERENCES training_generation_runs(dataset_id,plan_id,id)
);
CREATE TABLE training_overrides (
 dataset_id uuid NOT NULL, plan_id uuid NOT NULL, id uuid NOT NULL, session_id uuid NOT NULL,
 command_id uuid NOT NULL, request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32),
 revision bigint NOT NULL CHECK(revision>0), operation text NOT NULL CHECK(operation IN ('add','edit','move','skip','delete','revoke')),
 event jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(dataset_id,plan_id,id), UNIQUE(dataset_id,id), UNIQUE(dataset_id,plan_id,revision),
 FOREIGN KEY(dataset_id,plan_id,session_id) REFERENCES training_sessions(dataset_id,plan_id,id)
);
CREATE TABLE training_effective_versions (
 dataset_id uuid NOT NULL, plan_id uuid NOT NULL, id uuid NOT NULL, number bigint NOT NULL CHECK(number>0),
 base_id uuid NOT NULL, override_revision bigint NOT NULL CHECK(override_revision>=0), effective jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(dataset_id,plan_id,id), UNIQUE(dataset_id,plan_id,number),
 UNIQUE(dataset_id,plan_id,base_id,override_revision),
 UNIQUE(dataset_id,plan_id,id,base_id),
 FOREIGN KEY(dataset_id,plan_id,base_id) REFERENCES training_base_versions(dataset_id,plan_id,id)
);
ALTER TABLE training_plans ADD FOREIGN KEY(dataset_id,id,current_base_id) REFERENCES training_base_versions(dataset_id,plan_id,id);
ALTER TABLE training_plans ADD FOREIGN KEY(dataset_id,id,current_effective_id) REFERENCES training_effective_versions(dataset_id,plan_id,id);
ALTER TABLE training_plans ADD CONSTRAINT training_current_pair FOREIGN KEY(dataset_id,id,current_effective_id,current_base_id)
 REFERENCES training_effective_versions(dataset_id,plan_id,id,base_id) DEFERRABLE INITIALLY DEFERRED;
CREATE TABLE training_executions (
 dataset_id uuid NOT NULL, id uuid NOT NULL, plan_id uuid NOT NULL, effective_id uuid NOT NULL, session_id uuid NOT NULL,
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0), status text NOT NULL CHECK(status IN ('scheduled','started','completed','skipped')),
 snapshot jsonb NOT NULL, started_at timestamptz, completed_at timestamptz,
 workout_id uuid, notes text NOT NULL DEFAULT '' CHECK(length(notes)<=2000),
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(dataset_id,id), UNIQUE(dataset_id,plan_id,session_id),
 FOREIGN KEY(dataset_id,plan_id,effective_id) REFERENCES training_effective_versions(dataset_id,plan_id,id),
 FOREIGN KEY(dataset_id,plan_id,session_id) REFERENCES training_sessions(dataset_id,plan_id,id),
 CHECK(status NOT IN ('started','completed') OR started_at IS NOT NULL),
 CHECK((status IN ('completed','skipped'))=(completed_at IS NOT NULL))
);
CREATE TABLE training_feedback (
 dataset_id uuid NOT NULL, id uuid NOT NULL, execution_id uuid NOT NULL, execution_revision bigint NOT NULL,
 source_run_id uuid, feedback jsonb NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(dataset_id,id),
 FOREIGN KEY(dataset_id,execution_id) REFERENCES training_executions(dataset_id,id),
 FOREIGN KEY(dataset_id,source_run_id) REFERENCES analysis_runs(dataset_id,id),
 UNIQUE NULLS NOT DISTINCT(dataset_id,execution_id,execution_revision,source_run_id)
);
CREATE TABLE training_commands (
 dataset_id uuid NOT NULL, id uuid NOT NULL, plan_id uuid NOT NULL,
 request_hash bytea NOT NULL CHECK(octet_length(request_hash)=32), receipt jsonb NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(dataset_id,id), FOREIGN KEY(dataset_id,plan_id) REFERENCES training_plans(dataset_id,id)
);
ALTER TABLE training_overrides ADD FOREIGN KEY(dataset_id,command_id) REFERENCES training_commands(dataset_id,id) DEFERRABLE INITIALLY DEFERRED;
CREATE INDEX training_runs_plan ON training_generation_runs(dataset_id,plan_id,generation DESC);
CREATE INDEX training_executions_history ON training_executions(dataset_id,plan_id,created_at,id);
CREATE INDEX training_feedback_history ON training_feedback(dataset_id,execution_id,created_at,id);
CREATE TRIGGER immutable_training_parameters BEFORE UPDATE OR DELETE ON training_parameter_sets FOR EACH ROW EXECUTE FUNCTION rai_analysis_immutable();
CREATE TRIGGER immutable_training_configs BEFORE UPDATE OR DELETE ON training_configs FOR EACH ROW EXECUTE FUNCTION rai_analysis_immutable();
CREATE TRIGGER immutable_training_sessions BEFORE UPDATE OR DELETE ON training_sessions FOR EACH ROW EXECUTE FUNCTION rai_analysis_immutable();
CREATE TRIGGER immutable_training_base BEFORE UPDATE OR DELETE ON training_base_versions FOR EACH ROW EXECUTE FUNCTION rai_analysis_immutable();
CREATE TRIGGER immutable_training_effective BEFORE UPDATE OR DELETE ON training_effective_versions FOR EACH ROW EXECUTE FUNCTION rai_analysis_immutable();
CREATE TRIGGER immutable_training_overrides BEFORE UPDATE OR DELETE ON training_overrides FOR EACH ROW EXECUTE FUNCTION rai_analysis_immutable();
CREATE TRIGGER immutable_training_feedback BEFORE UPDATE OR DELETE ON training_feedback FOR EACH ROW EXECUTE FUNCTION rai_analysis_immutable();
CREATE TRIGGER immutable_training_commands BEFORE UPDATE OR DELETE ON training_commands FOR EACH ROW EXECUTE FUNCTION rai_analysis_immutable();
-- +goose StatementBegin
CREATE FUNCTION rai_training_execution_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='INSERT' THEN
  IF NEW.status IN ('scheduled','started') AND EXISTS(
   SELECT 1 FROM training_effective_versions e, jsonb_array_elements(e.effective->'sessions') s
   WHERE e.dataset_id=NEW.dataset_id AND e.plan_id=NEW.plan_id AND e.id=NEW.effective_id
   AND s->>'sessionID'=NEW.session_id::text AND s=NEW.snapshot AND s->>'type'<>'REST' AND s->>'status'='scheduled'
  ) THEN RETURN NEW; END IF;
 END IF;
 IF TG_OP='UPDATE' AND NEW.revision=OLD.revision+1 AND
 (to_jsonb(NEW)-'status'-'revision'-'started_at'-'completed_at'-'workout_id'-'notes')=
 (to_jsonb(OLD)-'status'-'revision'-'started_at'-'completed_at'-'workout_id'-'notes') AND
 (OLD.started_at IS NULL OR NEW.started_at=OLD.started_at) AND
 (OLD.completed_at IS NULL OR NEW.completed_at=OLD.completed_at) AND
 ((OLD.status='scheduled' AND NEW.status IN ('started','skipped')) OR
  (OLD.status='started' AND NEW.status IN ('completed','skipped')) OR
  (OLD.status='completed' AND NEW.status='completed' AND OLD.workout_id IS NULL AND NEW.workout_id IS NOT NULL))
 THEN RETURN NEW; END IF;
 RAISE EXCEPTION 'training_execution_immutable' USING ERRCODE='RA050';
END $$;
-- +goose StatementEnd
CREATE TRIGGER training_execution_guard BEFORE INSERT OR UPDATE OR DELETE ON training_executions FOR EACH ROW EXECUTE FUNCTION rai_training_execution_guard();
-- +goose StatementBegin
CREATE FUNCTION rai_training_run_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' AND OLD.status='RUNNING' AND NEW.status<>'RUNNING' AND
 (to_jsonb(NEW)-'status'-'completed_at'-'error_code')=(to_jsonb(OLD)-'status'-'completed_at'-'error_code') THEN RETURN NEW; END IF;
 RAISE EXCEPTION 'training_run_immutable' USING ERRCODE='RA051';
END $$;
-- +goose StatementEnd
CREATE TRIGGER training_run_guard BEFORE UPDATE OR DELETE ON training_generation_runs FOR EACH ROW EXECUTE FUNCTION rai_training_run_guard();
-- Caller holds dataset lock. One queued successor absorbs publication bursts.
-- +goose StatementBegin
CREATE FUNCTION rai_training_dirty(p_dataset uuid,p_plan uuid) RETURNS void LANGUAGE plpgsql AS $$
DECLARE g bigint; scope text := 'plan:'||p_plan::text;
BEGIN
 IF NOT EXISTS(SELECT 1 FROM training_plans WHERE dataset_id=p_dataset AND id=p_plan AND active) THEN RETURN; END IF;
 INSERT INTO dirty_scopes(dataset_id,scope_key,generation,dirty_from,dirty_through)
 VALUES(p_dataset,scope,1,now(),now())
 ON CONFLICT(dataset_id,scope_key) DO UPDATE SET generation=dirty_scopes.generation+1,dirty_through=now()
 RETURNING generation INTO g;
 UPDATE jobs SET target_generation=g,run_after=now()+interval '1 second'
 WHERE id=(SELECT id FROM jobs WHERE dataset_id=p_dataset AND scope_key=scope AND kind='training.plan' AND state='queued' ORDER BY target_generation DESC LIMIT 1 FOR UPDATE);
 IF NOT FOUND THEN
  INSERT INTO jobs(id,dataset_id,kind,scope_key,target_generation,run_after)
  VALUES(gen_random_uuid(),p_dataset,'training.plan',scope,g,now()+interval '1 second');
 END IF;
END $$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION rai_training_dirty(uuid,uuid) FROM PUBLIC;
-- No Down: training audit history must survive binary rollback.
