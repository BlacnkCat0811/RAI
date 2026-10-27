//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/pressly/goose/v3"
	"rai/server/internal/admin"
	"rai/server/internal/httpapi"
	"rai/server/internal/syncing"
	"rai/server/migrations"
)

func pageEnvelope(t *testing.T, f *fixture, page string, index, fragments int, events []syncing.Event, seq int64, previous *string) (syncing.Envelope, []byte) {
	t.Helper()
	e, wire := f.envelope(t, events, seq, previous)
	e.PageID, e.FragmentIndex, e.FragmentCount = page, index, fragments
	return e, wire
}
func registerCommit(t *testing.T, f *fixture, e syncing.Envelope, events []syncing.Event) []byte {
	t.Helper()
	if _, err := f.s.RegisterBatch(context.Background(), f.p, e); err != nil {
		t.Fatal(err)
	}
	receipt, err := f.s.Commit(context.Background(), f.p, e, events)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}
func pageState(t *testing.T, f *fixture, id string, fragments int, state string) syncing.PageState {
	t.Helper()
	p, err := f.s.Page(context.Background(), f.p, id)
	if err != nil || p.CommittedFragments != fragments || p.IngestStatus != state {
		t.Fatalf("page state: %+v %v", p, err)
	}
	if (state == "sealed") != (len(p.Receipt) > 0) {
		t.Fatal("page receipt does not match seal")
	}
	return p
}

func TestPageFragmentsSealAndImmutableReceipts(t *testing.T) {
	for _, payload := range []string{"sample", "workout"} {
		t.Run(payload, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			var events []syncing.Event
			if payload == "workout" {
				workoutStream(t, f)
				events = []syncing.Event{workout(t, syncing.NewID())}
			} else {
				events = []syncing.Event{sample(syncing.NewID(), 132)}
			}
			page := syncing.NewID()
			e1, _ := pageEnvelope(t, f, page, 0, 3, events, 1, nil)
			if _, err := f.s.RegisterBatch(ctx, f.p, e1); err != nil {
				t.Fatal(err)
			}
			pageState(t, f, page, 0, "pending")
			first := registerCommit(t, f, e1, events)
			pageState(t, f, page, 1, "pending")
			// A pure no-op fragment still contributes to the ordered page manifest.
			e2, _ := pageEnvelope(t, f, page, 1, 3, events, 2, &e1.BatchID)
			registerCommit(t, f, e2, events)
			pageState(t, f, page, 2, "pending")
			// An empty final HealthKit page fragment is a real durable gzip batch.
			e3, _ := pageEnvelope(t, f, page, 2, 3, nil, 3, &e2.BatchID)
			last := registerCommit(t, f, e3, nil)
			p := pageState(t, f, page, 3, "sealed")
			var r syncing.PageReceipt
			if err := json.Unmarshal(p.Receipt, &r); err != nil {
				t.Fatal(err)
			}
			chain := make([]byte, 32)
			for _, e := range []syncing.Envelope{e1, e2, e3} {
				_, digest := e.Canonical()
				h := sha256.Sum256(append(chain, digest...))
				chain = h[:]
			}
			if r.ManifestSHA256 != hex.EncodeToString(chain) || r.FirstClientSeq != 1 || r.LastClientSeq != 3 || r.LastBatchID != e3.BatchID || r.FragmentCount != 3 || r.CommitRevision != 1 || r.DatasetEpoch != e1.DatasetEpoch || r.CollectorEpoch != e1.CollectorEpoch || r.QueryFingerprint != e1.QueryFingerprint || r.StreamID != e1.StreamID {
				t.Fatalf("incorrect page receipt: %+v", r)
			}
			for _, c := range []struct {
				e       syncing.Envelope
				events  []syncing.Event
				receipt []byte
			}{{e1, events, first}, {e3, nil, last}} {
				actual := registerCommit(t, f, c.e, c.events)
				if !bytes.Equal(actual, c.receipt) {
					t.Fatal("batch retry changed original receipt")
				}
			}
			// Later pages/revisions must not mutate the sealed receipt.
			next, _, _ := f.commit(t, nil, 4, &e3.BatchID)
			again := pageState(t, f, page, 3, "sealed")
			if !bytes.Equal(p.Receipt, again.Receipt) {
				t.Fatal("sealed page receipt changed")
			}
			var revision int64
			if err := f.s.Pool.QueryRow(ctx, `SELECT commit_rev FROM datasets WHERE id=$1`, f.p.DatasetID).Scan(&revision); err != nil || revision != 1 {
				t.Fatal("page seal consumed business revision", revision, err)
			}
			later := sample(syncing.NewID(), 133)
			if payload == "workout" {
				later = workout(t, syncing.NewID())
			}
			_, laterReceipt, _ := f.commit(t, []syncing.Event{later}, 5, &next.BatchID)
			if laterReceipt.CommitRevision != 2 {
				t.Fatal("later fact failed to advance revision")
			}
			again = pageState(t, f, page, 3, "sealed")
			if !bytes.Equal(p.Receipt, again.Receipt) {
				t.Fatal("later revision changed sealed receipt")
			}
		})
	}
}

