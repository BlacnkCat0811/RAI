//go:build integration && perfsim

package integration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rai/server/internal/httpapi"
	"rai/server/internal/jobs"
	"rai/server/internal/modelmanifest"
	"rai/server/internal/syncing"
)

// TestPerfSimulatorAcceptance serves synthetic runs through the real API and
// Worker on 127.0.0.1 for a manually driven Simulator session. It writes the
// App configuration to $RAI_PERFSIM_DIR/config.json and reacts to control
// files in that directory:
//
//	sync  – invalidate today as a sync does and hold the recompute 90 s
//	stop  – end the session
//
// Synthetic data only; nothing is logged except paths and the server URL.
func TestPerfSimulatorAcceptance(t *testing.T) {
	dir := os.Getenv("RAI_PERFSIM_DIR")
	if dir == "" {
		t.Skip("RAI_PERFSIM_DIR not set")
	}
	f := setup(t)
	ctx := context.Background()
	if _, err := f.s.Pool.Exec(ctx, `UPDATE datasets SET synthetic_test=true WHERE id=$1`, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	for d := 60; d >= 1; d-- {
		if d%2 == 1 && d%5 != 0 {
			continue
		}
		at := time.Date(now.Year(), now.Month(), now.Day(), 7, 0, 0, 0, loc).AddDate(0, 0, -d)
		acceptIngest(t, f, f.p, realisticRun(f, syncing.NewID(), at, 35+(d*7)%40, d))
		processIngest(t, f.s)
	}
	todayRun := now.Add(-2 * time.Hour)
	if todayRun.Day() != now.Day() {
		todayRun = time.Date(now.Year(), now.Month(), now.Day(), 0, 5, 0, 0, loc)
	}
	acceptIngest(t, f, f.p, realisticRun(f, syncing.NewID(), todayRun, 40, 2))
	processIngest(t, f.s)
	today, _ := runStackDay(t, f, nil, stackDayOptions{})
	hash, _ := modelmanifest.CurrentHash()
	stack := modelmanifest.StackID()
	setModelChannel(t, f, "active", stack, hash)

	log := slog.New(slog.NewJSONHandler(io.Discard, nil))
	server := httptest.NewServer(httpapi.New(f.s, log))
	defer server.Close()
	startWorker := func() func() {
		wctx, c := context.WithCancel(ctx)
		d := make(chan error, 1)
		go func() { d <- jobs.RunWithClock(wctx, f.s, log, time.Now) }()
		return func() { c(); <-d }
	}
	stopWorker := startWorker()
	defer func() { stopWorker() }()

	secret, _ := json.Marshal(map[string]any{"configuration": map[string]string{"baseURL": server.URL, "collectorEpoch": f.enrollment.CollectorEpoch}, "token": f.enrollment.Token})
	if err := os.WriteFile(filepath.Join(dir, "config.json"), secret, 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(filepath.Join(dir, "config.json"))
	t.Logf("PERFSIM_READY url=%s", server.URL)
	scope := modelmanifest.DayScope(stack, today.Format(time.DateOnly))
	deadline := time.Now().Add(35 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		if _, err := os.Stat(filepath.Join(dir, "stop")); err == nil {
			os.Remove(filepath.Join(dir, "stop"))
			return
		}
		if _, err := os.Stat(filepath.Join(dir, "sync")); err == nil {
			os.Remove(filepath.Join(dir, "sync"))
			stopWorker()
			if _, err := f.s.Pool.Exec(ctx, `SELECT rai_model_stack_invalidate($1,clock_timestamp(),clock_timestamp(),NULL)`, f.p.DatasetID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.Pool.Exec(ctx, `UPDATE jobs SET run_after=now()+interval '90 seconds' WHERE dataset_id=$1 AND scope_key=$2 AND state='queued'`, f.p.DatasetID, scope); err != nil {
				t.Fatal(err)
			}
			stopWorker = startWorker()
			t.Logf("PERFSIM_SYNC held_until=%s", time.Now().Add(90*time.Second).Format(time.TimeOnly))
		}
	}
}
