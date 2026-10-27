package postgres

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"rai/server/internal/syncing"
)

const routeType = syncing.RouteType

type routeRecord struct {
	RouteID                       string
	WorkoutID                     string
	Source                        string
	State                         string
	DatasetEpoch                  string
	CollectorEpoch                string
	ExpectedChunkCount            int
	ExpectedPointCount            int64
	CommittedChunks               int
	CommittedPoints               int64
	ManifestSHA256                []byte
	PayloadManifestSHA256         []byte
	ExpectedPayloadManifestSHA256 []byte
	FirstRecordedAt               pgtype.Timestamptz
	LastRecordedAt                pgtype.Timestamptz
	ChunkCount                    pgtype.Int4
	TotalPoints                   pgtype.Int8
	SealedRev                     pgtype.Int8
	Receipt                       []byte
	WorkoutStart                  time.Time
	WorkoutEnd                    time.Time
}

const routeColumns = `r.route_uuid::text,r.workout_uuid::text,r.source_bundle_id,r.state,
	COALESCE(r.dataset_epoch::text,''),COALESCE(r.collector_epoch::text,''),
	r.expected_chunk_count,r.expected_point_count,r.committed_chunks,r.committed_points,
	r.manifest_sha256,r.payload_manifest_sha256,r.expected_payload_manifest_sha256,
	r.first_recorded_at,r.last_recorded_at,r.chunk_count,r.total_points,r.sealed_rev,r.receipt,
	w.event_start,w.event_end`

func scanRoute(row pgx.Row) (routeRecord, error) {
	var r routeRecord
	err := row.Scan(&r.RouteID, &r.WorkoutID, &r.Source, &r.State, &r.DatasetEpoch, &r.CollectorEpoch,
		&r.ExpectedChunkCount, &r.ExpectedPointCount, &r.CommittedChunks, &r.CommittedPoints,
		&r.ManifestSHA256, &r.PayloadManifestSHA256, &r.ExpectedPayloadManifestSHA256,
		&r.FirstRecordedAt, &r.LastRecordedAt, &r.ChunkCount, &r.TotalPoints, &r.SealedRev,
		&r.Receipt, &r.WorkoutStart, &r.WorkoutEnd)
	return r, err
}

func routeState(r routeRecord) syncing.RouteState {
	state := syncing.RouteState{
		RouteID: r.RouteID, WorkoutID: r.WorkoutID, Source: syncing.Source{BundleID: r.Source},
		DatasetEpoch: r.DatasetEpoch, CollectorEpoch: r.CollectorEpoch, IngestStatus: r.State,
		ExpectedChunkCount: r.ExpectedChunkCount, ExpectedPointCount: r.ExpectedPointCount,
		CommittedChunks: r.CommittedChunks, CommittedPoints: r.CommittedPoints,
		ManifestSHA256:        hex.EncodeToString(r.ManifestSHA256),
		PayloadManifestSHA256: hex.EncodeToString(r.PayloadManifestSHA256),
	}
	if r.FirstRecordedAt.Valid {
		state.FirstRecordedAtUs = r.FirstRecordedAt.Time.UnixMicro()
	}
	if r.LastRecordedAt.Valid {
		state.LastRecordedAtUs = r.LastRecordedAt.Time.UnixMicro()
	}
	if len(r.ExpectedPayloadManifestSHA256) > 0 {
		state.ExpectedPayloadManifestSHA256 = hex.EncodeToString(r.ExpectedPayloadManifestSHA256)
	}
	if r.ChunkCount.Valid {
		n := int(r.ChunkCount.Int32)
		state.ChunkCount = &n
	}
	if r.TotalPoints.Valid {
		n := r.TotalPoints.Int64
		state.TotalPoints = &n
	}
	if r.SealedRev.Valid {
		n := r.SealedRev.Int64
		state.CommitRevision = &n
	}
	if len(r.Receipt) > 0 {
		state.Receipt = json.RawMessage(r.Receipt)
	}
	return state
}

func (s *Store) routeRecord(ctx context.Context, dataset, route string) (routeRecord, error) {
	return scanRoute(s.Pool.QueryRow(ctx, `SELECT `+routeColumns+` FROM workout_routes r
		JOIN workouts w ON w.dataset_id=r.dataset_id AND w.workout_uuid=r.workout_uuid
		WHERE r.dataset_id=$1 AND r.route_uuid=$2`, dataset, route))
}