func TestPageRegistrationRejectsGapsAndIdentityChanges(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	page := syncing.NewID()
	first, _ := pageEnvelope(t, f, page, 1, 3, nil, 1, nil)
	_, err := f.s.RegisterBatch(ctx, f.p, first)
	requireCode(t, err, "page_fragment_gap")
	if count(t, f, "sync_batches") != 0 || count(t, f, "sync_pages") != 0 {
		t.Fatal("invalid first fragment persisted")
	}
	first.FragmentIndex = 0
	registerCommit(t, f, first, nil)
	next, _ := pageEnvelope(t, f, page, 1, 3, nil, 2, &first.BatchID)
	for _, tc := range []struct {
		name, code string
		edit       func(*syncing.Envelope)
	}{
		{"fragment_count", "page_identity_conflict", func(e *syncing.Envelope) { e.FragmentCount = 4 }},
		{"repeated_index", "page_identity_conflict", func(e *syncing.Envelope) { e.FragmentIndex = 0 }},
		{"missing_fragment", "page_identity_conflict", func(e *syncing.Envelope) { e.FragmentIndex = 2 }},
		{"interleaved_page", "page_incomplete", func(e *syncing.Envelope) { e.PageID = syncing.NewID(); e.FragmentIndex = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := next
			tc.edit(&e)
			_, err := f.s.RegisterBatch(ctx, f.p, e)
			requireCode(t, err, tc.code)
		})
	}
	// A page ID cannot be transplanted to another collection stream.
	workoutStream(t, f)
	other, _ := pageEnvelope(t, f, page, 0, 3, nil, 1, nil)
	_, err = f.s.RegisterBatch(ctx, f.p, other)
	requireCode(t, err, "page_identity_conflict")
	if count(t, f, "sync_batches") != 1 || count(t, f, "sync_pages") != 1 {
		t.Fatal("conflicts created registrations")
	}
	pageState(t, f, page, 1, "pending")
}

