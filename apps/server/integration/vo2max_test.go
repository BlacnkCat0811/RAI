//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/httpapi"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
)

func vo2Batch(t *testing.T, f *fixture) syncing.VO2Batch {
	t.Helper()
	raw, err := os.ReadFile("../../../contracts/fixtures/vo2max-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var b syncing.VO2Batch
	if err = json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	b.DatasetID = f.p.DatasetID
	b.DatasetEpoch = f.enrollment.DatasetEpoch
	b.CollectorEpoch = f.enrollment.CollectorEpoch
	b.StreamID = syncing.NewID()
	b.BatchID = syncing.NewID()
	b.Facts[0].UUID = syncing.NewID()
	return b
}
func nextVO2(b syncing.VO2Batch) syncing.VO2Batch {
	prev := b.BatchID
	b.PreviousBatchID = &prev
	b.ClientSeq++
	b.BatchID = syncing.NewID()
	b.Facts = []syncing.VO2Fact{}
	b.DeletedUUIDs = []string{}
	return b
}
func commitVO2(t *testing.T, f *fixture, b syncing.VO2Batch) ([]byte, error) {
	t.Helper()
	ctx := context.Background()
	lease, err := f.s.AcquireUpload(ctx, f.p)
	if err != nil {
		return nil, err
	}
	defer f.s.ReleaseUpload(ctx, f.p, lease)
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return f.s.CommitVO2(ctx, f.p, raw, lease)
}
func requireVO2(t *testing.T, f *fixture, b syncing.VO2Batch) syncing.VO2Receipt {
	t.Helper()
	raw, err := commitVO2(t, f, b)
	if err != nil {
		t.Fatal(err)
	}
	var r syncing.VO2Receipt
	if json.Unmarshal(raw, &r) != nil {
		t.Fatal("receipt decode")
	}
	return r
}
func TestVO2LifecycleIdentityDeletesCoverage(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := vo2Batch(t, f)
	c, err := f.s.VO2Coverage(ctx, f.p)
	if err != nil || c.QueryCompleted || c.HasMore != nil || c.BootstrapStart != nil || c.ReadAuthorization != "not_determinable" {
		t.Fatal("initial coverage", err)
	}
	fact := b.Facts[0]
	b.HasMore = true
	r := requireVO2(t, f, b)
	if r.Counts.Inserted != 1 || r.AnalysisStatus != "awaiting_model_implementation" {
		t.Fatal("wrong commit")
	}
	prior, err := commitVO2(t, f, b)
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := commitVO2(t, f, b)
	if err != nil || !bytes.Equal(prior, repeat) {
		t.Fatal("lost ACK identity")
	}
	c, err = f.s.VO2Coverage(ctx, f.p)
	if err != nil || c.State != "sync_incomplete" || c.BootstrapComplete || c.StreamSequence != 1 {
		t.Fatal("incomplete coverage", err)
	}
	same := nextVO2(b)
	same.Facts = []syncing.VO2Fact{fact}
	same.HasMore = false
	if requireVO2(t, f, same).Counts.Unchanged != 1 {
		t.Fatal("same identity")
	}
	for _, field := range []string{"value", "time", "source", "metadata", "unit"} {
		t.Run(field, func(t *testing.T) {
			var m syncing.VO2Fact
			raw, _ := json.Marshal(fact)
			_ = json.Unmarshal(raw, &m)
			switch field {
			case "value":
				m.Quantity.Value++
			case "time":
				m.EndUs++
			case "source":
				v := "different"
				m.Provenance.SourceRevision.Version = &v
			case "metadata":
				m.Metadata["HKAlgorithmVersion"] = syncing.VO2Metadata{Type: "int64", Value: json.RawMessage(`3`)}
			case "unit":
				m.Quantity.Unit = "L/min"
			}
			bad := nextVO2(same)
			bad.Facts = []syncing.VO2Fact{m}
			if _, err := commitVO2(t, f, bad); err == nil {
				t.Fatal("mutable identity")
			}
		})
	}
	del := nextVO2(same)
	del.DeletedUUIDs = []string{fact.UUID}
	if requireVO2(t, f, del).Counts.Deleted != 1 {
		t.Fatal("known delete")
	}
	var snapshot []byte
	if err = f.s.Pool.QueryRow(ctx, `SELECT provenance_snapshot FROM entity_tombstones WHERE dataset_id=$1 AND sample_uuid=$2`, f.p.DatasetID, fact.UUID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(snapshot, []byte("originInputs")) || bytes.Contains(snapshot, []byte("42.123")) || bytes.Contains(snapshot, []byte("HKVO2MaxValue")) {
		t.Fatal("withdrawal context missing or value copied")
	}
	retry, err := commitVO2(t, f, b)
	if err != nil || !bytes.Equal(prior, retry) {
		t.Fatal("old receipt changed")
	}
	stale := nextVO2(del)
	stale.Facts = []syncing.VO2Fact{fact}
	r = requireVO2(t, f, stale)
	if r.Counts.Suppressed != 1 || r.DeleteStatus != "deleted_precedence" {
		t.Fatal("resurrection")
	}
	unknown := syncing.NewID()
	before := nextVO2(stale)
	before.DeletedUUIDs = []string{unknown}
	if requireVO2(t, f, before).Counts.Tombstoned != 1 {
		t.Fatal("unknown delete")
	}
	var unknownContext []byte
	if err = f.s.Pool.QueryRow(ctx, `SELECT provenance_snapshot FROM entity_tombstones WHERE sample_uuid=$1`, unknown).Scan(&unknownContext); err != nil || unknownContext != nil {
		t.Fatal("invented source", err)
	}
	after := nextVO2(before)
	fact.UUID = unknown
	after.Facts = []syncing.VO2Fact{fact}
	if requireVO2(t, f, after).Counts.Suppressed != 1 {
		t.Fatal("delete before create")
	}
	both := nextVO2(after)
	fact.UUID = syncing.NewID()
	both.Facts = []syncing.VO2Fact{fact, fact}
	both.DeletedUUIDs = []string{fact.UUID}
	r = requireVO2(t, f, both)
	if r.Counts.Inserted != 0 || r.Counts.Tombstoned != 1 {
		t.Fatal("same batch delete")
	}
	c, err = f.s.VO2Coverage(ctx, f.p)
	if err != nil || c.State != "sample_deleted" || !c.BootstrapComplete || c.HasLiveFacts || c.LastFactAt == nil || c.StreamSequence != both.ClientSeq || c.ReadAuthorization != "not_determinable" {
		t.Fatal("final coverage", err)
	}
	if count(t, f, "jobs") != 0 {
		t.Fatal("fake job")
	}
	var generation int64
	if err = f.s.Pool.QueryRow(ctx, `SELECT generation FROM dirty_scopes WHERE dataset_id=$1 AND scope_key='external/vo2max/facts/v1'`, f.p.DatasetID).Scan(&generation); err != nil || generation != both.ClientSeq {
		t.Fatal("dirty generation", err)
	}
	daily, err := f.s.DailyCoverage(ctx, f.p)
	if err != nil || len(daily) != 3 {
		t.Fatal("daily coverage polluted", err)
	}
}
func TestVO2SequenceAndSessionFences(t *testing.T) {
	f := setup(t)
	b := vo2Batch(t, f)
	requireVO2(t, f, b)
	for _, tc := range []struct {
		name   string
		change func(*syncing.VO2Batch)
	}{
		{"gap", func(b *syncing.VO2Batch) { b.ClientSeq++ }}, {"sequence replay", func(b *syncing.VO2Batch) { b.ClientSeq = 1; b.PreviousBatchID = nil }},
		{"collector", func(b *syncing.VO2Batch) { b.CollectorEpoch = syncing.NewID() }}, {"epoch", func(b *syncing.VO2Batch) { b.DatasetEpoch = syncing.NewID() }},
		{"query", func(b *syncing.VO2Batch) {
			b.BootstrapStartUs--
			b.FlowCreatedAtUs--
			b.QueryFingerprint = syncing.VO2Fingerprint(b.BootstrapStartUs)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := nextVO2(b)
			tc.change(&bad)
			if _, err := commitVO2(t, f, bad); err == nil {
				t.Fatal("fence ignored")
			}
		})
	}
	raw, _ := json.Marshal(b)
	raw = append(raw, ' ')
	lease, err := f.s.AcquireUpload(context.Background(), f.p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.s.ReleaseUpload(context.Background(), f.p, lease)
	if _, err = f.s.CommitVO2(context.Background(), f.p, raw, lease); err == nil {
		t.Fatal("different bytes same batch")
	}
	_, err = f.s.Pool.Exec(context.Background(), `UPDATE sync_upload_slots SET expires_at=clock_timestamp()-interval '1 second' WHERE dataset_id=$1 AND token=$2`, f.p.DatasetID, lease.Token)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(b)
	if _, err = f.s.CommitVO2(context.Background(), f.p, raw, lease); err == nil {
		t.Fatal("expired lease obtained receipt")
	}
}
func TestVO2FaultsAndBackendTermination(t *testing.T) {
	stages := []struct{ table, when, op string }{{"health_samples", "AFTER", "INSERT"}, {"entity_tombstones", "AFTER", "INSERT"}, {"entity_changes", "AFTER", "INSERT"}, {"change_events", "AFTER", "INSERT"}, {"dirty_scopes", "AFTER", "INSERT OR UPDATE"}, {"daily_health_coverage", "AFTER", "INSERT OR UPDATE"}, {"sync_streams", "AFTER", "UPDATE"}, {"sync_batches", "BEFORE", "UPDATE"}}
	for _, kill := range []bool{false, true} {
		for _, stage := range stages {
			if kill && stage.table != "health_samples" && stage.table != "entity_tombstones" && stage.table != "sync_batches" {
				continue
			}
			t.Run(fmt.Sprintf("%s/terminate=%t", stage.table, kill), func(t *testing.T) {
				f := setup(t)
				ctx := context.Background()
				b := vo2Batch(t, f)
				b.DeletedUUIDs = []string{syncing.NewID()}
				action := `RAISE EXCEPTION 'synthetic_failure';`
				if kill {
					action = `PERFORM pg_terminate_backend(pg_backend_pid());`
				}
				sql := `CREATE FUNCTION vo2_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN ` + action + ` RETURN NEW; END $$; CREATE TRIGGER vo2_fault ` + stage.when + ` ` + stage.op + ` ON ` + stage.table + ` FOR EACH ROW EXECUTE FUNCTION vo2_fault()`
				if _, err := f.s.Pool.Exec(ctx, sql); err != nil {
					t.Fatal(err)
				}
				if _, err := commitVO2(t, f, b); err == nil {
					t.Fatal("injection ignored")
				}
				// A new connection observes durable state after the failed/terminated backend.
				conn, err := pgx.Connect(ctx, f.s.Pool.Config().ConnString())
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close(ctx)
				for _, table := range []string{"health_samples", "entity_tombstones", "entity_changes", "change_events", "dirty_scopes", "daily_health_coverage", "sync_batches"} {
					var n int
					if err = conn.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE dataset_id=$1`, f.p.DatasetID).Scan(&n); err != nil || n != 0 {
						t.Fatal("partial commit", table, err)
					}
				}
				var rev, streams int64
				if err = conn.QueryRow(ctx, `SELECT commit_rev,(SELECT count(*) FROM sync_streams WHERE sample_type=$2) FROM datasets WHERE id=$1`, f.p.DatasetID, syncing.VO2Type).Scan(&rev, &streams); err != nil || rev != 0 || streams != 0 {
					t.Fatal("revision or stream escaped", err)
				}
				if _, err = conn.Exec(ctx, `DROP TRIGGER vo2_fault ON `+stage.table+`; DROP FUNCTION vo2_fault()`); err != nil {
					t.Fatal(err)
				}
				requireVO2(t, f, b)
			})
		}
	}
}
func TestVO2RestrictedRoles(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	suffix := strings.ReplaceAll(syncing.NewID(), "-", "")
	apiRole, workerRole := "rai_api_"+suffix, "rai_worker_"+suffix
	grants, err := os.ReadFile("../../../deploy/postgres/roles.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(ctx, strings.ReplaceAll(strings.ReplaceAll(string(grants), "rai_api", apiRole), "rai_worker", workerRole)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := f.s.Pool.Exec(context.Background(), "DROP OWNED BY "+pgx.Identifier{apiRole}.Sanitize()+", "+pgx.Identifier{workerRole}.Sanitize()+"; DROP ROLE "+pgx.Identifier{apiRole}.Sanitize()+", "+pgx.Identifier{workerRole}.Sanitize()); err != nil {
			t.Error(err)
		}
	})
	openRole := func(role string) *postgres.Store {
		u, _ := url.Parse(f.s.Pool.Config().ConnString())
		q := u.Query()
		q.Set("options", "-crole="+role)
		u.RawQuery = q.Encode()
		s, err := postgres.Open(ctx, u.String(), 2)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(s.Pool.Close)
		return s
	}
	api := openRole(apiRole)
	rf := *f
	rf.s = api
	b := vo2Batch(t, f)
	requireVO2(t, &rf, b)
	del := nextVO2(b)
	del.DeletedUUIDs = []string{b.Facts[0].UUID}
	requireVO2(t, &rf, del)
	if c, err := api.VO2Coverage(ctx, f.p); err != nil || c.State != "sample_deleted" {
		t.Fatal("restricted coverage", err)
	}
	worker := openRole(workerRole)
	for _, sql := range []string{`INSERT INTO health_samples SELECT * FROM health_samples LIMIT 0`, `UPDATE daily_health_coverage SET anchor_present=false`} {
		if _, err = worker.Pool.Exec(ctx, sql); err == nil {
			t.Fatal("worker raw/coverage permission expanded")
		}
	}
	// M4-B3 explicitly adds SELECT-only coverage access for the projection
	// snapshot; B2 raw/coverage mutation restrictions above remain frozen.
	if _, err = worker.Pool.Exec(ctx, `SELECT * FROM daily_health_coverage`); err != nil {
		t.Fatal("worker cannot read projection coverage", err)
	}
	for _, sql := range []string{`UPDATE health_samples SET value_numeric=1`, `SELECT * FROM analysis_artifacts`, `UPDATE entity_tombstones SET provenance_snapshot='{}'`, `CREATE TABLE forbidden_vo2(x int)`} {
		if _, err = api.Pool.Exec(ctx, sql); err == nil {
			t.Fatal("API broad permission")
		}
	}
}
func TestVO2HTTPAndSyntheticPerformance(t *testing.T) {
	f := setup(t)
	server := httptest.NewServer(httpapi.New(f.s, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer server.Close()
	b := vo2Batch(t, f)
	template := b.Facts[0]
	for _, n := range []int{0, 1, 128} {
		b.Facts = []syncing.VO2Fact{}
		for i := 0; i < n; i++ {
			fact := template
			fact.UUID = syncing.NewID()
			b.Facts = append(b.Facts, fact)
		}
		raw, _ := json.Marshal(b)
		req, _ := http.NewRequest("POST", server.URL+"/v2/sync/vo2max-facts", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
		req.Header.Set("Content-Type", "application/json")
		start := time.Now()
		var body []byte
		for attempt := 0; attempt < 5; attempt++ {
			req.Body = io.NopCloser(bytes.NewReader(raw))
			response, err := server.Client().Do(req)
			if err != nil {
				t.Fatal("HTTP failed")
			}
			body, _ = io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode == 200 {
				break
			}
			if response.StatusCode != 429 || attempt == 4 {
				t.Fatal("HTTP status", response.StatusCode)
			}
			// Respect the real admission budget, including Retry-After.
			delay := time.Second
			if header := response.Header.Get("Retry-After"); header != "" {
				if d, e := time.ParseDuration(header + "s"); e == nil {
					delay = d
				}
			}
			time.Sleep(delay)
		}

		var r syncing.VO2Receipt
		if json.Unmarshal(body, &r) != nil || r.RequestSHA256 != syncing.DailyDigest(raw) {
			t.Fatal("HTTP receipt")
		}
		t.Logf("M4B2_SYNTHETIC facts=%d payload_bytes=%d http_ack_us=%d", n, len(raw), time.Since(start).Microseconds())
		b = nextVO2(b)
	}
	// Existing unique indexes serve bounded transport lookups; no speculative index.
	for name, query := range map[string]string{
		"uuid":      `SELECT * FROM health_samples WHERE dataset_id=$1 AND sample_type='HKQuantityTypeIdentifierVO2Max' AND sample_uuid='10000000-0000-4000-8000-000000000010'`,
		"tombstone": `SELECT * FROM entity_tombstones WHERE dataset_id=$1 AND sample_type='HKQuantityTypeIdentifierVO2Max' AND sample_uuid='10000000-0000-4000-8000-000000000010'`,
		"stream":    `SELECT * FROM sync_batches WHERE dataset_id=$1 AND stream_id='10000000-0000-4000-8000-000000000004' AND client_seq=1`,
		"coverage":  `SELECT * FROM daily_health_coverage WHERE dataset_id=$1 AND stream_id='10000000-0000-4000-8000-000000000004'`,
	} {
		rows, err := f.s.Pool.Query(context.Background(), "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+query, f.p.DatasetID)
		if err != nil {
			t.Fatal(err)
		}
		var plan []byte
		for rows.Next() {
			if err = rows.Scan(&plan); err != nil {
				t.Fatal(err)
			}
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
		var result []struct {
			Plan struct {
				NodeType   string  `json:"Node Type"`
				IndexName  string  `json:"Index Name"`
				ActualRows float64 `json:"Actual Rows"`
			} `json:"Plan"`
			ExecutionTime float64 `json:"Execution Time"`
		}
		if err = json.Unmarshal(plan, &result); err != nil || len(result) != 1 {
			t.Fatal("query plan decode")
		}
		// Retain only aggregate execution evidence, never condition literals.
		t.Logf("M4B2_PLAN query=%s indexed=%t node=%s index=%s actual_rows=%.3f execution_us=%.3f", name, bytes.Contains(plan, []byte("Index")), result[0].Plan.NodeType, result[0].Plan.IndexName, result[0].Plan.ActualRows, result[0].ExecutionTime*1000)
	}
}

func TestVO2SyntheticNinetyDayBootstrapAndHotEmpty(t *testing.T) {
	f := setup(t)
	b := vo2Batch(t, f)
	template := b.Facts[0]
	b.Facts = []syncing.VO2Fact{}
	for day := int64(0); day < 90; day++ {
		fact := template
		fact.UUID = syncing.NewID()
		fact.StartUs = b.BootstrapStartUs + day*86400000000
		fact.EndUs = fact.StartUs
		b.Facts = append(b.Facts, fact)
	}
	commit := func(batch syncing.VO2Batch) {
		t.Helper()
		ctx := syncing.ObservePhases(context.Background(), func(phase string, d time.Duration) {
			t.Logf("M4B2_SYNTHETIC_PHASE phase=%s duration_us=%d", phase, d.Microseconds())
		})
		lease, err := f.s.AcquireUpload(ctx, f.p)
		if err != nil {
			t.Fatal(err)
		}
		defer f.s.ReleaseUpload(ctx, f.p, lease)
		raw, _ := json.Marshal(batch)
		begin := time.Now()
		if _, err = f.s.CommitVO2(ctx, f.p, raw, lease); err != nil {
			t.Fatal(err)
		}
		t.Logf("M4B2_SYNTHETIC_TRANSPORT additions=%d payload_bytes=%d total_us=%d", len(batch.Facts), len(raw), time.Since(begin).Microseconds())
	}
	commit(b)
	commit(nextVO2(b))
	c, err := f.s.VO2Coverage(context.Background(), f.p)
	if err != nil || c.StreamSequence != 2 || !c.BootstrapComplete || c.SampleCount != 0 || len(c.SourceSummary) == 2 {
		t.Fatal("bootstrap or summary retention", err)
	}
}

func TestVO2CoverageNeverInheritsOldCollectorOrEpoch(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := vo2Batch(t, f)
	requireVO2(t, f, b)
	current, err := f.s.VO2Coverage(ctx, f.p)
	if err != nil || !current.QueryCompleted {
		t.Fatal("initial observation", err)
	}
	if _, err = f.s.Pool.Exec(ctx, `UPDATE collector_leases SET collector_epoch=$2 WHERE dataset_id=$1`, f.p.DatasetID, syncing.NewID()); err != nil {
		t.Fatal(err)
	}
	c, err := f.s.VO2Coverage(ctx, f.p)
	if err != nil || c.QueryCompleted || c.StreamSequence != 0 || c.HasMore != nil || !c.HasLiveFacts {
		t.Fatal("old collector coverage inherited", err)
	}
	if _, err = commitVO2(t, f, b); err == nil {
		t.Fatal("old collector obtained original receipt")
	}
	epoch := syncing.NewID()
	if _, err = f.s.Pool.Exec(ctx, `UPDATE datasets SET epoch=$2 WHERE id=$1`, f.p.DatasetID, epoch); err != nil {
		t.Fatal(err)
	}
	c, err = f.s.VO2Coverage(ctx, f.p)
	if err != nil || c.QueryCompleted || c.DatasetGeneration != epoch || !c.HasLiveFacts {
		t.Fatal("old epoch inherited", err)
	}
	if _, err = commitVO2(t, f, b); err == nil {
		t.Fatal("old epoch obtained receipt")
	}
}

func TestVO2AndDailyKeepOneEpochBoundChangeCursor(t *testing.T) {
	f := setup(t)
	daily := dailyBatch(f, syncing.DailyRHR)
	daily.Facts = []syncing.DailyFact{dailyFact(daily.SampleType)}
	first, err := commitDaily(t, f, daily)
	if err != nil {
		t.Fatal(err)
	}
	requireVO2(t, f, vo2Batch(t, f))
	retried, err := commitDaily(t, f, daily)
	if err != nil || !bytes.Equal(first, retried) {
		t.Fatal("VO2 altered old Daily receipt", err)
	}
	requireDaily(t, f, nextDaily(daily))
	cursor := "0"
	var lastRev int64
	var lastOrd int32 = -1
	seen := 0
	for {
		page, err := f.s.Changes(context.Background(), f.p, cursor, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, change := range page.Changes {
			if change.Revision < lastRev || change.Revision == lastRev && change.Ordinal <= lastOrd {
				t.Fatal("mixed cursor reordered")
			}
			lastRev, lastOrd = change.Revision, change.Ordinal
			seen++
		}
		dataset, epoch, _, _, err := syncing.ParseChangeCursor(page.NextCursor)
		if err != nil || dataset != f.p.DatasetID || epoch != f.enrollment.DatasetEpoch {
			t.Fatal("cursor identity changed", err)
		}
		cursor = page.NextCursor
		if !page.HasMore {
			break
		}
	}
	if seen != 5 || lastRev != 3 {
		t.Fatal("mixed coverage cursor loss", seen, lastRev)
	}
}

func TestVO2TypedMetadataRoundTripsPostgresWithoutIntegerLoss(t *testing.T) {
	f := setup(t)
	b := vo2Batch(t, f)
	template := b.Facts[0]
	b.Facts = []syncing.VO2Fact{}
	date := int64(1250000)
	unit := "mL/kg/min"
	for _, integer := range []string{"9007199254740993", "-9223372036854775808", "9223372036854775807"} {
		fact := template
		fact.UUID = syncing.NewID()
		fact.Metadata = map[string]syncing.VO2Metadata{
			"HKAlgorithmVersion":                  {Type: "int64", Value: json.RawMessage(integer)},
			"HKVO2MaxTestType":                    {Type: "int64", Value: json.RawMessage(`4`)},
			"HKMetadataKeyAppleDeviceCalibrated":  {Type: "boolean", Value: json.RawMessage(`false`)},
			"HKWasUserEntered":                    {Type: "boolean", Value: json.RawMessage(`true`)},
			"HKMetadataKeySyncIdentifier":         {Type: "string", Value: json.RawMessage(`"synthetic-consistency-evidence"`)},
			"HKMetadataKeySyncVersion":            {Type: "int64", Value: json.RawMessage(`1`)},
			"HKTimeZone":                          {Type: "string", Value: json.RawMessage(`"Asia/Shanghai"`)},
			"HKDateOfEarliestDataUsedForEstimate": {Type: "date", EpochUs: &date},
			"HKVO2MaxValue":                       {Type: "quantity", Value: json.RawMessage(`1.2345678901234567`), Unit: &unit},
		}
		b.Facts = append(b.Facts, fact)
	}
	// The provider's documented range is never a raw DB hard reject.
	b.Facts[0].Quantity.Value = 1000
	if requireVO2(t, f, b).Counts.Inserted != 3 {
		t.Fatal("typed facts not committed")
	}
	for _, fact := range b.Facts {
		var raw []byte
		if err := f.s.Pool.QueryRow(context.Background(), `SELECT metadata->'RAI:vo2maxFactV1'->'typedMetadata' FROM health_samples WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3`, f.p.DatasetID, syncing.VO2Type, fact.UUID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var stored map[string]syncing.VO2Metadata
		if err := json.Unmarshal(raw, &stored); err != nil {
			t.Fatal("typed metadata decode", err)
		}
		expected, _ := json.Marshal(fact.Metadata)
		actual, _ := json.Marshal(stored)
		if !bytes.Equal(syncing.VO2Canonical(json.RawMessage(expected)), syncing.VO2Canonical(json.RawMessage(actual))) {
			t.Fatal("typed metadata changed across PostgreSQL")
		}
	}
}
