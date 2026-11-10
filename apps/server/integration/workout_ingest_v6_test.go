//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"rai/server/internal/syncing"
)

const (
	v6HRType    = "HKQuantityTypeIdentifierHeartRate"
	v6PowerType = "HKQuantityTypeIdentifierRunningPower"
)

// ingestV6Raw is the exact raw protocol-6 bytes a Watch client uploads: no
// per-sample UUID, metadata or source, one series group and one route.
func ingestV6Raw(t *testing.T, datasetID, datasetEpoch, collectorEpoch, workoutID string, value float64) []byte {
	t.Helper()
	start := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC).UnixMicro()
	source := `{"bundleID":"com.apple.health","version":"11.0","productType":"Watch7,1"}`
	return []byte(fmt.Sprintf(`{
		"protocolVersion":6,
		"datasetID":%q,"datasetEpoch":%q,"collectorEpoch":%q,"workoutID":%q,
		"startUs":%d,"endUs":%d,
		"source":%s,
		"metadata":{"HKTimeZone":"Asia/Shanghai"},
		"workout":{"activityType":37,"durationUs":60000000},
		"streams":[
			{"type":%q,"unit":"count/min","status":"complete","samples":[[0,30000000,%g],[30000000,60000000,%g]]},
			{"type":%q,"unit":"W","series":[{"startDeltaUs":0,"endDeltaUs":60000000,"value":192.5,
				"points":[[0,30000000,190],[30000000,60000000,191]]}]}
		],
		"routes":[{"points":[[0,31.2304,121.4737,null,null,null,null,null],[1000000,31.2305,121.4738,null,null,null,null,null]]}]
	}`, datasetID, datasetEpoch, collectorEpoch, workoutID, start, start+60_000_000, source,
		v6HRType, value, value+1, v6PowerType))
}

func TestWorkoutUUIDIngestV6ActivitiesWithoutClientUUIDs(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	workoutID := syncing.NewID()
	var payload map[string]any
	if err := json.Unmarshal(ingestV6Raw(t, f.p.DatasetID, f.enrollment.DatasetEpoch,
		f.enrollment.CollectorEpoch, workoutID, 121), &payload); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC).UnixMicro()
	activities := []any{nil}
	for index := 0; index < 2; index++ {
		activities = append(activities, map[string]any{
			"startUs": start + int64(index)*30_000_000, "endUs": start + int64(index+1)*30_000_000,
			"durationUs":    30_000_000,
			"configuration": map[string]any{"activityType": 37, "locationType": 3, "swimmingLocationType": 0},
		})
	}
	payload["workout"].(map[string]any)["activities"] = activities
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"uuid"`)) || bytes.Count(raw, []byte(workoutID)) != 1 {
		t.Fatal("fixture must carry only the top-level HealthKit Workout UUID")
	}
	receipt := acceptIngest(t, f, f.p, raw)
	if outcome := processIngest(t, f.s); outcome.Status != "committed" {
		t.Fatalf("worker outcome: %+v", outcome)
	}
	for index := 0; index < 2; index++ {
		var id string
		var duration int64
		if err := f.s.Pool.QueryRow(ctx, `SELECT activity_uuid::text,duration_us FROM workout_activities
			WHERE dataset_id=$1 AND workout_uuid=$2 AND ordinal=$3`, f.p.DatasetID, workoutID, index).
			Scan(&id, &duration); err != nil {
			t.Fatal(err)
		}
		if id != syncing.V6FactUUID(f.p.DatasetID, workoutID, "", "activity", index+1) || duration != 30_000_000 {
			t.Fatalf("activity %d lost identity or duration", index)
		}
	}
	replay := acceptIngest(t, f, f.p, raw)
	if !replay.AcceptedAt.Equal(receipt.AcceptedAt) || count(t, f, "workout_activities") != 2 {
		t.Fatal("duplicate upload changed the receipt or added activities")
	}
	stored, state, _, _ := ingestRow(t, f.s, f.p.DatasetID, workoutID)
	if state != "committed" || !bytes.Equal(stored, raw) {
		t.Fatal("projection changed the original bytes or commit state")
	}
}

func TestWorkoutUUIDIngestV6RejectsBodyMismatch(t *testing.T) {
	f := setup(t)
	workoutID := syncing.NewID()
	raw := ingestV6Raw(t, f.p.DatasetID, f.enrollment.DatasetEpoch, f.enrollment.CollectorEpoch, workoutID, 121)
	envelope := ingestEnvelope(t, raw)
	if envelope.ProtocolVersion != 6 {
		t.Fatalf("v6 envelope: %+v", envelope)
	}
	envelope.WorkoutID = syncing.NewID()
	if _, err := f.s.AcceptWorkoutIngest(context.Background(), f.p, envelope, raw); err != nil {
		t.Fatal(err)
	}
	outcomes := processAllIngests(t, f.s)
	if len(outcomes) != 1 || outcomes[0].Status != "rejected" || outcomes[0].ErrorCode != "workout_ingest_mismatch" {
		t.Fatalf("mismatched outcome: %+v", outcomes)
	}
	var workouts int
	if err := f.s.Pool.QueryRow(context.Background(), `SELECT count(*) FROM workouts WHERE dataset_id=$1`, f.p.DatasetID).Scan(&workouts); err != nil {
		t.Fatal(err)
	}
	if workouts != 0 {
		t.Fatal("mismatched v6 raw wrote a workout")
	}
}

func TestWorkoutUUIDIngestV6StrictPayloadRejected(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	bad := []byte(`{"protocolVersion":6,"datasetID":"` + f.p.DatasetID + `"}`)
	envelope := syncing.WorkoutIngestEnvelope{DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch,
		CollectorEpoch: f.enrollment.CollectorEpoch, WorkoutID: syncing.NewID(), ProtocolVersion: 6}
	if _, err := f.s.AcceptWorkoutIngest(ctx, f.p, envelope, bad); err != nil {
		t.Fatal(err)
	}
	outcomes := processAllIngests(t, f.s)
	if len(outcomes) != 1 || outcomes[0].Status != "rejected" || outcomes[0].ErrorCode != "unsupported_payload" {
		t.Fatalf("invalid v6 outcome: %+v", outcomes)
	}
	var raw []byte
	if err := f.s.Pool.QueryRow(ctx, `SELECT raw_json FROM workout_ingests WHERE dataset_id=$1 AND workout_uuid=$2`,
		f.p.DatasetID, envelope.WorkoutID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, bad) {
		t.Fatal("rejected v6 raw bytes were not retained")
	}
}
