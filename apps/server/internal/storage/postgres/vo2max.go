package postgres

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"rai/server/internal/syncing"

	"github.com/jackc/pgx/v5"
)

// CommitVO2 owns the same dataset -> stream -> upload-slot lock order as
// generic sync. Even an empty query has a durable, ordered coverage commit.
func (s *Store) CommitVO2(ctx context.Context, p syncing.Principal, raw []byte, lease syncing.UploadLease) (json.RawMessage, error) {
	decodeTimer := syncing.PhaseTimer(ctx, "vo2_parse")
	b, err := syncing.DecodeVO2Batch(raw)
	decodeTimer()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	defer syncing.PhaseTimer(ctx, "vo2_transaction")()
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rev, err := lockDataset(ctx, tx, p, b.DatasetID, b.DatasetEpoch, b.CollectorEpoch)
	if err != nil {
		var e *syncing.Error
		if errors.As(err, &e) && e.Code == "dataset_epoch_changed" {
			return nil, syncing.Fail(409, "generation_conflict")
		}
		return nil, err
	}
	// Create the shared stream on the first request, atomically with its ACK.
	_, err = tx.Exec(ctx, `INSERT INTO sync_streams(dataset_id,id,collector_epoch,sample_type,query_fingerprint)
 VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, p.DatasetID, b.StreamID, b.CollectorEpoch, b.SampleType, b.QueryFingerprint)
	if err != nil {
		return nil, err
	}
	e := syncing.Envelope{StreamID: b.StreamID, CollectorEpoch: b.CollectorEpoch, QueryFingerprint: b.QueryFingerprint, ClientSeq: b.ClientSeq, PreviousBatchID: b.PreviousBatchID}
	stream, err := lockedStream(ctx, tx, p, e)
	if err != nil {
		var conflict *syncing.Error
		if errors.As(err, &conflict) && conflict.Code == "stream_not_found" {
			return nil, syncing.Fail(409, "stream_identity_conflict")
		}
		return nil, err
	}
	if stream.SampleType != b.SampleType {
		return nil, syncing.Fail(409, "stream_identity_conflict")
	}
	if err = lockUploadLease(ctx, tx, p, lease); err != nil {
		return nil, err
	}
	hash := syncing.DailyDigest(raw)
	digest, _ := hex.DecodeString(hash)
	var priorHash, priorReceipt []byte
	err = tx.QueryRow(ctx, `SELECT payload_sha256,receipt FROM sync_batches WHERE dataset_id=$1 AND batch_id=$2`, p.DatasetID, b.BatchID).Scan(&priorHash, &priorReceipt)
	if err == nil {
		if !bytes.Equal(priorHash, digest) {
			return nil, syncing.Fail(409, "batch_identity_conflict")
		}
		if priorReceipt == nil {
			return nil, syncing.Fail(409, "stale_batch")
		}
		return uploadRetryReceipt(ctx, tx, p, lease, priorReceipt)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err = checkSequence(stream, e); err != nil {
		return nil, syncing.Fail(409, "stale_batch")
	}
	if b.ClientSeq > 1 {
		var start, created int64
		if err = tx.QueryRow(ctx, `SELECT bootstrap_start_us,flow_created_at_us FROM daily_health_coverage WHERE dataset_id=$1 AND stream_id=$2`, p.DatasetID, b.StreamID).Scan(&start, &created); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, syncing.Fail(409, "stale_batch")
			}
			return nil, err
		}
		if start != b.BootstrapStartUs || created != b.FlowCreatedAtUs {
			return nil, syncing.Fail(409, "stream_identity_conflict")
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO sync_batches(dataset_id,batch_id,stream_id,client_seq,dataset_epoch,envelope_sha256,payload_sha256,envelope,event_count)
 VALUES($1,$2,$3,$4,$5,$6,$6,$7,$8)`, p.DatasetID, b.BatchID, b.StreamID, b.ClientSeq, b.DatasetEpoch, digest, raw, len(b.Facts)+len(b.DeletedUUIDs))
	if err != nil {
		return nil, err
	}
	events, err := syncing.Collapse(b.Events())
	if err != nil {
		return nil, err
	}
	r := syncing.VO2Receipt{Contract: syncing.VO2Contract, Version: 1, MetadataRegistryVersion: 1, BatchID: b.BatchID, DatasetID: p.DatasetID, DatasetEpoch: b.DatasetEpoch, CollectorEpoch: b.CollectorEpoch, StreamID: b.StreamID, ClientSeq: b.ClientSeq, RequestSHA256: hash, IngestStatus: "committed", CommitRevision: rev + 1, DeleteStatus: "none", AnalysisStatus: "awaiting_model_implementation"}
	// Pipeline new facts using the existing immutable identity/tombstone logic.
	upserts := make([]syncing.Event, 0, len(events))
	deletes := make([]syncing.Event, 0, len(events))
	for _, event := range events {
		if event.Op == "upsert" {
			upserts = append(upserts, event)
		} else {
			deletes = append(deletes, event)
		}
	}
	scopes := sampleDirtyScopes{}
	ordinal, err := applySampleUpsertsBatch(ctx, tx, p.DatasetID, b.BatchID, upserts, rev+1, &r.Counts, scopes)
	if err != nil {
		return nil, err
	}
	for _, event := range deletes {
		changed, _, err := applyEvent(ctx, tx, p.DatasetID, b.BatchID, event, rev+1, ordinal, &r.Counts, scopes)
		if err != nil {
			return nil, err
		}
		if changed {
			ordinal++
		}
	}
	if r.Counts.Suppressed > 0 {
		r.DeleteStatus = "deleted_precedence"
	}
	// This scope is a raw-observation invalidation marker. Its interval is
	// observation time, never a fabricated interval for an unknown deletion.
	_, err = tx.Exec(ctx, `INSERT INTO dirty_scopes(dataset_id,scope_key,generation,dirty_from,dirty_through)
 VALUES($1,'external/vo2max/facts/v1',1,$2,$2)
 ON CONFLICT(dataset_id,scope_key) DO UPDATE SET generation=dirty_scopes.generation+1,
 dirty_from=LEAST(dirty_scopes.dirty_from,EXCLUDED.dirty_from),dirty_through=GREATEST(dirty_scopes.dirty_through,EXCLUDED.dirty_through)`, p.DatasetID, time.UnixMicro(b.QueryCompletedAtUs).UTC())
	if err != nil {
		return nil, err
	}
	var last *int64
	sources := map[string]int{}
	for _, fact := range b.Facts {
		if last == nil || fact.EndUs > *last {
			v := fact.EndUs
			last = &v
		}
		sources[fact.Provenance.EpochID()]++
	}
	epochs := make([]string, 0, len(sources))
	for epoch := range sources {
		epochs = append(epochs, epoch)
	}
	sort.Strings(epochs)
	summary := make([]syncing.VO2SourceCount, 0, len(epochs))
	for _, epoch := range epochs {
		summary = append(summary, syncing.VO2SourceCount{SourceEpochID: epoch, Count: sources[epoch]})
	}
	sourceJSON, _ := json.Marshal(summary)
	_, err = tx.Exec(ctx, `INSERT INTO daily_health_coverage(dataset_id,stream_id,bootstrap_start_us,last_discovery_at_us,last_fact_at_us,bootstrap_complete,has_more,sample_count,deleted_count,source_context,
 flow_created_at_us,query_completed_at_us,anchor_present,source_summary,summary_observation_seq)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'[]',$10,$4,true,$11,CASE WHEN $8>0 THEN $12 ELSE 0 END)
 ON CONFLICT(dataset_id,stream_id) DO UPDATE SET last_discovery_at_us=EXCLUDED.last_discovery_at_us,last_sync_at=clock_timestamp(),
 last_fact_at_us=GREATEST(daily_health_coverage.last_fact_at_us,EXCLUDED.last_fact_at_us),
 bootstrap_complete=daily_health_coverage.bootstrap_complete OR EXCLUDED.bootstrap_complete,has_more=EXCLUDED.has_more,
 sample_count=EXCLUDED.sample_count,deleted_count=EXCLUDED.deleted_count,query_completed_at_us=EXCLUDED.query_completed_at_us,anchor_present=true,
 source_summary=CASE WHEN EXCLUDED.sample_count>0 THEN EXCLUDED.source_summary ELSE daily_health_coverage.source_summary END,
 summary_observation_seq=CASE WHEN EXCLUDED.sample_count>0 THEN EXCLUDED.summary_observation_seq ELSE daily_health_coverage.summary_observation_seq END`,
		p.DatasetID, b.StreamID, b.BootstrapStartUs, b.QueryCompletedAtUs, last, !b.HasMore, b.HasMore, len(b.Facts), len(b.DeletedUUIDs), b.FlowCreatedAtUs, sourceJSON, b.ClientSeq)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(r)
	// Coverage itself is observable state and therefore advances commit_rev.
	_, err = tx.Exec(ctx, `INSERT INTO change_events(dataset_id,commit_rev,ordinal,event_id,entity_kind,entity_key,operation,payload)
 VALUES($1,$2,$3,$4,'daily_coverage',$5,'observed',$6)`, p.DatasetID, rev+1, ordinal, syncing.NewID(), b.SampleType, body)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE sync_batches SET state='committed',receipt=$3,committed_at=now() WHERE dataset_id=$1 AND batch_id=$2`, p.DatasetID, b.BatchID, body)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE sync_streams SET last_client_seq=$3,last_batch_id=$4 WHERE dataset_id=$1 AND id=$2`, p.DatasetID, b.StreamID, b.ClientSeq, b.BatchID)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE datasets SET commit_rev=$2 WHERE id=$1`, p.DatasetID, rev+1)
	if err != nil {
		return nil, err
	}
	if err = checkUploadLease(ctx, tx, p, lease); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return body, nil
}

// One epoch-fenced snapshot. A new collector never inherits completed coverage.
func (s *Store) VO2Coverage(ctx context.Context, p syncing.Principal) (syncing.VO2Coverage, error) {
	c := syncing.VO2Coverage{Contract: syncing.VO2Contract, Version: 1, SampleType: syncing.VO2Type, State: "sync_incomplete", ReadAuthorization: "not_determinable", SourceSummary: json.RawMessage(`[]`)}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return c, err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `SELECT epoch::text,commit_rev,
 EXISTS(SELECT 1 FROM health_samples WHERE dataset_id=$1 AND sample_type=$2),
 EXISTS(SELECT 1 FROM entity_tombstones WHERE dataset_id=$1 AND sample_type=$2)
 FROM datasets WHERE id=$1`, p.DatasetID, syncing.VO2Type).Scan(&c.DatasetGeneration, &c.DatasetCommitRevision, &c.HasLiveFacts, &c.HasDeletedFacts)
	if err != nil {
		return c, err
	}
	var syncAt time.Time
	err = tx.QueryRow(ctx, `SELECT c.bootstrap_start_us,c.query_completed_at_us,c.last_sync_at,c.last_fact_at_us,c.bootstrap_complete,c.has_more,c.sample_count,c.deleted_count,c.anchor_present,c.source_summary,c.summary_observation_seq,s.last_client_seq
 FROM daily_health_coverage c JOIN sync_streams s ON s.dataset_id=c.dataset_id AND s.id=c.stream_id
 JOIN collector_leases l ON l.dataset_id=s.dataset_id AND l.collector_epoch=s.collector_epoch
 JOIN sync_batches b ON b.dataset_id=s.dataset_id AND b.batch_id=s.last_batch_id AND b.dataset_epoch=$3
 WHERE c.dataset_id=$1 AND s.sample_type=$2 AND b.envelope->>'contract'=$4
 ORDER BY c.last_sync_at DESC LIMIT 1`, p.DatasetID, syncing.VO2Type, c.DatasetGeneration, syncing.VO2Contract).Scan(&c.BootstrapStart, &c.QueryCompletedAt, &syncAt, &c.LastFactAt, &c.BootstrapComplete, &c.HasMore, &c.SampleCount, &c.DeletedCount, &c.AnchorPresent, &c.SourceSummary, &c.SummaryObservationSequence, &c.StreamSequence)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return c, err
	}
	if err == nil {
		us := syncAt.UnixMicro()
		c.LastSyncAt = &us
		c.QueryCompleted = true
		c.SyncCompleteForObservation = !*c.HasMore
		if c.BootstrapComplete && !*c.HasMore {
			c.State = "no_sample_observed"
			if c.HasLiveFacts {
				c.State = "sample_exists"
			} else if c.HasDeletedFacts {
				c.State = "sample_deleted"
			}
		}
	}
	return c, tx.Commit(ctx)
}
