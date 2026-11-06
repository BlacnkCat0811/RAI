//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"rai/server/internal/syncing"
)

// TestBarePGWriteCeiling measures the physical write floor of this schema on this
// machine without the HTTP/Go application layer: the same tables, the same
// indexes and the same durability settings (fsync / full_page_writes /
// synchronous_commit on), written by the most aggressive shapes PostgreSQL
// offers. It exists to answer one question: if even these cannot reach the
// R2-P08/P09 latency, the miss is a property of the machine + durability, not of
// the application code.
//
// Variants:
//
//	unnest            the production array shape (one fixed statement per batch)
//	unnest_uuid_order the same, rows sorted by sample_uuid (does insert order matter?)
//	copy_indexed      COPY straight into the indexed tables
//	copy_unindexed    COPY into the tables with the secondary indexes dropped, then
//	                  rebuild them (index build timed and reported separately)
//
// Every variant runs in its own disposable database and writes only facts: no
// identity checks, no tombstones, no receipt, no job, no change event. The
// measured interval is BEGIN … COMMIT including the commit fsync.
func TestBarePGWriteCeiling(t *testing.T) {
	if os.Getenv("RAI_SYNC_BARE_PG") == "" {
		t.Skip("set RAI_SYNC_BARE_PG=1")
	}
	iterations := perfEnvInt(t, "RAI_SYNC_BARE_ITERATIONS", 5)
	profiles := perfEnvString("RAI_SYNC_BARE_PROFILES", "S60,S240")
	for _, profile := range strings.Split(profiles, ",") {
		profile = strings.TrimSpace(profile)
		if profile == "" {
			continue
		}
		for _, variant := range []string{"unnest", "unnest_1000", "unnest_uuid_order", "copy_indexed", "copy_unindexed"} {
			profile, variant := profile, variant
			t.Run(profile+"/"+variant, func(t *testing.T) {
				measureBareWrite(t, profile, variant, iterations)
			})
		}
	}
}

func minimum(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	least := values[0]
	for _, value := range values[1:] {
		if value < least {
			least = value
		}
	}
	return least
}

func measureBareWrite(t *testing.T, profile, variant string, iterations int) {
	t.Helper()
	f := setup(t)
	ctx := context.Background()

	// One tiny real import creates the parent workout row and the collector lease
	// that the association foreign key and the device-binding trigger require.
	seedBundle := perfBundle(t, f, profile, 7)
	seedBundle.Samples = seedBundle.Samples[:1]
	seedBundle.Series = nil
	seedBundle.Routes = nil
	seedBundle.Observation = nil
	seedBundle.CompleteSampleTypes = nil
	seedBundle.CompleteRoutes = false
	raw, err := json.Marshal(seedBundle)
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	receipt, err := f.s.ImportWorkoutBundle(ctx, f.p, seedBundle, raw)
	if err != nil {
		t.Fatalf("seed import: %v", err)
	}
	var seedReceipt syncing.WorkoutImportReceipt
	if err := json.Unmarshal(receipt, &seedReceipt); err != nil {
		t.Fatalf("decode seed receipt: %v", err)
	}
	workoutID := seedReceipt.WorkoutID

	dropped := []string{}
	if variant == "copy_unindexed" {
		dropped = dropSecondaryIndexes(t, f)
	}

	var samples []float64
	var indexBuildMS []float64
	walBefore, fpiBefore := readWALCounters(t, f)
	tupBefore := readTableInserts(t, f)
	for i := 0; i < iterations; i++ {
		seed := uint64(900000) + uint64(i)
		bundle := perfBundle(t, f, profile, seed)
		facts := bareFacts(t, bundle.Samples)
		var buildMS float64
		start := time.Now()
		writeBareFacts(t, f, variant, f.p.DatasetID, workoutID, facts, &buildMS, i == iterations-1)
		elapsed := float64(time.Since(start).Microseconds()) / 1000
		samples = append(samples, elapsed)
		if variant == "copy_unindexed" {
			indexBuildMS = append(indexBuildMS, buildMS)
		}
		t.Logf("BARE_PG_ITER profile=%s variant=%s i=%d write_ms=%.1f index_build_ms=%.1f", profile, variant, i, elapsed, buildMS)
	}
	// pg_stat_wal and pg_stat_user_tables are updated asynchronously, so only an
	// aggregate over the whole subtest is trustworthy; per-iteration deltas are
	// frequently 0 even when rows were written.
	walAfter, fpiAfter := readWALCounters(t, f)
	tupAfter := readTableInserts(t, f)
	walTotal := float64(walAfter - walBefore)
	fpiTotal := float64(fpiAfter - fpiBefore)
	rowsTotal := float64(tupAfter - tupBefore)

	summary := map[string]any{
		"profile":              profile,
		"variant":              variant,
		"iterations":           iterations,
		"write_p50_ms":         nearestRank(samples, 0.5),
		"write_p95_ms":         nearestRank(samples, 0.95),
		"write_min_ms":         minimum(samples),
		"wal_bytes_per_import": walTotal / float64(iterations),
		"wal_fpi_per_import":   fpiTotal / float64(iterations),
		"fpi_share":            fpiTotal * 8192 / walTotal,
		"rows_per_import":      rowsTotal / float64(iterations),
		"wal_per_row":          walTotal / rowsTotal,
		"wal_per_sample":       walTotal / (rowsTotal / 2),
		"dropped_indexes":      dropped,
	}
	if len(indexBuildMS) > 0 {
		summary["index_build_p50_ms"] = nearestRank(indexBuildMS, 0.5)
	}
	encoded, _ := json.Marshal(summary)
	t.Logf("BARE_PG=%s", encoded)
}

