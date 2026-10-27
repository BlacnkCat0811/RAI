//go:build integration

package integration

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"rai/server/internal/admin"
	"rai/server/internal/httpapi"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
	"rai/server/migrations"
)

func TestLateSampleWorkoutAssociationIdempotencyAndConflict(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	sampleStream := f.stream
	s := sample(syncing.NewID(), 132)
	sampleBatch, _, _ := f.commit(t, []syncing.Event{s}, 1, nil)
	workoutStream(t, f)
	w := workoutAt(time.UnixMicro(*s.StartUs).UTC(), 60)
	workoutBatch, _, _ := f.commit(t, []syncing.Event{w}, 1, nil)
	request := syncing.AssociationRequest{
		ProtocolVersion: syncing.ProtocolVersion, DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch,
		CollectorEpoch: f.enrollment.CollectorEpoch, AssociationID: syncing.NewID(), SampleType: kind,
		SampleUUID: s.UUID, WorkoutUUID: w.UUID, Source: &syncing.Source{BundleID: "synthetic.test"},
	}
	first, err := f.s.AssociateSample(ctx, f.p, request)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.s.AssociateSample(ctx, f.p, request)
	if err != nil || !bytes.Equal(first, again) {
		t.Fatal("association retry changed receipt", err)
	}
	if count(t, f, "workout_samples") != 1 || count(t, f, "jobs") != 3 {
		t.Fatalf("association duplicates or jobs wrong: samples=%d jobs=%d", count(t, f, "workout_samples"), count(t, f, "jobs"))
	}
	var receipt syncing.AssociationReceipt
	if err = json.Unmarshal(first, &receipt); err != nil || receipt.IngestStatus != "committed" {
		t.Fatal("association receipt invalid", err)
	}
	// A second Workout must not silently steal the sample.
	other := workoutAt(time.UnixMicro(*s.StartUs).UTC(), 60)
	workoutBatch2, _, _ := f.commit(t, []syncing.Event{other}, 2, &workoutBatch.BatchID)
	if _, err = f.s.AssociateSample(ctx, f.p, syncing.AssociationRequest{
		ProtocolVersion: syncing.ProtocolVersion, DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch,
		CollectorEpoch: f.enrollment.CollectorEpoch, AssociationID: syncing.NewID(), SampleType: kind,
		SampleUUID: s.UUID, WorkoutUUID: other.UUID, Source: &syncing.Source{BundleID: "synthetic.test"},
	}); err == nil {
		t.Fatal("sample rebound to another workout")
	} else {
		requireCode(t, err, "sample_association_conflict")
	}
	// Delete the sample, then replay the old association: barrier wins.
	f.stream = sampleStream
	_, _, _ = f.commit(t, []syncing.Event{deleted(s.UUID)}, 2, &sampleBatch.BatchID)
	if _, err = f.s.AssociateSample(ctx, f.p, request); err == nil {
		t.Fatal("deleted sample was reassociated")
	} else {
		requireCode(t, err, "association_unavailable")
	}
	_ = workoutBatch2
}

