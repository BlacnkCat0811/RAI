//go:build integration

package integration

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"rai/server/internal/httpapi"
	"rai/server/internal/syncing"
)

// nearestRank returns the nearest-rank percentile of an unsorted sample.
func nearestRank(values []float64, percentile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	index := int(float64(len(sorted))*percentile+0.999999) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

func perfPercentile(values []float64, percentile float64) float64 {
	return nearestRank(values, percentile)
}

func perfEnvInt(t *testing.T, name string, fallback int) int {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		t.Fatalf("%s must be a positive integer, got %q", name, raw)
	}
	return value
}

func perfEnvString(name, fallback string) string {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	return raw
}

// ingestWire builds the production wire shape: one gzip file part whose
// filename and media type match the iOS client.
func ingestWire(t *testing.T, raw []byte) (*bytes.Buffer, string) {
	t.Helper()
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	if _, err := gz.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": {`form-data; name="file"; filename="workout.json.gz"`},
		"Content-Type":        {"application/gzip"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(compressed.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, writer.FormDataContentType()
}

func ingestRequest(t *testing.T, method, url, token, contentType string, body io.Reader) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return req
}

// TestWorkoutUUIDIngestLockIndependence holds the dataset row lock an older
// fact write would hold and proves the new acceptance and status endpoints do
// not queue behind it.
func TestWorkoutUUIDIngestLockIndependence(t *testing.T) {
	f := setup(t)
	server := httptest.NewServer(httpapi.New(f.s, slog.New(slog.NewJSONHandler(io.Discard, nil))))
	defer server.Close()
	client := server.Client()

	ctx := context.Background()
	tx, err := f.s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT id FROM datasets WHERE id=$1 FOR UPDATE`, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}

	missing := syncing.NewID()
	statusStart := time.Now()
	resp, err := client.Do(ingestRequest(t, "GET", server.URL+"/v2/sync/workout-ingests/"+missing, f.enrollment.Token, "", nil))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	statusElapsed := time.Since(statusStart)
	if resp.StatusCode != 404 {
		t.Fatalf("status under dataset lock: %d", resp.StatusCode)
	}

	id := syncing.NewID()
	raw := ingestRaw(t, f.p.DatasetID, f.enrollment.DatasetEpoch, f.enrollment.CollectorEpoch, id, 121)
	body, contentType := ingestWire(t, raw)
	postDone := make(chan int, 1)
	postStart := time.Now()
	go func() {
		resp, err := client.Do(ingestRequest(t, "POST", server.URL+"/v2/sync/workout-ingests/"+id,
			f.enrollment.Token, contentType, body))
		if err != nil {
			postDone <- 0
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		postDone <- resp.StatusCode
	}()
	var postStatus int
	select {
	case postStatus = <-postDone:
	case <-time.After(2 * time.Second):
		t.Fatal("acceptance queued behind the dataset row lock")
	}
	postElapsed := time.Since(postStart)
	if postStatus != 200 {
		t.Fatalf("acceptance under dataset lock: %d", postStatus)
	}
	if postElapsed > 500*time.Millisecond || statusElapsed > 500*time.Millisecond {
		t.Fatalf("dataset lock contention exceeded budget: post=%v status=%v", postElapsed, statusElapsed)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("dataset_lock_independence post=%v status=%v", postElapsed, statusElapsed)
}

// TestWorkoutUUIDIngestPerf records the plan's acceptance latency budget on a
// normal local build against disposable PostgreSQL. It is skipped unless
// explicitly enabled; the race-enabled completion gate never measures timings.
//
// Measured:
//   - POST loopback receipt latency for a compressed ~1 KiB payload, which is
//     the "request body received -> acceptance receipt" budget (the loopback
//     body transfer is included and only a few hundred microseconds).
//   - GET status latency.
//   - POST while another transaction holds the dataset row lock.
//
// The iOS "click -> receipt" number additionally includes the first HealthKit
// read; that is measured on-device and is not claimed here.
func TestWorkoutUUIDIngestPerf(t *testing.T) {
	if os.Getenv("RAI_WORKOUT_INGEST_PERF") == "" {
		t.Skip("set RAI_WORKOUT_INGEST_PERF=1 to measure acceptance latency")
	}
	iterations := perfEnvInt(t, "RAI_WORKOUT_INGEST_PERF_ITERATIONS", 30)
	if iterations < 10 {
		t.Fatalf("need at least 10 iterations, got %d", iterations)
	}
	f := setup(t)
	server := httptest.NewServer(httpapi.New(f.s, slog.New(slog.NewJSONHandler(io.Discard, nil))))
	defer server.Close()
	client := server.Client()
	client.Timeout = 30 * time.Second

	type wire struct {
		id          string
		body        *bytes.Buffer
		contentType string
		payload     int
	}
	build := func(value float64) wire {
		id := syncing.NewID()
		raw := ingestRaw(t, f.p.DatasetID, f.enrollment.DatasetEpoch, f.enrollment.CollectorEpoch, id, value)
		body, contentType := ingestWire(t, raw)
		return wire{id: id, body: body, contentType: contentType, payload: len(raw)}
	}
	post := func(w wire) (time.Duration, int) {
		start := time.Now()
		resp, err := client.Do(ingestRequest(t, "POST", server.URL+"/v2/sync/workout-ingests/"+w.id,
			f.enrollment.Token, w.contentType, w.body))
		if err != nil {
			t.Fatalf("POST %s: %v", w.id, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return time.Since(start), resp.StatusCode
	}
	status := func(id string) (time.Duration, int) {
		start := time.Now()
		resp, err := client.Do(ingestRequest(t, "GET", server.URL+"/v2/sync/workout-ingests/"+id,
			f.enrollment.Token, "", nil))
		if err != nil {
			t.Fatalf("GET %s: %v", id, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return time.Since(start), resp.StatusCode
	}

	warmups := perfEnvInt(t, "RAI_WORKOUT_INGEST_PERF_WARMUPS", 5)
	for i := 0; i < warmups; i++ {
		if _, code := post(build(float64(100 + i))); code != 200 {
			t.Fatalf("warmup POST: %d", code)
		}
	}
	postValues := make([]float64, 0, iterations)
	statusValues := make([]float64, 0, iterations)
	ids := make([]string, 0, iterations)
	payloadBytes := 0
	failures := 0
	for i := 0; i < iterations; i++ {
		w := build(float64(120 + i))
		payloadBytes = w.payload
		elapsed, code := post(w)
		if code != 200 {
			failures++
			continue
		}
		postValues = append(postValues, float64(elapsed.Microseconds())/1000)
		ids = append(ids, w.id)
	}
	// The status endpoint is deliberately rate limited per device (60/min,
	// burst 10). Pacing keeps the measurement on request latency instead of
	// measuring the limiter; one token refills per second.
	statusInterval := perfEnvInt(t, "RAI_WORKOUT_INGEST_PERF_STATUS_INTERVAL_MS", 1100)
	for i, id := range ids {
		if i > 0 {
			time.Sleep(time.Duration(statusInterval) * time.Millisecond)
		}
		elapsed, code := status(id)
		if code != 200 {
			failures++
			continue
		}
		statusValues = append(statusValues, float64(elapsed.Microseconds())/1000)
	}
	if failures > 0 {
		t.Fatalf("acceptance perf failures=%d", failures)
	}
	postP50, postP95 := perfPercentile(postValues, 0.50), perfPercentile(postValues, 0.95)
	statusP50, statusP95 := perfPercentile(statusValues, 0.50), perfPercentile(statusValues, 0.95)

	// Contended acceptance: hold the dataset row lock a legacy fact write would
	// take while the POST completes. The budget is 500 ms.
	ctx := context.Background()
	tx, err := f.s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `SELECT id FROM datasets WHERE id=$1 FOR UPDATE`, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}
	contended, code := post(build(999))
	if code != 200 {
		t.Fatalf("contended POST: %d", code)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if contended > 500*time.Millisecond {
		t.Fatalf("contended acceptance P100 %.1fms > 500ms", float64(contended.Microseconds())/1000)
	}
	t.Logf("workout_uuid_ingest_perf n=%d failures=%d post_p50=%.1fms post_p95=%.1fms status_p50=%.1fms status_p95=%.1fms contended_post=%.1fms payload_bytes=%d",
		iterations, failures, postP50, postP95, statusP50, statusP95, float64(contended.Microseconds())/1000, payloadBytes)
	if postP95 > 300 {
		t.Fatalf("acceptance P95 %.1fms > 300ms", postP95)
	}
	if statusP95 > 300 {
		t.Fatalf("status P95 %.1fms > 300ms", statusP95)
	}
}
