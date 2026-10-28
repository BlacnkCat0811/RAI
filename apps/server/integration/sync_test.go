//go:build integration

package integration

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/admin"
	"rai/server/internal/httpapi"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
)

const kind = "HKQuantityTypeIdentifierHeartRate"

type fixture struct {
	s          *postgres.Store
	enrollment admin.Enrollment
	p          syncing.Principal
	stream     syncing.StreamState
}

func setup(t *testing.T) *fixture {
	t.Helper()
	return setupWithMigration(t, admin.Migrate)
}
func setupWithMigration(t *testing.T, migrate func(context.Context, string) error) *fixture {
	t.Helper()
	ctx := context.Background()
	url := os.Getenv("RAI_TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("RAI_TEST_DATABASE_URL is required; integration tests must not silently skip")
	}
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database != "rai_test" {
		t.Fatal("test root database must be named rai_test")
	}
	base, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	name := "rai_test_" + strings.ReplaceAll(syncing.NewID(), "-", "")
	if _, err = base.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		base.Close(ctx)
		t.Fatal(err)
	}
	// pgx.Config.ConnString returns the original DSN, not mutated struct fields.
	// Rewrite the URI explicitly so migrations can never target the root DB.
	uri, err := neturl.Parse(url)
	if err != nil || (uri.Scheme != "postgres" && uri.Scheme != "postgresql") {
		t.Fatal("test URL must be a PostgreSQL URI")
	}
	uri.Path = "/" + name
	url = uri.String()
	t.Cleanup(func() {
		_, err := base.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		if err != nil {
			t.Error(err)
		}
		base.Close(context.Background())
	})
	if err = migrate(ctx, url); err != nil {
		t.Fatal(err)
	}
	s, err := postgres.Open(ctx, url, 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Pool.Close)
	enrollment, err := admin.Init(ctx, s.Pool)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Authenticate(ctx, enrollment.Token)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := s.RegisterStream(ctx, p, syncing.StreamRequest{DatasetID: p.DatasetID, DatasetEpoch: enrollment.DatasetEpoch, CollectorEpoch: enrollment.CollectorEpoch, StreamID: syncing.NewID(), SampleType: kind, QueryFingerprint: "sha256:" + strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{s: s, enrollment: enrollment, p: p, stream: stream}
}
func sample(id string, value float64) syncing.Event {
	stamp := int64(1793059200000001)
	return syncing.Event{Op: "upsert", Kind: "sample", Type: kind, UUID: id, StartUs: &stamp, EndUs: &stamp, Quantity: &syncing.Quantity{Value: value, Unit: "count/min"}, Source: &syncing.Source{BundleID: "synthetic.test"}}
}
func deleted(id string) syncing.Event {
	return syncing.Event{Op: "delete", Kind: "sample", Type: kind, UUID: id}
}
func (f *fixture) envelope(t *testing.T, events []syncing.Event, seq int64, previous *string) (syncing.Envelope, []byte) {
	t.Helper()
	var decoded bytes.Buffer
	for _, e := range events {
		_ = json.NewEncoder(&decoded).Encode(e)
	}
	var wire bytes.Buffer
	g := gzip.NewWriter(&wire)
	_, _ = g.Write(decoded.Bytes())
	_ = g.Close()
	h := sha256.Sum256(wire.Bytes())
	return syncing.Envelope{ProtocolVersion: 2, DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch, StreamID: f.stream.StreamID, BatchID: syncing.NewID(), ClientSeq: seq, PreviousBatchID: previous, PageID: syncing.NewID(), FragmentCount: 1, QueryFingerprint: f.stream.QueryFingerprint, ContentEncoding: "gzip", WireBytes: wire.Len(), DecodedBytes: decoded.Len(), EventCount: len(events), PayloadSHA256: hex.EncodeToString(h[:])}, wire.Bytes()
}
func (f *fixture) commit(t *testing.T, events []syncing.Event, seq int64, previous *string) (syncing.Envelope, syncing.Receipt, []byte) {
	t.Helper()
	e, wire := f.envelope(t, events, seq, previous)
	ctx := context.Background()
	if _, err := f.s.RegisterBatch(ctx, f.p, e); err != nil {
		t.Fatal(err)
	}
	parsed, err := syncing.ParseBody(bytes.NewReader(wire), e, f.stream.SampleType)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.s.Commit(ctx, f.p, e, parsed)
	if err != nil {
		t.Fatal(err)
	}
	var r syncing.Receipt
	if err = json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return e, r, b
}
func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *syncing.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}
func count(t *testing.T, f *fixture, table string) int {
	t.Helper()
	var n int
	if err := f.s.Pool.QueryRow(context.Background(), "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCommitRetryAndDeletionBarrier(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := syncing.NewID()
	events := []syncing.Event{sample(id, 132)}
	e, r, original := f.commit(t, events, 1, nil)
	if r.Counts.Inserted != 1 || r.CommitRevision != 1 || r.AnalysisStatus != "queued" || len(r.AnalysisJobIDs) != 1 {
		t.Fatalf("bad receipt: %+v", r)
	}
	for i := 0; i < 3; i++ {
		retry, err := f.s.Commit(ctx, f.p, e, events)
		if err != nil || !bytes.Equal(retry, original) {
			t.Fatal("original receipt changed", err)
		}
	}
	state, err := f.s.Batch(ctx, f.p, e.BatchID)
	if err != nil || !bytes.Equal(state.Receipt, original) {
		t.Fatal("GET receipt differs", err)
	}
	e2, r2, _ := f.commit(t, events, 2, &e.BatchID)
	if r2.CommitRevision != 1 || r2.Counts.Unchanged != 1 || r2.AnalysisStatus != "not_required" {
		t.Fatalf("no-op consumed revision %+v", r2)
	}
	e3, r3, _ := f.commit(t, []syncing.Event{deleted(id)}, 3, &e2.BatchID)
	if r3.Counts.Deleted != 1 || r3.CommitRevision != 2 {
		t.Fatal(r3)
	}
	_, r4, _ := f.commit(t, events, 4, &e3.BatchID)
	if r4.Counts.Suppressed != 1 || r4.CommitRevision != 2 || count(t, f, "health_samples") != 0 {
		t.Fatal("deleted UUID resurrected")
	}
	if count(t, f, "change_events") != 2 || count(t, f, "entity_changes") != 2 || count(t, f, "jobs") != 2 {
		t.Fatal("duplicate side effects")
	}
}
func TestUnknownDeleteAndSamePageDeleteWins(t *testing.T) {
	f := setup(t)
	id := syncing.NewID()
	bad := sample(id, 133)
	good := sample(id, 132)
	e, r, _ := f.commit(t, []syncing.Event{good, bad, deleted(id)}, 1, nil)
	if r.Counts.Tombstoned != 1 || r.AnalysisStatus != "not_required" || count(t, f, "health_samples") != 0 {
		t.Fatal(r)
	}
	_, r, _ = f.commit(t, []syncing.Event{good}, 2, &e.BatchID)
	if r.Counts.Suppressed != 1 {
		t.Fatal("unknown delete barrier missing")
	}
}
func TestIdentitySequenceAndFencing(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	e, _ := f.envelope(t, []syncing.Event{sample(syncing.NewID(), 120)}, 1, nil)
	if _, err := f.s.RegisterBatch(ctx, f.p, e); err != nil {
		t.Fatal(err)
	}
	changed := e
	changed.FragmentCount = 2
	_, err := f.s.RegisterBatch(ctx, f.p, changed)
	requireCode(t, err, "batch_identity_conflict")
	changed = e
	changed.BatchID = syncing.NewID()
	_, err = f.s.RegisterBatch(ctx, f.p, changed)
	requireCode(t, err, "sequence_identity_conflict")
	changed = e
	changed.BatchID = syncing.NewID()
	changed.ClientSeq = 3
	changed.PreviousBatchID = &e.BatchID
	_, err = f.s.RegisterBatch(ctx, f.p, changed)
	requireCode(t, err, "sequence_gap")
	changed = e
	changed.DatasetEpoch = syncing.NewID()
	_, err = f.s.RegisterBatch(ctx, f.p, changed)
	requireCode(t, err, "dataset_epoch_changed")
	changed = e
	changed.CollectorEpoch = syncing.NewID()
	_, err = f.s.RegisterBatch(ctx, f.p, changed)
	requireCode(t, err, "collector_fenced")
	changed = e
	changed.DatasetID = syncing.NewID()
	_, err = f.s.RegisterBatch(ctx, f.p, changed)
	requireCode(t, err, "dataset_forbidden")
	_, err = f.s.Pool.Exec(ctx, `UPDATE devices SET revoked_at=now() WHERE id=$1`, f.p.DeviceID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Authenticate(ctx, f.enrollment.Token)
	requireCode(t, err, "unauthorized")
	_, err = f.s.Commit(ctx, f.p, e, []syncing.Event{sample(syncing.NewID(), 120)})
	requireCode(t, err, "collector_fenced")
}
func TestConflictRollsBackEntireBatch(t *testing.T) {
	f := setup(t)
	oldID := syncing.NewID()
	first, _, _ := f.commit(t, []syncing.Event{sample(oldID, 100)}, 1, nil)
	// Ensure a new sorted entity is applied before the conflicting existing one.
	newID := "00000001-0000-4000-8000-000000000001"
	if oldID < newID {
		t.Fatal("unexpected random UUID order")
	}
	e, wire := f.envelope(t, []syncing.Event{sample(newID, 110), sample(oldID, 101)}, 2, &first.BatchID)
	if _, err := f.s.RegisterBatch(context.Background(), f.p, e); err != nil {
		t.Fatal(err)
	}
	events, err := syncing.ParseBody(bytes.NewReader(wire), e, kind)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Commit(context.Background(), f.p, e, events)
	requireCode(t, err, "sample_identity_conflict")
	if count(t, f, "health_samples") != 1 || count(t, f, "change_events") != 1 || count(t, f, "jobs") != 1 {
		t.Fatal("partial transaction committed")
	}
	state, err := f.s.Batch(context.Background(), f.p, e.BatchID)
	if err != nil || state.State != "registered" || len(state.Receipt) != 0 {
		t.Fatal("receipt survived rollback")
	}
	stream, err := f.s.Stream(context.Background(), f.p, f.stream.StreamID)
	if err != nil || stream.LastClientSeq != 1 {
		t.Fatal("stream advanced on rollback")
	}
}
func TestConcurrentSameBatchReturnsOriginal(t *testing.T) {
	f := setup(t)
	events := []syncing.Event{sample(syncing.NewID(), 132)}
	e, _ := f.envelope(t, events, 1, nil)
	ctx := context.Background()
	if _, err := f.s.RegisterBatch(ctx, f.p, e); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	outputs := make(chan []byte, 6)
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); b, err := f.s.Commit(ctx, f.p, e, events); outputs <- b; errs <- err }()
	}
	wg.Wait()
	close(outputs)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var original []byte
	for b := range outputs {
		if original == nil {
			original = b
		}
		if !bytes.Equal(original, b) {
			t.Fatal("concurrent receipt mismatch")
		}
	}
	if count(t, f, "health_samples") != 1 || count(t, f, "change_events") != 1 || count(t, f, "jobs") != 1 {
		t.Fatal("concurrent duplicate side effects")
	}
}
func TestDatasetLockOrdersCommitAndTimeout(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	events := []syncing.Event{sample(syncing.NewID(), 120)}
	e, _ := f.envelope(t, events, 1, nil)
	if _, err := f.s.RegisterBatch(ctx, f.p, e); err != nil {
		t.Fatal(err)
	}
	tx, err := f.s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT id FROM datasets WHERE id=$1 FOR UPDATE`, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = f.s.Commit(ctx, f.p, e, events)
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatal("lock timeout missing", err)
	}
	if count(t, f, "health_samples") != 0 {
		t.Fatal("writer passed uncommitted dataset lock")
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Commit(ctx, f.p, e, events); err != nil {
		t.Fatal(err)
	}
}
func TestQueueLeaseAndDirtyGeneration(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := syncing.NewID()
	e, _, _ := f.commit(t, []syncing.Event{sample(id, 132)}, 1, nil)
	l, err := f.s.Claim(ctx, []string{"analysis.sample"})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.Renew(ctx, l); err != nil {
		t.Fatal(err)
	}
	// While generation 1 is being processed, the deletion creates generation 2.
	f.commit(t, []syncing.Event{deleted(id)}, 2, &e.BatchID)
	if err = f.s.Complete(ctx, l); err != nil {
		t.Fatal(err)
	}
	if count(t, f, "dirty_scopes") != 1 {
		t.Fatal("new dirty generation lost")
	}
	l2, err := f.s.Claim(ctx, []string{"analysis.sample"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(ctx, `UPDATE jobs SET lease_until=now()-interval '1 second' WHERE id=$1`, l2.ID); err != nil {
		t.Fatal(err)
	}
	requireCode(t, f.s.Renew(ctx, l2), "lease_lost")
	requireCode(t, f.s.Complete(ctx, l2), "lease_lost")
	if n, err := f.s.RequeueExpired(ctx); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if _, err = f.s.Pool.Exec(ctx, `UPDATE jobs SET run_after=now() WHERE id=$1`, l2.ID); err != nil {
		t.Fatal(err)
	}
	l3, err := f.s.Claim(ctx, []string{"analysis.sample"})
	if err != nil || l3.FencingToken <= l2.FencingToken {
		t.Fatal("lease token not advanced", err)
	}
	requireCode(t, f.s.Complete(ctx, l2), "lease_lost")
	if err = f.s.Complete(ctx, l3); err != nil {
		t.Fatal(err)
	}
	var pending int
	if err = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM dirty_scopes WHERE generation > clean_generation`).Scan(&pending); err != nil || pending != 0 {
		t.Fatal("consumed generation not clean", err)
	}
}

