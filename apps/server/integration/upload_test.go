//go:build integration

package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pressly/goose/v3"
	"rai/server/internal/admin"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
	"rai/server/migrations"
)

func uploadPool(t *testing.T, f *fixture) *postgres.Store {
	t.Helper()
	s, err := postgres.Open(context.Background(), f.s.Pool.Config().ConnString(), 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Pool.Close)
	return s
}

func takeUpload(t *testing.T, s *postgres.Store, p syncing.Principal) syncing.UploadLease {
	t.Helper()
	l, err := s.AcquireUpload(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// Complete synthetic row snapshots catch mutations to receipts, stream watermarks,
// generations and nested Workout content as well as insert/delete side effects.
func businessSnapshot(t *testing.T, f *fixture) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, table := range []string{"datasets", "sync_streams", "sync_batches", "sync_pages", "health_samples", "workouts", "workout_statistics", "workout_events", "entity_tombstones", "entity_changes", "change_events", "dirty_scopes", "jobs"} {
		var raw []byte
		// Schema 18 adds nullable retirement metadata. Normalize only NULL on
		// the small dataset row across historical upgrades; non-NULL retirement
		// remains in the snapshot and every existing fact/receipt assertion stays.
		projection := "to_jsonb(x)"
		if table == "datasets" {
			projection = "jsonb_strip_nulls(to_jsonb(x)) - 'synthetic_test'"
		}
		if err := f.s.Pool.QueryRow(context.Background(), `SELECT coalesce(jsonb_agg(`+projection+` ORDER BY to_jsonb(x)::text),'[]') FROM `+pgx.Identifier{table}.Sanitize()+` x`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		out[table] = raw
	}
	return out
}

func sameBusiness(t *testing.T, f *fixture, before map[string][]byte) {
	t.Helper()
	for table, after := range businessSnapshot(t, f) {
		if !bytes.Equal(before[table], after) {
			t.Fatal("operational failure changed business state:", table)
		}
	}
}

// Schema 10 adds nullable provenance. Existing records must remain NULL, while
// all pre-existing business columns and receipts must remain byte-for-byte
// equivalent after normalization of that newly added field.
func sameBusinessAcrossDeviceMigration(t *testing.T, f *fixture, before map[string][]byte) {
	t.Helper()
	after := businessSnapshot(t, f)
	normalize := func(raw []byte, field string) []string {
		t.Helper()
		var rows []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &rows); err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(rows))
		for _, row := range rows {
			if device, ok := row[field]; ok && string(device) != "null" {
				t.Fatal("migration invented legacy device provenance")
			}
			delete(row, field)
			b, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, string(b))
		}
		sort.Strings(out)
		return out
	}
	for table, raw := range after {
		field := ""
		if table == "sync_streams" || table == "sync_batches" {
			field = "device_id"
		}
		if table == "health_samples" || table == "workouts" {
			field = "ingest_device_id"
		}
		if field != "" {
			if !reflect.DeepEqual(normalize(before[table], field), normalize(raw, field)) {
				t.Fatal("migration changed business state:", table)
			}
		} else if !bytes.Equal(before[table], raw) {
			t.Fatal("migration changed business state:", table)
		}
	}
}

