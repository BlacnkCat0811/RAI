//go:build integration

package admin

import (
	"context"
	"database/sql"
	"github.com/pressly/goose/v3"
	"rai/server/migrations"
	"testing"
)

func TestDatasetMigration17To18AndRepeatedMigrate(t *testing.T) {
	ctx := context.Background()
	url := isolatedDatabase(t)
	db, e := sql.Open("pgx", url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	provider, e := goose.NewProvider(goose.DialectPostgres, db, migrations.Files)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = provider.UpTo(ctx, 17); e != nil {
		t.Fatal(e)
	}
	if _, e = db.ExecContext(ctx, `INSERT INTO datasets(id,epoch,service_id,commit_rev) VALUES('10000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000002','10000000-0000-4000-8000-000000000003',42)`); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if _, e = provider.UpTo(ctx, 18); e != nil {
			t.Fatal(e)
		}
	}
	var rev int
	var active bool
	if e = db.QueryRowContext(ctx, `SELECT commit_rev,retired_at IS NULL FROM datasets`).Scan(&rev, &active); e != nil || rev != 42 || !active {
		t.Fatal("migration changed existing identity or revision", e)
	}
	var version, operations int
	if e = db.QueryRowContext(ctx, `SELECT max(version_id) FROM goose_db_version WHERE is_applied`).Scan(&version); e != nil || version != 18 {
		t.Fatal("not schema 18", e)
	}
	if e = db.QueryRowContext(ctx, `SELECT count(*) FROM admin_operations`).Scan(&operations); e != nil || operations != 0 {
		t.Fatal("migration wrote operations", e)
	}
	// Preserve the explicit 17->18 assertion above, then exercise the current
	// migration command twice as before (now expand-only 18->19).
	for i := 0; i < 2; i++ {
		if e = Migrate(ctx, url); e != nil {
			t.Fatal(e)
		}
	}
	if e = db.QueryRowContext(ctx, `SELECT max(version_id) FROM goose_db_version WHERE is_applied`).Scan(&version); e != nil || int64(version) != migrations.LatestVersion() {
		t.Fatal("current schema not latest", e)
	}

}
