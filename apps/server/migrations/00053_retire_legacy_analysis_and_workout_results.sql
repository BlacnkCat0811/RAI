-- +goose Up
-- Retire the legacy analysis pipeline (workout bundle / training_state TRIMP and
-- the v1 candidate models). The v2 model stack computes every product metric
-- itself; its invalidation still keys on the same dirty scopes, so dirtiness is
-- kept and only the retired job kinds stop being queued.

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rai_analysis_dirty(p_dataset uuid,p_scope text,p_kind text,p_from timestamptz,p_through timestamptz)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE g bigint;
BEGIN
    INSERT INTO dirty_scopes(dataset_id,scope_key,generation,dirty_from,dirty_through)
    VALUES(p_dataset,p_scope,1,p_from,p_through)
    ON CONFLICT(dataset_id,scope_key) DO UPDATE SET generation=dirty_scopes.generation+1,
        dirty_from=CASE WHEN dirty_scopes.generation=dirty_scopes.clean_generation THEN EXCLUDED.dirty_from ELSE LEAST(dirty_scopes.dirty_from,EXCLUDED.dirty_from) END,
        dirty_through=CASE WHEN dirty_scopes.generation=dirty_scopes.clean_generation THEN EXCLUDED.dirty_through ELSE GREATEST(dirty_scopes.dirty_through,EXCLUDED.dirty_through) END
    RETURNING generation INTO g;
    IF p_kind NOT IN ('analysis.workout','analysis.training_state','analysis.sample','analysis.series',
        'analysis.workout_features','analysis.training_load','analysis.load_state','analysis.performance',
        'analysis.physiological_recovery','analysis.training_readiness','analysis.vo2max_external_projection') THEN
        INSERT INTO jobs(id,dataset_id,kind,scope_key,target_generation)
        VALUES(gen_random_uuid(),p_dataset,p_kind,p_scope,g);
    END IF;
END $$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION rai_analysis_dirty(uuid,text,text,timestamptz,timestamptz) FROM PUBLIC;

-- The legacy athlete state is no longer derived from Workout dirtiness.
DROP TRIGGER IF EXISTS analysis_workout_dependency ON dirty_scopes;
DROP FUNCTION IF EXISTS rai_analysis_workout_dependency();

-- Queued or leased retired jobs end with an explicit code; history is kept.
UPDATE jobs SET state='failed',lease_until=NULL,last_error_code='retired_pipeline'
 WHERE state IN ('queued','running') AND kind IN ('analysis.workout','analysis.training_state','analysis.sample','analysis.series',
  'analysis.workout_features','analysis.training_load','analysis.load_state','analysis.performance',
  'analysis.physiological_recovery','analysis.training_readiness','analysis.vo2max_external_projection');

-- One row per running Workout and model stack: the cached feature and HR
-- exposure for the Workout's fact generation (the generation of its
-- 'workout:HKWorkoutTypeIdentifier:<id>' dirty scope, bumped by every fact
-- change), and the v2 load of the latest day release for product readers.
CREATE TABLE model_workout_results (
    dataset_id uuid NOT NULL REFERENCES datasets(id),
    stack_id text NOT NULL CHECK (length(stack_id) BETWEEN 1 AND 100),
    workout_uuid uuid NOT NULL,
    fact_generation bigint NOT NULL CHECK (fact_generation >= 0),
    source_epoch text NOT NULL,
    feature jsonb NOT NULL CHECK (jsonb_typeof(feature)='object'),
    exposure jsonb NOT NULL CHECK (jsonb_typeof(exposure)='object'),
    load jsonb CHECK (load IS NULL OR jsonb_typeof(load)='object'),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (dataset_id, stack_id, workout_uuid),
    FOREIGN KEY (dataset_id, workout_uuid) REFERENCES workouts(dataset_id, workout_uuid) ON DELETE CASCADE
);

-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='rai_worker') THEN
  GRANT SELECT, INSERT, UPDATE ON model_workout_results TO rai_worker;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='rai_api') THEN
  GRANT SELECT ON model_workout_results TO rai_api;
  -- Settings and Workout detail read the user's own effective HR reference.
  GRANT SELECT ON athlete_model_inputs TO rai_api;
 END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- No Down: retired job history and cached results must survive binary rollback.
