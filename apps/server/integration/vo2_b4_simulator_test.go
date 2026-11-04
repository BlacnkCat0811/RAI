//go:build integration && m4b4sim

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"rai/server/internal/admin"
	"rai/server/internal/httpapi"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
	ve "rai/server/internal/vo2maxexternal"
)

type b4SimCase struct {
	recipe     string
	count      int
	status     string
	f          *fixture
	worker     *postgres.Store
	mu         sync.Mutex
	batches    map[string]syncing.VO2Batch
	hashes     map[string]string
	first      *syncing.VO2Batch
	lost       bool
	lastCommit time.Time
	checked    int
}
type b4Checkpoint struct {
	c    *b4SimCase
	done chan struct{}
}

// Explicit opt-in: the production App target hosts the real SDK normalizer,
// SyncCoordinator, durable Outbox, API client and background file uploader.
// Only HealthKitReading and the outer ACK-loss/checkpoint proxy are synthetic.
func TestB4SimulatorVO2FullE2E(t *testing.T) {
	source, sim := os.Getenv("RAI_M4B4_XCTESTRUN"), os.Getenv("RAI_M4B4_SIMULATOR")
	if source == "" || sim == "" {
		t.Fatal("B4 Simulator configuration required")
	}
	checkpoints := make(chan b4Checkpoint)
	cases := []*b4SimCase{
		{recipe: "fresh", count: 1, status: "available"},
		{recipe: "empty", count: 0, status: "no_sample_observed"},
		{recipe: "stale", count: 1, status: "stale"},
		{recipe: "expired", count: 1, status: "expired"},
		{recipe: "third_party", count: 1, status: "source_selection_required"},
		{recipe: "manual", count: 1, status: "source_selection_required"},
		{recipe: "unclassified", count: 1, status: "source_selection_required"},
		{recipe: "epochs", count: 2, status: "source_selection_required"},
		{recipe: "incomplete", count: 128, status: "sync_incomplete"},
		{recipe: "conflict", count: 2, status: "conflicting_data"},
		{recipe: "equivalent", count: 2, status: "available"},
		{recipe: "facts128", count: 128, status: "available"},
		{recipe: "bootstrap90", count: 90, status: "available"},
		{recipe: "refs1024", count: 1024, status: "available"},
		{recipe: "epochs32", count: 32, status: "available"},
		{recipe: "ties256", count: 256, status: "available"},
	}
	// Keep each disposable 40-connection cluster below its bound. Run both groups.
	switch os.Getenv("RAI_M4B4_GROUP") {
	case "0":
		cases = cases[:8]
	case "1":
		cases = cases[8:]
	default:
		t.Fatal("RAI_M4B4_GROUP must be 0 or 1; both groups are required")
	}
	if recipe := os.Getenv("RAI_M4B4_RECIPE"); recipe != "" {
		filtered := []*b4SimCase{}
		for _, c := range cases {
			if c.recipe == recipe {
				filtered = append(filtered, c)
			}
		}
		cases = filtered
	}
	configs := []map[string]any{}
	for _, c := range cases {
		c.f = setup(t)
		api, worker := b4Roles(t, c.f)
		c.worker = worker
		c.batches = map[string]syncing.VO2Batch{}
		c.hashes = map[string]string{}
		handler := httpapi.New(api, analysisLog)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/__b4/checkpoint" {
				done := make(chan struct{})
				select {
				case checkpoints <- b4Checkpoint{c, done}:
				case <-r.Context().Done():
					return
				}
				select {
				case <-done:
					w.WriteHeader(204)
				case <-r.Context().Done():
				}
				return
			}
			if r.Method == "POST" && r.URL.Path == "/v2/sync/vo2max-facts" {
				raw, e := io.ReadAll(r.Body)
				if e != nil {
					w.WriteHeader(500)
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(raw))
				recorder := httptest.NewRecorder()
				r = r.WithContext(syncing.ObservePhases(r.Context(), func(phase string, duration time.Duration) {
					t.Logf("B4_PG_TIMING phase=%s duration_us=%d", phase, duration.Microseconds())
				}))
				handler.ServeHTTP(recorder, r)
				if recorder.Code == 200 {
					b, e := syncing.DecodeVO2Batch(raw)
					if e != nil {
						w.WriteHeader(500)
						return
					}
					c.mu.Lock()
					if c.first == nil {
						copy := b
						c.first = &copy
					}
					c.batches[b.BatchID] = b
					c.hashes[b.BatchID] = syncing.DailyDigest(raw)
					c.lastCommit = time.Now()
					drop := c.recipe == "fresh" && !c.lost
					if drop {
						c.lost = true
					}
					c.mu.Unlock()
					if drop {
						conn, _, e := w.(http.Hijacker).Hijack()
						if e == nil {
							conn.Close()
							return
						}
					}
				}
				if recorder.Code != 200 {
					t.Logf("B4_HTTP status=%d", recorder.Code)
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
		t.Cleanup(server.Close)
		f := c.f
		configs = append(configs, map[string]any{"recipe": c.recipe, "count": c.count, "baseURL": server.URL, "token": f.enrollment.Token, "serviceID": f.enrollment.ServiceID, "datasetID": f.p.DatasetID, "datasetEpoch": f.enrollment.DatasetEpoch, "collectorEpoch": f.enrollment.CollectorEpoch})
	}
	config, _ := json.Marshal(configs)
	file := filepath.Join(filepath.Dir(source), "M4B4-"+syncing.NewID()+".xctestrun")
	defer os.Remove(file)
	script := `import plistlib,sys,os
with open(sys.argv[1],'rb') as f:p=plistlib.load(f)
config=sys.stdin.read()
def visit(x):
 if isinstance(x,dict):
  if x.get('BlueprintName')=='RAITests':x.setdefault('EnvironmentVariables',{})['RAI_M4B4_SYNTHETIC']=config
  for v in x.values():visit(v)
 elif isinstance(x,list):
  for v in x:visit(v)
visit(p)
with open(sys.argv[2],'wb') as f:plistlib.dump(p,f)
os.chmod(sys.argv[2],0o600)`
	prepare := exec.Command("python3", "-c", script, source, file)
	prepare.Stdin = bytes.NewReader(config)
	if _, e := prepare.CombinedOutput(); e != nil {
		t.Fatal("prepare Simulator environment")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "xcodebuild", "test-without-building", "-xctestrun", file, "-destination", "platform=iOS Simulator,id="+sim, "-parallel-testing-enabled", "NO", "-only-testing:RAITests/NormalizerTests/testB4SimulatorFullExternalClosure")
	var output bytes.Buffer
	private, e := os.OpenFile(filepath.Join(filepath.Dir(source), "..", "..", "..", "simulator-private-"+os.Getenv("RAI_M4B4_GROUP")+".log"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	defer private.Close()
	cmd.Stdout = io.MultiWriter(&output, private)
	cmd.Stderr = io.MultiWriter(&output, private)
	if e := cmd.Start(); e != nil {
		t.Fatal(e)
	}
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	expectedChecks := len(cases)
	for _, c := range cases {
		if c.recipe == "fresh" {
			expectedChecks++
		}
	}
	checked := 0
	for {
		select {
		case checkpoint := <-checkpoints:
			func() {
				defer close(checkpoint.done)
				t.Run(checkpoint.c.recipe, func(t *testing.T) { b4CheckSimulator(t, checkpoint.c) })
			}()
			checked++
		case e := <-finished:
			// Only count/status/timing output is retained. Never retain config or payload.
			for _, line := range strings.Split(output.String(), "\n") {
				if strings.Contains(line, "M4B4_SYNTHETIC") {
					t.Log(strings.TrimSpace(line))
				}
			}
			if e != nil {
				t.Fatal("Simulator XCTest failed", e, "checkpoints", checked)
			}
			if checked != expectedChecks {
				t.Fatal("missing Simulator case", checked)
			}
			if t.Failed() {
				return
			}
			t.Logf("M4B4_SIMULATOR_VO2_FULL_E2E=PASS cases=%d restricted_api=true restricted_worker=true lost_ack=true", checked)
			return
		case <-ctx.Done():
			t.Fatal("Simulator E2E timeout", checked)
		}
	}
}
func b4CheckSimulator(t *testing.T, c *b4SimCase) {
	t.Helper()
	ctx := context.Background()
	f := c.f
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.first == nil || count(t, f, "health_samples") != c.count {
		t.Fatal("App transport count", c.recipe, "committed_first", c.first != nil, "facts", count(t, f, "health_samples"))
	}
	if len(c.batches) != count(t, f, "sync_batches") {
		t.Fatal("receipt duplication")
	}
	for batch, hash := range c.hashes {
		var match bool
		if e := f.s.Pool.QueryRow(ctx, `SELECT convert_from(receipt,'UTF8')::jsonb->>'requestSHA256'=$3 FROM sync_batches WHERE dataset_id=$1 AND batch_id=$2`, f.p.DatasetID, batch, hash).Scan(&match); e != nil || !match {
			t.Fatal("receipt hash", e)
		}
	}
	b := *c.first
	c.checked++
	if c.checked == 2 {
		if c.recipe != "fresh" {
			t.Fatal("unexpected checkpoint")
		}
		b4Worker(t, f, c.worker)
		b4Exact(t, f, 47.123456789012344)
		t.Log("B4_SIMULATOR_HOT_EMPTY=PASS count=0 value_preserved=true")
		return
	}
	if c.recipe == "fresh" && !c.lost {
		t.Fatal("ACK loss missing")
	}
	if c.count > 0 && c.recipe != "epochs" {
		b4Enroll(t, f, b.Facts[0].Provenance.EpochID(), b.FlowCreatedAtUs)
	} else {
		if e := admin.QueueVO2ExternalCandidate(ctx, f.s, f.p.DatasetID, syncing.NewID(), "B4 synthetic"); e != nil {
			t.Fatal(e)
		}
	}
	t.Logf("B4_TIMING phase=ack_to_worker_queue recipe=%s duration_us=%d", c.recipe, time.Since(c.lastCommit).Microseconds())
	b4Worker(t, f, c.worker)
	value := 47.123456789012344
	var expected *float64
	if c.status == "available" || c.status == "stale" {
		expected = &value
	}
	start := time.Now()
	r, _ := externalRead(t, f, c.status, expected)
	t.Logf("B4_TIMING phase=candidate_reader recipe=%s duration_us=%d", c.recipe, time.Since(start).Microseconds())
	if r.Confidence != nil {
		t.Fatal("fabricated confidence")
	}
	if c.status == "available" {
		b4Exact(t, f, value)
	}
	var out bytes.Buffer
	if e := admin.VO2ExternalCommand(ctx, f.s.Pool.Config().ConnString(), []string{"inspect", "--dataset", f.p.DatasetID}, &out); e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(out.Bytes(), []byte(`"value":`)) {
		t.Fatal("admin default value leak")
	}
	if c.recipe == "epochs" {
		first := b4Enroll(t, f, b.Facts[0].Provenance.EpochID(), b.FlowCreatedAtUs)
		b4Worker(t, f, c.worker)
		a, _ := externalRead(t, f, "available", &value)
		if a.SourceEpoch == nil || *a.SourceEpoch != first.Epoch {
			t.Fatal("selection A")
		}
		second := ve.Selection{ID: syncing.NewID(), Epoch: b.Facts[1].Provenance.EpochID(), Supersedes: first.ID, EffectiveFrom: first.EffectiveFrom}
		if e := admin.SetVO2SourceSelection(ctx, f.s, f.p.DatasetID, syncing.NewID(), "B4 synthetic", second); e != nil {
			t.Fatal(e)
		}
		if _, _, e := f.s.CandidateVO2External(ctx, f.p.DatasetID, time.Now()); e == nil {
			t.Fatal("selection pointer not invalidated")
		}
		b4Worker(t, f, c.worker)
		bResult, _ := externalRead(t, f, "available", &value)
		if bResult.SourceEpoch == nil || *bResult.SourceEpoch != second.Epoch {
			t.Fatal("selection B")
		}
	}
	t.Logf("B4_SIMULATOR_CASE recipe=%s facts=%d receipts=%d status=%s", c.recipe, c.count, len(c.batches), c.status)
}
