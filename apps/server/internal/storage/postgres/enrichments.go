package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/syncing"
)

type enrichmentRecord struct {
	WorkoutID     string
	Source        string
	SourceVersion int64
	ObservedAtUs  int64
	ContentSHA256 []byte
	Receipt       []byte
}

func (s *Store) WorkoutEnrichment(ctx context.Context, p syncing.Principal, id string) (json.RawMessage, error) {
	var receipt []byte
	err := s.Pool.QueryRow(ctx, `SELECT receipt FROM workout_enrichments WHERE dataset_id=$1 AND enrichment_id=$2`,
		p.DatasetID, id).Scan(&receipt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, syncing.Fail(404, "workout_enrichment_not_found")
	}
	if err != nil {
		return nil, err
	}
	return json.RawMessage(receipt), nil
}

type workoutBase struct {
	Payload    []byte
	Start, End time.Time
	Distance   *float64
	Energy     *float64
	Metadata   map[string]any
}

func decodeWorkoutBase(payload []byte) (workoutBase, error) {
	var base workoutBase
	var doc struct {
		Metadata map[string]any `json:"metadata"`
		Workout  *struct {
			TotalDistance *syncing.Quantity `json:"totalDistance"`
			TotalEnergy   *syncing.Quantity `json:"totalEnergyBurned"`
		} `json:"workout"`
	}
	if err := json.Unmarshal(payload, &doc); err != nil {
		return base, err
	}
	base.Metadata = doc.Metadata
	if base.Metadata == nil {
		base.Metadata = map[string]any{}
	}
	if doc.Workout != nil {
		if doc.Workout.TotalDistance != nil {
			value := doc.Workout.TotalDistance.Value
			base.Distance = &value
		}
		if doc.Workout.TotalEnergy != nil {
			value := doc.Workout.TotalEnergy.Value
			base.Energy = &value
		}
	}
	return base, nil
}

type enrichmentEffective struct {
	Distance       *float64
	Energy         *float64
	Metadata       map[string]any
	RouteAvailable *bool
	ObservedAtUs   int64
	SawAny         bool
}

func (s *Store) effectiveEnrichment(ctx context.Context, tx pgx.Tx, dataset, workout string, base workoutBase) (enrichmentEffective, error) {
	effective := enrichmentEffective{
		Distance: base.Distance, Energy: base.Energy, Metadata: map[string]any{},
	}
	for key, value := range base.Metadata {
		effective.Metadata[key] = value
	}
	rows, err := tx.Query(ctx, `SELECT total_distance_m,total_energy_kcal,metadata,route_available,observed_at_us
		FROM workout_enrichments WHERE dataset_id=$1 AND workout_uuid=$2
		ORDER BY observed_at_us DESC,source_version DESC,created_at DESC`, dataset, workout)
	if err != nil {
		return effective, err
	}
	defer rows.Close()
	for rows.Next() {
		var distance, energy *float64
		var route *bool
		var observed int64
		var metadata []byte
		if err = rows.Scan(&distance, &energy, &metadata, &route, &observed); err != nil {
			return effective, err
		}
		if !effective.SawAny {
			effective.ObservedAtUs = observed
			effective.SawAny = true
		}
		if effective.Distance == nil && distance != nil {
			effective.Distance = distance
		}
		if effective.Energy == nil && energy != nil {
			effective.Energy = energy
		}
		if effective.RouteAvailable == nil && route != nil {
			effective.RouteAvailable = route
		}
		var object map[string]any
		if err = json.Unmarshal(metadata, &object); err != nil {
			return effective, err
		}
		for key, value := range object {
			if _, exists := effective.Metadata[key]; !exists {
				effective.Metadata[key] = value
			}
		}
	}
	return effective, rows.Err()
}

func sameJSONValue(a, b any) bool {
	left, err1 := json.Marshal(a)
	right, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(left) == string(right)
}

