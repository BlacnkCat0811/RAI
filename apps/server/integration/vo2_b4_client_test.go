//go:build integration && m4b2client

package integration

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"rai/server/internal/httpapi"
)

// Actual macOS SIGKILL, never described as an iPhone kill. The existing B2
// probe intentionally carries third-party provenance; projection must remain
// a null state rather than pretending it is an Apple estimate.
func TestB4ClientSIGKILLThroughCandidatePublication(t *testing.T) {
	binary := os.Getenv("RAI_M4B2_PROBE")
	if binary == "" {
		t.Fatal("synthetic probe required")
	}
	for _, phase := range []string{"before-upload", "after-commit"} {
		t.Run(phase, func(t *testing.T) {
			f := setup(t)
			api, worker := b4Roles(t, f)
			server := httptest.NewServer(httpapi.New(api, analysisLog))
			defer server.Close()
			config, _ := json.Marshal(map[string]string{"baseURL": server.URL, "token": f.enrollment.Token, "serviceID": f.enrollment.ServiceID, "datasetID": f.p.DatasetID, "datasetEpoch": f.enrollment.DatasetEpoch, "collectorEpoch": f.enrollment.CollectorEpoch})
			dir := t.TempDir()
			command := func(mode string) *exec.Cmd {
				cmd := exec.Command(binary, dir, mode)
				cmd.Env = append(os.Environ(), "RAI_M4B2_SYNTHETIC="+string(config))
				return cmd
			}
			child := command(phase)
			child.Stdout = io.Discard
			child.Stderr = io.Discard
			if e := child.Start(); e != nil {
				t.Fatal(e)
			}
			defer child.Process.Kill()
			done := make(chan error, 1)
			go func() { done <- child.Wait() }()
			deadline := time.Now().Add(20 * time.Second)
			for {
				if _, e := os.Stat(filepath.Join(dir, "ready")); e == nil {
					break
				}
				select {
				case <-done:
					t.Fatal("probe exited before boundary")
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("kill boundary timeout")
				}
				time.Sleep(5 * time.Millisecond)
			}
			load := func() map[string]any {
				raw, e := os.ReadFile(filepath.Join(dir, "state.json"))
				if e != nil {
					t.Fatal(e)
				}
				var state map[string]any
				if json.Unmarshal(raw, &state) != nil {
					t.Fatal("state")
				}
				return state
			}
			initial := load()
			flow, ok := initial["vo2"].(map[string]any)
			if !ok || flow["anchor"] != nil || flow["pending"] == nil {
				t.Fatal("anchor advanced before durable ACK")
			}
			pending, _ := json.Marshal(flow["pending"])
			if e := child.Process.Kill(); e != nil {
				t.Fatal(e)
			}
			<-done
			crashed := load()
			p, _ := json.Marshal(crashed["vo2"].(map[string]any)["pending"])
			if string(p) != string(pending) {
				t.Fatal("pending changed on kill")
			}
			if phase == "after-commit" && count(t, f, "sync_batches") != 1 {
				t.Fatal("server commit not reached")
			}
			if out, e := command("resume").CombinedOutput(); e != nil {
				_ = out
				t.Fatal("resume failed", e)
			}
			if count(t, f, "health_samples") != 1 || count(t, f, "sync_batches") != 1 {
				t.Fatal("duplicate transport after kill")
			}
			var epoch string
			var at int64
			if e := f.s.Pool.QueryRow(context.Background(), `SELECT metadata->'RAI:vo2maxFactV1'->>'sourceEpochID',(SELECT flow_created_at_us FROM daily_health_coverage WHERE dataset_id=$1 LIMIT 1) FROM health_samples WHERE dataset_id=$1`, f.p.DatasetID).Scan(&epoch, &at); e != nil {
				t.Fatal(e)
			}
			b4Enroll(t, f, epoch, at)
			b4Worker(t, f, worker)
			externalRead(t, f, "source_selection_required", nil)
			if count(t, f, "analysis_artifacts") != 1 || count(t, f, "analysis_publications") != 1 {
				t.Fatal("semantic projection duplication")
			}
			t.Logf("B4_MACOS_SIGKILL phase=%s raw=1 receipt=1 projection=1 anchor_recovered=true", phase)
		})
	}
}
