//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	rd "rai/server/internal/readiness"
	rc "rai/server/internal/recovery"
	"rai/server/internal/syncing"
)

func TestModelStackDayPreRunRestingReadingDeletion(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	stream, morning, span := syncing.NewID(), syncing.NewID(), syncing.NewID()
	var first syncing.DailyBatch
	ages := []int{3, 4}
	for k := 10; k <= 23; k++ {
		ages = append(ages, k)
	}
	_, read := runStackDay(t, f, []stackDayRun{{id: syncing.NewID(), day: -1, version: "11.0", hr: steadyHR}}, stackDayOptions{
		maxOnly: true, dailyRHRAges: ages,
		seed: func(today time.Time) {
			start := today.AddDate(0, 0, -90).UnixMicro()
			runDay := today.AddDate(0, 0, -1)
			fact := func(id string, from, to time.Time) syncing.DailyFact {
				return syncing.DailyFact{UUID: id, StartUs: from.UnixMicro(), EndUs: to.UnixMicro(), Quantity: &syncing.Quantity{Value: 50, Unit: "count/min"}, Source: syncing.HealthKitSource{BundleID: "com.apple.health", ProductType: "Watch7,1", Version: "11.0"}}
			}
			first = syncing.DailyBatch{Version: 1, DatasetID: f.enrollment.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch,
				StreamID: stream, BatchID: syncing.NewID(), ClientSeq: 1, SampleType: syncing.DailyRHR, QueryFingerprint: syncing.DailyFingerprint(syncing.DailyRHR, start), BootstrapStartUs: start, ObservedAtUs: time.Now().UnixMicro(),
				Facts: []syncing.DailyFact{fact(morning, runDay.Add(6*time.Hour), runDay.Add(6*time.Hour)), fact(span, runDay, runDay.Add(9*time.Hour))}, DeletedUUIDs: []string{}}
			raw, err := json.Marshal(first)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = commitDaily(t, f, raw); err != nil {
				t.Fatal(err)
			}
		},
	})
	var ready rd.Result
	read("training_readiness", &ready)
	if ready.KnownLoads != 1 || ready.UnknownLoads != 0 {
		t.Fatal("pre-run morning did not establish the recent baseline")
	}
	deleted := first
	deleted.BatchID = syncing.NewID()
	deleted.ClientSeq = 2
	deleted.PreviousBatchID = &first.BatchID
	deleted.Facts = []syncing.DailyFact{}
	deleted.DeletedUUIDs = []string{morning}
	raw, err := json.Marshal(deleted)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = commitDaily(t, f, raw); err != nil {
		t.Fatal(err)
	}
	if processStackJobs(t, f) == 0 {
		t.Fatal("daily deletion did not regenerate the stack")
	}
	read("training_readiness", &ready)
	if ready.KnownLoads != 0 || ready.UnknownLoads != 1 {
		t.Fatal("deleted or post-start reading still supplies the baseline")
	}
	var recovery rc.V4Result
	read("physiological_recovery", &recovery)
	if recovery.Score == nil {
		t.Fatal("a tombstone must not block the whole recovery score")
	}
	var live, tombstones int
	if err = f.s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM health_samples WHERE dataset_id=$1 AND sample_uuid=$2),(SELECT count(*) FROM entity_tombstones WHERE dataset_id=$1 AND sample_uuid=$2)`, f.p.DatasetID, morning).Scan(&live, &tombstones); err != nil || live != 0 || tombstones != 1 {
		t.Fatal("daily deletion was not retained", err)
	}
	// An older upsert of the same UUID cannot revive the deleted observation.
	retry := first
	retry.BatchID = syncing.NewID()
	retry.ClientSeq = 3
	retry.PreviousBatchID = &deleted.BatchID
	retry.Facts = first.Facts[:1]
	raw, err = json.Marshal(retry)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = commitDaily(t, f, raw); err != nil {
		t.Fatal(err)
	}
	if err = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM health_samples WHERE dataset_id=$1 AND sample_uuid=$2`, f.p.DatasetID, morning).Scan(&live); err != nil || live != 0 {
		t.Fatal("old upload revived deleted resting reading", err)
	}
}
