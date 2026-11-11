package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"time"

	"rai/server/internal/analysis"
	"rai/server/internal/modelinput"
	"rai/server/internal/syncing"

	"github.com/jackc/pgx/v5"
)

// LatestAnalysisParameters returns the newest explicit TRIMP parameter set.
// No row means the athlete has not saved personal values; callers must not
// substitute defaults.
func (s *Store) LatestAnalysisParameters(ctx context.Context, p syncing.Principal) (*analysis.Parameters, error) {
	if !syncing.ValidID(p.DatasetID) {
		return nil, syncing.Fail(409, "dataset_unavailable")
	}
	var out analysis.Parameters
	err := s.Pool.QueryRow(ctx, `SELECT version,resting_hr,max_hr,coefficient_a,coefficient_b,provenance
	 FROM analysis_parameter_sets WHERE dataset_id=$1 ORDER BY version DESC LIMIT 1`, p.DatasetID).
		Scan(&out.Version, &out.RestHR, &out.MaxHR, &out.A, &out.B, &out.Provenance)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// hrReferenceAt is the latest-effective hr_reference/2 entry effective at at,
// the same choice the model stack makes (hrref.pick): newest effective date,
// then newest entry. Revoked entries are excluded.
func hrReferenceAt(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, dataset string, at time.Time) (*modelinput.HRReference, error) {
	var out modelinput.HRReference
	var payload []byte
	var from time.Time
	err := q.QueryRow(ctx, `SELECT i.id::text,i.effective_from,i.payload FROM athlete_model_inputs i
 WHERE i.dataset_id=$1 AND i.kind='hr_reference' AND i.input_schema='rai.hr_reference/2' AND i.revokes_id IS NULL
 AND i.effective_from<=$2 AND (i.effective_through IS NULL OR i.effective_through>$2)
 AND NOT EXISTS(SELECT 1 FROM athlete_model_inputs r WHERE r.dataset_id=i.dataset_id AND r.revokes_id=i.id)
 ORDER BY i.effective_from DESC,i.created_at DESC,i.id LIMIT 1`, dataset, at).Scan(&out.ID, &from, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var x struct {
		Max    float64  `json:"maxHR"`
		Rest   *float64 `json:"restingHR"`
		At     string   `json:"measuredAt"`
		Method string   `json:"method"`
	}
	if json.Unmarshal(payload, &x) != nil || x.Max <= 0 {
		return nil, nil
	}
	out.MaxHR, out.RestingHR, out.MeasuredAt, out.Method = x.Max, x.Rest, x.At, x.Method
	out.EffectiveFrom = from.UTC().Format(time.RFC3339)
	return &out, nil
}

// CurrentHRReference returns the HR reference effective now, or nil.
func (s *Store) CurrentHRReference(ctx context.Context, p syncing.Principal) (*modelinput.HRReference, error) {
	if !syncing.ValidID(p.DatasetID) {
		return nil, syncing.Fail(409, "dataset_unavailable")
	}
	return hrReferenceAt(ctx, s.Pool, p.DatasetID, time.Now())
}

// ObservedMaxHR is the highest 30 s median heart rate of any running Workout
// in the last 365 days, from the cached per-Workout features. It is only a
// suggestion for the user to confirm; the models never use it directly.
func (s *Store) ObservedMaxHR(ctx context.Context, p syncing.Principal) (*modelinput.ObservedMaxHR, error) {
	if !syncing.ValidID(p.DatasetID) {
		return nil, syncing.Fail(409, "dataset_unavailable")
	}
	var v *float64
	var at *int64
	err := s.Pool.QueryRow(ctx, `SELECT (r.feature->'heartRate'->>'robustMax30sMedian')::float8,(r.feature->'timing'->>'startUs')::bigint
 FROM model_workout_results r WHERE r.dataset_id=$1 AND r.feature->'heartRate'->>'robustMax30sMedian' IS NOT NULL
 AND (r.feature->'timing'->>'startUs')::bigint>$2 ORDER BY 1 DESC LIMIT 1`, p.DatasetID, time.Now().AddDate(-1, 0, 0).UnixMicro()).Scan(&v, &at)
	if errors.Is(err, pgx.ErrNoRows) || v == nil || at == nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &modelinput.ObservedMaxHR{Value: math.Round(*v), Date: time.UnixMicro(*at).UTC().Format(time.DateOnly)}, nil
}
