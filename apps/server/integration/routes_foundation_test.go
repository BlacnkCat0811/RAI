//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"rai/server/internal/syncing"
)

func TestWorkoutSampleAssociationConstraints(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	s := sample(syncing.NewID(), 120)
	f.commit(t, []syncing.Event{s}, 1, nil)
	workoutStream(t, f)
	w := workout(t, syncing.NewID())
	f.commit(t, []syncing.Event{w}, 1, nil)
	var collector string
	if err := f.s.Pool.QueryRow(ctx, `SELECT ingest_device_id FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2`, f.p.DatasetID, w.UUID).Scan(&collector); err != nil || collector != f.p.DeviceID {
		t.Fatal("workout collector provenance missing", err)
	}
	const insert = `INSERT INTO workout_samples(dataset_id,sample_type,sample_uuid,workout_uuid) VALUES($1,$2,$3,$4)`
	if _, err := f.s.Pool.Exec(ctx, insert, f.p.DatasetID, kind, s.UUID, w.UUID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(ctx, insert, f.p.DatasetID, kind, s.UUID, w.UUID); err == nil {
		t.Fatal("duplicate association accepted")
	}
	if _, err := f.s.Pool.Exec(ctx, insert, f.p.DatasetID, kind, syncing.NewID(), w.UUID); err == nil {
		t.Fatal("association without sample accepted")
	}
	if _, err := f.s.Pool.Exec(ctx, `DELETE FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2`, f.p.DatasetID, w.UUID); err != nil {
		t.Fatal(err)
	}
	if count(t, f, "workout_samples") != 0 || count(t, f, "health_samples") != 1 {
		t.Fatal("association cascade removed or orphaned sample")
	}
}

func TestSyntheticWorkoutDurationsAndRouteBatches(t *testing.T) {
	f := setup(t)
	workoutStream(t, f)
	var previous *string
	for i, seconds := range []int{1200, 2700, 14400} {
		start := time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC)
		startUs, endUs := start.UnixMicro(), start.Add(time.Duration(seconds)*time.Second).UnixMicro()
		duration, activity := int64(seconds)*1000000, int64(37)
		w := syncing.Event{Op: "upsert", Kind: "workout", Type: syncing.WorkoutType, UUID: syncing.NewID(), StartUs: &startUs, EndUs: &endUs,
			Source: &syncing.Source{BundleID: "synthetic.test"}, Workout: &syncing.Workout{ActivityType: &activity, DurationUs: &duration}}
		e, _, _ := f.commit(t, []syncing.Event{w}, int64(i+1), previous)
		previous = &e.BatchID
		route := syncing.NewID()
		chunks := (seconds + syncing.MaxRoutePointsPerChunk - 1) / syncing.MaxRoutePointsPerChunk
		registerTestRoute(t, f, w.UUID, route, start, start.Add(time.Duration(seconds-1)*time.Second), chunks, int64(seconds))
		for chunk, offset := 0, 0; offset < seconds; chunk++ {
			n := min(syncing.MaxRoutePointsPerChunk, seconds-offset)
			receipt := uploadTestRouteChunk(t, f, route, chunk, testRoutePoints(start.Add(time.Duration(offset)*time.Second), n, int64(offset)))
			if chunk == chunks-1 && receipt.CommittedChunks != chunks {
				t.Fatal("last chunk receipt wrong", receipt)
			}
			offset += n
		}
		manifest, payload := routeStateChains(t, f, route)
		sealed := sealTestRoute(t, f, route, chunks, int64(seconds), manifest, payload)
		if sealed.IngestStatus != "sealed" || sealed.TotalPoints != int64(seconds) {
			t.Fatal("route seal failed", sealed)
		}
		var got int
		if err := f.s.Pool.QueryRow(context.Background(), `SELECT count(*) FROM route_points WHERE dataset_id=$1 AND route_uuid=$2`, f.p.DatasetID, route).Scan(&got); err != nil || got != seconds {
			t.Fatalf("duration %d: %d points, %v", seconds, got, err)
		}
	}
}

