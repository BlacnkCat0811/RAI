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
	"rai/server/internal/storage/postgres/dbgen"
	"rai/server/internal/syncing"
)

type seriesRecord struct {
	SeriesID       string
	SampleType     string
	SampleUUID     string
	ParentStartUs  int64
	ParentEndUs    int64
	DatasetEpoch   string
	CollectorEpoch string
	State          string
	SealedChunks   int32
	TotalPoints    int64
	ManifestSHA256 []byte
	ChunkCount     *int
	Receipt        []byte
}

const seriesColumns = `series_id::text,sample_type,sample_uuid::text,parent_start_us,parent_end_us,
	dataset_epoch::text,collector_epoch::text,state,sealed_chunks,total_points,manifest_sha256,chunk_count,receipt`

func scanSeries(row pgx.Row) (seriesRecord, error) {
	var r seriesRecord
	var chunk pgtype.Int4
	err := row.Scan(&r.SeriesID, &r.SampleType, &r.SampleUUID, &r.ParentStartUs, &r.ParentEndUs,
		&r.DatasetEpoch, &r.CollectorEpoch, &r.State, &r.SealedChunks, &r.TotalPoints,
		&r.ManifestSHA256, &chunk, &r.Receipt)
	if chunk.Valid {
		n := int(chunk.Int32)
		r.ChunkCount = &n
	}
	return r, err
}

func (r seriesRecord) state() syncing.SeriesState {
	return syncing.SeriesState{
		SeriesID: r.SeriesID, SampleType: r.SampleType, SampleUUID: r.SampleUUID,
		SampleStartUs: r.ParentStartUs, SampleEndUs: r.ParentEndUs,
		DatasetEpoch: r.DatasetEpoch, CollectorEpoch: r.CollectorEpoch,
		IngestStatus: r.State, CommittedChunks: int(r.SealedChunks), TotalPoints: r.TotalPoints,
		ManifestSHA256: hex.EncodeToString(r.ManifestSHA256), ChunkCount: r.ChunkCount,
		Receipt: json.RawMessage(r.Receipt),
	}
}

func seriesStateFromRow(r dbgen.SeriesRow) syncing.SeriesState {
	state := syncing.SeriesState{
		SeriesID: r.SeriesID, SampleType: r.SampleType, SampleUUID: r.SampleUuid,
		SampleStartUs: r.ParentStartUs, SampleEndUs: r.ParentEndUs,
		DatasetEpoch: r.DatasetEpoch, CollectorEpoch: r.CollectorEpoch,
		IngestStatus: r.State, CommittedChunks: int(r.SealedChunks), TotalPoints: r.TotalPoints,
		ManifestSHA256: hex.EncodeToString(r.ManifestSha256), Receipt: json.RawMessage(r.Receipt),
	}
	if r.ChunkCount.Valid {
		n := int(r.ChunkCount.Int32)
		state.ChunkCount = &n
	}
	return state
}

