//go:build integration

package admin

import (
	"context"
	"database/sql"
	"github.com/pressly/goose/v3"
	"rai/server/migrations"
	"testing"
)

func TestVO2Migration23ToLatestPreservesDailyCoverage(t *testing.T) {
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
	if _, err = p.UpTo(ctx, 23); err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO datasets(id,epoch,service_id) VALUES('10000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000002','10000000-0000-4000-8000-000000000003');
 INSERT INTO devices(dataset_id,id,token_sha256,can_collect) VALUES('10000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000006',decode(repeat('ab',32),'hex'),true);
 INSERT INTO collector_leases(dataset_id,device_id,collector_epoch) VALUES('10000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000006','10000000-0000-4000-8000-000000000005');
 INSERT INTO sync_streams(dataset_id,id,collector_epoch,sample_type,query_fingerprint) VALUES('10000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000004','10000000-0000-4000-8000-000000000005','HKQuantityTypeIdentifierRestingHeartRate','sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa');
 INSERT INTO daily_health_coverage(dataset_id,stream_id,bootstrap_start_us,last_discovery_at_us,bootstrap_complete,has_more,sample_count,deleted_count,source_context)
 VALUES('10000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000004',1700000000000000,1700001000000000,true,false,0,0,'[]')`)
	if err != nil {
		t.Fatal(err)
	}
	var before, after string
	if err = db.QueryRowContext(ctx, `SELECT to_jsonb(c)::text FROM daily_health_coverage c`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = Migrate(ctx, dsn); err != nil {
			t.Fatal(err)
		}
	}
	if err = db.QueryRowContext(ctx, `SELECT (to_jsonb(c)-'flow_created_at_us'-'query_completed_at_us'-'anchor_present'-'source_summary'-'summary_observation_seq')::text FROM daily_health_coverage c`).Scan(&after); err != nil || before != after {
		t.Fatal("Daily coverage changed", err)
	}
	var nullable bool
	if err = db.QueryRowContext(ctx, `SELECT flow_created_at_us IS NULL AND query_completed_at_us IS NULL AND anchor_present IS NULL AND source_summary IS NULL AND summary_observation_seq IS NULL FROM daily_health_coverage`).Scan(&nullable); err != nil || !nullable {
		t.Fatal("old row fabricated VO2 context", err)
	}
}
