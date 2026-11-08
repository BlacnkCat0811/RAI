//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"rai/server/internal/httpapi"
	"rai/server/internal/syncing"
)

func bundleSample(workoutID string, start time.Time, value float64) syncing.Event {
	us := start.UnixMicro()
	return syncing.Event{Op: "upsert", Kind: "sample", Type: kind, UUID: syncing.NewID(), StartUs: &us, EndUs: &us,
		Quantity: &syncing.Quantity{Value: value, Unit: "count/min"}, Source: &syncing.Source{BundleID: "synthetic.test"}}
}

func testWorkoutBundle(t *testing.T, f *fixture, sampleCount int) (syncing.WorkoutBundle, []byte) {
	t.Helper()
	start := time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)
	workout := workoutAt(start, 60)
	samples := make([]syncing.Event, 0, sampleCount)
	for index := 0; index < sampleCount; index++ {
		samples = append(samples, bundleSample(workout.UUID, start.Add(time.Duration(index)*time.Second), 120+float64(index)))
	}
	var series []syncing.WorkoutBundleSeries
	if sampleCount > 0 {
		end := *samples[0].StartUs + 3_000_000
		samples[0].EndUs = &end
		series = []syncing.WorkoutBundleSeries{{SampleType: kind, SampleUUID: samples[0].UUID, Points: []syncing.SeriesPoint{
			{StartUs: *samples[0].StartUs, EndUs: *samples[0].StartUs, Value: 1, Unit: "count/min"},
			{StartUs: *samples[0].StartUs + 1_000_000, EndUs: *samples[0].StartUs + 1_000_000, Value: 2, Unit: "count/min"},
			{StartUs: *samples[0].StartUs + 2_000_000, EndUs: *samples[0].StartUs + 2_000_000, Value: 3, Unit: "count/min"},
		}}}
	}
	route := syncing.WorkoutBundleRoute{RouteID: syncing.NewID(), Source: syncing.Source{BundleID: "synthetic.test"},
		Points: []syncing.WorkoutBundlePoint{
			{RecordedAtUs: start.UnixMicro(), Latitude: 31.2304, Longitude: 121.4737},
			{RecordedAtUs: start.Add(time.Second).UnixMicro(), Latitude: 31.2305, Longitude: 121.4738},
			{RecordedAtUs: start.Add(2 * time.Second).UnixMicro(), Latitude: 31.2306, Longitude: 121.4739},
		}}
	bundle := syncing.WorkoutBundle{ProtocolVersion: 3, DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch,
		CollectorEpoch: f.enrollment.CollectorEpoch, WorkoutID: workout.UUID, Workout: workout,
		Samples: samples, Series: series, Routes: []syncing.WorkoutBundleRoute{route}}
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	return bundle, raw
}

func uploadBundleHTTP(t *testing.T, handler http.Handler, token string, raw []byte) (int, []byte) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "workout.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodPost, "/v2/sync/workout-bundles", &body)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response.Code, response.Body.Bytes()
}