// Series returns the registration/completeness state. It never exposes an
// unsealed chunk as a complete series.
func (s *Store) Series(ctx context.Context, p syncing.Principal, id string) (syncing.SeriesState, error) {
	r, err := s.q.Series(ctx, dbgen.SeriesParams{DatasetID: p.DatasetID, SeriesID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return syncing.SeriesState{}, syncing.Fail(404, "series_not_found")
	}
	if err != nil {
		return syncing.SeriesState{}, err
	}
	return seriesStateFromRow(r), nil
}

func seriesConflictFields(r seriesRecord, req syncing.SeriesRequest) bool {
	return r.SampleType != req.SampleType || r.SampleUUID != req.SampleUUID ||
		r.DatasetEpoch != req.DatasetEpoch || r.CollectorEpoch != req.CollectorEpoch
}

// RegisterSeries is a short metadata transaction. It binds one series identity
// to one already committed quantity sample. It never scans chunks, facts or
// history and never advances commit_rev.
func (s *Store) RegisterSeries(ctx context.Context, p syncing.Principal, req syncing.SeriesRequest) (syncing.SeriesState, error) {
	if err := req.Validate(); err != nil {
		return syncing.SeriesState{}, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return syncing.SeriesState{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = lockDataset(ctx, tx, p, req.DatasetID, req.DatasetEpoch, req.CollectorEpoch); err != nil {
		return syncing.SeriesState{}, err
	}
	q := dbgen.New(tx)
	existing, err := scanSeries(tx.QueryRow(ctx, `SELECT `+seriesColumns+` FROM series_uploads WHERE dataset_id=$1 AND series_id=$2`, p.DatasetID, req.SeriesID))
	if err == nil {
		if seriesConflictFields(existing, req) {
			return syncing.SeriesState{}, syncing.Fail(409, "series_identity_conflict")
		}
		if err = tx.Commit(ctx); err != nil {
			return syncing.SeriesState{}, err
		}
		return existing.state(), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return syncing.SeriesState{}, err
	}
	// A different seriesID for the same parent is not a retry; it is a second
	// identity for an immutable parent and must be rejected.
	if _, err = q.SeriesByParent(ctx, dbgen.SeriesByParentParams{DatasetID: p.DatasetID, SampleType: req.SampleType, SampleUuid: req.SampleUUID}); err == nil {
		return syncing.SeriesState{}, syncing.Fail(409, "series_identity_conflict")
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return syncing.SeriesState{}, err
	}
	tombstone, err := q.TombstoneExists(ctx, dbgen.TombstoneExistsParams{DatasetID: p.DatasetID, SampleType: req.SampleType, SampleUuid: req.SampleUUID})
	if err != nil {
		return syncing.SeriesState{}, err
	}
	if tombstone {
		return syncing.SeriesState{}, syncing.Fail(409, "series_parent_unavailable")
	}
	parent, err := q.SampleIdentity(ctx, dbgen.SampleIdentityParams{DatasetID: p.DatasetID, SampleType: req.SampleType, SampleUuid: req.SampleUUID})
	if errors.Is(err, pgx.ErrNoRows) {
		return syncing.SeriesState{}, syncing.Fail(409, "series_parent_unavailable")
	}
	if err != nil {
		return syncing.SeriesState{}, err
	}
	startUs, endUs := parent.EventStart.Time.UnixMicro(), parent.EventEnd.Time.UnixMicro()
	manifest := syncing.EmptySeriesManifest()
	_, err = tx.Exec(ctx, `INSERT INTO series_uploads(dataset_id,series_id,sample_type,sample_uuid,parent_start_us,parent_end_us,dataset_epoch,collector_epoch,manifest_sha256)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, p.DatasetID, req.SeriesID, req.SampleType, req.SampleUUID,
		startUs, endUs, req.DatasetEpoch, req.CollectorEpoch, manifest)
	if uniqueViolation(err) {
		return syncing.SeriesState{}, syncing.Fail(409, "series_identity_conflict")
	}
	if err != nil {
		return syncing.SeriesState{}, err
	}
	rec := seriesRecord{SeriesID: req.SeriesID, SampleType: req.SampleType, SampleUUID: req.SampleUUID,
		ParentStartUs: startUs, ParentEndUs: endUs, DatasetEpoch: req.DatasetEpoch,
		CollectorEpoch: req.CollectorEpoch, State: "pending", ManifestSHA256: manifest}
	if err = tx.Commit(ctx); err != nil {
		return syncing.SeriesState{}, err
	}
	return rec.state(), nil
}

func decodeSeriesDigest(value string) ([]byte, error) {
	b, err := hex.DecodeString(value)
	if err != nil || len(b) != sha256Size {
		return nil, syncing.Fail(422, "unsupported_payload")
	}
	return b, nil
}

const sha256Size = 32

// CommitSeriesChunk stores one immutable, contiguous chunk. Chunks are durable
// staging data; a series becomes visible only through SealSeries. The dataset
// lock serializes parent deletion with every chunk, and the series row lock
// serializes concurrent chunk attempts across API replicas.
func (s *Store) CommitSeriesChunk(ctx context.Context, p syncing.Principal, chunk syncing.SeriesChunk, lease syncing.UploadLease) (json.RawMessage, error) {
	if !syncing.ValidID(chunk.SeriesID) || chunk.ChunkIndex < 0 || chunk.ChunkIndex >= syncing.MaxSeriesChunks {
		return nil, syncing.Fail(422, "unsupported_payload")
	}
	if len(chunk.Points) == 0 {
		return nil, syncing.Fail(422, "unsupported_payload")
	}
	if len(chunk.Points) > syncing.MaxSeriesPointsPerChunk || chunk.PointCount != len(chunk.Points) {
		return nil, syncing.Fail(413, "series_chunk_too_large")
	}
	wireHash, err := decodeSeriesDigest(chunk.WireSHA256)
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
	rec, err := scanSeries(tx.QueryRow(ctx, `SELECT `+seriesColumns+` FROM series_uploads WHERE dataset_id=$1 AND series_id=$2 FOR UPDATE`, p.DatasetID, chunk.SeriesID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, syncing.Fail(404, "series_not_found")
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
	q := dbgen.New(tx)
	existing, err := q.SeriesChunk(ctx, dbgen.SeriesChunkParams{DatasetID: p.DatasetID, SeriesID: chunk.SeriesID, ChunkIndex: int32(chunk.ChunkIndex)})
	if err == nil {
		if bytes.Equal(existing.WireSha256, wireHash) {
			return existing.Receipt, nil
		}
		return nil, syncing.Fail(409, "series_chunk_conflict")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if rec.State == "sealed" {
		return nil, syncing.Fail(409, "series_sealed")
	}
	if int(rec.SealedChunks) != chunk.ChunkIndex {
		expected := int(rec.SealedChunks)
		return nil, &syncing.Error{Status: 409, Code: "series_chunk_gap", ExpectedChunkIndex: &expected}
	}
	canonical, digest := syncing.SeriesPointsCanonical(chunk.Points)
	minStart, maxEnd := int64(0), int64(0)
	for i, point := range chunk.Points {
		if err = point.Validate(rec.SampleType, rec.ParentStartUs, rec.ParentEndUs); err != nil {
			index := i
			return nil, &syncing.Error{Status: 422, Code: "unsupported_payload", EventIndex: &index}
		}
		if i == 0 || point.StartUs < minStart {
			minStart = point.StartUs
		}
		if i == 0 || point.EndUs > maxEnd {
			maxEnd = point.EndUs
		}
	}
	if chunk.PointsSHA256 != "" {
		passed, err := decodeSeriesDigest(chunk.PointsSHA256)
		if err != nil || !bytes.Equal(passed, digest) {
			return nil, syncing.Fail(422, "unsupported_payload")
		}
	}
	if rec.TotalPoints+int64(len(chunk.Points)) > syncing.MaxSeriesPoints {
		return nil, syncing.Fail(413, "series_too_large")
	}
	manifest := syncing.AdvanceSeriesManifest(rec.ManifestSHA256, digest)
	committed := int(rec.SealedChunks) + 1
	body, err := json.Marshal(syncing.SeriesChunkReceipt{SeriesID: chunk.SeriesID, ChunkIndex: chunk.ChunkIndex,
		IngestStatus: "pending", PointCount: len(chunk.Points), PayloadSHA256: chunk.WireSHA256, ChunkSHA256: hex.EncodeToString(digest),
		ManifestSHA256: hex.EncodeToString(manifest), CommittedChunks: committed})
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO series_chunks(dataset_id,series_id,chunk_index,point_count,min_start_us,max_end_us,wire_sha256,points_sha256,points,receipt)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10)`, p.DatasetID, chunk.SeriesID, chunk.ChunkIndex, len(chunk.Points),
		minStart, maxEnd, wireHash, digest, string(canonical), body)
	if err != nil {
		return nil, err
	}
	tag, err := tx.Exec(ctx, `UPDATE series_uploads SET sealed_chunks=sealed_chunks+1,total_points=total_points+$3,manifest_sha256=$4
		WHERE dataset_id=$1 AND series_id=$2 AND state='pending'`, p.DatasetID, chunk.SeriesID, len(chunk.Points), manifest)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() != 1 {
		return nil, syncing.Fail(409, "series_sealed")
	}
	if err = checkUploadLease(ctx, tx, p, lease); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}

// SealSeries records the completeness marker in one short transaction. It
// verifies only the fixed counts and chained digest, never re-reads all points.
// The marker, dataset revision, dirty scope, analysis job, change event and
// receipt commit together; an incomplete series cannot become visible.
func (s *Store) SealSeries(ctx context.Context, p syncing.Principal, id string, seal syncing.SeriesSeal) (json.RawMessage, error) {
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
	rec, err := scanSeries(tx.QueryRow(ctx, `SELECT `+seriesColumns+` FROM series_uploads WHERE dataset_id=$1 AND series_id=$2 FOR UPDATE`, p.DatasetID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, syncing.Fail(404, "series_not_found")
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
		if rec.ChunkCount != nil && *rec.ChunkCount == seal.ChunkCount && rec.TotalPoints == seal.TotalPoints && storedManifest == seal.ManifestSHA256 {
			return rec.Receipt, nil
		}
		return nil, syncing.Fail(409, "series_identity_conflict")
	}
	if int(rec.SealedChunks) != seal.ChunkCount || rec.TotalPoints != seal.TotalPoints {
		return nil, syncing.Fail(409, "series_incomplete")
	}
	if storedManifest != seal.ManifestSHA256 {
		return nil, syncing.Fail(409, "series_manifest_mismatch")
	}
	// Check only the bounded chunk index (at most 1000 rows); never re-read
	// point payloads or scan unrelated history to recompute completeness.
	var actualChunks int
	var actualPoints int64
	if err = tx.QueryRow(ctx, `SELECT count(*),coalesce(sum(point_count),0) FROM series_chunks WHERE dataset_id=$1 AND series_id=$2`,
		p.DatasetID, id).Scan(&actualChunks, &actualPoints); err != nil {
		return nil, err
	}
	if actualChunks != seal.ChunkCount || actualPoints != seal.TotalPoints {
		return nil, syncing.Fail(409, "series_incomplete")
	}
	rev++
	if _, err = tx.Exec(ctx, `UPDATE datasets SET commit_rev=$2 WHERE id=$1`, p.DatasetID, rev); err != nil {
		return nil, err
	}
	scope := fmt.Sprintf("series:%s:%s", rec.SampleType, rec.SampleUUID)
	var generation int64
	err = tx.QueryRow(ctx, `INSERT INTO dirty_scopes(dataset_id,scope_key,generation,dirty_from,dirty_through) VALUES($1,$2,1,$3,$4)
		ON CONFLICT(dataset_id,scope_key) DO UPDATE SET generation=dirty_scopes.generation+1,
		dirty_from=LEAST(dirty_scopes.dirty_from,EXCLUDED.dirty_from),dirty_through=GREATEST(dirty_scopes.dirty_through,EXCLUDED.dirty_through)
		RETURNING generation`, p.DatasetID, scope, time.UnixMicro(rec.ParentStartUs).UTC(), time.UnixMicro(rec.ParentEndUs).UTC()).Scan(&generation)
	if err != nil {
		return nil, err
	}
	job := syncing.NewID()
	jobPayload, _ := json.Marshal(map[string]any{"inputRevision": rev, "seriesID": rec.SeriesID})
	if _, err = tx.Exec(ctx, `INSERT INTO jobs(id,dataset_id,kind,scope_key,target_generation,payload) VALUES($1,$2,$3,$4,$5,$6)`,
		job, p.DatasetID, "analysis.series", scope, generation, jobPayload); err != nil {
		return nil, err
	}
	receipt := syncing.SeriesReceipt{SeriesID: rec.SeriesID, IngestStatus: "sealed", SampleType: rec.SampleType,
		SampleUUID: rec.SampleUUID, ChunkCount: seal.ChunkCount, TotalPoints: seal.TotalPoints,
		ManifestSHA256: storedManifest, CommitRevision: rev, AnalysisStatus: "queued", AnalysisJobIDs: []string{job}}
	body, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	eventPayload, _ := json.Marshal(map[string]any{"seriesID": rec.SeriesID, "sampleType": rec.SampleType, "sampleUUID": rec.SampleUUID,
		"chunkCount": seal.ChunkCount, "totalPoints": seal.TotalPoints, "manifestSHA256": storedManifest})
	if _, err = tx.Exec(ctx, `INSERT INTO change_events(dataset_id,commit_rev,ordinal,event_id,entity_kind,entity_key,operation,payload)
		VALUES($1,$2,0,$3,'series',$4,'sealed',$5)`, p.DatasetID, rev, syncing.NewID(), scope, eventPayload); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE series_uploads SET state='sealed',chunk_count=$3,receipt=$4,sealed_at=now() WHERE dataset_id=$1 AND series_id=$2`,
		p.DatasetID, id, seal.ChunkCount, body); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}
