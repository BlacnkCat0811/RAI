//go:build integration

package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"rai/server/internal/admin"
	"rai/server/internal/httpapi"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
	"rai/server/migrations"
)

func budgetTime(t *testing.T, f *fixture, device *string, lane syncing.AdmissionLane) time.Time {
	t.Helper()
	var at time.Time
	if err := f.s.Pool.QueryRow(context.Background(), `SELECT available_at FROM sync_request_budgets WHERE dataset_id=$1 AND device_id IS NOT DISTINCT FROM $2::uuid AND lane=$3`, f.p.DatasetID, device, lane).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at
}

func TestAdmissionDurableAtomicBudgets(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := f.s.Admit(ctx, f.p, syncing.UploadLane); err != nil {
			t.Fatal(err)
		}
	}
	datasetBefore := budgetTime(t, f, nil, syncing.UploadLane)
	deviceBefore := budgetTime(t, f, &f.p.DeviceID, syncing.UploadLane)
	// A fresh pool has no process-local limiter state to preserve.
	restarted, err := postgres.Open(ctx, f.s.Pool.Config().ConnString(), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Pool.Close()
	err = restarted.Admit(ctx, f.p, syncing.UploadLane)
	requireCode(t, err, "device_rate_exceeded")
	var limit *syncing.Error
	if !errors.As(err, &limit) || limit.Status != 429 || limit.RetryAfterSeconds < 1 || limit.RetryAfterSeconds > 2 {
		t.Fatal("bad retry delay", err)
	}
	if !budgetTime(t, f, nil, syncing.UploadLane).Equal(datasetBefore) || !budgetTime(t, f, &f.p.DeviceID, syncing.UploadLane).Equal(deviceBefore) {
		t.Fatal("denial consumed quota or extended debt")
	}
	rotated := strings.Repeat("synthetic-rotated-token-", 3)
	if _, err = f.s.Pool.Exec(ctx, `UPDATE devices SET token_sha256=sha256($2::bytea) WHERE id=$1`, f.p.DeviceID, []byte(rotated)); err != nil {
		t.Fatal(err)
	}
	rotatedPrincipal, err := restarted.Authenticate(ctx, rotated)
	if err != nil {
		t.Fatal(err)
	}
	requireCode(t, restarted.Admit(ctx, rotatedPrincipal, syncing.UploadLane), "device_rate_exceeded")
	for _, lane := range []syncing.AdmissionLane{syncing.ReadLane, syncing.WriteLane} {
		if err = f.s.Admit(ctx, f.p, lane); err != nil {
			t.Fatal("upload exhaustion blocked independent lane", err)
		}
	}
	meta, err := f.s.Metadata(ctx, f.p)
	if err != nil || meta.CommitRevision != 0 {
		t.Fatal("admission changed business revision", err)
	}
	for _, table := range []string{"sync_batches", "sync_pages", "health_samples", "entity_tombstones", "change_events", "dirty_scopes", "jobs"} {
		if count(t, f, table) != 0 {
			t.Fatal("admission touched", table)
		}
	}
	// Simulate an idle interval using only synthetic operational state. Idle time
	// restores a burst, never an unbounded accumulation of credits.
	if _, err = f.s.Pool.Exec(ctx, `UPDATE sync_request_budgets SET available_at=clock_timestamp()-interval '1 hour' WHERE lane='upload'`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = f.s.Admit(ctx, f.p, syncing.UploadLane); err != nil {
			t.Fatal(err)
		}
	}
	requireCode(t, f.s.Admit(ctx, f.p, syncing.UploadLane), "device_rate_exceeded")
	// Let one interval elapse naturally: one slot becomes available, not a full burst.
	time.Sleep(2100 * time.Millisecond)
	if err = f.s.Admit(ctx, f.p, syncing.UploadLane); err != nil {
		t.Fatal("continuous refill failed", err)
	}
	requireCode(t, f.s.Admit(ctx, f.p, syncing.UploadLane), "device_rate_exceeded")
}

func TestAdmissionSharedAcrossConcurrentPools(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	other, err := postgres.Open(ctx, f.s.Pool.Config().ConnString(), 8)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Pool.Close()
	start := make(chan struct{})
	results := make(chan error, 12)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			s := f.s
			if i%2 == 1 {
				s = other
			}
			results <- s.Admit(ctx, f.p, syncing.UploadLane)
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else {
			requireCode(t, err, "device_rate_exceeded")
		}
	}
	if accepted != 2 || count(t, f, "sync_request_budgets") != 2 {
		t.Fatal("concurrent admission exceeded burst", accepted)
	}
}

