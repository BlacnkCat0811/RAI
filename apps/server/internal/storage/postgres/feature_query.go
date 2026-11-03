package postgres

import (
	"context"
	"encoding/json"

	"rai/server/internal/storage/postgres/dbgen"
	"rai/server/internal/syncing"
	wf "rai/server/internal/workoutfeatures"
)

// CandidateFeature is an administrative projection. Product/API code never
// calls it. Freshness is checked in one SQL statement, including deletion state.
func (s *Store) CandidateFeature(ctx context.Context, dataset, workout string) (wf.WorkoutFeatures, string, error) {
	if !syncing.ValidID(dataset) || !syncing.ValidID(workout) {
		return wf.WorkoutFeatures{}, "", syncing.Fail(422, "unsupported_payload")
	}
	row, err := dbgen.New(s.Pool).CurrentCandidateFeature(ctx, dbgen.CurrentCandidateFeatureParams{DatasetID: dataset, ScopeKey: wf.Scope(workout), StackID: wf.StackID(), Column4: wf.DefaultPolicy().Hash(), WorkoutUuid: workout})
	if err != nil {
		return wf.WorkoutFeatures{}, "", err
	}
	var f wf.WorkoutFeatures
	if json.Unmarshal(row.Payload, &f) != nil {
		return f, "", syncing.Fail(500, "feature_artifact_invalid")
	}
	_, hash, err := f.MarshalArtifact()
	if err != nil || hash != row.ContentHash {
		return f, "", syncing.Fail(500, "feature_artifact_invalid")
	}
	return f, row.RunID, nil
}