func TestUploadLeaseSharedDurableAndFenced(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	other := uploadPool(t, f)
	before := businessSnapshot(t, f)
	start := make(chan struct{})
	leases := make([]syncing.UploadLease, 12)
	errs := make([]error, 12)
	var wg sync.WaitGroup
	for i := range leases {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			s := f.s
			if i%2 == 1 {
				s = other
			}
			leases[i], errs[i] = s.AcquireUpload(ctx, f.p)
		}(i)
	}
	close(start)
	wg.Wait()
	var original syncing.UploadLease
	accepted := 0
	for i, err := range errs {
		if err == nil {
			accepted++
			original = leases[i]
		} else {
			requireCode(t, err, "device_upload_capacity_exceeded")
		}
	}
	if accepted != 1 || count(t, f, "sync_upload_slots") != 2 {
		t.Fatal("replicas exceeded one device lease", accepted)
	}
	if left := time.Until(original.ExpiresAt); left < 25*time.Second || left > 31*time.Second {
		t.Fatal("incorrect lease TTL")
	}
	// Pool reconstruction and token rotation cannot bypass a live device lease.
	restarted := uploadPool(t, f)
	rotated := "synthetic-upload-token-rotation-012345678901234567890123456789"
	if _, err := f.s.Pool.Exec(ctx, `UPDATE devices SET token_sha256=sha256($2::bytea) WHERE id=$1`, f.p.DeviceID, []byte(rotated)); err != nil {
		t.Fatal(err)
	}
	p, err := restarted.Authenticate(ctx, rotated)
	if err != nil {
		t.Fatal(err)
	}
	_, err = restarted.AcquireUpload(ctx, p)
	requireCode(t, err, "device_upload_capacity_exceeded")
	// Synthetic owner shortens the deadline, standing in for a crashed process.
	if _, err = f.s.Pool.Exec(ctx, `UPDATE sync_upload_slots SET expires_at=clock_timestamp()-interval '1 second' WHERE token=$1`, original.Token); err != nil {
		t.Fatal(err)
	}
	replacement := takeUpload(t, restarted, p)
	if replacement.Token == original.Token {
		t.Fatal("reclamation reused fencing identity")
	}
	for i := 0; i < 2; i++ {
		if err = other.ReleaseUpload(ctx, p, original); err != nil {
			t.Fatal(err)
		}
	}
	_, err = other.AcquireUpload(ctx, p)
	requireCode(t, err, "device_upload_capacity_exceeded")
	if err = restarted.ReleaseUpload(ctx, p, replacement); err != nil {
		t.Fatal(err)
	}
	_ = takeUpload(t, other, p)
	sameBusiness(t, f, before)
	if count(t, f, "sync_request_budgets") != 0 {
		t.Fatal("lease operations changed rate debt")
	}
}

func TestUploadLeaseDatasetCapacityAndIsolation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	other := uploadPool(t, f)
	p2, p3 := enrolledDevice(t, f), enrolledDevice(t, f)
	one := takeUpload(t, f.s, f.p)
	two := takeUpload(t, other, p2)
	_, err := other.AcquireUpload(ctx, p3)
	requireCode(t, err, "dataset_upload_capacity_exceeded")
	_, err = other.AcquireUpload(ctx, f.p)
	requireCode(t, err, "device_upload_capacity_exceeded")
	// Another dataset is independent, including a deliberately wrong-scope release.
	p := syncing.Principal{DatasetID: syncing.NewID(), DeviceID: syncing.NewID(), CanCollect: true}
	if _, err = f.s.Pool.Exec(ctx, `INSERT INTO datasets(id,epoch,service_id) VALUES($1,$2,$3)`, p.DatasetID, syncing.NewID(), syncing.NewID()); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(ctx, `INSERT INTO devices(dataset_id,id,token_sha256,can_collect) VALUES($1,$2,sha256($3::bytea),true)`, p.DatasetID, p.DeviceID, []byte(p.DeviceID)); err != nil {
		t.Fatal(err)
	}
	_ = takeUpload(t, other, p)
	if err = other.ReleaseUpload(ctx, p, one); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.AcquireUpload(ctx, f.p)
	requireCode(t, err, "device_upload_capacity_exceeded")
	if err = f.s.ReleaseUpload(ctx, p2, two); err != nil {
		t.Fatal(err)
	}
	_ = takeUpload(t, f.s, p3)
	if count(t, f, "sync_upload_slots") != 4 {
		t.Fatal("slot rows grew with requests")
	}
	readOnly := p3
	readOnly.CanCollect = false
	_, err = other.AcquireUpload(ctx, readOnly)
	requireCode(t, err, "read_only_device")
}

func TestUploadLeaseConcurrentDatasetCap(t *testing.T) {
	f := setup(t)
	other := uploadPool(t, f)
	devices := make([]syncing.Principal, 12)
	for i := range devices {
		devices[i] = enrolledDevice(t, f)
	}
	start := make(chan struct{})
	errs := make([]error, len(devices))
	var wg sync.WaitGroup
	for i, p := range devices {
		wg.Add(1)
		go func(i int, p syncing.Principal) {
			defer wg.Done()
			<-start
			s := f.s
			if i%2 == 1 {
				s = other
			}
			_, errs[i] = s.AcquireUpload(context.Background(), p)
		}(i, p)
	}
	close(start)
	wg.Wait()
	accepted := 0
	for _, err := range errs {
		if err == nil {
			accepted++
		} else {
			requireCode(t, err, "dataset_upload_capacity_exceeded")
		}
	}
	if accepted != syncing.DatasetUploads || count(t, f, "sync_upload_slots") != 2 {
		t.Fatal("replicas exceeded dataset cap", accepted)
	}
}

