package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgtype"
	"rai/server/internal/storage/postgres/dbgen"
	tl "rai/server/internal/trainingload"
	wf "rai/server/internal/workoutfeatures"
	"time"
)

// Administrative candidate-only projections, never consumed by product/training.
func (s *Store) CandidateLoad(ctx context.Context, dataset, workout, date string) (tl.Result, string, error) {
	var out tl.Result
	row, err := s.candidateLoadArtifact(ctx, dataset, tl.Scope(workout), date, tl.ArtifactKey)
	if err != nil {
		return out, "", err
	}
	if json.Unmarshal(row.Payload, &out) != nil || tl.Hash(out) != row.ContentHash || out.ConfigHash != tl.ConfigHash() {
		return out, "", analysisArtifactInvalid()
	}
	return out, row.RunID, nil
}
func analysisArtifactInvalid() error { return errors.New("candidate artifact invalid") }
func (s *Store) CandidateLoadState(ctx context.Context, dataset, date string) (tl.DailyState, string, error) {
	var out tl.StateResult
	row, err := s.candidateLoadArtifact(ctx, dataset, tl.StateScope(), date, tl.StateKey)
	if err != nil {
		return tl.DailyState{}, "", err
	}
	if json.Unmarshal(row.Payload, &out) != nil || tl.Hash(out) != row.ContentHash || out.ConfigHash != tl.ConfigHash() {
		return tl.DailyState{}, "", analysisArtifactInvalid()
	}
	for _, d := range out.Days {
		if d.Day == date {
			return d, row.RunID, nil
		}
	}
	return tl.DailyState{}, "", analysisArtifactInvalid()
}
func (s *Store) candidateLoadArtifact(ctx context.Context, dataset, scope, date, key string) (dbgen.CurrentCandidateLoadArtifactRow, error) {
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		return dbgen.CurrentCandidateLoadArtifactRow{}, err
	}
	return dbgen.New(s.Pool).CurrentCandidateLoadArtifact(ctx, dbgen.CurrentCandidateLoadArtifactParams{DatasetID: dataset, ScopeKey: scope, Column3: pgtype.Date{Time: d, Valid: true}, ArtifactKey: key, StackID: wf.StackID(), Column6: wf.DefaultPolicy().Hash()})
}
