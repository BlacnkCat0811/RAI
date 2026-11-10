//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"rai/server/internal/storage/postgres"
)

// The analysis dirty-range helper is the one wrapper shared by the plain
// per-scope call sites; these tests pin its SQL contract against a real
// PostgreSQL schema: one committed dirty row plus one queued job per call,
// generation increments and min/max range merging on repeated calls, and
// transaction scoping (a rollback publishes nothing).

func TestMarkAnalysisDirtyWritesScopeRange(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	from := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	through := from
	const scope = "workout-features"
	const jobKind = "analysis.features"

	tx, err := f.s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = postgres.MarkAnalysisDirty(ctx, tx, f.p.DatasetID, scope, jobKind, from, through); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("MarkAnalysisDirty: %v", err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	generation, dirtyFrom, dirtyThrough := readDirtyScope(t, f, scope)
	if generation != 1 {
		t.Fatalf("generation = %d, want 1 after the first call", generation)
	}
	if !dirtyFrom.Equal(from) || !dirtyThrough.Equal(through) {
		t.Fatalf("dirty range = [%s, %s], want [%s, %s]", dirtyFrom, dirtyThrough, from, through)
	}

	var count int
	if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE dataset_id=$1 AND scope_key=$2`, f.p.DatasetID, scope).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("jobs rows = %d, want exactly one queued job per call", count)
	}
	var kind, state string
	var target int64
	if err := f.s.Pool.QueryRow(ctx, `SELECT kind, target_generation, state FROM jobs WHERE dataset_id=$1 AND scope_key=$2`, f.p.DatasetID, scope).Scan(&kind, &target, &state); err != nil {
		t.Fatal(err)
	}
	if kind != jobKind || target != generation || state != "queued" {
		t.Fatalf("job = (kind=%q, target_generation=%d, state=%q), want (%q, %d, \"queued\")", kind, target, state, jobKind, generation)
	}
}

// TestMarkAnalysisDirtyLoadDayBoundary mirrors the caller range used by
// load_advance.go: a UTC day start through the next day minus one microsecond.
// Repeating the same range bumps the generation but must keep the dirty range
// intact; a later wider range must extend it to the union (LEAST/GREATEST).
func TestMarkAnalysisDirtyLoadDayBoundary(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	day := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	from := day
	through := day.AddDate(0, 0, 1).Add(-time.Microsecond)
	const scope = "athlete/default"
	const jobKind = "analysis.training_state"

	apply := func(from, through time.Time) {
		t.Helper()
		tx, err := f.s.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = postgres.MarkAnalysisDirty(ctx, tx, f.p.DatasetID, scope, jobKind, from, through); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("MarkAnalysisDirty(%s, %s): %v", from, through, err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}

	apply(from, through)
	generation, dirtyFrom, dirtyThrough := readDirtyScope(t, f, scope)
	if generation != 1 || !dirtyFrom.Equal(from) || !dirtyThrough.Equal(through) {
		t.Fatalf("first load-day call = generation %d range [%s, %s], want 1 [%s, %s]", generation, dirtyFrom, dirtyThrough, from, through)
	}

	// The same range again must be accepted and bump the generation while the
	// merged range stays identical.
	apply(from, through)
	generation, dirtyFrom, dirtyThrough = readDirtyScope(t, f, scope)
	if generation != 2 || !dirtyFrom.Equal(from) || !dirtyThrough.Equal(through) {
		t.Fatalf("repeat load-day call = generation %d range [%s, %s], want 2 [%s, %s]", generation, dirtyFrom, dirtyThrough, from, through)
	}

	// A wider range merges to the union instead of replacing the observed
	// window, which is what the publisher consumes.
	wideFrom := from.Add(-time.Hour)
	wideThrough := through.Add(time.Hour)
	apply(wideFrom, wideThrough)
	generation, dirtyFrom, dirtyThrough = readDirtyScope(t, f, scope)
	if generation != 3 || !dirtyFrom.Equal(wideFrom) || !dirtyThrough.Equal(wideThrough) {
		t.Fatalf("wide load-day call = generation %d range [%s, %s], want 3 [%s, %s]", generation, dirtyFrom, dirtyThrough, wideFrom, wideThrough)
	}

	var count int
	if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE dataset_id=$1 AND scope_key=$2 AND kind=$3`, f.p.DatasetID, scope, jobKind).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("jobs rows = %d, want one queued job per helper call", count)
	}
	var target int64
	if err := f.s.Pool.QueryRow(ctx, `SELECT max(target_generation) FROM jobs WHERE dataset_id=$1 AND scope_key=$2`, f.p.DatasetID, scope).Scan(&target); err != nil {
		t.Fatal(err)
	}
	if target != 3 {
		t.Fatalf("max target_generation = %d, want the latest consumed generation 3", target)
	}
}

// TestMarkAnalysisDirtyRollbackLeavesNoScope proves the helper is
// transaction-scoped: rolling back the caller transaction publishes neither the
// dirty range nor its queued job.
func TestMarkAnalysisDirtyRollbackLeavesNoScope(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	const scope = "athlete/default"
	const jobKind = "analysis.training_state"

	tx, err := f.s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = postgres.MarkAnalysisDirty(ctx, tx, f.p.DatasetID, scope, jobKind, time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("MarkAnalysisDirty: %v", err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	if n := scopeRowCount(t, f, "dirty_scopes", scope); n != 0 {
		t.Fatalf("dirty_scopes rows after rollback = %d, want 0", n)
	}
	if n := scopeRowCount(t, f, "jobs", scope); n != 0 {
		t.Fatalf("jobs rows after rollback = %d, want 0", n)
	}
}

func readDirtyScope(t *testing.T, f *fixture, scope string) (int64, time.Time, time.Time) {
	t.Helper()
	var generation int64
	var from, through time.Time
	if err := f.s.Pool.QueryRow(context.Background(), `SELECT generation, dirty_from, dirty_through FROM dirty_scopes WHERE dataset_id=$1 AND scope_key=$2`, f.p.DatasetID, scope).Scan(&generation, &from, &through); err != nil {
		t.Fatalf("read dirty scope %q: %v", scope, err)
	}
	return generation, from, through
}

func scopeRowCount(t *testing.T, f *fixture, table, scope string) int {
	t.Helper()
	var n int
	query := `SELECT count(*) FROM ` + pgx.Identifier{table}.Sanitize() + ` WHERE dataset_id=$1 AND scope_key=$2`
	if err := f.s.Pool.QueryRow(context.Background(), query, f.p.DatasetID, scope).Scan(&n); err != nil {
		t.Fatalf("count %s for scope %q: %v", table, scope, err)
	}
	return n
}
