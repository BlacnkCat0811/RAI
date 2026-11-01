//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/httpapi"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
)

// Synthetic facts only; extend the shared rollback snapshot to cover bundle
// projections, durable receipts and P4 immutable history/current pointers.
func phase1Snapshot(t *testing.T, f *fixture) map[string][]byte {
	t.Helper()
	out := businessSnapshot(t, f)
	for _, table := range []string{"workout_samples", "workout_routes", "route_chunks", "route_points", "series_uploads", "series_chunks", "workout_imports", "analysis_runs", "analysis_results", "analysis_publications"} {
		var raw []byte
		if err := f.s.Pool.QueryRow(context.Background(), `SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text),'[]') FROM `+pgx.Identifier{table}.Sanitize()+` x`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		out[table] = raw
	}
	return out
}

func bundleDeleteRequest(f *fixture, id string) syncing.WorkoutDeleteRequest {
	return syncing.WorkoutDeleteRequest{ProtocolVersion: 3, DatasetID: f.p.DatasetID,
		DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch, WorkoutID: id}
}

func TestWorkoutBundleConcurrentSnapshotsRetainUnion(t *testing.T) {
	f := setup(t)
	full, fullRaw := testWorkoutBundle(t, f, 2)
	old := full
	old.Samples = old.Samples[:1]
	old.Routes = nil
	old.CompleteSampleTypes = []string{kind}
	old.CompleteRoutes = true
	oldRaw, _ := json.Marshal(old)
	start := make(chan struct{})
	done := make(chan error, 2)
	for _, input := range []struct {
		bundle syncing.WorkoutBundle
		raw    []byte
	}{{full, fullRaw}, {old, oldRaw}} {
		go func() {
			<-start
			_, err := f.s.ImportWorkoutBundle(context.Background(), f.p, input.bundle, input.raw)
			done <- err
		}()
	}
	close(start)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if count(t, f, "health_samples") != 2 || count(t, f, "workout_routes") != 1 || count(t, f, "entity_tombstones") != 0 {
		t.Fatal("concurrent snapshot lost facts")
	}
	if _, err := f.s.ImportWorkoutBundle(context.Background(), f.p, full, fullRaw); err != nil {
		t.Fatal("complete bundle cannot replay", err)
	}
}

func TestWorkoutBundleExplicitChildDeletionBoundary(t *testing.T) {
	for _, child := range []string{"sample", "route"} {
		t.Run(child, func(t *testing.T) {
			f := setup(t)
			bundle, raw := testWorkoutBundle(t, f, 2)
			if _, err := f.s.ImportWorkoutBundle(context.Background(), f.p, bundle, raw); err != nil {
				t.Fatal(err)
			}
			code := "sample_deleted"
			if child == "sample" {
				// Existing internal explicit HKDeletedObject event path, not a
				// newly exposed per-sample HTTP protocol.
				f.commit(t, []syncing.Event{deleted(bundle.Samples[1].UUID)}, 1, nil)
			} else {
				code = "route_deleted"
				if _, err := f.s.DeleteRoute(context.Background(), f.p, bundle.Routes[0].RouteID,
					syncing.RouteDeleteRequest{ProtocolVersion: syncing.ProtocolVersion, DatasetEpoch: f.enrollment.DatasetEpoch,
						CollectorEpoch: f.enrollment.CollectorEpoch, Reason: "healthkit_deleted_object"}); err != nil {
					t.Fatal(err)
				}
			}
			_, err := f.s.ImportWorkoutBundle(context.Background(), f.p, bundle, raw)
			requireCode(t, err, code)
			_, err = f.s.WorkoutImport(context.Background(), f.p, bundle.WorkoutID, syncing.BundleDigest(raw))
			requireCode(t, err, code)
		})
	}
}

func TestWorkoutDeleteBeforeUploadDurableStableAndIsolated(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	bundle, raw := testWorkoutBundle(t, f, 1)
	req := bundleDeleteRequest(f, bundle.WorkoutID)
	first, err := f.s.DeleteWorkoutBundle(ctx, f.p, req)
	if err != nil {
		t.Fatal(err)
	}
	var receipt syncing.WorkoutDeleteReceipt
	if err = json.Unmarshal(first, &receipt); err != nil || receipt.CommitRevision != 1 || receipt.IngestStatus != "deleted" {
		t.Fatalf("invalid durable ACK: %+v, %v", receipt, err)
	}
	var start, end *time.Time
	var epoch, collector string
	var deletedAt time.Time
	if err = f.s.Pool.QueryRow(ctx, `SELECT t.previous_start,t.previous_end,c.created_at,
		c.payload->>'datasetEpoch',c.payload->>'collectorEpoch'
		FROM entity_tombstones t JOIN change_events c ON c.dataset_id=t.dataset_id AND c.commit_rev=t.deleted_rev
		WHERE t.dataset_id=$1 AND t.sample_type=$2 AND t.sample_uuid=$3`,
		f.p.DatasetID, syncing.WorkoutType, bundle.WorkoutID).Scan(&start, &end, &deletedAt, &epoch, &collector); err != nil {
		t.Fatal(err)
	}
	if start != nil || end != nil || deletedAt.IsZero() || epoch != req.DatasetEpoch || collector != req.CollectorEpoch || count(t, f, "dirty_scopes") != 0 || count(t, f, "jobs") != 0 {
		t.Fatal("unknown deletion fabricated range/work or lost fencing lineage")
	}
	// A separate Store instance relies entirely on the committed database barrier.
	otherStore, err := postgres.Open(ctx, f.s.Pool.Config().ConnString(), 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(otherStore.Pool.Close)
	_, err = otherStore.ImportWorkoutBundle(ctx, f.p, bundle, raw)
	requireCode(t, err, "workout_deleted")
	bundle.Workout.Metadata = map[string]any{"synthetic": "different_bundle"}
	changedRaw, _ := json.Marshal(bundle)
	_, err = otherStore.ImportWorkoutBundle(ctx, f.p, bundle, changedRaw)
	requireCode(t, err, "workout_deleted")
	// Advance the dataset independently; deletion replay must keep its old ACK.
	if _, err = f.s.DeleteWorkoutBundle(ctx, f.p, bundleDeleteRequest(f, syncing.NewID())); err != nil {
		t.Fatal(err)
	}
	before := phase1Snapshot(t, f)
	req.Reason = "retry_with_different_reason"
	retry, err := otherStore.DeleteWorkoutBundle(ctx, f.p, req)
	if err != nil || !bytes.Equal(first, retry) {
		t.Fatal("deletion receipt changed", err)
	}
	for table, after := range phase1Snapshot(t, f) {
		if !bytes.Equal(before[table], after) {
			t.Fatal("duplicate delete changed", table)
		}
	}
	// A second dataset in the same database can import exactly the same UUID.
	p := syncing.Principal{DatasetID: syncing.NewID(), DeviceID: syncing.NewID(), CanCollect: true}
	epoch, collector = syncing.NewID(), syncing.NewID()
	tx, err := f.s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `INSERT INTO datasets(id,epoch,service_id) VALUES($1,$2,$3)`, p.DatasetID, epoch, syncing.NewID()); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO devices(dataset_id,id,token_sha256,can_collect) VALUES($1,$2,decode(repeat('ab',32),'hex'),true)`, p.DatasetID, p.DeviceID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO collector_leases(dataset_id,device_id,collector_epoch) VALUES($1,$2,$3)`, p.DatasetID, p.DeviceID, collector); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	bundle.DatasetID, bundle.DatasetEpoch, bundle.CollectorEpoch = p.DatasetID, epoch, collector
	raw, _ = json.Marshal(bundle)
	if _, err = f.s.ImportWorkoutBundle(ctx, p, bundle, raw); err != nil {
		t.Fatal("barrier crossed dataset boundary", err)
	}
}

