package postgres

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"rai/server/internal/analysis"
	rd "rai/server/internal/readiness"
	"time"
)

func readinessLineage(in rd.ReadinessSnapshot) any {
	refs := []rd.Ref{}
	for _, w := range in.Loads {
		refs = append(refs, rd.Ref{ID: w.ID, RunID: w.RunID, Hash: w.Hash, FactHash: w.FactHash, End: w.End, ParameterID: w.ParameterID})
	}
	return map[string]any{"recovery": in.Recovery, "loadRefs": refs, "configHash": in.Config.Hash(), "assignmentID": in.Assignment, "asOf": in.AsOf, "capturedAt": in.CapturedAt, "calendarHash": rd.Hash(in.Days), "workoutSync": in.Sync}
}
func (s *Store) readinessCurrent(ctx context.Context, tx pgx.Tx, l Lease, in analysis.Snapshot) (bool, error) {
	if in.ReadinessInput == nil {
		return false, analysis.Invalid()
	}
	a := *in.ReadinessInput
	var ok bool
	e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM datasets d JOIN analysis_model_channels m ON m.dataset_id=d.id AND m.channel='candidate' LEFT JOIN collector_leases c ON c.dataset_id=d.id WHERE d.id=$1 AND d.retired_at IS NULL AND d.epoch::text=$2 AND d.timezone=$3 AND COALESCE(c.collector_epoch::text,'')=$4 AND m.extension_configs->$5->>'assignmentID'=$6 AND m.extension_configs->$5->>'configHash'=$7 AND NOT EXISTS(SELECT 1 FROM collector_leases c LEFT JOIN devices v ON v.dataset_id=c.dataset_id AND v.id=c.device_id WHERE c.dataset_id=d.id AND (v.id IS NULL OR NOT v.can_collect OR v.revoked_at IS NOT NULL)))`, l.DatasetID, a.Epoch, a.Config.Timezone, a.Collector, rd.Model, a.Assignment, a.Config.Hash()).Scan(&ok)
	if e != nil || !ok {
		return false, e
	}
	check := analysis.Snapshot{Generation: in.Generation, Revision: in.Revision}
	if e = s.loadReadinessInput(ctx, tx, l, &check, time.UnixMicro(a.AsOf)); e != nil {
		return false, e
	}
	b := *check.ReadinessInput
	b.CapturedAt = a.CapturedAt
	return rd.InputHash(a) == rd.InputHash(b), nil
}
func writeReadinessArtifact(ctx context.Context, tx pgx.Tx, l Lease, id string, in analysis.Snapshot, ms []analysis.Metric) error {
	if in.ReadinessInput == nil || in.ReadinessArtifact == nil {
		return analysis.Invalid()
	}
	expected, e := rd.Calculate(*in.ReadinessInput)
	if e != nil || rd.Hash(expected) != rd.Hash(*in.ReadinessArtifact) || rd.Hash(ms) != rd.Hash(analysis.ReadinessMetrics(expected)) {
		return analysis.Invalid()
	}
	h, e := in.Hash()
	if e != nil {
		return analysis.Invalid()
	}
	var original []byte
	if e = tx.QueryRow(ctx, `SELECT input_hash FROM analysis_runs WHERE dataset_id=$1 AND id=$2 AND job_id=$3 AND fencing_token=$4 AND status='RUNNING'`, l.DatasetID, id, l.ID, l.FencingToken).Scan(&original); e != nil {
		return e
	}
	if hex.EncodeToString(h) != hex.EncodeToString(original) {
		return analysis.Invalid()
	}
	return insertModelArtifact(ctx, tx, l.DatasetID, id, rd.Key, rd.Schema, expected, rd.MaxArtifactBytes)
}

// CandidateReadiness is an administrative read. Its bounded dependency recheck
// uses only the M5B pointer and M2 ledger; it never reads raw daily health.
func (s *Store) CandidateReadiness(ctx context.Context, dataset string, at time.Time) (rd.Result, string, error) {
	tx, e := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return rd.Result{}, "", e
	}
	defer tx.Rollback(ctx)
	var raw []byte
	var hash, id, scope string
	var generation int64
	e = tx.QueryRow(ctx, `SELECT a.payload,encode(a.content_sha256,'hex'),p.published_run_id::text,p.scope_key,p.generation FROM analysis_publications p JOIN datasets d ON d.id=p.dataset_id AND d.retired_at IS NULL JOIN dirty_scopes x ON x.dataset_id=p.dataset_id AND x.scope_key=p.scope_key AND x.generation=p.generation AND x.clean_generation=x.generation JOIN analysis_runs r ON r.dataset_id=p.dataset_id AND r.id=p.published_run_id AND r.status='SUCCEEDED' JOIN analysis_artifacts a ON a.dataset_id=r.dataset_id AND a.run_id=r.id AND a.artifact_key='training_readiness' WHERE p.dataset_id=$1 AND p.metric_family=$2 ORDER BY p.published_at DESC LIMIT 1`, dataset, rd.Family).Scan(&raw, &hash, &id, &scope, &generation)
	if e != nil {
		return rd.Result{}, "", e
	}
	var r rd.Result
	if json.Unmarshal(raw, &r) != nil || rd.Hash(r) != hash {
		return r, "", analysisArtifactInvalid()
	}
	snap := analysis.Snapshot{Generation: generation}
	l := Lease{DatasetID: dataset, Kind: rd.JobKind, ScopeKey: scope, TargetGeneration: generation}
	if e = s.loadReadinessInput(ctx, tx, l, &snap, time.UnixMicro(r.AsOf)); e != nil {
		return r, "", e
	}
	snap.ReadinessInput.CapturedAt = r.CapturedAt
	if rd.InputHash(*snap.ReadinessInput) != r.InputHash {
		return rd.Result{}, "", pgx.ErrNoRows
	}
	r, e = rd.Freshen(r, at.UnixMicro())
	if e != nil {
		return rd.Result{}, "", e
	}
	return r, id, tx.Commit(ctx)
}
func (s *Store) advanceReadinessDay(ctx context.Context, at time.Time) error {
	// At most one new job per civil day, only for enrolled existing current scope.
	rows, e := s.Pool.Query(ctx, `SELECT d.id::text FROM datasets d JOIN analysis_model_channels m ON m.dataset_id=d.id AND m.channel='candidate' JOIN dirty_scopes x ON x.dataset_id=d.id AND x.scope_key=m.extension_configs->$2->>'scope' WHERE d.retired_at IS NULL AND d.timezone=m.extension_configs->$2->>'timezone' AND x.generation=x.clean_generation AND x.dirty_through<=date_trunc('day',$1::timestamptz,d.timezone) LIMIT 20`, at, rd.Model)
	if e != nil {
		return e
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, id := range ids {
		tx, e := s.begin(ctx)
		if e != nil {
			return e
		}
		if _, e = lockActiveDataset(ctx, tx, id); e == nil {
			_, e = tx.Exec(ctx, `SELECT rai_readiness_dirty($1) WHERE EXISTS(SELECT 1 FROM datasets d JOIN analysis_model_channels m ON m.dataset_id=d.id AND m.channel='candidate' JOIN dirty_scopes x ON x.dataset_id=d.id AND x.scope_key=m.extension_configs->$2->>'scope' WHERE d.id=$1 AND x.generation=x.clean_generation AND x.dirty_through<=date_trunc('day',$3::timestamptz,d.timezone))`, id, rd.Model, at)
		}
		if e == nil {
			e = tx.Commit(ctx)
		} else {
			_ = tx.Rollback(ctx)
		}
		if e != nil {
			return e
		}
	}
	return nil
}
