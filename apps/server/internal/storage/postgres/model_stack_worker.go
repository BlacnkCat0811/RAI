package postgres

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"rai/server/internal/analysis"
	"rai/server/internal/modelmanifest"
	"rai/server/internal/syncing"
	wf "rai/server/internal/workoutfeatures"

	"github.com/jackc/pgx/v5"
)

// ProcessModelStack computes a single bounded civil-day release. Readers see
// the old valid day or the whole new release, never a partially written DAG.
func (s *Store) ProcessModelStack(ctx context.Context, l Lease, now time.Time) (string, error) {
	stack, day, ok := modelStackDay(l.ScopeKey)
	if l.Kind != modelmanifest.StackJobKind || !ok {
		return "", &analysis.Failure{Code: "analysis_unsupported_algorithm"}
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout='5s'; SET LOCAL transaction_timeout='45s'`); err != nil {
		return "", err
	}
	var epoch, collector, timezone, manifest string
	var revision, generation int64
	var captured time.Time
	err = tx.QueryRow(ctx, `SELECT ds.epoch::text,COALESCE(c.collector_epoch::text,''),ds.timezone,ds.commit_rev,
 d.generation,clock_timestamp(),COALESCE((SELECT encode(m.config_sha256,'hex') FROM analysis_model_channels m
 WHERE m.dataset_id=ds.id AND m.channel IN ('active','candidate') AND m.stack_id=$3 AND m.extension_configs->>'rai.model_manifest/1'=encode(m.config_sha256,'hex') LIMIT 1),'')
 FROM datasets ds JOIN dirty_scopes d ON d.dataset_id=ds.id AND d.scope_key=$2
 LEFT JOIN collector_leases c ON c.dataset_id=ds.id
 LEFT JOIN devices v ON v.dataset_id=c.dataset_id AND v.id=c.device_id
 WHERE ds.id=$1 AND ds.retired_at IS NULL AND (c.device_id IS NULL OR (v.revoked_at IS NULL AND v.can_collect))`,
		l.DatasetID, l.ScopeKey, stack).Scan(&epoch, &collector, &timezone, &revision, &generation, &captured, &manifest)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", analysis.Invalid()
	}
	if err != nil {
		return "", err
	}
	expected, err := modelmanifest.CurrentHash()
	if err != nil {
		return "", err
	}
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT rai_backfill_job_allowed($1,$2)`, l.DatasetID, l.ID).Scan(&allowed); err != nil {
		return "", err
	}
	if generation != l.TargetGeneration || manifest != expected || !allowed {
		if err := tx.Commit(ctx); err != nil {
			return "", err
		}
		return s.publishModelStackDay(ctx, l, stack, day, expected, revision, nil)
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return "", analysis.Invalid()
	}
	date, err := time.ParseInLocation(time.DateOnly, day, loc)
	if err != nil || date.After(now.In(loc)) {
		return "", analysis.Invalid()
	}
	at := minTime(captured, date.AddDate(0, 0, 1).Add(-time.Microsecond))
	results, err := computeModelStackDay(ctx, tx, l, stack, date, at, captured, revision, epoch, collector)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return s.publishModelStackDay(ctx, l, stack, day, manifest, revision, results)
}

