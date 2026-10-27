-- Run explicitly as the database owner AFTER migration, inside the dedicated RAI DB.
-- These are NOLOGIN permission groups; create credential-bearing LOGIN roles separately.
CREATE ROLE rai_api NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION;
CREATE ROLE rai_worker NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO rai_api, rai_worker;
GRANT SELECT ON goose_db_version, datasets, devices, collector_leases, sync_streams, sync_batches, sync_pages,
    health_samples, workouts, workout_statistics, workout_events, workout_activities,
    series_uploads, series_chunks, workout_packages, workout_package_chunks,
    entity_tombstones, entity_changes, change_events, dirty_scopes, jobs TO rai_api;
GRANT UPDATE(commit_rev) ON datasets TO rai_api;
GRANT INSERT ON sync_streams, sync_batches, sync_pages, health_samples, workouts, workout_statistics, workout_events, workout_activities, series_uploads, series_chunks,
    workout_packages, workout_package_chunks, entity_tombstones,
    entity_changes, change_events, dirty_scopes, jobs TO rai_api;
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

GRANT SELECT ON datasets, dirty_scopes, jobs, job_attempts TO rai_worker;
-- SELECT FOR UPDATE requires UPDATE privilege, even when the locked value is unchanged.
GRANT UPDATE(commit_rev) ON datasets TO rai_worker;
GRANT UPDATE ON jobs, job_attempts TO rai_worker;
GRANT INSERT ON job_attempts TO rai_worker;
GRANT UPDATE(clean_generation) ON dirty_scopes TO rai_worker;