func TestUploadLeaseLossRollsBackEntireFactTransaction(t *testing.T) {
	for _, payload := range []string{"sample", "workout"} {
		for _, op := range []string{"upsert", "delete"} {
			t.Run(payload+"_"+op, func(t *testing.T) {
				f := setup(t)
				ctx := context.Background()
				id := syncing.NewID()
				event, deletion := sample(id, 132), deleted(id)
				if payload == "workout" {
					workoutStream(t, f)
					event, deletion = workout(t, id), workoutDelete(id)
				}
				seq := int64(1)
				var previous *string
				if op == "delete" {
					e, _, _ := f.commit(t, []syncing.Event{event}, seq, nil)
					previous, seq, event = &e.BatchID, 2, deletion
				}
				events := []syncing.Event{event}
				e, _ := f.envelope(t, events, seq, previous)
				if _, err := f.s.RegisterBatch(ctx, f.p, e); err != nil {
					t.Fatal(err)
				}
				before := businessSnapshot(t, f)
				old := takeUpload(t, f.s, f.p)
				if _, err := f.s.Pool.Exec(ctx, `UPDATE sync_upload_slots SET expires_at=clock_timestamp()-interval '1 second' WHERE token=$1`, old.Token); err != nil {
					t.Fatal(err)
				}
				_, err := f.s.CommitUpload(ctx, f.p, e, events, old)
				requireCode(t, err, "upload_lease_lost")
				current := takeUpload(t, uploadPool(t, f), f.p)
				_, err = f.s.CommitUpload(ctx, f.p, e, events, old)
				requireCode(t, err, "upload_lease_lost")
				sameBusiness(t, f, before)
				// Deterministically expire the synthetic lease at the page-seal write,
				// after facts, nested rows, barriers, logs, jobs and receipts changed.
				// The final guard must roll back every change, including this trigger.
				if _, err = f.s.Pool.Exec(ctx, `CREATE FUNCTION expire_upload_at_seal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
UPDATE sync_upload_slots SET expires_at=clock_timestamp()-interval '1 second' WHERE dataset_id=NEW.dataset_id;
RETURN NEW; END $$;
CREATE TRIGGER expire_upload_at_seal BEFORE UPDATE ON sync_pages FOR EACH ROW EXECUTE FUNCTION expire_upload_at_seal()`); err != nil {
					t.Fatal(err)
				}
				_, err = f.s.CommitUpload(ctx, f.p, e, events, current)
				requireCode(t, err, "upload_lease_lost")
				sameBusiness(t, f, before)
				if _, err = f.s.Pool.Exec(ctx, `DROP TRIGGER expire_upload_at_seal ON sync_pages`); err != nil {
					t.Fatal(err)
				}
				original, err := f.s.CommitUpload(ctx, f.p, e, events, current)
				if err != nil {
					t.Fatal("rolled-back batch cannot recover", err)
				}
				retry, err := f.s.CommitUpload(ctx, f.p, e, events, current)
				if err != nil || !bytes.Equal(original, retry) {
					t.Fatal("lease changed original receipt", err)
				}
				pageState(t, f, e.PageID, 1, "sealed")
			})
		}
	}
}

func TestSchema5UpgradePreservesReceiptsAndBudgets(t *testing.T) {
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
		_, err = provider.UpTo(ctx, 5)
		return err
	})
	ctx := context.Background()
	events := []syncing.Event{sample(syncing.NewID(), 132)}
	e, _, receipt := f.commit(t, events, 1, nil)
	if err := f.s.Admit(ctx, f.p, syncing.UploadLane); err != nil {
		t.Fatal(err)
	}
	debt := budgetTime(t, f, &f.p.DeviceID, syncing.UploadLane)
	before := businessSnapshot(t, f)
	if err := f.s.Ready(ctx); err == nil {
		t.Fatal("schema 5 accepted by schema 9 binary")
	}
	if err := admin.Migrate(ctx, f.s.Pool.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if count(t, f, "sync_upload_slots") != 0 {
		t.Fatal("migration invented leases")
	}
	lease := takeUpload(t, f.s, f.p)
	retry, err := f.s.CommitUpload(ctx, f.p, e, events, lease)
	if err != nil || !bytes.Equal(retry, receipt) {
		t.Fatal("upgrade altered receipt", err)
	}
	if err = admin.Migrate(ctx, f.s.Pool.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.AcquireUpload(ctx, f.p)
	requireCode(t, err, "device_upload_capacity_exceeded")
	if !budgetTime(t, f, &f.p.DeviceID, syncing.UploadLane).Equal(debt) {
		t.Fatal("upgrade reset rate debt")
	}
	sameBusinessAcrossDeviceMigration(t, f, before)
}
