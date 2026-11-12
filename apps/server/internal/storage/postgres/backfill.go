package postgres

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"time"

	am "rai/server/internal/athletemodel"
	"rai/server/internal/backfill"
	"rai/server/internal/modelmanifest"
	"rai/server/internal/syncing"

	"github.com/jackc/pgx/v5"
)

var ErrBackfillConflict = errors.New("backfill_operation_conflict")

type BackfillOperation struct {
	ID              string          `json:"id"`
	Operation       string          `json:"operation"`
	View            string          `json:"view"`
	StackID         string          `json:"stackID"`
	ManifestSHA     string          `json:"manifestSHA"`
	From            string          `json:"from"`
	Through         string          `json:"through"`
	AsOf            time.Time       `json:"asOf"`
	Status          string          `json:"status"`
	TotalBlocks     int             `json:"totalBlocks"`
	CompletedBlocks int             `json:"completedBlocks"`
	Checkpoint      json.RawMessage `json:"checkpoint"`
	Actor           string          `json:"actor"`
	ErrorCode       string          `json:"errorCode,omitempty"`
	CreatedAt       time.Time       `json:"createdAt"`
	UpdatedAt       time.Time       `json:"updatedAt"`
}

type backfillCheckpoint struct {
	Request   backfill.Request  `json:"request"`
	Blocks    []backfill.Block  `json:"blocks"`
	Warmup    []backfill.Warmup `json:"warmup"`
	Completed []backfill.Block  `json:"completed,omitempty"`
}

func marshalBackfill(req backfill.Request, p backfill.PlanResult) ([]byte, error) {
	return json.Marshal(backfillCheckpoint{Request: req, Blocks: p.Blocks, Warmup: p.Warmup})
}

