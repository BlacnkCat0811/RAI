-- Run explicitly as the database owner AFTER migration, inside the dedicated RAI DB.
-- These are NOLOGIN permission groups; create credential-bearing LOGIN roles separately.
CREATE ROLE rai_api NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION;
CREATE ROLE rai_worker NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
-- Runtime accounts may not shadow SECURITY DEFINER relations via pg_temp.
DO $$ BEGIN
 EXECUTE format('REVOKE TEMPORARY ON DATABASE %I FROM PUBLIC', current_database());
END $$;
REVOKE EXECUTE ON ALL FUNCTIONS IN SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO rai_api, rai_worker;
GRANT SELECT ON goose_db_version, datasets, devices, collector_leases, sync_streams, sync_batches, sync_pages,
    health_samples, workout_samples, workouts, workout_statistics, workout_events, workout_activities,
    series_uploads, series_chunks, workout_packages, workout_package_chunks,
    workout_enrichments, workout_imports, route_deletions,
    entity_tombstones, entity_changes, change_events, dirty_scopes, jobs TO rai_api;
-- Route staging is append-only. The API role can register the manifest and read
-- state, but cannot insert chunks/points, alter chunk data, set sealed state, or
-- forge a receipt. The SECURITY DEFINER functions below own those writes.
GRANT SELECT ON workout_routes, route_chunks, route_points TO rai_api;
GRANT INSERT(dataset_id,route_uuid,workout_uuid,source_bundle_id,dataset_epoch,collector_epoch,
    expected_chunk_count,expected_point_count,first_recorded_at,last_recorded_at,
    expected_payload_manifest_sha256) ON workout_routes TO rai_api;
GRANT INSERT ON route_deletions, workout_enrichments TO rai_api;
GRANT UPDATE(commit_rev) ON datasets TO rai_api;
GRANT INSERT ON sync_streams, sync_batches, sync_pages, health_samples, workout_samples, workouts, workout_statistics, workout_events, workout_activities, series_uploads, series_chunks,
    workout_packages, workout_package_chunks, workout_imports, entity_tombstones,
    entity_changes, change_events, dirty_scopes, jobs TO rai_api;
GRANT EXECUTE ON FUNCTION rai_route_append_chunk(uuid,uuid,integer,integer,bytea,bytea,jsonb) TO rai_api;
GRANT EXECUTE ON FUNCTION rai_route_seal(uuid,uuid,integer,bigint,bytea,bytea,bytea,bigint) TO rai_api;
GRANT EXECUTE ON FUNCTION rai_route_delete(uuid,uuid,bigint,text) TO rai_api;
GRANT UPDATE(last_client_seq,last_batch_id) ON sync_streams TO rai_api;
GRANT UPDATE(state,receipt,committed_at) ON sync_batches TO rai_api;
GRANT UPDATE(committed_fragments,manifest_sha256,receipt,sealed_at) ON sync_pages TO rai_api;
GRANT UPDATE(state,sealed_chunks,total_points,manifest_sha256,chunk_count,receipt,sealed_at) ON series_uploads TO rai_api;
GRANT UPDATE(committed_chunks,total_details,committed_activities,committed_statistics,committed_events,last_statistic_type,manifest_sha256,state,chunk_count,receipt,sealed_at) ON workout_packages TO rai_api;
GRANT DELETE ON health_samples, workouts, workout_packages TO rai_api;
GRANT UPDATE(generation,dirty_from,dirty_through) ON dirty_scopes TO rai_api;
GRANT SELECT, INSERT ON sync_request_budgets TO rai_api;
GRANT UPDATE(available_at) ON sync_request_budgets TO rai_api;
GRANT SELECT, INSERT ON sync_upload_slots TO rai_api;
GRANT UPDATE(device_id,token,expires_at) ON sync_upload_slots TO rai_api;

GRANT SELECT ON goose_db_version, datasets, dirty_scopes, jobs, job_attempts TO rai_worker;
-- SELECT FOR UPDATE requires UPDATE privilege, even when the locked value is unchanged.
GRANT UPDATE(commit_rev) ON datasets TO rai_worker;
GRANT UPDATE ON jobs, job_attempts TO rai_worker;
GRANT INSERT ON job_attempts TO rai_worker;
GRANT UPDATE(clean_generation) ON dirty_scopes TO rai_worker;

-- P4: API reads published DTO inputs; cannot write derived history/pointers.
GRANT SELECT ON analysis_runs, analysis_published_results, analysis_publications TO rai_api;
GRANT SELECT ON workouts, workout_statistics, workout_events, workout_activities,
    health_samples, workout_samples, workout_enrichments, workout_routes,
    route_points, series_uploads, series_chunks, workout_imports, analysis_parameter_sets,
    analysis_runs, analysis_results, analysis_publications TO rai_worker;