func TestPageSealFailureRollsBackFinalFragment(t *testing.T) {
	for _, scenario := range []struct{ payload, op string }{{"sample", "insert"}, {"sample", "delete"}, {"workout", "insert"}, {"workout", "delete"}} {
		t.Run(scenario.payload+"_"+scenario.op, func(t *testing.T) {
			op := scenario.op
			f := setup(t)
			ctx := context.Background()
			id := syncing.NewID()
			event := sample(id, 132)
			deletion := deleted(id)
			if scenario.payload == "workout" {
				workoutStream(t, f)
				event = workout(t, id)
				deletion = workoutDelete(id)
			}
			var previous *string
			seq := int64(1)
			if op == "delete" {
				e, _, _ := f.commit(t, []syncing.Event{event}, seq, nil)
				previous = &e.BatchID
				seq++
			}
			page := syncing.NewID()
			e1, _ := pageEnvelope(t, f, page, 0, 2, nil, seq, previous)
			registerCommit(t, f, e1, nil)
			if op == "delete" {
				event = deletion
			}
			events := []syncing.Event{event}
			e2, _ := pageEnvelope(t, f, page, 1, 2, events, seq+1, &e1.BatchID)
			if _, err := f.s.RegisterBatch(ctx, f.p, e2); err != nil {
				t.Fatal(err)
			}
			before := pageState(t, f, page, 1, "pending")
			var oldManifest []byte
			if err := f.s.Pool.QueryRow(ctx, `SELECT manifest_sha256 FROM sync_pages WHERE dataset_id=$1 AND page_id=$2`, f.p.DatasetID, page).Scan(&oldManifest); err != nil {
				t.Fatal(err)
			}
			tables := []string{"health_samples", "workouts", "workout_statistics", "workout_events", "entity_tombstones", "entity_changes", "change_events", "dirty_scopes", "jobs"}
			counts := map[string]int{}
			for _, table := range tables {
				counts[table] = count(t, f, table)
			}
			var rev, generation int64
			if err := f.s.Pool.QueryRow(ctx, `SELECT commit_rev FROM datasets WHERE id=$1`, f.p.DatasetID).Scan(&rev); err != nil {
				t.Fatal(err)
			}
			if err := f.s.Pool.QueryRow(ctx, `SELECT coalesce(sum(generation),0) FROM dirty_scopes`).Scan(&generation); err != nil {
				t.Fatal(err)
			}
			_, err := f.s.Pool.Exec(ctx, `CREATE FUNCTION fail_page_seal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic page seal failure'; END $$;
 CREATE TRIGGER fail_page_seal BEFORE UPDATE ON sync_pages FOR EACH ROW WHEN (NEW.receipt IS NOT NULL) EXECUTE FUNCTION fail_page_seal()`)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.s.Commit(ctx, f.p, e2, events); err == nil {
				t.Fatal("fault injection did not fail")
			}
			for table, n := range counts {
				if count(t, f, table) != n {
					t.Fatal("page failure leaked side effects", table)
				}
			}
			p := pageState(t, f, page, 1, "pending")
			var actualManifest []byte
			if err := f.s.Pool.QueryRow(ctx, `SELECT manifest_sha256 FROM sync_pages WHERE dataset_id=$1 AND page_id=$2`, f.p.DatasetID, page).Scan(&actualManifest); err != nil || !bytes.Equal(oldManifest, actualManifest) {
				t.Fatal("failed seal changed manifest", err)
			}
			if !bytes.Equal(before.Receipt, p.Receipt) {
				t.Fatal("page changed")
			}
			var actualRev, actualGeneration int64
			if err = f.s.Pool.QueryRow(ctx, `SELECT commit_rev FROM datasets WHERE id=$1`, f.p.DatasetID).Scan(&actualRev); err != nil {
				t.Fatal(err)
			}
			if err = f.s.Pool.QueryRow(ctx, `SELECT coalesce(sum(generation),0) FROM dirty_scopes`).Scan(&actualGeneration); err != nil || actualGeneration != generation || actualRev != rev {
				t.Fatal("failed seal changed revision/generation", err)
			}
			stream, err := f.s.Stream(ctx, f.p, e2.StreamID)
			if err != nil || stream.LastClientSeq != e1.ClientSeq {
				t.Fatal("failed seal advanced stream", err)
			}
			batch, err := f.s.Batch(ctx, f.p, e2.BatchID)
			if err != nil || batch.State != "registered" || len(batch.Receipt) != 0 {
				t.Fatal("failed seal saved batch receipt", err)
			}
			if _, err = f.s.Pool.Exec(ctx, `DROP TRIGGER fail_page_seal ON sync_pages`); err != nil {
				t.Fatal(err)
			}
			registerCommit(t, f, e2, events)
			pageState(t, f, page, 2, "sealed")
		})
	}
}

