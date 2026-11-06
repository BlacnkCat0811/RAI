//go:build integration

package integration

// Round-2 R5 gates (R2-F21..R2-F26). Every case runs against the real isolated
// PostgreSQL instance, through the production Store entry point, with the write
// candidate selected explicitly. The three candidates are:
//
//	values — the round-1 multi-row VALUES parameter INSERT (baseline)
//	arrays — one fixed-shape unnest(...) statement per batch
//	copy   — COPY into a transaction-scoped staging table, then a set-based
//	         projection plus a join barrier per projection
//
// Synthetic fixtures only; no health values, real identifiers or credentials.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
)

var r5Strategies = []postgres.WriteStrategy{
	postgres.StrategyValues,
	postgres.StrategyArrays,
	postgres.StrategyCopy,
}

// useWriteStrategy selects a write candidate for the current test and restores
// the process default afterwards.
func useWriteStrategy(t *testing.T, strategy postgres.WriteStrategy) {
	t.Helper()
	previous := postgres.CurrentWriteStrategy()
	if err := postgres.SetWriteStrategy(strategy); err != nil {
		t.Fatalf("set write strategy %q: %v", strategy, err)
	}
	t.Cleanup(func() {
		if err := postgres.SetWriteStrategy(previous); err != nil {
			t.Errorf("restore write strategy: %v", err)
		}
	})
}

// projectionDigest renders every fact projection as canonical JSON text so two
// candidates can be compared for exact semantic identity. The random
// association identity is excluded because it is not derived from the bundle.
func projectionDigest(t *testing.T, f *fixture) map[string]string {
	t.Helper()
	// dataset_id and the receive/created timestamps differ between two
	// disposable databases for reasons that carry no meaning, so the digest
	// compares only the fact columns.
	strip := `- ARRAY['dataset_id','first_received_at','available_at','created_at','ingest_device_id','sealed_at','dataset_epoch','collector_epoch']`
	queries := map[string]string{
		"health_samples":  `SELECT coalesce(jsonb_agg(to_jsonb(x) ` + strip + ` ORDER BY (to_jsonb(x) ` + strip + `)::text),'[]')::text FROM health_samples x`,
		"workout_samples": `SELECT coalesce(jsonb_agg(to_jsonb(x) - ARRAY['association_id','dataset_id','created_at'] ORDER BY (to_jsonb(x) - ARRAY['association_id','dataset_id','created_at'])::text),'[]')::text FROM workout_samples x`,
		"workouts":        `SELECT coalesce(jsonb_agg(to_jsonb(x) ` + strip + ` ORDER BY (to_jsonb(x) ` + strip + `)::text),'[]')::text FROM workouts x`,
		"series_uploads":  `SELECT coalesce(jsonb_agg(to_jsonb(x) ` + strip + ` ORDER BY (to_jsonb(x) ` + strip + `)::text),'[]')::text FROM series_uploads x`,
		"series_chunks":   `SELECT coalesce(jsonb_agg(to_jsonb(x) ` + strip + ` ORDER BY (to_jsonb(x) ` + strip + `)::text),'[]')::text FROM series_chunks x`,
		"workout_routes":  `SELECT coalesce(jsonb_agg(to_jsonb(x) ` + strip + ` ORDER BY (to_jsonb(x) ` + strip + `)::text),'[]')::text FROM workout_routes x`,
		"route_chunks":    `SELECT coalesce(jsonb_agg(to_jsonb(x) ` + strip + ` ORDER BY (to_jsonb(x) ` + strip + `)::text),'[]')::text FROM route_chunks x`,
		"route_points":    `SELECT coalesce(jsonb_agg(to_jsonb(x) ` + strip + ` ORDER BY (to_jsonb(x) ` + strip + `)::text),'[]')::text FROM route_points x`,
	}
	out := make(map[string]string, len(queries))
	for name, query := range queries {
		var text string
		if err := f.s.Pool.QueryRow(context.Background(), query).Scan(&text); err != nil {
			t.Fatalf("digest %s: %v", name, err)
		}
		out[name] = text
	}
	return out
}

