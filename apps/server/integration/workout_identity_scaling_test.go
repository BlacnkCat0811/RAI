//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"rai/server/internal/storage/postgres"
)

// TestIdentityCheckScaling characterises the identity-check tail seen in the R0
// logs (P50 ~159ms but P95 ~2.9s on S60). It grows one dataset import by import
// and, after each import, runs the exact production identity statement for the
// same key count with EXPLAIN (ANALYZE, BUFFERS) so the tail can be attributed
// to buffer reads, a plan change or planning time instead of being guessed.
//
// It is a diagnostic, not a gate: it asserts only that the statement returns the
// expected match count.
func TestIdentityCheckScaling(t *testing.T) {
	if os.Getenv("RAI_SYNC_IDENTITY_SCALING") == "" {
		t.Skip("set RAI_SYNC_IDENTITY_SCALING=1 to profile the identity statement")
	}
	imports := writeEnvInt(t, "RAI_SYNC_IDENTITY_IMPORTS", 12)
	profile := "S20"
	if configured := os.Getenv("RAI_SYNC_IDENTITY_PROFILE"); configured != "" {
		profile = configured
	}

	f := setup(t)
	ctx := context.Background()
	for iteration := 0; iteration < imports; iteration++ {
		bundle := perfBundle(t, f, profile, uint64(20000+iteration))
		raw, err := json.Marshal(bundle)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.s.ImportWorkoutBundle(ctx, f.p, bundle, raw); err != nil {
			t.Fatalf("import %d: %v", iteration, err)
		}
		var rows int64
		if err = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM health_samples`).Scan(&rows); err != nil {
			t.Fatal(err)
		}
		// Probe with the keys of this import (a full batch) so the key count is
		// stable across iterations and only the table size changes.
		types := make([]string, 0, len(bundle.Samples))
		uuids := make([]string, 0, len(bundle.Samples))
		for _, sample := range bundle.Samples {
			types = append(types, sample.Type)
			uuids = append(uuids, sample.UUID)
		}
		probeIdentity(t, f, rows, iteration, types, uuids)
	}
}

func probeIdentity(t *testing.T, f *fixture, rows int64, iteration int, types, uuids []string) {
	t.Helper()
	ctx := context.Background()
	tx, err := f.s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// The transaction-local settings the import path uses, so planning behaves
	// the same way as inside a real import.
	for _, statement := range []string{
		`SET LOCAL statement_timeout='120s'`,
		`SET LOCAL plan_cache_mode='force_custom_plan'`,
	} {
		if _, err = tx.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	started := time.Now()
	planRows, err := tx.Query(ctx, `EXPLAIN (ANALYZE, BUFFERS) `+postgres.SampleIdentityStatement, f.p.DatasetID, types, uuids)
	if err != nil {
		t.Fatalf("explain identity: %v", err)
	}
	var lines []string
	var execution float64
	for planRows.Next() {
		var line string
		if err = planRows.Scan(&line); err != nil {
			planRows.Close()
			t.Fatal(err)
		}
		lines = append(lines, line)
		for _, field := range strings.Fields(line) {
			if value, ok := strings.CutPrefix(field, "time="); ok {
				value = strings.TrimSuffix(value, "..")
				if parsed, convErr := strconv.ParseFloat(strings.TrimSuffix(value, "ms"), 64); convErr == nil && execution < parsed {
					execution = parsed
				}
			}
		}
	}
	planRows.Close()
	elapsed := float64(time.Since(started).Microseconds()) / 1000
	var matches int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT k.sample_type FROM unnest($2::text[],$3::text[]) AS k(sample_type,sample_uuid)
			JOIN health_samples h ON h.dataset_id=$1 AND h.sample_type=k.sample_type AND h.sample_uuid=k.sample_uuid::uuid) x`,
		f.p.DatasetID, types, uuids).Scan(&matches); err != nil {
		t.Fatal(err)
	}
	if matches != len(types) {
		t.Fatalf("identity probe matched %d of %d keys", matches, len(types))
	}
	buffers, plan := "", ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Buffers:") && buffers == "" {
			buffers = trimmed
		}
		if strings.Contains(trimmed, "Scan") || strings.Contains(trimmed, "Join") || strings.Contains(trimmed, "Loop") {
			if plan == "" {
				plan = trimmed
			}
		}
	}
	fmt.Printf("R5_IDENTITY iteration=%d table_rows=%d keys=%d explain_ms=%.1f exec_ms=%.1f %s %s\n",
		iteration, rows, len(types), elapsed, execution, plan, buffers)
}