// bareFact is one row of the fact set, independent of the production strategy.
type bareFact struct {
	sampleType string
	sampleUUID string
	start, end time.Time
	value      *float64
	category   *int32
	unit       *string
	source     string
	hash       []byte
	metadata   string
}

func bareFacts(t *testing.T, events []syncing.Event) []bareFact {
	t.Helper()
	facts := make([]bareFact, 0, len(events))
	for _, event := range events {
		canonical, hash := event.Canonical()
		_ = canonical
		metadata := "{}"
		if len(event.Metadata) > 0 {
			encoded, err := json.Marshal(event.Metadata)
			if err != nil {
				t.Fatalf("marshal metadata: %v", err)
			}
			metadata = string(encoded)
		}
		var value *float64
		var category *int32
		var unit *string
		if event.Quantity != nil {
			v := event.Quantity.Value
			u := event.Quantity.Unit
			value, unit = &v, &u
		}
		if event.Category != nil {
			c := int32(*event.Category)
			category = &c
		}
		facts = append(facts, bareFact{
			sampleType: event.Type,
			sampleUUID: event.UUID,
			start:      time.UnixMicro(*event.StartUs).UTC(),
			end:        time.UnixMicro(*event.EndUs).UTC(),
			value:      value,
			category:   category,
			unit:       unit,
			source:     event.Source.BundleID,
			hash:       hash,
			metadata:   metadata,
		})
	}
	return facts
}