func TestWorkoutBundleOneRequestAtomicImport(t *testing.T) {
	f := setup(t)
	bundle, raw := testWorkoutBundle(t, f, 3)
	body, err := f.s.ImportWorkoutBundle(context.Background(), f.p, bundle, raw)
	if err != nil {
		t.Fatal(err)
	}
	var receipt syncing.WorkoutImportReceipt
	if err = json.Unmarshal(body, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.IngestStatus != "committed" || receipt.Counts.SamplesInserted != 3 ||
		receipt.Counts.AssociationsInserted != 3 || receipt.Counts.SeriesInserted != 1 ||
		receipt.Counts.RoutesInserted != 1 ||
		len(receipt.AnalysisJobIDs) != 1 || receipt.AnalysisStatus != "queued" {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	if got := count(t, f, "health_samples"); got != 3 {
		t.Fatalf("samples=%d", got)
	}
	if got := count(t, f, "workout_samples"); got != 3 {
		t.Fatalf("associations=%d", got)
	}
	if got := count(t, f, "workout_routes"); got != 1 {
		t.Fatalf("routes=%d", got)
	}
	if got := count(t, f, "route_points"); got != 3 {
		t.Fatalf("route_points=%d", got)
	}
	if got := count(t, f, "jobs"); got != 1 {
		t.Fatalf("jobs=%d", got)
	}
	if got := count(t, f, "change_events"); got != 1 {
		t.Fatalf("change_events=%d", got)
	}

	// Exact retry returns the original receipt and does not advance generation.
	again, err := f.s.ImportWorkoutBundle(context.Background(), f.p, bundle, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, again) {
		t.Fatalf("retry changed receipt: %s vs %s", body, again)
	}
	if got := count(t, f, "jobs"); got != 1 {
		t.Fatalf("retry jobs=%d", got)
	}

	status, err := f.s.WorkoutImport(context.Background(), f.p, bundle.WorkoutID, receipt.BundleSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, status) {
		t.Fatalf("status mismatch")
	}
}

// A v3 bundle stores routes[].source as an object. The analysis provenance
// path must not try to decode that payload as the v4 integer-index shape.
func TestV3RouteAnalysisProvenanceRegression(t *testing.T) {
	f := setup(t)
	bundle, _ := testWorkoutBundle(t, f, 3)
	phase3Observation(&bundle)
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ImportWorkoutBundle(context.Background(), f.p, bundle, raw); err != nil {
		t.Fatal(err)
	}
	runOne(t, f, "analysis.workout")
}

func TestWorkoutBundleLateSampleAndConflicts(t *testing.T) {
	f := setup(t)
	bundle, raw := testWorkoutBundle(t, f, 2)
	first, err := f.s.ImportWorkoutBundle(context.Background(), f.p, bundle, raw)
	if err != nil {
		t.Fatal(err)
	}
	var firstReceipt syncing.WorkoutImportReceipt
	_ = json.Unmarshal(first, &firstReceipt)

	// Late HealthKit sample: same Workout, new sample is appended atomically.
	late := bundleSample(bundle.WorkoutID, time.UnixMicro(*bundle.Workout.StartUs).Add(30*time.Second), 181)
	bundle.Samples = append(bundle.Samples, late)
	lateRaw, _ := json.Marshal(bundle)
	second, err := f.s.ImportWorkoutBundle(context.Background(), f.p, bundle, lateRaw)
	if err != nil {
		t.Fatal(err)
	}
	var secondReceipt syncing.WorkoutImportReceipt
	_ = json.Unmarshal(second, &secondReceipt)
	if secondReceipt.Counts.SamplesInserted != 1 || secondReceipt.CommitRevision <= firstReceipt.CommitRevision ||
		len(secondReceipt.AnalysisJobIDs) != 1 {
		t.Fatalf("unexpected late receipt: %+v", secondReceipt)
	}
	if got := count(t, f, "jobs"); got != 2 {
		t.Fatalf("jobs=%d", got)
	}

	// Changing an existing sample's value is an identity conflict, not an update.
	bundle.Samples[0].Quantity.Value += 1
	conflictRaw, _ := json.Marshal(bundle)
	_, err = f.s.ImportWorkoutBundle(context.Background(), f.p, bundle, conflictRaw)
	requireCode(t, err, "sample_identity_conflict")
}

func TestWorkoutBundleHTTPMultipartAndStatus(t *testing.T) {
	f := setup(t)
	bundle, raw := testWorkoutBundle(t, f, 1)
	handler := httpapi.New(f.s, slog.Default())
	status, payload := uploadBundleHTTP(t, handler, f.enrollment.Token, raw)
	if status != 200 {
		t.Fatalf("upload status=%d body=%s", status, payload)
	}
	var receipt syncing.WorkoutImportReceipt
	if err := json.Unmarshal(payload, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.WorkoutID != bundle.WorkoutID || receipt.IngestStatus != "committed" {
		t.Fatalf("receipt=%+v", receipt)
	}

	request := httptest.NewRequest(http.MethodGet, "/v2/sync/workout-bundles/"+bundle.WorkoutID, nil)
	request.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("status=%d", response.Code)
	}
	if !bytes.Equal(payload, response.Body.Bytes()) {
		t.Fatalf("status payload mismatch")
	}

	// The old per-sample endpoint is no longer registered.
	request = httptest.NewRequest(http.MethodPost, "/v2/sync/associations", strings.NewReader("{}"))
	request.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("old endpoint status=%d", response.Code)
	}
}

func TestWorkoutBundleScaleFiveThousand(t *testing.T) {
	f := setup(t)
	start := time.Date(2026, 11, 1, 7, 0, 0, 0, time.UTC)
	workout := workoutAt(start, 7000)
	samples := make([]syncing.Event, 0, 5000)
	series := make([]syncing.WorkoutBundleSeries, 0, 500)
	for index := 0; index < 5000; index++ {
		sample := bundleSample(workout.UUID, start.Add(time.Duration(index)*time.Second), 100+float64(index%60))
		samples = append(samples, sample)
		if index < 500 {
			end := *sample.StartUs + 2_000_000
			samples[index].EndUs = &end
			series = append(series, syncing.WorkoutBundleSeries{SampleType: kind, SampleUUID: sample.UUID, Points: []syncing.SeriesPoint{
				{StartUs: *sample.StartUs, EndUs: *sample.StartUs, Value: 1, Unit: "count/min"},
				{StartUs: *sample.StartUs + 1_000_000, EndUs: *sample.StartUs + 1_000_000, Value: 2, Unit: "count/min"},
				{StartUs: *sample.StartUs + 2_000_000, EndUs: *sample.StartUs + 2_000_000, Value: 3, Unit: "count/min"},
			}})
		}
	}
	points := make([]syncing.WorkoutBundlePoint, 2283)
	for index := range points {
		points[index] = syncing.WorkoutBundlePoint{RecordedAtUs: start.Add(time.Duration(index) * time.Second).UnixMicro(), Latitude: 31.2, Longitude: 121.4}
	}
	bundle := syncing.WorkoutBundle{ProtocolVersion: 3, DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch,
		CollectorEpoch: f.enrollment.CollectorEpoch, WorkoutID: workout.UUID, Workout: workout, Samples: samples, Series: series,
		Routes: []syncing.WorkoutBundleRoute{{RouteID: syncing.NewID(), Source: syncing.Source{BundleID: "synthetic.test"}, Points: points}}}
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	begin := time.Now()
	body, err := f.s.ImportWorkoutBundle(context.Background(), f.p, bundle, raw)
	if err != nil {
		t.Fatal(err)
	}
	var receipt syncing.WorkoutImportReceipt
	if err = json.Unmarshal(body, &receipt); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(begin)
	t.Logf("bundle_bytes=%d samples=%d series=%d route_points=%d elapsed_ms=%d", len(raw), len(samples), len(series), len(points), elapsed.Milliseconds())
	if receipt.Counts.SamplesInserted != 5000 || receipt.Counts.SeriesInserted != 500 || receipt.Counts.RoutesInserted != 1 || len(receipt.AnalysisJobIDs) != 1 {
		t.Fatalf("unexpected scale receipt: %+v", receipt)
	}
	if elapsed > 30*time.Second {
		t.Fatalf("bundle import took too long: %s", elapsed)
	}
}

func TestWorkoutBundleLateCompleteSnapshotRetainsChildren(t *testing.T) {
	f := setup(t)
	first, _ := testWorkoutBundle(t, f, 2)
	secondRoute := first.Routes[0]
	secondRoute.RouteID = syncing.NewID()
	first.Routes = append(first.Routes, secondRoute)
	firstRaw, _ := json.Marshal(first)
	firstBody, err := f.s.ImportWorkoutBundle(context.Background(), f.p, first, firstRaw)
	if err != nil {
		t.Fatal(err)
	}
	before := phase1Snapshot(t, f)

	// The older A-only read may arrive after A+B, with both completeness flags.
	snapshot := first
	snapshot.Samples = snapshot.Samples[:1]
	snapshot.Routes = snapshot.Routes[:1]
	snapshot.CompleteSampleTypes = []string{kind}
	snapshot.CompleteRoutes = true
	snapshotRaw, _ := json.Marshal(snapshot)
	body, err := f.s.ImportWorkoutBundle(context.Background(), f.p, snapshot, snapshotRaw)
	if err != nil {
		t.Fatal(err)
	}
	var receipt syncing.WorkoutImportReceipt
	if err = json.Unmarshal(body, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Counts.SamplesDeleted != 0 || receipt.Counts.AssociationsDeleted != 0 || receipt.Counts.RoutesDeleted != 0 || receipt.AnalysisStatus != "not_required" {
		t.Fatalf("snapshot absence changed facts: %+v", receipt)
	}
	if count(t, f, "health_samples") != 2 || count(t, f, "workout_samples") != 2 || count(t, f, "workout_routes") != 2 || count(t, f, "entity_tombstones") != 0 {
		t.Fatal("late snapshot removed a child")
	}
	after := phase1Snapshot(t, f)
	for table, rows := range before {
		if table != "workout_imports" && !bytes.Equal(rows, after[table]) {
			t.Fatalf("late snapshot changed %s", table)
		}
	}
	// Replaying the original complete bundle returns its original durable ACK.
	retry, err := f.s.ImportWorkoutBundle(context.Background(), f.p, first, firstRaw)
	if err != nil || !bytes.Equal(retry, firstBody) {
		t.Fatal("original receipt lost", err)
	}
	status, err := f.s.WorkoutImport(context.Background(), f.p, first.WorkoutID, syncing.BundleDigest(firstRaw))
	if err != nil || !bytes.Equal(status, firstBody) {
		t.Fatal("original status lost", err)
	}
}

func TestWorkoutBundleReceiptHistoryKeepsEveryBundle(t *testing.T) {
	f := setup(t)
	first, firstRaw := testWorkoutBundle(t, f, 1)
	firstBody, err := f.s.ImportWorkoutBundle(context.Background(), f.p, first, firstRaw)
	if err != nil {
		t.Fatal(err)
	}
	var firstReceipt syncing.WorkoutImportReceipt
	if err = json.Unmarshal(firstBody, &firstReceipt); err != nil {
		t.Fatal(err)
	}

	late := bundleSample(first.WorkoutID, time.UnixMicro(*first.Workout.StartUs).Add(30*time.Second), 181)
	second := first
	second.Samples = append(append([]syncing.Event(nil), first.Samples...), late)
	secondRaw, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	secondBody, err := f.s.ImportWorkoutBundle(context.Background(), f.p, second, secondRaw)
	if err != nil {
		t.Fatal(err)
	}
	var secondReceipt syncing.WorkoutImportReceipt
	if err = json.Unmarshal(secondBody, &secondReceipt); err != nil {
		t.Fatal(err)
	}
	if secondReceipt.BundleSHA256 == firstReceipt.BundleSHA256 {
		t.Fatal("two bundles shared a digest")
	}
	if got := count(t, f, "workout_imports"); got != 2 {
		t.Fatalf("receipt history rows=%d", got)
	}

	old, err := f.s.WorkoutImport(context.Background(), f.p, first.WorkoutID, firstReceipt.BundleSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(old, firstBody) {
		t.Fatal("exact bundle receipt changed after later import")
	}
	latest, err := f.s.WorkoutImport(context.Background(), f.p, first.WorkoutID, "")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(latest, secondBody) {
		t.Fatal("latest receipt is not the newest bundle")
	}
}

func TestWorkoutDeleteBundleIsExplicitAndIdempotent(t *testing.T) {
	f := setup(t)
	bundle, raw := testWorkoutBundle(t, f, 1)
	body, err := f.s.ImportWorkoutBundle(context.Background(), f.p, bundle, raw)
	if err != nil {
		t.Fatal(err)
	}
	var receipt syncing.WorkoutImportReceipt
	if err = json.Unmarshal(body, &receipt); err != nil {
		t.Fatal(err)
	}
	req := syncing.WorkoutDeleteRequest{ProtocolVersion: 3, DatasetID: f.p.DatasetID,
		DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch,
		WorkoutID: bundle.WorkoutID}
	if _, err = f.s.DeleteWorkoutBundle(context.Background(), f.p, req); err != nil {
		t.Fatal(err)
	}
	if got := count(t, f, "workouts"); got != 0 {
		t.Fatalf("workouts=%d", got)
	}
	if got := count(t, f, "workout_routes"); got != 0 {
		t.Fatalf("routes=%d", got)
	}
	if _, err = f.s.WorkoutImport(context.Background(), f.p, bundle.WorkoutID, receipt.BundleSHA256); err == nil {
		t.Fatal("deleted workout retained its receipt")
	} else {
		requireCode(t, err, "workout_import_not_found")
	}
	if _, err = f.s.DeleteWorkoutBundle(context.Background(), f.p, req); err != nil {
		t.Fatal("idempotent delete failed", err)
	}
}

func TestHTTPWorkoutBundleStatusByHashAndExplicitDelete(t *testing.T) {
	f := setup(t)
	_, raw := testWorkoutBundle(t, f, 2)
	handler := httpapi.New(f.s, slog.Default())
	code, body := uploadBundleHTTP(t, handler, f.enrollment.Token, raw)
	if code != 200 {
		t.Fatalf("upload status=%d body=%s", code, body)
	}
	var receipt syncing.WorkoutImportReceipt
	if err := json.Unmarshal(body, &receipt); err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet,
		"/v2/sync/workout-bundles/"+receipt.WorkoutID+"?sha256="+receipt.BundleSHA256, nil)
	request.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 || !bytes.Equal(response.Body.Bytes(), body) {
		t.Fatalf("status by hash=%d body=%s", response.Code, response.Body.Bytes())
	}

	requestBody, _ := json.Marshal(syncing.WorkoutDeleteRequest{ProtocolVersion: 3,
		DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch,
		CollectorEpoch: f.enrollment.CollectorEpoch, WorkoutID: receipt.WorkoutID})
	request, _ = http.NewRequest(http.MethodDelete, "/v2/sync/workout-bundles/"+receipt.WorkoutID, bytes.NewReader(requestBody))
	request.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("delete status=%d body=%s", response.Code, response.Body.Bytes())
	}
	request, _ = http.NewRequest(http.MethodGet, "/v2/sync/workout-bundles/"+receipt.WorkoutID, nil)
	request.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 404 {
		t.Fatalf("deleted status=%d body=%s", response.Code, response.Body.Bytes())
	}
}
