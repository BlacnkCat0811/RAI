//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/admin"
	"rai/server/internal/httpapi"
	"rai/server/internal/syncing"
)

func datasetSnapshot(t *testing.T, f *fixture, dataset string) map[string]string {
	t.Helper()
	ctx := context.Background()
	out := map[string]string{}
	rows, e := f.s.Pool.Query(ctx, `SELECT table_name FROM information_schema.columns WHERE table_schema='public' AND column_name='dataset_id' AND table_name NOT IN ('request_budgets','sync_request_budgets','sync_upload_slots') ORDER BY table_name`)
	if e != nil {
		t.Fatal(e)
	}
	var tables []string
	for rows.Next() {
		var name string
		if e = rows.Scan(&name); e != nil {
			t.Fatal(e)
		}
		tables = append(tables, name)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		t.Fatal(e)
	}
	for _, table := range tables {
		var raw string
		if e = f.s.Pool.QueryRow(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text),'[]')::text FROM `+pgx.Identifier{table}.Sanitize()+` x WHERE dataset_id=$1`, dataset).Scan(&raw); e != nil {
			t.Fatal(e)
		}
		out[table] = raw
	}
	return out
}
func newDatasetRequest(f *fixture) admin.NewDatasetRequest {
	return admin.NewDatasetRequest{OperationID: syncing.NewID(), SourceDataset: f.p.DatasetID, Actor: "synthetic-operator"}
}
func preparedCredential(t *testing.T) admin.Enrollment {
	t.Helper()
	c, e := admin.PrepareEnrollment()
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func deleteBundleRequest(f *fixture, id string) syncing.WorkoutDeleteRequest {
	return syncing.WorkoutDeleteRequest{ProtocolVersion: 3, DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch, WorkoutID: id}
}
func requireSyncCode(t *testing.T, e error, code string) {
	t.Helper()
	var typed *syncing.Error
	if !errors.As(e, &typed) || typed.Code != code {
		t.Fatalf("expected %s; got %v", code, e)
	}
}

func TestNewDatasetIndependentIdentityPreservesHistoryAndFreshImport(t *testing.T) {
	ctx := context.Background()
	f, _ := trainingFixture(t)
	old, _ := testWorkoutBundle(t, f, 3)
	phase3Observation(&old)
	raw, _ := json.Marshal(old)
	if _, e := f.s.ImportWorkoutBundle(ctx, f.p, old, raw); e != nil {
		t.Fatal(e)
	}
	runOne(t, f, "analysis.workout")
	erased, _ := testWorkoutBundle(t, f, 1)
	phase3Observation(&erased)
	eraw, _ := json.Marshal(erased)
	if _, e := f.s.ImportWorkoutBundle(ctx, f.p, erased, eraw); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.DeleteWorkoutBundle(ctx, f.p, deleteBundleRequest(f, erased.WorkoutID)); e != nil {
		t.Fatal(e)
	}
	before := datasetSnapshot(t, f, f.p.DatasetID)
	oldMetadata, e := f.s.Metadata(ctx, f.p)
	if e != nil {
		t.Fatal(e)
	}
	r, c := newDatasetRequest(f), preparedCredential(t)
	result, e := admin.NewDataset(ctx, f.s.Pool, r, c)
	if e != nil {
		t.Fatal(e)
	}
	t.Run("independent identities and revision", func(t *testing.T) {
		if result.Enrollment.Token != "" || c.Token == f.enrollment.Token || c.DatasetID == f.p.DatasetID || c.DatasetEpoch == f.enrollment.DatasetEpoch || c.DeviceID == f.p.DeviceID || c.CollectorEpoch == f.enrollment.CollectorEpoch {
			t.Fatal("identity inherited")
		}
		p, e := f.s.Authenticate(ctx, c.Token)
		if e != nil {
			t.Fatal(e)
		}
		m, e := f.s.Metadata(ctx, p)
		if e != nil || m.CommitRevision != 0 || m.DatasetEpoch != c.DatasetEpoch || m.DatasetStatus != "active" {
			t.Fatal("new revision or identity incorrect")
		}
		m, e = f.s.Metadata(ctx, f.p)
		if e != nil || m.CommitRevision != oldMetadata.CommitRevision || m.DatasetStatus != "retired" {
			t.Fatal("old revision changed or not retired")
		}
	})
	t.Run("no facts training tombstones or receipts copied", func(t *testing.T) {
		for table, rows := range datasetSnapshot(t, f, c.DatasetID) {
			if table == "devices" || table == "collector_leases" {
				continue
			}
			if rows != "[]" {
				t.Fatal("copied state:", table)
			}
		}
	})
	handler := httpapi.New(f.s, analysisLog)
	t.Run("old bundle replay and token fencing", func(t *testing.T) {
		for _, token := range []string{f.enrollment.Token, c.Token} {
			status, body := uploadBundleHTTP(t, handler, token, raw)
			if status != 409 && status != 403 {
				t.Fatal("old bundle accepted", status, string(body))
			}
		}
		fresh := old
		fresh.DatasetID = c.DatasetID
		fresh.DatasetEpoch = c.DatasetEpoch
		fresh.CollectorEpoch = c.CollectorEpoch
		freshRaw, _ := json.Marshal(fresh)
		status, body := uploadBundleHTTP(t, handler, f.enrollment.Token, freshRaw)
		if status != 409 || !bytes.Contains(body, []byte("dataset_retired")) {
			t.Fatal("old token not retired", status)
		}
		p, e := f.s.Authenticate(ctx, c.Token)
		if e != nil {
			t.Fatal(e)
		}
		_, e = f.s.ImportWorkoutBundle(ctx, p, old, raw)
		requireSyncCode(t, e, "dataset_forbidden")
		bad := fresh
		bad.DatasetEpoch = f.enrollment.DatasetEpoch
		badRaw, _ := json.Marshal(bad)
		_, e = f.s.ImportWorkoutBundle(ctx, p, bad, badRaw)
		requireSyncCode(t, e, "dataset_epoch_changed")
		bad = fresh
		bad.CollectorEpoch = f.enrollment.CollectorEpoch
		badRaw, _ = json.Marshal(bad)
		_, e = f.s.ImportWorkoutBundle(ctx, p, bad, badRaw)
		requireSyncCode(t, e, "collector_fenced")
	})
	t.Run("retired upload delete child delete legacy and training reject", func(t *testing.T) {
		_, e := f.s.ImportWorkoutBundle(ctx, f.p, old, raw)
		requireSyncCode(t, e, "dataset_retired")
		_, e = f.s.DeleteWorkoutBundle(ctx, f.p, deleteBundleRequest(f, old.WorkoutID))
		requireSyncCode(t, e, "dataset_retired")
		child := old
		child.Observation = nil
		child.SampleDeletions = []syncing.SampleDeletion{{SampleType: kind, SampleUUID: old.Samples[0].UUID}}
		child.Samples = nil
		child.Series = nil
		child.Routes = nil
		childRaw, _ := json.Marshal(child)
		_, e = f.s.ImportWorkoutBundle(ctx, f.p, child, childRaw)
		requireSyncCode(t, e, "dataset_retired")
		for _, path := range []string{"/v2/sync/workout-bundles/" + old.WorkoutID, "/v2/sync/streams", "/v2/training/plans"} {
			method := "POST"
			if path == "/v2/sync/workout-bundles/"+old.WorkoutID {
				method = "DELETE"
			}
			q := httptest.NewRequest(method, path, bytes.NewReader([]byte(`{}`)))
			q.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, q)
			if w.Code != 409 || !bytes.Contains(w.Body.Bytes(), []byte("dataset_retired")) {
				t.Fatalf("retired %s: %d", path, w.Code)
			}
		}
	})
	t.Run("lost response idempotency and conflict", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			again, e := admin.NewDataset(ctx, f.s.Pool, r, admin.Enrollment{})
			if e != nil || !again.Replayed || again.Enrollment != result.Enrollment {
				t.Fatal("idempotency failed", e)
			}
		}
		if _, e := admin.NewDataset(ctx, f.s.Pool, r, preparedCredential(t)); e == nil {
			t.Fatal("unrelated prepared credential accepted for committed operation")
		}
		r.Actor = "another-actor"
		if _, e := admin.NewDataset(ctx, f.s.Pool, r, c); e == nil {
			t.Fatal("operation identity conflict accepted")
		}
		if count(t, f, "datasets") != 2 || count(t, f, "admin_operations") != 1 {
			t.Fatal("duplicate dataset created")
		}
	})
	t.Run("HealthKit visible old-deleted UUID can import into new namespace", func(t *testing.T) {
		p, e := f.s.Authenticate(ctx, c.Token)
		if e != nil {
			t.Fatal(e)
		}
		erased.DatasetID = c.DatasetID
		erased.DatasetEpoch = c.DatasetEpoch
		erased.CollectorEpoch = c.CollectorEpoch
		payload, _ := json.Marshal(erased)
		status, ack := uploadBundleHTTP(t, handler, c.Token, payload)
		if status != http.StatusOK {
			t.Fatal("fresh import failed", status, string(ack))
		}
		var receipt syncing.WorkoutImportReceipt
		if e = json.Unmarshal(ack, &receipt); e != nil || len(receipt.AnalysisJobIDs) != 1 {
			t.Fatal("new ACK invalid", e)
		}
		again, e := f.s.ImportWorkoutBundle(ctx, p, erased, payload)
		if e != nil || !bytes.Equal(bytes.TrimSpace(ack), bytes.TrimSpace(again)) {
			t.Fatal("fresh discovery retry not idempotent", e)
		}
		var ds string
		if e = f.s.Pool.QueryRow(ctx, `SELECT dataset_id::text FROM jobs WHERE id=$1`, receipt.AnalysisJobIDs[0]).Scan(&ds); e != nil || ds != c.DatasetID {
			t.Fatal("ACK job belongs to wrong dataset")
		}
		nf := *f
		nf.p = p
		nf.enrollment = c
		runOne(t, &nf, "analysis.workout")
		var n int
		if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM analysis_publications WHERE dataset_id=$1 AND scope_key=$2`, c.DatasetID, "workout:HKWorkoutTypeIdentifier:"+erased.WorkoutID).Scan(&n); e != nil || n != 1 {
			t.Fatal("new publication missing", e)
		}
	})
	counts := map[string]map[string]int{}
	for label, dataset := range map[string]string{"old": f.p.DatasetID, "new": c.DatasetID} {
		counts[label] = map[string]int{}
		for _, table := range []string{"workouts", "health_samples", "workout_routes", "entity_tombstones", "workout_imports", "training_plans", "analysis_publications"} {
			var n int
			if e := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM `+pgx.Identifier{table}.Sanitize()+` WHERE dataset_id=$1`, dataset).Scan(&n); e != nil {
				t.Fatal(e)
			}
			counts[label][table] = n
		}
	}
	summary, _ := json.Marshal(counts)
	t.Log("PHASE5A_SYNTHETIC_COUNTS", string(summary))
	after := datasetSnapshot(t, f, f.p.DatasetID)
	if !reflect.DeepEqual(before, after) {
		for table, rows := range after {
			if before[table] != rows {
				t.Error("old state changed:", table)
			}
		}
	}
}

func TestNewDatasetFailureBeforeCommitRollsBackAndPreparedResume(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	r, c := newDatasetRequest(f), preparedCredential(t)
	before := datasetSnapshot(t, f, f.p.DatasetID)
	if _, e := f.s.Pool.Exec(ctx, `CREATE FUNCTION phase5a_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic failure'; END $$; CREATE TRIGGER phase5a_fail BEFORE INSERT ON admin_operations FOR EACH ROW EXECUTE FUNCTION phase5a_fail()`); e != nil {
		t.Fatal(e)
	}
	if _, e := admin.NewDataset(ctx, f.s.Pool, r, c); e == nil {
		t.Fatal("injected failure ignored")
	}
	if count(t, f, "datasets") != 1 || count(t, f, "admin_operations") != 0 {
		t.Fatal("partial create survived rollback")
	}
	m, e := f.s.Metadata(ctx, f.p)
	if e != nil || m.DatasetStatus != "active" || !reflect.DeepEqual(before, datasetSnapshot(t, f, f.p.DatasetID)) {
		t.Fatal("source changed on rollback")
	}
	if _, e = f.s.Pool.Exec(ctx, `DROP TRIGGER phase5a_fail ON admin_operations`); e != nil {
		t.Fatal(e)
	}
	result, e := admin.NewDataset(ctx, f.s.Pool, r, c)
	if e != nil || result.Enrollment.DatasetID != c.DatasetID {
		t.Fatal("prepared resume regenerated identity", e)
	}
}