func TestRouteChunkRetryConflictGapAndSeal(t *testing.T) {
	f := setup(t)
	workoutStream(t, f)
	start := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	w := workoutAt(start, 12)
	f.commit(t, []syncing.Event{w}, 1, nil)
	route := syncing.NewID()
	registerTestRoute(t, f, w.UUID, route, start, start.Add(9*time.Second), 2, 10)
	points := testRoutePoints(start, 5, 0)
	first := uploadTestRouteChunk(t, f, route, 0, points)
	again := uploadTestRouteChunk(t, f, route, 0, points)
	if string(first.ManifestSHA256) != string(again.ManifestSHA256) || again.CommittedChunks != 1 {
		t.Fatal("identical chunk retry changed receipt")
	}
	changed := append([]syncing.RoutePoint(nil), points...)
	changed[0].Latitude = 31.3
	lease := takeUpload(t, f.s, f.p)
	_, err := f.s.CommitRouteChunk(context.Background(), f.p, testRouteChunk(route, 0, changed), lease)
	requireCode(t, err, "route_chunk_conflict")
	if err = f.s.ReleaseUpload(context.Background(), f.p, lease); err != nil {
		t.Fatal(err)
	}
	// A gap is rejected before writing any point.
	gapPoints := testRoutePoints(start.Add(10*time.Second), 1, 10)
	lease = takeUpload(t, f.s, f.p)
	_, err = f.s.CommitRouteChunk(context.Background(), f.p, testRouteChunk(route, 2, gapPoints), lease)
	requireCode(t, err, "route_chunk_gap")
	if err = f.s.ReleaseUpload(context.Background(), f.p, lease); err != nil {
		t.Fatal(err)
	}
	partialManifest, partialPayload := routeStateChains(t, f, route)
	if _, err = f.s.SealRoute(context.Background(), f.p, route, syncing.RouteSeal{
		ProtocolVersion: syncing.ProtocolVersion, ChunkCount: 2, TotalPoints: 10,
		ManifestSHA256: partialManifest, PayloadManifestSHA256: partialPayload,
	}); err == nil {
		t.Fatal("incomplete route sealed")
	} else {
		requireCode(t, err, "route_incomplete")
	}
	second := uploadTestRouteChunk(t, f, route, 1, testRoutePoints(start.Add(5*time.Second), 5, 5))
	if second.CommittedChunks != 2 || second.TotalPoints != 10 {
		t.Fatal("second chunk state wrong", second)
	}
	manifest, payloadManifest := routeStateChains(t, f, route)
	if _, err = f.s.SealRoute(context.Background(), f.p, route, syncing.RouteSeal{
		ProtocolVersion: syncing.ProtocolVersion, ChunkCount: 2, TotalPoints: 10,
		ManifestSHA256:        "0000000000000000000000000000000000000000000000000000000000000000",
		PayloadManifestSHA256: payloadManifest,
	}); err == nil {
		t.Fatal("wrong manifest sealed")
	} else {
		requireCode(t, err, "route_manifest_mismatch")
	}
	sealed := sealTestRoute(t, f, route, 2, 10, manifest, payloadManifest)
	if sealed.IngestStatus != "sealed" || len(sealed.AnalysisJobIDs) != 1 {
		t.Fatal("route seal incomplete", sealed)
	}
	replay := sealTestRoute(t, f, route, 2, 10, manifest, payloadManifest)
	if replay.CommitRevision != sealed.CommitRevision {
		t.Fatal("seal replay changed revision")
	}
	if _, err = f.s.SealRoute(context.Background(), f.p, route, syncing.RouteSeal{
		ProtocolVersion: syncing.ProtocolVersion, ChunkCount: 2, TotalPoints: 9,
		ManifestSHA256: manifest, PayloadManifestSHA256: payloadManifest,
	}); err == nil {
		t.Fatal("different seal declaration accepted")
	} else {
		requireCode(t, err, "route_already_sealed")
	}
	lease = takeUpload(t, f.s, f.p)
	sameAfterSeal, err := f.s.CommitRouteChunk(context.Background(), f.p, testRouteChunk(route, 0, points), lease)
	if err != nil || len(sameAfterSeal) == 0 {
		t.Fatal("exact chunk retry after seal lost receipt", err)
	}
	_, err = f.s.CommitRouteChunk(context.Background(), f.p, testRouteChunk(route, 2, points[:1]), lease)
	requireCode(t, err, "route_already_sealed")
	if err = f.s.ReleaseUpload(context.Background(), f.p, lease); err != nil {
		t.Fatal(err)
	}
	if count(t, f, "route_points") != 10 || count(t, f, "route_chunks") != 2 {
		t.Fatal("failed retries changed route facts")
	}
}

func TestRouteProjectionAndWorkoutCascade(t *testing.T) {
	f := setup(t)
	workoutStream(t, f)
	start := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	w := workoutAt(start, 5000)
	f.commit(t, []syncing.Event{w}, 1, nil)
	route := syncing.NewID()
	registerTestRoute(t, f, w.UUID, route, start, start.Add(4999*time.Second), 1, 5000)
	uploadTestRouteChunk(t, f, route, 0, testRoutePoints(start, 5000, 0))
	if count(t, f, "route_points") != 5000 {
		t.Fatal("projection incomplete")
	}
	if _, err := f.s.Pool.Exec(context.Background(), `DELETE FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2`, f.p.DatasetID, w.UUID); err != nil {
		t.Fatal(err)
	}
	if count(t, f, "workout_routes") != 0 || count(t, f, "route_chunks") != 0 || count(t, f, "route_points") != 0 {
		t.Fatal("route children not cascaded")
	}
}
