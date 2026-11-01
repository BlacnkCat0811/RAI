package httpapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"rai/server/internal/syncing"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type transportStore struct {
	syncing.Store
	imports atomic.Int32
}

func (s *transportStore) Authenticate(context.Context, string) (syncing.Principal, error) {
	return syncing.Principal{DatasetID: "11111111-1111-4111-8111-111111111111", DeviceID: "22222222-2222-4222-8222-222222222222", CanCollect: true}, nil
}
func (s *transportStore) Admit(context.Context, syncing.Principal, syncing.AdmissionLane) error {
	return nil
}
func (s *transportStore) ImportWorkoutBundle(context.Context, syncing.Principal, syncing.WorkoutBundle, []byte) (json.RawMessage, error) {
	s.imports.Add(1)
	return json.RawMessage(`{"ingestStatus":"committed"}`), nil
}
func transportFixture() []byte {
	return []byte(`{"protocolVersion":3,"datasetID":"11111111-1111-4111-8111-111111111111","datasetEpoch":"22222222-2222-4222-8222-222222222222","collectorEpoch":"33333333-3333-4333-8333-333333333333","workoutID":"44444444-4444-4444-8444-444444444444","workout":{"op":"upsert","kind":"workout","type":"HKWorkoutTypeIdentifier","uuid":"44444444-4444-4444-8444-444444444444","startUs":1793433600000000,"endUs":1793433601000000,"source":{"bundleID":"synthetic.test"},"workout":{"activityType":37,"durationUs":1000000}},"samples":[]}`)
}
func realProductionServer(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	s := httptest.NewUnstartedServer(h)
	s.Config = NewHTTPServer("", h)
	s.Start()
	t.Cleanup(s.Close)
	return s
}