func enrolledDevice(t *testing.T, f *fixture) syncing.Principal {
	t.Helper()
	p := f.p
	p.DeviceID = syncing.NewID()
	// Fixed synthetic digest input, unique per generated synthetic identity.
	if _, err := f.s.Pool.Exec(context.Background(), `INSERT INTO devices(dataset_id,id,token_sha256,can_collect) VALUES($1,$2,sha256($3::bytea),true)`, p.DatasetID, p.DeviceID, []byte(p.DeviceID)); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAdmissionDatasetAggregateAndScopeIsolation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	devices := []syncing.Principal{f.p, enrolledDevice(t, f), enrolledDevice(t, f)}
	for _, p := range devices[:2] {
		for i := 0; i < 2; i++ {
			if err := f.s.Admit(ctx, p, syncing.UploadLane); err != nil {
				t.Fatal(err)
			}
		}
	}
	before := budgetTime(t, f, nil, syncing.UploadLane)
	requireCode(t, f.s.Admit(ctx, devices[2], syncing.UploadLane), "dataset_rate_exceeded")
	if count(t, f, "sync_request_budgets") != 3 || !budgetTime(t, f, nil, syncing.UploadLane).Equal(before) {
		t.Fatal("dataset denial changed buckets")
	}
	// A separate dataset gets its own dataset bucket even on the same service.
	p := syncing.Principal{DatasetID: syncing.NewID(), DeviceID: syncing.NewID(), CanCollect: true}
	if _, err := f.s.Pool.Exec(ctx, `INSERT INTO datasets(id,epoch,service_id) VALUES($1,$2,$3)`, p.DatasetID, syncing.NewID(), syncing.NewID()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(ctx, `INSERT INTO devices(dataset_id,id,token_sha256) VALUES($1,$2,sha256($3::bytea))`, p.DatasetID, p.DeviceID, []byte(p.DeviceID)); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Admit(ctx, p, syncing.UploadLane); err != nil {
		t.Fatal("foreign dataset quota leaked", err)
	}
	if err := f.s.Admit(ctx, f.p, syncing.AdmissionLane("path-controlled-key")); err == nil {
		t.Fatal("unbounded lane accepted")
	}
	if count(t, f, "sync_request_budgets") != 5 {
		t.Fatal("unknown lane wrote state")
	}
}

func TestAdmissionFailureAndCancellationRollBackBothBuckets(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	_, err := f.s.Pool.Exec(ctx, `CREATE FUNCTION fail_device_budget() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.device_id IS NOT NULL THEN RAISE EXCEPTION 'synthetic admission failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER fail_device_budget BEFORE INSERT OR UPDATE ON sync_request_budgets FOR EACH ROW EXECUTE FUNCTION fail_device_budget()`)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.Admit(ctx, f.p, syncing.UploadLane); err == nil {
		t.Fatal("injected failure ignored")
	}
	if count(t, f, "sync_request_budgets") != 0 {
		t.Fatal("partial dataset charge survived failure")
	}
	if _, err = f.s.Pool.Exec(ctx, `DROP TRIGGER fail_device_budget ON sync_request_budgets`); err != nil {
		t.Fatal(err)
	}
	if err = f.s.Admit(ctx, f.p, syncing.UploadLane); err != nil {
		t.Fatal(err)
	}
	before := budgetTime(t, f, nil, syncing.UploadLane)
	tx, err := f.s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT available_at FROM sync_request_budgets WHERE device_id IS NULL FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	if err = f.s.Admit(short, f.p, syncing.UploadLane); err == nil {
		t.Fatal("cancelled admission succeeded")
	}
	if !budgetTime(t, f, nil, syncing.UploadLane).Equal(before) {
		t.Fatal("cancelled admission changed debt")
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err = f.s.Admit(ctx, f.p, syncing.UploadLane); err != nil {
		t.Fatal("retry after cancellation failed", err)
	}
}

type observedBody struct{ reads int }

func (b *observedBody) Read(p []byte) (int, error) { b.reads++; return 0, io.EOF }

type heldBody struct {
	*bytes.Reader
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *heldBody) Read(p []byte) (int, error) {
	b.once.Do(func() { close(b.started); <-b.release })
	return b.Reader.Read(p)
}

func TestHTTPActiveDeviceUploadRejectsBeforeReadingAndReleases(t *testing.T) {
	f := setup(t)
	e, wire := f.envelope(t, []syncing.Event{sample(syncing.NewID(), 132)}, 1, nil)
	if _, err := f.s.RegisterBatch(context.Background(), f.p, e); err != nil {
		t.Fatal(err)
	}
	h := httpapi.New(f.s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	path := "/v2/sync/batches/" + e.BatchID + "/body"
	held := &heldBody{Reader: bytes.NewReader(wire), started: make(chan struct{}), release: make(chan struct{})}
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		result <- admissionRequest(h, f.enrollment.Token, "PUT", path, "application/x-rai-health-delta+ndjson", "gzip", held)
	}()
	var release sync.Once
	unblock := func() { release.Do(func() { close(held.release) }) }
	defer unblock()
	select {
	case <-held.started:
	case <-time.After(3 * time.Second):
		t.Fatal("upload did not reach parser")
	}
	unread := &observedBody{}
	assert429(t, admissionRequest(h, f.enrollment.Token, "PUT", path, "application/x-rai-health-delta+ndjson", "gzip", unread), "device_upload_capacity_exceeded")
	if unread.reads != 0 {
		t.Fatal("concurrent device body was parsed")
	}
	unblock()
	var first *httptest.ResponseRecorder
	select {
	case first = <-result:
	case <-time.After(3 * time.Second):
		t.Fatal("upload did not complete")
	}
	if first.Code != 200 {
		t.Fatal(first.Code, first.Body.String())
	}
	// Only the synthetic test changes the clock debt; a normal client must wait.
	if _, err := f.s.Pool.Exec(context.Background(), `UPDATE sync_request_budgets SET available_at=clock_timestamp()-interval '1 minute' WHERE lane='upload'`); err != nil {
		t.Fatal(err)
	}
	retry := admissionRequest(h, f.enrollment.Token, "PUT", path, "application/x-rai-health-delta+ndjson", "gzip", bytes.NewReader(wire))
	if retry.Code != 200 || !bytes.Equal(retry.Body.Bytes(), first.Body.Bytes()) {
		t.Fatal("slot leak or changed receipt after upload", retry.Code, retry.Body.String())
	}
}

func admissionRequest(h http.Handler, token, method, path, media, encoding string, body io.Reader) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, body)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if media != "" {
		r.Header.Set("Content-Type", media)
	}
	if encoding != "" {
		r.Header.Set("Content-Encoding", encoding)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func assert429(t *testing.T, w *httptest.ResponseRecorder, code string) {
	t.Helper()
	var e syncing.Error
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil || w.Code != 429 || e.Code != code {
		t.Fatal("wrong throttle response", w.Code, w.Body.String(), err)
	}
	seconds, err := strconv.Atoi(w.Header().Get("Retry-After"))
	if err != nil || seconds < 1 || e.RetryAfterSeconds != seconds {
		t.Fatal("missing or inconsistent retry delay", w.Header(), e)
	}
}

func TestHTTPAdmissionBeforeBodyAndReceiptRecovery(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	e, _, receipt := f.commit(t, []syncing.Event{sample(syncing.NewID(), 132)}, 1, nil)
	h := httpapi.New(f.s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	path := "/v2/sync/batches/" + e.BatchID + "/body"
	// Invalid attempts consume quota even when content headers fail validation.
	for i := 0; i < 2; i++ {
		w := admissionRequest(h, f.enrollment.Token, "PUT", path, "application/json", "", nil)
		if w.Code != 415 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	unread := &observedBody{}
	w := admissionRequest(h, f.enrollment.Token, "PUT", path, "application/x-rai-health-delta+ndjson", "gzip", unread)
	assert429(t, w, "device_rate_exceeded")
	if unread.reads != 0 {
		t.Fatal("throttled upload body was parsed")
	}
	// Response-loss reconciliation uses a distinct read budget.
	w = admissionRequest(h, f.enrollment.Token, "GET", "/v2/sync/batches/"+e.BatchID, "", "", nil)
	var state syncing.BatchState
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil || w.Code != 200 || !bytes.Equal(state.Receipt, receipt) {
		t.Fatal("upload throttle blocked batch recovery", w.Code, err)
	}
	w = admissionRequest(h, f.enrollment.Token, "GET", "/v2/sync/pages/"+e.PageID, "", "", nil)
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"ingestStatus":"sealed"`)) {
		t.Fatal("page recovery blocked", w.Code)
	}
	// Registration and reads each honor their own lane, before JSON parsing.
	if _, err := f.s.Pool.Exec(ctx, `INSERT INTO sync_request_budgets(dataset_id,device_id,lane,available_at) VALUES($1,$2,'write',clock_timestamp()+interval '70 seconds')`, f.p.DatasetID, f.p.DeviceID); err != nil {
		t.Fatal(err)
	}
	unread = &observedBody{}
	assert429(t, admissionRequest(h, f.enrollment.Token, "POST", "/v2/sync/batches", "application/json", "", unread), "device_rate_exceeded")
	if unread.reads != 0 {
		t.Fatal("throttled JSON was parsed")
	}
	if _, err := f.s.Pool.Exec(ctx, `UPDATE sync_request_budgets SET available_at=clock_timestamp()+interval '70 seconds' WHERE lane='read' AND device_id IS NOT NULL`); err != nil {
		t.Fatal(err)
	}
	assert429(t, admissionRequest(h, f.enrollment.Token, "GET", "/v2/sync/streams/"+f.stream.StreamID, "", "", nil), "device_rate_exceeded")
	if count(t, f, "health_samples") != 1 || count(t, f, "sync_batches") != 1 {
		t.Fatal("throttled requests changed facts")
	}
	// Invalid/revoked credentials and public health checks cannot create buckets.
	before := count(t, f, "sync_request_budgets")
	for _, token := range []string{"", strings.Repeat("invalid-synthetic-", 4)} {
		if w = admissionRequest(h, token, "GET", "/v2/handshake", "", "", nil); w.Code != 401 {
			t.Fatal(w.Code)
		}
	}
	if w = admissionRequest(h, "", "GET", "/livez", "", "", nil); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if _, err := f.s.Pool.Exec(ctx, `UPDATE devices SET can_collect=false WHERE id=$1`, f.p.DeviceID); err != nil {
		t.Fatal(err)
	}
	if w = admissionRequest(h, f.enrollment.Token, "PUT", path, "", "", nil); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if _, err := f.s.Pool.Exec(ctx, `UPDATE devices SET revoked_at=now() WHERE id=$1`, f.p.DeviceID); err != nil {
		t.Fatal(err)
	}
	if w = admissionRequest(h, f.enrollment.Token, "GET", "/v2/handshake", "", "", nil); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if count(t, f, "sync_request_budgets") != before {
		t.Fatal("unauthorized request allocated buckets")
	}
}

func TestSchema4UpgradeKeepsReceiptsAndStartsWithEmptyBudgets(t *testing.T) {
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
		_, err = provider.UpTo(ctx, 4)
		return err
	})
	ctx := context.Background()
	fact := sample(syncing.NewID(), 132)
	e, _, original := f.commit(t, []syncing.Event{fact}, 1, nil)
	pageBefore := pageState(t, f, e.PageID, 1, "sealed")
	if err := f.s.Ready(ctx); err == nil {
		t.Fatal("schema 4 considered ready by schema 9 binary")
	}
	if err := admin.Migrate(ctx, f.s.Pool.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if count(t, f, "sync_request_budgets") != 0 {
		t.Fatal("migration invented admission history")
	}
	if err := f.s.Admit(ctx, f.p, syncing.UploadLane); err != nil {
		t.Fatal(err)
	}
	retry, err := f.s.Commit(ctx, f.p, e, []syncing.Event{fact})
	// Committed retry must return before reapplying any events.
	if err != nil || !bytes.Equal(retry, original) {
		t.Fatal("migration altered batch receipt", err)
	}
	pageAfter := pageState(t, f, e.PageID, 1, "sealed")
	if !bytes.Equal(pageBefore.Receipt, pageAfter.Receipt) {
		t.Fatal("migration altered page receipt")
	}
	if err = admin.Migrate(ctx, f.s.Pool.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
	if count(t, f, "sync_request_budgets") != 2 {
		t.Fatal("migration replay reset quotas")
	}
}
