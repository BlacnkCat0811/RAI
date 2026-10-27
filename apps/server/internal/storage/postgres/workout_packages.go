package postgres

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"rai/server/internal/syncing"
)

type workoutPackageRecord struct {
	WorkoutID           string
	DatasetEpoch        string
	CollectorEpoch      string
	EventStart          time.Time
	EventEnd            time.Time
	Core                []byte
	State               string
	CommittedChunks     int
	TotalDetails        int64
	CommittedActivities int
	CommittedStatistics int
	CommittedEvents     int
	LastStatisticType   string
	ManifestSHA256      []byte
	ChunkCount          *int
	Receipt             []byte
}

const workoutPackageColumns = `workout_uuid::text,dataset_epoch::text,collector_epoch::text,event_start,event_end,core,
	state,committed_chunks,total_details,committed_activities,committed_statistics,committed_events,last_statistic_type,
	manifest_sha256,chunk_count,receipt`

func scanWorkoutPackage(row pgx.Row) (workoutPackageRecord, error) {
	var r workoutPackageRecord
	var chunk pgtype.Int4
	err := row.Scan(&r.WorkoutID, &r.DatasetEpoch, &r.CollectorEpoch, &r.EventStart, &r.EventEnd, &r.Core,
		&r.State, &r.CommittedChunks, &r.TotalDetails, &r.CommittedActivities, &r.CommittedStatistics,
		&r.CommittedEvents, &r.LastStatisticType, &r.ManifestSHA256, &chunk, &r.Receipt)
	if chunk.Valid {
		n := int(chunk.Int32)
		r.ChunkCount = &n
	}
	return r, err
}

func (r workoutPackageRecord) state() syncing.WorkoutPackageState {
	return syncing.WorkoutPackageState{
		WorkoutID: r.WorkoutID, DatasetEpoch: r.DatasetEpoch, CollectorEpoch: r.CollectorEpoch,
		StartUs: r.EventStart.UnixMicro(), EndUs: r.EventEnd.UnixMicro(), IngestStatus: r.State,
		CommittedChunks: r.CommittedChunks, TotalDetails: r.TotalDetails,
		ManifestSHA256: hex.EncodeToString(r.ManifestSHA256), ChunkCount: r.ChunkCount,
		Receipt: json.RawMessage(r.Receipt),
	}
}