GRANT INSERT ON analysis_runs, analysis_results, analysis_publications, dirty_scopes, jobs TO rai_worker;
GRANT UPDATE(status,finished_at,error_code) ON analysis_runs TO rai_worker;
GRANT UPDATE ON analysis_publications TO rai_worker;
GRANT DELETE ON analysis_publications TO rai_worker;
GRANT UPDATE(generation,dirty_from,dirty_through) ON dirty_scopes TO rai_worker;
GRANT EXECUTE ON FUNCTION rai_analysis_dirty(uuid,text,text,timestamptz,timestamptz) TO rai_api,rai_worker;
GRANT SELECT ON analysis_parameter_sets TO rai_api;

-- P5: immutable audit rows are protected by triggers for both online roles.
GRANT SELECT ON training_parameter_sets, training_plans, training_configs,
 training_sessions, training_generation_runs, training_base_versions,
 training_overrides, training_effective_versions, training_executions,
 training_feedback, training_commands TO rai_api,rai_worker;
GRANT INSERT ON training_plans, training_configs, training_sessions, training_overrides,
 training_effective_versions, training_executions, training_feedback, training_commands TO rai_api;
GRANT UPDATE ON training_plans, training_executions TO rai_api;
GRANT INSERT ON training_sessions, training_generation_runs, training_base_versions,
 training_effective_versions, training_feedback TO rai_worker;
GRANT UPDATE ON training_plans TO rai_worker;
GRANT UPDATE(status,completed_at,error_code) ON training_generation_runs TO rai_worker;
GRANT EXECUTE ON FUNCTION rai_training_dirty(uuid,uuid) TO rai_api,rai_worker;


-- M5A reviewed daily coverage state, sharing the existing fact permissions.
GRANT SELECT, INSERT, UPDATE ON daily_health_coverage TO rai_api;

-- M1: online API cannot read/write candidate artifacts or parameter inputs.
-- A narrowly scoped SECURITY DEFINER dirty trigger owns immediate invalidation.
GRANT SELECT ON analysis_model_channels, analysis_model_dependencies TO rai_api,rai_worker;
GRANT SELECT ON analysis_artifacts, athlete_model_inputs, entity_tombstones TO rai_worker;
GRANT SELECT ON analysis_published_artifacts, analysis_published_research_artifacts TO rai_api,rai_worker;
GRANT INSERT ON analysis_artifacts TO rai_worker;
GRANT EXECUTE ON FUNCTION rai_feature_downstream_dirty(uuid,text,timestamptz,timestamptz) TO rai_worker;

-- M2: append-only candidate ledger; API has no load-ledger/artifact access.
GRANT SELECT, INSERT ON analysis_load_ledger TO rai_worker;

-- M4-B3: read-only snapshot identity/coverage for external candidate worker.
GRANT SELECT ON daily_health_coverage, sync_streams, collector_leases TO rai_worker;
GRANT SELECT(dataset_id,batch_id,dataset_epoch,envelope) ON sync_batches TO rai_worker;
GRANT SELECT(dataset_id,id,can_collect,revoked_at) ON devices TO rai_worker;

-- Fixed-scope, dataset-locked control-boundary invalidation; no channel/input writes.
GRANT EXECUTE ON FUNCTION rai_vo2_external_dirty(uuid) TO rai_worker;

-- M5B: day advancement only; no assignment/raw mutation privilege.
GRANT EXECUTE ON FUNCTION rai_recovery_dirty_day(uuid,date) TO rai_worker;
GRANT SELECT ON change_events TO rai_worker;

-- M6: candidate invalidation/day advancement, no coverage/assignment writes.
GRANT EXECUTE ON FUNCTION rai_readiness_dirty(uuid) TO rai_worker;

-- UA1-R: append-only verified coverage; no runtime mutation of history.
GRANT SELECT, INSERT ON workout_sync_completeness TO rai_api;
GRANT SELECT ON workout_sync_completeness TO rai_worker;

GRANT EXECUTE ON FUNCTION rai_commit_typed_model_input(uuid,uuid,uuid,text,timestamptz,timestamptz,uuid,uuid,text,bytea,jsonb,text) TO rai_api;
GRANT EXECUTE ON FUNCTION rai_model_stack_tick(uuid,timestamptz) TO rai_worker;
GRANT EXECUTE ON FUNCTION rai_backfill_progress(uuid,uuid,boolean),rai_backfill_job_allowed(uuid,uuid) TO rai_worker;
