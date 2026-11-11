//go:build integration

package admin

import (
	"context"
	"database/sql"
	"testing"

	"rai/server/migrations"

	"github.com/pressly/goose/v3"
)

func TestUpgrade29PreservesVo2MaxSourceSelectionInput(t *testing.T) {
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
	if _, err = p.UpTo(ctx, 29); err != nil {
		t.Fatal(err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO datasets(id,epoch,service_id) VALUES('20000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000002','20000000-0000-4000-8000-000000000003');
INSERT INTO athlete_model_inputs(dataset_id,id,kind,subject_key,effective_from,schema_version,content_sha256,payload,provenance)
VALUES('20000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000004','vo2max_source_selection','athlete',now(),'rai.vo2max.source_selection/1',decode(repeat('ab',32),'hex'),'{"sourceEpoch":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","revoked":false}','test')`)
	if err != nil {
		t.Fatal(err)
	}
	if err = Migrate(ctx, dsn); err != nil {
		t.Fatal(err)
	}
	var kind, schema string
	if err = db.QueryRowContext(ctx, `SELECT kind,schema_version FROM athlete_model_inputs WHERE id='20000000-0000-4000-8000-000000000004'`).Scan(&kind, &schema); err != nil {
		t.Fatal(err)
	}
	if kind != "vo2max_source_selection" || schema != "rai.vo2max.source_selection/1" {
		t.Fatalf("input changed: %s %s", kind, schema)
	}
}
