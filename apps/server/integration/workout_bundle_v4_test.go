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
// with the raw protocol-4 envelope: one Workout, one stream of per-sample
// objects with inline sources, and no observation or deletion sidecars.
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
	source := `{"bundleID":"com.apple.health","productType":"Watch7,1"}`
	raw := fmt.Sprintf(`{
		"protocolVersion":4,
		"datasetID":%q,"datasetEpoch":%q,"collectorEpoch":%q,"workoutID":%q,
		"startUs":%d,"endUs":%d,
		"source":%s,
		"workout":%s,
		"streams":[{"type":%q,"unit":"count/min","status":"complete","error":null,
			"samples":[{"uuid":%q,"startUs":%d,"endUs":%d,"value":121,"source":%s}]}],
		"series":[],"routes":[]
	}`, f.p.DatasetID, f.enrollment.DatasetEpoch, f.enrollment.CollectorEpoch, workout.UUID,
		startUs, start.Add(time.Minute).UnixMicro(), source, workoutJSON, kind, sampleUUID, startUs, endUs, source)
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
}
