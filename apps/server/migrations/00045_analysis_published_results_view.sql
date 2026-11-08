-- +goose Up
-- API reads only result rows belonging to a SUCCEEDED published run whose
-- dataset scope is clean at the publication generation. Candidate/unpublished
-- result rows remain readable only by the worker/admin roles.
CREATE VIEW analysis_published_results WITH (security_barrier=true) AS
SELECT p.dataset_id, p.scope_key, p.metric_family, p.day, p.published_run_id AS run_id,
       r.metric, r.algorithm_name, r.algorithm_version, r.value, r.unit, r.quality, r.details, r.computed_at
FROM analysis_publications p
JOIN analysis_runs run ON run.dataset_id=p.dataset_id AND run.id=p.published_run_id AND run.status='SUCCEEDED'
JOIN dirty_scopes d ON d.dataset_id=p.dataset_id AND d.scope_key=p.scope_key
  AND d.generation=p.generation AND d.clean_generation=p.generation
JOIN analysis_results r ON r.dataset_id=p.dataset_id AND r.run_id=p.published_run_id AND r.day=p.day;

-- +goose Down
DROP VIEW IF EXISTS analysis_published_results;
