//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"rai/server/internal/syncing"
)

func testRoutePoints(start time.Time, count int, startSequence int64) []syncing.RoutePoint {
	points := make([]syncing.RoutePoint, count)
	for i := range points {
		points[i] = syncing.RoutePoint{
			Sequence: startSequence + int64(i), RecordedAtUs: start.UnixMicro() + int64(i)*1000000,
			Latitude: 31.2304, Longitude: 121.4737,
		}
	}
	return points
}

func testRouteChunk(routeID string, index int, points []syncing.RoutePoint) syncing.RouteChunk {
	canonical, digest := syncing.RoutePointsCanonical(points)
	wire := sha256.Sum256(canonical)
	return syncing.RouteChunk{RouteID: routeID, ChunkIndex: index, Points: points,
		WireSHA256: hex.EncodeToString(wire[:]), PointsSHA256: hex.EncodeToString(digest)}
}

func registerTestRoute(t *testing.T, f *fixture, workoutID, routeID string, start, end time.Time, chunks int, points int64) syncing.RouteState {
	t.Helper()
	state, err := f.s.RegisterRoute(context.Background(), f.p, syncing.RouteManifest{
		ProtocolVersion: syncing.ProtocolVersion, DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch,
		CollectorEpoch: f.enrollment.CollectorEpoch, RouteID: routeID, WorkoutID: workoutID,
		Source: &syncing.Source{BundleID: "synthetic.test"}, ExpectedChunkCount: chunks,
		ExpectedPointCount: points, FirstRecordedAtUs: start.UnixMicro(), LastRecordedAtUs: end.UnixMicro(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func uploadTestRouteChunk(t *testing.T, f *fixture, routeID string, index int, points []syncing.RoutePoint) syncing.RouteChunkReceipt {
	t.Helper()
	ctx := context.Background()
	lease := takeUpload(t, f.s, f.p)
	body, err := f.s.CommitRouteChunk(ctx, f.p, testRouteChunk(routeID, index, points), lease)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.ReleaseUpload(ctx, f.p, lease); err != nil {
		t.Fatal(err)
	}
	var receipt syncing.RouteChunkReceipt
	if err = json.Unmarshal(body, &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func sealTestRoute(t *testing.T, f *fixture, routeID string, chunks int, points int64, manifest, payloadManifest string) syncing.RouteReceipt {
	t.Helper()
	body, err := f.s.SealRoute(context.Background(), f.p, routeID, syncing.RouteSeal{
		ProtocolVersion: syncing.ProtocolVersion, ChunkCount: chunks, TotalPoints: points,
		ManifestSHA256: manifest, PayloadManifestSHA256: payloadManifest,
	})
	if err != nil {
		t.Fatal(err)
	}
	var receipt syncing.RouteReceipt
	if err = json.Unmarshal(body, &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}

func routeStateChains(t *testing.T, f *fixture, routeID string) (string, string) {
	t.Helper()
	state, err := f.s.Route(context.Background(), f.p, routeID)
	if err != nil {
		t.Fatal(err)
	}
	return state.ManifestSHA256, state.PayloadManifestSHA256
}

func workoutAt(start time.Time, seconds int) syncing.Event {
	startUs, endUs := start.UnixMicro(), start.Add(time.Duration(seconds)*time.Second).UnixMicro()
	duration, activity := int64(seconds)*1000000, int64(37)
	return syncing.Event{Op: "upsert", Kind: "workout", Type: syncing.WorkoutType, UUID: syncing.NewID(),
		StartUs: &startUs, EndUs: &endUs, Source: &syncing.Source{BundleID: "synthetic.test"},
		Workout: &syncing.Workout{ActivityType: &activity, DurationUs: &duration}}
}
