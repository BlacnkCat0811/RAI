-- +goose Up
-- Performance caches and stale reads; no fact, input, publication or channel
-- is rewritten and no result changes.
--
-- 1. model_workout_results caches two per-Workout calculations of the day
--    release next to the feature: the VO2 evidence and the training effect.
--    Each is keyed by a hash of exactly the inputs it depends on, so a day
--    job reuses it instead of reloading the Workout's raw samples (VO2) or
--    its effort points (training effect). The row is per model stack, so an
--    algorithm change (new stack) never reads another stack's cache.
ALTER TABLE model_workout_results
 ADD COLUMN vo2_key text CHECK (vo2_key IS NULL OR length(vo2_key)=64),
 ADD COLUMN vo2_evidence jsonb CHECK (vo2_evidence IS NULL OR jsonb_typeof(vo2_evidence) IN ('object','null')),
 ADD COLUMN effect_key text CHECK (effect_key IS NULL OR length(effect_key)=64),
 ADD COLUMN effect jsonb CHECK (effect IS NULL OR jsonb_typeof(effect)='object');

-- 2. A day scope records the earliest event time of the facts and inputs that
--    invalidated it since its last clean publication (stale_from). It is only
--    trustworthy when the latest generation bump went through one of the two
--    functions below (stale_mark=generation); any other bump (an explicit
--    reanalysis) leaves it untrusted. The pace review of a Workout that ended
--    before stale_from does not depend on any of those changes, so the last
--    published review stays valid while the day is recomputed.
ALTER TABLE dirty_scopes ADD COLUMN stale_from timestamptz, ADD COLUMN stale_mark bigint;

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
  UPDATE dirty_scopes SET generation=generation+1,dirty_from=LEAST(dirty_from,p_from),dirty_through=GREATEST(dirty_through,p_through),
   stale_from=CASE WHEN generation=clean_generation THEN p_from
    WHEN stale_mark=generation AND stale_from IS NOT NULL THEN LEAST(stale_from,p_from) END,
   stale_mark=generation+1
   WHERE dataset_id=p_dataset AND scope_key >= day_prefix||affected_from::text
    AND scope_key < day_prefix||LEAST(today,affected_through+1)::text
    AND left(scope_key,length(day_prefix))=day_prefix;
  IF p_workout IS NOT NULL THEN
   UPDATE dirty_scopes SET generation=generation+1,dirty_from=LEAST(dirty_from,p_from),dirty_through=GREATEST(dirty_through,p_through)
    WHERE dataset_id=p_dataset AND scope_key='model/'||m.stack_id||'/workout/'||p_workout;
  END IF;
  IF today BETWEEN affected_from AND affected_through THEN
   PERFORM rai_schedule_model_day(p_dataset,m.stack_id,today,clock_timestamp());
   -- Scheduling bumps today with the current time; the change itself may be
   -- older (a morning run, a backdated setting).
   UPDATE dirty_scopes SET stale_from=CASE WHEN stale_mark=generation AND stale_from IS NOT NULL THEN LEAST(stale_from,p_from) END
    WHERE dataset_id=p_dataset AND scope_key=day_prefix||today::text;
  END IF;
 END LOOP;
END $$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION rai_analysis_dirty(p_dataset uuid,p_scope text,p_kind text,p_from timestamptz,p_through timestamptz)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE g bigint;
BEGIN
    INSERT INTO dirty_scopes(dataset_id,scope_key,generation,dirty_from,dirty_through,stale_from,stale_mark)
    VALUES(p_dataset,p_scope,1,p_from,p_through,p_from,1)
    ON CONFLICT(dataset_id,scope_key) DO UPDATE SET generation=dirty_scopes.generation+1,
        dirty_from=CASE WHEN dirty_scopes.generation=dirty_scopes.clean_generation THEN EXCLUDED.dirty_from ELSE LEAST(dirty_scopes.dirty_from,EXCLUDED.dirty_from) END,
        dirty_through=CASE WHEN dirty_scopes.generation=dirty_scopes.clean_generation THEN EXCLUDED.dirty_through ELSE GREATEST(dirty_scopes.dirty_through,EXCLUDED.dirty_through) END,
        stale_from=CASE WHEN dirty_scopes.generation=dirty_scopes.clean_generation THEN EXCLUDED.dirty_from
         WHEN dirty_scopes.stale_mark=dirty_scopes.generation AND dirty_scopes.stale_from IS NOT NULL THEN LEAST(dirty_scopes.stale_from,EXCLUDED.dirty_from) END,
        stale_mark=dirty_scopes.generation+1
    RETURNING generation INTO g;
    IF p_kind NOT IN ('analysis.workout','analysis.training_state','analysis.sample','analysis.series',
        'analysis.workout_features','analysis.training_load','analysis.load_state','analysis.performance',
        'analysis.physiological_recovery','analysis.training_readiness','analysis.vo2max_external_projection') THEN
        INSERT INTO jobs(id,dataset_id,kind,scope_key,target_generation)
        VALUES(gen_random_uuid(),p_dataset,p_kind,p_scope,g);
    END IF;
END $$;
-- +goose StatementEnd

-- 3. The active product view serves the last clean publication of a scope
--    even after a newer generation invalidated it. Every reader keeps its own
--    explicit generation check; only today's athlete read and the pace review
--    choose to serve the previous release while it is recomputed, and say so.
CREATE OR REPLACE VIEW analysis_published_artifacts WITH (security_barrier=true) AS
SELECT a.dataset_id, a.run_id, a.artifact_key, a.schema_version, a.content_sha256, a.payload
FROM analysis_artifacts a
JOIN analysis_runs r ON r.dataset_id=a.dataset_id AND r.id=a.run_id AND r.status='SUCCEEDED'
JOIN analysis_model_channels m ON m.dataset_id=r.dataset_id AND m.channel='active' AND m.stack_id<>'legacy'
JOIN datasets ds ON ds.id=m.dataset_id AND ds.retired_at IS NULL
WHERE left(r.scope_key,length(m.stack_id)+7)='model/'||m.stack_id||'/'
 AND a.artifact_key IN ('workout_features','load_state','performance_state_v2','vo2max_external_projection',
     'vo2max_submax','physiological_recovery','training_readiness','session_recovery','running_ability_estimate','load_focus',
     'running_fitness','lactate_threshold','workout_pace_reviews')
 AND EXISTS (
    SELECT 1
    FROM analysis_publications p
    JOIN dirty_scopes d ON d.dataset_id=p.dataset_id AND d.scope_key=p.scope_key
      AND d.clean_generation=p.generation
    WHERE p.dataset_id=r.dataset_id AND p.published_run_id=r.id
      AND p.scope_key=r.scope_key AND p.generation=r.target_generation
 );

-- +goose Down
-- Cache columns and stale markers are retained for binary rollback; an older
-- binary neither reads nor writes them.
