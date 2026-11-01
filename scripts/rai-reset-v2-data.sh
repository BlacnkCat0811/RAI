#!/bin/sh
# Explicit, destructive v2 -> v3 reset. This is intentionally NOT a migration:
# it must be run once by an operator against an isolated/approved dataset.
# It keeps datasets, devices and collector leases so the existing enrollment
# can upload fresh Protocol 3 bundles.
set -eu

: "${RAI_DATABASE_URL:?set RAI_DATABASE_URL}"
if [ "${1:-}" != "--confirm-delete-v2-facts" ]; then
    echo "usage: $0 --confirm-delete-v2-facts" >&2
    echo "This deletes all v2 fact, analysis, sync and training-derived rows; datasets/devices remain." >&2
    exit 2
fi

psql "$RAI_DATABASE_URL" -v ON_ERROR_STOP=1 <<'SQL'
BEGIN;
TRUNCATE TABLE
    workout_imports,
    workout_routes,
    route_points,
    route_chunks,
    workout_samples,
    series_chunks,
    series_uploads,
    workout_statistics,
    workout_events,
    workout_activities,
    workouts,
    health_samples,
    workout_package_chunks,
    workout_packages,
    workout_enrichments,
    route_deletions,
    sync_pages,
    sync_batches,
    sync_streams,
    sync_upload_slots,
    sync_request_budgets,
    entity_changes,
    entity_tombstones,
    change_events,
    dirty_scopes,
    job_attempts,
    jobs,
    analysis_publications,
    analysis_results,
    analysis_runs,
    analysis_parameter_sets,
    training_feedback,
    training_executions,
    training_effective_versions,
    training_overrides,
    training_base_versions,
    training_generation_runs,
    training_sessions,
    training_configs,
    training_plans,
    training_commands,
    training_parameter_sets
CASCADE;
COMMIT;
SQL
echo "v2 fact/analysis/training data removed; datasets and device enrollment preserved." >&2