func (s *Store) Route(ctx context.Context, p syncing.Principal, id string) (syncing.RouteState, error) {
	r, err := s.routeRecord(ctx, p.DatasetID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return syncing.RouteState{}, syncing.Fail(404, "route_not_found")
	}
	if err != nil {
		return syncing.RouteState{}, err
	}
	return routeState(r), nil
}

func routeManifestMatches(r routeRecord, req syncing.RouteManifest) bool {
	var expected []byte
	if req.ExpectedPayloadManifestSHA256 != "" {
		expected, _ = hex.DecodeString(req.ExpectedPayloadManifestSHA256)
	}
	return r.WorkoutID == req.WorkoutID && r.Source == req.Source.BundleID &&
		r.DatasetEpoch == req.DatasetEpoch && r.CollectorEpoch == req.CollectorEpoch &&
		r.ExpectedChunkCount == req.ExpectedChunkCount && r.ExpectedPointCount == req.ExpectedPointCount &&
		r.FirstRecordedAt.Valid && r.FirstRecordedAt.Time.UnixMicro() == req.FirstRecordedAtUs &&
		r.LastRecordedAt.Valid && r.LastRecordedAt.Time.UnixMicro() == req.LastRecordedAtUs &&
		bytes.Equal(r.ExpectedPayloadManifestSHA256, expected)
}

// RegisterRoute records the fixed route manifest, never points. Counts and
// first/last timestamps are later enforced again by the chunk and seal functions.
func (s *Store) RegisterRoute(ctx context.Context, p syncing.Principal, req syncing.RouteManifest) (syncing.RouteState, error) {
	if err := req.Validate(); err != nil {
		return syncing.RouteState{}, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return syncing.RouteState{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = lockDataset(ctx, tx, p, req.DatasetID, req.DatasetEpoch, req.CollectorEpoch); err != nil {
		return syncing.RouteState{}, err
	}
	var tombstone bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM entity_tombstones WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3)`,
		p.DatasetID, routeType, req.RouteID).Scan(&tombstone); err != nil {
		return syncing.RouteState{}, err
	}
	if tombstone {
		return syncing.RouteState{}, syncing.Fail(409, "route_unavailable")
	}
	existing, err := scanRoute(tx.QueryRow(ctx, `SELECT `+routeColumns+` FROM workout_routes r
		JOIN workouts w ON w.dataset_id=r.dataset_id AND w.workout_uuid=r.workout_uuid
		WHERE r.dataset_id=$1 AND r.route_uuid=$2`, p.DatasetID, req.RouteID))
	if err == nil {
		if !routeManifestMatches(existing, req) {
			return syncing.RouteState{}, syncing.Fail(409, "route_identity_conflict")
		}
		if err = tx.Commit(ctx); err != nil {
			return syncing.RouteState{}, err
		}
		return routeState(existing), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return syncing.RouteState{}, err
	}
	var workoutStart, workoutEnd time.Time
	err = tx.QueryRow(ctx, `SELECT event_start,event_end FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2`,
		p.DatasetID, req.WorkoutID).Scan(&workoutStart, &workoutEnd)
	if errors.Is(err, pgx.ErrNoRows) {
		return syncing.RouteState{}, syncing.Fail(409, "route_unavailable")
	}
	if err != nil {
		return syncing.RouteState{}, err
	}
	first, last := time.UnixMicro(req.FirstRecordedAtUs).UTC(), time.UnixMicro(req.LastRecordedAtUs).UTC()
	if first.Before(workoutStart) || last.After(workoutEnd) {
		return syncing.RouteState{}, syncing.Fail(422, "unsupported_payload")
	}
	var expectedPayload []byte
	if req.ExpectedPayloadManifestSHA256 != "" {
		expectedPayload, _ = hex.DecodeString(req.ExpectedPayloadManifestSHA256)
	}
	_, err = tx.Exec(ctx, `INSERT INTO workout_routes(
		dataset_id,route_uuid,workout_uuid,source_bundle_id,dataset_epoch,collector_epoch,
		expected_chunk_count,expected_point_count,first_recorded_at,last_recorded_at,expected_payload_manifest_sha256)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		p.DatasetID, req.RouteID, req.WorkoutID, req.Source.BundleID, req.DatasetEpoch, req.CollectorEpoch,
		req.ExpectedChunkCount, req.ExpectedPointCount, first, last, expectedPayload)
	if uniqueViolation(err) {
		return syncing.RouteState{}, syncing.Fail(409, "route_identity_conflict")
	}
	if err != nil {
		return syncing.RouteState{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return syncing.RouteState{}, err
	}
	return s.Route(ctx, p, req.RouteID)
}

func decodeRouteDigest(value string) ([]byte, error) {
	b, err := hex.DecodeString(value)
	if err != nil || len(b) != 32 {
		return nil, syncing.Fail(422, "unsupported_payload")
	}
	return b, nil
}

func routeDBError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.Code {
	case "RA001":
		return syncing.Fail(404, "route_not_found")
	case "RA011":
		return syncing.Fail(409, "route_chunk_gap")
	case "RA012":
		return syncing.Fail(413, "route_chunk_too_large")
	case "RA013":
		return syncing.Fail(409, "route_chunk_conflict")
	case "RA014", "RA016", "RA017":
		return syncing.Fail(422, "unsupported_payload")
	case "RA015":
		return syncing.Fail(413, "route_too_large")
	case "RA021":
		return syncing.Fail(409, "route_incomplete")
	case "RA022":
		return syncing.Fail(409, "route_manifest_mismatch")
	case "RA023":
		return syncing.Fail(409, "route_already_sealed")
	default:
		return err
	}
}