func TestWorkoutDeleteFencingBeforeBarrierReplay(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	bundle, raw := testWorkoutBundle(t, f, 1)
	req := bundleDeleteRequest(f, bundle.WorkoutID)
	if _, err := f.s.DeleteWorkoutBundle(ctx, f.p, req); err != nil {
		t.Fatal(err)
	}
	for _, exists := range []bool{false, true} {
		for _, code := range []string{"dataset_epoch_changed", "collector_fenced"} {
			bad := req
			if !exists {
				bad.WorkoutID = syncing.NewID()
			}
			if code == "dataset_epoch_changed" {
				bad.DatasetEpoch = syncing.NewID()
			} else {
				bad.CollectorEpoch = syncing.NewID()
			}
			before := phase1Snapshot(t, f)
			_, err := f.s.DeleteWorkoutBundle(ctx, f.p, bad)
			requireCode(t, err, code)
			stale := bundle
			stale.DatasetEpoch, stale.CollectorEpoch = bad.DatasetEpoch, bad.CollectorEpoch
			staleRaw, _ := json.Marshal(stale)
			_, err = f.s.ImportWorkoutBundle(ctx, f.p, stale, staleRaw)
			requireCode(t, err, code)
			for table, after := range phase1Snapshot(t, f) {
				if !bytes.Equal(before[table], after) {
					t.Fatal("fenced request changed", table)
				}
			}
		}
	}
	_, err := f.s.ImportWorkoutBundle(ctx, f.p, bundle, raw)
	requireCode(t, err, "workout_deleted")
}

