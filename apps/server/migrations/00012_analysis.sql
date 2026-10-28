-- +goose Up
CREATE TABLE analysis_parameter_sets (
    dataset_id uuid NOT NULL REFERENCES datasets(id),
    version bigint NOT NULL CHECK(version > 0),
    resting_hr double precision NOT NULL CHECK(resting_hr > 0 AND resting_hr < 'Infinity'::float8),
    max_hr double precision NOT NULL CHECK(max_hr > resting_hr AND max_hr < 'Infinity'::float8),
    coefficient_a double precision NOT NULL CHECK(coefficient_a > 0 AND coefficient_a <= 10),
    coefficient_b double precision NOT NULL CHECK(coefficient_b > 0 AND coefficient_b <= 10),
    provenance text NOT NULL CHECK(length(provenance) BETWEEN 1 AND 200),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(dataset_id,version)
);
ALTER TABLE jobs ADD CONSTRAINT jobs_dataset_identity UNIQUE(dataset_id,id);
CREATE TABLE analysis_runs (
    dataset_id uuid NOT NULL REFERENCES datasets(id),
    id uuid NOT NULL,
    job_id uuid NOT NULL,
    fencing_token bigint NOT NULL,
    scope_key text NOT NULL,
    target_generation bigint NOT NULL CHECK(target_generation > 0),
    algorithm_name text NOT NULL,
    algorithm_version text NOT NULL,
    input_version text NOT NULL,
    input_hash bytea NOT NULL CHECK(octet_length(input_hash)=32),
    input_from timestamptz NOT NULL,
    input_through timestamptz NOT NULL CHECK(input_through >= input_from),
    lineage jsonb NOT NULL,
    status text NOT NULL CHECK(status IN ('RUNNING','SUCCEEDED','SUPERSEDED','FAILED')),
    error_code text,
    started_at timestamptz NOT NULL DEFAULT now(),
    finished_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(dataset_id,id),
    FOREIGN KEY(dataset_id,job_id) REFERENCES jobs(dataset_id,id),
    CHECK((status='RUNNING') = (finished_at IS NULL))
);
CREATE TABLE analysis_results (
    dataset_id uuid NOT NULL,
    run_id uuid NOT NULL,
    day date NOT NULL,
    metric text NOT NULL,
    algorithm_name text NOT NULL,
    algorithm_version text NOT NULL,
    value double precision CHECK(value IS NULL OR (value > '-Infinity'::float8 AND value < 'Infinity'::float8)),
    unit text NOT NULL,
    quality text NOT NULL CHECK(quality IN ('available','partial','insufficient')),
    details jsonb NOT NULL DEFAULT '{}',
    computed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(dataset_id,run_id,day,metric),
    FOREIGN KEY(dataset_id,run_id) REFERENCES analysis_runs(dataset_id,id),
    CHECK(value IS NOT NULL OR quality='insufficient')
);
CREATE TABLE analysis_publications (
    dataset_id uuid NOT NULL,
    scope_key text NOT NULL,
    metric_family text NOT NULL,
    day date NOT NULL,
    published_run_id uuid NOT NULL,
    generation bigint NOT NULL CHECK(generation>0),
    published_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(dataset_id,scope_key,metric_family,day),
    FOREIGN KEY(dataset_id,published_run_id) REFERENCES analysis_runs(dataset_id,id)
);
CREATE UNIQUE INDEX analysis_runs_identity ON analysis_runs(dataset_id,scope_key,algorithm_name,algorithm_version,target_generation,input_hash) WHERE status IN ('SUCCEEDED','SUPERSEDED');
CREATE INDEX analysis_runs_scope ON analysis_runs(dataset_id,scope_key,target_generation DESC);
CREATE INDEX analysis_results_load_range ON analysis_results(dataset_id,day,run_id) WHERE metric='workout_load';

-- Finished executions, numerical results and parameter history are append-only.
-- +goose StatementBegin
CREATE FUNCTION rai_analysis_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_TABLE_NAME='analysis_runs' AND TG_OP='UPDATE' AND OLD.status='RUNNING' THEN
        IF (to_jsonb(NEW)-'status'-'finished_at'-'error_code') =
           (to_jsonb(OLD)-'status'-'finished_at'-'error_code') THEN RETURN NEW; END IF;
    END IF;
    RAISE EXCEPTION 'analysis_immutable' USING ERRCODE='RA040';
