-- +goose Up
-- A per-Workout failure is a terminal outcome, independent of a successful
-- feature cache. Publish it with the day release so readers cannot confuse
-- an isolated invalid/over-limit input with a still-running calculation.
CREATE TABLE model_workout_failures (
 dataset_id uuid NOT NULL,
 stack_id text NOT NULL,
 workout_uuid uuid NOT NULL,
 fact_generation bigint NOT NULL CHECK (fact_generation >= 0),
 error_code text NOT NULL CHECK (error_code IN ('analysis_invalid_input','analysis_input_limit')),
 failed_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(dataset_id,stack_id,workout_uuid),
 FOREIGN KEY(dataset_id,workout_uuid) REFERENCES workouts(dataset_id,workout_uuid) ON DELETE CASCADE
);
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='rai_worker') THEN
  GRANT SELECT,INSERT,UPDATE,DELETE ON model_workout_failures TO rai_worker;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='rai_api') THEN
  GRANT SELECT ON model_workout_failures TO rai_api;
 END IF;
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='rai_monitor_login') THEN
  GRANT SELECT(dataset_id,channel,stack_id) ON analysis_model_channels TO rai_monitor_login;
 END IF;
END $$;
-- +goose StatementEnd

-- previous is a frozen rollback reference, not a second live computation lane.
-- An active stack not supported by a rolling-upgrade worker is left for a
-- compatible worker; it must never be claimed and failed as unsupported.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rai_model_stack_invalidate(p_dataset uuid,p_from timestamptz,p_through timestamptz,p_workout text)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE m record; tz text; today date; day_prefix text; affected_from date; affected_through date;
BEGIN
 SELECT timezone INTO tz FROM datasets WHERE id=p_dataset AND retired_at IS NULL FOR UPDATE;
 IF NOT FOUND THEN RETURN; END IF;
 today:=(clock_timestamp() AT TIME ZONE tz)::date;
 affected_from:=(p_from AT TIME ZONE tz)::date;
 affected_through:=(p_through AT TIME ZONE tz)::date+214;
 FOR m IN SELECT DISTINCT stack_id FROM analysis_model_channels
  WHERE dataset_id=p_dataset AND channel IN ('active','candidate') AND extension_configs ? 'rai.model_manifest/1' ORDER BY stack_id
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
CREATE OR REPLACE FUNCTION rai_model_stack_tick(p_dataset uuid,p_at timestamptz)
RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE m record; day date; scope text;
BEGIN
 SELECT (p_at AT TIME ZONE timezone)::date INTO day FROM datasets WHERE id=p_dataset AND retired_at IS NULL FOR UPDATE;
 IF NOT FOUND THEN RETURN; END IF;
 FOR m IN SELECT DISTINCT stack_id FROM analysis_model_channels
  WHERE dataset_id=p_dataset AND channel IN ('active','candidate') AND extension_configs ? 'rai.model_manifest/1' ORDER BY stack_id
 LOOP
  scope:='model/'||m.stack_id||'/athlete/day/v2/'||day::text;
  IF NOT EXISTS (SELECT 1 FROM dirty_scopes WHERE dataset_id=p_dataset AND scope_key=scope) THEN
   PERFORM rai_schedule_model_day(p_dataset,m.stack_id,day,p_at);
  END IF;
 END LOOP;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rai_model_stack_rebuild_history(p_dataset uuid,p_stack text,p_at timestamptz,p_limit integer)
RETURNS integer LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,public AS $$
DECLARE tz text; today date; prefix text; queued integer;
BEGIN
 SELECT timezone INTO tz FROM datasets WHERE id=p_dataset AND retired_at IS NULL FOR UPDATE;
 IF NOT FOUND THEN RETURN 0; END IF;
 IF NOT EXISTS(SELECT 1 FROM analysis_model_channels WHERE dataset_id=p_dataset AND stack_id=p_stack
  AND channel IN ('active','candidate') AND extension_configs ? 'rai.model_manifest/1') THEN RETURN 0; END IF;
 IF EXISTS(SELECT 1 FROM jobs WHERE dataset_id=p_dataset AND kind='analysis.model_stack_v2'
  AND state IN ('queued','running') AND scope_key LIKE 'model/'||p_stack||'/athlete/day/v2/%') THEN RETURN 0; END IF;
 today:=(p_at AT TIME ZONE tz)::date;
 prefix:='model/'||p_stack||'/athlete/day/v2/';
 WITH stale AS (
  SELECT d.scope_key,d.generation FROM dirty_scopes d
  WHERE d.dataset_id=p_dataset AND d.generation<>d.clean_generation
   AND d.scope_key >= prefix||(today-365)::text AND d.scope_key < prefix||today::text
   AND left(d.scope_key,length(prefix))=prefix
   AND EXISTS(SELECT 1 FROM analysis_publications p WHERE p.dataset_id=d.dataset_id AND p.scope_key=d.scope_key)
   AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.dataset_id=d.dataset_id AND j.kind='analysis.model_stack_v2'
    AND j.scope_key=d.scope_key AND j.target_generation=d.generation)
  ORDER BY d.scope_key DESC LIMIT GREATEST(p_limit,0)
 ), added AS (
  INSERT INTO jobs(id,dataset_id,kind,scope_key,target_generation)
  SELECT gen_random_uuid(),p_dataset,'analysis.model_stack_v2',scope_key,generation FROM stale
  ON CONFLICT(dataset_id,kind,scope_key,target_generation) DO NOTHING RETURNING 1
 ) SELECT count(*) INTO queued FROM added;
 RETURN queued;
END $$;
-- +goose StatementEnd

-- +goose Down
-- Keep failure outcomes and the frozen previous stack on rollback.
