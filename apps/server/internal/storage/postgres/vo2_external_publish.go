package postgres

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"rai/server/internal/analysis"
	"rai/server/internal/storage/postgres/dbgen"
	ve "rai/server/internal/vo2maxexternal"
	"time"
)

func externalLineage(a ve.ExternalVO2Snapshot) any {
	facts := [][2]string{}
	tombs := [][2]string{}
	selections := [][2]string{}
	for _, f := range a.Facts {
		facts = append(facts, [2]string{f.ID, f.Hash})
	}
	for _, t := range a.Tombstones {
		tombs = append(tombs, [2]string{t.ID, t.Hash})
	}
	for _, x := range a.Selections {
		selections = append(selections, [2]string{x.ID, x.Hash})
	}
	return map[string]any{"factRefs": facts, "tombstoneRefs": tombs, "selectionRefs": selections, "coverage": a.Coverage, "assignmentID": a.Assignment, "configHash": a.Config.Hash(), "asOf": a.AsOf, "knownThroughRevision": a.KnownThroughRevision, "collectorEpoch": a.Collector, "datasetGeneration": a.DatasetEpoch}
}

// Under dataset/job/dirty locks, re-load the complete bounded domain. This
// revalidates hashes, liveness, conflict groups, coverage and control evidence
// even on the first publication when there has never been a current pointer.
func (s *Store) vo2ExternalCurrent(ctx context.Context, tx pgx.Tx, l Lease, in analysis.Snapshot) (bool, error) {
	if in.ExternalInput == nil {
		return false, analysis.Invalid()
	}
	var generation, revision int64
	if err := tx.QueryRow(ctx, `SELECT d.commit_rev,x.generation FROM datasets d JOIN dirty_scopes x ON x.dataset_id=d.id AND x.scope_key=$2 WHERE d.id=$1`, l.DatasetID, ve.Scope).Scan(&revision, &generation); err != nil {
		return false, err
	}
	check := analysis.Snapshot{Generation: generation, Revision: revision}
	if err := s.loadVO2ExternalInput(ctx, tx, l, &check, time.UnixMicro(in.ExternalInput.AsOf)); err != nil {
		return false, err
	}
	a, b := *in.ExternalInput, *check.ExternalInput
	before, _ := ve.SelectionAt(a)
	evaluation := b
	evaluation.AsOf = b.CapturedAt
	after, _ := ve.SelectionAt(evaluation)
	oldID, newID := "", ""
	if before != nil {
		oldID = before.ID
	}
	if after != nil {
		newID = after.ID
	}
	if oldID != newID {
		if _, err := tx.Exec(ctx, `SELECT rai_vo2_external_dirty($1)`, l.DatasetID); err != nil {
			return false, err
		}
		return false, nil
	}

	// Unrelated raw commits can advance the dataset revision without polluting
	// this model's dependencies. Preserve its capture fence for comparison.
	b.CapturedAt = a.CapturedAt
	b.KnownThroughRevision = a.KnownThroughRevision
	return ve.Hash(ve.Canonical(a)) == ve.Hash(ve.Canonical(b)), nil
}
func writeVO2ExternalArtifact(ctx context.Context, tx pgx.Tx, l Lease, id string, in analysis.Snapshot, metrics []analysis.Metric) error {
	if in.ExternalInput == nil || in.ExternalArtifact == nil {
		return analysis.Invalid()
	}
	h, err := in.Hash()
	if err != nil {
		return analysis.Invalid()
	}
	var expected []byte
	if err = tx.QueryRow(ctx, `SELECT input_hash FROM analysis_runs WHERE dataset_id=$1 AND id=$2 AND job_id=$3 AND fencing_token=$4 AND status='RUNNING'`, l.DatasetID, id, l.ID, l.FencingToken).Scan(&expected); err != nil {
		return err
	}
	if hex.EncodeToString(h) != hex.EncodeToString(expected) {
		return analysis.Invalid()
	}
	r, err := ve.Calculate(*in.ExternalInput)
	if err != nil {
		return err
	}
	if ve.Hash(r) != ve.Hash(*in.ExternalArtifact) || ve.Hash(metrics) != ve.Hash(analysis.ExternalVO2Metrics(r)) {
		return analysis.Invalid()
	}
	return insertModelArtifact(ctx, tx, l.DatasetID, id, ve.ArtifactKey, ve.Schema, r, ve.MaxArtifactBytes)
}

