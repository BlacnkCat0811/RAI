-- +goose Up
-- Retire the Workout completeness control plane.
--
-- Product decision (2026-11-10): the server judges a Workout only by whether its
-- upload committed (workout_ingests.state='committed'), exactly as RQrun does.
-- Nothing proves that a *set* of Workouts is complete, because the server cannot
-- distinguish "the client uploaded three of five" from "there were three", and
-- the v6 client no longer produces completeness receipts at all. Keeping the
-- table alive only let a stale row gate Readiness forever.
--
-- This is a single-phase removal: the development database is disposable and no
-- release needs to read the retired rows.
DROP TRIGGER IF EXISTS workout_completeness_immutable ON workout_sync_completeness;
DROP TRIGGER IF EXISTS workout_completeness_dirty ON workout_sync_completeness;
DROP TRIGGER IF EXISTS model_stack_dirty_proof ON workout_sync_completeness;
DROP TABLE workout_sync_completeness;
DROP FUNCTION IF EXISTS rai_workout_completeness_dirty();
DROP FUNCTION IF EXISTS rai_workout_completeness_immutable();
DROP INDEX IF EXISTS workout_completeness_fact_revision;

-- The readiness lineage no longer has a load-coverage upstream: its only producer
-- was the completeness receipt.
DELETE FROM analysis_model_dependencies WHERE upstream_scope = 'readiness/load-coverage/v1';

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
 VALUES(NEW.dataset_id,'daily/recovery/v1/'||split_part(scope,'/',4)||'/'||(NEW.payload->>'date'),scope) ON CONFLICT DO NOTHING;
 RETURN NEW;
END $$;
-- +goose StatementEnd

-- +goose Down
-- Irreversible by product decision: the retired control plane carried data the
-- server no longer produces, and no client can regenerate it. Rebuild it from
-- migrations 00028/00029 if an older binary must run against this database.
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'irreversible: workout_sync_completeness was retired by migration 51'; END $$;
-- +goose StatementEnd
