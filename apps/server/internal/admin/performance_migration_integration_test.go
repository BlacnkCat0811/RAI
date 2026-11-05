//go:build integration

package admin

import (
	"context"
	"database/sql"
	"errors"
	"github.com/pressly/goose/v3"
	"rai/server/migrations"
	"testing"
)

func TestPerformanceMigration22ToLatestRepeat(t *testing.T) {
	ctx := context.Background()
	dsn := isolatedDatabase(t)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.UpTo(ctx, 22); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, `INSERT INTO datasets(id,epoch,service_id,commit_rev) VALUES('10000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000002','10000000-0000-4000-8000-000000000003',42)`); err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err = db.QueryRowContext(ctx, `SELECT to_jsonb(d)::text FROM datasets d`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = Migrate(ctx, dsn); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.QueryRowContext(ctx, `SELECT to_jsonb(d)::text FROM datasets d`).Scan(&after); err != nil || before != after {
		t.Fatal("migration changed facts", err)
	}
	var version, tables int
	if err = db.QueryRowContext(ctx, `SELECT (SELECT max(version_id) FROM goose_db_version WHERE is_applied),(SELECT count(*) FROM information_schema.tables WHERE table_schema='public')`).Scan(&version, &tables); err != nil || int64(version) != migrations.LatestVersion() || tables != 54 {
		t.Fatal("schema", version, tables, err)
	}
	if err = MigrateDown(ctx, dsn); !errors.Is(err, ErrMigrationIrreversible) {
		t.Fatal("destructive rollback")
	}
}
