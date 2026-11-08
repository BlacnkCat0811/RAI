//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"rai/server/internal/syncing"
)

// TestWorkoutBundleV4ImportAndIdempotency exercises the production store path
// with the compact protocol-4 envelope: one Workout, one source table, one
// columnar stream, no per-sample objects and no 200-day completeness proof.
func TestWorkoutBundleV4ImportAndIdempotency(t *testing.T) {
	f := setup(t)
	start := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	workout := workoutAt(start, 60)
	workoutJSON, err := json.Marshal(workout.Workout)
	if err != nil {
		t.Fatal(err)
	}
	sampleUUID := syncing.NewID()
	startUs := start.UnixMicro()
	endUs := start.Add(500 * time.Millisecond).UnixMicro()
	raw := fmt.Sprintf(`{
		"protocolVersion":4,"metadataRegistryVersion":2,
		"datasetID":%q,"datasetEpoch":%q,"collectorEpoch":%q,"workoutID":%q,
		"startUs":%d,"endUs":%d,
		"workout":%s,
		"sources":[{"bundleID":"com.apple.health","productType":"Watch7,1"}],
		"streams":[{"type":%q,"kind":"quantity","unit":"count/min",
			"samples":[[%d,%d,121]],"uuids":[%q],"source":0,"src":[0]}],
		"series":[],"routes":[],"deletions":[],
		"observation":{"version":1,"observedAtUs":%d,"metadata":{},
			"provenance":{"workout":{"bundleID":"com.apple.health","productType":"Watch7,1"},"samples":[],"routes":[]},
			"coverage":{"workout":{"state":"complete","sampleCount":1,"queryCompleted":true,"localPersisted":true},
				%q:{"state":"complete","sampleCount":1,"queryCompleted":true,"localPersisted":true}}}
	}`, f.p.DatasetID, f.enrollment.DatasetEpoch, f.enrollment.CollectorEpoch, workout.UUID,
		startUs, start.Add(time.Minute).UnixMicro(), workoutJSON, kind, startUs, endUs, sampleUUID,
		time.Now().UnixMicro(), kind)
	bundle, err := syncing.DecodeWorkoutBundleV4JSON([]byte(raw))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	body, err := f.s.ImportWorkoutBundle(context.Background(), f.p, bundle, []byte(raw))
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	var receipt syncing.WorkoutImportReceipt
	if err := json.Unmarshal(body, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.IngestStatus != "committed" || receipt.Counts.SamplesInserted != 1 || receipt.Counts.AssociationsInserted != 1 {
		t.Fatalf("unexpected v4 receipt: %+v", receipt)
	}
	if got := count(t, f, "health_samples"); got != 1 {
		t.Fatalf("samples=%d", got)
	}
	if got := count(t, f, "workout_samples"); got != 1 {
		t.Fatalf("associations=%d", got)
	}
	// The exact same bytes are idempotent and return the original receipt.
	again, err := f.s.ImportWorkoutBundle(context.Background(), f.p, bundle, []byte(raw))
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if string(again) != string(body) {
		t.Fatalf("retry receipt changed: %s != %s", again, body)
	}
	if got := count(t, f, "health_samples"); got != 1 {
		t.Fatalf("retry samples=%d", got)
	}
	// A deletion must carry complete read coverage for the child type. A
	// successful query that returned zero rows is coverage, not deletion
	// authority, even when the bundle otherwise has a valid Workout barrier.
	rawDeletion := fmt.Sprintf(`{
		"protocolVersion":4,"metadataRegistryVersion":2,
		"datasetID":%q,"datasetEpoch":%q,"collectorEpoch":%q,"workoutID":%q,
		"startUs":%d,"endUs":%d,
		"workout":%s,
		"sources":[{"bundleID":"com.apple.health","productType":"Watch7,1"}],
		"streams":[],"series":[],"routes":[],
		"deletions":[{"type":%q,"uuid":%q}],
		"observation":{"version":1,"observedAtUs":%d,"metadata":{},
			"provenance":{"workout":{"bundleID":"com.apple.health","productType":"Watch7,1"},"samples":[],"routes":[]},
			"coverage":{"workout":{"state":"complete","sampleCount":1,"queryCompleted":true,"localPersisted":true},
				%q:{"state":"no_records_observed","sampleCount":0,"queryCompleted":true,"localPersisted":true}}}
	}`, f.p.DatasetID, f.enrollment.DatasetEpoch, f.enrollment.CollectorEpoch, workout.UUID,
		startUs, start.Add(time.Minute).UnixMicro(), workoutJSON, kind, sampleUUID, time.Now().Add(time.Second).UnixMicro(), kind)
	bad, err := syncing.DecodeWorkoutBundleV4JSON([]byte(rawDeletion))
	if err != nil {
		t.Fatalf("decode deletion bundle: %v", err)
	}
	if _, err = f.s.ImportWorkoutBundle(context.Background(), f.p, bad, []byte(rawDeletion)); err == nil {
		t.Fatal("deletion without complete coverage was accepted")
	}
	if got := count(t, f, "health_samples"); got != 1 {
		t.Fatalf("deletion without coverage changed samples=%d", got)
	}
}
