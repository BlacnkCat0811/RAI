//go:build integration

package integration

import (
	"context"
	"github.com/jackc/pgx/v5"
	"os"
	"rai/server/internal/admin"
	"rai/server/internal/jobs"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
	tl "rai/server/internal/trainingload"
	wf "rai/server/internal/workoutfeatures"
	"strings"
	"testing"
)

func TestLoadRuntimeRoleBoundary(t *testing.T) {
	f, b := featureFixture(t)
	if err := admin.QueueTrainingLoadCandidate(context.Background(), f.s, f.p.DatasetID, b.WorkoutID, syncing.NewID(), "synthetic roles"); err != nil {
		t.Fatal(err)
	}
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
	l, e := worker.Claim(ctx, []string{wf.JobKind})
	if e != nil {
		t.Fatal(e)
	}
	jobs.Process(ctx, worker, l, analysisLog)
	if _, _, err = f.s.CandidateFeature(ctx, f.p.DatasetID, b.WorkoutID); err != nil {
		t.Fatal("restricted worker failed", err)
	}
	for _, kind := range []string{tl.JobKind, tl.StateJobKind} {
		lease, e := worker.Claim(ctx, []string{kind})
		if e != nil {
			t.Fatal(e)
		}
		jobs.Process(ctx, worker, lease, analysisLog)
	}
	if _, _, err = f.s.CandidateLoad(ctx, f.p.DatasetID, b.WorkoutID, "2026-10-31"); err != nil {
		t.Fatal("restricted worker load", err)
	}
	if _, _, err = f.s.CandidateLoadState(ctx, f.p.DatasetID, "2026-10-31"); err != nil {
		t.Fatal("restricted worker state", err)
	}
	for _, statement := range []string{`SELECT * FROM analysis_load_ledger`, `INSERT INTO analysis_load_ledger(dataset_id) VALUES(NULL)`, `UPDATE analysis_load_ledger SET contribution=0`} {
		if _, err = api.Pool.Exec(ctx, statement); err == nil {
			t.Fatal("API ledger access")
		}
	}
	for _, statement := range []string{`UPDATE analysis_load_ledger SET contribution=0`, `DELETE FROM analysis_load_ledger`} {
		if _, err = worker.Pool.Exec(ctx, statement); err == nil {
			t.Fatal("worker history mutable")
		}
	}
	// An API-role fact deletion invokes only the reviewed definer invalidator.
	original := f.s
	f.s = api
	f.commit(t, []syncing.Event{{Op: "delete", Kind: "sample", Type: kind, UUID: b.Samples[0].UUID}}, 1, nil)
	f.s = original
	if _, _, err = f.s.CandidateFeature(ctx, f.p.DatasetID, b.WorkoutID); err == nil {
		t.Fatal("restricted API delete left feature live")
	}
	if _, _, err = f.s.CandidateLoad(ctx, f.p.DatasetID, b.WorkoutID, "2026-10-31"); err == nil {
		t.Fatal("restricted delete left load current")
	}
	if _, _, err = f.s.CandidateLoadState(ctx, f.p.DatasetID, "2026-10-31"); err == nil {
		t.Fatal("restricted delete left state current")
	}
}
