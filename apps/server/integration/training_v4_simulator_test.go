//go:build integration && traininguisim

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"rai/server/internal/httpapi"
	"rai/server/internal/jobs"
	"rai/server/internal/syncing"
	"rai/server/internal/training"
)

// This opt-in lane owns the full synthetic backend lifetime. Ordinary Go and
// integration gates never start Xcode or wait for a Simulator.
func TestTrainingPlanV4SimulatorHTTPPostgres(t *testing.T) {
	simulator := os.Getenv("RAI_UA1_R_SIMULATOR")
	if simulator == "" {
		t.Fatal("use scripts/local/test.sh training-ui")
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	var isolatedStarted atomic.Bool
	var normalRequestsAfterIsolation atomic.Int64
	startBackend := func(isolated bool) (*fixture, *httptest.Server) {
		t.Helper()
		f := setup(t)
		if _, err := f.s.Pool.Exec(context.Background(), `UPDATE datasets SET synthetic_test=true WHERE id=$1`, f.p.DatasetID); err != nil {
			t.Fatal(err)
		}
		log := slog.New(slog.NewJSONHandler(io.Discard, nil))
		api := httpapi.New(f.s, log)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isolated {
				isolatedStarted.Store(true)
			} else if isolatedStarted.Load() {
				normalRequestsAfterIsolation.Add(1)
			}
			api.ServeHTTP(w, r)
		}))
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- jobs.RunWithClock(ctx, f.s, log, time.Now) }()
		t.Cleanup(func() {
			cancel()
			if err := <-done; err != nil {
				t.Error(err)
			}
			server.Close()
		})
		return f, server
	}
	f, server := startBackend(false)
	isolated, isolatedServer := startBackend(true)
	ctx := context.Background()
	output := filepath.Join(root, ".local/testing")
	if err = os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	work, err := os.MkdirTemp(output, "training-ui-")
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(work, "config.json")
	secret := map[string]any{"configuration": map[string]string{"baseURL": server.URL, "collectorEpoch": f.enrollment.CollectorEpoch}, "token": f.enrollment.Token}
	b, err := json.Marshal(secret)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(config, b, 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(config)

	localConfig := filepath.Join(work, "local-config.json")
	digest := sha256.Sum256([]byte(isolated.p.DatasetID))
	local := map[string]any{"environment": "local-mac-device/v1", "command": "training", "baseURL": isolatedServer.URL,
		"datasetSHA256": hex.EncodeToString(digest[:]), "outboxNamespace": syncing.NewID(),
		"collectorEpoch": isolated.enrollment.CollectorEpoch, "token": isolated.enrollment.Token}
	b, err = json.Marshal(local)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(localConfig, b, 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(localConfig)
	logFile, err := os.OpenFile(filepath.Join(work, "xcodebuild.log"), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	testCtx, testCancel := context.WithTimeout(ctx, 12*time.Minute)
	defer testCancel()
	cmd := exec.CommandContext(testCtx, "xcodebuild", "test", "-project", filepath.Join(root, "ios/RAI/RAI.xcodeproj"), "-scheme", "RAI",
		"-destination", "platform=iOS Simulator,id="+simulator, "-derivedDataPath", os.Getenv("RAI_UA1_R_DERIVED"),
		"-parallel-testing-enabled", "NO", "-only-testing:RAIUITests/TrainingV4UITests",
		"-resultBundlePath", filepath.Join(work, "result.xcresult"), "CODE_SIGNING_ALLOWED=YES", "CODE_SIGN_IDENTITY=-", "RAI_DEV_HTTP_HOST=127.0.0.1", "-quiet")
	cmd.Env = append(os.Environ(), "TEST_RUNNER_RAI_TRAINING_V4_CONFIG="+config, "TEST_RUNNER_RAI_LOCAL_TRAINING_CONFIG="+localConfig)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	t.Log("synthetic Simulator result directory:", work)
	if err = cmd.Run(); err != nil {
		t.Fatalf("training UI failed: %v; inspect xcodebuild.log and result.xcresult in %s", err, work)
	}

	isolatedPlan, err := isolated.s.TrainingPlan(ctx, isolated.p, "")
	if err != nil || isolatedPlan.Effective == nil || !isolatedPlan.Config.V2() {
		t.Fatal("isolated UI plan was not published", err)
	}
	if n := normalRequestsAfterIsolation.Load(); n != 0 {
		t.Fatalf("isolated mode sent %d requests to the normal backend", n)
	}
	// Xcode shuts down a destination it booted for testing. Read the report
	// through simctl after a bounded boot and restore that shutdown state.
	inventory, err := exec.Command("xcrun", "simctl", "list", "devices", "-j").Output()
	if err != nil {
		t.Fatal(err)
	}
	var devices struct {
		Devices map[string][]struct {
			UDID  string `json:"udid"`
			State string `json:"state"`
		} `json:"devices"`
	}
	if err = json.Unmarshal(inventory, &devices); err != nil {
		t.Fatal(err)
	}
	for _, group := range devices.Devices {
		for _, device := range group {
			if device.UDID == simulator && device.State == "Shutdown" {
				bootCtx, bootCancel := context.WithTimeout(ctx, 90*time.Second)
				err = exec.CommandContext(bootCtx, "xcrun", "simctl", "bootstatus", simulator, "-b").Run()
				bootCancel()
				t.Cleanup(func() {
					if err := exec.Command("xcrun", "simctl", "shutdown", simulator).Run(); err != nil {
						t.Error(err)
					}
				})
				if err != nil {
					t.Fatal("report reader could not boot the Simulator", err)
				}
			}
		}
	}
	path, err := exec.Command("xcrun", "simctl", "get_app_container", simulator, "com.rai.v2.ios", "data").Output()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(path)), "Documents/local-device-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Result               string `json:"result"`
		Published            bool   `json:"trainingPlanPublished"`
		Unchanged            bool   `json:"originalSessionManifestUnchanged"`
		OriginalPresent      bool   `json:"originalSessionManifestPresent"`
		HealthValuesExported bool   `json:"healthValuesExported"`
	}
	if err = json.Unmarshal(raw, &report); err != nil || report.Result != "TRAINING_UI_READY" || !report.Published || !report.Unchanged || !report.OriginalPresent || report.HealthValuesExported {
		t.Fatal("isolated UI/report/session check failed", err)
	}
	p, err := f.s.TrainingPlan(ctx, f.p, "")
	if err != nil || p.Effective == nil || !p.Config.V2() || !p.Config.HasGoal() || p.Effective.Mode != "race" {
		t.Fatal("UI did not persist the adaptive race plan", err)
	}
	var version string
	if err = f.s.Pool.QueryRow(ctx, `SELECT parameter_version FROM training_generation_runs WHERE dataset_id=$1 AND plan_id=$2 AND status='SUCCEEDED' ORDER BY started_at DESC LIMIT 1`, f.p.DatasetID, p.ID).Scan(&version); err != nil || version != training.ParameterVersionV3 {
		t.Fatal("UI used the wrong parameter revision", err)
	}
}