// The package tables arrive in schema 9. Normal sample uploads remain usable
// during an explicit upgrade window, so the older Workout path must not assume
// the table exists until readyz has approved the new schema.
func workoutPackageExists(ctx context.Context, tx pgx.Tx, dataset, id string) (bool, error) {
	var table pgtype.Text
	if err := tx.QueryRow(ctx, `SELECT to_regclass('workout_packages')::text`).Scan(&table); err != nil {
		return false, err
	}
	if !table.Valid {
		return false, nil
	}
	var exists bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workout_packages WHERE dataset_id=$1 AND workout_uuid=$2)`, dataset, id).Scan(&exists)
	return exists, err
}

func deleteWorkoutPackage(ctx context.Context, tx pgx.Tx, dataset, id string) error {
	var table pgtype.Text
	if err := tx.QueryRow(ctx, `SELECT to_regclass('workout_packages')::text`).Scan(&table); err != nil {
		return err
	}
	if !table.Valid {
		return nil
	}
	_, err := tx.Exec(ctx, `DELETE FROM workout_packages WHERE dataset_id=$1 AND workout_uuid=$2`, dataset, id)
	return err
}

func (s *Store) WorkoutPackage(ctx context.Context, p syncing.Principal, id string) (syncing.WorkoutPackageState, error) {
	r, err := scanWorkoutPackage(s.Pool.QueryRow(ctx, `SELECT `+workoutPackageColumns+` FROM workout_packages WHERE dataset_id=$1 AND workout_uuid=$2`, p.DatasetID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return syncing.WorkoutPackageState{}, syncing.Fail(404, "workout_package_not_found")
	}
	if err != nil {
		return syncing.WorkoutPackageState{}, err
	}
	return r.state(), nil
}

// RegisterWorkoutPackage reserves the Workout UUID and stores only the small
// core Event. It does not create a Workout fact and does not advance any stream.
func (s *Store) RegisterWorkoutPackage(ctx context.Context, p syncing.Principal, req syncing.WorkoutPackageRequest) (syncing.WorkoutPackageState, error) {
	if err := req.Validate(); err != nil {
		return syncing.WorkoutPackageState{}, err
	}
	core, _ := req.CoreCanonical()
	tx, err := s.begin(ctx)
	if err != nil {
		return syncing.WorkoutPackageState{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = lockDataset(ctx, tx, p, req.DatasetID, req.DatasetEpoch, req.CollectorEpoch); err != nil {
		return syncing.WorkoutPackageState{}, err
	}
	existing, err := scanWorkoutPackage(tx.QueryRow(ctx, `SELECT `+workoutPackageColumns+` FROM workout_packages WHERE dataset_id=$1 AND workout_uuid=$2 FOR UPDATE`, p.DatasetID, req.WorkoutID))
	if err == nil {
		if existing.DatasetEpoch != req.DatasetEpoch || existing.CollectorEpoch != req.CollectorEpoch || !bytes.Equal(existing.Core, core) {
			return syncing.WorkoutPackageState{}, syncing.Fail(409, "workout_package_identity_conflict")
		}
		if err = tx.Commit(ctx); err != nil {
			return syncing.WorkoutPackageState{}, err
		}
		return existing.state(), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return syncing.WorkoutPackageState{}, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2)`, p.DatasetID, req.WorkoutID).Scan(&exists); err != nil {
		return syncing.WorkoutPackageState{}, err
	}
	if exists {
		return syncing.WorkoutPackageState{}, syncing.Fail(409, "workout_package_identity_conflict")
	}
	var tombstone bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM entity_tombstones WHERE dataset_id=$1 AND sample_uuid=$2 AND sample_type=$3)`,
		p.DatasetID, req.WorkoutID, syncing.WorkoutType).Scan(&tombstone); err != nil {
		return syncing.WorkoutPackageState{}, err
	}
	if tombstone {
		return syncing.WorkoutPackageState{}, syncing.Fail(409, "workout_package_unavailable")
	}
	manifest := syncing.EmptyWorkoutPackageManifest()
	start := time.UnixMicro(*req.StartUs).UTC()
	end := time.UnixMicro(*req.EndUs).UTC()
	if _, err = tx.Exec(ctx, `INSERT INTO workout_packages(dataset_id,workout_uuid,dataset_epoch,collector_epoch,event_start,event_end,core,manifest_sha256)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, p.DatasetID, req.WorkoutID, req.DatasetEpoch, req.CollectorEpoch, start, end, core, manifest); err != nil {
		if uniqueViolation(err) {
			return syncing.WorkoutPackageState{}, syncing.Fail(409, "workout_package_identity_conflict")
		}
		return syncing.WorkoutPackageState{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return syncing.WorkoutPackageState{}, err
	}
	return syncing.WorkoutPackageState{
		WorkoutID: req.WorkoutID, DatasetEpoch: req.DatasetEpoch, CollectorEpoch: req.CollectorEpoch,
		StartUs: *req.StartUs, EndUs: *req.EndUs, IngestStatus: "pending",
		ManifestSHA256: hex.EncodeToString(manifest),
	}, nil
}

func decodePackageDigest(value string) ([]byte, error) {
	b, err := hex.DecodeString(value)
	if err != nil || len(b) != sha256Size {
		return nil, syncing.Fail(422, "unsupported_payload")
	}
	return b, nil
}

