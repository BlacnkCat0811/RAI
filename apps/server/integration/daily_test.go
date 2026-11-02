//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/httpapi"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
)

func dailyBatch(f *fixture, kind string) syncing.DailyBatch {
	now := time.Now().UTC().UnixMicro()
	return syncing.DailyBatch{Version: 1, DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch,
		StreamID: syncing.NewID(), BatchID: syncing.NewID(), ClientSeq: 1, SampleType: kind, BootstrapStartUs: now - 90*86400000000, ObservedAtUs: now, Facts: []syncing.DailyFact{}, DeletedUUIDs: []string{}}
}
func dailyFact(kind string) syncing.DailyFact {
	now := time.Now().UTC().UnixMicro()
	fact := syncing.DailyFact{UUID: syncing.NewID(), StartUs: now - 3600000000, EndUs: now, Source: syncing.HealthKitSource{BundleID: "synthetic.thirdparty", Version: "1.2", ProductType: "Watch9,1", OperatingSystemVersion: "26.0", Device: &syncing.HealthKitDevice{Manufacturer: "Synthetic", Model: "Fixture"}}, Metadata: map[string]any{"HKTimeZone": "Asia/Shanghai", "HKAlgorithmVersion": int64(2), "HKWasUserEntered": true}}
	switch kind {
	case syncing.DailySleep:
		stage := 3
		fact.Category = &stage
	case syncing.DailyHRV:
		fact.Quantity = &syncing.Quantity{Value: 42, Unit: "ms"}
	default:
		fact.Quantity = &syncing.Quantity{Value: 58, Unit: "count/min"}
	}
	return fact
}
func nextDaily(b syncing.DailyBatch) syncing.DailyBatch {
	prev := b.BatchID
	b.PreviousBatchID = &prev
	b.BatchID = syncing.NewID()
	b.ClientSeq++
	b.Facts = []syncing.DailyFact{}
	b.DeletedUUIDs = []string{}
	return b
}
func rawDaily(b syncing.DailyBatch) []byte {
	b.QueryFingerprint = syncing.DailyFingerprint(b.SampleType, b.BootstrapStartUs)
	raw, _ := json.Marshal(b)
	return raw
}
func commitDaily(t *testing.T, f *fixture, b syncing.DailyBatch) ([]byte, error) {
	t.Helper()
	return commitDailyObserved(t, f, b, nil)
}
func commitDailyObserved(t *testing.T, f *fixture, b syncing.DailyBatch, observe func(string, time.Duration)) ([]byte, error) {
	t.Helper()
	ctx := context.Background()
	if observe != nil {
		ctx = syncing.ObservePhases(ctx, observe)
	}
	lease, err := f.s.AcquireUpload(ctx, f.p)
	if err != nil {
		return nil, err
	}
	defer f.s.ReleaseUpload(ctx, f.p, lease)
	return f.s.CommitDaily(ctx, f.p, rawDaily(b), lease)
}
func requireDaily(t *testing.T, f *fixture, b syncing.DailyBatch) syncing.DailyReceipt {
	t.Helper()
	raw, err := commitDaily(t, f, b)
	if err != nil {
		t.Fatal(err)
	}
	var r syncing.DailyReceipt
	if err = json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r
}
func TestDailyLifecycle(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	for _, kind := range syncing.DailyTypes {
		t.Run(kind, func(t *testing.T) {
			b := dailyBatch(f, kind)
			fact := dailyFact(kind)
			b.Facts = []syncing.DailyFact{fact}
			b.HasMore = true
			r := requireDaily(t, f, b)
			if r.Counts.Inserted != 1 {
				t.Fatal(r.Counts)
			}
			first, err := commitDaily(t, f, b)
			if err != nil {
				t.Fatal(err)
			}
			second, err := commitDaily(t, f, b)
			if err != nil || !bytes.Equal(first, second) {
				t.Fatal("retry did not return immutable receipt")
			}
			duplicate := nextDaily(b)
			duplicate.Facts = []syncing.DailyFact{fact}
			r = requireDaily(t, f, duplicate)
			if r.Counts.Unchanged != 1 {
				t.Fatal(r.Counts)
			}
			conflict := nextDaily(duplicate)
			modified := fact
			modified.Source.Version = "different"
			conflict.Facts = []syncing.DailyFact{modified}
			if _, err = commitDaily(t, f, conflict); err == nil {
				t.Fatal("source mutation overwrote immutable identity")
			}
			del := nextDaily(duplicate)
			del.DeletedUUIDs = []string{fact.UUID}
			r = requireDaily(t, f, del)
			if r.Counts.Deleted != 1 {
				t.Fatal(r.Counts)
			}
			// Original pre-delete batch retry still gets its original ACK, without writes.
			if old, err := commitDaily(t, f, b); err != nil || !bytes.Equal(old, first) {
				t.Fatal("old ACK changed")
			}
			late := nextDaily(del)
			late.Facts = []syncing.DailyFact{fact}
			r = requireDaily(t, f, late)
			if r.Counts.Suppressed != 1 || r.DeleteStatus != "deleted_precedence" {
				t.Fatal(r)
			}
			unknown := syncing.NewID()
			before := nextDaily(late)
			before.DeletedUUIDs = []string{unknown}
			r = requireDaily(t, f, before)
			if r.Counts.Tombstoned != 1 {
				t.Fatal(r.Counts)
			}
			blocked := nextDaily(before)
			fact.UUID = unknown
			blocked.Facts = []syncing.DailyFact{fact}
			r = requireDaily(t, f, blocked)
			if r.Counts.Suppressed != 1 {
				t.Fatal(r.Counts)
			}
			var count int
			if err = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM health_samples WHERE dataset_id=$1 AND sample_type=$2`, f.p.DatasetID, kind).Scan(&count); err != nil || count != 0 {
				t.Fatal("fact resurrected")
			}
		})
	}
	var jobs int
	_ = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE dataset_id=$1`, f.p.DatasetID).Scan(&jobs)
	if jobs != 0 {
		t.Fatal("fictional analysis jobs", jobs)
	}
	var dirty int
	_ = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM dirty_scopes WHERE dataset_id=$1 AND scope_key LIKE 'daily:%' AND generation=2 AND clean_generation=0`, f.p.DatasetID).Scan(&dirty)
	if dirty != 3 {
		t.Fatal("delete invalidation", dirty)
	}
}
func TestDailyRHRReplacementAndSleepOverlap(t *testing.T) {
	f := setup(t)
	b := dailyBatch(f, syncing.DailyRHR)
	old := dailyFact(b.SampleType)
	b.Facts = []syncing.DailyFact{old}
	requireDaily(t, f, b)
	replacement := nextDaily(b)
	newFact := old
	newFact.UUID = syncing.NewID()
	newFact.Quantity = &syncing.Quantity{Value: 56, Unit: "count/min"}
	replacement.Facts = []syncing.DailyFact{newFact}
	replacement.DeletedUUIDs = []string{old.UUID}
	r := requireDaily(t, f, replacement)
	if r.Counts.Inserted != 1 || r.Counts.Deleted != 1 {
		t.Fatal(r.Counts)
	}
	sleep := dailyBatch(f, syncing.DailySleep)
	for stage := 0; stage <= 5; stage++ {
		fact := dailyFact(sleep.SampleType)
		v := stage
		fact.Category = &v
		fact.Source.BundleID = fmt.Sprintf("synthetic.source.%d", stage)
		sleep.Facts = append(sleep.Facts, fact)
	}
	if r = requireDaily(t, f, sleep); r.Counts.Inserted != 6 {
		t.Fatal(r.Counts)
	}
	var preserved bool
	err := f.s.Pool.QueryRow(context.Background(), `SELECT bool_and(metadata ? 'RAI:dailySource' AND metadata->'RAI:dailySource'->>'version'='1.2') FROM health_samples WHERE dataset_id=$1`, f.p.DatasetID).Scan(&preserved)
	if err != nil || !preserved {
		t.Fatal("provenance missing", err)
	}
	// Same values and timestamps are distinct when UUIDs differ.
	another := nextDaily(replacement)
	newFact.UUID = syncing.NewID()
	another.Facts = []syncing.DailyFact{newFact}
	if r = requireDaily(t, f, another); r.Counts.Inserted != 1 {
		t.Fatal(r.Counts)
	}
}
func TestDailyCoverageEmptyDeletedIncompleteAndFencing(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	coverage, err := f.s.DailyCoverage(ctx, f.p)
	if err != nil || len(coverage) != 3 {
		t.Fatal(err)
	}
	for _, c := range coverage {
		if c.State != "sync_not_completed" || c.QueryCompleted {
			t.Fatal(c)
		}
	}
	b := dailyBatch(f, syncing.DailyHRV)
	requireDaily(t, f, b)
	coverage, _ = f.s.DailyCoverage(ctx, f.p)
	if coverage[0].State != "no_sample_observed" || !coverage[0].BootstrapComplete {
		t.Fatal(coverage[0])
	}
	b = nextDaily(b)
	b.Facts = []syncing.DailyFact{dailyFact(b.SampleType)}
	b.HasMore = true
	requireDaily(t, f, b)
	coverage, _ = f.s.DailyCoverage(ctx, f.p)
	if coverage[0].State != "sync_not_completed" || !coverage[0].HasLiveFacts {
		t.Fatal(coverage[0])
	}
	del := nextDaily(b)
	del.HasMore = false
	del.DeletedUUIDs = []string{b.Facts[0].UUID}
	requireDaily(t, f, del)
	coverage, _ = f.s.DailyCoverage(ctx, f.p)
	if coverage[0].State != "sample_deleted" || coverage[0].HasLiveFacts {
		t.Fatal(coverage[0])
	}
	stale := nextDaily(del)
	stale.ClientSeq++
	if _, err = commitDaily(t, f, stale); err == nil {
		t.Fatal("sequence gap accepted")
	}
	epoch := nextDaily(del)
	epoch.DatasetEpoch = syncing.NewID()
	if _, err = commitDaily(t, f, epoch); err == nil {
		t.Fatal("generation accepted")
	} else {
		var e *syncing.Error
		if !errors.As(err, &e) || e.Code != "generation_conflict" {
			t.Fatal(err)
		}
	}
	fenced := nextDaily(del)
	fenced.CollectorEpoch = syncing.NewID()
	if _, err = commitDaily(t, f, fenced); err == nil {
		t.Fatal("fenced collector accepted")
	}
	// A valid old response cannot be delivered through an expired operational lease.
	if _, err = f.s.CommitDaily(ctx, f.p, rawDaily(del), syncing.UploadLease{Token: syncing.NewID()}); err == nil {
		t.Fatal("lost upload lease accepted")
	}
}
func TestDailyRollbackAndBackendCrash(t *testing.T) {
	for _, target := range []string{"health_samples", "entity_tombstones", "entity_changes", "dirty_scopes", "daily_health_coverage", "change_events", "sync_batches"} {
		t.Run(target, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			b := dailyBatch(f, syncing.DailyHRV)
			b.Facts = []syncing.DailyFact{dailyFact(b.SampleType)}
			b.DeletedUUIDs = []string{syncing.NewID()}
			operation := "INSERT OR UPDATE"
			if target == "sync_batches" {
				operation = "UPDATE"
			} // fail final receipt after all fact/coverage writes
			sql := `CREATE FUNCTION daily_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic_failure'; END $$; CREATE TRIGGER daily_fault BEFORE ` + operation + ` ON ` + target + ` FOR EACH ROW EXECUTE FUNCTION daily_fault()`
			if _, err := f.s.Pool.Exec(ctx, sql); err != nil {
				t.Fatal(err)
			}
			if _, err := commitDaily(t, f, b); err == nil {
				t.Fatal("fault ignored")
			}
			for _, table := range []string{"health_samples", "entity_tombstones", "sync_batches", "entity_changes", "change_events", "daily_health_coverage", "dirty_scopes"} {
				var n int
				if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE dataset_id=$1`, f.p.DatasetID).Scan(&n); err != nil || n != 0 {
					t.Fatal("partial transaction", table, n, err)
				}
			}
			if _, err := f.s.Pool.Exec(ctx, `DROP TRIGGER daily_fault ON `+target+`; DROP FUNCTION daily_fault()`); err != nil {
				t.Fatal(err)
			}
			requireDaily(t, f, b)
		})
	}
	t.Run("backend_terminated_after_facts", func(t *testing.T) {
		f := setup(t)
		ctx := context.Background()
		b := dailyBatch(f, syncing.DailyHRV)
		b.Facts = []syncing.DailyFact{dailyFact(b.SampleType)}
		_, err := f.s.Pool.Exec(ctx, `CREATE FUNCTION daily_crash() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_terminate_backend(pg_backend_pid()); RETURN NEW; END $$; CREATE TRIGGER daily_crash BEFORE INSERT ON daily_health_coverage FOR EACH ROW EXECUTE FUNCTION daily_crash()`)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = commitDaily(t, f, b); err == nil {
			t.Fatal("backend was not terminated")
		}
		var n int
		if err = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM health_samples`).Scan(&n); err != nil || n != 0 {
			t.Fatal("uncommitted facts survived", err)
		}
		_, err = f.s.Pool.Exec(ctx, `DROP TRIGGER daily_crash ON daily_health_coverage; DROP FUNCTION daily_crash()`)
		if err != nil {
			t.Fatal(err)
		}
		requireDaily(t, f, b)
	})
}