// EnrichWorkout appends an immutable supplement fact. It never updates the
// Workout payload and rejects stale observations instead of overwriting newer
// source data. The dataset lock serializes all revision and generation writes.
func (s *Store) EnrichWorkout(ctx context.Context, p syncing.Principal, req syncing.WorkoutEnrichmentRequest) (json.RawMessage, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	_, digest := req.Canonical()
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rev, err := lockDataset(ctx, tx, p, req.DatasetID, req.DatasetEpoch, req.CollectorEpoch)
	if err != nil {
		return nil, err
	}
	var existing enrichmentRecord
	err = tx.QueryRow(ctx, `SELECT workout_uuid::text,source_bundle_id,source_version,observed_at_us,content_sha256,receipt
		FROM workout_enrichments WHERE dataset_id=$1 AND enrichment_id=$2`, p.DatasetID, req.EnrichmentID).
		Scan(&existing.WorkoutID, &existing.Source, &existing.SourceVersion, &existing.ObservedAtUs, &existing.ContentSHA256, &existing.Receipt)
	if err == nil {
		if existing.WorkoutID == req.WorkoutID && string(existing.ContentSHA256) == string(digest) {
			return existing.Receipt, nil
		}
		return nil, syncing.Fail(409, "workout_enrichment_conflict")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var payload []byte
	var workoutStart, workoutEnd time.Time
	err = tx.QueryRow(ctx, `SELECT payload,event_start,event_end FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2`,
		p.DatasetID, req.WorkoutID).Scan(&payload, &workoutStart, &workoutEnd)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, syncing.Fail(409, "workout_unavailable")
	}
	if err != nil {
		return nil, err
	}
	var tombstone bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM entity_tombstones WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3)`,
		p.DatasetID, syncing.WorkoutType, req.WorkoutID).Scan(&tombstone); err != nil {
		return nil, err
	}
	if tombstone {
		return nil, syncing.Fail(409, "workout_unavailable")
	}
	base, err := decodeWorkoutBase(payload)
	if err != nil {
		return nil, err
	}
	base.Start, base.End = workoutStart, workoutEnd
	effective, err := s.effectiveEnrichment(ctx, tx, p.DatasetID, req.WorkoutID, base)
	if err != nil {
		return nil, err
	}
	if effective.SawAny && req.ObservedAtUs < effective.ObservedAtUs {
		return nil, syncing.Fail(409, "workout_enrichment_stale")
	}
	var maxSourceVersion int64
	if err = tx.QueryRow(ctx, `SELECT COALESCE(max(source_version),0) FROM workout_enrichments
		WHERE dataset_id=$1 AND workout_uuid=$2 AND source_bundle_id=$3`,
		p.DatasetID, req.WorkoutID, req.Source.BundleID).Scan(&maxSourceVersion); err != nil {
		return nil, err
	}
	if req.SourceVersion <= maxSourceVersion {
		return nil, syncing.Fail(409, "workout_enrichment_stale")
	}
	if req.TotalDistanceM != nil && effective.Distance != nil && *req.TotalDistanceM != *effective.Distance {
		return nil, syncing.Fail(409, "workout_enrichment_conflict")
	}
	if req.TotalEnergyKcal != nil && effective.Energy != nil && *req.TotalEnergyKcal != *effective.Energy {
		return nil, syncing.Fail(409, "workout_enrichment_conflict")
	}
	if req.RouteAvailable != nil && effective.RouteAvailable != nil && *req.RouteAvailable != *effective.RouteAvailable {
		return nil, syncing.Fail(409, "workout_enrichment_conflict")
	}
	for key, value := range req.Metadata {
		if current, exists := effective.Metadata[key]; exists && !sameJSONValue(current, value) {
			return nil, syncing.Fail(409, "workout_enrichment_conflict")
		}
	}
	nextRev := rev + 1
	job := syncing.NewID()
	receipt := syncing.WorkoutEnrichmentReceipt{
		EnrichmentID: req.EnrichmentID, WorkoutID: req.WorkoutID, IngestStatus: "committed",
		SourceVersion: req.SourceVersion, ObservedAtUs: req.ObservedAtUs, CommitRevision: nextRev,
		AnalysisStatus: "queued", AnalysisJobIDs: []string{job},
	}
	body, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	metadata := req.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	metaJSON, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO workout_enrichments(
		dataset_id,enrichment_id,workout_uuid,source_bundle_id,source_version,observed_at_us,
		total_distance_m,total_energy_kcal,metadata,route_available,content_sha256,receipt,commit_rev)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		p.DatasetID, req.EnrichmentID, req.WorkoutID, req.Source.BundleID, req.SourceVersion, req.ObservedAtUs,
		req.TotalDistanceM, req.TotalEnergyKcal, metaJSON, req.RouteAvailable, digest, body, nextRev)
	if uniqueViolation(err) {
		return nil, syncing.Fail(409, "workout_enrichment_conflict")
	}
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE datasets SET commit_rev=$2 WHERE id=$1`, p.DatasetID, nextRev); err != nil {
		return nil, err
	}
	scope := "workout:" + syncing.WorkoutType + ":" + req.WorkoutID
	var generation int64
	err = tx.QueryRow(ctx, `INSERT INTO dirty_scopes(dataset_id,scope_key,generation,dirty_from,dirty_through)
		VALUES($1,$2,1,$3,$4)
		ON CONFLICT(dataset_id,scope_key) DO UPDATE SET generation=dirty_scopes.generation+1,
			dirty_from=LEAST(dirty_scopes.dirty_from,EXCLUDED.dirty_from),
			dirty_through=GREATEST(dirty_scopes.dirty_through,EXCLUDED.dirty_through)
		RETURNING generation`, p.DatasetID, scope, workoutStart, workoutEnd).Scan(&generation)
	if err != nil {
		return nil, err
	}
	payloadJob, _ := json.Marshal(map[string]any{"inputRevision": nextRev, "enrichmentID": req.EnrichmentID})
	if _, err = tx.Exec(ctx, `INSERT INTO jobs(id,dataset_id,kind,scope_key,target_generation,payload)
		VALUES($1,$2,'analysis.workout',$3,$4,$5)`, job, p.DatasetID, scope, generation, payloadJob); err != nil {
		return nil, err
	}
	changePayload, _ := json.Marshal(map[string]any{"enrichmentID": req.EnrichmentID, "sourceVersion": req.SourceVersion, "observedAtUs": req.ObservedAtUs})
	if _, err = tx.Exec(ctx, `INSERT INTO change_events(dataset_id,commit_rev,ordinal,event_id,entity_kind,entity_key,operation,payload)
		VALUES($1,$2,0,$3,'workout',$4,'enrich',$5)`, p.DatasetID, nextRev, syncing.NewID(),
		fmt.Sprintf("%s:%s", syncing.WorkoutType, req.WorkoutID), changePayload); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}
