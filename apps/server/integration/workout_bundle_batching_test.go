//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rai/server/internal/syncing"
)

// These tests cover the P4 backend work: one semantic validation pass at the
// Store entry point, set-based identity/barrier checks across batch
// boundaries, and multi-row writes. Every failure case asserts the final
// database state, not only the returned error.

// bundleWithSamples builds a bundle whose samples are owned by one Workout, so
// a single import can cross more than one identity batch.
func bundleWithSamples(t *testing.T, f *fixture, sampleCount int) (syncing.WorkoutBundle, []byte) {
	t.Helper()
	bundle, raw := testWorkoutBundle(t, f, sampleCount)
	return bundle, raw
}

func importBundle(t *testing.T, f *fixture, bundle syncing.WorkoutBundle, raw []byte) ([]byte, error) {
	t.Helper()
	body, err := f.s.ImportWorkoutBundle(context.Background(), f.p, bundle, raw)
	return []byte(body), err
}

func TestWorkoutBundleSetBasedIdentityAcrossBatchBoundaries(t *testing.T) {
	f := setup(t)
	const sampleCount = 1205 // crosses the 1,000-row identity/write batch bound
	bundle, raw := bundleWithSamples(t, f, sampleCount)
	body, err := importBundle(t, f, bundle, raw)
	if err != nil {
		t.Fatal(err)
	}
	var receipt syncing.WorkoutImportReceipt
	if err = json.Unmarshal(body, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Counts.SamplesInserted != sampleCount || receipt.Counts.AssociationsInserted != sampleCount {
		t.Fatalf("multi-row write counts=%+v", receipt.Counts)
	}
	if count(t, f, "health_samples") != sampleCount || count(t, f, "workout_samples") != sampleCount {
		t.Fatal("set-based writes did not persist every sample")
	}
	if count(t, f, "workout_imports") != 1 || count(t, f, "change_events") != 1 || count(t, f, "jobs") != 1 {
		t.Fatal("one import must publish exactly one receipt, change event and job")
	}

	// Same bytes again: the original receipt, no new revision, no new work.
	replay, err := importBundle(t, f, bundle, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(replay, body) {
		t.Fatal("same-byte replay did not return the original receipt")
	}
	if count(t, f, "workout_imports") != 1 || count(t, f, "jobs") != 1 || count(t, f, "change_events") != 1 {
		t.Fatal("same-byte replay created new work")
	}

	// A conflict behind the first batch must still abort the whole bundle.
	t.Run("identity_conflict_in_second_batch", func(t *testing.T) {
		var changed syncing.WorkoutBundle
		if err := json.Unmarshal(raw, &changed); err != nil {
			t.Fatal(err)
		}
		changed.Samples[sampleCount-5].Quantity.Value++
		changedRaw, _ := json.Marshal(changed)
		before := phase1Snapshot(t, f)
		_, err := importBundle(t, f, changed, changedRaw)
		requireCode(t, err, "sample_identity_conflict")
		assertSnapshotUnchanged(t, f, before)
	})

	t.Run("association_conflict_in_second_batch", func(t *testing.T) {
		other := workoutAt(time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC), 60)
		conflict := syncing.WorkoutBundle{ProtocolVersion: 3, DatasetID: f.p.DatasetID,
			DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch,
			WorkoutID: other.UUID, Workout: other, Samples: []syncing.Event{bundle.Samples[sampleCount-3]}}
		conflictRaw, _ := json.Marshal(conflict)
		before := phase1Snapshot(t, f)
		_, err := importBundle(t, f, conflict, conflictRaw)
		requireCode(t, err, "sample_association_conflict")
		assertSnapshotUnchanged(t, f, before)
	})

	t.Run("tombstone_in_second_batch_blocks_old_retry", func(t *testing.T) {
		// The explicit child-deletion path is the existing internal HKDeletedObject
		// event; a deletionOnly bundle would need a parent barrier first.
		f.commit(t, []syncing.Event{deleted(bundle.Samples[sampleCount-2].UUID)}, 1, nil)
		if count(t, f, "entity_tombstones") != 1 {
			t.Fatal("explicit deletion did not create a barrier")
		}
		// The original bundle bytes are unchanged, so only the barrier check can
		// reject them; returning the old ACK here would resurrect a deleted child.
		_, err := importBundle(t, f, bundle, raw)
		requireCode(t, err, "sample_deleted")
		if count(t, f, "health_samples") != sampleCount-1 {
			t.Fatal("deleted child was resurrected by a byte-identical retry")
		}
	})
}

func assertSnapshotUnchanged(t *testing.T, f *fixture, before map[string][]byte) {
	t.Helper()
	for table, after := range phase1Snapshot(t, f) {
		if !bytes.Equal(before[table], after) {
			t.Fatalf("rejected bundle changed %s", table)
		}
	}
}

// TestWorkoutBundleStoreValidationIsSinglePass proves the semantic pass lives at
// the Store entry point: a direct caller cannot bypass it, and the error
// contract (status/code/field/reason) is the one the HTTP boundary used to
// produce. Nothing is persisted for a rejected bundle.
func TestWorkoutBundleStoreValidationIsSinglePass(t *testing.T) {
	f := setup(t)
	cases := []struct {
		name   string
		mutate func(*syncing.WorkoutBundle)
		field  string
		reason string
	}{
		{"illegal_unit", func(b *syncing.WorkoutBundle) { b.Samples[1].Quantity.Unit = "kg" }, "samples[1].quantity.unit", "unsupported_unit"},
		{"nonfinite_value", func(b *syncing.WorkoutBundle) { b.Samples[0].Quantity.Value = mathInf() }, "samples[0].quantity.value", "nonfinite"},
		{"unsupported_protocol", func(b *syncing.WorkoutBundle) { b.ProtocolVersion = 2 }, "protocolVersion", "unsupported_protocol"},
		{"invalid_time_range", func(b *syncing.WorkoutBundle) {
			end := *b.Samples[0].StartUs - 1
			b.Samples[0].EndUs = &end
		}, "samples[0].endUs", "invalid_time_range"},
		{"duplicate_sample", func(b *syncing.WorkoutBundle) { b.Samples = append(b.Samples, b.Samples[0]) }, "$", "invalid_value_or_membership"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bundle, _ := bundleWithSamples(t, f, 2)
			c.mutate(&bundle)
			mutatedRaw, _ := json.Marshal(bundle)
			_, err := importBundle(t, f, bundle, mutatedRaw)
			var failure *syncing.Error
			if !errors.As(err, &failure) {
				t.Fatalf("expected a typed failure, got %v", err)
			}
			if failure.Status != 422 || failure.Code != "unsupported_payload" {
				t.Fatalf("status=%d code=%s", failure.Status, failure.Code)
			}
			if failure.Field != "$"+strings.TrimPrefix(c.field, "$") && failure.Field != c.field {
				t.Fatalf("field=%q want %q", failure.Field, c.field)
			}
			if failure.Reason != c.reason {
				t.Fatalf("reason=%q want %q", failure.Reason, c.reason)
			}
			for _, table := range []string{"workouts", "health_samples", "workout_samples", "workout_imports", "change_events", "jobs", "entity_tombstones", "series_uploads", "workout_routes"} {
				if n := count(t, f, table); n != 0 {
					t.Fatalf("rejected bundle left %d rows in %s", n, table)
				}
			}
			var commitRev int64
			if err = f.s.Pool.QueryRow(context.Background(), `SELECT commit_rev FROM datasets WHERE id=$1`, f.p.DatasetID).Scan(&commitRev); err != nil {
				t.Fatal(err)
			}
			if commitRev != 0 {
				t.Fatalf("rejected bundle advanced commit_rev to %d", commitRev)
			}
		})
	}

}

