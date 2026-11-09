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
	importCalls int
	admitCalls  int
	status      syncing.WorkoutIngestStatus
	statusErr   error
	statusCalls int
}

func (s *fakeIngestStore) Authenticate(context.Context, string) (syncing.Principal, error) {
	return s.principal, nil
}

func (s *fakeIngestStore) Admit(context.Context, syncing.Principal, syncing.AdmissionLane) error {
	s.admitCalls++
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

func (s *fakeIngestStore) ImportWorkoutBundle(context.Context, syncing.Principal, syncing.WorkoutBundle, []byte) (json.RawMessage, error) {
	s.importCalls++
	return nil, syncing.Fail(500, "unexpected_import")
}

func ingestIDs() (string, string, string, string) {
	return "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222",
		"33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444"
}

func ingestPayload(datasetID, datasetEpoch, collectorEpoch, workoutID string) []byte {
	start := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC).UnixMicro()
	return []byte(fmt.Sprintf(`{"protocolVersion":4,"datasetID":%q,"datasetEpoch":%q,"collectorEpoch":%q,"workoutID":%q,"startUs":%d,"endUs":%d,"source":{"bundleID":"com.apple.health"},"workout":{"activityType":37,"durationUs":60000000},"streams":[],"series":[],"routes":[]}`,
		datasetID, datasetEpoch, collectorEpoch, workoutID, start, start+60_000_000))
}

