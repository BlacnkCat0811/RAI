//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rai/server/internal/httpapi"
	"rai/server/internal/syncing"
)

// requestProbe observes the request body as the HTTP handler sees it. The
// net/http server may drain a bounded suffix after the handler returns, so
// reads are counted only while the handler itself is running.
type requestProbe struct {
	reads atomic.Int64
	done  chan struct{}
}

type probeBody struct {
	io.ReadCloser
	reads *atomic.Int64
}

func (b probeBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.reads.Add(int64(n))
	return n, err
}

func probeServer(t *testing.T, handler http.Handler) (*httptest.Server, *sync.Map) {
	t.Helper()
	probes := &sync.Map{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Rai-Probe")
		if id == "" {
			handler.ServeHTTP(w, r)
			return
		}
		probe := &requestProbe{done: make(chan struct{})}
		probes.Store(id, probe)
		defer close(probe.done)
		r.Body = probeBody{ReadCloser: r.Body, reads: &probe.reads}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server, probes
}

func waitProbe(t *testing.T, probes *sync.Map, id string) *requestProbe {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if value, ok := probes.Load(id); ok {
			return value.(*requestProbe)
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("probe %q was not observed", id)
	return nil
}

func waitProbeRead(t *testing.T, probe *requestProbe) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if probe.reads.Load() > 0 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("handler never started reading the request body")
}

func waitProbeDone(t *testing.T, probe *requestProbe) {
	t.Helper()
	select {
	case <-probe.done:
	case <-time.After(10 * time.Second):
		t.Fatal("handler did not exit")
	}
}

func multipartBundleBytes(t *testing.T, raw []byte) ([]byte, string) {
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
	return body.Bytes(), writer.FormDataContentType()
}

func postBundle(t *testing.T, server *httptest.Server, token, probeID string, body []byte, contentType string) (int, []byte) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/v2/sync/workout-bundles", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("X-Rai-Probe", probeID)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, payload
}

func startStreamingProbe(t *testing.T, server *httptest.Server, probes *sync.Map, token, probeID string, raw []byte) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := io.Pipe()
	mw := multipart.NewWriter(writer)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v2/sync/workout-bundles", reader)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	request.ContentLength = -1
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", mw.FormDataContentType())
	request.Header.Set("X-Rai-Probe", probeID)
	go func() {
		part, err := mw.CreateFormFile("file", "workout.json")
		if err != nil {
			writer.CloseWithError(err)
			return
		}
		if _, err = part.Write(raw[:len(raw)/2]); err != nil {
			writer.CloseWithError(err)
			return
		}
		<-ctx.Done()
		writer.CloseWithError(context.Canceled)
	}()
	go func() {
		response, err := http.DefaultClient.Do(request)
		if err == nil {
			response.Body.Close()
		}
	}()
	waitProbeRead(t, waitProbe(t, probes, probeID))
	return cancel
}

func bundleErrorCode(t *testing.T, body []byte) string {
	t.Helper()
	var payload struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode error body %q: %v", body, err)
	}
	return payload.Code
}

func assertNoBundleFacts(t *testing.T, f *fixture) {
	t.Helper()
	for _, table := range []string{"workout_imports", "health_samples"} {
		if n := count(t, f, table); n != 0 {
			t.Fatalf("%s rows created before a complete import: %d", table, n)
		}
	}
}

// A. Unauthorized v3 requests must be rejected before the body is read, must
// not consume durable admission buckets, and must not occupy the device slot.
func TestWorkoutBundleHTTPUnauthorizedRejectsBeforeBody(t *testing.T) {
	f := setup(t)
	handler := httpapi.New(f.s, slog.Default())
	server, probes := probeServer(t, handler)
	_, raw := testWorkoutBundle(t, f, 3)
	body, contentType := multipartBundleBytes(t, raw)

	status, payload := postBundle(t, server, strings.Repeat("x", 64), "unauthorized", body, contentType)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d body=%s", status, payload)
	}
	probe := waitProbe(t, probes, "unauthorized")
	waitProbeDone(t, probe)
	if probe.reads.Load() != 0 {
		t.Fatalf("unauthorized request read %d body bytes", probe.reads.Load())
	}
	if n := count(t, f, "sync_request_budgets"); n != 0 {
		t.Fatalf("unauthorized request allocated %d admission rows", n)
	}
	assertNoBundleFacts(t, f)

	// The same device must still be able to send a legitimate request.
	status, payload = postBundle(t, server, f.enrollment.Token, "authorized", body, contentType)
	if status != http.StatusOK {
		t.Fatalf("legitimate request after unauthorized one: status = %d body=%s", status, payload)
	}
}

// B1. A durable admission rejection must happen before any body read and must
// leave no import, raw facts or slot debt behind.
func TestWorkoutBundleHTTPDurableRejectBeforeBody(t *testing.T) {
	f := setup(t)
	handler := httpapi.New(f.s, slog.Default())
	server, probes := probeServer(t, handler)
	_, raw := testWorkoutBundle(t, f, 3)
	body, contentType := multipartBundleBytes(t, raw)

	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := f.s.Admit(ctx, f.p, syncing.UploadLane); err != nil {
			t.Fatalf("prime admission %d: %v", i, err)
		}
	}
	status, payload := postBundle(t, server, f.enrollment.Token, "durable-reject", body, contentType)
	if status != http.StatusTooManyRequests {
		t.Fatalf("status = %d body=%s", status, payload)
	}
	if code := bundleErrorCode(t, payload); code != "device_rate_exceeded" {
		t.Fatalf("code = %q body=%s", code, payload)
	}
	probe := waitProbe(t, probes, "durable-reject")
	waitProbeDone(t, probe)
	if probe.reads.Load() != 0 {
		t.Fatalf("durably rejected request read %d body bytes", probe.reads.Load())
	}
	assertNoBundleFacts(t, f)

	// The rejection itself must not consume an extra window; the device can
	// enter again after the fixed burst interval.
	time.Sleep(2100 * time.Millisecond)
	status, payload = postBundle(t, server, f.enrollment.Token, "durable-recovered", body, contentType)
	if status != http.StatusOK {
		t.Fatalf("request after budget recovery: status = %d body=%s", status, payload)
	}
}