// TestWorkoutBundleDeletionSetBasedSemantics keeps the explicit deletion path
// equivalent: duplicate keys collapse, parent membership is still enforced, and
// the barrier stays durable.
func TestWorkoutBundleDeletionSetBasedSemantics(t *testing.T) {
	f := setup(t)
	bundle, raw := bundleWithSamples(t, f, 4)
	if _, err := importBundle(t, f, bundle, raw); err != nil {
		t.Fatal(err)
	}
	deletion := bundle
	deletion.Samples, deletion.Series, deletion.Routes = nil, nil, nil
	deletion.SampleDeletions = []syncing.SampleDeletion{
		{SampleType: bundle.Samples[0].Type, SampleUUID: bundle.Samples[0].UUID},
		{SampleType: bundle.Samples[1].Type, SampleUUID: bundle.Samples[1].UUID},
	}
	deletionRaw, _ := json.Marshal(deletion)
	body, err := importBundle(t, f, deletion, deletionRaw)
	if err != nil {
		t.Fatal(err)
	}
	var receipt syncing.WorkoutImportReceipt
	if err = json.Unmarshal(body, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Counts.SamplesDeleted != 2 || receipt.Counts.AssociationsDeleted != 2 {
		t.Fatalf("deletion counts=%+v", receipt.Counts)
	}
	if count(t, f, "entity_tombstones") != 2 || count(t, f, "health_samples") != 2 {
		t.Fatal("set-based deletion did not create exactly two barriers")
	}

	// A known child of another Workout can never be deleted from this one.
	other := workoutAt(time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC), 60)
	mismatch := syncing.WorkoutBundle{ProtocolVersion: 3, DatasetID: f.p.DatasetID,
		DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch,
		WorkoutID: other.UUID, Workout: other,
		SampleDeletions: []syncing.SampleDeletion{{SampleType: bundle.Samples[2].Type, SampleUUID: bundle.Samples[2].UUID}}}
	mismatchRaw, _ := json.Marshal(mismatch)
	before := phase1Snapshot(t, f)
	_, err = importBundle(t, f, mismatch, mismatchRaw)
	requireCode(t, err, "child_workout_mismatch")
	assertSnapshotUnchanged(t, f, before)
	if count(t, f, "entity_tombstones") != 2 {
		t.Fatal("rejected deletion created a barrier")
	}
}