func TestWorkoutEnrichmentVersioningStaleAndConflict(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	workoutStream(t, f)
	start := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	w := workoutAt(start, 600)
	e, _, _ := f.commit(t, []syncing.Event{w}, 1, nil)
	base := syncing.WorkoutEnrichmentRequest{
		ProtocolVersion: syncing.ProtocolVersion, DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch,
		CollectorEpoch: f.enrollment.CollectorEpoch, EnrichmentID: syncing.NewID(), WorkoutID: w.UUID,
		Source: &syncing.Source{BundleID: "com.example.source"}, SourceVersion: 1,
		ObservedAtUs: start.UnixMicro() + 1000000,
		Metadata:     map[string]any{"weather": "synthetic"},
	}
	first, err := f.s.EnrichWorkout(ctx, f.p, base)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.s.EnrichWorkout(ctx, f.p, base)
	if err != nil || !bytes.Equal(first, again) {
		t.Fatal("enrichment retry changed receipt", err)
	}
	changed := base
	changed.Metadata = map[string]any{"weather": "different"}
	changed.EnrichmentID = syncing.NewID()
	if _, err = f.s.EnrichWorkout(ctx, f.p, changed); err == nil {
		t.Fatal("stale source version accepted")
	} else {
		requireCode(t, err, "workout_enrichment_stale")
	}
	next := base
	next.EnrichmentID = syncing.NewID()
	next.SourceVersion = 2
	next.ObservedAtUs += 1000000
	value := 420.5
	next.TotalEnergyKcal = &value
	next.Metadata = map[string]any{"temperatureC": 19.5}
	if _, err = f.s.EnrichWorkout(ctx, f.p, next); err != nil {
		t.Fatal("valid enrichment version 2 rejected", err)
	}
	conflict := next
	conflict.EnrichmentID = syncing.NewID()
	conflict.SourceVersion = 3
	conflict.ObservedAtUs += 1000000
	other := 421.0
	conflict.TotalEnergyKcal = &other
	if _, err = f.s.EnrichWorkout(ctx, f.p, conflict); err == nil {
		t.Fatal("enrichment overwrote newer value")
	} else {
		requireCode(t, err, "workout_enrichment_conflict")
	}
	older := base
	older.EnrichmentID = syncing.NewID()
	older.SourceVersion = 4
	older.ObservedAtUs = start.UnixMicro()
	if _, err = f.s.EnrichWorkout(ctx, f.p, older); err == nil {
		t.Fatal("older observation accepted")
	} else {
		requireCode(t, err, "workout_enrichment_stale")
	}
	if count(t, f, "workout_enrichments") != 2 {
		t.Fatal("conflict/stale enrichment wrote rows")
	}
	// Workout deletion cascades supplements and permanently blocks replay.
	_, _, _ = f.commit(t, []syncing.Event{workoutDelete(w.UUID)}, 2, &e.BatchID)
	afterDelete := base
	afterDelete.EnrichmentID = syncing.NewID()
	afterDelete.SourceVersion = 5
	afterDelete.ObservedAtUs += 2000000
	if _, err = f.s.EnrichWorkout(ctx, f.p, afterDelete); err == nil {
		t.Fatal("deleted workout accepted enrichment")
	} else {
		requireCode(t, err, "workout_unavailable")
	}
}

func TestRouteDeleteAndStaleReplayBarrier(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	workoutStream(t, f)
	start := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	w := workoutAt(start, 100)
	_, _, _ = f.commit(t, []syncing.Event{w}, 1, nil)
	route := syncing.NewID()
	registerTestRoute(t, f, w.UUID, route, start, start.Add(9*time.Second), 1, 10)
	uploadTestRouteChunk(t, f, route, 0, testRoutePoints(start, 10, 0))
	manifest, payloadManifest := routeStateChains(t, f, route)
	sealed := sealTestRoute(t, f, route, 1, 10, manifest, payloadManifest)
	request := syncing.RouteDeleteRequest{ProtocolVersion: syncing.ProtocolVersion, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch, Reason: "healthkit_deleted"}
	first, err := f.s.DeleteRoute(ctx, f.p, route, request)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.s.DeleteRoute(ctx, f.p, route, request)
	if err != nil || !bytes.Equal(first, again) {
		t.Fatal("route delete retry changed receipt", err)
	}
	if count(t, f, "workout_routes") != 0 || count(t, f, "route_chunks") != 0 || count(t, f, "route_points") != 0 {
		t.Fatal("route delete left staging")
	}
	var deleted syncing.RouteDeleteReceipt
	if err = json.Unmarshal(first, &deleted); err != nil || !deleted.RouteExisted || deleted.IngestStatus != "deleted" {
		t.Fatal("route delete receipt invalid", err)
	}
	if embeddedRouteTombstone(t, f, route) != true {
		t.Fatal("route tombstone missing")
	}
	// Register, chunk and seal replay cannot resurrect the deleted route.
	if _, err = f.s.RegisterRoute(ctx, f.p, syncing.RouteManifest{
		ProtocolVersion: syncing.ProtocolVersion, DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch,
		CollectorEpoch: f.enrollment.CollectorEpoch, RouteID: route, WorkoutID: w.UUID,
		Source: &syncing.Source{BundleID: "synthetic.test"}, ExpectedChunkCount: 1, ExpectedPointCount: 10,
		FirstRecordedAtUs: start.UnixMicro(), LastRecordedAtUs: start.Add(9 * time.Second).UnixMicro(),
	}); err == nil {
		t.Fatal("deleted route re-registered")
	} else {
		requireCode(t, err, "route_unavailable")
	}
	lease := takeUpload(t, f.s, f.p)
	_, err = f.s.CommitRouteChunk(ctx, f.p, testRouteChunk(route, 0, testRoutePoints(start, 10, 0)), lease)
	requireCode(t, err, "route_not_found")
	_ = f.s.ReleaseUpload(ctx, f.p, lease)
	if sealed.CommitRevision == 0 {
		t.Fatal("sealed route revision missing")
	}
}

