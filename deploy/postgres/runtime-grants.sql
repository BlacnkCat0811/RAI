-- Explicitly run as bootstrap after owner migration and roles.sql.
GRANT rai_api TO rai_api_login;
GRANT rai_worker TO rai_worker_login;
GRANT USAGE ON SCHEMA public TO rai_backup_login,rai_monitor_login;
GRANT SELECT ON ALL TABLES IN SCHEMA public TO rai_backup_login;
GRANT SELECT ON ALL SEQUENCES IN SCHEMA public TO rai_backup_login;
GRANT SELECT ON jobs,job_attempts,dirty_scopes,analysis_runs,analysis_publications,
 training_generation_runs,training_executions TO rai_monitor_login;
GRANT SELECT(dataset_id,channel,stack_id) ON analysis_model_channels TO rai_monitor_login;
GRANT pg_read_all_stats TO rai_monitor_login;
ALTER ROLE rai_api_login SET statement_timeout='2s';
ALTER ROLE rai_worker_login SET statement_timeout='2s';
ALTER ROLE rai_backup_login SET statement_timeout='10min';
ALTER ROLE rai_monitor_login SET statement_timeout='5s';
-- New objects never grant PUBLIC EXECUTE or runtime write access by default.
ALTER DEFAULT PRIVILEGES FOR ROLE rai_owner IN SCHEMA public REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC;
-- Refresh explicitly after every reviewed migration; do not grant future tables.