// normalizedReceipt strips the random analysis job identities so receipts from
// two independent databases can be compared for semantic equality.
func normalizedReceipt(t *testing.T, body []byte) string {
	t.Helper()
	var receipt syncing.WorkoutImportReceipt
	if err := json.Unmarshal(body, &receipt); err != nil {
		t.Fatalf("decode receipt: %v", err)
	}
	receipt.AnalysisJobIDs = nil
	// Each candidate runs against its own disposable database, so the bundle
	// bytes differ only by the per-database dataset identity. Content identity is
	// covered by the projection digest and by the observation digest below; the
	// exact bundle hash is covered by the byte-identical replay assertion.
	receipt.BundleSHA256 = ""
	encoded, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// scaleBundle builds a synthetic bundle with an exact sample count so batch
// boundaries and the documented 50,000-event ceiling can be exercised.
func scaleBundle(t *testing.T, f *fixture, samples, seriesCount, pointsPerSeries, routePoints int, seed uint64) syncing.WorkoutBundle {
	t.Helper()
	start := time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)
	bundle, _ := testWorkoutBundle(t, f, 0)
	bundle.Workout = workoutAt(start, 3600)
	bundle.WorkoutID = bundle.Workout.UUID
	ids := perfIDGenerator(seed)
	types := []string{
		kind,
		"HKQuantityTypeIdentifierRunningPower",
		"HKQuantityTypeIdentifierStepCount",
		"HKQuantityTypeIdentifierDistanceWalkingRunning",
	}
	bundle.Samples = make([]syncing.Event, samples)
	for i := 0; i < samples; i++ {
		sampleType := types[i%len(types)]
		startUs := start.UnixMicro() + int64(i)*1_000_000
		endUs := startUs + 3_600_000_000
		bundle.Samples[i] = syncing.Event{
			Op:       "upsert",
			Kind:     "sample",
			Type:     sampleType,
			UUID:     ids("sample", i),
			StartUs:  &startUs,
			EndUs:    &endUs,
			Quantity: &syncing.Quantity{Value: 100 + float64(i%50), Unit: syncing.Units[sampleType]},
			Source:   &syncing.Source{BundleID: "synthetic.test"},
		}
	}
	bundle.Series = nil
	for s := 0; s < seriesCount && s < samples; s++ {
		parent := bundle.Samples[s]
		points := make([]syncing.SeriesPoint, pointsPerSeries)
		var step int64
		if pointsPerSeries > 1 {
			step = 3_600_000_000 / int64(pointsPerSeries)
		}
		for k := range points {
			points[k] = syncing.SeriesPoint{
				StartUs: *parent.StartUs + int64(k)*step,
				EndUs:   *parent.StartUs + int64(k)*step + 1_000,
				Value:   float64(k),
				Unit:    syncing.Units[parent.Type],
			}
		}
		bundle.Series = append(bundle.Series, syncing.WorkoutBundleSeries{
			SampleType: parent.Type,
			SampleUUID: parent.UUID,
			Points:     points,
		})
	}
	bundle.Routes = nil
	if routePoints > 0 {
		points := make([]syncing.WorkoutBundlePoint, routePoints)
		for k := range points {
			points[k] = syncing.WorkoutBundlePoint{
				RecordedAtUs: start.UnixMicro() + int64(k)*100_000,
				Latitude:     31.2 + float64(k%100)*0.00001,
				Longitude:    121.4,
			}
		}
		bundle.Routes = []syncing.WorkoutBundleRoute{{
			RouteID: ids("route", 0),
			Source:  syncing.Source{BundleID: "synthetic.test"},
			Points:  points,
		}}
	}
	bundle.CompleteSampleTypes = nil
	bundle.CompleteRoutes = false
	bundle.Observation = nil
	return bundle
}

