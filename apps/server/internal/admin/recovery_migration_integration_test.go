//go:build integration

package admin

import (
	"context"
	"database/sql"
	"testing"

	"github.com/pressly/goose/v3"
	"rai/server/migrations"
)

func TestRecoveryMigration18To19AndRepeatedMigrate(t *testing.T) {
	ctx := context.Background()
	dsn := isolatedDatabase(t)
	db, e := sql.Open("pgx", dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	provider, e := goose.NewProvider(goose.DialectPostgres, db, migrations.Files)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = provider.UpTo(ctx, 18); e != nil {
		t.Fatal(e)
	}
	_, e = db.ExecContext(ctx, `INSERT INTO datasets(id,epoch,service_id,commit_rev) VALUES('10000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000002','10000000-0000-4000-8000-000000000003',42),('10000000-0000-4000-8000-000000000004','10000000-0000-4000-8000-000000000005','10000000-0000-4000-8000-000000000006',0); INSERT INTO admin_operations(operation_id,operation_type,source_dataset,target_dataset,actor,status) VALUES('10000000-0000-4000-8000-000000000007','new_dataset','10000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000004','synthetic','committed')`)
	if e != nil {
		t.Fatal(e)
	}
	var before string
	if e = db.QueryRowContext(ctx, `SELECT to_jsonb(a)::text FROM admin_operations a`).Scan(&before); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if e = Migrate(ctx, dsn); e != nil {
			t.Fatal(e)
		}
	}
	var after string
	if e = db.QueryRowContext(ctx, `SELECT to_jsonb(a)::text FROM admin_operations a`).Scan(&after); e != nil || after != before {
		t.Fatal("existing audit changed")
	}
	var version, rev int
	if e = db.QueryRowContext(ctx, `SELECT max(version_id) FROM goose_db_version WHERE is_applied`).Scan(&version); e != nil || version != 19 {
		t.Fatal("schema not 19")
	}
	if e = db.QueryRowContext(ctx, `SELECT commit_rev FROM datasets WHERE id='10000000-0000-4000-8000-000000000001'`).Scan(&rev); e != nil || rev != 42 {
		t.Fatal("existing revision changed")
	}
}