func TestDailySameBatchDeleteWins(t *testing.T) {
	f := setup(t)
	b := dailyBatch(f, syncing.DailyHRV)
	fact := dailyFact(b.SampleType)
	b.Facts = []syncing.DailyFact{fact, fact}
	b.DeletedUUIDs = []string{fact.UUID}
	r := requireDaily(t, f, b)
	if r.Counts.Inserted != 0 || r.Counts.Tombstoned != 1 {
		t.Fatal(r.Counts)
	}
	late := nextDaily(b)
	late.Facts = []syncing.DailyFact{fact}
	r = requireDaily(t, f, late)
	if r.Counts.Suppressed != 1 {
		t.Fatal("same-page delete did not fence retry", r.Counts)
	}
	mutated := b
	mutated.Facts = []syncing.DailyFact{}
	if _, err := commitDaily(t, f, mutated); err == nil {
		t.Fatal("batch identity reused for different bytes")
	}
}
func TestDailyHTTPRetryAndStrictErrors(t *testing.T) {
	f := setup(t)
	handler := httpapi.New(f.s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	b := dailyBatch(f, syncing.DailySleep)
	b.Facts = []syncing.DailyFact{dailyFact(b.SampleType)}
	send := func(raw []byte) *httptest.ResponseRecorder {
		// Each assertion gets a fresh synthetic admission budget; the shared
		// production rate-limit behavior is covered by admission tests.
		if _, err := f.s.Pool.Exec(context.Background(), `DELETE FROM sync_request_budgets WHERE dataset_id=$1`, f.p.DatasetID); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/v2/sync/daily-facts", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	raw := rawDaily(b)
	first := send(raw)
	retry := send(raw)
	if first.Code != 200 || retry.Code != 200 || first.Body.String() != retry.Body.String() {
		t.Fatal(first.Code, retry.Code)
	}
	for _, tc := range []struct {
		name, code string
		mutate     func(*syncing.DailyBatch)
	}{
		{"category", "invalid_category", func(b *syncing.DailyBatch) { v := 99; b.Facts[0].Category = &v }},
		{"source", "source_invalid", func(b *syncing.DailyBatch) { b.Facts[0].Source.BundleID = "" }},
		{"type", "unsupported_type", func(b *syncing.DailyBatch) { b.SampleType = "unknown" }},
		{"unit", "unsupported_unit", func(b *syncing.DailyBatch) {
			b.SampleType = syncing.DailyHRV
			b.Facts[0].Category = nil
			b.Facts[0].Quantity = &syncing.Quantity{Value: 2, Unit: "s"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := dailyBatch(f, b.SampleType)
			candidate.Facts = []syncing.DailyFact{dailyFact(b.SampleType)}
			tc.mutate(&candidate)
			w := send(rawDaily(candidate))
			var v map[string]any
			_ = json.Unmarshal(w.Body.Bytes(), &v)
			if w.Code != 422 || v["code"] != tc.code {
				t.Fatal(w.Code, v["code"])
			}
		})
	}
	if w := send(bytes.Replace(raw, []byte(`"hasMore":false`), []byte(`"hasMore":false,"permissionGranted":true`), 1)); w.Code != 422 {
		t.Fatal("unknown field accepted", w.Code)
	}
	if w := send(make([]byte, syncing.MaxDailyBytes+1)); w.Code != 413 {
		t.Fatal("body limit missing", w.Code)
	}
	if _, err := f.s.Pool.Exec(context.Background(), `CREATE FUNCTION daily_http_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic_private_diagnostic'; END $$; CREATE TRIGGER daily_http_fault BEFORE INSERT ON daily_health_coverage FOR EACH ROW EXECUTE FUNCTION daily_http_fault()`); err != nil {
		t.Fatal(err)
	}
	fault := dailyBatch(f, syncing.DailyHRV)
	fault.Facts = []syncing.DailyFact{dailyFact(fault.SampleType)}
	w := send(rawDaily(fault))
	var failure map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &failure)
	if w.Code != 500 || failure["code"] != "internal_error" || bytes.Contains(w.Body.Bytes(), []byte("synthetic_private")) {
		t.Fatal("internal failure classification", w.Code)
	}
}

func TestDailySyntheticPerformance(t *testing.T) {
	f := setup(t)
	for _, days := range []int{7, 30, 90, 180} {
		t.Run(fmt.Sprint(days), func(t *testing.T) {
			b := dailyBatch(f, syncing.DailySleep)
			b.BootstrapStartUs = b.ObservedAtUs - int64(days)*86400000000
			// Different predicates isolate the four synthetic histories in one dataset.
			perDay := 32
			total := days * perDay
			facts := make([]syncing.DailyFact, 0, total)
			fixtureStart := time.Now()
			for i := 0; i < total; i++ {
				fact := dailyFact(b.SampleType)
				fact.StartUs = b.BootstrapStartUs + int64(i)*86400000000/int64(perDay)
				fact.EndUs = fact.StartUs + 1800000000
				facts = append(facts, fact)
			}
			fixtureTime := time.Since(fixtureStart)
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			begin := time.Now()
			var payload int
			var parse, transaction, pgTransaction time.Duration
			batches := 0
			for start := 0; start < total; start += syncing.MaxDailyChanges {
				end := min(start+syncing.MaxDailyChanges, total)
				b.Facts = facts[start:end]
				b.HasMore = end < total
				raw := rawDaily(b)
				payload += len(raw)
				tick := time.Now()
				if _, err := syncing.DecodeDailyBatch(raw); err != nil {
					t.Fatal(err)
				}
				parse += time.Since(tick)
				tick = time.Now()
				if _, err := commitDailyObserved(t, f, b, func(name string, elapsed time.Duration) {
					if name == "daily_transaction" {
						pgTransaction += elapsed
					}
				}); err != nil {
					t.Fatal(err)
				}
				transaction += time.Since(tick)
				batches++
				b = nextDaily(b)
			}
			elapsed := time.Since(begin)
			runtime.ReadMemStats(&after)
			hot := dailyFact(b.SampleType)
			b.Facts = []syncing.DailyFact{hot}
			tick := time.Now()
			requireDaily(t, f, b)
			hotTime := time.Since(tick)
			t.Logf("M5A_PERF days=%d facts=%d fixture_ms=%.2f batches=%d payload_bytes=%d parse_ms=%.2f pg_tx_ms=%.2f pg_transport_ms=%.2f bootstrap_ms=%.2f alloc_bytes=%d heap_bytes=%d hot_ms=%.2f", days, total, float64(fixtureTime.Microseconds())/1000, batches, payload, float64(parse.Microseconds())/1000, float64(pgTransaction.Microseconds())/1000, float64(transaction.Microseconds())/1000, float64(elapsed.Microseconds())/1000, after.TotalAlloc-before.TotalAlloc, after.HeapAlloc, float64(hotTime.Microseconds())/1000)
		})
	}
}

func TestDailyBatchBudgetComparison(t *testing.T) {
	f := setup(t)
	for _, size := range []int{32, 64, 128} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			b := dailyBatch(f, syncing.DailySleep)
			b.BootstrapStartUs -= int64(size) * 1000000
			const total = 1024
			begin := time.Now()
			var maxTxn, txn time.Duration
			var bytesTotal, maxBytes int
			for i := 0; i < total; i += size {
				for j := 0; j < size; j++ {
					b.Facts = append(b.Facts, dailyFact(b.SampleType))
				}
				b.HasMore = i+size < total
				raw := rawDaily(b)
				bytesTotal += len(raw)
				maxBytes = max(maxBytes, len(raw))
				lease, err := f.s.AcquireUpload(context.Background(), f.p)
				if err != nil {
					t.Fatal(err)
				}
				ctx := syncing.ObservePhases(context.Background(), func(name string, d time.Duration) {
					if name == "daily_transaction" {
						txn += d
						if d > maxTxn {
							maxTxn = d
						}
					}
				})
				_, err = f.s.CommitDaily(ctx, f.p, raw, lease)
				_ = f.s.ReleaseUpload(context.Background(), f.p, lease)
				if err != nil {
					t.Fatal(err)
				}
				b = nextDaily(b)
			}
			t.Logf("M5A_BATCH_LIMIT size=%d batches=%d bytes=%d max_payload=%d total_ms=%.2f pg_tx_ms=%.2f max_pg_tx_ms=%.2f", size, total/size, bytesTotal, maxBytes, float64(time.Since(begin).Microseconds())/1000, float64(txn.Microseconds())/1000, float64(maxTxn.Microseconds())/1000)
		})
	}
}