func TestHTTPUploadReceiptAndAuthentication(t *testing.T) {
	for _, payloadKind := range []string{"sample", "workout", "workout_activities"} {
		t.Run(payloadKind, func(t *testing.T) {
			f := setup(t)
			fact := sample(syncing.NewID(), 132)
			factTable := "health_samples"
			if payloadKind != "sample" {
				workoutStream(t, f)
				fact = workout(t, syncing.NewID())
				if payloadKind == "workout_activities" {
					fact = multiWorkout(t, fact.UUID)
				}
				factTable = "workouts"
			}
			server := httptest.NewServer(httpapi.New(f.s, slog.New(slog.NewTextHandler(io.Discard, nil))))
			defer server.Close()
			request := func(method, path string, body []byte, token, contentType, encoding string) (int, []byte) {
				t.Helper()
				req, err := http.NewRequest(method, server.URL+path, bytes.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				if token != "" {
					req.Header.Set("Authorization", "Bearer "+token)
				}
				if contentType != "" {
					req.Header.Set("Content-Type", contentType)
				}
				if encoding != "" {
					req.Header.Set("Content-Encoding", encoding)
				}
				resp, err := server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				b, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatal(err)
				}
				return resp.StatusCode, b
			}
			status, _ := request("GET", "/v2/handshake", nil, "", "", "")
			if status != 401 {
				t.Fatal(status)
			}
			status, b := request("GET", "/v2/handshake", nil, f.enrollment.Token, "", "")
			if status != 200 || !bytes.Contains(b, []byte(`"pageReceipts":true`)) || !bytes.Contains(b, []byte(`"analysis":true`)) || !bytes.Contains(b, []byte(`"workouts":true`)) || !bytes.Contains(b, []byte(`"workoutActivities":true`)) {
				t.Fatal(status, string(b))
			}
			status, _ = request("GET", "/readyz", nil, "", "", "")
			if status != 200 {
				t.Fatal(status)
			}
			e, wire := f.envelope(t, []syncing.Event{fact}, 1, nil)
			envelope, _ := json.Marshal(e)
			status, b = request("POST", "/v2/sync/batches", envelope, f.enrollment.Token, "application/json", "")
			if status != 200 {
				t.Fatal(status, string(b))
			}
			path := "/v2/sync/batches/" + e.BatchID + "/body"
			status, b = request("PUT", path, wire, f.enrollment.Token, "application/x-rai-health-delta+ndjson", "gzip")
			if status != 200 {
				t.Fatal(status, string(b))
			}
			original := b
			status, b = request("PUT", path, wire, f.enrollment.Token, "application/x-rai-health-delta+ndjson", "gzip")
			if status != 200 || !bytes.Equal(original, b) {
				t.Fatal("HTTP retry changed receipt", status, string(b))
			}
			status, b = request("GET", "/v2/sync/batches/"+e.BatchID, nil, f.enrollment.Token, "", "")
			var state syncing.BatchState
			if err := json.Unmarshal(b, &state); err != nil || status != 200 || !bytes.Equal(state.Receipt, original) {
				t.Fatal("GET original receipt failed", status, err)
			}
			if count(t, f, factTable) != 1 || count(t, f, "jobs") != 1 {
				t.Fatal("HTTP retry lost or duplicated fact/work")
			}

			status, b = request("GET", "/v2/jobs/"+syncing.NewID(), nil, f.enrollment.Token, "", "")
			if status != 404 {
				t.Fatal(fmt.Sprint(status, string(b)))
			}
		})
	}
}