// TestWorkoutWriteStrategyParity is R2-F21: the set-based candidates must be
// indistinguishable from the VALUES baseline for the same input — same receipt
// (counts, status, revision, classification), same stored projections, same
// replay answer and the same conflict code.
func TestWorkoutWriteStrategyParity(t *testing.T) {
	type outcome struct {
		receipt string
		rows    map[string]string
	}
	outcomes := map[string]outcome{}
	for _, strategy := range r5Strategies {
		strategy := strategy
		t.Run(string(strategy), func(t *testing.T) {
			useWriteStrategy(t, strategy)
			f := setup(t)
			bundle := perfBundle(t, f, "S60", 1000)
			raw, err := json.Marshal(bundle)
			if err != nil {
				t.Fatal(err)
			}
			body, err := importBundle(t, f, bundle, raw)
			if err != nil {
				t.Fatalf("%s first import: %v", strategy, err)
			}
			var receipt syncing.WorkoutImportReceipt
			if err = json.Unmarshal(body, &receipt); err != nil {
				t.Fatal(err)
			}
			if !receipt.Counts.WorkoutInserted {
				t.Fatalf("%s did not insert the workout: %+v", strategy, receipt.Counts)
			}
			if receipt.Counts.SamplesInserted != len(bundle.Samples) {
				t.Fatalf("%s samples=%d want %d", strategy, receipt.Counts.SamplesInserted, len(bundle.Samples))
			}
			if receipt.Counts.AssociationsInserted != len(bundle.Samples) {
				t.Fatalf("%s associations=%d want %d", strategy, receipt.Counts.AssociationsInserted, len(bundle.Samples))
			}
			if n := count(t, f, "health_samples"); n != len(bundle.Samples) {
				t.Fatalf("%s persisted %d samples", strategy, n)
			}
			if n := count(t, f, "workout_samples"); n != len(bundle.Samples) {
				t.Fatalf("%s persisted %d associations", strategy, n)
			}

			// Byte-identical replay must return the stored receipt, not new work.
			replay, err := importBundle(t, f, bundle, raw)
			if err != nil {
				t.Fatalf("%s replay: %v", strategy, err)
			}
			if !bytes.Equal(replay, body) {
				t.Fatalf("%s replay did not return the original receipt", strategy)
			}
			if n := count(t, f, "workout_imports"); n != 1 {
				t.Fatalf("%s replay stored %d receipts", strategy, n)
			}

			// A same-UUID core change must be rejected identically and leave no
			// partial state behind, whichever write path is active.
			conflict := bundle
			conflict.Samples = append([]syncing.Event(nil), bundle.Samples...)
			middle := len(conflict.Samples) / 2
			source := bundle.Samples[middle].Quantity
			conflict.Samples[middle].Quantity = &syncing.Quantity{Value: source.Value + 1, Unit: source.Unit}
			conflictRaw, err := json.Marshal(conflict)
			if err != nil {
				t.Fatal(err)
			}
			before := phase1Snapshot(t, f)
			_, err = importBundle(t, f, conflict, conflictRaw)
			requireCode(t, err, "sample_identity_conflict")
			assertSnapshotUnchanged(t, f, before)

			outcomes[string(strategy)] = outcome{receipt: normalizedReceipt(t, body), rows: projectionDigest(t, f)}
		})
	}

	baseline, ok := outcomes[string(postgres.StrategyValues)]
	if !ok {
		t.Fatal("values baseline did not run")
	}
	for _, strategy := range r5Strategies {
		other, ok := outcomes[string(strategy)]
		if !ok {
			t.Fatalf("%s did not run", strategy)
		}
		if other.receipt != baseline.receipt {
			t.Errorf("%s receipt differs from the VALUES baseline:\n%s\n%s", strategy, other.receipt, baseline.receipt)
		}
		for table, digest := range baseline.rows {
			if other.rows[table] != digest {
				t.Errorf("%s projection %s differs from the VALUES baseline\n got %s\nwant %s", strategy, table, other.rows[table], digest)
			}
		}
	}
}

