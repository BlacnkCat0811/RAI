//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
)

// R5 write-path comparison: the three fact-write candidates are measured
// through the real import transaction on a real isolated PostgreSQL, on the
// normal (non-race) build. Nothing here calls a Store helper that bypasses the
// import path, so the numbers describe what the server actually does.
//
//	RAI_SYNC_WRITE_CANDIDATES=1 go test -tags=integration -run '^TestWorkoutWriteCandidates$'
//
// Environment:
//
//	RAI_SYNC_WRITE_ITERATIONS  measured imports per candidate (default 10)
//	RAI_SYNC_WRITE_WARMUPS     discarded imports per candidate (default 2)
//	RAI_SYNC_WRITE_PROFILE     fixture profile (default S60)
func TestWorkoutWriteCandidates(t *testing.T) {
	if os.Getenv("RAI_SYNC_WRITE_CANDIDATES") == "" {
		t.Skip("set RAI_SYNC_WRITE_CANDIDATES=1 to run the write-path comparison")
	}
	iterations := writeEnvInt(t, "RAI_SYNC_WRITE_ITERATIONS", 10)
	warmups := writeEnvInt(t, "RAI_SYNC_WRITE_WARMUPS", 2)
	profile := os.Getenv("RAI_SYNC_WRITE_PROFILE")
	if profile == "" {
		profile = "S60"
	}
	strategies := []postgres.WriteStrategy{
		postgres.StrategyValues,
		postgres.StrategyArrays,
		postgres.StrategyCopy,
	}
	results := make([]writeCandidateResult, 0, len(strategies))
	for _, strategy := range strategies {
		result := measureWriteCandidate(t, strategy, profile, iterations, warmups)
		t.Logf("SYNC_WRITE_CANDIDATE=%s", result.json())
		results = append(results, result)
	}
	compareWriteCandidates(t, results)
}

func writeEnvInt(t *testing.T, name string, fallback int) int {
	t.Helper()
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		t.Fatalf("%s=%q is not a positive integer", name, raw)
	}
	return value
}

// writeCandidateResult is one candidate's measured summary.
type writeCandidateResult struct {
	Strategy     string             `json:"strategy"`
	Profile      string             `json:"profile"`
	Iterations   int                `json:"iterations"`
	Failures     int                `json:"failures"`
	Samples      int                `json:"samples"`
	FixtureBytes int                `json:"fixture_bytes"`
	ImportP50    float64            `json:"import_total_p50_ms"`
	ImportP95    float64            `json:"import_total_p95_ms"`
	PhaseP50     map[string]float64 `json:"phase_p50_ms"`
	PhaseP95     map[string]float64 `json:"phase_p95_ms"`
	Digest       string             `json:"digest"`
	Receipt      string             `json:"receipt"`
	WalBytes     float64            `json:"wal_bytes_per_import"`
	WalRecords   float64            `json:"wal_records_per_import"`
	TupInserted  float64            `json:"tup_inserted_per_import"`
	writeDur     []float64
	assocDur     []float64
	identDur     []float64
}

func (r writeCandidateResult) json() string {
	raw, err := json.Marshal(r)
	if err != nil {
		return fmt.Sprintf(`{"strategy":%q,"error":%q}`, r.Strategy, err.Error())
	}
	return string(raw)
}

// measureWriteCandidate imports the same fixture shape `iterations` times with
// fresh identities under one strategy and reports the phase distribution.
func measureWriteCandidate(t *testing.T, strategy postgres.WriteStrategy, profile string, iterations, warmups int) writeCandidateResult {
	t.Helper()
	t.Run(string(strategy), func(t *testing.T) {
		previous := postgres.CurrentWriteStrategy()
		if err := postgres.SetWriteStrategy(strategy); err != nil {
			t.Fatalf("select write strategy: %v", err)
		}
		t.Cleanup(func() {
			if err := postgres.SetWriteStrategy(previous); err != nil {
				t.Fatalf("restore write strategy: %v", err)
			}
		})
		measureWriteCandidateOn(t, strategy, profile, iterations, warmups)
	})
	return writeCandidateStore[strategy]
}

// writeCandidateStore collects results from the sequential subtests.
var writeCandidateStore = map[postgres.WriteStrategy]writeCandidateResult{}

// writeStats reads the cluster level write counters around the measured loop.
type writeStats struct {
	walBytes   int64
	walRecords int64
	tupInsert  int64
}

