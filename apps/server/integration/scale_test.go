//go:build integration && scale

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"rai/server/internal/storage/postgres/dbgen"
	"rai/server/internal/syncing"
)

// Explicit scale tag: these are real, durable transactions, not a fabricated
// page counter or a wall-clock performance assertion. CI runs this suite too.
func TestScaleMaximumPageRecovery(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	started := time.Now()
	const fragments = syncing.MaxPageFragments
	page, id := syncing.NewID(), syncing.NewID()
	events := []syncing.Event{sample(id, 132)}
	template, wire := f.envelope(t, events, 1, nil)
	parsed, err := syncing.ParseBody(bytes.NewReader(wire), template, kind)
	if err != nil {
		t.Fatal(err)
	}
	var first, last syncing.Envelope
	var firstReceipt []byte
	chain := make([]byte, sha256.Size)
	var windows []float64
	window := time.Now()
	for i := 0; i < fragments-1; i++ {
		e := template
		e.BatchID, e.PageID = syncing.NewID(), page
		e.ClientSeq, e.FragmentIndex, e.FragmentCount = int64(i+1), i, fragments
		if i > 0 {
			previous := last.BatchID
			e.PreviousBatchID = &previous
		}
		receipt := registerCommit(t, f, e, parsed)
		if i == 0 {
			first, firstReceipt = e, receipt
		}
		_, digest := e.Canonical()
		h := sha256.Sum256(append(chain, digest...))
		chain, last = h[:], e
		if (i+1)%1000 == 0 {
			windows = append(windows, time.Since(window).Seconds())
			t.Logf("SCALE_PROGRESS committed_fragments=%d", i+1)
			window = time.Now()
		}
		if i+1 == fragments/2 {
			// Reconstruct the repository/pool, keeping only durable server state.
			next := uploadPool(t, f)
			f.s.Pool.Close()
			f.s = next
			pageState(t, f, page, i+1, "pending")
			retry := registerCommit(t, f, first, parsed)
			if !bytes.Equal(retry, firstReceipt) {
				t.Fatal("pool reconstruction changed first receipt")
			}
		}
	}
	pageState(t, f, page, fragments-1, "pending")
	finalEvents := []syncing.Event{deleted(id)}
	final, finalWire := pageEnvelope(t, f, page, fragments-1, fragments, finalEvents, int64(fragments), &last.BatchID)
	finalParsed, err := syncing.ParseBody(bytes.NewReader(finalWire), final, kind)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.RegisterBatch(ctx, f.p, final); err != nil {
		t.Fatal(err)
	}
	// Snapshot the affected business rows. Never aggregate all historical batch
	// JSON merely to check one final fragment in a large page.
	before := scaleFinalSnapshot(t, f, final)
	_, err = f.s.Pool.Exec(ctx, `CREATE FUNCTION fail_scale_seal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic final seal failure'; END $$;
CREATE TRIGGER fail_scale_seal BEFORE UPDATE ON sync_pages FOR EACH ROW WHEN (NEW.receipt IS NOT NULL) EXECUTE FUNCTION fail_scale_seal()`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Commit(ctx, f.p, final, finalParsed); err == nil {
		t.Fatal("final seal fault did not abort")
	}
	for table, after := range scaleFinalSnapshot(t, f, final) {
		if !bytes.Equal(before[table], after) {
			t.Fatal("final fragment failure changed", table)
		}
	}
	if _, err = f.s.Pool.Exec(ctx, `DROP TRIGGER fail_scale_seal ON sync_pages`); err != nil {
		t.Fatal(err)
	}
	// Reopen with a registered, uncommitted last fragment and recover it.
	next := uploadPool(t, f)
	f.s.Pool.Close()
	f.s = next
	finalReceipt := registerCommit(t, f, final, finalParsed)
	sealed := pageState(t, f, page, fragments, "sealed")
	_, digest := final.Canonical()
	h := sha256.Sum256(append(chain, digest...))
	var receipt syncing.PageReceipt
	if err = json.Unmarshal(sealed.Receipt, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.ManifestSHA256 != hex.EncodeToString(h[:]) || receipt.FirstClientSeq != 1 || receipt.LastClientSeq != int64(fragments) || receipt.LastBatchID != final.BatchID || receipt.FragmentCount != fragments || receipt.CommitRevision != 2 {
		t.Fatalf("maximum page receipt mismatch: %+v", receipt)
	}
	if count(t, f, "sync_batches") != fragments || count(t, f, "sync_pages") != 1 || count(t, f, "health_samples") != 0 || count(t, f, "entity_tombstones") != 1 || count(t, f, "entity_changes") != 2 || count(t, f, "change_events") != 2 || count(t, f, "jobs") != 2 {
		t.Fatal("no-op fragments or failed seal changed business counts")
	}
	for _, c := range []struct {
		e       syncing.Envelope
		events  []syncing.Event
		receipt []byte
	}{{first, parsed, firstReceipt}, {final, finalParsed, finalReceipt}} {
		if got := registerCommit(t, f, c.e, c.events); !bytes.Equal(got, c.receipt) {
			t.Fatal("maximum-page retry changed receipt")
		}
		got, err := f.s.Batch(ctx, f.p, c.e.BatchID)
		if err != nil || !bytes.Equal(got.Receipt, c.receipt) {
			t.Fatal("maximum-page GET changed receipt", err)
		}
	}
	// A subsequent page starts exactly after the maximum page. Its new fact is
	// still suppressed by the delete barrier; the previous page stays immutable.
	_, r, _ := f.commit(t, events, int64(fragments+1), &final.BatchID)
	if r.Counts.Suppressed != 1 || r.CommitRevision != 2 {
		t.Fatal("later page revived a deleted UUID or consumed a revision")
	}
	if after := pageState(t, f, page, fragments, "sealed"); !bytes.Equal(sealed.Receipt, after.Receipt) {
		t.Fatal("subsequent page changed maximum page receipt")
	}
	t.Logf("SCALE_PAGE fragments=%d seconds=%.3f thousand_fragment_seconds=%v recovery=pool_reconstruction final_seal_rollback=true", fragments, time.Since(started).Seconds(), windows)
}

func scaleFinalSnapshot(t *testing.T, f *fixture, e syncing.Envelope) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for table, clause := range map[string]string{
		"datasets": "WHERE id=$1", "sync_streams": "WHERE dataset_id=$1 AND id=$2",
		"sync_pages": "WHERE dataset_id=$1 AND page_id=$3", "sync_batches": "WHERE dataset_id=$1 AND batch_id=$4",
		"health_samples": "WHERE dataset_id=$1", "entity_tombstones": "WHERE dataset_id=$1",
		"entity_changes": "WHERE dataset_id=$1", "change_events": "WHERE dataset_id=$1",
		"dirty_scopes": "WHERE dataset_id=$1", "jobs": "WHERE dataset_id=$1",
	} {
		// Four typed parameters let each bounded predicate use the same argument
		// list without unknown unused PostgreSQL parameter types.
		query := `WITH args AS (SELECT $1::uuid,$2::uuid,$3::uuid,$4::uuid)
SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text),'[]') FROM (SELECT * FROM ` + pgx.Identifier{table}.Sanitize() + ` ` + clause + `) x`
		var raw []byte
		if err := f.s.Pool.QueryRow(context.Background(), query, f.p.DatasetID, e.StreamID, e.PageID, e.BatchID).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		out[table] = raw
	}
	return out
}

// Capture EXPLAIN for exactly the SQL/arguments emitted by sqlc. Returning a
// sentinel from Scan prevents the generated method from interpreting plan JSON
// as its ordinary result. No copied production query can drift from this gate.
var errPlanCaptured = errors.New("plan captured")

type planDB struct {
	tx  pgx.Tx
	raw []byte
}

func (d *planDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return planRow{d: d, row: d.tx.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+sql, args...)}
}
func (d *planDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected multirow query in plan gate")
}
func (d *planDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("unexpected exec in plan gate")
}