type lostOutput struct{}

func (lostOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
func TestNewDatasetCLIReadOnlyDryRunAndLostResponseRecovery(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	r := newDatasetRequest(f)
	url := f.s.Pool.Config().ConnString()
	before := businessSnapshot(t, f)
	var out bytes.Buffer
	if e := admin.AdminCommand(ctx, url, []string{"new-dataset", "--source-dataset", r.SourceDataset, "--dry-run"}, &out); e != nil {
		t.Fatal(e)
	}
	sameBusiness(t, f, before)
	if count(t, f, "admin_operations") != 0 {
		t.Fatal("dry run wrote audit")
	}
	file := filepath.Join(t.TempDir(), "enrollment.json")
	args := []string{"new-dataset", "--source-dataset", r.SourceDataset, "--operation-id", r.OperationID, "--actor", r.Actor, "--confirm-source", r.SourceDataset, "--confirm-phrase", "NEW-DATASET", "--credential-file", file}
	// Crash/failure after the private prepare file is durable but before commit.
	if _, e := f.s.Pool.Exec(ctx, `CREATE FUNCTION phase5a_cli_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic failure'; END $$; CREATE TRIGGER phase5a_cli_fail BEFORE INSERT ON admin_operations FOR EACH ROW EXECUTE FUNCTION phase5a_cli_fail()`); e != nil {
		t.Fatal(e)
	}
	if e := admin.AdminCommand(ctx, url, args, &out); e == nil {
		t.Fatal("prepared transaction failure ignored")
	}
	prepared, e := os.ReadFile(file)
	if e != nil || count(t, f, "datasets") != 1 || count(t, f, "admin_operations") != 0 {
		t.Fatal("prepare file missing or partial commit")
	}
	if _, e = f.s.Pool.Exec(ctx, `DROP TRIGGER phase5a_cli_fail ON admin_operations`); e != nil {
		t.Fatal(e)
	}
	if e := admin.AdminCommand(ctx, url, args, lostOutput{}); e == nil {
		t.Fatal("output failure missing")
	}
	if count(t, f, "datasets") != 2 {
		t.Fatal("lost response did not commit")
	}
	info, e := os.Stat(file)
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal("credential permissions incorrect")
	}
	original, e := os.ReadFile(file)
	if !bytes.Equal(original, prepared) {
		t.Fatal("resume regenerated credential")
	}
	if e != nil {
		t.Fatal(e)
	}
	out.Reset()
	if e = admin.AdminCommand(ctx, url, args, &out); e != nil {
		t.Fatal(e)
	}
	var result admin.NewDatasetResult
	if e = json.Unmarshal(out.Bytes(), &result); e != nil || !result.Replayed || result.Enrollment.Token != "" {
		t.Fatal("retry exposed credential or new operation")
	}
	again, _ := os.ReadFile(file)
	if !bytes.Equal(original, again) || count(t, f, "datasets") != 2 {
		t.Fatal("retry overwrote credential or duplicated dataset")
	}
}