// Observe real database lock waiters, rather than assuming goroutine start
// order or using sleeps as evidence of which command acquired the dataset lock.
func waitDatasetLockWaiters(t *testing.T, f *fixture, want int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		var n int
		if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname=current_database() AND query LIKE 'SELECT epoch::text, commit_rev,%FROM datasets%FOR UPDATE'
			AND cardinality(pg_blocking_pids(pid))>0`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == want {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("dataset lock waiters=%d want=%d", n, want)
		case <-ticker.C:
		}
	}
}

func TestWorkoutDeleteAndImportBothLockOrders(t *testing.T) {
	for _, deleteFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("delete_first_%t", deleteFirst), func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			bundle, raw := testWorkoutBundle(t, f, 1)
			gate, err := f.s.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer gate.Rollback(ctx)
			if _, err = gate.Exec(ctx, `SELECT id FROM datasets WHERE id=$1 FOR UPDATE`, f.p.DatasetID); err != nil {
				t.Fatal(err)
			}
			deletes, imports := make(chan error, 1), make(chan error, 1)
			startDelete := func() {
				go func() {
					_, err := f.s.DeleteWorkoutBundle(ctx, f.p, bundleDeleteRequest(f, bundle.WorkoutID))
					deletes <- err
				}()
			}
			startImport := func() { go func() { _, err := f.s.ImportWorkoutBundle(ctx, f.p, bundle, raw); imports <- err }() }
			if deleteFirst {
				startDelete()
			} else {
				startImport()
			}
			waitDatasetLockWaiters(t, f, 1)
			if deleteFirst {
				startImport()
			} else {
				startDelete()
			}
			waitDatasetLockWaiters(t, f, 2)
			if err = gate.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-deletes; err != nil {
				t.Fatal(err)
			}
			importErr := <-imports
			if deleteFirst {
				requireCode(t, importErr, "workout_deleted")
			} else if importErr != nil {
				t.Fatal(importErr)
			}
			if count(t, f, "workouts") != 0 || count(t, f, "workout_routes") != 0 {
				t.Fatal("concurrent delete left workout facts")
			}
			_, err = f.s.ImportWorkoutBundle(ctx, f.p, bundle, raw)
			requireCode(t, err, "workout_deleted")
		})
	}
}

func TestWorkoutDeleteInvalidatesPublishedP4AndKeepsHistory(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	bundle, raw := testWorkoutBundle(t, f, 1)
	phase3Observation(&bundle)
	raw, _ = json.Marshal(bundle)
	if _, err := f.s.ImportWorkoutBundle(ctx, f.p, bundle, raw); err != nil {
		t.Fatal(err)
	}
	runOne(t, f, "analysis.workout")
	runOne(t, f, "analysis.training_state")
	if _, err := f.s.CurrentTrainingState(ctx, f.p); err != nil {
		t.Fatal(err)
	}
	old := phase1Snapshot(t, f)
	if _, err := f.s.DeleteWorkoutBundle(ctx, f.p, bundleDeleteRequest(f, bundle.WorkoutID)); err != nil {
		t.Fatal(err)
	}
	_, err := f.s.CurrentWorkoutAnalysis(ctx, f.p, bundle.WorkoutID)
	requireCode(t, err, "analysis_not_ready")
	_, err = f.s.CurrentTrainingState(ctx, f.p)
	requireCode(t, err, "analysis_not_ready")
	var start, end time.Time
	if err = f.s.Pool.QueryRow(ctx, `SELECT previous_start,previous_end FROM entity_tombstones WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3`,
		f.p.DatasetID, syncing.WorkoutType, bundle.WorkoutID).Scan(&start, &end); err != nil || start.UnixMicro() != *bundle.Workout.StartUs || end.UnixMicro() != *bundle.Workout.EndUs {
		t.Fatal("lost old time range", err)
	}
	afterDelete := phase1Snapshot(t, f)
	for _, table := range []string{"analysis_results", "analysis_publications"} {
		if !bytes.Equal(old[table], afterDelete[table]) {
			t.Fatal("delete rewrote P4 history/current pointer prematurely", table)
		}
	}
	runOne(t, f, "analysis.workout")
	var pointers int
	if err = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM analysis_publications WHERE dataset_id=$1 AND metric_family='workout'`, f.p.DatasetID).Scan(&pointers); err != nil || pointers != 0 {
		t.Fatal("deleted workout publication not retired", pointers, err)
	}
	// Older queued state generations may be superseded before the current one.
	for range 8 {
		if _, err = f.s.CurrentTrainingState(ctx, f.p); err == nil {
			break
		}
		runOne(t, f, "analysis.training_state")
	}
	current, err := f.s.CurrentTrainingState(ctx, f.p)
	if err != nil {
		t.Fatal("state was not republished", err)
	}
	var staleLoads int
	if err = f.s.Pool.QueryRow(ctx, `SELECT jsonb_array_length(COALESCE(NULLIF(lineage->'upstreamRuns','null'::jsonb),'[]'::jsonb)) FROM analysis_runs WHERE dataset_id=$1 AND id=$2`, f.p.DatasetID, current.RunID).Scan(&staleLoads); err != nil || staleLoads != 0 {
		t.Fatal("new state still uses deleted workout", staleLoads, err)
	}
	// The exact old run/result rows remain; analysis_publications is the existing
	// mutable current pointer, not an immutable publication-history table.
	var keptRuns, keptResults bool
	if err = f.s.Pool.QueryRow(ctx, `SELECT
		NOT EXISTS(SELECT 1 FROM jsonb_array_elements($1::jsonb) old WHERE NOT EXISTS(SELECT 1 FROM analysis_runs r WHERE to_jsonb(r)=old)),
		NOT EXISTS(SELECT 1 FROM jsonb_array_elements($2::jsonb) old WHERE NOT EXISTS(SELECT 1 FROM analysis_results r WHERE to_jsonb(r)=old))`,
		string(old["analysis_runs"]), string(old["analysis_results"])).Scan(&keptRuns, &keptResults); err != nil || !keptRuns || !keptResults {
		t.Fatal("immutable analysis history changed", err)
	}
}

