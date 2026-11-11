//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"rai/server/internal/syncing"
)

// dailyPage builds a synthetic sleep page of n facts (or n deletions of the
// given UUIDs) with Watch-like sources and metadata, so its size and the
// per-row work match a full real page.
func dailyPage(t *testing.T, f *fixture, stream string, seq int64, prev *string, start int64, facts int, deleted []string) ([]byte, string) {
	t.Helper()
	b := syncing.DailyBatch{Version: 1, DatasetID: f.enrollment.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch,
		StreamID: stream, BatchID: syncing.NewID(), ClientSeq: seq, PreviousBatchID: prev, SampleType: syncing.DailySleep,
		QueryFingerprint: syncing.DailyFingerprint(syncing.DailySleep, start), BootstrapStartUs: start, ObservedAtUs: start + 30*24*3600*1_000_000,
		HasMore: true, Facts: []syncing.DailyFact{}, DeletedUUIDs: deleted}
	for i := 0; i < facts; i++ {
		category := i % 6
		at := start + int64(i)*90*1_000_000
		b.Facts = append(b.Facts, syncing.DailyFact{UUID: syncing.NewID(), StartUs: at, EndUs: at + 90*1_000_000, Category: &category,
			Source: syncing.HealthKitSource{BundleID: "com.apple.health.synthetic", Name: "Synthetic Watch", Version: "26.1", ProductType: "Watch7,1", OperatingSystemVersion: "26.1",
				Device: &syncing.HealthKitDevice{Manufacturer: "Apple Inc.", Model: "Watch", HardwareVersion: "Watch7,1", SoftwareVersion: "26.1"}},
			Metadata: map[string]any{"HKTimeZone": "Asia/Shanghai", "HKMetadataKeySyncIdentifier": syncing.NewID(), "HKMetadataKeySyncVersion": 1}})
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return raw, b.BatchID
}

func commitDaily(t *testing.T, f *fixture, raw []byte) (json.RawMessage, time.Duration, error) {
	t.Helper()
	ctx := context.Background()
	lease, err := f.s.AcquireUpload(ctx, f.p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.s.ReleaseUpload(ctx, f.p, lease)
	started := time.Now()
	receipt, err := f.s.CommitDaily(ctx, f.p, raw, lease)
	return receipt, time.Since(started), err
}

// A full 500-fact page, and a 500-deletion page (applied row by row), commit
// inside the 5 s transaction budget; replay returns the original receipt.
func TestDailyFullPageCommitsWithinTransactionBudget(t *testing.T) {
	f := setup(t)
	stream, start := syncing.NewID(), time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).UnixMicro()
	raw, batch := dailyPage(t, f, stream, 1, nil, start, syncing.MaxDailyChanges, []string{})
	if len(raw) > syncing.MaxDailyBytes {
		t.Fatalf("a full synthetic page is %d bytes, over the %d byte limit", len(raw), syncing.MaxDailyBytes)
	}
	receipt, elapsed, err := commitDaily(t, f, raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("500 facts: %d bytes, committed in %v", len(raw), elapsed)
	var r syncing.DailyReceipt
	if err = json.Unmarshal(receipt, &r); err != nil || r.IngestStatus != "committed" || r.Counts.Inserted != syncing.MaxDailyChanges {
		t.Fatalf("receipt %s: %v", receipt, err)
	}
	again, _, err := commitDaily(t, f, raw)
	if err != nil || !bytes.Equal(again, receipt) {
		t.Fatalf("replay changed the receipt: %v", err)
	}

	var ids []string
	var page syncing.DailyBatch
	if err = json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	for _, fact := range page.Facts {
		ids = append(ids, fact.UUID)
	}
	deletes, _ := dailyPage(t, f, stream, 2, &batch, start, 0, ids)
	receipt, elapsed, err = commitDaily(t, f, deletes)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("500 deletions: committed in %v", elapsed)
	if err = json.Unmarshal(receipt, &r); err != nil || r.Counts.Deleted != syncing.MaxDailyChanges {
		t.Fatalf("delete receipt %s: %v", receipt, err)
	}
	if n := count(t, f, "entity_tombstones"); n != syncing.MaxDailyChanges {
		t.Fatalf("tombstones %d", n)
	}

	over, _ := dailyPage(t, f, syncing.NewID(), 1, nil, start, syncing.MaxDailyChanges+1, []string{})
	if _, _, err = commitDaily(t, f, over); err == nil {
		t.Fatalf("%d facts accepted", syncing.MaxDailyChanges+1)
	}
	requireCode(t, err, "batch_too_large")
}