func readWriteStats(t *testing.T, f *fixture) writeStats {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var stats writeStats
	if err := f.s.Pool.QueryRow(ctx, `SELECT wal_bytes, wal_records FROM pg_stat_wal`).Scan(&stats.walBytes, &stats.walRecords); err != nil {
		t.Fatalf("read pg_stat_wal: %v", err)
	}
	if err := f.s.Pool.QueryRow(ctx, `SELECT tup_inserted FROM pg_stat_database WHERE datname = current_database()`).Scan(&stats.tupInsert); err != nil {
		t.Fatalf("read pg_stat_database: %v", err)
	}
	return stats
}

func measureWriteCandidateOn(t *testing.T, strategy postgres.WriteStrategy, profile string, iterations, warmups int) {
	t.Helper()
	f := setup(t)
	phases := map[string][]float64{}
	totals := make([]float64, 0, iterations)
	failures := 0
	var samples, fixtureBytes int
	var lastReceipt string
	before := readWriteStats(t, f)
	for iteration := 0; iteration < warmups+iterations; iteration++ {
		bundle := perfBundle(t, f, profile, uint64(1000+iteration))
		raw, err := json.Marshal(bundle)
		if err != nil {
			t.Fatalf("marshal bundle: %v", err)
		}
		if iteration == 0 {
			samples = len(bundle.Samples)
			fixtureBytes = len(raw)
		}
		collected := map[string]float64{}
		ctx := syncing.ObservePhases(context.Background(), func(name string, duration time.Duration) {
			collected[name] = float64(duration.Microseconds()) / 1000
		})
		start := time.Now()
		receipt, err := f.s.ImportWorkoutBundle(ctx, f.p, bundle, raw)
		elapsed := float64(time.Since(start).Microseconds()) / 1000
		if err != nil {
			failures++
			t.Logf("iteration %d failed: %v", iteration, err)
			continue
		}
		lastReceipt = normalizedReceipt(t, receipt)
		if iteration < warmups {
			continue
		}
		totals = append(totals, elapsed)
		for name, value := range collected {
			phases[name] = append(phases[name], value)
		}
	}
	after := readWriteStats(t, f)
	result := writeCandidateResult{
		Strategy:     string(strategy),
		Profile:      profile,
		Iterations:   len(totals),
		Failures:     failures,
		Samples:      samples,
		FixtureBytes: fixtureBytes,
		ImportP50:    nearestRank(totals, 0.5),
		ImportP95:    nearestRank(totals, 0.95),
		PhaseP50:     map[string]float64{},
		PhaseP95:     map[string]float64{},
		Digest:       writeDigest(t, f),
		Receipt:      lastReceipt,
	}
	for name, values := range phases {
		result.PhaseP50[name] = nearestRank(values, 0.5)
		result.PhaseP95[name] = nearestRank(values, 0.95)
	}
	if len(totals) > 0 {
		result.WalBytes = float64(after.walBytes-before.walBytes) / float64(len(totals))
		result.WalRecords = float64(after.walRecords-before.walRecords) / float64(len(totals))
		result.TupInserted = float64(after.tupInsert-before.tupInsert) / float64(len(totals))
	}
	writeCandidateStore[strategy] = result
	t.Logf("strategy=%s profile=%s n=%d failures=%d samples=%d bytes=%d import_p50=%.1fms p95=%.1fms sample_writes_p50=%.1fms association_writes_p50=%.1fms wal_bytes_per_import=%.0f",
		strategy, profile, result.Iterations, result.Failures, samples, fixtureBytes,
		result.ImportP50, result.ImportP95, result.PhaseP50["sample_writes"], result.PhaseP50["association_writes"], result.WalBytes)
	if result.Failures != 0 {
		t.Errorf("strategy %s had %d failed imports", strategy, result.Failures)
	}
}

