package postgres

import (
	"context"
	"errors"
	"time"

	am "rai/server/internal/athletemodel"
	"rai/server/internal/backfill"
	"rai/server/internal/modelmanifest"

	"github.com/jackc/pgx/v5"
)

// Auto-activation outcomes for one dataset and one pass.
const (
	AutoActivationActive           = "already_active"
	AutoActivationActivated        = "activated"
	AutoActivationPending          = "pending"
	AutoActivationFailed           = "candidate_failed"
	AutoActivationRollbackRequired = "manual_rollback_required"
	AutoActivationBackfillPaused   = "history_backfill_paused"
)

// historyBackfillDays is how far back a deployed stack recomputes history
// before activation (never before the dataset's first fact). It matches the
// window in which late facts already rebuild published days.
const historyBackfillDays = 365

// AutoActivation is what one deployment-time pass did for one dataset.
type AutoActivation struct {
	Status    string `json:"status"`
	FromStack string `json:"fromStack,omitempty"`
	ToStack   string `json:"toStack"`
	Revision  int64  `json:"revision,omitempty"`
	// History backfill of the candidate, in blocks of up to 31 days.
	HistoryFrom     string `json:"historyFrom,omitempty"`
	HistoryThrough  string `json:"historyThrough,omitempty"`
	HistoryBlocks   int    `json:"historyBlocks,omitempty"`
	HistoryComplete int    `json:"historyBlocksComplete,omitempty"`
}

