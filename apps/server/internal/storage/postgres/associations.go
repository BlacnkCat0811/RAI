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

type associationRecord struct {
	AssociationID string
	SampleType    string
	SampleUUID    string
	WorkoutUUID   string
	Source        string
	Receipt       []byte
}

func (s *Store) associationByID(ctx context.Context, tx pgx.Tx, dataset, id string) (associationRecord, error) {
	var r associationRecord
	err := tx.QueryRow(ctx, `SELECT COALESCE(association_id::text,''),sample_type,sample_uuid::text,workout_uuid::text,
		COALESCE(source_bundle_id,''),association_receipt
		FROM workout_samples WHERE dataset_id=$1 AND association_id=$2`, dataset, id).
		Scan(&r.AssociationID, &r.SampleType, &r.SampleUUID, &r.WorkoutUUID, &r.Source, &r.Receipt)
	return r, err
}

// AssociateSample is the sole P3 relation write path. It locks the dataset,
// validates both parents and tombstones, then writes the relation, revision,
// Workout dirty generation and analysis job atomically.
func (s *Store) AssociateSample(ctx context.Context, p syncing.Principal, req syncing.AssociationRequest) (json.RawMessage, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rev, err := lockDataset(ctx, tx, p, req.DatasetID, req.DatasetEpoch, req.CollectorEpoch)
	if err != nil {
		return nil, err
	}
	byID, err := s.associationByID(ctx, tx, p.DatasetID, req.AssociationID)
	if err == nil {
		if byID.SampleType == req.SampleType && byID.SampleUUID == req.SampleUUID && byID.WorkoutUUID == req.WorkoutUUID {
			return byID.Receipt, nil
		}
		return nil, syncing.Fail(409, "sample_association_conflict")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var sampleExists, sampleTombstone bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM health_samples WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3),
		EXISTS(SELECT 1 FROM entity_tombstones WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3)`,
		p.DatasetID, req.SampleType, req.SampleUUID).Scan(&sampleExists, &sampleTombstone); err != nil {
		return nil, err
	}
	var workoutExists, workoutTombstone bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2),
		EXISTS(SELECT 1 FROM entity_tombstones WHERE dataset_id=$1 AND sample_type=$3 AND sample_uuid=$2)`,
		p.DatasetID, req.WorkoutUUID, syncing.WorkoutType).Scan(&workoutExists, &workoutTombstone); err != nil {
		return nil, err
	}
	if !sampleExists || !workoutExists || sampleTombstone || workoutTombstone {
		return nil, syncing.Fail(409, "association_unavailable")
	}
	var currentWorkout string
	var currentReceipt []byte
	err = tx.QueryRow(ctx, `SELECT workout_uuid::text,association_receipt FROM workout_samples
		WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3`, p.DatasetID, req.SampleType, req.SampleUUID).
		Scan(&currentWorkout, &currentReceipt)
	if err == nil {
		if currentWorkout != req.WorkoutUUID {
			return nil, syncing.Fail(409, "sample_association_conflict")
		}
		if len(currentReceipt) > 0 {
			return currentReceipt, nil
		}
		return nil, syncing.Fail(409, "association_unavailable")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var start, end time.Time
	if err = tx.QueryRow(ctx, `SELECT event_start,event_end FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2`,
		p.DatasetID, req.WorkoutUUID).Scan(&start, &end); err != nil {
		return nil, err
	}
	nextRev := rev + 1
	job := syncing.NewID()
	receipt := syncing.AssociationReceipt{
		AssociationID: req.AssociationID, SampleType: req.SampleType, SampleUUID: req.SampleUUID,
		WorkoutUUID: req.WorkoutUUID, IngestStatus: "committed", CommitRevision: nextRev,
		AnalysisStatus: "queued", AnalysisJobIDs: []string{job},
	}
	body, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO workout_samples(dataset_id,sample_type,sample_uuid,workout_uuid,association_id,source_bundle_id,association_receipt,commit_rev)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, p.DatasetID, req.SampleType, req.SampleUUID, req.WorkoutUUID,
		req.AssociationID, req.Source.BundleID, body, nextRev)
	if uniqueViolation(err) {
		return nil, syncing.Fail(409, "sample_association_conflict")
	}
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE datasets SET commit_rev=$2 WHERE id=$1`, p.DatasetID, nextRev); err != nil {
		return nil, err
	}
	scope := "workout:" + syncing.WorkoutType + ":" + req.WorkoutUUID
	var generation int64
	err = tx.QueryRow(ctx, `INSERT INTO dirty_scopes(dataset_id,scope_key,generation,dirty_from,dirty_through)
		VALUES($1,$2,1,$3,$4)
		ON CONFLICT(dataset_id,scope_key) DO UPDATE SET generation=dirty_scopes.generation+1,
			dirty_from=LEAST(dirty_scopes.dirty_from,EXCLUDED.dirty_from),
			dirty_through=GREATEST(dirty_scopes.dirty_through,EXCLUDED.dirty_through)
		RETURNING generation`, p.DatasetID, scope, start, end).Scan(&generation)
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(map[string]any{"inputRevision": nextRev, "associationID": req.AssociationID})
	if _, err = tx.Exec(ctx, `INSERT INTO jobs(id,dataset_id,kind,scope_key,target_generation,payload)
		VALUES($1,$2,'analysis.workout',$3,$4,$5)`, job, p.DatasetID, scope, generation, payload); err != nil {
		return nil, err
	}
	changePayload, _ := json.Marshal(map[string]any{"sampleType": req.SampleType, "sampleUUID": req.SampleUUID, "workoutUUID": req.WorkoutUUID})
	if _, err = tx.Exec(ctx, `INSERT INTO change_events(dataset_id,commit_rev,ordinal,event_id,entity_kind,entity_key,operation,payload)
		VALUES($1,$2,0,$3,'association',$4,'upsert',$5)`, p.DatasetID, nextRev, syncing.NewID(),
		fmt.Sprintf("%s:%s", req.SampleType, req.SampleUUID), changePayload); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}