func TestDailyRestrictedAPIRole(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	suffix := strings.ReplaceAll(syncing.NewID(), "-", "")
	apiRole, workerRole := "rai_api_"+suffix, "rai_worker_"+suffix
	grants, err := os.ReadFile("../../../deploy/postgres/roles.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.s.Pool.Exec(ctx, strings.ReplaceAll(strings.ReplaceAll(string(grants), "rai_api", apiRole), "rai_worker", workerRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := f.s.Pool.Exec(context.Background(), "DROP OWNED BY "+pgx.Identifier{apiRole}.Sanitize()+", "+pgx.Identifier{workerRole}.Sanitize()+"; DROP ROLE "+pgx.Identifier{apiRole}.Sanitize()+", "+pgx.Identifier{workerRole}.Sanitize())
		if err != nil {
			t.Error(err)
		}
	})
	u, err := url.Parse(f.s.Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("options", "-crole="+apiRole)
	u.RawQuery = q.Encode()
	restricted, err := postgres.Open(ctx, u.String(), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer restricted.Pool.Close()
	if err = restricted.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	restrictedFixture := *f
	restrictedFixture.s = restricted
	b := dailyBatch(f, syncing.DailyRHR)
	fact := dailyFact(b.SampleType)
	b.Facts = []syncing.DailyFact{fact}
	requireDaily(t, &restrictedFixture, b)
	del := nextDaily(b)
	del.DeletedUUIDs = []string{fact.UUID}
	requireDaily(t, &restrictedFixture, del)
	coverage, err := restricted.DailyCoverage(ctx, f.p)
	if err != nil || coverage[1].State != "sample_deleted" {
		t.Fatal("API role cannot read coverage", err)
	}
}
