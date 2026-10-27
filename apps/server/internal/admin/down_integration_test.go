//go:build integration

package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"
	"github.com/pressly/goose/v3"
)

func isolatedDatabase(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	baseURL := os.Getenv("RAI_TEST_DATABASE_URL")
	cfg, err := pgx.ParseConfig(baseURL)
	if err != nil || cfg.Database != "rai_test" {
		t.Fatal("RAI_TEST_DATABASE_URL must identify isolated rai_test")
	}
	base, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	name := "rai_test_down_" + hex.EncodeToString(random[:])
	if _, err := base.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := base.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
			t.Error(err)
		}
		_ = base.Close(ctx)
	})
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		t.Fatal("test URL must be a PostgreSQL URI")
	}
	u.Path = "/" + name
	return u.String()
}

func TestFrozenMigrationDownRefused(t *testing.T) {
	ctx := context.Background()
	url := isolatedDatabase(t)
	if err := Migrate(ctx, url); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `INSERT INTO datasets(id,epoch,service_id) VALUES ('10000000-0000-0000-0000-000000000001','10000000-0000-0000-0000-000000000002','10000000-0000-0000-0000-000000000003')`); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM goose_db_version`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := MigrateDown(ctx, url); !errors.Is(err, ErrMigrationIrreversible) {
		t.Fatalf("expected irreversible error, got %v", err)
	}
	var after, facts int
	if err := db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM goose_db_version), (SELECT count(*) FROM datasets)`).Scan(&after, &facts); err != nil {
		t.Fatal(err)
	}
	if before != after || facts != 1 {
		t.Fatalf("refused down changed version or facts: %d %d %d", before, after, facts)
	}
}

func TestApprovedMigrationDownRoundTrip(t *testing.T) {
	ctx := context.Background()
	url := isolatedDatabase(t)
	const body = "-- +goose Up\nCREATE TABLE rai_synthetic_marker (id integer PRIMARY KEY);\n-- +goose Down\nDROP TABLE rai_synthetic_marker;\n"
	files := fstest.MapFS{"00001_synthetic.sql": {Data: []byte(body)}}
	sum := sha256.Sum256([]byte(body))
	approved := map[int64]string{1: hex.EncodeToString(sum[:])}
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if err := migrateDown(ctx, url, files, nil); !errors.Is(err, ErrMigrationIrreversible) {
		t.Fatalf("unapproved migration accepted: %v", err)
	}
	if err := migrateDown(ctx, url, files, map[int64]string{1: "wrong hash"}); !errors.Is(err, ErrMigrationIrreversible) {
		t.Fatalf("changed migration accepted: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO goose_db_version(version_id,is_applied) VALUES (2,true)`); err != nil {
		t.Fatal(err)
	}
	if err := migrateDown(ctx, url, files, approved); !errors.Is(err, ErrMigrationIrreversible) {
		t.Fatalf("unknown migration accepted: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM goose_db_version WHERE version_id=2`); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { results <- migrateDown(ctx, url, files, approved) }()
	}
	var successes, refused int
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			successes++
		} else if errors.Is(err, ErrMigrationIrreversible) {
			refused++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || refused != 1 {
		t.Fatalf("concurrent downs: successes=%d refused=%d", successes, refused)
	}
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('rai_synthetic_marker') IS NOT NULL`).Scan(&exists); err != nil || exists {
		t.Fatalf("down did not remove synthetic table: exists=%v err=%v", exists, err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('rai_synthetic_marker') IS NOT NULL`).Scan(&exists); err != nil || !exists {
		t.Fatalf("second up did not restore synthetic table: exists=%v err=%v", exists, err)
	}
}