func ingestHandler(mode syncing.WorkoutIngestMode, store *fakeIngestStore) http.Handler {
	return NewWithOptions(store, slog.New(slog.NewJSONHandler(io.Discard, nil)), Options{WorkoutUUIDIngestMode: mode})
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
	for mode, accepting := range map[syncing.WorkoutIngestMode]bool{
		syncing.WorkoutIngestLegacy: false,
		syncing.WorkoutIngestUUID:   true,
		syncing.WorkoutIngestPaused: false,
	} {
		w := httptest.NewRecorder()
		ingestHandler(mode, store).ServeHTTP(w, authorized("GET", "/v2/handshake", nil, ""))
		if w.Code != 200 {
			t.Fatalf("%s handshake status %d", mode, w.Code)
		}
		var body struct {
			Capabilities map[string]any `json:"capabilities"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Capabilities["workoutUUIDIngestVersion"] != float64(1) {
			t.Fatalf("%s version capability: %v", mode, body.Capabilities["workoutUUIDIngestVersion"])
		}
		if body.Capabilities["workoutUUIDIngestAccepting"] != accepting {
			t.Fatalf("%s accepting capability: %v", mode, body.Capabilities["workoutUUIDIngestAccepting"])
		}
	}
}

func TestWorkoutUUIDIngestPostModesAndRouting(t *testing.T) {
	datasetID, datasetEpoch, collectorEpoch, workoutID := ingestIDs()
	payload := ingestPayload(datasetID, datasetEpoch, collectorEpoch, workoutID)
	store := &fakeIngestStore{principal: syncing.Principal{DatasetID: datasetID, DeviceID: collectorEpoch, CanCollect: true}}
	multipartBody, media := plainMultipart(payload)

	for mode, want := range map[syncing.WorkoutIngestMode]struct {
		status int
		code   string
	}{
		syncing.WorkoutIngestLegacy: {409, "workout_uuid_ingest_disabled"},
		syncing.WorkoutIngestPaused: {503, "workout_uuid_ingest_paused"},
	} {
		w := httptest.NewRecorder()
		ingestHandler(mode, store).ServeHTTP(w, authorized("POST", "/v2/sync/workout-ingests/"+workoutID, multipartBody, media))
		if w.Code != want.status || errorCode(t, w) != want.code {
			t.Fatalf("%s: %d %s", mode, w.Code, w.Body.String())
		}
	}
	if store.acceptCalls != 0 || store.admitCalls != 0 {
		t.Fatalf("disabled modes reached storage: accept=%d admit=%d", store.acceptCalls, store.admitCalls)
	}

	w := httptest.NewRecorder()
	ingestHandler(syncing.WorkoutIngestUUID, store).ServeHTTP(w, authorized("POST", "/v2/sync/workout-ingests/"+workoutID, multipartBody, media))
	if w.Code != 200 {
		t.Fatalf("accept status %d: %s", w.Code, w.Body.String())
	}
	if !bytes.Equal(store.accepted, payload) {
		t.Fatal("accepted raw bytes changed")
	}
	if store.envelope.WorkoutID != workoutID || store.envelope.DatasetID != datasetID ||
		store.envelope.DatasetEpoch != datasetEpoch || store.envelope.CollectorEpoch != collectorEpoch {
		t.Fatalf("unexpected envelope: %+v", store.envelope)
	}
	if store.admitCalls != 0 {
		t.Fatal("UUID ingest used the legacy admission budget")
	}

	other := "55555555-5555-4555-8555-555555555555"
	w = httptest.NewRecorder()
	ingestHandler(syncing.WorkoutIngestUUID, store).ServeHTTP(w, authorized("POST", "/v2/sync/workout-ingests/"+other, multipartBody, media))
	if w.Code != 422 || errorCode(t, w) != "workout_ingest_mismatch" {
		t.Fatalf("mismatch: %d %s", w.Code, w.Body.String())
	}
	if store.acceptCalls != 1 {
		t.Fatalf("mismatch reached acceptance: %d", store.acceptCalls)
	}
}

func TestWorkoutUUIDIngestReadOnlyDeviceCannotPost(t *testing.T) {
	datasetID, datasetEpoch, collectorEpoch, workoutID := ingestIDs()
	payload := ingestPayload(datasetID, datasetEpoch, collectorEpoch, workoutID)
	body, media := plainMultipart(payload)
	store := &fakeIngestStore{principal: syncing.Principal{DatasetID: datasetID, DeviceID: collectorEpoch, CanCollect: false}}
	w := httptest.NewRecorder()
	ingestHandler(syncing.WorkoutIngestUUID, store).ServeHTTP(w, authorized("POST", "/v2/sync/workout-ingests/"+workoutID, body, media))
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
	h := ingestHandler(syncing.WorkoutIngestLegacy, store)
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
	ingestHandler(syncing.WorkoutIngestLegacy, fresh).ServeHTTP(w, authorized("GET", "/v2/sync/workout-ingests/"+workoutID, nil, ""))
	if w.Code != 404 || errorCode(t, w) != "workout_ingest_not_found" {
		t.Fatalf("missing status: %d %s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	ingestHandler(syncing.WorkoutIngestLegacy, fresh).ServeHTTP(w, authorized("GET", "/v2/sync/workout-ingests/not-a-uuid", nil, ""))
	if w.Code != 422 {
		t.Fatalf("invalid path id: %d", w.Code)
	}
}

func TestLegacyV4BundleEntryIsClosedInUUIDModes(t *testing.T) {
	datasetID, datasetEpoch, collectorEpoch, workoutID := ingestIDs()
	payload := ingestPayload(datasetID, datasetEpoch, collectorEpoch, workoutID)
	body, media := plainMultipart(payload)
	store := &fakeIngestStore{principal: syncing.Principal{DatasetID: datasetID, DeviceID: collectorEpoch, CanCollect: true}}
	for mode, want := range map[syncing.WorkoutIngestMode]struct {
		status int
		code   string
	}{
		syncing.WorkoutIngestUUID:   {409, "workout_uuid_ingest_required"},
		syncing.WorkoutIngestPaused: {503, "workout_uuid_ingest_paused"},
	} {
		w := httptest.NewRecorder()
		ingestHandler(mode, store).ServeHTTP(w, authorized("POST", "/v2/sync/workout-bundles", body, media))
		if w.Code != want.status || errorCode(t, w) != want.code {
			t.Fatalf("%s old v4: %d %s", mode, w.Code, w.Body.String())
		}
	}
	if store.importCalls != 0 {
		t.Fatalf("old v4 entry reached import: %d", store.importCalls)
	}

	w := httptest.NewRecorder()
	ingestHandler(syncing.WorkoutIngestLegacy, store).ServeHTTP(w, authorized("POST", "/v2/sync/workout-bundles", body, media))
	if w.Code == 409 && errorCode(t, w) == "workout_uuid_ingest_required" {
		t.Fatal("legacy mode closed the old entry")
	}
}
