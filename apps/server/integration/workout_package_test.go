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
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"rai/server/internal/admin"
	"rai/server/internal/httpapi"
	"rai/server/internal/syncing"
	"rai/server/migrations"
)

func workoutPackageRequest(f *fixture, id string, start, end int64) syncing.WorkoutPackageRequest {
	activityType := int64(37)
	duration := end - start
	return syncing.WorkoutPackageRequest{
		DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch,
		WorkoutID: id, StartUs: &start, EndUs: &end,
		Source:   &syncing.Source{BundleID: "synthetic.package"},
		Metadata: map[string]any{"source": "package-test"},
		Workout:  &syncing.Workout{ActivityType: &activityType, DurationUs: &duration},
	}
}

func workoutPackageActivityLine(t *testing.T, ordinal int, start, end int64) string {
	t.Helper()
	uuid := syncing.NewID()
	return `{"kind":"activity","ordinal":` + itoaIntegration(ordinal) + `,"activity":{"uuid":"` + uuid +
		`","startUs":` + itoaIntegration64(start) + `,"durationUs":` + itoaIntegration64(end-start) +
		`,"configuration":{"activityType":37,"locationType":1,"swimmingLocationType":0},` +
		`"statistics":[{"type":"HKQuantityTypeIdentifierHeartRate","startUs":` + itoaIntegration64(start) +
		`,"endUs":` + itoaIntegration64(start+1000000) + `,"average":{"value":130,"unit":"count/min"}}],` +
		`"events":[{"type":1,"startUs":` + itoaIntegration64(start) + `,"endUs":` + itoaIntegration64(start+1000000) + `}]}}`
}

