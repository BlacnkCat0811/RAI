//go:build integration

package integration

import (
	"context"
	pf "rai/server/internal/performance"
	"testing"
	"time"
)

// These are real PostgreSQL backend deaths after an actual artifact INSERT.
// The advisory lock is a test-only barrier; no new production lock is used.
func TestPerformanceBackendCrashAtomicity(t *testing.T) {
	for _, stage := range []string{"after_evidence", "before_publication", "before_clean"} {
		t.Run(stage, func(t *testing.T) {
			f, _ := performanceFixture(t, true)
			ctx := context.Background()
			kind := pf.JobKind
			l := claimAnalysis(t, f, kind)
			in, id, ms := computePerformance(t, f, l)
			blocker, err := f.s.Pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Release()
			var blockerPID int
			if err = blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
				t.Fatal(err)
			}
			if _, err = blocker.Exec(ctx, `SELECT pg_advisory_lock(91723001)`); err != nil {
				t.Fatal(err)
			}
			defer blocker.Exec(ctx, `SELECT pg_advisory_unlock_all()`)
			table, op, condition := "analysis_artifacts", "INSERT", "NEW.artifact_key='performance_curve'"
			if stage == "before_publication" {
				table = "analysis_publications"
				condition = "true"
			}
			if stage == "before_clean" {
				table = "dirty_scopes"
				op = "UPDATE"
				condition = "NEW.scope_key LIKE 'model/%' AND NEW.clean_generation>OLD.clean_generation"
			}
			if _, err = f.s.Pool.Exec(ctx, `CREATE FUNCTION m3_crash_barrier() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF `+condition+` THEN PERFORM pg_advisory_xact_lock(91723001); END IF; RETURN NEW; END $$; CREATE TRIGGER m3_crash_barrier BEFORE `+op+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION m3_crash_barrier()`); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { _, e := f.s.PublishAnalysis(ctx, l, id, in, ms); done <- e }()
			deadline := time.Now().Add(2 * time.Second)
			pid := 0
			for time.Now().Before(deadline) {
				if err = f.s.Pool.QueryRow(ctx, `SELECT COALESCE((SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)) AND wait_event_type='Lock' LIMIT 1),0)`, blockerPID).Scan(&pid); err != nil {
					t.Fatal(err)
				}
				if pid > 0 {
					break
				}
				time.Sleep(2 * time.Millisecond)
			}
			if pid == 0 {
				t.Fatal("publisher did not reach crash barrier")
			}
			var killed bool
			if err = f.s.Pool.QueryRow(ctx, `SELECT pg_terminate_backend($1)`, pid).Scan(&killed); err != nil || !killed {
				t.Fatal("termination failed", err)
			}
			if err = <-done; err == nil {
				t.Fatal("dead backend published")
			}
			var partial int
			if err = f.s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM analysis_artifacts WHERE run_id=$1)+(SELECT count(*) FROM analysis_results WHERE run_id=$1)+(SELECT count(*) FROM analysis_publications WHERE published_run_id=$1)`, id).Scan(&partial); err != nil || partial != 0 {
				t.Fatal("crash leaked partial current state", err)
			}
			var status string
			var clean bool
			if err = f.s.Pool.QueryRow(ctx, `SELECT r.status,d.generation=d.clean_generation FROM analysis_runs r JOIN dirty_scopes d ON d.dataset_id=r.dataset_id AND d.scope_key=r.scope_key WHERE r.id=$1`, id).Scan(&status, &clean); err != nil || status != "RUNNING" || clean {
				t.Fatal("crash cleaned generation", err)
			}
			if _, err = f.s.Pool.Exec(ctx, `DROP TRIGGER m3_crash_barrier ON `+table); err != nil {
				t.Fatal(err)
			}
			if _, err = blocker.Exec(ctx, `SELECT pg_advisory_unlock_all()`); err != nil {
				t.Fatal(err)
			}
			if _, err = f.s.PublishAnalysis(ctx, l, id, in, ms); err != nil {
				t.Fatal("crash replay failed", err)
			}

		})
	}
}