// TestWorkoutBundleSeriesAndRouteIdentityBatching keeps series/route identity
// and chunk-hash comparison exact while reading them by key.
func TestWorkoutBundleSeriesAndRouteIdentityBatching(t *testing.T) {
	f := setup(t)
	start := time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)
	bundle, _ := testWorkoutBundle(t, f, 4)
	// A second series over the same parent plus a route long enough for two chunks.
	parentEnd := *bundle.Samples[1].StartUs + 5_002_000_000
	bundle.Samples[1].EndUs = &parentEnd
	bundle.Series = append(bundle.Series, syncing.WorkoutBundleSeries{
		SampleType: kind, SampleUUID: bundle.Samples[1].UUID,
		Points: bundleSeriesPoints(*bundle.Samples[1].StartUs, 5001),
	})
	bundle.Routes[0].Points = bundleRoutePoints(start, 5001)
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	body, err := importBundle(t, f, bundle, raw)
	if err != nil {
		t.Fatal(err)
	}
	var receipt syncing.WorkoutImportReceipt
	if err = json.Unmarshal(body, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Counts.SeriesInserted != 2 || receipt.Counts.RoutesInserted != 1 {
		t.Fatalf("series/route counts=%+v", receipt.Counts)
	}
	if count(t, f, "series_chunks") != 3 || count(t, f, "route_chunks") != 2 {
		t.Fatal("chunked writes did not persist every chunk")
	}
	replay, err := importBundle(t, f, bundle, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(replay, body) {
		t.Fatal("series/route replay did not return the original receipt")
	}
	if count(t, f, "series_uploads") != 2 || count(t, f, "workout_routes") != 1 {
		t.Fatal("series/route replay wrote again")
	}
	// A changed point in the second chunk must still conflict.
	conflict := bundle
	conflict.Series = []syncing.WorkoutBundleSeries{bundle.Series[1]}
	conflict.Series[0].Points[5000].Value++
	conflictRaw, _ := json.Marshal(conflict)
	before := phase1Snapshot(t, f)
	_, err = importBundle(t, f, conflict, conflictRaw)
	requireCode(t, err, "series_identity_conflict")
	assertSnapshotUnchanged(t, f, before)
}

func bundleSeriesPoints(startUs int64, count int) []syncing.SeriesPoint {
	points := make([]syncing.SeriesPoint, count)
	for i := range points {
		at := startUs + int64(i)*1000000
		points[i] = syncing.SeriesPoint{StartUs: at, EndUs: at, Value: float64(i), Unit: "count/min"}
	}
	return points
}

func bundleRoutePoints(start time.Time, count int) []syncing.WorkoutBundlePoint {
	points := make([]syncing.WorkoutBundlePoint, count)
	for i := range points {
		points[i] = syncing.WorkoutBundlePoint{RecordedAtUs: start.UnixMicro() + int64(i)*1000000,
			Latitude: 31.2304 + float64(i)*1e-6, Longitude: 121.4737}
	}
	return points
}

// multiRowSampleInsertPlan builds the multi-row health_samples INSERT used by
// insertSampleRows, as an EXPLAIN-able statement with 12 parameters per row.
func multiRowSampleInsertPlan(datasetID string, bundle syncing.WorkoutBundle, rows int, uuids []string) (string, []any) {
	if rows > len(bundle.Samples) {
		rows = len(bundle.Samples)
	}
	placeholders := make([]string, 0, rows)
	args := make([]any, 0, rows*12)
	for index := 0; index < rows; index++ {
		sample := bundle.Samples[index]
		base := index * 12
		placeholders = append(placeholders, fmt.Sprintf(
			"($%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d,$%d::jsonb)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8, base+9, base+10, base+11, base+12))
		args = append(args, datasetID, sample.Type, uuids[index],
			time.UnixMicro(*sample.StartUs).UTC(), time.UnixMicro(*sample.EndUs).UTC(),
			sample.Quantity.Value, nil, sample.Quantity.Unit, sample.Source.BundleID,
			[]byte("0123456789abcdef0123456789abcdef"), int64(1), "{}")
	}
	return `INSERT INTO health_samples(dataset_id,sample_type,sample_uuid,event_start,event_end,value_numeric,
			value_category,unit,source_bundle_id,content_sha256,entity_revision,metadata)
		VALUES ` + strings.Join(placeholders, ","), args
}

// multiRowAssociationInsertPlan builds the multi-row workout_samples INSERT used
// by insertAssociationRows, as an EXPLAIN-able statement with 7 parameters per row.
func multiRowAssociationInsertPlan(datasetID string, bundle syncing.WorkoutBundle, rows int, uuids []string) (string, []any) {
	if rows > len(bundle.Samples) {
		rows = len(bundle.Samples)
	}
	placeholders := make([]string, 0, rows)
	args := make([]any, 0, rows*7)
	for index := 0; index < rows; index++ {
		sample := bundle.Samples[index]
		base := index * 7
		placeholders = append(placeholders, fmt.Sprintf("($%d,$%d,$%d,$%d,$%d,$%d,$%d)",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7))
		args = append(args, datasetID, sample.Type, uuids[index], bundle.WorkoutID,
			syncing.NewID(), sample.Source.BundleID, int64(1))
	}
	return `INSERT INTO workout_samples(dataset_id,sample_type,sample_uuid,workout_uuid,association_id,source_bundle_id,commit_rev)
		VALUES ` + strings.Join(placeholders, ","), args
}

// TestWorkoutBundleSetBasedSQLPlans records EXPLAIN (ANALYZE, BUFFERS, WAL) for
// the set-based statements that replaced the per-sample queries. Output goes to
// docs/evidence/workout-sync-speed/sql-plans/<runID>-<query>.txt with synthetic
// identifiers only.
func TestWorkoutBundleSetBasedSQLPlans(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	const sampleCount = 5000
	bundle, raw := bundleWithSamples(t, f, sampleCount)
	if _, err := importBundle(t, f, bundle, raw); err != nil {
		t.Fatal(err)
	}
	var err error
	types := make([]string, sampleCount)
	uuids := make([]string, sampleCount)
	for index, sample := range bundle.Samples {
		types[index], uuids[index] = sample.Type, sample.UUID
	}
	workoutStart, workoutEnd := time.UnixMicro(*bundle.Workout.StartUs).UTC(), time.UnixMicro(*bundle.Workout.EndUs).UTC()
	// The two write statements below mirror insertSampleRows and
	// insertAssociationRows parameter order exactly (12 and 7 parameters per
	// row) so the recorded plan and parameter count describe the shipped path.
	freshUUIDs := make([]string, 1000)
	for index := range freshUUIDs {
		freshUUIDs[index] = syncing.NewID()
	}
	sampleInsertSQL, sampleInsertArgs := multiRowSampleInsertPlan(f.p.DatasetID, bundle, 1000, freshUUIDs)
	associationInsertSQL, associationInsertArgs := multiRowAssociationInsertPlan(f.p.DatasetID, bundle, 1000, freshUUIDs)
	plans := []struct {
		name      string
		sql       string
		args      []any
		setupSQL  string
		setupArgs []any
	}{
		{"sample-identity", `EXPLAIN (ANALYZE, BUFFERS, WAL)
			SELECT k.sample_type,k.sample_uuid::text,(h.sample_uuid IS NOT NULL),
				EXISTS(SELECT 1 FROM entity_tombstones t
					WHERE t.dataset_id=$1 AND t.sample_type=k.sample_type AND t.sample_uuid=k.sample_uuid::uuid),
				COALESCE(w.workout_uuid::text,''),
				h.content_sha256,h.event_start,h.event_end,h.unit,h.value_numeric,h.value_category,h.source_bundle_id
			FROM unnest($2::text[],$3::text[]) AS k(sample_type,sample_uuid)
			LEFT JOIN health_samples h ON h.dataset_id=$1 AND h.sample_type=k.sample_type AND h.sample_uuid=k.sample_uuid::uuid
			LEFT JOIN workout_samples w ON w.dataset_id=$1 AND w.sample_type=k.sample_type AND w.sample_uuid=k.sample_uuid::uuid`,
			[]any{f.p.DatasetID, types[:1000], uuids[:1000]}, "", nil},
		{"child-delete-state", `EXPLAIN (ANALYZE, BUFFERS, WAL)
			SELECT k.sample_type,k.sample_uuid::text,COALESCE(w.workout_uuid::text,''),
				EXISTS(SELECT 1 FROM entity_tombstones t
					WHERE t.dataset_id=$1 AND t.sample_type=k.sample_type AND t.sample_uuid=k.sample_uuid::uuid)
			FROM unnest($2::text[],$3::text[]) AS k(sample_type,sample_uuid)
			LEFT JOIN workout_samples w ON w.dataset_id=$1 AND w.sample_type=k.sample_type AND w.sample_uuid=k.sample_uuid::uuid`,
			[]any{f.p.DatasetID, types[:1000], uuids[:1000]}, "", nil},
		{"series-identity", `EXPLAIN (ANALYZE, BUFFERS, WAL)
			SELECT k.sample_type,k.sample_uuid::text,s.series_id::text,s.state,s.total_points,COALESCE(s.chunk_count,0)
			FROM unnest($2::text[],$3::text[]) AS k(sample_type,sample_uuid)
			LEFT JOIN series_uploads s ON s.dataset_id=$1 AND s.sample_type=k.sample_type AND s.sample_uuid=k.sample_uuid::uuid`,
			[]any{f.p.DatasetID, types[:2], uuids[:2]}, "", nil},
		{"route-identity", `EXPLAIN (ANALYZE, BUFFERS, WAL)
			SELECT k.route_uuid::text,r.workout_uuid::text,r.source_bundle_id,r.state,COALESCE(r.total_points,0)
			FROM unnest($2::text[]) AS k(route_uuid)
			LEFT JOIN workout_routes r ON r.dataset_id=$1 AND r.route_uuid=k.route_uuid::uuid`,
			[]any{f.p.DatasetID, []string{bundle.Routes[0].RouteID}}, "", nil},
		{"sample-writes", "EXPLAIN (ANALYZE, BUFFERS, WAL)\n" + sampleInsertSQL, sampleInsertArgs, "", nil},
		// The association plan needs its parent sample rows in the same rolled-back
		// transaction, otherwise the foreign key would reject the measurement.
		{"association-writes", "EXPLAIN (ANALYZE, BUFFERS, WAL)\n" + associationInsertSQL, associationInsertArgs, sampleInsertSQL, sampleInsertArgs},
		{"dirty-scope-upsert", `EXPLAIN (ANALYZE, BUFFERS, WAL)
			INSERT INTO dirty_scopes(dataset_id,scope_key,generation,dirty_from,dirty_through)
			VALUES($1,$2,1,$3,$4)
			ON CONFLICT(dataset_id,scope_key) DO UPDATE SET generation=dirty_scopes.generation+1,
				dirty_from=LEAST(dirty_scopes.dirty_from,EXCLUDED.dirty_from),
				dirty_through=GREATEST(dirty_scopes.dirty_through,EXCLUDED.dirty_through)
			RETURNING generation`,
			[]any{f.p.DatasetID, "workout:" + syncing.WorkoutType + ":" + bundle.WorkoutID, workoutStart, workoutEnd}, "", nil},
	}
	dir := filepath.Join("..", "..", "..", "docs", "evidence", "workout-sync-speed", "sql-plans")
	if err = os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runID := fmt.Sprintf("%s-%s", time.Now().UTC().Format("20060102T150405Z"), syncing.NewID()[:8])
	t.Logf("sql plan run id: %s", runID)
	for _, plan := range plans {
		// Write statements run inside a transaction that is rolled back, so the
		// measured plan is real without changing committed state.
		tx, err := f.s.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if plan.setupSQL != "" {
			if _, err := tx.Exec(ctx, plan.setupSQL, plan.setupArgs...); err != nil {
				_ = tx.Rollback(ctx)
				t.Fatal(err)
			}
		}
		rows, err := tx.Query(ctx, plan.sql, plan.args...)
		if err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		var out strings.Builder
		out.WriteString("-- statement: " + plan.name + "\n")
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				rows.Close()
				_ = tx.Rollback(ctx)
				t.Fatal(err)
			}
			out.WriteString(line)
			out.WriteByte('\n')
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err = tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, runID+"-"+plan.name+".txt")
		if err = os.WriteFile(path, []byte(out.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "Planning") && !strings.Contains(out.String(), "Execution") {
			t.Fatalf("%s produced no plan output", plan.name)
		}
	}
}

func mathInf() float64 {
	var zero float64
	return 1 / zero
}

// TestWorkoutBundleSetBasedPhaseTimings records one synthetic fresh import's
// phase durations so the ack path can be attributed without a perf harness. It
// is a single sample, not a percentile: it proves the phases exist and shows
// where the time goes, and it never claims an SLO.
func TestWorkoutBundleSetBasedPhaseTimings(t *testing.T) {
	f := setup(t)
	const sampleCount = 5000
	bundle, raw := bundleWithSamples(t, f, sampleCount)
	phases := make(map[string]time.Duration)
	ordered := make([]string, 0, 16)
	ctx := syncing.ObservePhases(context.Background(), func(name string, d time.Duration) {
		if _, seen := phases[name]; !seen {
			ordered = append(ordered, name)
		}
		phases[name] += d
	})
	start := time.Now()
	if _, err := f.s.ImportWorkoutBundle(ctx, f.p, bundle, raw); err != nil {
		t.Fatal(err)
	}
	total := time.Since(start)
	if count(t, f, "health_samples") != sampleCount {
		t.Fatal("import did not write every sample")
	}
	dir := filepath.Join("..", "..", "..", "docs", "evidence", "workout-sync-speed", "phase-timings")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	runID := fmt.Sprintf("%s-%s", time.Now().UTC().Format("20060102T150405Z"), syncing.NewID()[:8])
	var out strings.Builder
	fmt.Fprintf(&out, "# workout bundle import phase timings (synthetic, single run)\n")
	fmt.Fprintf(&out, "samples=%d routes=%d series=%d total_ms=%.3f\n", sampleCount, len(bundle.Routes), len(bundle.Series), float64(total.Microseconds())/1000)
	for _, name := range ordered {
		fmt.Fprintf(&out, "phase=%s duration_ms=%.3f\n", name, float64(phases[name].Microseconds())/1000)
	}
	path := filepath.Join(dir, runID+"-import-phases.txt")
	if err := os.WriteFile(path, []byte(out.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("phase timings written to %s", path)
	t.Log("\n" + out.String())
}

// TestWorkoutBundleLargeBatchImportSurvivesConnectionStatementTimeout guards the
// regression that batching introduced: the pool default statement_timeout is 2s,
// and one batch statement now carries a thousand facts, so a legitimate bundle of
// tens of thousands of samples would fail with 57014 instead of committing. The
// import transaction must raise the per-statement bound, and a byte-identical
// replay must still return the original receipt.
func TestWorkoutBundleLargeBatchImportSurvivesConnectionStatementTimeout(t *testing.T) {
	f := setup(t)
	const sampleCount = 20000
	bundle, raw := bundleWithSamples(t, f, sampleCount)
	body, err := importBundle(t, f, bundle, raw)
	if err != nil {
		t.Fatal(err)
	}
	var receipt syncing.WorkoutImportReceipt
	if err := json.Unmarshal(body, &receipt); err != nil {
		t.Fatalf("receipt: %v body=%s", err, body)
	}
	if receipt.Counts.SamplesInserted != sampleCount || receipt.Counts.AssociationsInserted != sampleCount {
		t.Fatalf("sample loss: %+v", receipt.Counts)
	}
	if count(t, f, "health_samples") != sampleCount || count(t, f, "workout_samples") != sampleCount {
		t.Fatal("facts missing after large import")
	}
	// The same bytes must replay to the identical receipt without new work.
	replay, err := importBundle(t, f, bundle, raw)
	if err != nil || string(replay) != string(body) {
		t.Fatalf("replay identical=%t body=%s err=%v", string(replay) == string(body), replay, err)
	}
	if count(t, f, "health_samples") != sampleCount || count(t, f, "jobs") != 1 || count(t, f, "workout_imports") != 1 {
		t.Fatal("replay created work")
	}
}
