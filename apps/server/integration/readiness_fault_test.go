//go:build integration

package integration

import (
	"context"
	"github.com/jackc/pgx/v5"
	"os"
	"rai/server/internal/admin"
	"rai/server/internal/analysis"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
	"strings"
	"testing"
	"time"
)

func readinessNoPartial(t *testing.T, f *fixture, id string) {
	t.Helper()
	ctx := context.Background()
	var n int
	for _, table := range []string{"analysis_artifacts", "analysis_results"} {
		if e := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM `+table+` WHERE run_id=$1`, id).Scan(&n); e != nil || n != 0 {
			t.Fatal("partial model output", table, e)
		}
	}
	if e := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM analysis_publications WHERE published_run_id=$1`, id).Scan(&n); e != nil || n != 0 {
		t.Fatal("partial publication", e)
	}
	var status, job string
	var clean bool
	if e := f.s.Pool.QueryRow(ctx, `SELECT r.status,j.state,d.generation=d.clean_generation FROM analysis_runs r JOIN jobs j ON j.id=r.job_id JOIN dirty_scopes d ON d.dataset_id=r.dataset_id AND d.scope_key=r.scope_key WHERE r.id=$1`, id).Scan(&status, &job, &clean); e != nil || status != "RUNNING" || job != "running" || clean {
		t.Fatal("partial terminal transition", e)
	}
}
func TestReadinessFaultRollback(t *testing.T) {
	for _, stage := range []string{"snapshot", "artifact", "result", "publication", "dirty_clean", "run_terminal", "job_terminal", "attempt_terminal"} {
		t.Run(stage, func(t *testing.T) {
			f, _, _ := readinessFixture(t)
			ctx := context.Background()
			l := latestReadinessLease(t, f)
			table, op, condition := "analysis_runs", "INSERT", "NEW.algorithm_name='rai.readiness.training'"
			switch stage {
			case "artifact":
				table = "analysis_artifacts"
				condition = "NEW.artifact_key='training_readiness'"
			case "result":
				table = "analysis_results"
				condition = "NEW.metric='training_readiness'"
			case "publication":
				table = "analysis_publications"
				condition = "NEW.metric_family='training_readiness_candidate'"
			case "dirty_clean":
				table = "dirty_scopes"
				op = "UPDATE"
				condition = "NEW.scope_key LIKE 'instant/readiness/v1/%' AND NEW.clean_generation>OLD.clean_generation"
			case "run_terminal":
				op = "UPDATE"
				condition = "NEW.algorithm_name='rai.readiness.training' AND NEW.status='SUCCEEDED'"
			case "job_terminal":
				table = "jobs"
				op = "UPDATE"
				condition = "NEW.kind='analysis.training_readiness' AND NEW.state='succeeded'"
			case "attempt_terminal":
				table = "job_attempts"
				op = "UPDATE"
				condition = "NEW.finished_at IS NOT NULL"
			}
			in, e := f.s.LoadAnalysis(ctx, l, time.Now())
			if e != nil {
				t.Fatal(e)
			}
			var id string
			var ms []analysis.Metric
			if stage != "snapshot" {
				id, e = f.s.StartAnalysisRun(ctx, l, in)
				if e != nil {
					t.Fatal(e)
				}
				ms, e = analysis.CalculateCandidateReadiness(&in)
				if e != nil {
					t.Fatal(e)
				}
			}
			if _, e = f.s.Pool.Exec(ctx, `CREATE FUNCTION m6_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF `+condition+` THEN RAISE EXCEPTION 'synthetic M6 failure' USING ERRCODE='23514'; END IF; RETURN NEW; END $$; CREATE TRIGGER m6_fault BEFORE `+op+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION m6_fault()`); e != nil {
				t.Fatal(e)
			}
			if stage == "snapshot" {
				if _, e = f.s.StartAnalysisRun(ctx, l, in); e == nil {
					t.Fatal("snapshot capture fault did not abort")
				}
			} else {
				if _, e = f.s.PublishAnalysis(ctx, l, id, in, ms); e == nil {
					t.Fatal("fault did not abort")
				}
				readinessNoPartial(t, f, id)
			}
			if _, e = f.s.Pool.Exec(ctx, `DROP TRIGGER m6_fault ON `+table); e != nil {
				t.Fatal(e)
			}
			if stage == "snapshot" {
				id, e = f.s.StartAnalysisRun(ctx, l, in)
				if e != nil {
					t.Fatal(e)
				}
				ms, e = analysis.CalculateCandidateReadiness(&in)
				if e != nil {
					t.Fatal(e)
				}
			}
			if status, e := f.s.PublishAnalysis(ctx, l, id, in, ms); e != nil || status != "SUCCEEDED" {
				t.Fatal("fault replay", e, status)
			}
		})
	}
}
func TestReadinessBackendCrashAtomicity(t *testing.T) {
	for _, stage := range []string{"after_artifact", "before_publication", "before_clean"} {
		t.Run(stage, func(t *testing.T) {
			f, _, _ := readinessFixture(t)
			ctx := context.Background()
			l := latestReadinessLease(t, f)
			in, id, ms := computeReadiness(t, f, l)
			blocker, e := f.s.Pool.Acquire(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer blocker.Release()
			var blockerPID int
			if e = blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); e != nil {
				t.Fatal(e)
			}
			if _, e = blocker.Exec(ctx, `SELECT pg_advisory_lock(91727001)`); e != nil {
				t.Fatal(e)
			}
			defer blocker.Exec(ctx, `SELECT pg_advisory_unlock_all()`)
			table, op, cond := "analysis_results", "INSERT", "NEW.metric='training_readiness'"
			if stage == "before_publication" {
				table = "analysis_publications"
				cond = "NEW.metric_family='training_readiness_candidate'"
			}
			if stage == "before_clean" {
				table = "dirty_scopes"
				op = "UPDATE"
				cond = "NEW.scope_key LIKE 'instant/readiness/v1/%' AND NEW.clean_generation>OLD.clean_generation"
			}
			if _, e = f.s.Pool.Exec(ctx, `CREATE FUNCTION m6_crash() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF `+cond+` THEN PERFORM pg_advisory_xact_lock(91727001); END IF; RETURN NEW; END $$; CREATE TRIGGER m6_crash BEFORE `+op+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION m6_crash()`); e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() { _, err := f.s.PublishAnalysis(ctx, l, id, in, ms); done <- err }()
			deadline := time.Now().Add(3 * time.Second)
			pid := 0
			for time.Now().Before(deadline) {
				if e = f.s.Pool.QueryRow(ctx, `SELECT COALESCE((SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)) AND wait_event_type='Lock' LIMIT 1),0)`, blockerPID).Scan(&pid); e != nil {
					t.Fatal(e)
				}
				if pid > 0 {
					break
				}
				time.Sleep(2 * time.Millisecond)
			}
			if pid == 0 {
				t.Fatal("crash barrier not reached")
			}
			var killed bool
			if e = f.s.Pool.QueryRow(ctx, `SELECT pg_terminate_backend($1)`, pid).Scan(&killed); e != nil || !killed {
				t.Fatal(e)
			}
			if e = <-done; e == nil {
				t.Fatal("dead backend published")
			}
			readinessNoPartial(t, f, id)
			if _, e = f.s.Pool.Exec(ctx, `DROP TRIGGER m6_crash ON `+table); e != nil {
				t.Fatal(e)
			}
			if _, e = blocker.Exec(ctx, `SELECT pg_advisory_unlock_all()`); e != nil {
				t.Fatal(e)
			}
			if status, e := f.s.PublishAnalysis(ctx, l, id, in, ms); e != nil || status != "SUCCEEDED" {
				t.Fatal("crash replay", status, e)
			}
		})
	}
}
func TestReadinessForgedOutputs(t *testing.T) {
	for _, what := range []string{"score", "baseline", "metrics", "snapshot"} {
		t.Run(what, func(t *testing.T) {
			f, _, _ := readinessFixture(t)
			l := latestReadinessLease(t, f)
			in, id, ms := computeReadiness(t, f, l)
			switch what {
			case "score":
				in.ReadinessArtifact.Score = analysis.Ptr(99)
			case "baseline":
				in.ReadinessArtifact.Baseline.Value = analysis.Ptr(1)
			case "metrics":
				ms[0].Value = analysis.Ptr(99)
			case "snapshot":
				in.ReadinessInput.Config.TauUs++
			}
			if status, e := f.s.PublishAnalysis(context.Background(), l, id, in, ms); e == nil && status != "SUPERSEDED" {
				t.Fatal("forged result accepted")
			}
			if what != "snapshot" {
				readinessNoPartial(t, f, id)
			} else {
				var n int
				if e := f.s.Pool.QueryRow(context.Background(), `SELECT count(*) FROM analysis_artifacts WHERE run_id=$1`, id).Scan(&n); e != nil || n != 0 {
					t.Fatal("forged snapshot artifact", e)
				}
			}
		})
	}
}
func TestReadinessRolesAndRawRollback(t *testing.T) {
	f, ws, _ := readinessFixture(t)
	ctx := context.Background()
	suffix := strings.ReplaceAll(syncing.NewID(), "-", "")
	apiRole, workerRole := "rai_api_"+suffix, "rai_worker_"+suffix
	raw, e := os.ReadFile("../../../deploy/postgres/roles.sql")
	if e != nil {
		t.Fatal(e)
	}
	sql := strings.ReplaceAll(strings.ReplaceAll(string(raw), "rai_api", apiRole), "rai_worker", workerRole)
	if _, e = f.s.Pool.Exec(ctx, sql); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if _, e := f.s.Pool.Exec(ctx, "DROP OWNED BY "+pgx.Identifier{apiRole}.Sanitize()+", "+pgx.Identifier{workerRole}.Sanitize()+"; DROP ROLE "+pgx.Identifier{apiRole}.Sanitize()+", "+pgx.Identifier{workerRole}.Sanitize()); e != nil {
			t.Error(e)
		}
	})
	open := func(role string) *postgres.Store {
		dsn := f.s.Pool.Config().ConnString()
		sep := "&"
		if !strings.Contains(dsn, "?") {
			sep = "?"
		}
		s, e := postgres.Open(ctx, dsn+sep+"options=-crole%3D"+role, 2)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(s.Pool.Close)
		return s
	}
	worker, api := open(workerRole), open(apiRole)
	for _, s := range []*postgres.Store{worker, api} {
		if e = s.RuntimeRoleSafe(ctx); e != nil {
			t.Fatal(e)
		}
	}
	for _, q := range []string{`SELECT * FROM analysis_artifacts`, `SELECT * FROM analysis_load_ledger`, `UPDATE analysis_model_channels SET extension_configs='{}'`, `DELETE FROM analysis_publications`} {
		if _, e = api.Pool.Exec(ctx, q); e == nil {
			t.Fatal("API candidate privilege")
		}
	}
	for _, q := range []string{`UPDATE workouts SET payload='{}'`, `UPDATE daily_health_coverage SET has_more=false`, `UPDATE analysis_model_channels SET extension_configs='{}'`, `UPDATE analysis_artifacts SET payload='{}'`} {
		if _, e = worker.Pool.Exec(ctx, q); e == nil {
			t.Fatal("worker raw privilege")
		}
	}
	old := f.s
	f.s = worker
	runReadinessLatest(t, f)
	f.s = old
	r, id := readReadiness(t, f)
	if r.Score == nil {
		t.Fatal("restricted worker unavailable", r.Reasons)
	}
	// Force a raw delete to fail after its invalidating trigger has run.
	if _, e = f.s.Pool.Exec(ctx, `CREATE FUNCTION m6_raw_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic raw failure' USING ERRCODE='23514'; END $$; CREATE TRIGGER zz_m6_raw_fault AFTER DELETE ON workouts FOR EACH ROW EXECUTE FUNCTION m6_raw_fault()`); e != nil {
		t.Fatal(e)
	}
	var event = syncing.Event{Op: "delete", Kind: "workout", Type: "HKWorkoutTypeIdentifier", UUID: ws[5].WorkoutID}
	// Commit through a direct transaction to verify delete + dirty + pointer rollback.
	tx, e := f.s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(ctx, `SELECT 1 FROM datasets WHERE id=$1 FOR UPDATE`, f.p.DatasetID); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(ctx, `DELETE FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2`, f.p.DatasetID, event.UUID); e == nil {
		t.Fatal("raw failure absent")
	}
	_ = tx.Rollback(ctx)
	if _, gotID := readReadiness(t, f); gotID != id {
		t.Fatal("rollback withdrew current")
	}
	if _, e = f.s.Pool.Exec(ctx, `DROP TRIGGER zz_m6_raw_fault ON workouts`); e != nil {
		t.Fatal(e)
	}
	f.s = api
	if _, e = f.s.DeleteWorkoutBundle(ctx, f.p, bundleDeleteRequest(f, event.UUID)); e != nil {
		t.Fatal(e)
	}
	f.s = old
	if _, _, e = f.s.CandidateReadiness(ctx, f.p.DatasetID, time.Now()); e == nil {
		t.Fatal("API delete left readiness live")
	}
	if e = admin.QueueReadinessCandidate(ctx, f.s, f.p.DatasetID, syncing.NewID(), "synthetic post delete"); e != nil {
		t.Fatal(e)
	}
}
