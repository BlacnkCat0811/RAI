//go:build integration && m4b2sim

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
	"rai/server/internal/httpapi"
	"rai/server/internal/syncing"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Explicit acceptance only: a real Simulator App XCTest host exercises the
// production normalizer, coordinator, file uploader, outbox and HTTP handler.
func TestVO2SimulatorAppToIsolatedPG18(t *testing.T) {
	source := os.Getenv("RAI_M4B2_XCTESTRUN")
	sim := os.Getenv("RAI_M4B2_SIMULATOR")
	if source == "" || sim == "" {
		t.Fatal("RAI_M4B2_XCTESTRUN and RAI_M4B2_SIMULATOR required")
	}
	f := setup(t)
	handler := httpapi.New(f.s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var lost, injected atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" && r.URL.Path == "/v2/sync/vo2max-facts" && lost.CompareAndSwap(false, true) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, r)
			if recorder.Code == 200 {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					conn.Close()
					injected.Store(true)
					return
				}
			}
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
	// Keep __TESTROOT__ paths resolving to the original products directory.
	file := filepath.Join(filepath.Dir(source), "M4B2-"+syncing.NewID()+".xctestrun")
	defer os.Remove(file)
	script := `import plistlib,sys,json,os
with open(sys.argv[1],'rb') as f: p=plistlib.load(f)
config=sys.stdin.read()
def visit(x):
 if isinstance(x,dict):
  if x.get('BlueprintName')=='RAITests':
   x.setdefault('EnvironmentVariables',{})['RAI_M4B2_SYNTHETIC']=config
  for v in x.values(): visit(v)
 elif isinstance(x,list):
  for v in x: visit(v)
visit(p)
with open(sys.argv[2],'wb') as f: plistlib.dump(p,f)
os.chmod(sys.argv[2],0o600)`
	prepare := exec.Command("python3", "-c", script, source, file)
	prepare.Stdin = bytes.NewReader(config)
	if out, err := prepare.CombinedOutput(); err != nil {
		t.Fatal("test environment setup failed", err, string(out))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "xcodebuild", "test-without-building", "-xctestrun", file, "-destination", "platform=iOS Simulator,id="+sim, "-parallel-testing-enabled", "NO", "-only-testing:RAITests/NormalizerTests/testVO2SimulatorHTTPPostgresEndToEnd", "-quiet")
	out, err := cmd.CombinedOutput()
	// Synthetic ephemeral credentials never enter retained output.
	safe := strings.ReplaceAll(string(out), f.enrollment.Token, "[synthetic credential redacted]")
	if err != nil {
		t.Fatalf("Simulator failed: %v\n%s", err, safe)
	}
	var facts, receipts, dirty int
	if err = f.s.Pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM health_samples WHERE dataset_id=$1 AND sample_type=$2),(SELECT count(*) FROM sync_batches WHERE dataset_id=$1 AND envelope->>'contract'=$3),(SELECT count(*) FROM dirty_scopes WHERE dataset_id=$1 AND scope_key='external/vo2max/facts/v1')`, f.p.DatasetID, syncing.VO2Type, syncing.VO2Contract).Scan(&facts, &receipts, &dirty); err != nil || facts != 1 || receipts != 1 || dirty != 1 {
		t.Fatal("durability or duplicate", facts, receipts, dirty, err)
	}
	var integer string
	if err = f.s.Pool.QueryRow(context.Background(), `SELECT metadata->'RAI:vo2maxFactV1'->'typedMetadata'->'HKAlgorithmVersion'->>'value' FROM health_samples WHERE dataset_id=$1 AND sample_type=$2`, f.p.DatasetID, syncing.VO2Type).Scan(&integer); err != nil || integer != "9007199254740993" {
		t.Fatal("SDK-to-PG precision", err)
	}
	if !injected.Load() {
		t.Fatal("lost ACK was not injected")
	}
	t.Log("M4B2_SIMULATOR_APP_E2E=PASS facts=1 receipts=1 dirty=1 lost_ack_injected=true committed_anchor_asserted=true")
}