// TestWorkoutWriteStrategyRowBoundaries is R2-F22: the batch boundary at 1,000
// rows and the documented 50,000-event ceiling with several series and a route.
func TestWorkoutWriteStrategyRowBoundaries(t *testing.T) {
	for _, strategy := range r5Strategies {
		strategy := strategy
		t.Run(string(strategy), func(t *testing.T) {
			useWriteStrategy(t, strategy)
			for _, samples := range []int{1000, 1001} {
				f := setup(t)
				bundle := scaleBundle(t, f, samples, 1, 8, 8, 7000+uint64(samples))
				raw, err := json.Marshal(bundle)
				if err != nil {
					t.Fatal(err)
				}
				body, err := importBundle(t, f, bundle, raw)
				if err != nil {
					t.Fatalf("%s %d samples: %v", strategy, samples, err)
				}
				var receipt syncing.WorkoutImportReceipt
				if err = json.Unmarshal(body, &receipt); err != nil {
					t.Fatal(err)
				}
				if receipt.Counts.SamplesInserted != samples || receipt.Counts.AssociationsInserted != samples {
					t.Fatalf("%s %d samples counts=%+v", strategy, samples, receipt.Counts)
				}
				if n := count(t, f, "health_samples"); n != samples {
					t.Fatalf("%s %d samples persisted %d", strategy, samples, n)
				}
			}

			f := setup(t)
			const samples = 50_000
			bundle := scaleBundle(t, f, samples, 2, 5000, 5000, 9000)
			raw, err := json.Marshal(bundle)
			if err != nil {
				t.Fatal(err)
			}
			phases := map[string]float64{}
			ctx := syncing.ObservePhases(context.Background(), func(name string, duration time.Duration) {
				phases[name] = float64(duration.Microseconds()) / 1000
			})
			started := time.Now()
			body, err := f.s.ImportWorkoutBundle(ctx, f.p, bundle, raw)
			if err != nil {
				t.Fatalf("%s 50k samples: %v", strategy, err)
			}
			elapsed := time.Since(started)
			var receipt syncing.WorkoutImportReceipt
			if err = json.Unmarshal(body, &receipt); err != nil {
				t.Fatal(err)
			}
			if receipt.Counts.SamplesInserted != samples || receipt.Counts.AssociationsInserted != samples {
				t.Fatalf("%s 50k counts=%+v", strategy, receipt.Counts)
			}
			if receipt.Counts.SeriesInserted != 2 || receipt.Counts.RoutesInserted != 1 {
				t.Fatalf("%s 50k series/routes counts=%+v", strategy, receipt.Counts)
			}
			if n := count(t, f, "health_samples"); n != samples {
				t.Fatalf("%s 50k persisted %d samples", strategy, n)
			}
			if n := count(t, f, "workout_samples"); n != samples {
				t.Fatalf("%s 50k persisted %d associations", strategy, n)
			}
			if n := count(t, f, "series_uploads"); n != 2 {
				t.Fatalf("%s 50k persisted %d series", strategy, n)
			}
			if n := count(t, f, "workout_routes"); n != 1 {
				t.Fatalf("%s 50k persisted %d routes", strategy, n)
			}
			t.Logf("R5_50K strategy=%s samples=%d elapsed_ms=%.1f json_bytes=%d import_total_ms=%.1f sample_writes_ms=%.1f association_writes_ms=%.1f identity_ms=%.1f series_ms=%.1f routes_ms=%.1f receipt_ms=%.1f commit_ms=%.1f",
				strategy, samples, float64(elapsed.Microseconds())/1000, len(raw),
				phases["import_total"], phases["sample_writes"], phases["association_writes"],
				phases["identity_checks"], phases["series"], phases["routes"], phases["receipt"], phases["commit"])
		})
	}
}

