//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"rai/server/internal/syncing"
)

func TestSampleBatchPipelineAndIdempotency(t *testing.T) {
	f := setup(t)
	var previous *string
	var first syncing.Event
	started := time.Now()
	for seq := int64(1); seq <= 5; seq++ {
		events := make([]syncing.Event, 1000)
		for i := range events {
			events[i] = sample(syncing.NewID(), 120)
		}
		if seq == 1 {
			first = events[0]
		}
		batchStart := time.Now()
		e, r, _ := f.commit(t, events, seq, previous)
		if r.Counts.Inserted != 1000 || len(r.AnalysisJobIDs) != 1000 {
			t.Fatalf("batch %d counts: %+v", seq, r.Counts)
		}
		if seq == 1 {
			t.Logf("synthetic 1000-sample complete commit: %s", time.Since(batchStart))
		}
		previous = &e.BatchID
	}
	t.Logf("synthetic 5000 samples across five complete commits: %s", time.Since(started))
	if count(t, f, "health_samples") != 5000 || count(t, f, "jobs") != 5000 {
		t.Fatal("bulk facts or jobs missing")
	}
	var streamDevice, batchDevice string
	ctx := context.Background()
	if err := f.s.Pool.QueryRow(ctx, `SELECT s.device_id,b.device_id FROM sync_streams s JOIN sync_batches b ON b.dataset_id=s.dataset_id AND b.stream_id=s.id WHERE s.dataset_id=$1 AND b.batch_id=$2`, f.p.DatasetID, *previous).Scan(&streamDevice, &batchDevice); err != nil || streamDevice != f.p.DeviceID || batchDevice != f.p.DeviceID {
		t.Fatal("device provenance missing", err)
	}
	var missing int
	if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM health_samples WHERE dataset_id=$1 AND ingest_device_id IS DISTINCT FROM $2::uuid`, f.p.DatasetID, f.p.DeviceID).Scan(&missing); err != nil || missing != 0 {
		t.Fatal("sample collector provenance missing", err)
	}
	if _, err := f.s.Pool.Exec(ctx, `UPDATE sync_batches SET device_id=$1 WHERE dataset_id=$2 AND batch_id=$3`, syncing.NewID(), f.p.DatasetID, *previous); err == nil {
		t.Fatal("invalid device FK accepted")
	}
	_, receipt, _ := f.commit(t, []syncing.Event{first}, 6, previous)
	if receipt.Counts.Unchanged != 1 || len(receipt.AnalysisJobIDs) != 0 {
		t.Fatal("sample retry duplicated facts or jobs")
	}
}