func TestConcurrentNewDatasetOperationsSerializeWithOldPostDeleteWorker(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	bundle, _ := testWorkoutBundle(t, f, 1)
	phase3Observation(&bundle)
	raw, _ := json.Marshal(bundle)
	if _, e := f.s.ImportWorkoutBundle(ctx, f.p, bundle, raw); e != nil {
		t.Fatal(e)
	}
	lease := claimAnalysis(t, f, "analysis.workout")
	input, run, metrics := computeAnalysis(t, f, lease)
	next, nextRaw := testWorkoutBundle(t, f, 1)
	r, c := newDatasetRequest(f), preparedCredential(t)
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 6)
	call := func(fn func() error) { wg.Add(1); go func() { defer wg.Done(); <-start; results <- fn() }() }
	call(func() error { _, e := admin.NewDataset(ctx, f.s.Pool, r, c); return e })
	call(func() error { _, e := admin.NewDataset(ctx, f.s.Pool, r, c); return e })
	call(func() error { _, e := f.s.ImportWorkoutBundle(ctx, f.p, next, nextRaw); return e })
	call(func() error {
		_, e := f.s.DeleteWorkoutBundle(ctx, f.p, deleteBundleRequest(f, bundle.WorkoutID))
		return e
	})
	call(func() error { _, e := f.s.PublishAnalysis(ctx, lease, run, input, metrics); return e })
	close(start)
	wg.Wait()
	close(results)
	for e := range results {
		if e != nil {
			requireSyncCode(t, e, "dataset_retired")
		}
	}
	if count(t, f, "datasets") != 2 || count(t, f, "admin_operations") != 1 {
		t.Fatal("partial retirement or third dataset")
	}
	frozen := datasetSnapshot(t, f, f.p.DatasetID)
	_, e := f.s.PublishAnalysis(ctx, lease, run, input, metrics)
	requireSyncCode(t, e, "dataset_retired")
	if e = f.s.Renew(ctx, lease); e == nil {
		t.Fatal("old worker renewed")
	}
	if _, e = f.s.Claim(ctx, []string{"analysis.workout"}); !errors.Is(e, pgx.ErrNoRows) {
		t.Fatal("retired job claimed", e)
	}
	if !reflect.DeepEqual(frozen, datasetSnapshot(t, f, f.p.DatasetID)) {
		t.Fatal("old worker changed retired data")
	}
	for table, rows := range datasetSnapshot(t, f, c.DatasetID) {
		if table != "devices" && table != "collector_leases" && rows != "[]" {
			t.Fatal("old request reached new dataset:", table)
		}
	}
}

func TestConcurrentDifferentNewDatasetOperationsCannotCreateThirdDataset(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	start := make(chan struct{})
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		r, c := newDatasetRequest(f), preparedCredential(t)
		go func() { <-start; _, e := admin.NewDataset(ctx, f.s.Pool, r, c); done <- e }()
	}
	close(start)
	success := 0
	for i := 0; i < 2; i++ {
		if e := <-done; e == nil {
			success++
		}
	}
	if success != 1 || count(t, f, "datasets") != 2 || count(t, f, "admin_operations") != 1 {
		t.Fatal("source retirement did not serialize distinct operations")
	}
}