// writeDigest is a content digest of every fact the import produced, excluding
// the randomly generated association identity so two strategies can be compared
// on everything that is deterministic.
func writeDigest(t *testing.T, f *fixture) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const statement = `SELECT
  (SELECT md5(string_agg(t::text, '|' ORDER BY t.sample_type, t.sample_uuid)) FROM (
      SELECT sample_type, sample_uuid, event_start, event_end, value_numeric, value_category,
             unit, source_bundle_id, encode(content_sha256,'hex') AS hash, entity_revision, metadata
      FROM health_samples WHERE dataset_id = $1) AS t),
  (SELECT md5(string_agg(a::text, '|' ORDER BY a.sample_type, a.sample_uuid)) FROM (
      SELECT sample_type, sample_uuid, workout_uuid, source_bundle_id, commit_rev
      FROM workout_samples WHERE dataset_id = $1) AS a),
  (SELECT md5(string_agg(r::text, '|' ORDER BY r.workout_uuid)) FROM (
      SELECT workout_uuid, content_sha256, entity_revision FROM workouts WHERE dataset_id = $1) AS r)`
	var samples, associations, workouts string
	if err := f.s.Pool.QueryRow(ctx, statement, f.p.DatasetID).Scan(&samples, &associations, &workouts); err != nil {
		t.Fatalf("digest facts: %v", err)
	}
	return samples + ":" + associations + ":" + workouts
}

// compareWriteCandidates prints the comparison table and asserts that the
// candidates agree on content, so a faster candidate can only be adopted when it
// is also semantically identical.
func compareWriteCandidates(t *testing.T, results []writeCandidateResult) {
	t.Helper()
	baseline := results[0]
	for _, result := range results[1:] {
		if result.Digest != baseline.Digest {
			t.Errorf("strategy %s produced different facts than %s: %s != %s",
				result.Strategy, baseline.Strategy, result.Digest, baseline.Digest)
		}
		if result.Receipt != baseline.Receipt {
			t.Errorf("strategy %s produced a different receipt than %s:\n%s\n%s", result.Strategy, baseline.Strategy, result.Receipt, baseline.Receipt)
		}
	}
	var table strings.Builder
	table.WriteString("strategy  n   fail  import_p50  import_p95  sample_writes_p50  association_writes_p50  identity_p50  wal_bytes\n")
	for _, result := range results {
		fmt.Fprintf(&table, "%-8s %-3d %-5d %-11.1f %-11.1f %-18.1f %-23.1f %-12.1f %.0f\n",
			result.Strategy, result.Iterations, result.Failures, result.ImportP50, result.ImportP95,
			result.PhaseP50["sample_writes"], result.PhaseP50["association_writes"],
			result.PhaseP50["identity_checks"], result.WalBytes)
	}
	t.Logf("SYNC_WRITE_COMPARISON\n%s", table.String())
	names := make([]string, 0, len(results))
	for _, result := range results {
		names = append(names, result.Strategy)
	}
	sort.Strings(names)
	t.Logf("SYNC_WRITE_STRATEGIES=%s", strings.Join(names, ","))
}

// TestWorkoutWriteCandidatePlans captures EXPLAIN (ANALYZE, BUFFERS, WAL) for
// every candidate over one identical fact batch. Each candidate runs inside a
// savepoint that is rolled back, so the plans describe the same rows.
func TestWorkoutWriteCandidatePlans(t *testing.T) {
	if os.Getenv("RAI_SYNC_WRITE_PLANS") == "" {
		t.Skip("set RAI_SYNC_WRITE_PLANS=1 to capture the write-path plans")
	}
	f := setup(t)
	bundle := perfBundle(t, f, "S60", 1)
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}
	if _, err = f.s.ImportWorkoutBundle(context.Background(), f.p, bundle, raw); err != nil {
		t.Fatalf("seed import: %v", err)
	}
	fresh := perfBundle(t, f, "S60", 2)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	tx, err := f.s.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout='120s'`); err != nil {
		t.Fatalf("statement_timeout: %v", err)
	}
	plans, err := postgres.ExplainFactWrites(ctx, tx, f.p.DatasetID, bundle.WorkoutID, fresh.Samples, 2)
	if err != nil {
		t.Fatalf("explain write candidates: %v", err)
	}
	for _, strategy := range []postgres.WriteStrategy{postgres.StrategyValues, postgres.StrategyArrays, postgres.StrategyCopy} {
		lines := plans[string(strategy)]
		t.Logf("SYNC_WRITE_PLAN_BEGIN strategy=%s rows=%d", strategy, len(fresh.Samples))
		for _, line := range lines {
			t.Logf("SYNC_WRITE_PLAN %s | %s", strategy, line)
		}
		t.Logf("SYNC_WRITE_PLAN_END strategy=%s", strategy)
	}
}