func modelStackDay(scope string) (stack, day string, ok bool) {
	const prefix = "model/"
	if !strings.HasPrefix(scope, prefix) {
		return "", "", false
	}
	stack, day, ok = strings.Cut(strings.TrimPrefix(scope, prefix), "/athlete/day/v2/")
	_, err := time.Parse(time.DateOnly, day)
	return stack, day, ok && stack == modelmanifest.StackID() && err == nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

type stackArtifact struct {
	key, family, schema, algorithm, version, runID, inputHash string
	value                                                     any
}

func (s *Store) publishModelStackDay(ctx context.Context, l Lease, stack, day, manifest string, revision int64, artifacts []stackArtifact) (string, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if err = validAnalysisLease(ctx, tx, l); err != nil {
		return "", err
	}
	var deadline time.Time
	if err = tx.QueryRow(ctx, `SELECT lease_until FROM jobs WHERE id=$1`, l.ID).Scan(&deadline); err != nil {
		return "", err
	}
	var current bool
	if err := tx.QueryRow(ctx, `SELECT d.generation=$3 AND rai_backfill_job_allowed($1,$4) AND EXISTS(
 SELECT 1 FROM analysis_model_channels m WHERE m.dataset_id=ds.id AND m.channel IN ('active','candidate') AND m.stack_id=$5
 AND m.extension_configs->>'rai.model_manifest/1'=$6 AND m.config_sha256=decode($6,'hex'))
 FROM datasets ds JOIN dirty_scopes d ON d.dataset_id=ds.id AND d.scope_key=$2 WHERE ds.id=$1`,
		l.DatasetID, l.ScopeKey, l.TargetGeneration, l.ID, stack, manifest).Scan(&current); err != nil {
		return "", err
	}
	status := "SUPERSEDED"
	if current && modelStackProductsPresent(artifacts) {
		for _, a := range artifacts {
			if r, ok := a.value.(workoutResultRow); ok {
				if err := writeWorkoutResult(ctx, tx, l.DatasetID, stack, r); err != nil {
					return "", err
				}
				continue
			}
			if f, ok := a.value.(wf.WorkoutFeatures); ok {
				if err := writeStackWorkout(ctx, tx, l, stack, manifest, revision, day, a, f); err != nil {
					return "", err
				}
				continue
			}
			if err := writeStackArtifact(ctx, tx, l, day, manifest, revision, a); err != nil {
				return "", err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE dirty_scopes SET clean_generation=generation WHERE dataset_id=$1 AND scope_key=$2`, l.DatasetID, l.ScopeKey); err != nil {
			return "", err
		}
		// Today's release of the active stack (new readiness, load or Workout)
		// regenerates active training plans so today's session can follow it.
		var activeToday bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM analysis_model_channels m JOIN datasets ds ON ds.id=m.dataset_id
 WHERE m.dataset_id=$1 AND m.channel='active' AND m.stack_id=$2 AND (clock_timestamp() AT TIME ZONE ds.timezone)::date=$3::date)`,
			l.DatasetID, stack, day).Scan(&activeToday); err != nil {
			return "", err
		}
		if activeToday {
			if err := dirtyTrainingPlans(ctx, tx, l.DatasetID); err != nil {
				return "", err
			}
		}
		status = "SUCCEEDED"
	} else if current {
		return "", analysis.Invalid()
	}
	tag, err := tx.Exec(ctx, `UPDATE jobs SET state='succeeded',lease_until=NULL,last_error_code=NULL
 WHERE dataset_id=$1 AND id=$2 AND fencing_token=$3 AND state='running' AND lease_until>clock_timestamp()`, l.DatasetID, l.ID, l.FencingToken)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() != 1 {
		return "", syncing.Fail(409, "lease_lost")
	}
	tag, err = tx.Exec(ctx, `UPDATE job_attempts SET finished_at=clock_timestamp(),result_code=$3 WHERE job_id=$1 AND fencing_token=$2 AND finished_at IS NULL`, l.ID, l.FencingToken, status)
	if err != nil || tag.RowsAffected() != 1 {
		if err == nil {
			err = syncing.Fail(409, "lease_lost")
		}
		return "", err
	}
	if backfillID := modelStackBackfillID(l); status == "SUCCEEDED" && backfillID != "" {
		if _, err := tx.Exec(ctx, `SELECT rai_backfill_progress($1,$2,false)`, l.DatasetID, backfillID); err != nil {
			return "", err
		}
	}
	var beforeDeadline bool
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()<$1`, deadline).Scan(&beforeDeadline); err != nil {
		return "", err
	}
	if !beforeDeadline {
		return "", syncing.Fail(409, "lease_lost")
	}
	return status, tx.Commit(ctx)
}

func modelStackBackfillID(l Lease) string {
	var job struct {
		BackfillID string `json:"backfillID"`
	}
	if json.Unmarshal(l.Payload, &job) != nil || !syncing.ValidID(job.BackfillID) {
		return ""
	}
	return job.BackfillID
}

const modelProductSlots = 6

func writeStackArtifact(ctx context.Context, tx pgx.Tx, l Lease, day, manifest string, revision int64, a stackArtifact) error {
	inputHash, err := hex.DecodeString(a.inputHash)
	if err != nil || len(inputHash) != 32 {
		return analysis.Invalid()
	}
	run := a.runID
	if run == "" {
		run = syncing.NewID()
	}
	if _, err := tx.Exec(ctx, `INSERT INTO analysis_runs(dataset_id,id,job_id,fencing_token,scope_key,target_generation,algorithm_name,algorithm_version,
 input_version,input_hash,input_from,input_through,lineage,status)
 SELECT $1,$2,$3,$4,$5,$6,$7,$8,'rai.model_stack.input/2',$9,dirty_from,dirty_through,
 jsonb_build_object('manifestSHA256',$10::text,'revision',$11::bigint,'date',$12::text),'RUNNING'
 FROM dirty_scopes WHERE dataset_id=$1 AND scope_key=$5`,
		l.DatasetID, run, l.ID, l.FencingToken, l.ScopeKey, l.TargetGeneration, a.algorithm, a.version, inputHash, manifest, revision, day); err != nil {
		return err
	}
	limit := 2 << 20
	if a.key == wf.ArtifactKey {
		limit = wf.MaxArtifactBytes
	}
	if err := insertModelArtifact(ctx, tx, l.DatasetID, run, a.key, a.schema, a.value, limit); err != nil {
		return fmt.Errorf("artifact %s (%s/%s): %w", a.key, a.schema, a.version, err)
	}
	if _, err := tx.Exec(ctx, `UPDATE analysis_runs SET status='SUCCEEDED',finished_at=clock_timestamp() WHERE dataset_id=$1 AND id=$2`, l.DatasetID, run); err != nil {
		return err
	}
	family := a.family
	if family == "" {
		family = "model_product_v2/" + a.key
	}
	_, err = tx.Exec(ctx, `INSERT INTO analysis_publications(dataset_id,scope_key,metric_family,day,published_run_id,generation)
VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(dataset_id,scope_key,metric_family,day)
DO UPDATE SET published_run_id=EXCLUDED.published_run_id,generation=EXCLUDED.generation,published_at=clock_timestamp()`,
		l.DatasetID, l.ScopeKey, family, day, run, l.TargetGeneration)
	return err
}

func writeStackWorkout(ctx context.Context, tx pgx.Tx, l Lease, stack, manifest string, revision int64, day string, a stackArtifact, f wf.WorkoutFeatures) error {
	scope := "model/" + stack + "/workout/" + f.WorkoutID
	if _, err := tx.Exec(ctx, `INSERT INTO dirty_scopes(dataset_id,scope_key,generation,dirty_from,dirty_through)
 VALUES($1,$2,1,$3,$4) ON CONFLICT DO NOTHING`, l.DatasetID, scope, time.UnixMicro(f.Timing.Start), time.UnixMicro(f.Timing.End)); err != nil {
		return err
	}
	var gen, clean int64
	if err := tx.QueryRow(ctx, `SELECT generation,clean_generation FROM dirty_scopes WHERE dataset_id=$1 AND scope_key=$2`, l.DatasetID, scope).Scan(&gen, &clean); err != nil {
		return err
	}
	if gen == clean {
		return nil
	}
	l.ScopeKey, l.TargetGeneration = scope, gen
	f.Generation = gen
	a.value = f
	if err := writeStackArtifact(ctx, tx, l, day, manifest, revision, a); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE dirty_scopes SET clean_generation=generation WHERE dataset_id=$1 AND scope_key=$2`, l.DatasetID, scope)
	return err
}

func (s *Store) advanceModelStackDay(ctx context.Context, at time.Time) error {
	rows, err := s.Pool.Query(ctx, `SELECT DISTINCT ds.id::text FROM datasets ds
 JOIN analysis_model_channels m ON m.dataset_id=ds.id
 WHERE ds.retired_at IS NULL AND m.channel IN ('active','candidate') AND m.stack_id=$2 AND m.extension_configs ? 'rai.model_manifest/1'
 AND NOT EXISTS (SELECT 1 FROM dirty_scopes x WHERE x.dataset_id=ds.id AND x.scope_key=
  'model/'||m.stack_id||'/athlete/day/v2/'||($1::timestamptz AT TIME ZONE ds.timezone)::date::text)
 ORDER BY 1 LIMIT 20`, at, modelmanifest.StackID())
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := s.Pool.Exec(ctx, `SELECT rai_model_stack_tick($1,$2)`, id, at); err != nil {
			return err
		}
	}
	return s.rebuildModelStackHistory(ctx, at)
}