// AutoActivationDatasets lists the datasets a deployment activates the
// deployed model stack for: every dataset that is not retired.
func (s *Store) AutoActivationDatasets(ctx context.Context) ([]string, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id::text FROM datasets WHERE retired_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// AutoActivateModelStack moves one dataset one step towards the stack built
// into this binary. It is idempotent and meant to be called repeatedly after
// a deployment: the first call registers the candidate and queues today's
// release, the Worker computes it, and the first call that finds the release
// ready switches active through the normal promotion transaction (the same
// readiness, backfill and manifest checks; previous is retained).
//
// Before switching, the candidate's past days are recomputed through the
// existing history backfill (up to 365 days, oldest block first), so the new
// stack's history is continuous instead of starting on the activation day.
//
// A deployed stack that is currently previous means an older image was
// redeployed; that is a rollback and stays a manual, explicit operation.
func (s *Store) AutoActivateModelStack(ctx context.Context, dataset, actor string) (AutoActivation, error) {
	stack := modelmanifest.StackID()
	out := AutoActivation{ToStack: stack}
	manifest, err := modelmanifest.CurrentHash()
	if err != nil {
		return out, err
	}
	var active, previous string
	if err = s.Pool.QueryRow(ctx, `SELECT
 COALESCE((SELECT stack_id FROM analysis_model_channels WHERE dataset_id=$1 AND channel='active'),''),
 COALESCE((SELECT stack_id FROM analysis_model_channels WHERE dataset_id=$1 AND channel='previous'),'')`, dataset).Scan(&active, &previous); err != nil {
		return out, err
	}
	out.FromStack = active
	switch stack {
	case active:
		out.Status = AutoActivationActive
		return out, nil
	case previous:
		out.Status = AutoActivationRollbackRequired
		return out, nil
	}
	if _, err = s.prepareModelStack(ctx, dataset, nil, true); err != nil {
		return out, err
	}
	op, err := s.ensureHistoryBackfill(ctx, dataset, actor, stack, manifest)
	if err != nil {
		return out, err
	}
	if op != nil {
		out.HistoryFrom, out.HistoryThrough, out.HistoryBlocks, out.HistoryComplete = op.From, op.Through, op.TotalBlocks, op.CompletedBlocks
		switch op.Status {
		case "paused":
			// A day failed; `rai analysis backfill resume` retries it.
			out.Status = AutoActivationBackfillPaused
			return out, nil
		case "candidate_ready":
		default:
			out.Status = AutoActivationPending
			return out, nil
		}
	}
	audit, err := s.promoteModelStack(ctx, dataset, actor, stack, manifest, nil, nil, true)
	if err == nil {
		out.Status, out.Revision, out.FromStack = AutoActivationActivated, audit.Revision, audit.FromStack
		return out, nil
	}
	if !errors.Is(err, am.ErrGateClosed) {
		return out, err
	}
	// Not ready yet. Report a candidate whose release for today has failed
	// with nothing left to retry, so the caller does not wait for nothing.
	var failed bool
	if err = s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM jobs j JOIN datasets d ON d.id=j.dataset_id
 WHERE j.dataset_id=$1 AND j.kind=$2 AND j.state IN ('failed','repair_required')
 AND j.scope_key='model/'||$3||'/athlete/day/v2/'||(clock_timestamp() AT TIME ZONE d.timezone)::date::text)
 AND NOT EXISTS(SELECT 1 FROM jobs j JOIN datasets d ON d.id=j.dataset_id
 WHERE j.dataset_id=$1 AND j.kind=$2 AND j.state IN ('queued','running')
 AND j.scope_key='model/'||$3||'/athlete/day/v2/'||(clock_timestamp() AT TIME ZONE d.timezone)::date::text)`,
		dataset, modelmanifest.StackJobKind, stack).Scan(&failed); err != nil {
		return out, err
	}
	out.Status = AutoActivationPending
	if failed {
		out.Status = AutoActivationFailed
	}
	return out, nil
}

// ensureHistoryBackfill returns the candidate's history backfill, creating
// and scheduling it on first use. It returns nil when there is no past day
// with facts to recompute.
func (s *Store) ensureHistoryBackfill(ctx context.Context, dataset, actor, stack, manifest string) (*BackfillOperation, error) {
	var id, status string
	err := s.Pool.QueryRow(ctx, `SELECT id::text,status FROM model_backfill_operations
 WHERE dataset_id=$1 AND stack_id=$2 AND manifest_sha256=decode($3,'hex') AND status<>'cancelled'
 ORDER BY created_at DESC LIMIT 1`, dataset, stack, manifest).Scan(&id, &status)
	if err == nil {
		if status == "planned" { // created, but the first block was never scheduled
			if _, err = s.ResumeBackfillOperation(ctx, dataset, id); err != nil {
				return nil, err
			}
		}
		op, err := s.ReadBackfillOperation(ctx, dataset, id)
		return &op, err
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var today, earliest string
	if err = s.Pool.QueryRow(ctx, `SELECT (clock_timestamp() AT TIME ZONE d.timezone)::date::text,
 COALESCE((LEAST((SELECT min(event_start) FROM workouts WHERE dataset_id=d.id),
                 (SELECT min(event_start) FROM health_samples WHERE dataset_id=d.id)) AT TIME ZONE d.timezone)::date::text,'')
 FROM datasets d WHERE d.id=$1`, dataset).Scan(&today, &earliest); err != nil {
		return nil, err
	}
	if earliest == "" {
		return nil, nil
	}
	day, err := time.Parse(backfill.DayLayout, today)
	if err != nil {
		return nil, err
	}
	first, err := time.Parse(backfill.DayLayout, earliest)
	if err != nil {
		return nil, err
	}
	// Today is the prepared release itself; history ends yesterday.
	through, from := day.AddDate(0, 0, -1), day.AddDate(0, 0, -historyBackfillDays)
	if first.After(from) {
		from = first
	}
	if from.After(through) {
		return nil, nil
	}
	req := backfill.Request{DatasetID: dataset, ManifestSHA: manifest, StackID: stack,
		From: from.Format(backfill.DayLayout), Through: through.Format(backfill.DayLayout), AsOf: today,
		View: backfill.ViewRevised, Operation: backfill.OperationBackfill, Actor: actor, BlockDays: backfill.MaxBlockDays}
	plan, err := backfill.Plan(req, backfill.Facts{Today: today})
	if err != nil {
		return nil, err
	}
	op, err := s.CreateBackfillOperation(ctx, dataset, actor, req, plan)
	if err != nil {
		return nil, err
	}
	if _, err = s.ResumeBackfillOperation(ctx, dataset, op.ID); err != nil {
		return nil, err
	}
	op, err = s.ReadBackfillOperation(ctx, dataset, op.ID)
	return &op, err
}