// Real TCP sockets with the executable's actual http.Server settings. The
// timers pace bytes in a live body stream; they do not replace HTTP execution.
func TestBundleProductionServerSlowUpload(t *testing.T) {
	if os.Getenv("RAI_PHASE4_SLOW") == "" {
		t.Skip("explicit 5/15/30s real socket pacing run")
	}
	for _, pause := range []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second} {
		t.Run(pause.String(), func(t *testing.T) {
			t.Parallel()
			store := &transportStore{}
			server := realProductionServer(t, New(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
			var zipped bytes.Buffer
			gz := gzip.NewWriter(&zipped)
			_, _ = gz.Write(transportFixture())
			_ = gz.Close()
			var wire bytes.Buffer
			mw := multipart.NewWriter(&wire)
			p, _ := mw.CreatePart(mapHeader("application/gzip"))
			_, _ = p.Write(zipped.Bytes())
			_ = mw.Close()
			body, media := wire.Bytes(), mw.FormDataContentType()
			reader, writer := io.Pipe()
			req, _ := http.NewRequest("POST", server.URL+"/v2/sync/workout-bundles", reader)
			req.Header.Set("Authorization", "Bearer "+strings.Repeat("x", 64))
			req.Header.Set("Content-Type", media)
			req.ContentLength = int64(len(body))
			done := make(chan error, 1)
			go func() {
				defer writer.Close()
				_, e := writer.Write(body[:len(body)/2])
				if e == nil {
					timer := time.NewTimer(pause)
					<-timer.C
					_, e = writer.Write(body[len(body)/2:])
				}
				done <- e
			}()
			response, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if err = <-done; err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != 200 || store.imports.Load() != 1 {
				t.Fatal("legitimate paced upload rejected", response.StatusCode)
			}
		})
	}
}
func TestBundleTimeoutAndCancellationClassification(t *testing.T) {
	t.Run("read-timeout", func(t *testing.T) {
		store := &transportStore{}
		api := New(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
		// Exercise the same production handler and socket controller with a short
		// phase deadline so CI need not hold a malicious connection for 180s.
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v2/sync/workout-bundles" {
				r.Body = &deadlineBody{ReadCloser: r.Body, w: w}
			}
			api.ServeHTTP(w, r)
		})
		server := realProductionServer(t, h)
		body, media := plainMultipart(transportFixture())
		reader, writer := io.Pipe()
		defer writer.Close()
		req, _ := http.NewRequest("POST", server.URL+"/v2/sync/workout-bundles", reader)
		req.Header.Set("Authorization", "Bearer "+strings.Repeat("x", 64))
		req.Header.Set("Content-Type", media)
		go func() { _, _ = writer.Write(body[:len(body)/2]) }()
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != 408 || !bytes.Contains(raw, []byte("upload_read_timeout")) || store.imports.Load() != 0 {
			t.Fatalf("timeout classification status=%d", response.StatusCode)
		}
	})
	t.Run("client-cancel", func(t *testing.T) {
		store := &transportStore{}
		server := realProductionServer(t, New(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
		conn, err := net.Dial("tcp", server.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(conn, "POST /v2/sync/workout-bundles HTTP/1.1\r\nHost: test\r\nAuthorization: Bearer "+strings.Repeat("x", 64)+"\r\nContent-Type: multipart/form-data; boundary=x\r\nContent-Length: 10000\r\n\r\n--x\r\n")
		_ = conn.Close()
		server.Close()
		if store.imports.Load() != 0 {
			t.Fatal("cancel entered import")
		}
	})
}

type deadlineBody struct {
	io.ReadCloser
	w http.ResponseWriter
}

func (r *deadlineBody) Read(p []byte) (int, error) {
	_ = http.NewResponseController(r.w).SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	return r.ReadCloser.Read(p)
}

func TestBundleProductionBodyBudget(t *testing.T) {
	if os.Getenv("RAI_PHASE4_LONG") == "" {
		t.Skip("explicit ~180s production deadline boundary run")
	}
	for _, over := range []bool{false, true} {
		t.Run(map[bool]string{false: "near-179s", true: "over-180s"}[over], func(t *testing.T) {
			t.Parallel()
			store := &transportStore{}
			server := realProductionServer(t, New(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
			body, media := plainMultipart(transportFixture())
			reader, writer := io.Pipe()
			defer writer.Close()
			req, _ := http.NewRequest("POST", server.URL+"/v2/sync/workout-bundles", reader)
			req.Header.Set("Authorization", "Bearer "+strings.Repeat("x", 64))
			req.Header.Set("Content-Type", media)
			req.ContentLength = int64(len(body))
			cancel := make(chan struct{})
			defer close(cancel)
			go func() {
				_, _ = writer.Write(body[:len(body)/2])
				timer := time.NewTimer(BundleBodyBudget - time.Second)
				if over {
					timer.Stop()
					<-cancel
					return
				}
				select {
				case <-timer.C:
					_, _ = writer.Write(body[len(body)/2:])
					_ = writer.Close()
				case <-cancel:
					timer.Stop()
				}
			}()
			start := time.Now()
			response, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if over {
				if response.StatusCode != 408 || !bytes.Contains(raw, []byte("upload_read_timeout")) || store.imports.Load() != 0 {
					t.Fatal("body deadline failed", response.StatusCode)
				}
			} else if response.StatusCode != 200 || store.imports.Load() != 1 {
				t.Fatal("near deadline legitimate upload", response.StatusCode)
			}
			t.Logf("production_body_budget=%s actual_elapsed=%s status=%d", BundleBodyBudget, time.Since(start), response.StatusCode)
		})
	}
}

type delayedACKWriter struct{ http.ResponseWriter }

func (w delayedACKWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w delayedACKWriter) Write(p []byte) (int, error) {
	timer := time.NewTimer(ACKWriteBudget + time.Second)
	<-timer.C
	return w.ResponseWriter.Write(p)
}
func TestBundleACKWriteDeadlineAfterCommit(t *testing.T) {
	if os.Getenv("RAI_PHASE4_SLOW") == "" {
		t.Skip("explicit 6s ACK write deadline test")
	}
	store := &transportStore{}
	var logs bytes.Buffer
	api := New(store, slog.New(slog.NewJSONHandler(&logs, nil)))
	server := realProductionServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { api.ServeHTTP(delayedACKWriter{w}, r) }))
	body, media := plainMultipart(transportFixture())
	req, _ := http.NewRequest("POST", server.URL+"/v2/sync/workout-bundles", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("x", 64))
	req.Header.Set("Content-Type", media)
	resp, err := server.Client().Do(req)
	if err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	if store.imports.Load() != 1 || !strings.Contains(logs.String(), "ack_write_failed") {
		t.Fatal("ACK deadline did not preserve committed outcome")
	}
}

type pacedBody struct {
	data     *bytes.Reader
	quantum  int
	interval time.Duration
}

func (r *pacedBody) Read(p []byte) (int, error) {
	if r.data.Len() == 0 {
		return 0, io.EOF
	}
	timer := time.NewTimer(r.interval)
	<-timer.C
	return r.data.Read(p[:min(len(p), r.quantum)])
}
func TestBundleProductionThrottledUpload(t *testing.T) {
	if os.Getenv("RAI_PHASE4_SLOW") == "" {
		t.Skip("explicit real TCP bandwidth pacing run")
	}
	for _, interval := range []time.Duration{25 * time.Millisecond, 250 * time.Millisecond} {
		t.Run(interval.String(), func(t *testing.T) {
			t.Parallel()
			store := &transportStore{}
			server := realProductionServer(t, New(store, slog.New(slog.NewTextHandler(io.Discard, nil))))
			raw := append(transportFixture(), bytes.Repeat([]byte(" "), 256<<10)...)
			body, media := plainMultipart(raw)
			req, _ := http.NewRequest("POST", server.URL+"/v2/sync/workout-bundles", &pacedBody{bytes.NewReader(body), 4096, interval})
			req.Header.Set("Authorization", "Bearer "+strings.Repeat("x", 64))
			req.Header.Set("Content-Type", media)
			resp, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode != 200 || store.imports.Load() != 1 {
				t.Fatal("throttled body rejected", resp.StatusCode)
			}
		})
	}
}
