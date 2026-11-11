//go:build integration

package admin

import (
	"context"
	"database/sql"
	"rai/server/migrations"
	"testing"

	"github.com/pressly/goose/v3"
)

func TestReadinessMigration26ToLatest(t *testing.T) {
	ctx := context.Background()
	dsn := isolatedDatabase(t)
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	p, e := goose.NewProvider(goose.DialectPostgres, db, migrations.Files)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = p.UpTo(ctx, 26); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if e = Migrate(ctx, dsn); e != nil {
			t.Fatal(e)
		}
	}
	var v int64
	if e = db.QueryRowContext(ctx, `SELECT max(version_id) FROM goose_db_version WHERE is_applied`).Scan(&v); e != nil || v != migrations.LatestVersion() {
		t.Fatal("26->latest", e, v)
	}
}