// CandidateVO2External is admin-only. All fences and the artifact are read in
// one stable snapshot; HTTP product DTOs never call this reader.
func (s *Store) CandidateVO2External(ctx context.Context, dataset string, at time.Time) (ve.Result, string, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return ve.Result{}, "", err
	}
	defer tx.Rollback(ctx)
	row, err := dbgen.New(tx).CurrentCandidateVO2External(ctx, dbgen.CurrentCandidateVO2ExternalParams{DatasetID: dataset, Column2: ve.Scope, Column3: ve.Family, Column4: ve.Schema, Column5: ve.Model})
	raw, hash, id := row.Payload, row.Encode, row.RID
	if err != nil {
		return ve.Result{}, "", err
	}
	var r ve.Result
	if json.Unmarshal(raw, &r) != nil || ve.Hash(r) != hash || r.ConfigHash != ve.DefaultConfig().Hash() {
		return r, "", analysisArtifactInvalid()
	}
	resolved, e := externalSelectionAt(ctx, tx, dataset, at)
	if e != nil {
		return r, "", e
	}
	persisted := ""
	if r.SelectionID != nil {
		persisted = *r.SelectionID
	}
	if resolved != persisted {
		return r, "", pgx.ErrNoRows
	}
	r, err = ve.Freshen(r, at.UnixMicro())
	if err != nil {
		return r, "", err
	}
	return r, id, tx.Commit(ctx)
}
func (s *Store) HistoricalVO2External(ctx context.Context, dataset, run string) (ve.Result, bool, error) {
	var raw []byte
	var hash string
	var withdrawn bool
	err := s.Pool.QueryRow(ctx, `SELECT a.payload,encode(a.content_sha256,'hex'),EXISTS(SELECT 1 FROM jsonb_array_elements(a.payload->'factRefs') f JOIN entity_tombstones t ON t.dataset_id=a.dataset_id AND t.sample_type='HKQuantityTypeIdentifierVO2Max' AND t.sample_uuid=(f->>0)::uuid) FROM analysis_artifacts a JOIN analysis_runs r ON r.dataset_id=a.dataset_id AND r.id=a.run_id AND r.status='SUCCEEDED' WHERE a.dataset_id=$1 AND a.run_id=$2 AND a.artifact_key=$3`, dataset, run, ve.ArtifactKey).Scan(&raw, &hash, &withdrawn)
	if err != nil {
		return ve.Result{}, false, err
	}
	var r ve.Result
	if json.Unmarshal(raw, &r) != nil || ve.Hash(r) != hash {
		return r, false, analysisArtifactInvalid()
	}
	return r, withdrawn, nil
}

// Resolve evaluation-time controls in the reader's existing stable snapshot.
// No raw reload or expensive projection is needed to reject a crossed boundary.
func externalSelectionAt(ctx context.Context, tx pgx.Tx, dataset string, at time.Time) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `WITH active AS (
 SELECT id,supersedes_id,payload FROM athlete_model_inputs WHERE dataset_id=$1 AND kind='vo2max_source_selection'
 AND created_at<=$2 AND effective_from<=$2 AND (effective_through IS NULL OR effective_through>$2)),
 terminal AS (SELECT a.* FROM active a WHERE NOT EXISTS(SELECT 1 FROM active b WHERE b.supersedes_id=a.id))
 SELECT CASE WHEN count(*)=1 AND bool_and(NOT (payload->>'revoked')::boolean) THEN min(id::text) ELSE '' END FROM terminal`, dataset, at).Scan(&id)
	return id, err
}

// The shared worker already calls AdvanceAnalysisDay on its recovery cadence.
// Touch only enrolled clean external publications crossing an explicit control
// boundary. Freshness age alone never enqueues a projection.
func (s *Store) advanceExternalControls(ctx context.Context, at time.Time) error {
	rows, err := s.Pool.Query(ctx, `SELECT DISTINCT p.dataset_id::text FROM analysis_publications p
 JOIN analysis_artifacts a ON a.dataset_id=p.dataset_id AND a.run_id=p.published_run_id AND a.artifact_key='vo2max_external_projection'
 JOIN athlete_model_inputs i ON i.dataset_id=p.dataset_id AND i.kind='vo2max_source_selection'
 WHERE p.metric_family='vo2max_external_candidate' AND i.created_at<=$1 AND
 (((extract(epoch FROM i.effective_from)*1000000)::bigint>(a.payload->>'asOf')::bigint AND i.effective_from<=$1) OR
 ((extract(epoch FROM i.effective_through)*1000000)::bigint>(a.payload->>'asOf')::bigint AND i.effective_through<=$1)) LIMIT 20`, at)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		tx, e := s.begin(ctx)
		if e != nil {
			return e
		}
		if _, e = lockActiveDataset(ctx, tx, id); e == nil {
			// Recheck under the dataset lock so concurrent pollers do not duplicate work.
			var changed bool
			e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM analysis_publications p JOIN analysis_artifacts a ON a.dataset_id=p.dataset_id AND a.run_id=p.published_run_id AND a.artifact_key='vo2max_external_projection' JOIN athlete_model_inputs i ON i.dataset_id=p.dataset_id AND i.kind='vo2max_source_selection'
 WHERE p.dataset_id=$1 AND p.metric_family='vo2max_external_candidate' AND i.created_at<=$2 AND (((extract(epoch FROM i.effective_from)*1000000)::bigint>(a.payload->>'asOf')::bigint AND i.effective_from<=$2) OR ((extract(epoch FROM i.effective_through)*1000000)::bigint>(a.payload->>'asOf')::bigint AND i.effective_through<=$2)))`, id, at).Scan(&changed)
			if e == nil && changed {
				_, e = tx.Exec(ctx, `SELECT rai_vo2_external_dirty($1)`, id)
			}
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