func TestReceiptFailureRollsBackFactsAndWork(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	events := []syncing.Event{sample(syncing.NewID(), 123)}
	e, _ := f.envelope(t, events, 1, nil)
	if _, err := f.s.RegisterBatch(ctx, f.p, e); err != nil {
		t.Fatal(err)
	}
	_, err := f.s.Pool.Exec(ctx, `CREATE FUNCTION fail_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic receipt failure'; END $$;
		CREATE TRIGGER fail_receipt BEFORE UPDATE ON sync_batches FOR EACH ROW EXECUTE FUNCTION fail_receipt()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Commit(ctx, f.p, e, events); err == nil {
		t.Fatal("injected receipt failure did not abort transaction")
	}
	for _, table := range []string{"health_samples", "entity_tombstones", "entity_changes", "change_events", "jobs", "dirty_scopes"} {
		if count(t, f, table) != 0 {
			t.Fatal("partial side effect in", table)
		}
	}
	meta, err := f.s.Metadata(ctx, f.p)
	if err != nil || meta.CommitRevision != 0 {
		t.Fatal("revision escaped failed transaction", err)
	}
	state, err := f.s.Batch(ctx, f.p, e.BatchID)
	if err != nil || state.State != "registered" {
		t.Fatal("receipt persisted on failure", err)
	}
	if _, err = f.s.Pool.Exec(ctx, `DROP TRIGGER fail_receipt ON sync_batches`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Commit(ctx, f.p, e, events); err != nil {
		t.Fatal(err)
	}
}

func TestCommitResponseLossRecoveredByGET(t *testing.T) {
	for _, payloadKind := range []string{"sample", "workout", "workout_activities"} {
		t.Run(payloadKind, func(t *testing.T) {
			f := setup(t)
			fact := sample(syncing.NewID(), 132)
			factTable := "health_samples"
			if payloadKind != "sample" {
				workoutStream(t, f)
				fact = workout(t, syncing.NewID())
				if payloadKind == "workout_activities" {
					fact = multiWorkout(t, fact.UUID)
				}
				factTable = "workouts"
			}
			ctx := context.Background()
			e, wire := f.envelope(t, []syncing.Event{fact}, 1, nil)
			if _, err := f.s.RegisterBatch(ctx, f.p, e); err != nil {
				t.Fatal(err)
			}
			handler := httpapi.New(f.s, slog.New(slog.NewTextHandler(io.Discard, nil)))
			var dropped atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "PUT" && !dropped.Swap(true) {
					// Run the actual handler to commit, then close TCP before sending its response.
					recorder := httptest.NewRecorder()
					handler.ServeHTTP(recorder, r)
					if recorder.Code != 200 {
						t.Errorf("commit failed before response loss: %d", recorder.Code)
					}
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
					return
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			req, err := http.NewRequest("PUT", server.URL+"/v2/sync/batches/"+e.BatchID+"/body", bytes.NewReader(wire))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
			req.Header.Set("Content-Type", "application/x-rai-health-delta+ndjson")
			req.Header.Set("Content-Encoding", "gzip")
			if resp, err := server.Client().Do(req); err == nil {
				resp.Body.Close()
				t.Fatal("expected lost HTTP response")
			}
			req, err = http.NewRequest("GET", server.URL+"/v2/sync/batches/"+e.BatchID, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
			resp, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var state syncing.BatchState
			if err = json.NewDecoder(resp.Body).Decode(&state); err != nil || resp.StatusCode != 200 || state.State != "committed" || len(state.Receipt) == 0 {
				t.Fatal("receipt not recoverable", err)
			}
			if count(t, f, factTable) != 1 || count(t, f, "jobs") != 1 {
				t.Fatal("lost response duplicated or lost work")
			}
		})
	}
}

func TestAPIRoleCannotMigrateOrMaintain(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	suffix := strings.ReplaceAll(syncing.NewID(), "-", "")
	apiRole, workerRole := "rai_api_"+suffix, "rai_worker_"+suffix
	b, err := os.ReadFile("../../../deploy/postgres/roles.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ReplaceAll(strings.ReplaceAll(string(b), "rai_api", apiRole), "rai_worker", workerRole)
	if _, err = f.s.Pool.Exec(ctx, sql); err != nil {
		t.Fatal(err)
	}
	// Cleanup happens before database deletion; DROP OWNED removes only this test's grants.
	t.Cleanup(func() {
		_, err := f.s.Pool.Exec(context.Background(), "DROP OWNED BY "+pgx.Identifier{apiRole}.Sanitize()+", "+pgx.Identifier{workerRole}.Sanitize()+"; DROP ROLE "+pgx.Identifier{apiRole}.Sanitize()+", "+pgx.Identifier{workerRole}.Sanitize())
		if err != nil {
			t.Error(err)
		}
	})
	conn, err := f.s.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, "SET ROLE "+pgx.Identifier{apiRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(ctx, "RESET ROLE")
	for _, sql := range []string{`CREATE TABLE forbidden(id int)`, `DELETE FROM entity_tombstones`, `UPDATE devices SET can_collect=true`, `DELETE FROM sync_pages`, `UPDATE sync_pages SET fragment_count=2`, `DELETE FROM sync_request_budgets`, `UPDATE sync_request_budgets SET lane='read'`, `DELETE FROM workout_activities`, `UPDATE workout_activities SET ordinal=1`, `DELETE FROM sync_upload_slots`, `UPDATE sync_upload_slots SET slot=1`, `DELETE FROM series_chunks`, `UPDATE series_chunks SET point_count=1`, `DELETE FROM series_uploads`, `DELETE FROM workout_package_chunks`, `UPDATE workout_package_chunks SET detail_count=1`, `UPDATE workout_packages SET core='{}'`, `SELECT pg_read_file('/etc/passwd')`, `SELECT pg_backup_start('forbidden',false)`} {
		if _, err = conn.Exec(ctx, sql); err == nil {
			t.Fatalf("API role permitted maintenance: %s", sql)
		}
	}
	// Every pool connection uses SET ROLE on startup via a verified URI parameter.
	uri, err := neturl.Parse(f.s.Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	query := uri.Query()
	query.Set("options", "-crole="+apiRole)
	uri.RawQuery = query.Encode()
	restricted, err := postgres.Open(ctx, uri.String(), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer restricted.Pool.Close()
	p, err := restricted.Authenticate(ctx, f.enrollment.Token)
	if err != nil {
		t.Fatal(err)
	}
	for _, lane := range []syncing.AdmissionLane{syncing.ReadLane, syncing.WriteLane, syncing.UploadLane} {
		if err = restricted.Admit(ctx, p, lane); err != nil {
			t.Fatal("API role cannot enforce admission", err)
		}
	}
	events := []syncing.Event{sample(syncing.NewID(), 132)}
	e, _ := f.envelope(t, events, 1, nil)
	if _, err = restricted.RegisterBatch(ctx, p, e); err != nil {
		t.Fatal("API role cannot register", err)
	}
	lease := takeUpload(t, restricted, p)
	if _, err = restricted.CommitUpload(ctx, p, e, events, lease); err != nil {
		t.Fatal("API role cannot commit", err)
	}
	if err = restricted.ReleaseUpload(ctx, p, lease); err != nil {
		t.Fatal("API role cannot release lease", err)
	}
	page, err := restricted.Page(ctx, p, e.PageID)
	if err != nil || page.IngestStatus != "sealed" {
		t.Fatal("API role cannot recover page receipt", err)
	}
	// Quantity-series detail uses the same restricted API role, including
	// parent binding, immutable chunk insertion, upload fencing and seal.
	seriesID := syncing.NewID()
	parentID := syncing.NewID()
	parentStart := int64(1793059200000001)
	f.commit(t, []syncing.Event{seriesParent(parentID, parentStart, parentStart, 1)}, 2, &e.BatchID)
	seriesState, err := restricted.RegisterSeries(ctx, p, syncing.SeriesRequest{DatasetID: p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch,
		CollectorEpoch: f.enrollment.CollectorEpoch, SeriesID: seriesID, SampleType: kind, SampleUUID: parentID})
	if err != nil {
		t.Fatal("API role cannot register series", err)
	}
	point := syncing.SeriesPoint{StartUs: parentStart, EndUs: parentStart, Value: 132, Unit: "count/min"}
	chunk, err := syncing.ParseSeriesChunk(bytes.NewReader(seriesWire(t, []syncing.SeriesPoint{point})), kind, seriesState.SampleStartUs, seriesState.SampleEndUs)
	if err != nil {
		t.Fatal(err)
	}
	chunk.SeriesID, chunk.ChunkIndex = seriesID, 0
	lease = takeUpload(t, restricted, p)
	chunkBody, err := restricted.CommitSeriesChunk(ctx, p, chunk, lease)
	if err != nil {
		t.Fatal("API role cannot commit series chunk", err)
	}
	if err = restricted.ReleaseUpload(ctx, p, lease); err != nil {
		t.Fatal("API role cannot release series lease", err)
	}
	var chunkReceipt syncing.SeriesChunkReceipt
	if err = json.Unmarshal(chunkBody, &chunkReceipt); err != nil {
		t.Fatal(err)
	}
	if _, err = restricted.SealSeries(ctx, p, seriesID, syncing.SeriesSeal{ChunkCount: 1, TotalPoints: 1, ManifestSHA256: chunkReceipt.ManifestSHA256}); err != nil {
		t.Fatal("API role cannot seal series", err)
	}
	if seriesCount(t, f, "series_uploads", "state='sealed'") != 1 || seriesCount(t, f, "series_chunks", "point_count=1") != 1 {
		t.Fatal("restricted role lost series detail")
	}
	// Parent deletion under the API role cascades series detail without a direct
	// DELETE privilege on the child tables.
	if _, err = restricted.Pool.Exec(ctx, `DELETE FROM health_samples WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3`, p.DatasetID, kind, parentID); err != nil {
		t.Fatal("API role cannot delete parent sample", err)
	}
	if count(t, f, "series_uploads") != 0 || count(t, f, "series_chunks") != 0 {
		t.Fatal("API role parent deletion orphaned series detail")
	}
	workoutStream(t, f)
	w := multiWorkout(t, syncing.NewID())
	e, _ = f.envelope(t, []syncing.Event{w}, 1, nil)
	if _, err = restricted.RegisterBatch(ctx, p, e); err != nil {
		t.Fatal("API role cannot register workout", err)
	}
	lease = takeUpload(t, restricted, p)
	if _, err = restricted.CommitUpload(ctx, p, e, []syncing.Event{w}, lease); err != nil {
		t.Fatal("API role cannot commit workout and children", err)
	}
	if count(t, f, "workout_activities") != 2 {
		t.Fatal("restricted role lost activities")
	}
	routeID := syncing.NewID()
	routeStart := time.UnixMicro(*w.StartUs).UTC()
	if _, err := restricted.RegisterRoute(ctx, p, syncing.RouteManifest{
		ProtocolVersion: syncing.ProtocolVersion, DatasetID: p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch,
		CollectorEpoch: f.enrollment.CollectorEpoch, RouteID: routeID, WorkoutID: w.UUID,
		Source: &syncing.Source{BundleID: "synthetic.test"}, ExpectedChunkCount: 1, ExpectedPointCount: 1,
		FirstRecordedAtUs: routeStart.UnixMicro(), LastRecordedAtUs: routeStart.UnixMicro(),
	}); err != nil {
		t.Fatal("restricted role cannot register route", err)
	}
	if _, err := restricted.CommitRouteChunk(ctx, p, testRouteChunk(routeID, 0, testRoutePoints(routeStart, 1, 0)), lease); err != nil {
		t.Fatal("restricted role cannot append route chunk", err)
	}
	routeState, err := restricted.Route(ctx, p, routeID)
	if err != nil || routeState.CommittedPoints != 1 {
		t.Fatal("restricted role cannot read route state", err)
	}
	if _, err := restricted.SealRoute(ctx, p, routeID, syncing.RouteSeal{
		ProtocolVersion: syncing.ProtocolVersion, ChunkCount: 1, TotalPoints: 1,
		ManifestSHA256: routeState.ManifestSHA256, PayloadManifestSHA256: routeState.PayloadManifestSHA256,
	}); err != nil {
		t.Fatal("restricted role cannot seal route", err)
	}
	for _, sql := range []string{
		`UPDATE workout_routes SET state='sealed', sealed_at=now(), receipt='synthetic' WHERE dataset_id=$1 AND route_uuid=$2`,
		`DELETE FROM workout_routes WHERE dataset_id=$1 AND route_uuid=$2`,
		`INSERT INTO route_chunks(dataset_id,route_uuid,chunk_index,point_count,content_sha256,points) VALUES($1,$2,0,1,sha256('x'::bytea),'[{"sequence":0}]'::jsonb)`,
		`INSERT INTO route_points(dataset_id,route_uuid,chunk_index,sequence,recorded_at,latitude,longitude) VALUES($1,$2,0,0,now(),0,0)`,
		`DELETE FROM route_points WHERE dataset_id=$1 AND route_uuid=$2`,
	} {
		if _, err := restricted.Pool.Exec(ctx, sql, p.DatasetID, routeID); err == nil {
			t.Fatalf("restricted role permitted direct route mutation: %s", sql)
		}
	}
	// A second route is removed by the Workout cascade; the first is deleted
	// through the only barrier-writing function to prove that permission path.
	route2 := syncing.NewID()
	if _, err := restricted.RegisterRoute(ctx, p, syncing.RouteManifest{
		ProtocolVersion: syncing.ProtocolVersion, DatasetID: p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch,
		CollectorEpoch: f.enrollment.CollectorEpoch, RouteID: route2, WorkoutID: w.UUID,
		Source: &syncing.Source{BundleID: "synthetic.test"}, ExpectedChunkCount: 1, ExpectedPointCount: 1,
		FirstRecordedAtUs: routeStart.UnixMicro(), LastRecordedAtUs: routeStart.UnixMicro(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := restricted.CommitRouteChunk(ctx, p, testRouteChunk(route2, 0, testRoutePoints(routeStart, 1, 0)), lease); err != nil {
		t.Fatal(err)
	}
	route2State, err := restricted.Route(ctx, p, route2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restricted.SealRoute(ctx, p, route2, syncing.RouteSeal{
		ProtocolVersion: syncing.ProtocolVersion, ChunkCount: 1, TotalPoints: 1,
		ManifestSHA256: route2State.ManifestSHA256, PayloadManifestSHA256: route2State.PayloadManifestSHA256,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := restricted.DeleteRoute(ctx, p, routeID, syncing.RouteDeleteRequest{
		ProtocolVersion: syncing.ProtocolVersion, DatasetEpoch: f.enrollment.DatasetEpoch,
		CollectorEpoch: f.enrollment.CollectorEpoch,
	}); err != nil {
		t.Fatal("restricted role cannot delete route", err)
	}
	if count(t, f, "route_deletions") != 1 || !embeddedRouteTombstone(t, f, routeID) {
		t.Fatal("restricted route delete missed receipt/tombstone")
	}
	del, _ := f.envelope(t, []syncing.Event{workoutDelete(w.UUID)}, 2, &e.BatchID)
	if _, err = restricted.RegisterBatch(ctx, p, del); err != nil {
		t.Fatal(err)
	}
	if _, err = restricted.CommitUpload(ctx, p, del, []syncing.Event{workoutDelete(w.UUID)}, lease); err != nil {
		t.Fatal("API role cannot delete workout and cascade children", err)
	}
	if count(t, f, "workout_activities") != 0 {
		t.Fatal("restricted role orphaned activities")
	}
	if count(t, f, "workout_routes") != 0 || count(t, f, "route_chunks") != 0 || count(t, f, "route_points") != 0 {
		t.Fatal("restricted role orphaned route staging")
	}
	if err = restricted.ReleaseUpload(ctx, p, lease); err != nil {
		t.Fatal("API role cannot release workout lease", err)
	}
	// Oversized Workout package uses the same restricted role for registration,
	// immutable chunk insertion, upload fencing and seal.
	pkgID := syncing.NewID()
	pkgStart, pkgEnd := int64(1793059200000000), int64(1793059260000000)
	pkgState, err := restricted.RegisterWorkoutPackage(ctx, p, workoutPackageRequest(f, pkgID, pkgStart, pkgEnd))
	if err != nil {
		t.Fatal("API role cannot register workout package", err)
	}
	pkgChunk, err := syncing.ParseWorkoutDetailChunk(bytes.NewReader(workoutPackageWire(t, workoutPackageActivityLine(t, 0, pkgStart, pkgEnd))), pkgState.StartUs, pkgState.EndUs)
	if err != nil {
		t.Fatal(err)
	}
	pkgChunk.WorkoutID, pkgChunk.ChunkIndex = pkgID, 0
	lease = takeUpload(t, restricted, p)
	pkgBody, err := restricted.CommitWorkoutPackageChunk(ctx, p, pkgChunk, lease)
	if err != nil {
		t.Fatal("API role cannot commit workout package chunk", err)
	}
	if err = restricted.ReleaseUpload(ctx, p, lease); err != nil {
		t.Fatal(err)
	}
	var pkgReceipt syncing.WorkoutPackageChunkReceipt
	if err = json.Unmarshal(pkgBody, &pkgReceipt); err != nil {
		t.Fatal(err)
	}
	if _, err = restricted.SealWorkoutPackage(ctx, p, pkgID, syncing.WorkoutPackageSeal{ChunkCount: 1, TotalDetails: 3, ManifestSHA256: pkgReceipt.ManifestSHA256}); err != nil {
		t.Fatal("API role cannot seal workout package", err)
	}
	if seriesCount(t, f, "workout_packages", "state='sealed'") != 1 {
		t.Fatal("restricted role lost package marker")
	}
	if _, err = restricted.Pool.Exec(ctx, `DELETE FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2`, p.DatasetID, pkgID); err != nil {
		t.Fatal("API role cannot delete packaged workout", err)
	}
	if count(t, f, "workout_packages") != 0 || count(t, f, "workout_package_chunks") != 0 {
		t.Fatal("packaged workout delete orphaned staging")
	}
}

func TestCleanScopeKeepsMonotonicGeneration(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := syncing.NewID()
	e, _, _ := f.commit(t, []syncing.Event{sample(id, 132)}, 1, nil)
	l, err := f.s.Claim(ctx, []string{"analysis.sample"})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.Complete(ctx, l); err != nil {
		t.Fatal(err)
	}
	_, r, _ := f.commit(t, []syncing.Event{deleted(id)}, 2, &e.BatchID)
	if len(r.AnalysisJobIDs) != 1 {
		t.Fatal("new change failed to schedule")
	}
	l, err = f.s.Claim(ctx, []string{"analysis.sample"})
	if err != nil || l.TargetGeneration != 2 {
		t.Fatal("clean scope reused old generation", l.TargetGeneration, err)
	}
}

func TestMaximumDeleteBatchAndEmptyBatch(t *testing.T) {
	f := setup(t)
	events := make([]syncing.Event, syncing.MaxEvents)
	for i := range events {
		events[i] = deleted(syncing.NewID())
	}
	e, r, _ := f.commit(t, events, 1, nil)
	if r.Counts.Tombstoned != syncing.MaxEvents || count(t, f, "entity_tombstones") != syncing.MaxEvents || count(t, f, "change_events") != syncing.MaxEvents {
		t.Fatal("delete storm was truncated")
	}
	_, r, _ = f.commit(t, nil, 2, &e.BatchID)
	if r.CommitRevision != 1 || r.AnalysisStatus != "not_required" {
		t.Fatal("empty batch changed facts", r)
	}
}

func TestReadOnlyDeviceAndForeignDatasetIsolation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	token := strings.Repeat("synthetic-read-only-", 3)
	digest := sha256.Sum256([]byte(token))
	device := syncing.NewID()
	if _, err := f.s.Pool.Exec(ctx, `INSERT INTO devices(dataset_id,id,token_sha256,can_collect) VALUES($1,$2,$3,false)`, f.p.DatasetID, device, digest[:]); err != nil {
		t.Fatal(err)
	}
	p, err := f.s.Authenticate(ctx, token)
	if err != nil || p.CanCollect {
		t.Fatal("read-only scope wrong", err)
	}
	e, _ := f.envelope(t, nil, 1, nil)
	_, err = f.s.RegisterBatch(ctx, p, e)
	requireCode(t, err, "read_only_device")
	otherDataset := syncing.NewID()
	otherDevice := syncing.NewID()
	otherToken := strings.Repeat("other-synthetic-", 4)
	otherDigest := sha256.Sum256([]byte(otherToken))
	if _, err = f.s.Pool.Exec(ctx, `INSERT INTO datasets(id,epoch,service_id) VALUES($1,$2,$3)`, otherDataset, syncing.NewID(), syncing.NewID()); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(ctx, `INSERT INTO devices(dataset_id,id,token_sha256) VALUES($1,$2,$3)`, otherDataset, otherDevice, otherDigest[:]); err != nil {
		t.Fatal(err)
	}
	other, err := f.s.Authenticate(ctx, otherToken)
	if err != nil {
		t.Fatal(err)
	}
	first, _, _ := f.commit(t, []syncing.Event{sample(syncing.NewID(), 120)}, 1, nil)
	_, err = f.s.Batch(ctx, other, first.BatchID)
	requireCode(t, err, "batch_not_found")
	_, err = f.s.Stream(ctx, other, f.stream.StreamID)
	requireCode(t, err, "stream_not_found")
}

func TestCancellationDoesNotAdvanceStream(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	events := []syncing.Event{sample(syncing.NewID(), 132)}
	e, _ := f.envelope(t, events, 1, nil)
	if _, err := f.s.RegisterBatch(ctx, f.p, e); err != nil {
		t.Fatal(err)
	}
	tx, err := f.s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT id FROM datasets WHERE id=$1 FOR UPDATE`, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if _, err = f.s.Commit(short, f.p, e, events); err == nil {
		t.Fatal("cancelled transaction committed")
	}
	if count(t, f, "health_samples") != 0 {
		t.Fatal("cancelled transaction wrote facts")
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Commit(ctx, f.p, e, events); err != nil {
		t.Fatal("retry after cancellation failed", err)
	}
}

func TestReconnectAfterTerminatedConnection(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	isolated, err := postgres.Open(ctx, f.s.Pool.Config().ConnString(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer isolated.Pool.Close()
	var pid int
	if err = isolated.Pool.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(ctx, `SELECT pg_terminate_backend($1)`, pid); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		err = isolated.Ready(ctx)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("pool did not recover", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestCommitRevisionCannotOvertakeUncommittedWrite(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	events := []syncing.Event{sample(syncing.NewID(), 132)}
	e, _ := f.envelope(t, events, 1, nil)
	if _, err := f.s.RegisterBatch(ctx, f.p, e); err != nil {
		t.Fatal(err)
	}
	tx, err := f.s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `UPDATE datasets SET commit_rev=1 WHERE id=$1`, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO change_events(dataset_id,commit_rev,ordinal,event_id,entity_kind,entity_key,operation,payload)
		VALUES($1,1,0,$2,'synthetic','A','upsert','{}')`, f.p.DatasetID, syncing.NewID()); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := f.s.Commit(ctx, f.p, e, events); result <- err }()
	select {
	case err := <-result:
		t.Fatal("writer overtook uncommitted revision", err)
	case <-time.After(80 * time.Millisecond):
	}
	var visible int
	if err = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM change_events`).Scan(&visible); err != nil || visible != 0 {
		t.Fatal("uncommitted event became visible", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-result; err != nil {
		t.Fatal(err)
	}
	var max int64
	if err = f.s.Pool.QueryRow(ctx, `SELECT commit_rev FROM datasets WHERE id=$1`, f.p.DatasetID).Scan(&max); err != nil || max != 2 {
		t.Fatal("commit order lost", max, err)
	}
	if count(t, f, "change_events") != 2 {
		t.Fatal("publication log lost an event")
	}
}

func TestMigrationsIdempotentAndInitializationProtected(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if err := admin.Migrate(ctx, f.s.Pool.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Init(ctx, f.s.Pool); err == nil {
		t.Fatal("init replaced existing dataset")
	}
	if count(t, f, "datasets") != 1 || count(t, f, "devices") != 1 {
		t.Fatal("initialization changed data")
	}
	var version string
	if err := f.s.Pool.QueryRow(ctx, `SHOW server_version`).Scan(&version); err != nil || !strings.HasPrefix(version, "18.") {
		t.Fatal("tests require PostgreSQL 18", version, err)
	}
	for _, setting := range []string{"fsync", "full_page_writes", "synchronous_commit", "data_checksums"} {
		var value string
		if err := f.s.Pool.QueryRow(ctx, "SHOW "+setting).Scan(&value); err != nil || value != "on" {
			t.Fatal("durability setting", setting, value, err)
		}
	}
}