// historyRebuildBatch is how many invalidated day releases one recovery tick
// queues again per dataset. The worker handles about one job per second, so a
// batch is finished before the next tick.
const historyRebuildBatch = 10

// rebuildModelStackHistory queues published day releases that a late fact
// invalidated (last night's sleep, a run uploaded the next day), newest first.
// The database function skips a dataset while any of its day jobs is queued or
// running, so today's release always goes first and a long backfill is not
// recomputed page by page.
func (s *Store) rebuildModelStackHistory(ctx context.Context, at time.Time) error {
	stack := modelmanifest.StackID()
	rows, err := s.Pool.Query(ctx, `SELECT DISTINCT ds.id::text FROM datasets ds
 JOIN analysis_model_channels m ON m.dataset_id=ds.id
 WHERE ds.retired_at IS NULL AND m.channel IN ('active','candidate') AND m.stack_id=$1 AND m.extension_configs ? 'rai.model_manifest/1'
 AND EXISTS (SELECT 1 FROM dirty_scopes x WHERE x.dataset_id=ds.id AND x.generation<>x.clean_generation
  AND left(x.scope_key,length('model/'||m.stack_id||'/athlete/day/v2/'))='model/'||m.stack_id||'/athlete/day/v2/')
 ORDER BY 1 LIMIT 20`, stack)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := s.Pool.Exec(ctx, `SELECT rai_model_stack_rebuild_history($1,$2,$3,$4)`, id, stack, at, historyRebuildBatch); err != nil {
			return err
		}
	}
	return nil
}

func modelStackProductsPresent(artifacts []stackArtifact) bool {
	required := map[string]bool{"load_state": false, "running_fitness": false, "vo2max_external_projection": false, "vo2max_submax": false, "physiological_recovery": false, "training_readiness": false, "session_recovery": false, "load_focus": false}
	for _, a := range artifacts {
		if seen, ok := required[a.key]; ok {
			if seen {
				return false
			}
			required[a.key] = true
		}
	}
	for _, seen := range required {
		if !seen {
			return false
		}
	}
	return true
}