func embeddedRouteTombstone(t *testing.T, f *fixture, routeID string) bool {
	t.Helper()
	var exists bool
	if err := f.s.Pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM entity_tombstones WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3)`,
		f.p.DatasetID, syncing.RouteType, routeID).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func TestP3HTTPRoutesAssociationsEnrichmentAndErrors(t *testing.T) {
	f := setup(t)
	sampleStream := f.stream
	s := sample(syncing.NewID(), 132)
	sampleBatch, _, _ := f.commit(t, []syncing.Event{s}, 1, nil)
	workoutStream(t, f)
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	w := workoutAt(start, 90)
	f.commit(t, []syncing.Event{w}, 1, nil)
	server := httptest.NewServer(httpapi.New(f.s, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer server.Close()
	request := func(method, path string, body []byte, token, contentType, encoding string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if encoding != "" {
			req.Header.Set("Content-Encoding", encoding)
		}
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, out
	}
	status, body := request("GET", "/v2/handshake", nil, f.enrollment.Token, "", "")
	if status != 200 || !bytes.Contains(body, []byte(`"routes":true`)) || !bytes.Contains(body, []byte(`"workoutEnrichment":true`)) {
		t.Fatal("handshake missing P3 capabilities", status, string(body))
	}
	association := syncing.AssociationRequest{
		ProtocolVersion: syncing.ProtocolVersion, DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch,
		CollectorEpoch: f.enrollment.CollectorEpoch, AssociationID: syncing.NewID(), SampleType: kind,
		SampleUUID: s.UUID, WorkoutUUID: w.UUID, Source: &syncing.Source{BundleID: "synthetic.test"},
	}
	associationBody, _ := json.Marshal(association)
	status, body = request("POST", "/v2/sync/associations", associationBody, f.enrollment.Token, "application/json", "")
	if status != 200 || !bytes.Contains(body, []byte(`"ingestStatus":"committed"`)) {
		t.Fatal("association HTTP failed", status, string(body))
	}
	status, body = request("POST", "/v2/sync/associations", associationBody, f.enrollment.Token, "application/json", "")
	if status != 200 || !bytes.Contains(body, []byte(`"associationID"`)) {
		t.Fatal("association HTTP replay failed", status, string(body))
	}
	enrichment := syncing.WorkoutEnrichmentRequest{
		ProtocolVersion: syncing.ProtocolVersion, DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch,
		CollectorEpoch: f.enrollment.CollectorEpoch, EnrichmentID: syncing.NewID(), WorkoutID: w.UUID,
		Source: &syncing.Source{BundleID: "synthetic.source"}, SourceVersion: 1,
		ObservedAtUs: start.UnixMicro() + 1000000, Metadata: map[string]any{"synthetic": true},
	}
	enrichmentBody, _ := json.Marshal(enrichment)
	status, body = request("POST", "/v2/sync/workout-enrichments", enrichmentBody, f.enrollment.Token, "application/json", "")
	if status != 200 {
		t.Fatal("enrichment HTTP failed", status, string(body))
	}
	var enrichmentReceipt syncing.WorkoutEnrichmentReceipt
	if err := json.Unmarshal(body, &enrichmentReceipt); err != nil || enrichmentReceipt.IngestStatus != "committed" {
		t.Fatal("enrichment receipt invalid", err)
	}
	status, body = request("GET", "/v2/sync/workout-enrichments/"+enrichmentReceipt.EnrichmentID, nil, f.enrollment.Token, "", "")
	if status != 200 {
		t.Fatal("enrichment GET failed", status, string(body))
	}
	routeID := syncing.NewID()
	manifest := syncing.RouteManifest{
		ProtocolVersion: syncing.ProtocolVersion, DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch,
		CollectorEpoch: f.enrollment.CollectorEpoch, RouteID: routeID, WorkoutID: w.UUID,
		Source: &syncing.Source{BundleID: "synthetic.test"}, ExpectedChunkCount: 1, ExpectedPointCount: 10,
		FirstRecordedAtUs: start.UnixMicro(), LastRecordedAtUs: start.Add(9 * time.Second).UnixMicro(),
	}
	manifestBody, _ := json.Marshal(manifest)
	status, body = request("POST", "/v2/routes", manifestBody, f.enrollment.Token, "application/json", "")
	if status != 200 {
		t.Fatal("route register HTTP failed", status, string(body))
	}
	var points []syncing.RoutePoint
	for i := 0; i < 10; i++ {
		points = append(points, syncing.RoutePoint{Sequence: int64(i), RecordedAtUs: start.UnixMicro() + int64(i)*1000000, Latitude: 31.2, Longitude: 121.4})
	}
	var decoded bytes.Buffer
	for _, point := range points {
		_ = json.NewEncoder(&decoded).Encode(point)
	}
	var wire bytes.Buffer
	gz := gzip.NewWriter(&wire)
	_, _ = gz.Write(decoded.Bytes())
	_ = gz.Close()
	status, body = request("PUT", "/v2/routes/"+routeID+"/chunks/0", wire.Bytes(), f.enrollment.Token, "application/x-rai-health-route+ndjson", "gzip")
	if status != 200 {
		t.Fatal("route chunk HTTP failed", status, string(body))
	}
	var chunkReceipt syncing.RouteChunkReceipt
	if err := json.Unmarshal(body, &chunkReceipt); err != nil {
		t.Fatal(err)
	}
	status, body = request("POST", "/v2/routes/"+routeID+"/seal", p3JSON(t, syncing.RouteSeal{
		ProtocolVersion: syncing.ProtocolVersion, ChunkCount: 1, TotalPoints: 10,
		ManifestSHA256: chunkReceipt.ManifestSHA256, PayloadManifestSHA256: chunkReceipt.PayloadManifestSHA256,
	}), f.enrollment.Token, "application/json", "")
	if status != 200 || !bytes.Contains(body, []byte(`"ingestStatus":"sealed"`)) {
		t.Fatal("route seal HTTP failed", status, string(body))
	}
	status, body = request("POST", "/v2/routes/"+routeID+"/delete", p3JSON(t, syncing.RouteDeleteRequest{
		ProtocolVersion: syncing.ProtocolVersion, DatasetEpoch: f.enrollment.DatasetEpoch,
		CollectorEpoch: f.enrollment.CollectorEpoch, Reason: "healthkit_deleted",
	}), f.enrollment.Token, "application/json", "")
	if status != 200 || !bytes.Contains(body, []byte(`"ingestStatus":"deleted"`)) {
		t.Fatal("route delete HTTP failed", status, string(body))
	}
	// Unsupported protocol is machine-readable, never a generic success/payload.
	bad := manifest
	bad.ProtocolVersion = 99
	status, body = request("POST", "/v2/routes", p3JSON(t, bad), f.enrollment.Token, "application/json", "")
	if status != 422 || !bytes.Contains(body, []byte(`"code":"unsupported_protocol"`)) {
		t.Fatal("unsupported protocol not explicit", status, string(body))
	}
	// A known type with the wrong unit gets a stable unsupported_unit code.
	f.stream = sampleStream
	badEvent := sample(syncing.NewID(), 120)
	badEvent.Quantity.Unit = "Hz"
	env, chunkWire := f.envelope(t, []syncing.Event{badEvent}, 2, &sampleBatch.BatchID)
	status, body = request("POST", "/v2/sync/batches", p3JSON(t, env), f.enrollment.Token, "application/json", "")
	if status != 200 {
		t.Fatal("bad-unit batch registration failed", status, string(body))
	}
	status, body = request("PUT", "/v2/sync/batches/"+env.BatchID+"/body", chunkWire, f.enrollment.Token, "application/x-rai-health-delta+ndjson", "gzip")
	if status != 422 || !bytes.Contains(body, []byte(`"code":"unsupported_unit"`)) {
		t.Fatal("unsupported unit not explicit", status, string(body))
	}
}

func p3JSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestConcurrentRouteChunkAndSeal(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	workoutStream(t, f)
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	w := workoutAt(start, 100)
	f.commit(t, []syncing.Event{w}, 1, nil)
	route := syncing.NewID()
	registerTestRoute(t, f, w.UUID, route, start, start.Add(99*time.Second), 1, 100)
	lease := takeUpload(t, f.s, f.p)
	chunk := testRouteChunk(route, 0, testRoutePoints(start, 100, 0))
	const workers = 8
	results := make(chan []byte, workers)
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			body, err := f.s.CommitRouteChunk(ctx, f.p, chunk, lease)
			results <- body
			errs <- err
		}()
	}
	group.Wait()
	close(results)
	close(errs)
	var original []byte
	for err := range errs {
		if err != nil {
			t.Fatal("concurrent chunk failed", err)
		}
	}
	for body := range results {
		if original == nil {
			original = body
		}
		if !bytes.Equal(original, body) {
			t.Fatal("concurrent chunk receipts differ")
		}
	}
	if err := f.s.ReleaseUpload(ctx, f.p, lease); err != nil {
		t.Fatal(err)
	}
	state, err := f.s.Route(ctx, f.p, route)
	if err != nil || state.CommittedChunks != 1 || count(t, f, "route_chunks") != 1 {
		t.Fatal("concurrent chunk duplicated", state, err)
	}
	seal := syncing.RouteSeal{ProtocolVersion: syncing.ProtocolVersion, ChunkCount: 1, TotalPoints: 100,
		ManifestSHA256: state.ManifestSHA256, PayloadManifestSHA256: state.PayloadManifestSHA256}
	sealResults := make(chan []byte, workers)
	sealErrs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			body, err := f.s.SealRoute(ctx, f.p, route, seal)
			sealResults <- body
			sealErrs <- err
		}()
	}
	group.Wait()
	close(sealResults)
	close(sealErrs)
	original = nil
	for err := range sealErrs {
		if err != nil {
			t.Fatal("concurrent seal failed", err)
		}
	}
	for body := range sealResults {
		if original == nil {
			original = body
		}
		if !bytes.Equal(original, body) {
			t.Fatal("concurrent seal receipts differ")
		}
	}
	if count(t, f, "workout_routes") != 1 || seriesCount(t, f, "workout_routes", "state='sealed'") != 1 ||
		count(t, f, "jobs") != 2 || count(t, f, "change_events") != 2 {
		t.Fatal("concurrent seal double-published or duplicated side effects")
	}
}

func TestSchema10UpgradePreservesP2AndEnablesP3(t *testing.T) {
	f := setupWithMigration(t, func(ctx context.Context, url string) error {
		db, err := sql.Open("pgx", url)
		if err != nil {
			return err
		}
		defer db.Close()
		provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.Files)
		if err != nil {
			return err
		}
		_, err = provider.UpTo(ctx, 10)
		return err
	})
	ctx := context.Background()
	sampleEvent := sample(syncing.NewID(), 132)
	sampleBatch, sampleReceipt, sampleBody := f.commit(t, []syncing.Event{sampleEvent}, 1, nil)
	workoutStream(t, f)
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	w := workoutAt(start, 100)
	workoutBatch, _, workoutBody := f.commit(t, []syncing.Event{w}, 1, nil)
	before := businessSnapshot(t, f)
	if err := f.s.Ready(ctx); err == nil {
		t.Fatal("schema 10 accepted by schema 11 binary")
	}
	for range 2 {
		if err := admin.Migrate(ctx, f.s.Pool.Config().ConnString()); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.s.Ready(ctx); err != nil {
		t.Fatal("schema 11 not ready", err)
	}
	sameBusiness(t, f, before)
	retry, err := f.s.Commit(ctx, f.p, sampleBatch, []syncing.Event{sampleEvent})
	if err != nil || !bytes.Equal(retry, sampleBody) {
		t.Fatal("upgrade changed sample receipt", err)
	}
	retry, err = f.s.Commit(ctx, f.p, workoutBatch, []syncing.Event{w})
	if err != nil || !bytes.Equal(retry, workoutBody) {
		t.Fatal("upgrade changed workout receipt", err)
	}
	if sampleReceipt.CommitRevision != 1 {
		t.Fatal("sample revision wrong")
	}
	route := syncing.NewID()
	registerTestRoute(t, f, w.UUID, route, start, start.Add(9*time.Second), 1, 10)
	uploadTestRouteChunk(t, f, route, 0, testRoutePoints(start, 10, 0))
	manifest, payload := routeStateChains(t, f, route)
	if _, err = f.s.SealRoute(ctx, f.p, route, syncing.RouteSeal{
		ProtocolVersion: syncing.ProtocolVersion, ChunkCount: 1, TotalPoints: 10,
		ManifestSHA256: manifest, PayloadManifestSHA256: payload,
	}); err != nil {
		t.Fatal("P3 route unavailable after upgrade", err)
	}
}

func TestWorkoutDeleteFencesLateRouteAndAssociation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	s := sample(syncing.NewID(), 132)
	f.commit(t, []syncing.Event{s}, 1, nil)
	workoutStream(t, f)
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	w := workoutAt(start, 100)
	workoutBatch, _, _ := f.commit(t, []syncing.Event{w}, 1, nil)
	association := syncing.AssociationRequest{
		ProtocolVersion: syncing.ProtocolVersion, DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch,
		CollectorEpoch: f.enrollment.CollectorEpoch, AssociationID: syncing.NewID(), SampleType: kind,
		SampleUUID: s.UUID, WorkoutUUID: w.UUID, Source: &syncing.Source{BundleID: "synthetic.test"},
	}
	if _, err := f.s.AssociateSample(ctx, f.p, association); err != nil {
		t.Fatal(err)
	}
	route := syncing.NewID()
	registerTestRoute(t, f, w.UUID, route, start, start.Add(9*time.Second), 1, 10)
	uploadTestRouteChunk(t, f, route, 0, testRoutePoints(start, 10, 0))
	manifest, payload := routeStateChains(t, f, route)
	sealTestRoute(t, f, route, 1, 10, manifest, payload)
	_, _, _ = f.commit(t, []syncing.Event{workoutDelete(w.UUID)}, 2, &workoutBatch.BatchID)
	if count(t, f, "workouts") != 0 || count(t, f, "workout_samples") != 0 || count(t, f, "workout_routes") != 0 {
		t.Fatal("workout delete did not cascade route/association")
	}
	if !embeddedRouteTombstone(t, f, route) {
		t.Fatal("workout delete did not fence route UUID")
	}
	if _, err := f.s.AssociateSample(ctx, f.p, association); err == nil {
		t.Fatal("association replayed after workout delete")
	} else {
		requireCode(t, err, "association_unavailable")
	}
	if _, err := f.s.RegisterRoute(ctx, f.p, syncing.RouteManifest{
		ProtocolVersion: syncing.ProtocolVersion, DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch,
		CollectorEpoch: f.enrollment.CollectorEpoch, RouteID: route, WorkoutID: w.UUID,
		Source: &syncing.Source{BundleID: "synthetic.test"}, ExpectedChunkCount: 1, ExpectedPointCount: 10,
		FirstRecordedAtUs: start.UnixMicro(), LastRecordedAtUs: start.Add(9 * time.Second).UnixMicro(),
	}); err == nil {
		t.Fatal("late route re-registered after workout delete")
	} else {
		requireCode(t, err, "route_unavailable")
	}
	lease := takeUpload(t, f.s, f.p)
	_, err := f.s.CommitRouteChunk(ctx, f.p, testRouteChunk(route, 0, testRoutePoints(start, 10, 0)), lease)
	requireCode(t, err, "route_not_found")
	_ = f.s.ReleaseUpload(ctx, f.p, lease)
	if _, err = f.s.SealRoute(ctx, f.p, route, syncing.RouteSeal{
		ProtocolVersion: syncing.ProtocolVersion, ChunkCount: 1, TotalPoints: 10,
		ManifestSHA256: manifest, PayloadManifestSHA256: payload,
	}); err == nil {
		t.Fatal("late seal resurrected deleted route")
	} else {
		requireCode(t, err, "route_not_found")
	}
}

func TestWorkoutEnrichmentCannotOverwriteBaseFacts(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	workoutStream(t, f)
	w := workout(t, syncing.NewID())
	f.commit(t, []syncing.Event{w}, 1, nil)
	start := time.UnixMicro(*w.StartUs).UTC()
	base := syncing.WorkoutEnrichmentRequest{
		ProtocolVersion: syncing.ProtocolVersion, DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch,
		CollectorEpoch: f.enrollment.CollectorEpoch, EnrichmentID: syncing.NewID(), WorkoutID: w.UUID,
		Source: &syncing.Source{BundleID: "synthetic.source"}, SourceVersion: 1,
		ObservedAtUs: start.UnixMicro() + 1000,
	}
	badMetadata := base
	badMetadata.Metadata = map[string]any{"synthetic": false}
	if _, err := f.s.EnrichWorkout(ctx, f.p, badMetadata); err == nil {
		t.Fatal("base metadata overwritten")
	} else {
		requireCode(t, err, "workout_enrichment_conflict")
	}
	distance := 12345.0
	badDistance := base
	badDistance.TotalDistanceM = &distance
	if _, err := f.s.EnrichWorkout(ctx, f.p, badDistance); err == nil {
		t.Fatal("base distance overwritten")
	} else {
		requireCode(t, err, "workout_enrichment_conflict")
	}
	equal := 10000.0
	energy := 700.0
	same := base
	same.TotalDistanceM, same.TotalEnergyKcal = &equal, &energy
	if _, err := f.s.EnrichWorkout(ctx, f.p, same); err != nil {
		t.Fatal("equal base values not accepted", err)
	}
}

func TestUnsupportedUnitAndProtocolAreStableCodes(t *testing.T) {
	if err := (syncing.Envelope{ProtocolVersion: 1}).Validate(); err == nil {
		t.Fatal("old protocol accepted")
	} else {
		requireCode(t, err, "unsupported_protocol")
	}
	start := int64(1793059200000001)
	event := syncing.Event{Op: "upsert", Kind: "sample", Type: kind, UUID: syncing.NewID(), StartUs: &start, EndUs: &start,
		Quantity: &syncing.Quantity{Value: 1, Unit: "Hz"}, Source: &syncing.Source{BundleID: "synthetic.test"}}
	if err := event.Validate(kind); err == nil {
		t.Fatal("unsupported unit accepted")
	} else {
		requireCode(t, err, "unsupported_unit")
	}
}

func TestUnknownRouteDeleteCreatesBarrierAndIsIdempotent(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	route := syncing.NewID()
	req := syncing.RouteDeleteRequest{ProtocolVersion: syncing.ProtocolVersion, DatasetEpoch: f.enrollment.DatasetEpoch,
		CollectorEpoch: f.enrollment.CollectorEpoch}
	first, err := f.s.DeleteRoute(ctx, f.p, route, req)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.s.DeleteRoute(ctx, f.p, route, req)
	if err != nil || !bytes.Equal(first, again) {
		t.Fatal("unknown route delete retry changed receipt", err)
	}
	if !embeddedRouteTombstone(t, f, route) || count(t, f, "workout_routes") != 0 {
		t.Fatal("unknown route delete did not leave a barrier")
	}
	var receipt syncing.RouteDeleteReceipt
	if err = json.Unmarshal(first, &receipt); err != nil || receipt.RouteExisted || !receipt.TombstoneCreated {
		t.Fatal("unknown route delete receipt invalid", err)
	}
}

func TestAPIStoreRestartRetainsRegisteredBatchAndReceipt(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	event := sample(syncing.NewID(), 132)
	envelope, wire := f.envelope(t, []syncing.Event{event}, 1, nil)
	if _, err := f.s.RegisterBatch(ctx, f.p, envelope); err != nil {
		t.Fatal(err)
	}
	// Construct a second pool over the same database to model an API process
	// restart while the immutable batch remains registered.
	restarted, err := postgres.Open(ctx, f.s.Pool.Config().ConnString(), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Pool.Close()
	p, err := restarted.Authenticate(ctx, f.enrollment.Token)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := syncing.ParseBody(bytes.NewReader(wire), envelope, f.stream.SampleType)
	if err != nil {
		t.Fatal(err)
	}
	body, err := restarted.Commit(ctx, p, envelope, parsed)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := restarted.Commit(ctx, p, envelope, parsed)
	if err != nil || !bytes.Equal(body, retry) {
		t.Fatal("restart retry changed receipt", err)
	}
	// The original pool can still recover the same durable receipt.
	state, err := f.s.Batch(ctx, f.p, envelope.BatchID)
	if err != nil || !bytes.Equal(state.Receipt, body) {
		t.Fatal("restart lost durable receipt", err)
	}
	if count(t, f, "health_samples") != 1 || count(t, f, "jobs") != 1 {
		t.Fatal("restart retry duplicated facts")
	}
}

func TestRouteSealResponseLossRecoveredByGET(t *testing.T) {
	f := setup(t)
	workoutStream(t, f)
	start := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	w := workoutAt(start, 10)
	f.commit(t, []syncing.Event{w}, 1, nil)
	route := syncing.NewID()
	registerTestRoute(t, f, w.UUID, route, start, start.Add(9*time.Second), 1, 10)
	uploadTestRouteChunk(t, f, route, 0, testRoutePoints(start, 10, 0))
	manifest, payload := routeStateChains(t, f, route)
	handler := httpapi.New(f.s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var committedBody []byte
	var dropped atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/seal") && !dropped.Swap(true) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, r)
			if recorder.Code != 200 {
				t.Errorf("seal failed before response loss: %d %s", recorder.Code, recorder.Body.String())
			}
			committedBody = append([]byte(nil), recorder.Body.Bytes()...)
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack: %v", err)
				return
			}
			_ = conn.Close()
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	sealBody := p3JSON(t, syncing.RouteSeal{ProtocolVersion: syncing.ProtocolVersion, ChunkCount: 1, TotalPoints: 10,
		ManifestSHA256: manifest, PayloadManifestSHA256: payload})
	req, _ := http.NewRequest("POST", server.URL+"/v2/routes/"+route+"/seal", bytes.NewReader(sealBody))
	req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
	req.Header.Set("Content-Type", "application/json")
	if resp, err := server.Client().Do(req); err == nil {
		resp.Body.Close()
		t.Fatal("expected lost seal response")
	}
	req, _ = http.NewRequest("GET", server.URL+"/v2/routes/"+route, nil)
	req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var state syncing.RouteState
	if err = json.NewDecoder(resp.Body).Decode(&state); err != nil || state.IngestStatus != "sealed" {
		t.Fatal("sealed state not recoverable", err)
	}
	if !bytes.Equal(state.Receipt, committedBody) {
		t.Fatal("route GET did not return original seal receipt")
	}
	if count(t, f, "route_chunks") != 1 || count(t, f, "jobs") != 2 {
		t.Fatal("seal response loss duplicated work")
	}
}
