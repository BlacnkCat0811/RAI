package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/analysis"
	"rai/server/internal/syncing"
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
