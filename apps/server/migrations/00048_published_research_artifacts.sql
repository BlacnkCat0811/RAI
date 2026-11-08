-- +goose Up
-- The athlete diagnostics product surface reads the five independent research
-- artifacts. Keep them behind a separate read-only publication boundary so the
-- six-slot product artifact view keeps its exact row set: succeeded run, active
-- non-legacy stack, clean generation, and a matching publication. Raw evidence
-- never lives in these artifacts.
CREATE VIEW analysis_published_research_artifacts WITH (security_barrier=true) AS
SELECT a.dataset_id, a.run_id, a.artifact_key, a.schema_version, a.content_sha256, a.payload
FROM analysis_artifacts a
JOIN analysis_runs r ON r.dataset_id=a.dataset_id AND r.id=a.run_id AND r.status='SUCCEEDED'
JOIN analysis_model_channels m ON m.dataset_id=r.dataset_id AND m.channel='active' AND m.stack_id<>'legacy'
JOIN datasets ds ON ds.id=m.dataset_id AND ds.retired_at IS NULL
WHERE left(r.scope_key,length(m.stack_id)+7)='model/'||m.stack_id||'/'
 AND a.artifact_key IN ('running_ability_maximal_v2','workout_durability','aerobic_efficiency','critical_speed','load_settling')
 AND EXISTS (
    SELECT 1
    FROM analysis_publications p
    JOIN dirty_scopes d ON d.dataset_id=p.dataset_id AND d.scope_key=p.scope_key
      AND d.generation=p.generation AND d.clean_generation=p.generation
    WHERE p.dataset_id=r.dataset_id AND p.published_run_id=r.id
      AND p.scope_key=r.scope_key AND p.generation=r.target_generation
 );

-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='rai_api') THEN
  GRANT SELECT ON analysis_published_research_artifacts TO rai_api;
 END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
DROP VIEW IF EXISTS analysis_published_research_artifacts;