func itoaIntegration(n int) string {
	return json.Number(strings.TrimSpace(string(mustJSON(n)))).String()
}
func itoaIntegration64(n int64) string {
	return json.Number(strings.TrimSpace(string(mustJSON(n)))).String()
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func workoutPackageWire(t *testing.T, lines ...string) []byte {
	t.Helper()
	var wire bytes.Buffer
	g := gzip.NewWriter(&wire)
	for _, line := range lines {
		if _, err := g.Write([]byte(line + "\n")); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	return wire.Bytes()
}

func commitWorkoutPackageChunk(t *testing.T, f *fixture, id string, index int, lines ...string) (syncing.WorkoutPackageChunkReceipt, []byte) {
	t.Helper()
	state, err := f.s.WorkoutPackage(context.Background(), f.p, id)
	if err != nil {
		t.Fatal(err)
	}
	wire := workoutPackageWire(t, lines...)
	chunk, err := syncing.ParseWorkoutDetailChunk(bytes.NewReader(wire), state.StartUs, state.EndUs)
	if err != nil {
		t.Fatal(err)
	}
	chunk.WorkoutID, chunk.ChunkIndex = id, index
	lease := takeUpload(t, f.s, f.p)
	body, err := f.s.CommitWorkoutPackageChunk(context.Background(), f.p, chunk, lease)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.ReleaseUpload(context.Background(), f.p, lease); err != nil {
		t.Fatal(err)
	}
	var receipt syncing.WorkoutPackageChunkReceipt
	if err = json.Unmarshal(body, &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt, wire
}

func TestWorkoutPackageChunksSealAndFacts(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := syncing.NewID()
	start, end := int64(1793059200000000), int64(1793059200000000)+3600000000
	req := workoutPackageRequest(f, id, start, end)
	state, err := f.s.RegisterWorkoutPackage(ctx, f.p, req)
	if err != nil || state.IngestStatus != "pending" || state.CommittedChunks != 0 {
		t.Fatalf("register package: %+v %v", state, err)
	}
	// Same core is a retry; a different core is not a second identity.
	if retry, err := f.s.RegisterWorkoutPackage(ctx, f.p, req); err != nil || retry.WorkoutID != id {
		t.Fatal("package registration retry failed", err)
	}
	other := req
	other.Metadata = map[string]any{"source": "different"}
	if _, err = f.s.RegisterWorkoutPackage(ctx, f.p, other); err == nil {
		t.Fatal("different package core reused UUID")
	} else {
		requireCode(t, err, "workout_package_identity_conflict")
	}

	first := workoutPackageActivityLine(t, 0, start, start+1800000000)
	parentStat := `{"kind":"statistic","ordinal":0,"statistic":{"type":"HKQuantityTypeIdentifierHeartRate","startUs":1793059200000000,"endUs":1793059260000000,"average":{"value":131,"unit":"count/min"}}}`
	parentEvent := `{"kind":"event","ordinal":0,"event":{"type":1,"startUs":1793059201000000,"endUs":1793059202000000,"metadata":{"lane":1}}}`
	second := workoutPackageActivityLine(t, 1, start+1800000000, end)
	receipt1, wire1 := commitWorkoutPackageChunk(t, f, id, 0, first, parentStat, parentEvent)
	if receipt1.CommittedChunks != 1 || receipt1.TotalDetails != 5 || receipt1.DetailCount != 3 {
		t.Fatalf("unexpected first chunk receipt: %+v", receipt1)
	}
	state, err = f.s.WorkoutPackage(ctx, f.p, id)
	if err != nil || state.CommittedChunks != 1 || state.TotalDetails != 5 || state.IngestStatus != "pending" {
		t.Fatalf("pending state wrong: %+v %v", state, err)
	}
	// Same index and bytes are idempotent; different bytes conflict before seal.
	chunk := syncing.WorkoutDetailChunk{WorkoutID: id, ChunkIndex: 0}
	parsed, err := syncing.ParseWorkoutDetailChunk(bytes.NewReader(wire1), start, end)
	if err != nil {
		t.Fatal(err)
	}
	chunk = parsed
	chunk.WorkoutID, chunk.ChunkIndex = id, 0
	lease := takeUpload(t, f.s, f.p)
	retryBody, err := f.s.CommitWorkoutPackageChunk(ctx, f.p, chunk, lease)
	if err != nil || !bytes.Equal(retryBody, mustJSON(receipt1)) {
		t.Fatal("same chunk bytes did not return original receipt", err)
	}
	if err = f.s.ReleaseUpload(ctx, f.p, lease); err != nil {
		t.Fatal(err)
	}
	conflict := workoutPackageWire(t, first)
	conflictChunk, err := syncing.ParseWorkoutDetailChunk(bytes.NewReader(conflict), start, end)
	if err != nil {
		t.Fatal(err)
	}
	conflictChunk.WorkoutID, conflictChunk.ChunkIndex = id, 0
	lease = takeUpload(t, f.s, f.p)
	if _, err = f.s.CommitWorkoutPackageChunk(ctx, f.p, conflictChunk, lease); err == nil {
		t.Fatal("different chunk bytes accepted")
	} else {
		requireCode(t, err, "workout_package_chunk_conflict")
	}
	_ = f.s.ReleaseUpload(ctx, f.p, lease)

	receipt2, _ := commitWorkoutPackageChunk(t, f, id, 1, second)
	if receipt2.CommittedChunks != 2 || receipt2.TotalDetails != 8 {
		t.Fatalf("unexpected second chunk receipt: %+v", receipt2)
	}
	state, err = f.s.WorkoutPackage(ctx, f.p, id)
	if err != nil || state.TotalDetails != 8 {
		t.Fatal(err)
	}
	sealBody, err := f.s.SealWorkoutPackage(ctx, f.p, id, syncing.WorkoutPackageSeal{ChunkCount: 2, TotalDetails: 8, ManifestSHA256: state.ManifestSHA256})
	if err != nil {
		t.Fatal(err)
	}
	var seal syncing.WorkoutPackageReceipt
	if err = json.Unmarshal(sealBody, &seal); err != nil || seal.IngestStatus != "sealed" || seal.CommitRevision != 1 || seal.TotalDetails != 8 {
		t.Fatalf("bad seal receipt: %v %+v", err, seal)
	}
	if count(t, f, "workouts") != 1 || count(t, f, "workout_activities") != 2 || count(t, f, "workout_statistics") != 1 || count(t, f, "workout_events") != 1 {
		t.Fatal("package did not materialize normalized facts")
	}
	if count(t, f, "jobs") != 1 || count(t, f, "change_events") != 1 || count(t, f, "dirty_scopes") != 1 {
		t.Fatal("package did not create durable work/commit log")
	}
	var payload []byte
	if err = f.s.Pool.QueryRow(ctx, `SELECT payload FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2`, f.p.DatasetID, id).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var stored syncing.Event
	if err = syncing.DecodeJSON(payload, &stored); err != nil {
		t.Fatal(err)
	}
	expectedWorkout := *req.Workout
	expected := req.CoreEvent()
	expected.Workout = &expectedWorkout
	expected.Workout.Activities = []syncing.WorkoutActivity{*chunkActivity(t, first), *chunkActivity(t, second)}
	expected.Workout.Statistics = []syncing.WorkoutStatistic{{Type: "HKQuantityTypeIdentifierHeartRate", StartUs: ptr(start), EndUs: ptr(start + 60000000), Average: &syncing.Quantity{Value: 131, Unit: "count/min"}}}
	expected.Workout.Events = []syncing.WorkoutEvent{{Type: ptr(int64(1)), StartUs: ptr(start + 1000000), EndUs: ptr(start + 2000000), Metadata: map[string]any{"lane": json.Number("1")}}}
	a, _ := expected.Canonical()
	b, _ := stored.Canonical()
	if !bytes.Equal(a, b) {
		t.Fatalf("materialized payload differs from core+chunks\n%s\n%s", a, b)
	}
	if retry, err := f.s.SealWorkoutPackage(ctx, f.p, id, syncing.WorkoutPackageSeal{ChunkCount: 2, TotalDetails: 8, ManifestSHA256: state.ManifestSHA256}); err != nil || !bytes.Equal(retry, sealBody) {
		t.Fatal("seal retry changed original bytes", err)
	}
	if retry, err := f.s.RegisterWorkoutPackage(ctx, f.p, req); err != nil || retry.IngestStatus != "sealed" {
		t.Fatal("sealed package registration retry failed", err)
	}
	lateWire := workoutPackageWire(t, second)
	lateChunk, err := syncing.ParseWorkoutDetailChunk(bytes.NewReader(lateWire), start, end)
	if err != nil {
		t.Fatal(err)
	}
	lateChunk.WorkoutID, lateChunk.ChunkIndex = id, 2
	lease = takeUpload(t, f.s, f.p)
	if _, err = f.s.CommitWorkoutPackageChunk(ctx, f.p, lateChunk, lease); err == nil {
		t.Fatal("late chunk accepted")
	} else {
		requireCode(t, err, "workout_package_sealed")
	}
	_ = f.s.ReleaseUpload(ctx, f.p, lease)
}

func ptr[T any](v T) *T { return &v }

func chunkActivity(t *testing.T, line string) *syncing.WorkoutActivity {
	t.Helper()
	var detail syncing.WorkoutDetail
	if err := syncing.DecodeJSON([]byte(line), &detail); err != nil {
		t.Fatal(err)
	}
	return detail.Activity
}

func TestWorkoutPackageMaxChunksAndDelete(t *testing.T) {
	f := setup(t)
	workoutStream(t, f)
	ctx := context.Background()
	id := syncing.NewID()
	start, end := int64(1793059200000000), int64(1793059200000000)+int64(syncing.MaxWorkoutPackageChunks)*1000000
	req := workoutPackageRequest(f, id, start, end)
	if _, err := f.s.RegisterWorkoutPackage(ctx, f.p, req); err != nil {
		t.Fatal(err)
	}
	// One activity per chunk exercises the maximum chunk count without
	// exceeding the 128-activity or 20,000-detail caps.
	for i := 0; i < syncing.MaxWorkoutPackageChunks; i++ {
		line := workoutPackageActivityLine(t, i, start+int64(i)*1000000, start+int64(i+1)*1000000)
		commitWorkoutPackageChunk(t, f, id, i, line)
	}
	state, err := f.s.WorkoutPackage(ctx, f.p, id)
	if err != nil || state.CommittedChunks != syncing.MaxWorkoutPackageChunks || state.TotalDetails != int64(syncing.MaxWorkoutPackageChunks)*3 {
		t.Fatalf("max package state wrong: %+v %v", state, err)
	}
	seal, err := f.s.SealWorkoutPackage(ctx, f.p, id, syncing.WorkoutPackageSeal{ChunkCount: syncing.MaxWorkoutPackageChunks, TotalDetails: state.TotalDetails, ManifestSHA256: state.ManifestSHA256})
	if err != nil {
		t.Fatal(err)
	}
	var receipt syncing.WorkoutPackageReceipt
	if err = json.Unmarshal(seal, &receipt); err != nil || receipt.IngestStatus != "sealed" || receipt.ChunkCount != syncing.MaxWorkoutPackageChunks {
		t.Fatalf("max package seal invalid: %v %+v", err, receipt)
	}
	// Normal batch delete of the materialized Workout removes package and
	// projections, leaves a tombstone and cannot resurrect the UUID.
	e, r, _ := f.commit(t, []syncing.Event{workoutDelete(id)}, 1, nil)
	if r.Counts.Deleted != 1 {
		t.Fatal("package workout delete failed")
	}
	if count(t, f, "workouts") != 0 || count(t, f, "workout_activities") != 0 || count(t, f, "workout_packages") != 0 || count(t, f, "workout_package_chunks") != 0 {
		t.Fatal("delete left package detail")
	}
	if _, err = f.s.WorkoutPackage(ctx, f.p, id); err == nil {
		t.Fatal("deleted package remained readable")
	} else {
		requireCode(t, err, "workout_package_not_found")
	}
	if _, err := f.s.RegisterWorkoutPackage(ctx, f.p, req); err == nil {
		t.Fatal("deleted Workout UUID was resurrected")
	} else {
		requireCode(t, err, "workout_package_unavailable")
	}
	_ = e
}

func TestWorkoutPackageSealFailureRollsBack(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := syncing.NewID()
	start, end := int64(1793059200000000), int64(1793059260000000)
	req := workoutPackageRequest(f, id, start, end)
	if _, err := f.s.RegisterWorkoutPackage(ctx, f.p, req); err != nil {
		t.Fatal(err)
	}
	line := workoutPackageActivityLine(t, 0, start, end)
	commitWorkoutPackageChunk(t, f, id, 0, line)
	state, err := f.s.WorkoutPackage(ctx, f.p, id)
	if err != nil {
		t.Fatal(err)
	}
	before := businessSnapshot(t, f)
	if _, err := f.s.Pool.Exec(ctx, `CREATE FUNCTION fail_workout_package_seal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic package seal failure'; END $$;
		CREATE TRIGGER fail_workout_package_seal BEFORE UPDATE ON workout_packages FOR EACH ROW WHEN (NEW.state='sealed') EXECUTE FUNCTION fail_workout_package_seal()`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SealWorkoutPackage(ctx, f.p, id, syncing.WorkoutPackageSeal{ChunkCount: 1, TotalDetails: 3, ManifestSHA256: state.ManifestSHA256}); err == nil {
		t.Fatal("seal fault did not abort")
	}
	sameBusiness(t, f, before)
	state2, err := f.s.WorkoutPackage(ctx, f.p, id)
	if err != nil || state2.IngestStatus != "pending" || state2.CommittedChunks != 1 || state2.TotalDetails != 3 {
		t.Fatalf("failed seal changed package staging: %+v %v", state2, err)
	}
	if _, err := f.s.Pool.Exec(ctx, `DROP TRIGGER fail_workout_package_seal ON workout_packages`); err != nil {
		t.Fatal(err)
	}
	sealed, err := f.s.SealWorkoutPackage(ctx, f.p, id, syncing.WorkoutPackageSeal{ChunkCount: 1, TotalDetails: 3, ManifestSHA256: state.ManifestSHA256})
	if err != nil {
		t.Fatal("package could not recover after fault", err)
	}
	var receipt syncing.WorkoutPackageReceipt
	if err = json.Unmarshal(sealed, &receipt); err != nil || receipt.IngestStatus != "sealed" {
		t.Fatal(err)
	}
}

func TestSchema8UpgradeAddsWorkoutPackagesWithoutRewritingFacts(t *testing.T) {
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
		_, err = provider.UpTo(ctx, 8)
		return err
	})
	ctx := context.Background()
	seed := sample(syncing.NewID(), 61.5)
	e, _, original := f.commit(t, []syncing.Event{seed}, 1, nil)
	before := businessSnapshot(t, f)
	if err := f.s.Ready(ctx); err == nil {
		t.Fatal("schema 8 accepted by schema 9 binary")
	}
	for range 2 {
		if err := admin.Migrate(ctx, f.s.Pool.Config().ConnString()); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.s.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	sameBusinessAcrossDeviceMigration(t, f, before)
	if count(t, f, "workout_packages") != 0 || count(t, f, "workout_package_chunks") != 0 {
		t.Fatal("migration fabricated package staging")
	}
	if retry, err := f.s.Commit(ctx, f.p, e, []syncing.Event{seed}); err != nil || !bytes.Equal(retry, original) {
		t.Fatal("upgrade changed original workout receipt", err)
	}
	id := syncing.NewID()
	start, end := int64(1793059200000000), int64(1793059260000000)
	if _, err := f.s.RegisterWorkoutPackage(ctx, f.p, workoutPackageRequest(f, id, start, end)); err != nil {
		t.Fatal("package unavailable after upgrade", err)
	}
	line := workoutPackageActivityLine(t, 0, start, end)
	commitWorkoutPackageChunk(t, f, id, 0, line)
	state2, err := f.s.WorkoutPackage(ctx, f.p, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.SealWorkoutPackage(ctx, f.p, id, syncing.WorkoutPackageSeal{ChunkCount: 1, TotalDetails: 3, ManifestSHA256: state2.ManifestSHA256}); err != nil {
		t.Fatal("package seal unavailable after upgrade", err)
	}
}

func TestWorkoutPackageGapOrderAndSealChecks(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := syncing.NewID()
	start, end := int64(1793059200000000), int64(1793059260000000)
	if _, err := f.s.RegisterWorkoutPackage(ctx, f.p, workoutPackageRequest(f, id, start, end)); err != nil {
		t.Fatal(err)
	}
	line := workoutPackageActivityLine(t, 0, start, end)
	wire := workoutPackageWire(t, line)
	chunk, err := syncing.ParseWorkoutDetailChunk(bytes.NewReader(wire), start, end)
	if err != nil {
		t.Fatal(err)
	}
	chunk.WorkoutID, chunk.ChunkIndex = id, 1
	lease := takeUpload(t, f.s, f.p)
	if _, err = f.s.CommitWorkoutPackageChunk(ctx, f.p, chunk, lease); err == nil {
		t.Fatal("chunk gap accepted")
	} else {
		requireCode(t, err, "workout_package_chunk_gap")
	}
	_ = f.s.ReleaseUpload(ctx, f.p, lease)
	// Parent statistics must be strictly type-sorted across the whole Workout.
	bad := workoutPackageWire(t,
		`{"kind":"statistic","ordinal":0,"statistic":{"type":"HKQuantityTypeIdentifierStepCount","startUs":1793059200000000,"endUs":1793059260000000,"sum":{"value":100,"unit":"count"}}}`,
		`{"kind":"statistic","ordinal":1,"statistic":{"type":"HKQuantityTypeIdentifierHeartRate","startUs":1793059200000000,"endUs":1793059260000000,"average":{"value":100,"unit":"count/min"}}}`)
	badChunk, err := syncing.ParseWorkoutDetailChunk(bytes.NewReader(bad), start, end)
	if err != nil {
		t.Fatal(err)
	}
	badChunk.WorkoutID, badChunk.ChunkIndex = id, 0
	lease = takeUpload(t, f.s, f.p)
	if _, err = f.s.CommitWorkoutPackageChunk(ctx, f.p, badChunk, lease); err == nil {
		t.Fatal("unsorted statistics accepted")
	} else {
		requireCode(t, err, "unsupported_payload")
	}
	_ = f.s.ReleaseUpload(ctx, f.p, lease)
	if _, err = f.s.SealWorkoutPackage(ctx, f.p, id, syncing.WorkoutPackageSeal{ChunkCount: 2, TotalDetails: 2, ManifestSHA256: strings.Repeat("0", 64)}); err == nil {
		t.Fatal("incomplete package sealed")
	} else {
		requireCode(t, err, "workout_package_incomplete")
	}
}

func TestWorkoutPackageNormalWorkoutIdentityConflict(t *testing.T) {
	f := setup(t)
	workoutStream(t, f)
	ctx := context.Background()
	// A normal Workout already owns the UUID; a package cannot attach later.
	id := syncing.NewID()
	w := workout(t, id)
	e, _, _ := f.commit(t, []syncing.Event{w}, 1, nil)
	if _, err := f.s.RegisterWorkoutPackage(ctx, f.p, workoutPackageRequest(f, id, *w.StartUs, *w.EndUs)); err == nil {
		t.Fatal("package attached to existing Workout")
	} else {
		requireCode(t, err, "workout_package_identity_conflict")
	}
	// A pending package blocks a normal Workout with the same UUID before facts commit.
	id2 := syncing.NewID()
	if _, err := f.s.RegisterWorkoutPackage(ctx, f.p, workoutPackageRequest(f, id2, *w.StartUs, *w.EndUs)); err != nil {
		t.Fatal(err)
	}
	w2 := workout(t, id2)
	e2, wire2 := f.envelope(t, []syncing.Event{w2}, 2, &e.BatchID)
	if _, err := f.s.RegisterBatch(ctx, f.p, e2); err != nil {
		t.Fatal(err)
	}
	parsed, err := syncing.ParseBody(bytes.NewReader(wire2), e2, syncing.WorkoutType)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Commit(ctx, f.p, e2, parsed); err == nil {
		t.Fatal("normal Workout crossed a pending package")
	} else {
		requireCode(t, err, "workout_package_conflict")
	}
	if count(t, f, "workouts") != 1 || count(t, f, "workout_packages") != 1 {
		t.Fatal("identity conflict had side effects")
	}
}

func TestHTTPWorkoutPackageUploadAndSeal(t *testing.T) {
	f := setup(t)
	id := syncing.NewID()
	start, end := int64(1793059200000000), int64(1793059260000000)
	server := httptest.NewServer(httpapi.New(f.s, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer server.Close()
	reqBody, _ := json.Marshal(workoutPackageRequest(f, id, start, end))
	req, _ := http.NewRequest("POST", server.URL+"/v2/sync/workout-packages", bytes.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("register status %d: %s", resp.StatusCode, b)
	}
	resp.Body.Close()
	line := workoutPackageActivityLine(t, 0, start, end)
	wire := workoutPackageWire(t, line)
	req, _ = http.NewRequest("PUT", server.URL+"/v2/sync/workout-packages/"+id+"/chunks/0", bytes.NewReader(wire))
	req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
	req.Header.Set("Content-Type", "application/x-rai-health-workout-detail+ndjson")
	req.Header.Set("Content-Encoding", "gzip")
	resp, err = server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("chunk status %d: %s", resp.StatusCode, b)
	}
	var chunkReceipt syncing.WorkoutPackageChunkReceipt
	if err = json.NewDecoder(resp.Body).Decode(&chunkReceipt); err != nil || chunkReceipt.CommittedChunks != 1 || chunkReceipt.TotalDetails != 3 {
		t.Fatalf("chunk receipt invalid: %v %+v", err, chunkReceipt)
	}
	resp.Body.Close()
	req, _ = http.NewRequest("GET", server.URL+"/v2/sync/workout-packages/"+id, nil)
	req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
	resp, err = server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var state syncing.WorkoutPackageState
	if err = json.NewDecoder(resp.Body).Decode(&state); err != nil || resp.StatusCode != 200 || state.CommittedChunks != 1 {
		t.Fatalf("GET package failed: %v %+v", err, state)
	}
	resp.Body.Close()
	sealBody, _ := json.Marshal(syncing.WorkoutPackageSeal{ChunkCount: 1, TotalDetails: 3, ManifestSHA256: state.ManifestSHA256})
	req, _ = http.NewRequest("POST", server.URL+"/v2/sync/workout-packages/"+id+"/seal", bytes.NewReader(sealBody))
	req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err = server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("seal status %d: %s", resp.StatusCode, b)
	}
	var receipt syncing.WorkoutPackageReceipt
	if err = json.NewDecoder(resp.Body).Decode(&receipt); err != nil || receipt.IngestStatus != "sealed" {
		t.Fatalf("seal response invalid: %v %+v", err, receipt)
	}
	resp.Body.Close()
}
