//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"rai/server/internal/storage/postgres"
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
	ctx := context.Background()
	workoutStream(t, f)
	var previous *string
	for i, seconds := range []int{1200, 2700, 14400} {
		start := time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC)
		startUs, endUs := start.UnixMicro(), start.Add(time.Duration(seconds)*time.Second).UnixMicro()
		duration, activity := int64(seconds)*1000000, int64(37)
		w := syncing.Event{Op: "upsert", Kind: "workout", Type: syncing.WorkoutType, UUID: syncing.NewID(), StartUs: &startUs, EndUs: &endUs,
			Source: &syncing.Source{BundleID: "synthetic.test"}, Workout: &syncing.Workout{ActivityType: &activity, DurationUs: &duration}}
		workoutStart := time.Now()
		e, _, _ := f.commit(t, []syncing.Event{w}, int64(i+1), previous)
		if i == 0 {
			t.Logf("synthetic workout complete commit: %s", time.Since(workoutStart))
		}
		previous = &e.BatchID
		route := syncing.NewID()
		for offset, chunk := 0, int32(0); offset < seconds; chunk++ {
			n := min(5000, seconds-offset)
			points := make([]postgres.RoutePoint, n)
			for j := range points {
				points[j] = postgres.RoutePoint{Sequence: int64(offset + j), RecordedAt: start.Add(time.Duration(offset+j) * time.Second)}
			}
			if _, err := f.s.StageRouteChunk(ctx, f.p.DatasetID, w.UUID, route, "synthetic", chunk, points); err != nil {
				t.Fatal(err)
			}
			offset += n
		}
		var got int
		if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM route_points WHERE dataset_id=$1 AND route_uuid=$2`, f.p.DatasetID, route).Scan(&got); err != nil || got != seconds {
			t.Fatalf("duration %d: %d points, %v", seconds, got, err)
		}
	}
}

func TestStageRouteChunkBatchRetryAndRollback(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	workoutStream(t, f)
	w := workout(t, syncing.NewID())
	f.commit(t, []syncing.Event{w}, 1, nil)
	route := syncing.NewID()
	points := make([]postgres.RoutePoint, 5000)
	for i := range points {
		points[i] = postgres.RoutePoint{Sequence: int64(i), RecordedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Second), Latitude: 0, Longitude: 0}
	}
	started := time.Now()
	inserted, err := f.s.StageRouteChunk(ctx, f.p.DatasetID, w.UUID, route, "synthetic", 0, points)
	if err != nil || !inserted {
		t.Fatalf("stage: %t %v", inserted, err)
	}
	t.Logf("synthetic storage 5000-point chunk: %s", time.Since(started))
	inserted, err = f.s.StageRouteChunk(ctx, f.p.DatasetID, w.UUID, route, "synthetic", 0, points)
	if err != nil || inserted {
		t.Fatalf("retry: %t %v", inserted, err)
	}
	points[0].Latitude = 1
	if _, err = f.s.StageRouteChunk(ctx, f.p.DatasetID, w.UUID, route, "synthetic", 0, points); err == nil {
		t.Fatal("different retry accepted")
	}
	if _, err = f.s.StageRouteChunk(ctx, f.p.DatasetID, w.UUID, route, "synthetic", 1, points[:1]); err == nil {
		t.Fatal("duplicate sequence accepted")
	}
	if count(t, f, "route_chunks") != 1 || count(t, f, "route_points") != 5000 {
		t.Fatal("failed insert left partial chunk")
	}
}

func TestRouteFoundationConstraintsAndBulkProjection(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	workoutStream(t, f)
	w := workout(t, syncing.NewID())
	f.commit(t, []syncing.Event{w}, 1, nil)
	route := syncing.NewID()
	if _, err := f.s.Pool.Exec(ctx, `INSERT INTO workout_routes(dataset_id,route_uuid,workout_uuid,source_bundle_id) VALUES($1,$2,$3,'synthetic')`, f.p.DatasetID, route, w.UUID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(ctx, `INSERT INTO workout_routes(dataset_id,route_uuid,workout_uuid,source_bundle_id) VALUES($1,$2,$3,'synthetic')`, f.p.DatasetID, route, w.UUID); err == nil {
		t.Fatal("duplicate route accepted")
	}
	if _, err := f.s.Pool.Exec(ctx, `INSERT INTO workout_routes(dataset_id,route_uuid,workout_uuid,source_bundle_id) VALUES($1,$2,$3,'synthetic')`, f.p.DatasetID, syncing.NewID(), syncing.NewID()); err == nil {
		t.Fatal("route without parent accepted")
	}
	tx, err := f.s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	started := time.Now()
	_, err = tx.Exec(ctx, `INSERT INTO route_chunks(dataset_id,route_uuid,chunk_index,point_count,content_sha256,points)
		SELECT $1,$2,0,5000,decode(repeat('00',32),'hex'),jsonb_agg(jsonb_build_object('sequence',n) ORDER BY n) FROM generate_series(0,4999) n`, f.p.DatasetID, route)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO route_points(dataset_id,route_uuid,chunk_index,sequence,recorded_at,latitude,longitude)
		SELECT $1,$2,0,n,'2026-01-01T00:00:00Z'::timestamptz + n*interval '1 second',0,0 FROM generate_series(0,4999) n`, f.p.DatasetID, route)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("synthetic 5000-point chunk plus projection transaction: %s", time.Since(started))
	if count(t, f, "route_points") != 5000 {
		t.Fatal("projection incomplete")
	}
	for _, q := range []string{
		`INSERT INTO route_points(dataset_id,route_uuid,chunk_index,sequence,recorded_at,latitude,longitude) VALUES($1,$2,0,0,now(),0,0)`,
		`INSERT INTO route_points(dataset_id,route_uuid,chunk_index,sequence,recorded_at,latitude,longitude) VALUES($1,$2,0,5000,now(),91,0)`,
		`INSERT INTO route_points(dataset_id,route_uuid,chunk_index,sequence,recorded_at,latitude,longitude) VALUES($1,$2,1,5000,now(),0,0)`,
	} {
		if _, err = f.s.Pool.Exec(ctx, q, f.p.DatasetID, route); err == nil {
			t.Fatal("invalid route point accepted")
		}
	}
	if _, err = f.s.Pool.Exec(ctx, `DELETE FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2`, f.p.DatasetID, w.UUID); err != nil {
		t.Fatal(err)
	}
	if count(t, f, "workout_routes") != 0 || count(t, f, "route_chunks") != 0 || count(t, f, "route_points") != 0 {
		t.Fatal("route children not cascaded")
	}
}