// B2. A storage failure inside admission must stop the request before the
// body is read and must roll back every budget bucket.
func TestWorkoutBundleHTTPAdmissionStorageFailureBeforeBody(t *testing.T) {
	f := setup(t)
	handler := httpapi.New(f.s, slog.Default())
	server, probes := probeServer(t, handler)
	_, raw := testWorkoutBundle(t, f, 3)
	body, contentType := multipartBundleBytes(t, raw)

	ctx := context.Background()
	_, err := f.s.Pool.Exec(ctx, `CREATE FUNCTION fail_http_bundle_budget() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.device_id IS NOT NULL THEN RAISE EXCEPTION 'synthetic HTTP admission failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER fail_http_bundle_budget BEFORE INSERT OR UPDATE ON sync_request_budgets FOR EACH ROW EXECUTE FUNCTION fail_http_bundle_budget()`)
	if err != nil {
		t.Fatal(err)
	}
	status, payload := postBundle(t, server, f.enrollment.Token, "storage-failure", body, contentType)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%s", status, payload)
	}
	probe := waitProbe(t, probes, "storage-failure")
	waitProbeDone(t, probe)
	if probe.reads.Load() != 0 {
		t.Fatalf("failed admission read %d body bytes", probe.reads.Load())
	}
	if n := count(t, f, "sync_request_budgets"); n != 0 {
		t.Fatalf("failed admission left %d budget rows", n)
	}
	assertNoBundleFacts(t, f)

	if _, err = f.s.Pool.Exec(ctx, `DROP TRIGGER fail_http_bundle_budget ON sync_request_budgets`); err != nil {
		t.Fatal(err)
	}
	status, payload = postBundle(t, server, f.enrollment.Token, "storage-recovered", body, contentType)
	if status != http.StatusOK {
		t.Fatalf("request after storage recovery: status = %d body=%s", status, payload)
	}
}

// B3. A busy process-local device slot must reject a second request before its
// body is read, and the same device must be admitted again once the first
// request ends.
func TestWorkoutBundleHTTPDeviceSlotBusyBeforeBody(t *testing.T) {
	f := setup(t)
	handler := httpapi.New(f.s, slog.Default())
	server, probes := probeServer(t, handler)
	_, raw := testWorkoutBundle(t, f, 3)
	body, contentType := multipartBundleBytes(t, raw)

	stop := startStreamingProbe(t, server, probes, f.enrollment.Token, "slot-holder", raw)
	first := waitProbe(t, probes, "slot-holder")
	defer func() {
		stop()
		waitProbeDone(t, first)
	}()

	status, payload := postBundle(t, server, f.enrollment.Token, "slot-busy", body, contentType)
	if status != http.StatusTooManyRequests {
		t.Fatalf("status = %d body=%s", status, payload)
	}
	if code := bundleErrorCode(t, payload); code != "device_upload_capacity_exceeded" {
		t.Fatalf("code = %q body=%s", code, payload)
	}
	second := waitProbe(t, probes, "slot-busy")
	waitProbeDone(t, second)
	if second.reads.Load() != 0 {
		t.Fatalf("busy-slot request read %d body bytes", second.reads.Load())
	}
	assertNoBundleFacts(t, f)

	// End the holder and prove the released slot is immediately reusable.
	// The busy attempt already consumed its admission window before the
	// handler rejected the slot, so wait for the fixed burst interval before
	// the reuse request.
	stop()
	waitProbeDone(t, first)
	time.Sleep(2100 * time.Millisecond)
	status, payload = postBundle(t, server, f.enrollment.Token, "slot-reuse", body, contentType)
	if status != http.StatusOK {
		t.Fatalf("request after slot release: status = %d body=%s", status, payload)
	}
}

// C. A client cancellation during body reading must roll back the import and
// release the device slot so the same device can immediately retry.
func TestWorkoutBundleHTTPClientCancelReleasesDeviceSlot(t *testing.T) {
	f := setup(t)
	handler := httpapi.New(f.s, slog.Default())
	server, probes := probeServer(t, handler)
	_, raw := testWorkoutBundle(t, f, 3)
	body, contentType := multipartBundleBytes(t, raw)

	stop := startStreamingProbe(t, server, probes, f.enrollment.Token, "cancel-holder", raw)
	holder := waitProbe(t, probes, "cancel-holder")
	stop()
	waitProbeDone(t, holder)
	assertNoBundleFacts(t, f)

	// The same device must be able to enter immediately after cancellation.
	status, payload := postBundle(t, server, f.enrollment.Token, "cancel-retry", body, contentType)
	if status != http.StatusOK {
		t.Fatalf("retry after cancel: status = %d body=%s", status, payload)
	}
	var receipt syncing.WorkoutImportReceipt
	if err := json.Unmarshal(payload, &receipt); err != nil {
		t.Fatalf("decode receipt %q: %v", payload, err)
	}
	if receipt.BundleSHA256 == "" {
		t.Fatalf("successful retry returned no receipt: %s", payload)
	}
}
