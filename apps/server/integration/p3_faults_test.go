//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"rai/server/internal/syncing"
)

func failTableWrite(t *testing.T, f *fixture, table, operation, function string) {
	t.Helper()
	ctx := context.Background()
	_, err := f.s.Pool.Exec(ctx, `CREATE FUNCTION `+function+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic p3 failure'; END $$;
		CREATE TRIGGER `+function+` BEFORE `+operation+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION `+function+`()`)
	if err != nil {
		t.Fatal(err)
	}
}
func dropTableWrite(t *testing.T, f *fixture, table, function string) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.s.Pool.Exec(ctx, `DROP TRIGGER `+function+` ON `+table+`; DROP FUNCTION `+function+`()`); err != nil {
		t.Fatal(err)
	}
}

func TestRouteSealJobFailureRollsBackPublication(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	workoutStream(t, f)
	start := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	w := workoutAt(start, 10)
	f.commit(t, []syncing.Event{w}, 1, nil)
	route := syncing.NewID()
	registerTestRoute(t, f, w.UUID, route, start, start.Add(9*time.Second), 1, 10)
	uploadTestRouteChunk(t, f, route, 0, testRoutePoints(start, 10, 0))
	manifest, payload := routeStateChains(t, f, route)
	rev, _ := f.s.Metadata(ctx, f.p)
	beforeChange := count(t, f, "change_events")
	beforeJobs := count(t, f, "jobs")
	failTableWrite(t, f, "jobs", "INSERT", "fail_p3_route_seal_job")
	if _, err := f.s.SealRoute(ctx, f.p, route, syncing.RouteSeal{
		ProtocolVersion: syncing.ProtocolVersion, ChunkCount: 1, TotalPoints: 10,
		ManifestSHA256: manifest, PayloadManifestSHA256: payload,
	}); err == nil {
		t.Fatal("injected seal job failure succeeded")
	}
	state, err := f.s.Route(ctx, f.p, route)
	if err != nil || state.IngestStatus != "pending" || state.Receipt != nil {
		t.Fatal("partial route publication survived rollback", state, err)
	}
	after, _ := f.s.Metadata(ctx, f.p)
	if after.CommitRevision != rev.CommitRevision || count(t, f, "jobs") != beforeJobs || count(t, f, "change_events") != beforeChange {
		t.Fatal("failed seal advanced revision/change/job")
	}
	dropTableWrite(t, f, "jobs", "fail_p3_route_seal_job")
	if _, err = f.s.SealRoute(ctx, f.p, route, syncing.RouteSeal{
		ProtocolVersion: syncing.ProtocolVersion, ChunkCount: 1, TotalPoints: 10,
		ManifestSHA256: manifest, PayloadManifestSHA256: payload,
	}); err != nil {
		t.Fatal("seal retry failed", err)
	}
}

func TestRouteDeleteReceiptFailureRollsBackBarrier(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	workoutStream(t, f)
	start := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	w := workoutAt(start, 10)
	f.commit(t, []syncing.Event{w}, 1, nil)
	route := syncing.NewID()
	registerTestRoute(t, f, w.UUID, route, start, start.Add(9*time.Second), 1, 10)
	uploadTestRouteChunk(t, f, route, 0, testRoutePoints(start, 10, 0))
	manifest, payload := routeStateChains(t, f, route)
	sealTestRoute(t, f, route, 1, 10, manifest, payload)
	failTableWrite(t, f, "route_deletions", "INSERT", "fail_p3_route_delete")
	req := syncing.RouteDeleteRequest{ProtocolVersion: syncing.ProtocolVersion, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch}
	if _, err := f.s.DeleteRoute(ctx, f.p, route, req); err == nil {
		t.Fatal("injected route delete receipt failure succeeded")
	}
	if count(t, f, "workout_routes") != 1 || count(t, f, "route_deletions") != 0 || embeddedRouteTombstone(t, f, route) {
		t.Fatal("failed route delete did not roll back route/tombstone/receipt")
	}
	dropTableWrite(t, f, "route_deletions", "fail_p3_route_delete")
	if _, err := f.s.DeleteRoute(ctx, f.p, route, req); err != nil {
		t.Fatal("route delete retry failed", err)
	}
}

func TestAssociationAndEnrichmentJobFailureAtomicity(t *testing.T) {
	// The per-operation versions below use the real *testing.T and are split so
	// helper failures report against the right subtest.
	t.Run("association", func(t *testing.T) {
		f := setup(t)
		ctx := context.Background()
		s := sample(syncing.NewID(), 132)
		f.commit(t, []syncing.Event{s}, 1, nil)
		workoutStream(t, f)
		start := time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC)
		w := workoutAt(start, 60)
		f.commit(t, []syncing.Event{w}, 1, nil)
		beforeRev, _ := f.s.Metadata(ctx, f.p)
		failTableWrite(t, f, "jobs", "INSERT", "fail_p3_assoc_job")
		_, err := f.s.AssociateSample(ctx, f.p, syncing.AssociationRequest{
			ProtocolVersion: syncing.ProtocolVersion, DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch,
			CollectorEpoch: f.enrollment.CollectorEpoch, AssociationID: syncing.NewID(), SampleType: kind,
			SampleUUID: s.UUID, WorkoutUUID: w.UUID, Source: &syncing.Source{BundleID: "synthetic.test"},
		})
		if err == nil {
			t.Fatal("injected association job failure succeeded")
		}
		if count(t, f, "workout_samples") != 0 {
			t.Fatal("association survived job failure")
		}
		after, _ := f.s.Metadata(ctx, f.p)
		if after.CommitRevision != beforeRev.CommitRevision {
			t.Fatal("failed association advanced revision")
		}
		dropTableWrite(t, f, "jobs", "fail_p3_assoc_job")
	})
	t.Run("enrichment", func(t *testing.T) {
		f := setup(t)
		ctx := context.Background()
		workoutStream(t, f)
		start := time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC)
		w := workoutAt(start, 60)
		f.commit(t, []syncing.Event{w}, 1, nil)
		beforeRev, _ := f.s.Metadata(ctx, f.p)
		value := 1000.0
		req := syncing.WorkoutEnrichmentRequest{
			ProtocolVersion: syncing.ProtocolVersion, DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch,
			CollectorEpoch: f.enrollment.CollectorEpoch, EnrichmentID: syncing.NewID(), WorkoutID: w.UUID,
			Source: &syncing.Source{BundleID: "synthetic.test"}, SourceVersion: 1,
			ObservedAtUs: start.UnixMicro() + 1000, TotalDistanceM: &value,
		}
		failTableWrite(t, f, "jobs", "INSERT", "fail_p3_enrich_job")
		if _, err := f.s.EnrichWorkout(ctx, f.p, req); err == nil {
			t.Fatal("injected enrichment job failure succeeded")
		}
		if count(t, f, "workout_enrichments") != 0 {
			t.Fatal("enrichment survived job failure")
		}
		after, _ := f.s.Metadata(ctx, f.p)
		if after.CommitRevision != beforeRev.CommitRevision {
			t.Fatal("failed enrichment advanced revision")
		}
		dropTableWrite(t, f, "jobs", "fail_p3_enrich_job")
	})
}