// CommitRouteChunk is the only point-write path. The database function locks the
// route, validates counts and sequencing, inserts the immutable chunk and lets a
// trigger build route_points from the same canonical JSON.
func (s *Store) CommitRouteChunk(ctx context.Context, p syncing.Principal, chunk syncing.RouteChunk, lease syncing.UploadLease) (json.RawMessage, error) {
	if !syncing.ValidID(chunk.RouteID) || chunk.ChunkIndex < 0 || chunk.ChunkIndex >= syncing.MaxRouteChunks ||
		len(chunk.Points) < 1 || len(chunk.Points) > syncing.MaxRoutePointsPerChunk {
		return nil, syncing.Fail(422, "unsupported_payload")
	}
	wireHash, err := decodeRouteDigest(chunk.WireSHA256)
	if err != nil {
		return nil, err
	}
	pointsHash, err := decodeRouteDigest(chunk.PointsSHA256)
	if err != nil {
		return nil, err
	}
	canonical, digest := syncing.RoutePointsCanonical(chunk.Points)
	if !bytes.Equal(pointsHash, digest) {
		return nil, syncing.Fail(422, "payload_hash_mismatch")
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
	rec, err := scanRoute(tx.QueryRow(ctx, `SELECT `+routeColumns+` FROM workout_routes r
		JOIN workouts w ON w.dataset_id=r.dataset_id AND w.workout_uuid=r.workout_uuid
		WHERE r.dataset_id=$1 AND r.route_uuid=$2`, p.DatasetID, chunk.RouteID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, syncing.Fail(404, "route_not_found")
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
	if rec.State == "sealed" {
		var oldWire, oldReceipt []byte
		err = tx.QueryRow(ctx, `SELECT wire_sha256,receipt FROM route_chunks
			WHERE dataset_id=$1 AND route_uuid=$2 AND chunk_index=$3`,
			p.DatasetID, chunk.RouteID, chunk.ChunkIndex).Scan(&oldWire, &oldReceipt)
		if err == nil && bytes.Equal(oldWire, wireHash) {
			return oldReceipt, nil
		}
		return nil, syncing.Fail(409, "route_already_sealed")
	}
	if chunk.ChunkIndex > rec.CommittedChunks {
		expected := rec.CommittedChunks
		return nil, &syncing.Error{Status: 409, Code: "route_chunk_gap", ExpectedChunkIndex: &expected}
	}
	first, last := rec.FirstRecordedAt.Time.UnixMicro(), rec.LastRecordedAt.Time.UnixMicro()
	for _, point := range chunk.Points {
		if point.RecordedAtUs < first || point.RecordedAtUs > last {
			return nil, syncing.Fail(422, "unsupported_payload")
		}
	}
	var receipt []byte
	err = tx.QueryRow(ctx, `SELECT rai_route_append_chunk($1,$2,$3,$4,$5,$6,$7::jsonb)`,
		p.DatasetID, chunk.RouteID, chunk.ChunkIndex, len(chunk.Points), wireHash, pointsHash, canonical).Scan(&receipt)
	if err != nil {
		return nil, routeDBError(err)
	}
	if err = checkUploadLease(ctx, tx, p, lease); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return json.RawMessage(receipt), nil
}

func (s *Store) SealRoute(ctx context.Context, p syncing.Principal, id string, seal syncing.RouteSeal) (json.RawMessage, error) {
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
	rec, err := scanRoute(tx.QueryRow(ctx, `SELECT `+routeColumns+` FROM workout_routes r
		JOIN workouts w ON w.dataset_id=r.dataset_id AND w.workout_uuid=r.workout_uuid
		WHERE r.dataset_id=$1 AND r.route_uuid=$2`, p.DatasetID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, syncing.Fail(404, "route_not_found")
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
	if rec.State == "sealed" {
		if rec.ChunkCount.Valid && rec.TotalPoints.Valid &&
			int(rec.ChunkCount.Int32) == seal.ChunkCount && rec.TotalPoints.Int64 == seal.TotalPoints &&
			hex.EncodeToString(rec.ManifestSHA256) == seal.ManifestSHA256 &&
			hex.EncodeToString(rec.PayloadManifestSHA256) == seal.PayloadManifestSHA256 {
			return rec.Receipt, nil
		}
		return nil, syncing.Fail(409, "route_already_sealed")
	}
	if rec.CommittedChunks != seal.ChunkCount || rec.CommittedPoints != seal.TotalPoints {
		return nil, syncing.Fail(409, "route_incomplete")
	}
	job := syncing.NewID()
	nextRev := rev + 1
	receipt := syncing.RouteReceipt{
		RouteID: id, WorkoutID: rec.WorkoutID, IngestStatus: "sealed", ChunkCount: seal.ChunkCount,
		TotalPoints: seal.TotalPoints, ManifestSHA256: seal.ManifestSHA256,
		PayloadManifestSHA256: seal.PayloadManifestSHA256, CommitRevision: nextRev,
		AnalysisStatus: "queued", AnalysisJobIDs: []string{job},
	}
	body, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	manifest, err := decodeRouteDigest(seal.ManifestSHA256)
	if err != nil {
		return nil, err
	}
	payloadManifest, err := decodeRouteDigest(seal.PayloadManifestSHA256)
	if err != nil {
		return nil, err
	}
	var applied bool
	var stored []byte
	err = tx.QueryRow(ctx, `SELECT applied,receipt FROM rai_route_seal($1,$2,$3,$4,$5,$6,$7,$8)`,
		p.DatasetID, id, seal.ChunkCount, seal.TotalPoints, manifest, payloadManifest, body, nextRev).Scan(&applied, &stored)
	if err != nil {
		return nil, routeDBError(err)
	}
	if !applied {
		return json.RawMessage(stored), nil
	}
	if _, err = tx.Exec(ctx, `UPDATE datasets SET commit_rev=$2 WHERE id=$1`, p.DatasetID, nextRev); err != nil {
		return nil, err
	}
	scope := "workout:" + syncing.WorkoutType + ":" + rec.WorkoutID
	var generation int64
	err = tx.QueryRow(ctx, `INSERT INTO dirty_scopes(dataset_id,scope_key,generation,dirty_from,dirty_through)
		VALUES($1,$2,1,$3,$4)
		ON CONFLICT(dataset_id,scope_key) DO UPDATE SET generation=dirty_scopes.generation+1,
			dirty_from=LEAST(dirty_scopes.dirty_from,EXCLUDED.dirty_from),
			dirty_through=GREATEST(dirty_scopes.dirty_through,EXCLUDED.dirty_through)
		RETURNING generation`, p.DatasetID, scope, rec.WorkoutStart, rec.WorkoutEnd).Scan(&generation)
	if err != nil {
		return nil, err
	}
	payload, _ := json.Marshal(map[string]any{"inputRevision": nextRev, "routeID": id})
	if _, err = tx.Exec(ctx, `INSERT INTO jobs(id,dataset_id,kind,scope_key,target_generation,payload)
		VALUES($1,$2,'analysis.workout',$3,$4,$5)`, job, p.DatasetID, scope, generation, payload); err != nil {
		return nil, err
	}
	changePayload, _ := json.Marshal(map[string]any{"routeID": id, "workoutID": rec.WorkoutID, "state": "sealed"})
	if _, err = tx.Exec(ctx, `INSERT INTO change_events(dataset_id,commit_rev,ordinal,event_id,entity_kind,entity_key,operation,payload)
		VALUES($1,$2,0,$3,'route',$4,'upsert',$5)`, p.DatasetID, nextRev, syncing.NewID(), id, changePayload); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}

func (s *Store) DeleteRoute(ctx context.Context, p syncing.Principal, id string, req syncing.RouteDeleteRequest) (json.RawMessage, error) {
	if err := req.Validate(); err != nil {
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
	rev, err := lockDataset(ctx, tx, p, p.DatasetID, req.DatasetEpoch, req.CollectorEpoch)
	if err != nil {
		return nil, err
	}
	var existing []byte
	err = tx.QueryRow(ctx, `SELECT receipt FROM route_deletions WHERE dataset_id=$1 AND route_uuid=$2`, p.DatasetID, id).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	reason := req.Reason
	if reason == "" {
		reason = "healthkit_deleted_object"
	}
	nextRev := rev + 1
	var workoutID *string
	var routeExisted, tombstoneCreated bool
	err = tx.QueryRow(ctx, `SELECT workout_uuid::text,route_existed,tombstone_created
		FROM rai_route_delete($1,$2,$3,$4)`, p.DatasetID, id, nextRev, reason).Scan(&workoutID, &routeExisted, &tombstoneCreated)
	if err != nil {
		return nil, routeDBError(err)
	}
	receipt := syncing.RouteDeleteReceipt{
		RouteID: id, IngestStatus: "deleted", RouteExisted: routeExisted,
		TombstoneCreated: tombstoneCreated, CommitRevision: rev, AnalysisStatus: "not_required",
		AnalysisJobIDs: []string{},
	}
	if workoutID != nil {
		receipt.WorkoutID = *workoutID
	}
	if routeExisted || tombstoneCreated {
		if _, err = tx.Exec(ctx, `UPDATE datasets SET commit_rev=$2 WHERE id=$1`, p.DatasetID, nextRev); err != nil {
			return nil, err
		}
		receipt.CommitRevision = nextRev
		changePayload, _ := json.Marshal(map[string]any{"routeID": id, "reason": reason})
		if _, err = tx.Exec(ctx, `INSERT INTO change_events(dataset_id,commit_rev,ordinal,event_id,entity_kind,entity_key,operation,payload)
			VALUES($1,$2,0,$3,'route',$4,'delete',$5)`, p.DatasetID, nextRev, syncing.NewID(), id, changePayload); err != nil {
			return nil, err
		}
		if workoutID != nil {
			scope := "workout:" + syncing.WorkoutType + ":" + *workoutID
			var start, end time.Time
			if err = tx.QueryRow(ctx, `SELECT event_start,event_end FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2`,
				p.DatasetID, *workoutID).Scan(&start, &end); err != nil {
				return nil, err
			}
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
			job := syncing.NewID()
			payload, _ := json.Marshal(map[string]any{"inputRevision": nextRev, "routeID": id})
			if _, err = tx.Exec(ctx, `INSERT INTO jobs(id,dataset_id,kind,scope_key,target_generation,payload)
				VALUES($1,$2,'analysis.workout',$3,$4,$5)`, job, p.DatasetID, scope, generation, payload); err != nil {
				return nil, err
			}
			receipt.AnalysisStatus = "queued"
			receipt.AnalysisJobIDs = []string{job}
		}
	}
	body, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO route_deletions(dataset_id,route_uuid,workout_uuid,deleted_rev,reason,receipt)
		VALUES($1,$2,$3,$4,$5,$6)`, p.DatasetID, id, workoutID, receipt.CommitRevision, reason, body)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}
