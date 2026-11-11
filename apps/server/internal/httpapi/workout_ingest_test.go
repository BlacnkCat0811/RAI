package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"rai/server/internal/syncing"
)

type fakeIngestStore struct {
	syncing.Store
	principal   syncing.Principal
	metadata    syncing.Metadata
	accepted    []byte
	envelope    syncing.WorkoutIngestEnvelope
	acceptCalls int
	status      syncing.WorkoutIngestStatus
	statusErr   error
	statusCalls int
}

func (s *fakeIngestStore) Authenticate(context.Context, string) (syncing.Principal, error) {
	return s.principal, nil
}

func (s *fakeIngestStore) Admit(context.Context, syncing.Principal, syncing.AdmissionLane) error {
	return nil
}

func (s *fakeIngestStore) Metadata(context.Context, syncing.Principal) (syncing.Metadata, error) {
	return s.metadata, nil
}

func (s *fakeIngestStore) AcceptWorkoutIngest(_ context.Context, _ syncing.Principal, envelope syncing.WorkoutIngestEnvelope, raw []byte) (json.RawMessage, error) {
	s.acceptCalls++
	s.envelope = envelope
	s.accepted = append([]byte(nil), raw...)
	return json.RawMessage(fmt.Sprintf(`{"workoutID":%q,"ingestStatus":"accepted","acceptedAt":"2026-11-09T00:00:00Z"}`, envelope.WorkoutID)), nil
}

func (s *fakeIngestStore) WorkoutIngest(context.Context, syncing.Principal, string) (syncing.WorkoutIngestStatus, error) {
	s.statusCalls++
	return s.status, s.statusErr
}

func ingestIDs() (string, string, string, string) {
	return "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444"
}

func ingestV6Payload(datasetID, datasetEpoch, collectorEpoch, workoutID string) []byte {
	start := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC).UnixMicro()
	return []byte(fmt.Sprintf(`{"protocolVersion":6,"datasetID":%q,"datasetEpoch":%q,"collectorEpoch":%q,"workoutID":%q,"startUs":%d,"endUs":%d,"source":{"bundleID":"com.apple.health"},"workout":{"activityType":37,"durationUs":60000000},"streams":[{"type":"HKQuantityTypeIdentifierHeartRate","unit":"count/min","samples":[[0,60000000,92]]}],"routes":[{"points":[[0,31.2,121.4]]}]}`,
		datasetID, datasetEpoch, collectorEpoch, workoutID, start, start+60_000_000))
}

func ingestHandler(store *fakeIngestStore) http.Handler {
	return New(store, slog.New(slog.NewJSONHandler(io.Discard, nil)))
}

func authorized(method, target string, body []byte, contentType string) *http.Request {
	r := httptest.NewRequest(method, target, bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("token-", 12))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	return r
}

func errorCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var e syncing.Error
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatalf("decode error body: %v (%s)", err, w.Body.String())
	}
	return e.Code
}