func writeBareFacts(t *testing.T, f *fixture, variant, dataset, workoutID string, facts []bareFact, indexBuildMS *float64, rebuild bool) {
	t.Helper()
	ctx := context.Background()
	tx, err := f.s.Pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout='600s'"); err != nil {
		t.Fatalf("set statement_timeout: %v", err)
	}
	ordered := facts
	if variant == "unnest_uuid_order" {
		ordered = append([]bareFact(nil), facts...)
		sort.Slice(ordered, func(i, j int) bool {
			if ordered[i].sampleType != ordered[j].sampleType {
				return ordered[i].sampleType < ordered[j].sampleType
			}
			return ordered[i].sampleUUID < ordered[j].sampleUUID
		})
	}
	switch variant {
	case "unnest", "unnest_uuid_order":
		writeBareUnnest(ctx, t, tx, dataset, workoutID, ordered, 5000)
	case "unnest_1000":
		writeBareUnnest(ctx, t, tx, dataset, workoutID, ordered, 1000)
	case "copy_indexed", "copy_unindexed":
		writeBareCopy(ctx, t, tx, dataset, workoutID, ordered)
	default:
		t.Fatalf("unknown variant %q", variant)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if variant == "copy_unindexed" && rebuild {
		start := time.Now()
		if _, err := f.s.Pool.Exec(ctx, `CREATE INDEX health_samples_type_time ON health_samples(dataset_id, sample_type, event_start, sample_uuid) INCLUDE (event_end, value_numeric, unit)`); err != nil {
			t.Fatalf("rebuild type_time index: %v", err)
		}
		if _, err := f.s.Pool.Exec(ctx, `CREATE INDEX health_samples_analysis_identity ON health_samples(dataset_id, sample_uuid, sample_type) INCLUDE (event_start, event_end, value_numeric, unit, content_sha256)`); err != nil {
			t.Fatalf("rebuild analysis identity index: %v", err)
		}
		if _, err := f.s.Pool.Exec(ctx, `CREATE INDEX workout_samples_workout ON workout_samples(dataset_id, workout_uuid)`); err != nil {
			t.Fatalf("rebuild workout index: %v", err)
		}
		if _, err := f.s.Pool.Exec(ctx, `CREATE UNIQUE INDEX workout_samples_association_id ON workout_samples(dataset_id, association_id) WHERE association_id IS NOT NULL`); err != nil {
			t.Fatalf("rebuild association index: %v", err)
		}
		*indexBuildMS = float64(time.Since(start).Microseconds()) / 1000
	}
}

const bareUnnestStatement = `INSERT INTO health_samples(dataset_id,sample_type,sample_uuid,event_start,event_end,value_numeric,value_category,unit,source_bundle_id,content_sha256,entity_revision,metadata)
SELECT $1::uuid, s.sample_type, s.sample_uuid::uuid, s.event_start, s.event_end, s.value_numeric, s.value_category, s.unit, s.source_bundle_id, s.content_sha256, $2::bigint, s.metadata::jsonb
FROM unnest($3::text[], $4::text[], $5::timestamptz[], $6::timestamptz[], $7::double precision[], $8::integer[], $9::text[], $10::text[], $11::bytea[], $12::text[])
AS s(sample_type, sample_uuid, event_start, event_end, value_numeric, value_category, unit, source_bundle_id, content_sha256, metadata)`

const bareAssociationUnnestStatement = `INSERT INTO workout_samples(dataset_id,sample_type,sample_uuid,workout_uuid,association_id,source_bundle_id,commit_rev)
SELECT $1::uuid, s.sample_type, s.sample_uuid::uuid, $2::uuid, s.association_id::uuid, s.source_bundle_id, $3::bigint
FROM unnest($4::text[], $5::text[], $6::text[], $7::text[])
AS s(sample_type, sample_uuid, association_id, source_bundle_id)`

