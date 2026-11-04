//go:build integration

package admin

import (
	"context"
	"database/sql"
	"github.com/pressly/goose/v3"
	"rai/server/migrations"
	"testing"
)

func TestDailyMigration19To20PreservesFactsAndRepeats(t *testing.T) {
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
	if _, err = provider.UpTo(ctx, 19); err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO datasets(id,epoch,service_id,commit_rev) VALUES('10000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000002','10000000-0000-4000-8000-000000000003',42);
 INSERT INTO devices(dataset_id,id,token_sha256,can_collect) VALUES('10000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000005',decode(repeat('cd',32),'hex'),true);
 INSERT INTO collector_leases(dataset_id,device_id,collector_epoch) VALUES('10000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000005','10000000-0000-4000-8000-000000000006');
 INSERT INTO health_samples(dataset_id,sample_type,sample_uuid,event_start,event_end,value_numeric,unit,source_bundle_id,content_sha256,entity_revision,metadata)
 VALUES('10000000-0000-4000-8000-000000000001','HKQuantityTypeIdentifierHeartRate','10000000-0000-4000-8000-000000000004',now(),now(),42,'count/min','synthetic',decode(repeat('ab',32),'hex'),42,'{"synthetic":true}')`)
	if err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err = db.QueryRowContext(ctx, `SELECT to_jsonb(s)::text FROM health_samples s`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = Migrate(ctx, dsn); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.QueryRowContext(ctx, `SELECT to_jsonb(s)::text FROM health_samples s`).Scan(&after); err != nil || before != after {
		t.Fatal("existing fact changed", err)
	}
	var version, rev, count int
	if err = db.QueryRowContext(ctx, `SELECT (SELECT max(version_id) FROM goose_db_version WHERE is_applied),commit_rev,(SELECT count(*) FROM daily_health_coverage) FROM datasets`).Scan(&version, &rev, &count); err != nil || int64(version) != migrations.LatestVersion() || rev != 42 || count != 0 {
		t.Fatal("migration rewrote state", err)
	}
}