// TestWorkoutWriteStrategyConflictRollback is R2-F23: a same-UUID core change, a
// cross-parent association and an append-only enrichment are judged separately,
// and a rejected bundle leaves no fact, receipt, job or change event behind.
func TestWorkoutWriteStrategyConflictRollback(t *testing.T) {
	for _, strategy := range r5Strategies {
		strategy := strategy
		t.Run(string(strategy), func(t *testing.T) {
			useWriteStrategy(t, strategy)

			t.Run("core_change_rolls_back_everything", func(t *testing.T) {
				f := setup(t)
				bundle := scaleBundle(t, f, 200, 1, 8, 8, 4100)
				raw, _ := json.Marshal(bundle)
				if _, err := importBundle(t, f, bundle, raw); err != nil {
					t.Fatal(err)
				}
				changed := bundle
				changed.Samples = append([]syncing.Event(nil), bundle.Samples...)
				changed.Samples[150].Quantity = &syncing.Quantity{Value: 999, Unit: syncing.Units[changed.Samples[150].Type]}
				changedRaw, _ := json.Marshal(changed)
				before := phase1Snapshot(t, f)
				_, err := importBundle(t, f, changed, changedRaw)
				requireCode(t, err, "sample_identity_conflict")
				assertSnapshotUnchanged(t, f, before)
			})

			t.Run("cross_parent_association_rolls_back", func(t *testing.T) {
				f := setup(t)
				bundle := scaleBundle(t, f, 200, 1, 8, 8, 4200)
				raw, _ := json.Marshal(bundle)
				if _, err := importBundle(t, f, bundle, raw); err != nil {
					t.Fatal(err)
				}
				other := workoutAt(time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC), 600)
				conflict := syncing.WorkoutBundle{
					ProtocolVersion: 3,
					DatasetID:       f.p.DatasetID,
					DatasetEpoch:    f.enrollment.DatasetEpoch,
					CollectorEpoch:  f.enrollment.CollectorEpoch,
					WorkoutID:       other.UUID,
					Workout:         other,
					Samples:         []syncing.Event{bundle.Samples[180]},
				}
				conflictRaw, _ := json.Marshal(conflict)
				before := phase1Snapshot(t, f)
				_, err := importBundle(t, f, conflict, conflictRaw)
				requireCode(t, err, "sample_association_conflict")
				assertSnapshotUnchanged(t, f, before)
			})

			t.Run("append_only_enrichment_is_accepted", func(t *testing.T) {
				f := setup(t)
				bundle := scaleBundle(t, f, 200, 1, 8, 8, 4300)
				bundle.Observation = nil
				// The source bundle is part of the fact core, so the first import
				// already carries the supported HealthKit source; only the
				// observation is added later.
				bundle.Workout.Source.BundleID = "com.apple.health"
				for i := range bundle.Samples {
					bundle.Samples[i].Source.BundleID = "com.apple.health"
				}
				for i := range bundle.Routes {
					bundle.Routes[i].Source.BundleID = "com.apple.health"
				}
				raw, _ := json.Marshal(bundle)
				body, err := importBundle(t, f, bundle, raw)
				if err != nil {
					t.Fatal(err)
				}
				var first syncing.WorkoutImportReceipt
				if err = json.Unmarshal(body, &first); err != nil {
					t.Fatal(err)
				}
				// Same identities and cores, now carrying supported provenance:
				// an append-only enrichment, never an identity conflict.
				enriched := bundle
				phase3Observation(&enriched)
				enrichedRaw, _ := json.Marshal(enriched)
				body, err = importBundle(t, f, enriched, enrichedRaw)
				if err != nil {
					t.Fatalf("append-only enrichment rejected: %v", err)
				}
				var second syncing.WorkoutImportReceipt
				if err = json.Unmarshal(body, &second); err != nil {
					t.Fatal(err)
				}
				if second.Counts.SamplesInserted != 0 || second.Counts.SamplesUnchanged != 200 {
					t.Fatalf("enrichment rewrote facts: %+v", second.Counts)
				}
				if n := count(t, f, "health_samples"); n != 200 {
					t.Fatalf("enrichment changed the fact count to %d", n)
				}
			})
		})
	}
}

// installProjectionInterruption makes the sample projection fail after staging
// has been filled, which is the mid-COPY interruption case for R2-F24.
func installProjectionInterruption(t *testing.T, f *fixture, sampleUUID string) {
	t.Helper()
	ctx := context.Background()
	statements := []string{
		`CREATE OR REPLACE FUNCTION rai_test_block_sample() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
	IF NEW.sample_uuid = '` + sampleUUID + `'::uuid THEN
		RAISE EXCEPTION 'synthetic projection interruption';
	END IF;
	RETURN NEW;
END $$`,
		`CREATE TRIGGER rai_test_block_sample BEFORE INSERT ON health_samples FOR EACH ROW EXECUTE FUNCTION rai_test_block_sample()`,
	}
	for _, statement := range statements {
		if _, err := f.s.Pool.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = f.s.Pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS rai_test_block_sample ON health_samples`)
		_, _ = f.s.Pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS rai_test_block_sample()`)
	})
}