type planRow struct {
	d   *planDB
	row pgx.Row
}

func (r planRow) Scan(...any) error {
	if err := r.row.Scan(&r.d.raw); err != nil {
		return err
	}
	return errPlanCaptured
}

type queryPlan struct {
	Plan planNode `json:"Plan"`
}
type planNode struct {
	NodeType string     `json:"Node Type"`
	Relation string     `json:"Relation Name"`
	Index    string     `json:"Index Name"`
	Rows     float64    `json:"Actual Rows"`
	Loops    float64    `json:"Actual Loops"`
	Removed  float64    `json:"Rows Removed by Filter"`
	Hits     int        `json:"Shared Hit Blocks"`
	Reads    int        `json:"Shared Read Blocks"`
	Plans    []planNode `json:"Plans"`
}

func TestScaleUploadQueryPlans(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	// A dedicated fixture writer may take longer than an online request. Online
	// pool/transaction deadlines and planner settings are never relaxed.
	seed, err := pgx.Connect(ctx, f.s.Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close(ctx)
	if _, err = seed.Exec(ctx, `SET statement_timeout='120s'`); err != nil {
		t.Fatal(err)
	}
	for _, rows := range []int{10000, 100000} {
		t.Run(fmt.Sprintf("rows_%d", rows), func(t *testing.T) {
			seedScaleRows(t, seed, f, rows)
			for _, hit := range []bool{true, false} {
				id := fmt.Sprintf("00000000-0000-0000-0000-%012d", rows)
				seq := int64(rows)
				if !hit {
					id, seq = "ffffffff-ffff-ffff-ffff-ffffffffffff", int64(rows+1)
				}
				queries := []struct {
					name  string
					index string
					call  func(*dbgen.Queries) error
				}{
					{"batch", "sync_batches_pkey", func(q *dbgen.Queries) error {
						_, e := q.Batch(ctx, dbgen.BatchParams{DatasetID: f.p.DatasetID, BatchID: id})
						return e
					}},
					{"page", "sync_pages_pkey", func(q *dbgen.Queries) error {
						_, e := q.Page(ctx, dbgen.PageParams{DatasetID: f.p.DatasetID, PageID: id})
						return e
					}},
					{"sequence", "sync_batches_dataset_id_stream_id_client_seq_key", func(q *dbgen.Queries) error {
						_, e := q.BatchSequenceOccupied(ctx, dbgen.BatchSequenceOccupiedParams{DatasetID: f.p.DatasetID, StreamID: f.stream.StreamID, ClientSeq: seq})
						return e
					}},
					{"legacy_page", "sync_batches_legacy_page", func(q *dbgen.Queries) error {
						_, e := q.LegacyPageExists(ctx, dbgen.LegacyPageExistsParams{DatasetID: f.p.DatasetID, PageID: id})
						return e
					}},
					{"open_page", "sync_pages_open", func(q *dbgen.Queries) error {
						_, e := q.OpenPageExists(ctx, dbgen.OpenPageExistsParams{DatasetID: f.p.DatasetID, StreamID: id})
						return e
					}},
					{"tombstone", "entity_tombstones_pkey", func(q *dbgen.Queries) error {
						_, e := q.TombstoneExists(ctx, dbgen.TombstoneExistsParams{DatasetID: f.p.DatasetID, SampleType: kind, SampleUuid: id})
						return e
					}},
					{"sample", "health_samples_pkey", func(q *dbgen.Queries) error {
						_, e := q.SampleIdentity(ctx, dbgen.SampleIdentityParams{DatasetID: f.p.DatasetID, SampleType: kind, SampleUuid: id})
						return e
					}},
					{"workout", "workouts_pkey", func(q *dbgen.Queries) error {
						_, e := q.WorkoutIdentity(ctx, dbgen.WorkoutIdentityParams{DatasetID: f.p.DatasetID, WorkoutUuid: id})
						return e
					}},
				}
				for _, query := range queries {
					t.Run(fmt.Sprintf("%s/hit_%t", query.name, hit), func(t *testing.T) {
						tx, err := f.s.Pool.Begin(ctx)
						if err != nil {
							t.Fatal(err)
						}
						defer tx.Rollback(ctx)
						d := &planDB{tx: tx}
						if err := query.call(dbgen.New(d)); !errors.Is(err, errPlanCaptured) {
							t.Fatal(err)
						}
						var plans []queryPlan
						if err = json.Unmarshal(d.raw, &plans); err != nil || len(plans) != 1 {
							t.Fatal("bad explain", err)
						}
						root := plans[0].Plan
						found := false
						var check func(planNode)
						check = func(p planNode) {
							if p.NodeType == "Seq Scan" || p.NodeType == "Bitmap Heap Scan" || p.Removed > 1 || p.Rows*p.Loops > 1 {
								t.Fatalf("unbounded point lookup: %s", d.raw)
							}
							if p.Index == query.index {
								found = true
								want := float64(0)
								if hit {
									want = 1
								}
								if p.Rows != want {
									t.Fatalf("lookup hit/miss mismatch: %s", d.raw)
								}
							}
							for _, child := range p.Plans {
								check(child)
							}
						}
						check(root)
						if !found || root.Hits+root.Reads > 32 {
							t.Fatalf("index/buffer budget exceeded: %s", d.raw)
						}
						t.Logf("SCALE_PLAN rows=%d query=%s hit=%t plan=%s", rows, query.name, hit, d.raw)
					})
				}
			}
		})
	}
}

func seedScaleRows(t *testing.T, conn *pgx.Conn, f *fixture, rows int) {
	t.Helper()
	ctx := context.Background()
	// Synthetic owner-only bulk fixtures, never an alternate application write
	// path. Tables have their real constraints/indexes and normal durability.
	const generated = ` FROM generate_series($2::int,$3::int) n CROSS JOIN LATERAL (SELECT ('00000000-0000-0000-0000-'||lpad(n::text,12,'0'))::uuid id) g`
	from := 1
	if rows == 100000 {
		from = 10001
	}
	queries := []string{
		`INSERT INTO health_samples(dataset_id,sample_type,sample_uuid,event_start,event_end,value_numeric,unit,source_bundle_id,content_sha256,entity_revision) SELECT $1,'` + kind + `',id,'2026-01-01'::timestamptz,'2026-01-01'::timestamptz,132,'count/min','synthetic.scale',decode(repeat('00',32),'hex'),1` + generated,
		`INSERT INTO entity_tombstones(dataset_id,sample_type,sample_uuid,deleted_rev,reason) SELECT $1,'` + kind + `',id,1,'synthetic.scale'` + generated,
		`INSERT INTO workouts(dataset_id,workout_uuid,event_start,event_end,activity_type,duration_us,source_bundle_id,content_sha256,entity_revision,payload) SELECT $1,id,'2026-01-01'::timestamptz,'2026-01-01'::timestamptz,37,0,'synthetic.scale',decode(repeat('00',32),'hex'),1,'{}'` + generated,
		// Legacy registrations exercise the partial expression index with a real
		// backlog. They have no verified page linkage or invented page receipts.
		`INSERT INTO sync_batches(dataset_id,batch_id,stream_id,client_seq,dataset_epoch,envelope_sha256,payload_sha256,envelope,event_count) SELECT $1,id,'` + f.stream.StreamID + `',n,'` + f.enrollment.DatasetEpoch + `',decode(repeat('00',32),'hex'),decode(repeat('00',32),'hex'),jsonb_build_object('pageID',id::text),0` + generated,
		// One pending page per distinct stream, satisfying the partial unique
		// index while making the open-page probe selective within one dataset.
		`INSERT INTO sync_streams(dataset_id,id,collector_epoch,sample_type,query_fingerprint) SELECT $1,id,'` + f.enrollment.CollectorEpoch + `','` + kind + `','sha256:'||md5(n::text)||md5(n::text)` + generated,
		`INSERT INTO sync_pages(dataset_id,page_id,stream_id,dataset_epoch,collector_epoch,query_fingerprint,fragment_count,first_client_seq,manifest_sha256) SELECT $1,id,id,'` + f.enrollment.DatasetEpoch + `','` + f.enrollment.CollectorEpoch + `','sha256:'||md5(n::text)||md5(n::text),1,1,decode(repeat('00',32),'hex')` + generated,
	}
	for i, query := range queries {
		start := time.Now()
		if _, err := conn.Exec(ctx, query, f.p.DatasetID, from, rows); err != nil {
			t.Fatal(err)
		}
		t.Logf("SCALE_SEED stage=%d rows=%d seconds=%.3f", i, rows, time.Since(start).Seconds())
	}
	for _, table := range []string{"health_samples", "entity_tombstones", "workouts", "sync_batches", "sync_streams", "sync_pages"} {
		if _, err := conn.Exec(ctx, `ANALYZE `+pgx.Identifier{table}.Sanitize()); err != nil {
			t.Fatal(err)
		}
		var actual, size int64
		if err := conn.QueryRow(ctx, `SELECT count(*),pg_total_relation_size($1::regclass) FROM `+pgx.Identifier{table}.Sanitize(), table).Scan(&actual, &size); err != nil {
			t.Fatal(err)
		}
		expected := int64(rows)
		if table == "sync_streams" {
			expected++
		}
		if actual != expected {
			t.Fatalf("%s fixture rows=%d want=%d", table, actual, expected)
		}
		t.Logf("SCALE_TABLE table=%s rows=%d bytes=%d", table, actual, size)
	}
}