func writeBareUnnest(ctx context.Context, t *testing.T, tx pgx.Tx, dataset, workoutID string, facts []bareFact, batch int) {
	t.Helper()
	for start := 0; start < len(facts); start += batch {
		end := start + batch
		if end > len(facts) {
			end = len(facts)
		}
		chunk := facts[start:end]
		types := make([]string, len(chunk))
		uuids := make([]string, len(chunk))
		starts := make([]time.Time, len(chunk))
		ends := make([]time.Time, len(chunk))
		values := make([]*float64, len(chunk))
		categories := make([]*int32, len(chunk))
		units := make([]*string, len(chunk))
		sources := make([]string, len(chunk))
		hashes := make([][]byte, len(chunk))
		metadata := make([]string, len(chunk))
		assocTypes := make([]string, len(chunk))
		assocUUIDs := make([]string, len(chunk))
		assocIDs := make([]string, len(chunk))
		assocSources := make([]string, len(chunk))
		for index, fact := range chunk {
			types[index] = fact.sampleType
			uuids[index] = fact.sampleUUID
			starts[index] = fact.start
			ends[index] = fact.end
			values[index] = fact.value
			categories[index] = fact.category
			units[index] = fact.unit
			sources[index] = fact.source
			hashes[index] = fact.hash
			metadata[index] = fact.metadata
			assocTypes[index] = fact.sampleType
			assocUUIDs[index] = fact.sampleUUID
			assocIDs[index] = syncing.NewID()
			assocSources[index] = fact.source
		}
		if _, err := tx.Exec(ctx, bareUnnestStatement, dataset, 2, types, uuids, starts, ends, values, categories, units, sources, hashes, metadata); err != nil {
			t.Fatalf("unnest insert: %v", err)
		}
		if _, err := tx.Exec(ctx, bareAssociationUnnestStatement, dataset, workoutID, 2, assocTypes, assocUUIDs, assocIDs, assocSources); err != nil {
			t.Fatalf("unnest association insert: %v", err)
		}
	}
}

func writeBareCopy(ctx context.Context, t *testing.T, tx pgx.Tx, dataset, workoutID string, facts []bareFact) {
	t.Helper()
	sampleRows := make([][]any, 0, len(facts))
	associationRows := make([][]any, 0, len(facts))
	for _, fact := range facts {
		sampleRows = append(sampleRows, []any{
			dataset, fact.sampleType, fact.sampleUUID, fact.start, fact.end,
			fact.value, fact.category, fact.unit, fact.source, fact.hash, int64(2), fact.metadata,
		})
		associationRows = append(associationRows, []any{
			dataset, fact.sampleType, fact.sampleUUID, workoutID, syncing.NewID(), fact.source, int64(2),
		})
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"health_samples"}, []string{
		"dataset_id", "sample_type", "sample_uuid", "event_start", "event_end",
		"value_numeric", "value_category", "unit", "source_bundle_id", "content_sha256",
		"entity_revision", "metadata",
	}, pgx.CopyFromRows(sampleRows)); err != nil {
		t.Fatalf("copy health_samples: %v", err)
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"workout_samples"}, []string{
		"dataset_id", "sample_type", "sample_uuid", "workout_uuid", "association_id",
		"source_bundle_id", "commit_rev",
	}, pgx.CopyFromRows(associationRows)); err != nil {
		t.Fatalf("copy workout_samples: %v", err)
	}
}

func dropSecondaryIndexes(t *testing.T, f *fixture) []string {
	t.Helper()
	ctx := context.Background()
	rows, err := f.s.Pool.Query(ctx, `SELECT indexname FROM pg_indexes
		WHERE schemaname='public' AND tablename IN ('health_samples','workout_samples')
		AND indexname NOT LIKE '%_pkey' ORDER BY indexname`)
	if err != nil {
		t.Fatalf("list indexes: %v", err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("collect indexes: %v", err)
	}
	for _, name := range names {
		if _, err := f.s.Pool.Exec(ctx, "DROP INDEX "+name); err != nil {
			t.Fatalf("drop index %s: %v", name, err)
		}
	}
	return names
}

func readWALCounters(t *testing.T, f *fixture) (int64, int64) {
	t.Helper()
	var walBytes, walFPI int64
	if err := f.s.Pool.QueryRow(context.Background(),
		`SELECT wal_bytes, wal_fpi FROM pg_stat_wal`).Scan(&walBytes, &walFPI); err != nil {
		t.Fatalf("read pg_stat_wal: %v", err)
	}
	return walBytes, walFPI
}

func readTableInserts(t *testing.T, f *fixture) int64 {
	t.Helper()
	var inserted int64
	if err := f.s.Pool.QueryRow(context.Background(),
		`SELECT COALESCE(sum(n_tup_ins), 0)::bigint FROM pg_stat_user_tables`).Scan(&inserted); err != nil {
		t.Fatalf("read pg_stat_user_tables: %v", err)
	}
	return inserted
}
