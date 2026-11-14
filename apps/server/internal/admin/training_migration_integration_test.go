//go:build integration

package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pressly/goose/v3"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
	"rai/server/internal/training"
	"rai/server/migrations"
)

func TestTrainingMigration59To61PreservesPublishedV3(t *testing.T) {
	ctx := context.Background()
	dsn := isolatedDatabase(t)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.UpTo(ctx, 59); err != nil {
		t.Fatal(err)
	}
	s, err := postgres.Open(ctx, dsn, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Pool.Close()
	enrollment, err := Init(ctx, s.Pool)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := s.Authenticate(ctx, enrollment.Token)
	if err != nil {
		t.Fatal(err)
	}
	id := syncing.NewID()
	_, err = s.CreateTrainingPlan(ctx, principal, training.CreateRequest{CommandID: syncing.NewID(), PlanID: id, Config: training.Config{
		StartDate: "2026-11-09", Weeks: 4, AvailableDays: []int{2, 4, 7}, LongRunDay: 7, BaselineWeeklyM: training.Ptr(20000),
		Goal: training.Goal{Type: "running", Date: "2026-12-06", DistanceM: 5000},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE jobs SET run_after=now() WHERE kind='training.plan'`); err != nil {
		t.Fatal(err)
	}
	lease, err := s.Claim(ctx, []string{"training.plan"})
	if err != nil {
		t.Fatal(err)
	}
	in, err := s.LoadTraining(ctx, lease, time.Date(2026, 11, 9, 4, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	run, err := s.StartTrainingRun(ctx, lease, in)
	if err != nil {
		t.Fatal(err)
	}
	base, err := training.Generate(in.Context)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := s.PublishTraining(ctx, lease, run, in, base); err != nil || status != "SUCCEEDED" {
		t.Fatalf("v3 seed not published: %s %v", status, err)
	}
	tables := []string{"datasets", "training_plans", "training_configs", "training_sessions", "training_generation_runs", "training_base_versions", "training_effective_versions", "training_overrides", "training_executions", "training_feedback", "training_commands", "jobs", "dirty_scopes"}
	snapshot := func() map[string]string {
		t.Helper()
		out := map[string]string{}
		for _, table := range tables {
			var b string
			q := `SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),'[]'::jsonb)::text FROM ` + pgx.Identifier{table}.Sanitize() + ` t`
			if err := db.QueryRowContext(ctx, q).Scan(&b); err != nil {
				t.Fatal(err)
			}
			out[table] = b
		}
		return out
	}
	before := snapshot()
	var v1 string
	if err = db.QueryRowContext(ctx, `SELECT parameters::text FROM training_parameter_sets WHERE version='coach_parameters/v1'`).Scan(&v1); err != nil {
		t.Fatal(err)
	}
	// The old schema rejects day slots beyond the fixed sixteen-week plan.
	insert := `INSERT INTO training_sessions(dataset_id,plan_id,id,origin,slot) VALUES($1,$2,$3,'generated',112)`
	if _, err = db.ExecContext(ctx, insert, principal.DatasetID, id, syncing.NewID()); err == nil {
		t.Fatal("schema 59 accepted slot 112")
	}
	for i := 0; i < 2; i++ {
		if _, err = provider.UpTo(ctx, 60); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err = Migrate(ctx, dsn); err != nil {
			t.Fatal(err)
		}
	}
	after := snapshot()
	for _, table := range tables {
		if before[table] != after[table] {
			t.Fatalf("migration rewrote %s", table)
		}
	}
	var afterV1 string
	if err = db.QueryRowContext(ctx, `SELECT parameters::text FROM training_parameter_sets WHERE version='coach_parameters/v1'`).Scan(&afterV1); err != nil || afterV1 != v1 {
		t.Fatal("v1 parameters changed", err)
	}
	for _, want := range []training.CoachParameters{training.V2Parameters(), training.V3Parameters()} {
		var raw []byte
		if err = db.QueryRowContext(ctx, `SELECT parameters FROM training_parameter_sets WHERE version=$1`, want.Version).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var got training.CoachParameters
		if err = json.Unmarshal(raw, &got); err != nil || got != want {
			t.Fatalf("wrong frozen parameters %s: %v", want.Version, err)
		}
		if _, err = db.ExecContext(ctx, `UPDATE training_parameter_sets SET parameters='{}' WHERE version=$1`, want.Version); err == nil {
			t.Fatal("parameter update allowed")
		}
		if _, err = db.ExecContext(ctx, `DELETE FROM training_parameter_sets WHERE version=$1`, want.Version); err == nil {
			t.Fatal("parameter delete allowed")
		}
	}
	if _, err = db.ExecContext(ctx, insert, principal.DatasetID, id, syncing.NewID()); err != nil {
		t.Fatal("expanded slot rejected", err)
	}
	if err = s.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if err = MigrateDown(ctx, dsn); !errors.Is(err, ErrMigrationIrreversible) {
		t.Fatal("frozen migration rollback allowed", err)
	}
}
