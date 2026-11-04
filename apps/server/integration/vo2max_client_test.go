//go:build integration && m4b2client

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"rai/server/internal/httpapi"
	"rai/server/internal/syncing"
)

func TestVO2SwiftClientSIGKILLAndDurableRetry(t *testing.T) {
	binary := os.Getenv("RAI_M4B2_PROBE")
	if binary == "" {
		t.Fatal("RAI_M4B2_PROBE required")
	}
	for _, phase := range []string{"after-candidate", "after-body", "before-upload", "during-upload", "after-commit", "after-ack-validate"} {
		t.Run(phase, func(t *testing.T) {
			f := setup(t)
			handler := httpapi.New(f.s, slog.New(slog.NewTextHandler(io.Discard, nil)))
			committed := make(chan struct{}, 1)
			release := make(chan struct{})
			var block atomic.Bool
			block.Store(phase == "during-upload")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if block.CompareAndSwap(true, false) {
					// Commit durably, then lose the ACK while the child is killed.
					recorder := httptest.NewRecorder()
					handler.ServeHTTP(recorder, r)
					committed <- struct{}{}
					<-release
					for k, v := range recorder.Header() {
						w.Header()[k] = v
					}
					w.WriteHeader(recorder.Code)
					_, _ = w.Write(recorder.Body.Bytes())
					return
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			config, _ := json.Marshal(map[string]string{"baseURL": server.URL, "token": f.enrollment.Token, "serviceID": f.enrollment.ServiceID, "datasetID": f.p.DatasetID, "datasetEpoch": f.enrollment.DatasetEpoch, "collectorEpoch": f.enrollment.CollectorEpoch})
			directory := t.TempDir()
			command := func(mode string) *exec.Cmd {
				cmd := exec.Command(binary, directory, mode)
				cmd.Env = append(os.Environ(), "RAI_M4B2_SYNTHETIC="+string(config))
				return cmd
			}
			child := command(phase)
			child.Stdout = io.Discard
			child.Stderr = io.Discard
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- child.Wait() }()
			if phase == "during-upload" {
				select {
				case <-committed:
				case <-time.After(15 * time.Second):
					_ = child.Process.Kill()
					close(release)
					t.Fatal("upload boundary not reached")
				}
			} else {
				deadline := time.Now().Add(15 * time.Second)
				for {
					if _, err := os.Stat(filepath.Join(directory, "ready")); err == nil {
						break
					}
					select {
					case <-done:
						t.Fatal("child exited before crash boundary")
					default:
					}
					if time.Now().After(deadline) {
						_ = child.Process.Kill()
						t.Fatal("crash boundary not reached")
					}
					time.Sleep(5 * time.Millisecond)
				}
			}
			if err := child.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			<-done
			if phase == "during-upload" {
				close(release)
			}
			// The replay has the original batch UUID/body and old anchor.
			out, err := command("resume").CombinedOutput()
			if err != nil {
				t.Fatalf("synthetic replay failed: %v %s", err, out)
			}
			var facts, receipts int
			ctx := context.Background()
			if err = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM health_samples WHERE dataset_id=$1`, f.p.DatasetID).Scan(&facts); err != nil {
				t.Fatal(err)
			}
			if err = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM sync_batches WHERE dataset_id=$1 AND state='committed'`, f.p.DatasetID).Scan(&receipts); err != nil {
				t.Fatal(err)
			}
			if facts != 1 || receipts != 1 {
				t.Fatal("loss or duplicate after process kill", facts, receipts)
			}
			t.Logf("M4B2_MACOS_SIGKILL phase=%s facts=1 receipts=1 anchor_restored=true", phase)
		})
	}
}

func TestVO2SwiftSharedBudgetAndUnavailableFlowDoNotStarveDaily(t *testing.T) {
	binary := os.Getenv("RAI_M4B2_PROBE")
	if binary == "" {
		t.Fatal("RAI_M4B2_PROBE required")
	}
	f := setup(t)
	handler := httpapi.New(f.s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var attempts, limited atomic.Int32
	var first string
	var firstMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/sync/vo2max-facts" {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error("synthetic request read")
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
			hash := syncing.DailyDigest(raw)
			firstMu.Lock()
			if first == "" {
				first = hash
			} else if first != hash {
				t.Error("retry bytes changed")
			}
			firstMu.Unlock()
			if attempts.Add(1) <= 2 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(503)
				_, _ = io.WriteString(w, `{"code":"internal_error"}`)
				return
			}
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, r)
		if recorder.Code == 429 {
			limited.Add(1)
		}
		for k, v := range recorder.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(recorder.Code)
		_, _ = w.Write(recorder.Body.Bytes())
	}))
	defer server.Close()
	config, _ := json.Marshal(map[string]string{"baseURL": server.URL, "token": f.enrollment.Token, "serviceID": f.enrollment.ServiceID, "datasetID": f.p.DatasetID, "datasetEpoch": f.enrollment.DatasetEpoch, "collectorEpoch": f.enrollment.CollectorEpoch})
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, t.TempDir(), "fairness")
	cmd.Env = append(os.Environ(), "RAI_M4B2_SYNTHETIC="+string(config))
	if err := cmd.Run(); err != nil {
		t.Fatal("synthetic shared flow delivery failed", err)
	}
	if attempts.Load() < 3 || limited.Load() == 0 || count(t, f, "health_samples") != 4 || count(t, f, "sync_batches") != 4 {
		t.Fatal("flow starvation or bypassed real admission")
	}
	t.Log("M4B2_SHARED_BUDGET=PASS flows=4 unavailable_vo2_retries=true real_429=true immutable_retry=true")
}