func TestConcurrentFinalPageFragmentSealsOnce(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	page := syncing.NewID()
	first, _ := pageEnvelope(t, f, page, 0, 2, nil, 1, nil)
	registerCommit(t, f, first, nil)
	events := []syncing.Event{sample(syncing.NewID(), 132)}
	last, _ := pageEnvelope(t, f, page, 1, 2, events, 2, &first.BatchID)
	if _, err := f.s.RegisterBatch(ctx, f.p, last); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	receipts := make([][]byte, 6)
	errs := make([]error, 6)
	for i := range receipts {
		wg.Add(1)
		go func(i int) { defer wg.Done(); receipts[i], errs[i] = f.s.Commit(ctx, f.p, last, events) }(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil || !bytes.Equal(receipts[0], receipts[i]) {
			t.Fatal("concurrent retry differs", i, err)
		}
	}
	pageState(t, f, page, 2, "sealed")
	if count(t, f, "health_samples") != 1 || count(t, f, "jobs") != 1 || count(t, f, "change_events") != 1 {
		t.Fatal("concurrent seal duplicated facts/jobs")
	}
}

func TestHTTPPageRecoveryAfterLostFinalResponse(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	page := syncing.NewID()
	first, _ := pageEnvelope(t, f, page, 0, 2, nil, 1, nil)
	registerCommit(t, f, first, nil)
	last, wire := pageEnvelope(t, f, page, 1, 2, nil, 2, &first.BatchID)
	if _, err := f.s.RegisterBatch(ctx, f.p, last); err != nil {
		t.Fatal(err)
	}
	handler := httpapi.New(f.s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, r)
			if rec.Code != 200 {
				t.Error("final fragment did not commit", rec.Code)
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
	req, err := http.NewRequest("PUT", server.URL+"/v2/sync/batches/"+last.BatchID+"/body", bytes.NewReader(wire))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
	req.Header.Set("Content-Type", "application/x-rai-health-delta+ndjson")
	req.Header.Set("Content-Encoding", "gzip")
	resp, err := server.Client().Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected lost response")
	}
	query := func(token, id string) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest("GET", server.URL+"/v2/sync/pages/"+id, nil)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
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
	status, body := query(f.enrollment.Token, page)
	var p syncing.PageState
	if err = json.Unmarshal(body, &p); err != nil || status != 200 || p.IngestStatus != "sealed" || p.CommittedFragments != 2 {
		t.Fatal("GET failed to recover sealed page", status, string(body), err)
	}
	original := pageState(t, f, page, 2, "sealed")
	if !bytes.Equal(p.Receipt, original.Receipt) {
		t.Fatal("GET changed original page receipt")
	}
	if status, _ = query("", page); status != 401 {
		t.Fatal("unauthenticated page visible")
	}
	if status, _ = query(f.enrollment.Token, syncing.NewID()); status != 404 {
		t.Fatal("unknown page found")
	}
	if status, _ = query(f.enrollment.Token, "bad-id"); status != 422 {
		t.Fatal("invalid page ID accepted")
	}
	// Dataset-scoped lookup cannot see another dataset's receipt, even read-only.
	_, err = f.s.Page(ctx, syncing.Principal{DatasetID: syncing.NewID()}, page)
	requireCode(t, err, "page_not_found")
	readOnly := f.p
	readOnly.CanCollect = false
	if _, err = f.s.Page(ctx, readOnly, page); err != nil {
		t.Fatal("read-only cannot recover receipt", err)
	}
}

func TestSchema3UpgradePreservesBatchReceiptsWithoutInventingPageSeals(t *testing.T) {
	f := setupWithMigration(t, func(ctx context.Context, url string) error {
		db, err := sql.Open("pgx", url)
		if err != nil {
			return err
		}
		defer db.Close()
		provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.Files)
		if err != nil {
			return err
		}
		_, err = provider.UpTo(ctx, 3)
		return err
	})
	ctx := context.Background()
	first, _ := f.envelope(t, nil, 1, nil)
	original, err := json.Marshal(syncing.Receipt{BatchID: first.BatchID, IngestStatus: "committed", DatasetEpoch: first.DatasetEpoch, ClientSeq: 1, PayloadSHA256: first.PayloadSHA256, CommitRevision: 0, AnalysisStatus: "not_required", AnalysisJobIDs: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	next, _ := f.envelope(t, nil, 2, &first.BatchID)
	// Seed schema-3 records through its original columns, including one registered
	// but not committed batch. No schema-4 code runs against schema-3 projections.
	for _, e := range []syncing.Envelope{first, next} {
		canonical, digest := e.Canonical()
		payload, err := hex.DecodeString(e.PayloadSHA256)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.s.Pool.Exec(ctx, `INSERT INTO sync_batches(dataset_id,batch_id,stream_id,client_seq,dataset_epoch,envelope_sha256,payload_sha256,envelope,event_count) VALUES($1,$2,$3,$4,$5,$6,$7,$8,0)`, f.p.DatasetID, e.BatchID, e.StreamID, e.ClientSeq, e.DatasetEpoch, digest, payload, canonical)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err = f.s.Pool.Exec(ctx, `UPDATE sync_batches SET state='committed',receipt=$3,committed_at=now() WHERE dataset_id=$1 AND batch_id=$2`, f.p.DatasetID, first.BatchID, original); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(ctx, `UPDATE sync_streams SET last_client_seq=1,last_batch_id=$2 WHERE dataset_id=$1 AND id=$3`, f.p.DatasetID, first.BatchID, first.StreamID); err != nil {
		t.Fatal(err)
	}
	if err = admin.Migrate(ctx, f.s.Pool.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
	if err = f.s.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	stored, err := f.s.Batch(ctx, f.p, first.BatchID)
	if err != nil || !bytes.Equal(stored.Receipt, original) {
		t.Fatal("migration altered original receipt", err)
	}
	retry, err := f.s.Commit(ctx, f.p, first, nil)
	if err != nil || !bytes.Equal(retry, original) {
		t.Fatal("migration broke retry", err)
	}
	if _, err = f.s.Commit(ctx, f.p, next, nil); err != nil {
		t.Fatal("legacy registration cannot complete", err)
	}
	if count(t, f, "sync_pages") != 0 {
		t.Fatal("migration invented page evidence")
	}
	_, err = f.s.Page(ctx, f.p, first.PageID)
	requireCode(t, err, "page_not_found")
	reused, _ := f.envelope(t, nil, 3, &next.BatchID)
	reused.PageID = first.PageID
	_, err = f.s.RegisterBatch(ctx, f.p, reused)
	requireCode(t, err, "page_identity_conflict")
	e, _, _ := f.commit(t, nil, 3, &next.BatchID)
	pageState(t, f, e.PageID, 1, "sealed")
	if err = admin.Migrate(ctx, f.s.Pool.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
	pageState(t, f, e.PageID, 1, "sealed")
}

func TestPageSequenceBounds(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	// Synthetic near-exhausted sequence: a page cannot reserve a span past bigint.
	previous := syncing.NewID()
	if _, err := f.s.Pool.Exec(ctx, `UPDATE sync_streams SET last_client_seq=$3,last_batch_id=$4 WHERE dataset_id=$1 AND id=$2`, f.p.DatasetID, f.stream.StreamID, int64(math.MaxInt64-1), previous); err != nil {
		t.Fatal(err)
	}
	e, _ := f.envelope(t, nil, math.MaxInt64, &previous)
	e.FragmentCount = 2
	_, err := f.s.RegisterBatch(ctx, f.p, e)
	requireCode(t, err, "unsupported_payload")
	if count(t, f, "sync_pages") != 0 || count(t, f, "sync_batches") != 0 {
		t.Fatal("overflowing page reserved identity")
	}
	e.FragmentCount = 1
	registerCommit(t, f, e, nil)
	pageState(t, f, e.PageID, 1, "sealed")
}