// CreateBackfillOperation records a deterministic plan as a single generic
// operation. It creates only staged planning metadata; no candidate artifact
// becomes readable and no raw fact is changed.
func (s *Store) CreateBackfillOperation(ctx context.Context, dataset, actor string, req backfill.Request, p backfill.PlanResult) (BackfillOperation, error) {
	var zero BackfillOperation
	if !syncing.ValidID(dataset) || dataset != req.DatasetID || actor == "" || len(actor) > 200 {
		return zero, backfill.ErrInvalidRequest
	}
	expected, err := modelmanifest.CurrentHash()
	if req.View == backfill.ViewAsKnown {
		return zero, am.ErrHistoricalInputUnavailable
	}
	if err != nil || req.ManifestSHA != expected || req.StackID != modelmanifest.StackID() || req.Operation == backfill.OperationDryRun || len(p.Blocks) == 0 {
		return zero, backfill.ErrInvalidRequest
	}
	manifest, err := hex.DecodeString(req.ManifestSHA)
	if err != nil || len(manifest) != 32 {
		return zero, backfill.ErrInvalidRequest
	}
	cp, err := marshalBackfill(req, p)
	if err != nil {
		return zero, err
	}
	from, err := time.Parse(backfill.DayLayout, req.From)
	if err != nil {
		return zero, backfill.ErrInvalidRequest
	}
	through, err := time.Parse(backfill.DayLayout, req.Through)
	if err != nil {
		return zero, backfill.ErrInvalidRequest
	}
	asOf, err := time.Parse(backfill.DayLayout, req.AsOf)
	if err != nil {
		return zero, backfill.ErrInvalidRequest
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(ctx)
	var today string
	var eligible bool
	if err = tx.QueryRow(ctx, `SELECT (clock_timestamp() AT TIME ZONE d.timezone)::date::text,
 d.synthetic_test AND EXISTS(SELECT 1 FROM analysis_model_channels m WHERE m.dataset_id=d.id AND m.stack_id=$2 AND m.channel='candidate'
 AND m.extension_configs->>'rai.model_manifest/1'=$3 AND m.config_sha256=decode($3,'hex'))
 FROM datasets d WHERE d.id=$1 AND d.retired_at IS NULL FOR UPDATE`, dataset, req.StackID, req.ManifestSHA).Scan(&today, &eligible); err != nil {
		return zero, err
	}
	if !eligible || req.AsOf != today || req.Through > today {
		return zero, ErrBackfillConflict
	}
	expectedPlan, err := backfill.Plan(req, backfill.Facts{Today: today})
	if err != nil || !slices.Equal(expectedPlan.Blocks, p.Blocks) {
		return zero, backfill.ErrInvalidRequest
	}
	var id string
	err = tx.QueryRow(ctx, `SELECT id::text FROM model_backfill_operations WHERE dataset_id=$1 AND status<>'cancelled'
 AND checkpoint->'request'=$2::jsonb->'request' ORDER BY created_at DESC LIMIT 1`, dataset, cp).Scan(&id)
	if err == nil {
		op, e := scanBackfill(tx.QueryRow(ctx, backfillSelect+` WHERE dataset_id=$1 AND id=$2`, dataset, id))
		if e != nil {
			return zero, e
		}
		return op, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return zero, err
	}
	var other bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM model_backfill_operations WHERE dataset_id=$1 AND stack_id=$2 AND status IN ('planned','running','paused'))`, dataset, req.StackID).Scan(&other); err != nil {
		return zero, err
	}
	if other {
		return zero, ErrBackfillConflict
	}
	id = syncing.NewID()
	_, err = tx.Exec(ctx, `INSERT INTO model_backfill_operations(dataset_id,id,operation,view,stack_id,manifest_sha256,from_date,through_date,as_of,status,total_blocks,completed_blocks,checkpoint,actor) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'planned',$10,0,$11,$12)`,
		dataset, id, "execute", string(req.View), req.StackID, manifest, from, through, asOf, len(p.Blocks), cp, actor)
	if err != nil {
		return zero, err
	}
	op, e := scanBackfill(tx.QueryRow(ctx, backfillSelect+` WHERE dataset_id=$1 AND id=$2`, dataset, id))
	if e != nil {
		return zero, e
	}
	return op, tx.Commit(ctx)
}

const backfillSelect = `SELECT id::text,operation,view,stack_id,encode(manifest_sha256,'hex'),from_date::text,through_date::text,as_of,status,total_blocks,completed_blocks,checkpoint,actor,COALESCE(error_code,''),created_at,updated_at FROM model_backfill_operations`

func scanBackfill(row interface{ Scan(...any) error }) (BackfillOperation, error) {
	var o BackfillOperation
	err := row.Scan(&o.ID, &o.Operation, &o.View, &o.StackID, &o.ManifestSHA, &o.From, &o.Through, &o.AsOf, &o.Status, &o.TotalBlocks, &o.CompletedBlocks, &o.Checkpoint, &o.Actor, &o.ErrorCode, &o.CreatedAt, &o.UpdatedAt)
	return o, err
}

func (s *Store) ReadBackfillOperation(ctx context.Context, dataset, id string) (BackfillOperation, error) {
	return scanBackfill(s.Pool.QueryRow(ctx, backfillSelect+` WHERE dataset_id=$1 AND id=$2`, dataset, id))
}

func (s *Store) ResumeBackfillOperation(ctx context.Context, dataset, id string) ([]backfill.Block, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = lockActiveDataset(ctx, tx, dataset); err != nil {
		return nil, err
	}
	var status string
	if err = tx.QueryRow(ctx, `SELECT status FROM model_backfill_operations WHERE dataset_id=$1 AND id=$2 FOR UPDATE`, dataset, id).Scan(&status); err != nil {
		return nil, err
	}
	if status == "cancelled" {
		return nil, ErrBackfillConflict
	}
	if _, err = tx.Exec(ctx, `SELECT rai_backfill_progress($1,$2,true)`, dataset, id); err != nil {
		return nil, err
	}
	var cpRaw []byte
	if err = tx.QueryRow(ctx, `SELECT checkpoint,status FROM model_backfill_operations WHERE dataset_id=$1 AND id=$2`, dataset, id).Scan(&cpRaw, &status); err != nil {
		return nil, err
	}
	if status == "planned" {
		return nil, ErrBackfillConflict
	}
	var cp backfillCheckpoint
	if err = json.Unmarshal(cpRaw, &cp); err != nil {
		return nil, err
	}
	remaining, conflict := backfill.Resume(cp.Request, cp.Completed)
	if conflict {
		return nil, ErrBackfillConflict
	}
	return remaining, tx.Commit(ctx)
}

func (s *Store) CancelBackfillOperation(ctx context.Context, dataset, id string) (BackfillOperation, error) {
	var zero BackfillOperation
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback(ctx)
	if _, err = lockActiveDataset(ctx, tx, dataset); err != nil {
		return zero, err
	}
	var cpRaw []byte
	var status string
	if err = tx.QueryRow(ctx, `SELECT checkpoint,status FROM model_backfill_operations WHERE dataset_id=$1 AND id=$2 FOR UPDATE`, dataset, id).Scan(&cpRaw, &status); err != nil {
		return zero, err
	}
	if status == "candidate_ready" {
		return zero, ErrBackfillConflict
	}
	var cp backfillCheckpoint
	if err = json.Unmarshal(cpRaw, &cp); err != nil {
		return zero, err
	}
	for _, done := range cp.Completed {
		cp.Blocks[done.Index].Status = backfill.BlockComplete
	}
	cp.Blocks = backfill.Cancel(cp.Blocks)
	cpRaw, _ = json.Marshal(cp)
	if _, err = tx.Exec(ctx, `UPDATE model_backfill_operations SET status='cancelled',checkpoint=$3,updated_at=clock_timestamp() WHERE dataset_id=$1 AND id=$2`, dataset, id, cpRaw); err != nil {
		return zero, err
	}
	if err = tx.Commit(ctx); err != nil {
		return zero, err
	}
	return s.ReadBackfillOperation(ctx, dataset, id)
}

func (s *Store) ListBackfillOperations(ctx context.Context, dataset string, limit int) ([]BackfillOperation, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.Pool.Query(ctx, backfillSelect+` WHERE dataset_id=$1 ORDER BY created_at DESC,id LIMIT $2`, dataset, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BackfillOperation{}
	for rows.Next() {
		o, err := scanBackfill(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// DiffBackfillOperations compares immutable completed operation outputs. It does
// not consult old raw facts or reuse withdrawn points in the product reader.
func (s *Store) DiffBackfillOperations(ctx context.Context, dataset, before, after string) ([]backfill.Change, error) {
	if !syncing.ValidID(dataset) || !syncing.ValidID(before) || !syncing.ValidID(after) {
		return nil, backfill.ErrInvalidRequest
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	a, err := readBackfillSnapshot(ctx, tx, dataset, before)
	if err != nil {
		return nil, err
	}
	b, err := readBackfillSnapshot(ctx, tx, dataset, after)
	if err != nil {
		return nil, err
	}
	return backfill.Diff(a, b), tx.Commit(ctx)
}

func readBackfillSnapshot(ctx context.Context, tx pgx.Tx, dataset, id string) (backfill.Snapshot, error) {
	var out backfill.Snapshot
	var stack, from, through, tz string
	err := tx.QueryRow(ctx, `SELECT o.view,o.as_of::date::text,o.stack_id,o.from_date::text,o.through_date::text,d.timezone
 FROM model_backfill_operations o JOIN datasets d ON d.id=o.dataset_id AND d.synthetic_test
 WHERE o.dataset_id=$1 AND o.id=$2 AND o.status='candidate_ready'`, dataset, id).Scan(&out.View, &out.AsOf, &stack, &from, &through, &tz)
	if err != nil {
		return out, err
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT ON (r.scope_key,a.artifact_key)
 r.lineage->>'date',a.artifact_key,a.payload,encode(a.content_sha256,'hex'),a.schema_version,r.algorithm_name,r.algorithm_version,r.target_generation,r.finished_at,r.lineage->>'manifestSHA256'
 FROM analysis_runs r JOIN jobs j ON j.dataset_id=r.dataset_id AND j.id=r.job_id
 JOIN analysis_artifacts a ON a.dataset_id=r.dataset_id AND a.run_id=r.id
 WHERE r.dataset_id=$1 AND r.scope_key BETWEEN $3 AND $4 AND r.status='SUCCEEDED'
  AND j.kind='analysis.model_stack_v2' AND j.state='succeeded'
  AND j.target_generation=r.target_generation AND j.payload->>'backfillID'=$2
  AND EXISTS (SELECT 1 FROM analysis_publications p JOIN dirty_scopes d
    ON d.dataset_id=p.dataset_id AND d.scope_key=p.scope_key AND d.generation=p.generation AND d.clean_generation=p.generation
    WHERE p.dataset_id=r.dataset_id AND p.published_run_id=r.id AND p.scope_key=r.scope_key AND p.generation=r.target_generation)
  AND a.artifact_key IN ('load_state','running_fitness','vo2max_external_projection','vo2max_submax','physiological_recovery','training_readiness')
 ORDER BY r.scope_key,a.artifact_key,r.target_generation DESC LIMIT 2197`,
		dataset, id, modelmanifest.DayScope(stack, from), modelmanifest.DayScope(stack, through))
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var a publishedModelArtifact
		var day string
		if err := rows.Scan(&day, &a.Key, &a.Payload, &a.ContentHash, &a.Schema, &a.Algorithm, &a.Version, &a.Generation, &a.PublishedAt, &a.ManifestSHA); err != nil {
			return out, err
		}
		metric := modelProductMetric(a.Key)
		if len(out.Observations) >= 366*modelProductSlots || !validHistoryArtifactForStack(metric, dataset, tz, stack, a) {
			return out, ErrBackfillConflict
		}
		point := historyPoint(metric, day, a.Algorithm, a.Version, a.Schema, a.PublishedAt, a.Payload)
		out.Observations = append(out.Observations, backfill.Observation{Model: metric, Day: day, Value: point.Value, Unit: point.Unit, Version: point.ModelVersion, Mode: point.Mode, State: point.State, Reasons: point.ReasonCodes})
	}
	return out, rows.Err()
}
