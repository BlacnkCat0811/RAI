//go:build integration && ua1rsim

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
	"rai/server/internal/admin"
	"rai/server/internal/httpapi"
	"rai/server/internal/syncing"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWorkoutCompletenessSimulatorHTTPPostgres(t *testing.T) {
	source, sim := os.Getenv("RAI_UA1_R_XCTESTRUN"), os.Getenv("RAI_UA1_R_SIMULATOR")
	if source == "" || sim == "" {
		t.Fatal("explicit synthetic Simulator runner required")
	}
	f := setup(t)
	ctx := context.Background()
	if err := admin.QueueReadinessCandidate(ctx, f.s, f.p.DatasetID, syncing.NewID(), "synthetic UA1-R Simulator"); err != nil {
		t.Fatal(err)
	}
	handler := httpapi.New(f.s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var mu sync.Mutex
	dropped := false
	hashes := map[string]string{}
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/v2/sync/workout-completeness" {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				w.WriteHeader(500)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, r)
			if recorder.Code == 200 {
				m, err := syncing.DecodeWorkoutCompleteness(raw)
				if err != nil {
					w.WriteHeader(500)
					return
				}
				hash := syncing.BundleDigest(raw)
				mu.Lock()
				attempts++
				if prior, ok := hashes[m.ManifestID]; ok && prior != hash {
					t.Error("immutable manifest changed on retry")
				}
				hashes[m.ManifestID] = hash
				drop := !dropped
				dropped = true
				mu.Unlock()
				if drop {
					conn, _, e := w.(http.Hijacker).Hijack()
					if e == nil {
						conn.Close()
						return
					}
				}
			} else {
				t.Logf("UA1_R_SYNTHETIC_HTTP status=%d", recorder.Code)
			}
			for k, v := range recorder.Header() {
				w.Header()[k] = v
			}
			w.WriteHeader(recorder.Code)
			_, _ = w.Write(recorder.Body.Bytes())
			return
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, r)
		if recorder.Code >= 400 {
			var failure struct {
				Code string `json:"code"`
			}
			_ = json.Unmarshal(recorder.Body.Bytes(), &failure)
			t.Logf("UA1_R_SYNTHETIC_API status=%d code=%s", recorder.Code, failure.Code)
		}
		for key, values := range recorder.Header() {
			w.Header()[key] = values
		}
		w.WriteHeader(recorder.Code)
		_, _ = w.Write(recorder.Body.Bytes())
	}))
	defer server.Close()
	config, _ := json.Marshal(map[string]any{"baseURL": server.URL, "collectorEpoch": f.enrollment.CollectorEpoch, "token": f.enrollment.Token})
	directory := t.TempDir()
	file := filepath.Join(directory, "ua1-r.xctestrun")
	script := `import os,plistlib,sys
p=plistlib.load(open(sys.argv[1],'rb'));config=sys.stdin.read()
def visit(v):
 if isinstance(v,dict):
  if v.get('BlueprintName')=='RAITests':v.setdefault('EnvironmentVariables',{})['RAI_UA1_R_SIMULATOR_CONFIG']=config
  for x in v.values():visit(x)
 elif isinstance(v,list):
  for x in v:visit(x)
visit(p)
plistlib.dump(p,open(sys.argv[2],'wb'));os.chmod(sys.argv[2],0o600)`
	// __TESTROOT__ paths are relative to the original Products directory.
	// Keep the generated run file beside that root, then remove it after the run.
	file = filepath.Join(filepath.Dir(source), "ua1-r-e2e-"+syncing.NewID()+".xctestrun")
	defer os.Remove(file)
	prepare := exec.Command("python3", "-c", script, source, file)
	prepare.Stdin = bytes.NewReader(config)
	if err := prepare.Run(); err != nil {
		t.Fatal("prepare private synthetic test configuration", err)
	}
	runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	result := filepath.Join(directory, "result.xcresult")
	cmd := exec.CommandContext(runCtx, "xcodebuild", "test-without-building", "-xctestrun", file, "-destination", "platform=iOS Simulator,id="+sim, "-parallel-testing-enabled", "NO", "-only-testing:RAITests/WorkoutCompletenessNativeTests/testR3SimulatorHTTPPostgres", "-resultBundlePath", result, "-quiet")
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	runErr := cmd.Run()
	if runErr != nil {
		inspect := exec.Command("xcrun", "xcresulttool", "get", "test-results", "summary", "--path", result)
		if summary, err := inspect.Output(); err == nil {
			if diagnostics := os.Getenv("RAI_UA1_R_DIAGNOSTICS_DIR"); diagnostics != "" {
				_ = os.WriteFile(filepath.Join(diagnostics, "r3-e2e-summary.json"), summary, 0600)
			}
		}
	}
	if diagnostics := os.Getenv("RAI_UA1_R_DIAGNOSTICS_DIR"); diagnostics != "" {
		if err := os.WriteFile(filepath.Join(diagnostics, "r3-e2e-xcode-output.log"), output.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := runErr; err != nil {
		for _, line := range strings.Split(output.String(), "\n") {
			if strings.Contains(line, "UA1_R_SYNTHETIC") {
				t.Log(line)
			}
		}
		t.Fatal("synthetic Simulator/HTTP/PG test failed", err)
	}
	mu.Lock()
	wasDropped, total, identities := dropped, attempts, len(hashes)
	mu.Unlock()
	if !wasDropped || total < 3 || identities != 2 || count(t, f, "workout_sync_completeness") != 2 || count(t, f, "workout_imports") != 1 || count(t, f, "workouts") != 1 || count(t, f, "health_samples") != 1 {
		t.Fatal("missing real HTTP idempotency/PG evidence")
	}
	runReadinessLatest(t, f)
	r, _ := readReadiness(t, f)
	if !r.Coverage.CaughtUp || r.Coverage.Synthetic || r.UnknownLoads != 1 || strings.Contains(strings.Join(r.Reasons, ","), "load_sync_incomplete") {
		t.Fatal("M6 did not preserve real completeness and unknown load separately")
	}
	t.Logf("UA1_R_SIMULATOR_HTTP_PG=PASS manifests=2 streams=12 imports=1 lost_ack=true m6_synthetic=false unknown_loads=1")
}
