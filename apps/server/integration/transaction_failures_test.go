//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/syncing"
)

func TestBatchRegistrationFailureRollsBack(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	e, _ := f.envelope(t, []syncing.Event{sample(syncing.NewID(), 120)}, 1, nil)
	if _, err := f.s.Pool.Exec(ctx, `CREATE FUNCTION fail_after_batch() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic failure'; END $$;
		CREATE TRIGGER fail_after_batch AFTER INSERT ON sync_batches FOR EACH ROW EXECUTE FUNCTION fail_after_batch()`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.RegisterBatch(ctx, f.p, e); err == nil {
		t.Fatal("injected registration failure did not abort")
	}
	if count(t, f, "sync_batches") != 0 || count(t, f, "sync_pages") != 0 || count(t, f, "health_samples") != 0 || count(t, f, "jobs") != 0 {
		t.Fatal("failed registration left partial state")
	}
	if _, err := f.s.Pool.Exec(ctx, `DROP TRIGGER fail_after_batch ON sync_batches`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.RegisterBatch(ctx, f.p, e); err != nil {
		t.Fatal(err)
	}
}

func TestCommitFailureInjectionAtomicity(t *testing.T) {
	for _, test := range []struct {
		name, table, operation string
		deletion               bool
	}{
		{"fact", "health_samples", "INSERT", false},
		{"tombstone", "entity_tombstones", "INSERT", true},
		{"change log", "change_events", "INSERT", false},
		{"job", "jobs", "INSERT", false},
		{"receipt", "sync_batches", "UPDATE", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			id := syncing.NewID()
			seq := int64(1)
			var previous *string
			events := []syncing.Event{sample(id, 120)}
			if test.deletion {
				seed, _, _ := f.commit(t, events, 1, nil)
				previous = &seed.BatchID
				seq = 2
				events = []syncing.Event{deleted(id)}
			}
			e, _ := f.envelope(t, events, seq, previous)
			if _, err := f.s.RegisterBatch(ctx, f.p, e); err != nil {
				t.Fatal(err)
			}
			before := businessSnapshot(t, f)
			if _, err := f.s.Pool.Exec(ctx, `CREATE FUNCTION fail_p2_write() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic failure'; END $$`); err != nil {
				t.Fatal(err)
			}
			trigger := `CREATE TRIGGER fail_p2_write BEFORE ` + test.operation + ` ON ` + pgx.Identifier{test.table}.Sanitize() + ` FOR EACH ROW EXECUTE FUNCTION fail_p2_write()`
			if _, err := f.s.Pool.Exec(ctx, trigger); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.Commit(ctx, f.p, e, events); err == nil {
				t.Fatal("injected failure did not abort commit")
			}
			sameBusiness(t, f, before)
			if _, err := f.s.Pool.Exec(ctx, `DROP TRIGGER fail_p2_write ON `+pgx.Identifier{test.table}.Sanitize()); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.Commit(ctx, f.p, e, events); err != nil {
				t.Fatal("retry after rollback failed", err)
			}
		})
	}
}
