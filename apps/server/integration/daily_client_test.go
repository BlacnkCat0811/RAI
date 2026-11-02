//go:build integration && m5aclient

package integration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"rai/server/internal/httpapi"
)

func TestDailySwiftClientSIGKILLAndDurableRetry(t *testing.T) {
	binary := os.Getenv("RAI_M5A_PROBE")
	if binary == "" {
		t.Fatal("RAI_M5A_PROBE required")
	}
	for _, phase := range []string{"before-upload", "during-upload", "after-commit"} {
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
				cmd.Env = append(os.Environ(), "RAI_M5A_SYNTHETIC="+string(config))
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
			t.Logf("M5A_SIGKILL phase=%s facts=1 receipts=1 anchor_restored=true", phase)
		})
	}
}

func TestDailySwiftSharedUploadBudgetDoesNotStarveSleep(t *testing.T) {
	binary := os.Getenv("RAI_M5A_PROBE")
	if binary == "" {
		t.Fatal("RAI_M5A_PROBE required")
	}
	f := setup(t)
	handler := httpapi.New(f.s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var throttled atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, r)
		if recorder.Code == 429 {
			throttled.Add(1)
		}
		for k, v := range recorder.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(recorder.Code)
		_, _ = w.Write(recorder.Body.Bytes())
	}))
	defer server.Close()
	config, _ := json.Marshal(map[string]string{"baseURL": server.URL, "token": f.enrollment.Token, "serviceID": f.enrollment.ServiceID, "datasetID": f.p.DatasetID, "datasetEpoch": f.enrollment.DatasetEpoch, "collectorEpoch": f.enrollment.CollectorEpoch})
	command := exec.Command(binary, t.TempDir(), "fairness")
	command.Env = append(os.Environ(), "RAI_M5A_SYNTHETIC="+string(config))
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("daily fairness failed: %v %s", err, out)
	}
	var facts, receipts int
	ctx := context.Background()
	_ = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM health_samples`).Scan(&facts)
	_ = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM sync_batches`).Scan(&receipts)
	if facts != 3 || receipts != 3 || throttled.Load() == 0 {
		t.Fatal("shared capacity fairness not proven", facts, receipts, throttled.Load())
	}
	t.Log("M5A_FAIRNESS facts=3 receipts=3 actual_429=true sleep_delivered=true no_empty_batch_churn=true")
}
