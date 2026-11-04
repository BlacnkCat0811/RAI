package postgres

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/analysis"
	rc "rai/server/internal/recovery"
)

func recoveryLineage(in rc.Input) any {
	refs := []rc.Ref{}
	for _, f := range in.Facts {
		refs = append(refs, rc.Ref{Type: f.Type, ID: f.ID, Hash: f.Hash})
	}
	for _, t := range in.Tombstones {
		refs = append(refs, rc.Ref{Type: t.Type, ID: t.ID, Hash: t.Hash})
	}
	return map[string]any{"refs": refs, "configHash": in.Config.Hash(), "assignmentID": in.Assignment, "timezone": in.Config.Timezone, "calendarHash": rc.Hash(in.Days), "asOf": in.AsOf, "capturedAt": in.CapturedAt, "view": in.View, "knownThroughRevision": in.KnownThroughRevision}
}
func (s *Store) recoveryCurrent(ctx context.Context, tx pgx.Tx, l Lease, in analysis.Snapshot) (bool, error) {
	if in.RecoveryInput == nil {
		return false, analysis.Invalid()
	}
	a := *in.RecoveryInput
	var enabled bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM datasets d JOIN analysis_model_channels m ON m.dataset_id=d.id AND m.channel='candidate' LEFT JOIN collector_leases c ON c.dataset_id=d.id WHERE d.id=$1 AND d.retired_at IS NULL AND d.epoch::text=$2 AND d.timezone=$3 AND COALESCE(c.collector_epoch::text,'')=$4 AND m.extension_configs->$5->>'assignmentID'=$6 AND m.extension_configs->$5->>'configHash'=$7 AND NOT EXISTS(SELECT 1 FROM collector_leases c LEFT JOIN devices v ON v.dataset_id=c.dataset_id AND v.id=c.device_id WHERE c.dataset_id=d.id AND (v.id IS NULL OR NOT v.can_collect OR v.revoked_at IS NOT NULL)))`, l.DatasetID, a.DatasetEpoch, a.Config.Timezone, a.Collector, rc.Model, a.Assignment, a.Config.Hash()).Scan(&enabled)
	if err != nil || !enabled {
		return false, err
	}
	check := analysis.Snapshot{Generation: in.Generation, Revision: in.Revision}
	if err = s.loadRecoveryInput(ctx, tx, l, &check, time.UnixMicro(a.AsOf)); err != nil {
		return false, err
	}
	b := *check.RecoveryInput
	b.CapturedAt = a.CapturedAt
	b.KnownThroughRevision = a.KnownThroughRevision
	return rc.InputHash(a) == rc.InputHash(b), nil
}

type recoverySummaryArtifact struct {
	Schema                string `json:"schemaVersion"`
	Dataset               string `json:"datasetID"`
	Generation            int64  `json:"generation"`
	ConfigHash, InputHash string
	Features              []rc.DailyHealthFeatures `json:"features,omitempty"`
	Baseline              *rc.DailyBaseline        `json:"baseline,omitempty"`
}

func writeRecoveryArtifacts(ctx context.Context, tx pgx.Tx, l Lease, id string, in analysis.Snapshot, ms []analysis.Metric) error {
	if in.RecoveryInput == nil || in.RecoveryArtifact == nil {
		return analysis.Invalid()
	}
	expected, e := rc.Calculate(*in.RecoveryInput)
	if e != nil {
		return analysis.Invalid()
	}
	if rc.Hash(expected) != rc.Hash(*in.RecoveryArtifact) || rc.Hash(ms) != rc.Hash(analysis.RecoveryMetrics(expected.Recovery)) {
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
	f := recoverySummaryArtifact{Schema: rc.FeatureSchema, Dataset: l.DatasetID, Generation: l.TargetGeneration, ConfigHash: expected.Recovery.ConfigHash, InputHash: expected.Recovery.InputHash, Features: expected.Features}
	b := recoverySummaryArtifact{Schema: rc.BaselineSchema, Dataset: l.DatasetID, Generation: l.TargetGeneration, ConfigHash: expected.Recovery.ConfigHash, InputHash: expected.Recovery.InputHash, Baseline: &expected.Baseline}
	for _, a := range []struct {
		key, schema string
		value       any
	}{{"daily_health_features", rc.FeatureSchema, f}, {"daily_health_baseline", rc.BaselineSchema, b}, {"physiological_recovery", rc.Schema, expected.Recovery}} {
		if e = insertModelArtifact(ctx, tx, l.DatasetID, id, a.key, a.schema, a.value, rc.MaxArtifactBytes); e != nil {
			return e
		}
	}
	return nil
}

// CandidateRecovery is admin-only and reads every fence in one stable snapshot.
func (s *Store) CandidateRecovery(ctx context.Context, dataset, day string, at time.Time) (rc.Result, string, error) {
	tx, e := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return rc.Result{}, "", e
	}
	defer tx.Rollback(ctx)
	var raw []byte
	var hash, id, scope string
	var generation int64
	e = tx.QueryRow(ctx, `SELECT a.payload,encode(a.content_sha256,'hex'),p.published_run_id::text,p.scope_key,p.generation FROM analysis_publications p JOIN datasets d ON d.id=p.dataset_id AND d.retired_at IS NULL JOIN dirty_scopes x ON x.dataset_id=p.dataset_id AND x.scope_key=p.scope_key AND x.generation=p.generation AND x.clean_generation=x.generation JOIN analysis_runs r ON r.dataset_id=p.dataset_id AND r.id=p.published_run_id AND r.status='SUCCEEDED' JOIN analysis_artifacts a ON a.dataset_id=r.dataset_id AND a.run_id=r.id AND a.artifact_key='physiological_recovery' WHERE p.dataset_id=$1 AND p.metric_family=$2 AND p.day=$3::date ORDER BY p.published_at DESC LIMIT 1`, dataset, rc.Family, day).Scan(&raw, &hash, &id, &scope, &generation)
	if e != nil {
		return rc.Result{}, "", e
	}
	var r rc.Result
	if json.Unmarshal(raw, &r) != nil || rc.Hash(r) != hash {
		return r, "", analysisArtifactInvalid()
	}
	// Revalidation includes newly arriving facts and complete baseline context;
	// the read is bounded by the same 28-day window as worker computation.
	snap := analysis.Snapshot{Generation: generation, Revision: r.KnownThroughRevision}
	l := Lease{DatasetID: dataset, Kind: rc.JobKind, ScopeKey: scope, TargetGeneration: generation}
	if e = s.loadRecoveryInput(ctx, tx, l, &snap, time.UnixMicro(r.AsOf)); e != nil {
		return r, "", e
	}
	check := *snap.RecoveryInput
	check.CapturedAt = r.CapturedAt
	check.KnownThroughRevision = r.KnownThroughRevision
	if rc.InputHash(check) != r.InputHash {
		return r, "", pgx.ErrNoRows
	}
	r, e = rc.Freshen(r, at.UnixMicro())
	if e != nil {
		return r, "", e
	}
	return r, id, tx.Commit(ctx)
}
func (s *Store) HistoricalRecovery(ctx context.Context, dataset, run string) (rc.Result, bool, error) {
	var raw []byte
	var hash string
	var withdrawn bool
	e := s.Pool.QueryRow(ctx, `SELECT a.payload,encode(a.content_sha256,'hex'),EXISTS(SELECT 1 FROM jsonb_array_elements(a.payload->'factRefs') f JOIN entity_tombstones t ON t.dataset_id=a.dataset_id AND t.sample_type=f->>'type' AND t.sample_uuid=(f->>'id')::uuid) FROM analysis_artifacts a JOIN analysis_runs r ON r.dataset_id=a.dataset_id AND r.id=a.run_id AND r.status='SUCCEEDED' WHERE a.dataset_id=$1 AND a.run_id=$2 AND a.artifact_key='physiological_recovery'`, dataset, run).Scan(&raw, &hash, &withdrawn)
	var r rc.Result
	if e != nil {
		return r, false, e
	}
	if json.Unmarshal(raw, &r) != nil || rc.Hash(r) != hash {
		return r, false, analysisArtifactInvalid()
	}
	return r, withdrawn, nil
}
func (s *Store) advanceRecoveryDay(ctx context.Context, at time.Time) error {
	rows, e := s.Pool.Query(ctx, `SELECT d.id::text,m.extension_configs->$2->>'timezone' FROM datasets d JOIN analysis_model_channels m ON m.dataset_id=d.id AND m.channel='candidate' WHERE d.retired_at IS NULL AND m.extension_configs ? $2 AND d.timezone=m.extension_configs->$2->>'timezone' AND NOT EXISTS(SELECT 1 FROM dirty_scopes x WHERE x.dataset_id=d.id AND x.scope_key=(m.extension_configs->$2->>'scopePrefix')||($1::timestamptz AT TIME ZONE d.timezone)::date::text) LIMIT 20`, at, rc.Model)
	if e != nil {
		return e
	}
	type pair struct{ id, tz string }
	pairs := []pair{}
	for rows.Next() {
		var p pair
		if e = rows.Scan(&p.id, &p.tz); e != nil {
			rows.Close()
			return e
		}
		pairs = append(pairs, p)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, p := range pairs {
		loc, e := time.LoadLocation(p.tz)
		if e != nil {
			return e
		}
		day := at.In(loc).Format("2006-01-02")
		tx, e := s.begin(ctx)
		if e != nil {
			return e
		}
		if _, e = lockActiveDataset(ctx, tx, p.id); e == nil {
			_, e = tx.Exec(ctx, `SELECT rai_recovery_dirty_day($1,$2::date) WHERE NOT EXISTS(SELECT 1 FROM dirty_scopes WHERE dataset_id=$1 AND scope_key=$3)`, p.id, day, rc.Scope(p.tz, day))
		}
		if e == nil {
			e = tx.Commit(ctx)
		} else {
			_ = tx.Rollback(ctx)
		}
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
	}
	return nil
}