func TestWorkoutDeleteSupersedesInFlightWorkers(t *testing.T) {
	for _, kind := range []string{"analysis.workout", "analysis.training_state"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			bundle, raw := testWorkoutBundle(t, f, 1)
			phase3Observation(&bundle)
			raw, _ = json.Marshal(bundle)
			if _, err := f.s.ImportWorkoutBundle(ctx, f.p, bundle, raw); err != nil {
				t.Fatal(err)
			}
			if kind == "analysis.training_state" {
				runOne(t, f, "analysis.workout")
			}
			l := claimAnalysis(t, f, kind)
			in, runID, metrics := computeAnalysis(t, f, l)
			if _, err := f.s.DeleteWorkoutBundle(ctx, f.p, bundleDeleteRequest(f, bundle.WorkoutID)); err != nil {
				t.Fatal(err)
			}
			status, err := f.s.PublishAnalysis(ctx, l, runID, in, metrics)
			if err != nil || status != "SUPERSEDED" {
				t.Fatal("pre-delete worker published stale result", status, err)
			}
			_, err = f.s.CurrentWorkoutAnalysis(ctx, f.p, bundle.WorkoutID)
			requireCode(t, err, "analysis_not_ready")
			_, err = f.s.CurrentTrainingState(ctx, f.p)
			requireCode(t, err, "analysis_not_ready")
		})
	}
}

