package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"rai/server/internal/syncing"

	"github.com/jackc/pgx/v5"
)

type sampleDirtyRange struct{ start, end time.Time }
type sampleDirtyScopes map[string]sampleDirtyRange

func (scopes sampleDirtyScopes) add(sampleType string, start, end time.Time) {
	scope := "sample:" + sampleType + ":" + start.UTC().Format("20060102")
	g, ok := scopes[scope]
	if !ok {
		scopes[scope] = sampleDirtyRange{start, end}
		return
	}
	if start.Before(g.start) {
		g.start = start
	}
	if end.After(g.end) {
		g.end = end
	}
	scopes[scope] = g
}

// The dataset row is already locked and all events validated and collapsed.
// Pipeline identity checks and writes without weakening the receipt, log,
// delete barrier or dirty-generation transaction used by applyEvent.
func applySampleUpsertsBatch(ctx context.Context, tx pgx.Tx, dataset, batchID string, events []syncing.Event, rev int64, counts *syncing.Counts, scopes sampleDirtyScopes) (int, error) {
	checks := &pgx.Batch{}
	for _, e := range events {
		checks.Queue(`SELECT EXISTS(SELECT 1 FROM entity_tombstones WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3),
			(SELECT content_sha256 FROM health_samples WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3)`, dataset, e.Type, e.UUID)
	}
	results := tx.SendBatch(ctx, checks)
	newEvents := make([]syncing.Event, 0, len(events))
	for _, e := range events {
		var tombstone bool
		var oldHash []byte
		if err := results.QueryRow().Scan(&tombstone, &oldHash); err != nil {
			_ = results.Close()
			return 0, err
		}
		if tombstone {
			counts.Suppressed++
			continue
		}
		if oldHash != nil {
			_, hash := e.Canonical()
			if !bytes.Equal(oldHash, hash) {
				_ = results.Close()
				return 0, syncing.Fail(409, "sample_identity_conflict")
			}
			counts.Unchanged++
			continue
		}
		newEvents = append(newEvents, e)
	}
	if err := results.Close(); err != nil {
		return 0, err
	}
	writes := &pgx.Batch{}
	for ordinal, e := range newEvents {
		canonical, hash := e.Canonical()
		start, end := time.UnixMicro(*e.StartUs).UTC(), time.UnixMicro(*e.EndUs).UTC()
		var value *float64
		var unit *string
		if e.Quantity != nil {
			value, unit = &e.Quantity.Value, &e.Quantity.Unit
		}
		metadata := e.Metadata
		if metadata == nil {
			metadata = map[string]any{}
		}
		meta, err := json.Marshal(metadata)
		if err != nil {
			return 0, err
		}
		scopes.add(e.Type, start, end)
		writes.Queue(`WITH fact AS (
			INSERT INTO health_samples(dataset_id,sample_type,sample_uuid,event_start,event_end,value_numeric,value_category,unit,source_bundle_id,content_sha256,entity_revision,metadata)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		), entity_log AS (
			INSERT INTO entity_changes(dataset_id,batch_id,ordinal,commit_rev,sample_type,sample_uuid,operation,new_start,new_end)
			VALUES($1,$13,$14,$11,$2,$3,'upsert',$4,$5)
		), event_log AS (
			INSERT INTO change_events(dataset_id,commit_rev,ordinal,event_id,entity_kind,entity_key,operation,payload)
			VALUES($1,$11,$14,$15,'sample',$16,'upsert',$17)
        ) SELECT 1`, dataset, e.Type, e.UUID, start, end, value, e.Category, unit, e.Source.BundleID, hash, rev, meta, batchID, ordinal, syncing.NewID(), e.Type+":"+e.UUID, canonical)
	}
	if len(newEvents) > 0 {
		results = tx.SendBatch(ctx, writes)
		for range newEvents {
			if _, err := results.Exec(); err != nil {
				_ = results.Close()
				return 0, err
			}
		}
		if err := results.Close(); err != nil {
			return 0, err
		}
	}
	counts.Inserted += len(newEvents)
	return len(newEvents), nil
}