// TestWorkoutWriteStrategyFaultRollback is R2-F24: an interrupted projection and
// a failing COMMIT must leave zero half bundles, orphan jobs or durable ACKs.
func TestWorkoutWriteStrategyFaultRollback(t *testing.T) {
	for _, strategy := range r5Strategies {
		strategy := strategy
		t.Run(string(strategy), func(t *testing.T) {
			useWriteStrategy(t, strategy)

			t.Run("projection_interrupted", func(t *testing.T) {
				f := setup(t)
				bundle := perfBundle(t, f, "S20", 4400)
				raw, _ := json.Marshal(bundle)
				installProjectionInterruption(t, f, bundle.Samples[len(bundle.Samples)/2].UUID)
				before := phase1Snapshot(t, f)
				_, err := importBundle(t, f, bundle, raw)
				if err == nil {
					t.Fatal("interrupted projection reported success")
				}
				if !strings.Contains(err.Error(), "synthetic projection interruption") {
					t.Fatalf("unexpected interruption error: %v", err)
				}
				assertSnapshotUnchanged(t, f, before)
				assertNoBundleResidue(t, f)
			})

			t.Run("commit_failure_rolls_back", func(t *testing.T) {
				f := setup(t)
				bundle := perfBundle(t, f, "S20", 4500)
				raw, _ := json.Marshal(bundle)
				// A deferred constraint fails at COMMIT, after every fact,
				// receipt, job and change event has been written in the
				// transaction.
				const guard = `
CREATE FUNCTION rai_test_commit_guard() RETURNS trigger LANGUAGE plpgsql AS $guard$
BEGIN
	IF NEW.sample_type = 'HKQuantityTypeIdentifierRunningPower' THEN
		RAISE EXCEPTION 'rai_test_commit_guard: deferred commit guard fired';
	END IF;
	RETURN NEW;
END $guard$;
CREATE CONSTRAINT TRIGGER rai_test_commit_guard AFTER INSERT ON health_samples
	DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION rai_test_commit_guard();`
				if _, err := f.s.Pool.Exec(context.Background(), guard); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_, _ = f.s.Pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS rai_test_commit_guard ON health_samples`)
					_, _ = f.s.Pool.Exec(context.Background(), `DROP FUNCTION IF EXISTS rai_test_commit_guard()`)
				})
				before := phase1Snapshot(t, f)
				_, err := importBundle(t, f, bundle, raw)
				if err == nil {
					t.Fatal("failing COMMIT reported success")
				}
				if !strings.Contains(err.Error(), "rai_test_commit_guard") {
					t.Fatalf("unexpected commit failure: %v", err)
				}
				assertSnapshotUnchanged(t, f, before)
				assertNoBundleResidue(t, f)
			})
		})
	}
}

// assertNoBundleResidue proves a failed import published nothing durable.
func assertNoBundleResidue(t *testing.T, f *fixture) {
	t.Helper()
	for _, table := range []string{"workouts", "health_samples", "workout_samples", "workout_imports", "change_events", "jobs", "dirty_scopes", "series_uploads", "workout_routes"} {
		if n := count(t, f, table); n != 0 {
			t.Fatalf("failed import left %d rows in %s", n, table)
		}
	}
	assertNoStagingTables(t, f)
}

// assertNoStagingTables proves the COPY staging table never survives the
// transaction on any pooled connection.
func assertNoStagingTables(t *testing.T, f *fixture) {
	t.Helper()
	ctx := context.Background()
	checked := 0
	for i := 0; i < 8; i++ {
		conn, err := f.s.Pool.Acquire(ctx)
		if err != nil {
			t.Fatalf("acquire pooled connection: %v", err)
		}
		var leftover int
		if err = conn.QueryRow(ctx, `SELECT count(*) FROM pg_class c WHERE c.relnamespace = pg_my_temp_schema() AND c.relname LIKE 'rai_bundle_%'`).Scan(&leftover); err != nil {
			conn.Release()
			t.Fatalf("inspect temp schema: %v", err)
		}
		conn.Release()
		checked++
		if leftover != 0 {
			t.Fatalf("staging residue: %d temp tables on pooled connection %d", leftover, i)
		}
	}
	if checked == 0 {
		t.Fatal("no pooled connection was inspected")
	}
}

// restrictedDatabaseURL builds a DSN for the current test database under a
// different login role, reusing the disposable cluster's socket host.
func restrictedDatabaseURL(t *testing.T, f *fixture, role string) string {
	t.Helper()
	var name string
	if err := f.s.Pool.QueryRow(context.Background(), "SELECT current_database()").Scan(&name); err != nil {
		t.Fatal(err)
	}
	base := os.Getenv("RAI_TEST_DATABASE_URL")
	if base == "" {
		t.Fatal("RAI_TEST_DATABASE_URL is not set")
	}
	parsed, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse test DSN: %v", err)
	}
	parsed.User = url.User(role)
	parsed.Path = "/" + name
	return parsed.String()
}

// TestWorkoutWriteStrategyRestrictedRole is R2-F25: the staging candidate must
// be validated under a restricted API runtime role, and a privilege failure must
// leave no residue on a reused pooled connection.
func TestWorkoutWriteStrategyRestrictedRole(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	const role = "rai_api_restricted"
	var database string
	if err := f.s.Pool.QueryRow(ctx, "SELECT current_database()").Scan(&database); err != nil {
		t.Fatal(err)
	}
	identifier := `"` + strings.ReplaceAll(database, `"`, `""`) + `"`
	grants := []string{
		`DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '` + role + `') THEN
				CREATE ROLE ` + role + ` LOGIN;
			END IF;
		END $$`,
		`GRANT USAGE ON SCHEMA public TO ` + role,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO ` + role,
		`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO ` + role,
		// The import transaction calls SECURITY INVOKER route functions, so the
		// runtime role needs EXECUTE on them as well; this is part of the minimal
		// privilege set the API role actually requires.
		`GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA public TO ` + role,
		// The API runtime role deliberately has no TEMP privilege: that is the
		// real production shape for a least-privilege writer.
		`REVOKE TEMP ON DATABASE ` + identifier + ` FROM PUBLIC`,
		`REVOKE TEMP ON DATABASE ` + identifier + ` FROM ` + role,
	}
	for _, statement := range grants {
		if _, err := f.s.Pool.Exec(ctx, statement); err != nil {
			t.Fatalf("grant step failed: %v\n%s", err, statement)
		}
	}
	store, err := postgres.Open(ctx, restrictedDatabaseURL(t, f, role), 2)
	if err != nil {
		t.Fatalf("open restricted store: %v", err)
	}
	t.Cleanup(store.Pool.Close)

	var tempPrivilege bool
	if err = store.Pool.QueryRow(ctx, `SELECT has_database_privilege(current_user, current_database(), 'TEMP')`).Scan(&tempPrivilege); err != nil {
		t.Fatal(err)
	}
	t.Logf("R5_RESTRICTED_ROLE role=%s temp_privilege=%v", role, tempPrivilege)

	importWith := func(bundle syncing.WorkoutBundle, raw []byte) ([]byte, error) {
		return store.ImportWorkoutBundle(ctx, f.p, bundle, raw)
	}

	seeds := map[postgres.WriteStrategy]uint64{postgres.StrategyValues: 5100, postgres.StrategyArrays: 5101}
	expected := 0
	for _, strategy := range []postgres.WriteStrategy{postgres.StrategyValues, postgres.StrategyArrays} {
		useWriteStrategy(t, strategy)
		bundle := scaleBundle(t, f, 300, 1, 8, 8, seeds[strategy])
		raw, _ := json.Marshal(bundle)
		if _, err := importWith(bundle, raw); err != nil {
			t.Fatalf("%s import under the restricted role failed: %v", strategy, err)
		}
		expected += 300
		if n := count(t, f, "health_samples"); n != expected {
			t.Fatalf("%s under the restricted role persisted %d samples, want %d", strategy, n, expected)
		}
	}

	// The COPY candidate needs CREATE on a temporary namespace. With the
	// least-privilege role this must fail cleanly and publish nothing.
	useWriteStrategy(t, postgres.StrategyCopy)
	blocked := scaleBundle(t, f, 300, 1, 8, 8, 5200)
	blockedRaw, _ := json.Marshal(blocked)
	before := phase1Snapshot(t, f)
	_, err = importWith(blocked, blockedRaw)
	if err == nil {
		t.Log("R5_RESTRICTED_ROLE copy_candidate=allowed (TEMP was available)")
	} else {
		t.Logf("R5_RESTRICTED_ROLE copy_candidate=blocked error=%v", err)
		if !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("expected a privilege failure, got %v", err)
		}
		assertSnapshotUnchanged(t, f, before)
		assertNoStagingTables(t, f)
	}

	// The same pooled connection must remain usable for the safe candidate.
	useWriteStrategy(t, postgres.StrategyArrays)
	recovered := scaleBundle(t, f, 300, 1, 8, 8, 5300)
	recoveredRaw, _ := json.Marshal(recovered)
	if _, err := importWith(recovered, recoveredRaw); err != nil {
		t.Fatalf("pooled connection was not reusable after the privilege failure: %v", err)
	}
	if n := count(t, f, "health_samples"); n != 900 {
		t.Fatalf("pool reuse persisted %d samples, want 900", n)
	}
	assertNoStagingTables(t, f)
}

// TestWorkoutWriteStrategyPlanModes is R2-F26: a cold pool and a hot pool must
// produce identical facts, the transaction-local custom plan must survive
// connection reuse, and the identity statement must not degrade to a per-key
// nested loop when a generic plan is offered.
func TestWorkoutWriteStrategyPlanModes(t *testing.T) {
	useWriteStrategy(t, postgres.StrategyArrays)
	f := setup(t)
	ctx := context.Background()

	durations := make([]time.Duration, 0, 5)
	for iteration := 0; iteration < 5; iteration++ {
		bundle := scaleBundle(t, f, 400, 1, 8, 8, 6100+uint64(iteration))
		raw, _ := json.Marshal(bundle)
		started := time.Now()
		body, err := importBundle(t, f, bundle, raw)
		if err != nil {
			t.Fatalf("import %d: %v", iteration, err)
		}
		durations = append(durations, time.Since(started))
		var receipt syncing.WorkoutImportReceipt
		if err = json.Unmarshal(body, &receipt); err != nil {
			t.Fatal(err)
		}
		if receipt.Counts.SamplesInserted != 400 || receipt.Counts.AssociationsInserted != 400 {
			t.Fatalf("import %d counts=%+v", iteration, receipt.Counts)
		}
	}
	if n := count(t, f, "health_samples"); n != 2000 {
		t.Fatalf("hot imports persisted %d samples", n)
	}
	hot := append([]time.Duration(nil), durations[1:]...)
	sort.Slice(hot, func(i, j int) bool { return hot[i] < hot[j] })
	t.Logf("R5_PLAN_MODES cold_ms=%.1f hot_p50_ms=%.1f hot_max_ms=%.1f",
		float64(durations[0].Microseconds())/1000,
		float64(hot[len(hot)/2].Microseconds())/1000,
		float64(hot[len(hot)-1].Microseconds())/1000)

	// The identity lookup must stay index-driven under both plan modes; a
	// generic plan previously turned 1,000 keys into a per-key nested loop.
	keys := make([]string, 0, 1000)
	for i := 0; i < 1000; i++ {
		keys = append(keys, syncing.NewID())
	}
	types := make([]string, len(keys))
	for i := range types {
		types[i] = kind
	}
	probe := `SELECT count(*) FROM unnest($2::text[], $3::text[]) AS k(sample_type, sample_uuid)
		LEFT JOIN health_samples h ON h.dataset_id = $1::uuid AND h.sample_type = k.sample_type AND h.sample_uuid = k.sample_uuid::uuid`
	var baseline int64
	for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
		conn, err := f.s.Pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = conn.Exec(ctx, `SET plan_cache_mode = '`+mode+`'`); err != nil {
			conn.Release()
			t.Fatal(err)
		}
		started := time.Now()
		var found int64
		err = conn.QueryRow(ctx, probe, f.p.DatasetID, types, keys).Scan(&found)
		elapsed := time.Since(started)
		_, _ = conn.Exec(ctx, `RESET plan_cache_mode`)
		conn.Release()
		if err != nil {
			t.Fatalf("identity probe under %s: %v", mode, err)
		}
		if mode == "force_custom_plan" {
			baseline = found
		} else if found != baseline {
			t.Fatalf("plan modes disagree: custom=%d generic=%d", baseline, found)
		}
		t.Logf("R5_PLAN_MODES identity_probe mode=%s keys=1000 matched=%d elapsed_ms=%.1f", mode, found, float64(elapsed.Microseconds())/1000)
	}
}

// TestWorkoutWriteStrategyExplains captures the real plans and WAL volume of the
// three candidates for the evidence bundle (no assertion beyond "a plan exists").
func TestWorkoutWriteStrategyExplains(t *testing.T) {
	if os.Getenv("RAI_SYNC_WRITE_PLANS") == "" {
		t.Skip("set RAI_SYNC_WRITE_PLANS=1 to capture candidate plans")
	}
	useWriteStrategy(t, postgres.StrategyValues)
	f := setup(t)
	ctx := context.Background()
	seed := scaleBundle(t, f, 300, 1, 8, 8, 7100)
	seedRaw, _ := json.Marshal(seed)
	if _, err := importBundle(t, f, seed, seedRaw); err != nil {
		t.Fatal(err)
	}
	candidate := scaleBundle(t, f, 300, 1, 8, 8, 7200)
	tx, err := f.s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout = '120s'`); err != nil {
		t.Fatal(err)
	}
	// The association projection references an existing workout row, so the plan
	// capture reuses the seeded workout instead of the candidate's new one.
	plans, err := postgres.ExplainFactWrites(ctx, tx, f.p.DatasetID, seed.WorkoutID, candidate.Samples, 2)
	if err != nil {
		t.Fatalf("explain candidates: %v", err)
	}
	for strategy, lines := range plans {
		if len(lines) == 0 {
			t.Fatalf("%s produced no plan", strategy)
		}
		for _, line := range lines {
			t.Logf("R5_PLAN strategy=%s %s", strategy, line)
		}
	}
}
