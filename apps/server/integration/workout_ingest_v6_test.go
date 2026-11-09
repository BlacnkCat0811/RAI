//go:build integration

package integration

import (
	"bytes"
	"context"
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

func TestWorkoutUUIDIngestV6CommitReplayDelete(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	workoutID := syncing.NewID()
	raw := ingestV6Raw(t, f.p.DatasetID, f.enrollment.DatasetEpoch, f.enrollment.CollectorEpoch, workoutID, 121)

	receipt := acceptIngest(t, f, f.p, raw)
	if receipt.WorkoutID != workoutID || receipt.IngestStatus != "accepted" || receipt.AcceptedAt.IsZero() {
		t.Fatalf("acceptance: %+v", receipt)
	}
	stored, state, attempts, _ := ingestRow(t, f.s, f.p.DatasetID, workoutID)
	if !bytes.Equal(stored, raw) || state != "accepted" || attempts != 0 {
		t.Fatalf("stored raw not exact: state=%s attempts=%d equal=%v", state, attempts, bytes.Equal(stored, raw))
	}

	outcome := processIngest(t, f.s)
	if outcome.Status != "committed" || outcome.WorkoutID != workoutID {
		t.Fatalf("worker outcome: %+v", outcome)
	}
	status := ingestStatus(t, f.s, f.p, workoutID)
	if status.IngestStatus != "committed" || status.AnalysisStatus != "queued" ||
		len(status.AnalysisJobIDs) != 1 || status.CommitRevision == nil {
		t.Fatalf("committed status: %+v", status)
	}
	if count(t, f, "workouts") != 1 || count(t, f, "workout_samples") != 3 {
		t.Fatalf("facts missing: workouts=%d associations=%d", count(t, f, "workouts"), count(t, f, "workout_samples"))
	}

	hr0 := syncing.V6FactUUID(f.p.DatasetID, workoutID, v6HRType, "sample", 0)
	hr1 := syncing.V6FactUUID(f.p.DatasetID, workoutID, v6HRType, "sample", 1)
	parent := syncing.V6FactUUID(f.p.DatasetID, workoutID, v6PowerType, "series", 0)
	routeID := syncing.V6FactUUID(f.p.DatasetID, workoutID, "", "route", 0)
	var generated int
	if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM health_samples
		WHERE dataset_id=$1 AND sample_uuid=ANY($2::uuid[]) AND source_bundle_id='com.apple.health'`,
		f.p.DatasetID, []string{hr0, hr1, parent}).Scan(&generated); err != nil {
		t.Fatal(err)
	}
	if generated != 3 {
		t.Fatalf("deterministic sample UUIDs missing: %d", generated)
	}
	var parentPoints int
	var seriesState string
	if err := f.s.Pool.QueryRow(ctx, `SELECT total_points,state FROM series_uploads
		WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3`,
		f.p.DatasetID, v6PowerType, parent).Scan(&parentPoints, &seriesState); err != nil {
		t.Fatal(err)
	}
	if parentPoints != 2 || seriesState != "sealed" {
		t.Fatalf("series parent: points=%d state=%s", parentPoints, seriesState)
	}
	var routePoints, expectedRoutePoints int
	var routeSource string
	if err := f.s.Pool.QueryRow(ctx, `SELECT r.expected_point_count,r.source_bundle_id,
		(SELECT count(*) FROM route_points p WHERE p.dataset_id=r.dataset_id AND p.route_uuid=r.route_uuid)
		FROM workout_routes r WHERE r.dataset_id=$1 AND r.route_uuid=$2`,
		f.p.DatasetID, routeID).Scan(&expectedRoutePoints, &routeSource, &routePoints); err != nil {
		t.Fatal(err)
	}
	if routePoints != 2 || expectedRoutePoints != 2 || routeSource != "com.apple.health" {
		t.Fatalf("route: points=%d expected=%d source=%s", routePoints, expectedRoutePoints, routeSource)
	}

	// A duplicate POST with different bytes returns the first acceptance and
	// never adds facts or a second job.
	changed := ingestV6Raw(t, f.p.DatasetID, f.enrollment.DatasetEpoch, f.enrollment.CollectorEpoch, workoutID, 199)
	replay := acceptIngest(t, f, f.p, changed)
	if replay.IngestStatus != "accepted" || !replay.AcceptedAt.Equal(receipt.AcceptedAt) {
		t.Fatalf("replay changed acceptance: %+v", replay)
	}
	if ingestRowCount(t, f.s, f.p.DatasetID) != 1 {
		t.Fatal("replay created a second ingest row")
	}
	stored, _, _, _ = ingestRow(t, f.s, f.p.DatasetID, workoutID)
	if !bytes.Equal(stored, raw) {
		t.Fatal("replay replaced the original v6 bytes")
	}
	if count(t, f, "workout_samples") != 3 {
		t.Fatal("replay wrote facts")
	}
	if afterReplay := ingestStatus(t, f.s, f.p, workoutID); len(afterReplay.AnalysisJobIDs) != 1 {
		t.Fatalf("replay added analysis jobs: %+v", afterReplay)
	}

	// Root deletion removes the Workout, its associations and routes, writes the
	// tombstone barrier, and blocks the UUID. Child samples follow the same
	// legacy root-deletion semantics as v3/v4: the rows may remain but are no
	// longer reachable through any Workout.
	if _, err := f.s.DeleteWorkoutBundle(ctx, f.p, bundleDeleteRequest(f, workoutID)); err != nil {
		t.Fatal(err)
	}
	if deleted := ingestStatus(t, f.s, f.p, workoutID); deleted.IngestStatus != "deleted" {
		t.Fatalf("deleted status: %+v", deleted)
	}
	if count(t, f, "workouts") != 0 || count(t, f, "workout_samples") != 0 || count(t, f, "workout_routes") != 0 {
		t.Fatalf("root deletion left reachable facts: workouts=%d associations=%d routes=%d",
			count(t, f, "workouts"), count(t, f, "workout_samples"), count(t, f, "workout_routes"))
	}
	var tombstoned bool
	if err := f.s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM entity_tombstones
		WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3)`,
		f.p.DatasetID, syncing.WorkoutType, workoutID).Scan(&tombstoned); err != nil {
		t.Fatal(err)
	}
	if !tombstoned {
		t.Fatal("root deletion did not write the UUID barrier")
	}
	if _, err := f.s.AcceptWorkoutIngest(ctx, f.p, ingestEnvelope(t, raw), raw); err == nil {
		t.Fatal("deleted UUID accepted again")
	} else {
		requireSyncCode(t, err, "workout_deleted")
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

func TestWorkoutUUIDIngestV6CannotUseCompactEntry(t *testing.T) {
	f := setup(t)
	raw := ingestV6Raw(t, f.p.DatasetID, f.enrollment.DatasetEpoch, f.enrollment.CollectorEpoch, syncing.NewID(), 121)
	bundle, err := syncing.DecodeWorkoutBundleV6JSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ImportWorkoutBundle(context.Background(), f.p, bundle, raw); err != nil {
		t.Fatal(err)
	}
	if count(t, f, "workout_samples") != 3 {
		t.Fatal("direct v6 import did not write facts")
	}
	// The same Workout UUID is now already imported; UUID ingest returns that
	// terminal answer without creating a row.
	receipt := acceptIngest(t, f, f.p, raw)
	if receipt.IngestStatus != "already_imported" {
		t.Fatalf("already imported v6: %+v", receipt)
	}
	if ingestRowCount(t, f.s, f.p.DatasetID) != 0 {
		t.Fatal("already imported v6 created an ingest row")
	}
}