func TestWorkoutDeleteRollbackIncludingCommitFailure(t *testing.T) {
	for _, point := range []string{"barrier", "dirty", "job", "change", "commit", "unknown_commit"} {
		t.Run(point, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			bundle, raw := testWorkoutBundle(t, f, 1)
			phase3Observation(&bundle)
			raw, _ = json.Marshal(bundle)
			if point != "unknown_commit" {
				if _, err := f.s.ImportWorkoutBundle(ctx, f.p, bundle, raw); err != nil {
					t.Fatal(err)
				}
				runOne(t, f, "analysis.workout")
				runOne(t, f, "analysis.training_state")
			}
			before := phase1Snapshot(t, f)
			if _, err := f.s.Pool.Exec(ctx, `CREATE FUNCTION phase1_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic_failure'; END $$`); err != nil {
				t.Fatal(err)
			}
			target := map[string]string{"barrier": "entity_tombstones", "dirty": "dirty_scopes", "job": "jobs", "change": "change_events", "commit": "change_events", "unknown_commit": "change_events"}[point]
			stmt := "CREATE TRIGGER phase1_fail BEFORE INSERT OR UPDATE ON " + target + " FOR EACH ROW EXECUTE FUNCTION phase1_fail()"
			if point == "commit" || point == "unknown_commit" {
				stmt = "CREATE CONSTRAINT TRIGGER phase1_fail AFTER INSERT ON change_events DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION phase1_fail()"
			}
			if _, err := f.s.Pool.Exec(ctx, stmt); err != nil {
				t.Fatal(err)
			}
			body, err := f.s.DeleteWorkoutBundle(ctx, f.p, bundleDeleteRequest(f, bundle.WorkoutID))
			if err == nil || len(body) != 0 {
				t.Fatal("failed deletion returned an ACK", err)
			}
			for table, after := range phase1Snapshot(t, f) {
				if !bytes.Equal(before[table], after) {
					t.Fatal("failed deletion changed", table)
				}
			}
		})
	}
}

func TestHTTPWorkoutDeleteBeforeUploadAndRetry(t *testing.T) {
	f := setup(t)
	bundle, raw := testWorkoutBundle(t, f, 1)
	handler := httpapi.New(f.s, analysisLog)
	body, _ := json.Marshal(bundleDeleteRequest(f, bundle.WorkoutID))
	request := httptest.NewRequest(http.MethodDelete, "/v2/sync/workout-bundles/"+bundle.WorkoutID, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("unknown delete status=%d body=%s", response.Code, response.Body.Bytes())
	}
	// Replay through storage to preserve the real two-request upload-lane burst
	// for DELETE + POST, without resetting operational rate-limit state.
	retry, err := f.s.DeleteWorkoutBundle(context.Background(), f.p, bundleDeleteRequest(f, bundle.WorkoutID))
	if err != nil || !bytes.Equal(retry, bytes.TrimSpace(response.Body.Bytes())) {
		t.Fatal("HTTP deletion ACK changed", err)
	}
	status, payload := uploadBundleHTTP(t, handler, f.enrollment.Token, raw)
	var failure struct{ Code string }
	if err := json.Unmarshal(payload, &failure); err != nil || status != http.StatusConflict || failure.Code != "workout_deleted" {
		t.Fatalf("late HTTP upload status=%d body=%s err=%v", status, payload, err)
	}
	if count(t, f, "entity_tombstones") != 1 || count(t, f, "change_events") != 1 || count(t, f, "workouts") != 0 {
		t.Fatal("HTTP delete did not create exactly one durable barrier")
	}
}

func TestWorkoutBundleSnapshotStillRejectsIdentityConflicts(t *testing.T) {
	f := setup(t)
	bundle, raw := testWorkoutBundle(t, f, 1)
	if _, err := f.s.ImportWorkoutBundle(context.Background(), f.p, bundle, raw); err != nil {
		t.Fatal(err)
	}
	for _, child := range []string{"workout", "sample", "series", "route"} {
		t.Run(child, func(t *testing.T) {
			var changed syncing.WorkoutBundle
			if err := json.Unmarshal(raw, &changed); err != nil {
				t.Fatal(err)
			}
			switch child {
			case "workout":
				changed.Workout.Workout.TotalDistance = &syncing.Quantity{Value: 1000, Unit: "m"}
			case "sample":
				changed.Samples[0].Quantity.Value++
			case "series":
				changed.Series[0].Points[0].Value++
			case "route":
				changed.Routes[0].Points[0].Latitude += 0.000001
			}
			changed.CompleteSampleTypes, changed.CompleteRoutes = []string{kind}, true
			changedRaw, _ := json.Marshal(changed)
			before := phase1Snapshot(t, f)
			_, err := f.s.ImportWorkoutBundle(context.Background(), f.p, changed, changedRaw)
			requireCode(t, err, child+"_identity_conflict")
			for table, after := range phase1Snapshot(t, f) {
				if !bytes.Equal(before[table], after) {
					t.Fatal("conflicting snapshot changed", table)
				}
			}
		})
	}
}
