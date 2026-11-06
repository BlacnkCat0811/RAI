//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"testing"
	"time"

	"rai/server/internal/httpapi"
)

// TestWorkoutBundleStatementTimeoutCode answers the one open question left by
// the contended S240 baseline run (r0-baseline-syncperf.log), where six timed
// imports returned HTTP 503 and the binary in use did not yet record the
// response body. The handler can only answer 503 with one of four codes:
// request_timeout (handler context), database_statement_timeout_or_cancel
// (SQLSTATE 57014), database_transaction_timeout (25P04) or
// temporarily_unavailable. This test makes one of them happen deterministically
// — a per-row trigger that makes a single batch statement exceed the
// transaction-local 30s statement_timeout — and records the observed code.
//
// It is gated because it deliberately burns ~30s of wall clock.
func TestWorkoutBundleStatementTimeoutCode(t *testing.T) {
	if os.Getenv("RAI_SYNC_TIMEOUT_PROBE") == "" {
		t.Skip("set RAI_SYNC_TIMEOUT_PROBE=1 to run the statement-timeout probe")
	}
	f := setup(t)
	ctx := context.Background()

	if _, err := f.s.Pool.Exec(ctx, `CREATE OR REPLACE FUNCTION rai_test_slow_sample() RETURNS trigger AS $$
		BEGIN
			PERFORM pg_sleep(0.04);
			RETURN NEW;
		END $$ LANGUAGE plpgsql`); err != nil {
		t.Fatalf("create slow trigger function: %v", err)
	}
	if _, err := f.s.Pool.Exec(ctx, `CREATE TRIGGER rai_test_slow_sample BEFORE INSERT ON health_samples
		FOR EACH ROW EXECUTE FUNCTION rai_test_slow_sample()`); err != nil {
		t.Fatalf("create slow trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.s.Pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS rai_test_slow_sample ON health_samples`)
		_, _ = f.s.Pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS rai_test_slow_sample()`)
	})

	// 2,000 samples => two 1,000-row batches; at 40ms per row the first batch
	// alone needs ~40s, so the transaction-local statement_timeout (30s) fires
	// inside the fact write.
	bundle := scaleBundle(t, f, 2000, 0, 0, 0, 7001)
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}
	api := httpapi.New(f.s, slog.New(slog.NewJSONHandler(io.Discard, nil)))

	started := time.Now()
	status, body := uploadBundleHTTP(t, api, f.enrollment.Token, raw)
	elapsed := time.Since(started)

	var payload map[string]string
	_ = json.Unmarshal(body, &payload)
	t.Logf("R5_TIMEOUT_PROBE status=%d code=%q elapsed_ms=%.0f body=%s", status, payload["code"], float64(elapsed.Milliseconds()), body)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 for a statement that exceeds statement_timeout, got %d: %s", status, body)
	}
	if payload["code"] != "database_statement_timeout_or_cancel" {
		t.Fatalf("expected database_statement_timeout_or_cancel, got %q: %s", payload["code"], body)
	}

	// The whole bundle must be rolled back: no facts, no receipt, no job.
	var samples, receipts, jobs int
	if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM health_samples WHERE dataset_id = $1`, f.p.DatasetID).Scan(&samples); err != nil {
		t.Fatalf("count samples: %v", err)
	}
	if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM workout_imports WHERE dataset_id = $1`, f.p.DatasetID).Scan(&receipts); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE dataset_id = $1`, f.p.DatasetID).Scan(&jobs); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if samples != 0 || receipts != 0 || jobs != 0 {
		t.Fatalf("statement timeout left residue: samples=%d receipts=%d jobs=%d", samples, receipts, jobs)
	}
}
