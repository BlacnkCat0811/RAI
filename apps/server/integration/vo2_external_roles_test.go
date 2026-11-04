//go:build integration

package integration

import (
	"context"
	"github.com/jackc/pgx/v5"
	"os"
	"rai/server/internal/jobs"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
	ve "rai/server/internal/vo2maxexternal"
	"strings"
	"testing"
	"time"
)

func TestVO2ExternalRuntimeRoleBoundary(t *testing.T) {
	f, b, _ := externalFixture(t)
	ctx := context.Background()
	suffix := strings.ReplaceAll(syncing.NewID(), "-", "")
	apiRole, workerRole := "rai_api_"+suffix, "rai_worker_"+suffix
	raw, err := os.ReadFile("../../../deploy/postgres/roles.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ReplaceAll(strings.ReplaceAll(string(raw), "rai_api", apiRole), "rai_worker", workerRole)
	if _, err = f.s.Pool.Exec(ctx, sql); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, e := f.s.Pool.Exec(context.Background(), "DROP OWNED BY "+pgx.Identifier{apiRole}.Sanitize()+", "+pgx.Identifier{workerRole}.Sanitize()+"; DROP ROLE "+pgx.Identifier{apiRole}.Sanitize()+", "+pgx.Identifier{workerRole}.Sanitize()); e != nil {
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
	if err = worker.RuntimeRoleSafe(ctx); err != nil {
		t.Fatal(err)
	}
	if err = api.RuntimeRoleSafe(ctx); err != nil {
		t.Fatal(err)
	}
	for _, sql := range []string{`SELECT * FROM analysis_artifacts`, `INSERT INTO athlete_model_inputs(dataset_id,id) VALUES(NULL,NULL)`, `UPDATE analysis_model_channels SET stack_id='legacy'`, `DELETE FROM analysis_publications`} {
		if _, err = api.Pool.Exec(ctx, sql); err == nil {
			t.Fatal("API candidate/admin privilege")
		}
	}
	for _, sql := range []string{`UPDATE health_samples SET value_numeric=0`, `UPDATE workouts SET payload='{}'`, `UPDATE athlete_model_inputs SET provenance='x'`, `UPDATE analysis_model_channels SET assignment_version=2`, `UPDATE analysis_artifacts SET payload='{}'`} {
		if _, err = worker.Pool.Exec(ctx, sql); err == nil {
			t.Fatal("worker mutation privilege")
		}
	}
	for _, kind := range []string{ve.JobKind} {
		lease, e := worker.Claim(ctx, []string{kind})
		if e != nil {
			t.Fatal(e)
		}
		jobs.Process(ctx, worker, lease, analysisLog)
	}
	if _, _, err = f.s.CandidateVO2External(ctx, f.p.DatasetID, time.Now().UTC()); err != nil {
		t.Fatal("restricted external worker", err)
	}
	for _, statement := range []string{`UPDATE analysis_artifacts SET payload='{}'`, `DELETE FROM analysis_artifacts`, `UPDATE analysis_model_channels SET extension_configs='{}'`} {
		if _, err = worker.Pool.Exec(ctx, statement); err == nil {
			t.Fatal("worker mutated candidate history/manifest")
		}
	}
	// An API-role fact deletion invokes only the reviewed definer invalidator.
	original := f.s
	f.s = api
	d := nextVO2(b)
	d.DeletedUUIDs = []string{b.Facts[0].UUID}
	_, err = commitVO2(t, f, d)
	f.s = original
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.s.CandidateVO2External(ctx, f.p.DatasetID, time.Now().UTC()); err == nil {
		t.Fatal("restricted deletion left external current")
	}
}
