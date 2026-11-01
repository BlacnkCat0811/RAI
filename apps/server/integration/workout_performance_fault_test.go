//go:build integration

package integration

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"io"
	"log/slog"
	"rai/server/internal/jobs"
	"testing"
	"time"
)

func TestPhase4SnapshotCancelLockFailureAndWorkerRetry(t *testing.T) {
	f := setup(t)
	b, _ := testWorkoutBundle(t, f, 3)
	phase3Observation(&b)
	_, _ = importPhase3(t, f, b)
	ctx := context.Background()
	lease, err := f.s.Claim(ctx, []string{"analysis.workout"})
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := f.s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(ctx)
	if _, err = blocker.Exec(ctx, `LOCK TABLE health_samples IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 40*time.Millisecond)
	_, err = f.s.LoadAnalysis(short, lease, time.Now())
	cancel()
	if err == nil || (!errors.Is(err, context.DeadlineExceeded) && !pgconn.Timeout(err)) {
		t.Fatal("snapshot cancellation not effective", err)
	}
	// A real worker attempt encountering the held table lock must requeue its
	// storage failure; it must not publish an incomplete snapshot.
	jobs.Process(ctx, f.s, lease, slog.New(slog.NewTextHandler(io.Discard, nil)))
	var state, code string
	err = f.s.Pool.QueryRow(ctx, `SELECT state,last_error_code FROM jobs WHERE id=$1`, lease.ID).Scan(&state, &code)
	if err != nil || state != "queued" || code != "analysis_storage_transient" {
		t.Fatal("worker retry classification", state, code, err)
	}
	if err = blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if count(t, f, "health_samples") != 3 || count(t, f, "workout_imports") != 1 || count(t, f, "analysis_publications") != 0 {
		t.Fatal("failed snapshot changed durable facts/publication")
	}
	_, err = f.s.Pool.Exec(ctx, `UPDATE jobs SET run_after=clock_timestamp() WHERE id=$1`, lease.ID)
	if err != nil {
		t.Fatal(err)
	}
	lease, err = f.s.Claim(ctx, []string{"analysis.workout"})
	if err != nil {
		t.Fatal(err)
	}
	jobs.Process(ctx, f.s, lease, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err = f.s.Pool.QueryRow(ctx, `SELECT state FROM jobs WHERE id=$1`, lease.ID).Scan(&state); err != nil || state != "succeeded" {
		t.Fatal("worker did not recover", state, err)
	}
}

func TestPhase4ImportConnectionLossRollsBack(t *testing.T) {
	f := setup(t)
	b, raw := testWorkoutBundle(t, f, 3)
	if _, err := f.s.Pool.Exec(context.Background(), `CREATE FUNCTION phase4_connection_lost() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_terminate_backend(pg_backend_pid()); RETURN NEW; END $$; CREATE TRIGGER phase4_connection_lost BEFORE INSERT ON workout_imports FOR EACH ROW EXECUTE FUNCTION phase4_connection_lost()`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ImportWorkoutBundle(context.Background(), f.p, b, raw); err == nil {
		t.Fatal("connection loss accepted")
	}
	for _, table := range []string{"workouts", "health_samples", "workout_samples", "workout_routes", "route_points", "workout_imports", "dirty_scopes", "jobs", "change_events"} {
		if count(t, f, table) != 0 {
			t.Fatal("partial commit after connection loss", table)
		}
	}
	if _, err := f.s.Pool.Exec(context.Background(), `DROP TRIGGER phase4_connection_lost ON workout_imports; DROP FUNCTION phase4_connection_lost()`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ImportWorkoutBundle(context.Background(), f.p, b, raw); err != nil {
		t.Fatal("original bundle could not retry", err)
	}
}

func TestPhase4StatementTimeoutAndQueryCancel(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	tx, err := f.s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout='50ms'`); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = tx.Exec(ctx, `SELECT pg_sleep(1)`)
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "57014" || time.Since(start) > time.Second {
		t.Fatal("finite statement timeout not effective", err)
	}
	_ = tx.Rollback(ctx)
	short, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	_, err = f.s.Pool.Exec(short, `SELECT sum(n) FROM generate_series(1,1000000000) n`)
	cancel()
	if err == nil {
		t.Fatal("CPU query cancellation not effective")
	}
	var one int
	if err = f.s.Pool.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil || one != 1 {
		t.Fatal("pool did not recover after cancel", err)
	}
}
