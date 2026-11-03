package postgres

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"rai/server/internal/analysis"
	pf "rai/server/internal/performance"
	"rai/server/internal/storage/postgres/dbgen"
	"rai/server/internal/syncing"
	wf "rai/server/internal/workoutfeatures"
	"time"
)

func writePerformanceArtifacts(ctx context.Context, tx pgx.Tx, l Lease, id string, in analysis.Snapshot, metrics []analysis.Metric) error {
	r := in.PerformanceArtifact
	if r == nil || in.PerformanceInput == nil || r.Schema != pf.Schema || r.DatasetID != l.DatasetID || r.Generation != l.TargetGeneration || r.ConfigHash != pf.ConfigHash() || r.EvidenceMode != pf.Mode || r.Model != pf.AbilityModel || r.Version != pf.Version || r.AsOf != in.PerformanceInput.AsOf {
		return analysis.Invalid()
	}
	// Bind all output to the immutable input hash captured at StartAnalysisRun.
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
	if pf.Hash(metrics) != pf.Hash(analysis.PerformanceMetrics(*r)) {
		return analysis.Invalid()
	}
	for _, a := range []struct {
		key, schema string
		value       any
	}{
		{pf.EvidenceKey, pf.EvidenceSchema, r.Envelope(pf.EvidenceSchema, r.Evidence)},
		{pf.CurveKey, pf.CurveSchema, r.Envelope(pf.CurveSchema, struct {
			Curve   *pf.Curve   `json:"curve"`
			Targets []pf.Target `json:"targets"`
		}{r.Curve, r.Targets})},
		{pf.AbilityKey, pf.AbilitySchema, r.Envelope(pf.AbilitySchema, r.Ability)},
		{pf.EnduranceKey, pf.EnduranceSchema, r.Envelope(pf.EnduranceSchema, struct {
			Retention *float64 `json:"retention"`
			State     string   `json:"state"`
		}{retention(r), r.Status})},
		{pf.ArtifactKey, pf.Schema, r},
	} {
		if err = insertModelArtifact(ctx, tx, l.DatasetID, id, a.key, a.schema, a.value, pf.MaxArtifactBytes); err != nil {
			return err
		}
	}
	return nil
}

// Check the complete captured feature identity under the dataset lock. A stale
// seed can race even when there is no current pointer to invalidate yet.
func performanceDependenciesCurrent(ctx context.Context, tx pgx.Tx, l Lease, in analysis.Snapshot) (bool, error) {
	if in.PerformanceInput == nil {
		return false, analysis.Invalid()
	}
	refs := []map[string]any{}
	for _, ref := range in.PerformanceInput.Features {
		refs = append(refs, map[string]any{"workout": ref.Feature.WorkoutID, "run": ref.RunID, "generation": ref.Feature.Generation})
	}
	body, err := json.Marshal(refs)
	if err != nil {
		return false, analysis.Invalid()
	}
	var current bool
	err = tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM jsonb_to_recordset($2::jsonb) AS x(workout uuid,run uuid,generation bigint)
 LEFT JOIN workouts w ON w.dataset_id=$1 AND w.workout_uuid=x.workout
 LEFT JOIN dirty_scopes d ON d.dataset_id=$1 AND d.scope_key=$3||x.workout::text
 LEFT JOIN analysis_publications p ON p.dataset_id=d.dataset_id AND p.scope_key=d.scope_key AND p.metric_family='workout_model'
 WHERE w.workout_uuid IS NULL OR d.generation IS DISTINCT FROM x.generation OR d.clean_generation IS DISTINCT FROM x.generation
 OR p.published_run_id IS DISTINCT FROM x.run OR p.generation IS DISTINCT FROM x.generation)`, l.DatasetID, json.RawMessage(body), "model/"+wf.StackID()+"/workout/").Scan(&current)
	return current, err
}
func retention(r *pf.Result) *float64 {
	if r.Ability == nil {
		return nil
	}
	v := r.Ability.Retention
	return &v
}

// CandidatePerformance is administrative, never a product/home/training input.
// Freshness is evaluated at read time without creating a new performance target.
func (s *Store) CandidatePerformance(ctx context.Context, dataset string, at time.Time) (pf.Result, string, error) {
	row, err := s.q.CurrentCandidatePerformance(ctx, dbPerformanceParams(dataset))
	if err != nil {
		return pf.Result{}, "", err
	}
	var r pf.Result
	if json.Unmarshal(row.Payload, &r) != nil || pf.Hash(r) != row.ContentHash || r.ConfigHash != pf.ConfigHash() || at.UnixMicro() < r.AsOf {
		return r, "", analysisArtifactInvalid()
	}
	r = pf.Freshen(r, at.UnixMicro())
	var tz string
	if err = s.Pool.QueryRow(ctx, `SELECT timezone FROM datasets WHERE id=$1`, dataset).Scan(&tz); err != nil {
		return r, "", err
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return r, "", err
	}
	h := at.In(loc)
	previousDay := time.Date(h.Year(), h.Month(), h.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, -28)
	previous, e := s.q.ComparablePerformanceHistory(ctx, dbgen.ComparablePerformanceHistoryParams{DatasetID: dataset, ScopeKey: pf.Scope(), StartedAt: pgtype.Timestamptz{Time: previousDay, Valid: true}, StartedAt_2: pgtype.Timestamptz{Time: previousDay.AddDate(0, 0, 1), Valid: true}})
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return r, "", e
	}
	if e == nil {
		var p pf.Result
		if json.Unmarshal(previous.Payload, &p) != nil || pf.Hash(p) != previous.ContentHash {
			return r, "", analysisArtifactInvalid()
		}
		r.Trend = pf.ComparableTrend(r, p, loc)
	}
	return r, row.RunID, nil
}

func (s *Store) HistoricalPerformance(ctx context.Context, dataset, run string) (pf.Result, error) {
	if !syncing.ValidID(dataset) || !syncing.ValidID(run) {
		return pf.Result{}, analysis.Invalid()
	}
	row, err := s.q.HistoricalPerformance(ctx, dbgen.HistoricalPerformanceParams{DatasetID: dataset, ID: run})
	if err != nil {
		return pf.Result{}, err
	}
	var r pf.Result
	if json.Unmarshal(row.Payload, &r) != nil || pf.Hash(r) != row.ContentHash {
		return r, analysisArtifactInvalid()
	}
	return r, nil
}

func dbPerformanceParams(dataset string) dbgen.CurrentCandidatePerformanceParams {
	return dbgen.CurrentCandidatePerformanceParams{DatasetID: dataset, ScopeKey: pf.Scope(), StackID: wf.StackID(), Column4: wf.DefaultPolicy().Hash(), Column5: pf.ConfigHash()}
}