func TestWorkoutUUIDIngestHandshakeCapability(t *testing.T) {
	datasetID, datasetEpoch, collectorEpoch, _ := ingestIDs()
	store := &fakeIngestStore{
		principal: syncing.Principal{DatasetID: datasetID, DeviceID: collectorEpoch, CanCollect: true},
		metadata:  syncing.Metadata{DatasetID: datasetID, DatasetEpoch: datasetEpoch, DeviceID: collectorEpoch, DatasetStatus: "active"},
	}
	w := httptest.NewRecorder()
	ingestHandler(store).ServeHTTP(w, authorized("GET", "/v2/handshake", nil, ""))
	if w.Code != 200 {
		t.Fatalf("handshake status %d", w.Code)
	}
	var body struct {
		ProtocolVersion int            `json:"protocolVersion"`
		Capabilities    map[string]any `json:"capabilities"`
		Limits          map[string]any `json:"limits"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Limits["workoutUploads"] != float64(DeviceWorkoutUploadSlots) ||
		body.Limits["healthUploads"] != float64(1) || body.Limits["processUploads"] != float64(ProcessUploadSlots) ||
		body.Limits["processDeviceUploads"] != float64(DeviceWorkoutUploadSlots+1) {
		t.Fatalf("upload budgets: %+v", body.Limits)
	}
	if body.ProtocolVersion != 3 {
		t.Fatalf("session protocol shape changed: %d", body.ProtocolVersion)
	}
	if body.Capabilities["workoutPayloadVersion"] != float64(6) ||
		body.Capabilities["bundleGzipPart"] != true {
		t.Fatalf("workout payload capabilities: %+v", body.Capabilities)
	}
	for _, legacy := range []string{"workoutBundles", "bundleProtocolVersion", "bundleObservationVersion", "bundleCompact", "explicitChildDeletions"} {
		if _, ok := body.Capabilities[legacy]; ok {
			t.Fatalf("removed capability %s advertised", legacy)
		}
	}
	if body.Capabilities["workoutUUIDIngestVersion"] != float64(1) ||
		body.Capabilities["workoutUUIDIngestAccepting"] != true {
		t.Fatalf("UUID ingest capabilities: %+v", body.Capabilities)
	}
}

func TestWorkoutUUIDIngestAcceptsV6AndRoutes(t *testing.T) {
	datasetID, datasetEpoch, collectorEpoch, workoutID := ingestIDs()
	payload := ingestV6Payload(datasetID, datasetEpoch, collectorEpoch, workoutID)
	store := &fakeIngestStore{principal: syncing.Principal{DatasetID: datasetID, DeviceID: collectorEpoch, CanCollect: true}}
	multipartBody, media := plainMultipart(payload)

	w := httptest.NewRecorder()
	ingestHandler(store).ServeHTTP(w, authorized("POST", "/v2/sync/workout-ingests/"+workoutID, multipartBody, media))
	if w.Code != 200 {
		t.Fatalf("accept status %d: %s", w.Code, w.Body.String())
	}
	if !bytes.Equal(store.accepted, payload) {
		t.Fatal("accepted raw bytes changed")
	}
	if store.envelope.ProtocolVersion != 6 || store.envelope.WorkoutID != workoutID ||
		store.envelope.DatasetID != datasetID || store.envelope.DatasetEpoch != datasetEpoch ||
		store.envelope.CollectorEpoch != collectorEpoch {
		t.Fatalf("unexpected envelope: %+v", store.envelope)
	}

	other := "55555555-5555-4555-8555-555555555555"
	w = httptest.NewRecorder()
	ingestHandler(store).ServeHTTP(w, authorized("POST", "/v2/sync/workout-ingests/"+other, multipartBody, media))
	if w.Code != 422 || errorCode(t, w) != "workout_ingest_mismatch" {
		t.Fatalf("mismatch: %d %s", w.Code, w.Body.String())
	}
	if store.acceptCalls != 1 {
		t.Fatalf("mismatch reached acceptance: %d", store.acceptCalls)
	}
}

func TestWorkoutUUIDIngestRejectsOlderPayloadVersions(t *testing.T) {
	datasetID, datasetEpoch, collectorEpoch, workoutID := ingestIDs()
	store := &fakeIngestStore{principal: syncing.Principal{DatasetID: datasetID, DeviceID: collectorEpoch, CanCollect: true}}
	for _, version := range []int{3, 4, 5} {
		payload := bytes.Replace(ingestV6Payload(datasetID, datasetEpoch, collectorEpoch, workoutID),
			[]byte(`"protocolVersion":6`), []byte(fmt.Sprintf(`"protocolVersion":%d`, version)), 1)
		body, media := plainMultipart(payload)
		w := httptest.NewRecorder()
		ingestHandler(store).ServeHTTP(w, authorized("POST", "/v2/sync/workout-ingests/"+workoutID, body, media))
		if w.Code != 422 || errorCode(t, w) != "unsupported_payload" {
			t.Fatalf("v%d: %d %s", version, w.Code, w.Body.String())
		}
	}
	if store.acceptCalls != 0 {
		t.Fatalf("older payloads reached acceptance: %d", store.acceptCalls)
	}
}

func TestWorkoutUUIDIngestReadOnlyDeviceCannotPost(t *testing.T) {
	datasetID, datasetEpoch, collectorEpoch, workoutID := ingestIDs()
	payload := ingestV6Payload(datasetID, datasetEpoch, collectorEpoch, workoutID)
	body, media := plainMultipart(payload)
	store := &fakeIngestStore{principal: syncing.Principal{DatasetID: datasetID, DeviceID: collectorEpoch, CanCollect: false}}
	w := httptest.NewRecorder()
	ingestHandler(store).ServeHTTP(w, authorized("POST", "/v2/sync/workout-ingests/"+workoutID, body, media))
	if w.Code != 403 || errorCode(t, w) != "read_only_device" {
		t.Fatalf("read-only POST: %d %s", w.Code, w.Body.String())
	}
	if store.acceptCalls != 0 {
		t.Fatal("read-only device reached acceptance")
	}
}

func TestWorkoutUUIDIngestStatusAndLimit(t *testing.T) {
	datasetID, _, collectorEpoch, workoutID := ingestIDs()
	revision := int64(7)
	store := &fakeIngestStore{
		principal: syncing.Principal{DatasetID: datasetID, DeviceID: collectorEpoch},
		status: syncing.WorkoutIngestStatus{WorkoutID: workoutID, IngestStatus: "committed",
			CommitRevision: &revision, AnalysisStatus: "queued", AnalysisJobIDs: []string{"job-1"}},
	}
	h := ingestHandler(store)
	for i := 0; i < statusBurst; i++ {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, authorized("GET", "/v2/sync/workout-ingests/"+workoutID, nil, ""))
		if w.Code != 200 {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, authorized("GET", "/v2/sync/workout-ingests/"+workoutID, nil, ""))
	if w.Code != 429 || errorCode(t, w) != "status_rate_exceeded" {
		t.Fatalf("status limit: %d %s", w.Code, w.Body.String())
	}
	if store.statusCalls != statusBurst {
		t.Fatalf("limited request reached storage: %d", store.statusCalls)
	}

	fresh := &fakeIngestStore{principal: store.principal, statusErr: syncing.Fail(404, "workout_ingest_not_found")}
	w = httptest.NewRecorder()
	ingestHandler(fresh).ServeHTTP(w, authorized("GET", "/v2/sync/workout-ingests/"+workoutID, nil, ""))
	if w.Code != 404 || errorCode(t, w) != "workout_ingest_not_found" {
		t.Fatalf("missing status: %d %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	ingestHandler(fresh).ServeHTTP(w, authorized("GET", "/v2/sync/workout-ingests/not-a-uuid", nil, ""))
	if w.Code != 422 {
		t.Fatalf("invalid path id: %d", w.Code)
	}
}

func TestRetiredWorkoutBundleEntryIsGone(t *testing.T) {
	datasetID, datasetEpoch, collectorEpoch, workoutID := ingestIDs()
	payload := ingestV6Payload(datasetID, datasetEpoch, collectorEpoch, workoutID)
	body, media := plainMultipart(payload)
	store := &fakeIngestStore{principal: syncing.Principal{DatasetID: datasetID, DeviceID: collectorEpoch, CanCollect: true}}
	for _, request := range []*http.Request{
		authorized("POST", "/v2/sync/workout-bundles", body, media),
		authorized("GET", "/v2/sync/workout-bundles/"+workoutID, nil, ""),
		authorized("DELETE", "/v2/sync/workout-bundles/"+workoutID, nil, ""),
	} {
		w := httptest.NewRecorder()
		ingestHandler(store).ServeHTTP(w, request)
		if w.Code != 404 {
			t.Fatalf("%s %s: %d", request.Method, request.URL.Path, w.Code)
		}
	}
	if store.acceptCalls != 0 {
		t.Fatalf("retired entry reached storage: %d", store.acceptCalls)
	}
}

func TestWorkoutUUIDIngestV6PathMismatch(t *testing.T) {
	datasetID, datasetEpoch, collectorEpoch, workoutID := ingestIDs()
	body, media := plainMultipart(ingestV6Payload(datasetID, datasetEpoch, collectorEpoch, workoutID))
	store := &fakeIngestStore{principal: syncing.Principal{DatasetID: datasetID, DeviceID: collectorEpoch, CanCollect: true}}
	w := httptest.NewRecorder()
	ingestHandler(store).ServeHTTP(w, authorized("POST", "/v2/sync/workout-ingests/"+syncing.NewID(), body, media))
	if w.Code != 422 || errorCode(t, w) != "workout_ingest_mismatch" {
		t.Fatalf("v6 mismatch: %d %s", w.Code, w.Body.String())
	}
	if store.acceptCalls != 0 {
		t.Fatal("mismatch reached storage")
	}
}
