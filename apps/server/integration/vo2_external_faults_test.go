//go:build integration

package integration

import (
	"context"
	ve "rai/server/internal/vo2maxexternal"
	"testing"
)

func TestVO2ExternalPublicationFaultsRollback(t *testing.T) {
	for _, stage := range []string{"artifact", "result", "publication", "dirty_clean", "run_terminal", "job_terminal", "attempt_terminal"} {
		t.Run(stage, func(t *testing.T) {
			f, _, _ := externalFixture(t)
			ctx := context.Background()
			l := claimAnalysis(t, f, ve.JobKind)
			in, id, ms := computeExternal(t, f, l)
			table, op, condition := "analysis_artifacts", "INSERT", "NEW.artifact_key='vo2max_external_projection'"
			switch stage {
			case "result":
				table = "analysis_results"
				condition = "NEW.metric='vo2max_external_value'"
			case "publication":
				table = "analysis_publications"
				condition = "true"
			case "dirty_clean":
				table = "dirty_scopes"
				op = "UPDATE"
				condition = "NEW.clean_generation>OLD.clean_generation"
			case "run_terminal":
				table = "analysis_runs"
				op = "UPDATE"
				condition = "NEW.status='SUCCEEDED'"
			case "attempt_terminal":
				table = "job_attempts"
				op = "UPDATE"
				condition = "NEW.finished_at IS NOT NULL"
			case "job_terminal":
				table = "jobs"
				op = "UPDATE"
				condition = "NEW.state='succeeded'"
			}
			if _, err := f.s.Pool.Exec(ctx, `CREATE FUNCTION b3_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF `+condition+` THEN RAISE EXCEPTION 'synthetic_b3_fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER b3_fault BEFORE `+op+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION b3_fault()`); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.PublishAnalysis(ctx, l, id, in, ms); err == nil {
				t.Fatal("fault succeeded")
			}
			assertExternalRollback(t, f, id)
			if _, err := f.s.Pool.Exec(ctx, `DROP TRIGGER b3_fault ON `+table); err != nil {
				t.Fatal(err)
			}
			if status, err := f.s.PublishAnalysis(ctx, l, id, in, ms); err != nil || status != "SUCCEEDED" {
				t.Fatal("retry", status, err)
			}
		})
	}
}
func assertExternalRollback(t *testing.T, f *fixture, id string) {
	t.Helper()
	var partial int
	var status, state string
	var clean bool
	err := f.s.Pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM analysis_artifacts WHERE run_id=$1)+(SELECT count(*) FROM analysis_results WHERE run_id=$1)+(SELECT count(*) FROM analysis_publications WHERE published_run_id=$1),r.status,j.state,d.generation=d.clean_generation FROM analysis_runs r JOIN jobs j ON j.id=r.job_id JOIN dirty_scopes d ON d.dataset_id=r.dataset_id AND d.scope_key=r.scope_key WHERE r.id=$1`, id).Scan(&partial, &status, &state, &clean)
	if err != nil || partial != 0 || status != "RUNNING" || state != "running" || clean {
		t.Fatal("partial publication after fault/crash", err, partial, status, state, clean)
	}
}
