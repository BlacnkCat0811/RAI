-- +goose Up
-- API reads only artifacts that belong to a SUCCEEDED published run whose
-- dataset scope is clean at the publication generation. Candidate/unpublished
-- artifacts remain readable only by the worker/admin roles.
CREATE VIEW analysis_published_artifacts WITH (security_barrier=true) AS
SELECT a.dataset_id, a.run_id, a.artifact_key, a.schema_version, a.content_sha256, a.payload
FROM analysis_artifacts a
JOIN analysis_runs r ON r.dataset_id=a.dataset_id AND r.id=a.run_id AND r.status='SUCCEEDED'
WHERE EXISTS (
    SELECT 1
    FROM analysis_publications p
    JOIN dirty_scopes d ON d.dataset_id=p.dataset_id AND d.scope_key=p.scope_key
      AND d.generation=p.generation AND d.clean_generation=p.generation
    WHERE p.dataset_id=r.dataset_id AND p.published_run_id=r.id AND p.generation=r.target_generation
);

-- +goose Down
DROP VIEW IF EXISTS analysis_published_artifacts;