END $$;
-- +goose StatementEnd
CREATE TRIGGER immutable_analysis_runs BEFORE UPDATE OR DELETE ON analysis_runs FOR EACH ROW EXECUTE FUNCTION rai_analysis_immutable();
CREATE TRIGGER immutable_analysis_results BEFORE UPDATE OR DELETE ON analysis_results FOR EACH ROW EXECUTE FUNCTION rai_analysis_immutable();
CREATE TRIGGER immutable_analysis_parameters BEFORE UPDATE OR DELETE ON analysis_parameter_sets FOR EACH ROW EXECUTE FUNCTION rai_analysis_immutable();

-- Same queue and generations; no trigger reads health values. Caller owns the
-- dataset lock, as in all existing fact paths. Old queued generations are kept
-- as historical job identities and cheaply superseded by the publisher.
-- +goose StatementBegin
CREATE FUNCTION rai_analysis_dirty(p_dataset uuid,p_scope text,p_kind text,p_from timestamptz,p_through timestamptz)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE g bigint;
BEGIN
    INSERT INTO dirty_scopes(dataset_id,scope_key,generation,dirty_from,dirty_through)
    VALUES(p_dataset,p_scope,1,p_from,p_through)
    ON CONFLICT(dataset_id,scope_key) DO UPDATE SET generation=dirty_scopes.generation+1,
        dirty_from=CASE WHEN dirty_scopes.generation=dirty_scopes.clean_generation THEN EXCLUDED.dirty_from ELSE LEAST(dirty_scopes.dirty_from,EXCLUDED.dirty_from) END,
        dirty_through=CASE WHEN dirty_scopes.generation=dirty_scopes.clean_generation THEN EXCLUDED.dirty_through ELSE GREATEST(dirty_scopes.dirty_through,EXCLUDED.dirty_through) END
    RETURNING generation INTO g;
    INSERT INTO jobs(id,dataset_id,kind,scope_key,target_generation)
    VALUES(gen_random_uuid(),p_dataset,p_kind,p_scope,g);
END $$;
-- +goose StatementEnd
-- Once state exists, any Workout dirtiness invalidates it in the fact commit,
-- before a slower feature Worker can publish. Bootstrap is at first publication.
-- +goose StatementBegin
CREATE FUNCTION rai_analysis_workout_dependency() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.scope_key LIKE 'workout:HKWorkoutTypeIdentifier:%' AND
       (TG_OP='INSERT' OR NEW.generation<>OLD.generation) AND
       EXISTS(SELECT 1 FROM dirty_scopes WHERE dataset_id=NEW.dataset_id AND scope_key='athlete/default') THEN
        PERFORM rai_analysis_dirty(NEW.dataset_id,'athlete/default','analysis.training_state',NEW.dirty_from,NEW.dirty_through);
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER analysis_workout_dependency AFTER INSERT OR UPDATE ON dirty_scopes FOR EACH ROW EXECUTE FUNCTION rai_analysis_workout_dependency();
-- +goose StatementBegin
CREATE FUNCTION rai_analysis_association_deleted() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE w workouts%ROWTYPE;
BEGIN
    SELECT * INTO w FROM workouts WHERE dataset_id=OLD.dataset_id AND workout_uuid=OLD.workout_uuid;
    IF FOUND THEN
        PERFORM rai_analysis_dirty(OLD.dataset_id,'workout:HKWorkoutTypeIdentifier:'||OLD.workout_uuid,'analysis.workout',w.event_start,w.event_end);
    END IF;
    RETURN OLD;
END $$;
-- +goose StatementEnd
CREATE TRIGGER analysis_association_deleted AFTER DELETE ON workout_samples FOR EACH ROW EXECUTE FUNCTION rai_analysis_association_deleted();
-- +goose StatementBegin
CREATE FUNCTION rai_analysis_series_sealed() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE w record;
BEGIN
    IF NEW.state='sealed' AND OLD.state<>'sealed' THEN
        FOR w IN SELECT x.* FROM workout_samples a JOIN workouts x USING(dataset_id,workout_uuid)
            WHERE a.dataset_id=NEW.dataset_id AND a.sample_type=NEW.sample_type AND a.sample_uuid=NEW.sample_uuid LOOP
            PERFORM rai_analysis_dirty(NEW.dataset_id,'workout:HKWorkoutTypeIdentifier:'||w.workout_uuid,'analysis.workout',w.event_start,w.event_end);
        END LOOP;
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER analysis_series_sealed AFTER UPDATE ON series_uploads FOR EACH ROW EXECUTE FUNCTION rai_analysis_series_sealed();
-- No Down: immutable run and parameter history must survive binary rollback.

REVOKE ALL ON FUNCTION rai_analysis_dirty(uuid,text,text,timestamptz,timestamptz) FROM PUBLIC;