// CommitWorkoutPackageChunk stores one immutable staging chunk. It does not
// create a Workout fact, dirty scope, analysis job or business revision.
func (s *Store) CommitWorkoutPackageChunk(ctx context.Context, p syncing.Principal, chunk syncing.WorkoutDetailChunk, lease syncing.UploadLease) (json.RawMessage, error) {
	if !syncing.ValidID(chunk.WorkoutID) || chunk.ChunkIndex < 0 || chunk.ChunkIndex >= syncing.MaxWorkoutPackageChunks ||
		len(chunk.Entries) == 0 || len(chunk.Entries) > syncing.MaxWorkoutPackageDetailsPerChunk {
		return nil, syncing.Fail(422, "unsupported_payload")
	}
	wireHash, err := decodePackageDigest(chunk.WireSHA256)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	actualEpoch, _, err := lockDatasetRow(ctx, tx, p, p.DatasetID)
	if err != nil {
		return nil, err
	}
	rec, err := scanWorkoutPackage(tx.QueryRow(ctx, `SELECT `+workoutPackageColumns+` FROM workout_packages WHERE dataset_id=$1 AND workout_uuid=$2 FOR UPDATE`, p.DatasetID, chunk.WorkoutID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, syncing.Fail(404, "workout_package_not_found")
	}
	if err != nil {
		return nil, err
	}
	if rec.DatasetEpoch != actualEpoch {
		return nil, syncing.Fail(409, "dataset_epoch_changed")
	}
	if err = checkCollectorLease(ctx, tx, p, rec.CollectorEpoch); err != nil {
		return nil, err
	}
	var existingWire []byte
	var existingReceipt []byte
	err = tx.QueryRow(ctx, `SELECT wire_sha256,receipt FROM workout_package_chunks WHERE dataset_id=$1 AND workout_uuid=$2 AND chunk_index=$3`,
		p.DatasetID, chunk.WorkoutID, chunk.ChunkIndex).Scan(&existingWire, &existingReceipt)
	if err == nil {
		if bytes.Equal(existingWire, wireHash) {
			return existingReceipt, nil
		}
		return nil, syncing.Fail(409, "workout_package_chunk_conflict")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if rec.State == "sealed" {
		return nil, syncing.Fail(409, "workout_package_sealed")
	}
	if rec.CommittedChunks != chunk.ChunkIndex {
		expected := rec.CommittedChunks
		return nil, &syncing.Error{Status: 409, Code: "workout_package_chunk_gap", ExpectedChunkIndex: &expected}
	}
	activities, statistics, events := rec.CommittedActivities, rec.CommittedStatistics, rec.CommittedEvents
	lastType := rec.LastStatisticType
	details := chunk.Entries
	for i := range details {
		detail := details[i]
		switch detail.Kind {
		case "activity":
			if detail.Activity == nil || detail.Ordinal != activities || activities >= syncing.MaxWorkoutActivities {
				return nil, &syncing.Error{Status: 422, Code: "unsupported_payload", EventIndex: &i}
			}
			activities++
		case "statistic":
			if detail.Ordinal != statistics || statistics >= syncing.MaxWorkoutStatistics ||
				detail.Statistic == nil || detail.Statistic.Type <= lastType {
				return nil, &syncing.Error{Status: 422, Code: "unsupported_payload", EventIndex: &i}
			}
			lastType = detail.Statistic.Type
			statistics++
		case "event":
			if detail.Event == nil || detail.Ordinal != events || events >= syncing.MaxWorkoutEvents {
				return nil, &syncing.Error{Status: 422, Code: "unsupported_payload", EventIndex: &i}
			}
			events++
		default:
			return nil, &syncing.Error{Status: 422, Code: "unsupported_payload", EventIndex: &i}
		}
	}
	computedTotal := syncing.WorkoutDetailCount(details)
	if chunk.TotalDetails != 0 && chunk.TotalDetails != computedTotal {
		return nil, syncing.Fail(422, "unsupported_payload")
	}
	total := rec.TotalDetails + computedTotal
	if total > syncing.MaxWorkoutPackageDetails {
		return nil, syncing.Fail(413, "workout_package_too_large")
	}
	canonical, digest := syncing.WorkoutDetailsCanonical(details)
	if chunk.DetailsSHA256 != "" {
		passed, err := decodePackageDigest(chunk.DetailsSHA256)
		if err != nil || !bytes.Equal(passed, digest) {
			return nil, syncing.Fail(422, "unsupported_payload")
		}
	}
	manifest := syncing.AdvanceWorkoutPackageManifest(rec.ManifestSHA256, digest)
	committed := rec.CommittedChunks + 1
	body, err := json.Marshal(syncing.WorkoutPackageChunkReceipt{
		WorkoutID: chunk.WorkoutID, ChunkIndex: chunk.ChunkIndex, IngestStatus: "pending",
		DetailCount: len(details), PayloadSHA256: chunk.WireSHA256, DetailsSHA256: hex.EncodeToString(digest),
		ManifestSHA256: hex.EncodeToString(manifest), CommittedChunks: committed, TotalDetails: total,
	})
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO workout_package_chunks(dataset_id,workout_uuid,chunk_index,detail_count,wire_sha256,details_sha256,details,receipt)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, p.DatasetID, chunk.WorkoutID, chunk.ChunkIndex, len(details), wireHash, digest, canonical, body); err != nil {
		return nil, err
	}
	tag, err := tx.Exec(ctx, `UPDATE workout_packages SET committed_chunks=committed_chunks+1,total_details=total_details+$3,
		committed_activities=$4,committed_statistics=$5,committed_events=$6,last_statistic_type=$7,manifest_sha256=$8
		WHERE dataset_id=$1 AND workout_uuid=$2 AND state='pending'`, p.DatasetID, chunk.WorkoutID, computedTotal,
		activities, statistics, events, lastType, manifest)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() != 1 {
		return nil, syncing.Fail(409, "workout_package_sealed")
	}
	if err = checkUploadLease(ctx, tx, p, lease); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}

// SealWorkoutPackage is the only publication point. It verifies the bounded
// chunk index and manifest, then materializes exactly one Workout fact with its
// normalized child projections in the same transaction as revision, scope,
// job, change event and original receipt.
func (s *Store) SealWorkoutPackage(ctx context.Context, p syncing.Principal, id string, seal syncing.WorkoutPackageSeal) (json.RawMessage, error) {
	if err := seal.Validate(); err != nil {
		return nil, err
	}
	if !syncing.ValidID(id) {
		return nil, syncing.Fail(422, "unsupported_payload")
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	actualEpoch, rev, err := lockDatasetRow(ctx, tx, p, p.DatasetID)
	if err != nil {
		return nil, err
	}
	rec, err := scanWorkoutPackage(tx.QueryRow(ctx, `SELECT `+workoutPackageColumns+` FROM workout_packages WHERE dataset_id=$1 AND workout_uuid=$2 FOR UPDATE`, p.DatasetID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, syncing.Fail(404, "workout_package_not_found")
	}
	if err != nil {
		return nil, err
	}
	if rec.DatasetEpoch != actualEpoch {
		return nil, syncing.Fail(409, "dataset_epoch_changed")
	}
	if err = checkCollectorLease(ctx, tx, p, rec.CollectorEpoch); err != nil {
		return nil, err
	}
	storedManifest := hex.EncodeToString(rec.ManifestSHA256)
	if rec.State == "sealed" {
		if rec.ChunkCount != nil && *rec.ChunkCount == seal.ChunkCount && rec.TotalDetails == seal.TotalDetails && storedManifest == seal.ManifestSHA256 {
			return rec.Receipt, nil
		}
		return nil, syncing.Fail(409, "workout_package_identity_conflict")
	}
	if rec.CommittedChunks != seal.ChunkCount || rec.TotalDetails != seal.TotalDetails {
		return nil, syncing.Fail(409, "workout_package_incomplete")
	}
	if storedManifest != seal.ManifestSHA256 {
		return nil, syncing.Fail(409, "workout_package_manifest_mismatch")
	}
	var actualChunks int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM workout_package_chunks WHERE dataset_id=$1 AND workout_uuid=$2`,
		p.DatasetID, id).Scan(&actualChunks); err != nil {
		return nil, err
	}
	if actualChunks != seal.ChunkCount {
		return nil, syncing.Fail(409, "workout_package_incomplete")
	}
	rows, err := tx.Query(ctx, `SELECT chunk_index,details FROM workout_package_chunks WHERE dataset_id=$1 AND workout_uuid=$2 ORDER BY chunk_index`, p.DatasetID, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var details []syncing.WorkoutDetail
	expectedChunk := 0
	var total int64
	for rows.Next() {
		var index int
		var raw []byte
		if err = rows.Scan(&index, &raw); err != nil {
			return nil, err
		}
		if index != expectedChunk {
			return nil, syncing.Fail(409, "workout_package_incomplete")
		}
		expectedChunk++
		decoded, err := syncing.DecodeWorkoutDetails(raw)
		if err != nil {
			return nil, syncing.Fail(409, "workout_package_manifest_mismatch")
		}
		count := syncing.WorkoutDetailCount(decoded)
		if count <= 0 {
			return nil, syncing.Fail(409, "workout_package_incomplete")
		}
		total += count
		details = append(details, decoded...)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if expectedChunk != seal.ChunkCount || total != seal.TotalDetails || total != rec.TotalDetails {
		return nil, syncing.Fail(409, "workout_package_incomplete")
	}
	var core syncing.Event
	if err = syncing.DecodeJSON(rec.Core, &core); err != nil || core.Workout == nil {
		return nil, syncing.Fail(409, "workout_package_identity_conflict")
	}
	if err = core.Validate(syncing.WorkoutType); err != nil {
		return nil, syncing.Fail(409, "workout_package_identity_conflict")
	}
	full := *core.Workout
	full.Statistics = nil
	full.Events = nil
	full.Activities = nil
	activities, statistics, events := 0, 0, 0
	lastType := ""
	for _, detail := range details {
		switch detail.Kind {
		case "activity":
			if detail.Activity == nil || detail.Ordinal != activities || activities >= syncing.MaxWorkoutActivities {
				return nil, syncing.Fail(409, "workout_package_identity_conflict")
			}
			full.Activities = append(full.Activities, *detail.Activity)
			activities++
		case "statistic":
			if detail.Statistic == nil || detail.Ordinal != statistics || statistics >= syncing.MaxWorkoutStatistics || detail.Statistic.Type <= lastType {
				return nil, syncing.Fail(409, "workout_package_identity_conflict")
			}
			full.Statistics = append(full.Statistics, *detail.Statistic)
			lastType = detail.Statistic.Type
			statistics++
		case "event":
			if detail.Event == nil || detail.Ordinal != events || events >= syncing.MaxWorkoutEvents {
				return nil, syncing.Fail(409, "workout_package_identity_conflict")
			}
			full.Events = append(full.Events, *detail.Event)
			events++
		default:
			return nil, syncing.Fail(409, "workout_package_identity_conflict")
		}
	}
	if activities != rec.CommittedActivities || statistics != rec.CommittedStatistics || events != rec.CommittedEvents {
		return nil, syncing.Fail(409, "workout_package_incomplete")
	}
	core.Workout = &full
	if err = core.Validate(syncing.WorkoutType); err != nil {
		return nil, syncing.Fail(409, "workout_package_identity_conflict")
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2)`, p.DatasetID, id).Scan(&exists); err != nil {
		return nil, err
	}
	if exists {
		return nil, syncing.Fail(409, "workout_package_identity_conflict")
	}
	var tombstone bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM entity_tombstones WHERE dataset_id=$1 AND sample_uuid=$2 AND sample_type=$3)`,
		p.DatasetID, id, syncing.WorkoutType).Scan(&tombstone); err != nil {
		return nil, err
	}
	if tombstone {
		return nil, syncing.Fail(409, "workout_package_unavailable")
	}
	canonical, hash := core.Canonical()
	rev++
	if err = insertWorkout(ctx, tx, p.DatasetID, core, rev, canonical, hash); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE datasets SET commit_rev=$2 WHERE id=$1`, p.DatasetID, rev); err != nil {
		return nil, err
	}
	scope := fmt.Sprintf("workout:%s:%s", syncing.WorkoutType, id)
	var generation int64
	err = tx.QueryRow(ctx, `INSERT INTO dirty_scopes(dataset_id,scope_key,generation,dirty_from,dirty_through) VALUES($1,$2,1,$3,$4)
		ON CONFLICT(dataset_id,scope_key) DO UPDATE SET generation=dirty_scopes.generation+1,
		dirty_from=LEAST(dirty_scopes.dirty_from,EXCLUDED.dirty_from),dirty_through=GREATEST(dirty_scopes.dirty_through,EXCLUDED.dirty_through)
		RETURNING generation`, p.DatasetID, scope, rec.EventStart, rec.EventEnd).Scan(&generation)
	if err != nil {
		return nil, err
	}
	job := syncing.NewID()
	jobPayload, _ := json.Marshal(map[string]any{"inputRevision": rev})
	if _, err = tx.Exec(ctx, `INSERT INTO jobs(id,dataset_id,kind,scope_key,target_generation,payload) VALUES($1,$2,$3,$4,$5,$6)`,
		job, p.DatasetID, "analysis.workout", scope, generation, jobPayload); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO change_events(dataset_id,commit_rev,ordinal,event_id,entity_kind,entity_key,operation,payload)
		VALUES($1,$2,0,$3,'workout',$4,'upsert',$5)`, p.DatasetID, rev, syncing.NewID(), scope, canonical); err != nil {
		return nil, err
	}
	receipt := syncing.WorkoutPackageReceipt{
		WorkoutID: id, IngestStatus: "sealed", StartUs: rec.EventStart.UnixMicro(), EndUs: rec.EventEnd.UnixMicro(),
		ChunkCount: seal.ChunkCount, TotalDetails: seal.TotalDetails, ManifestSHA256: storedManifest,
		CommitRevision: rev, AnalysisStatus: "queued", AnalysisJobIDs: []string{job},
	}
	body, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE workout_packages SET state='sealed',chunk_count=$3,receipt=$4,sealed_at=now() WHERE dataset_id=$1 AND workout_uuid=$2`,
		p.DatasetID, id, seal.ChunkCount, body); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}
