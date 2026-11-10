//go:build integration

package integration

import (
	"context"
	"errors"
	neturl "net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/admin"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
)

const kind = "HKQuantityTypeIdentifierHeartRate"

type fixture struct {
	s          *postgres.Store
	enrollment admin.Enrollment
	p          syncing.Principal
}

func setup(t *testing.T) *fixture {
	t.Helper()
	return setupWithMigration(t, admin.Migrate)
}
func setupWithMigration(t *testing.T, migrate func(context.Context, string) error) *fixture {
	t.Helper()
	ctx := context.Background()
	url := os.Getenv("RAI_TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("RAI_TEST_DATABASE_URL is required; integration tests must not silently skip")
	}
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database != "rai_test" {
		t.Fatal("test root database must be named rai_test")
	}
	base, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	name := "rai_test_" + strings.ReplaceAll(syncing.NewID(), "-", "")
	if _, err = base.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		base.Close(ctx)
		t.Fatal(err)
	}
	// pgx.Config.ConnString returns the original DSN, not mutated struct fields.
	// Rewrite the URI explicitly so migrations can never target the root DB.
	uri, err := neturl.Parse(url)
	if err != nil || (uri.Scheme != "postgres" && uri.Scheme != "postgresql") {
		t.Fatal("test URL must be a PostgreSQL URI")
	}
	uri.Path = "/" + name
	url = uri.String()
	t.Cleanup(func() {
		_, err := base.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		if err != nil {
			t.Error(err)
		}
		base.Close(context.Background())
	})
	if err = migrate(ctx, url); err != nil {
		t.Fatal(err)
	}
	s, err := postgres.Open(ctx, url, 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Pool.Close)
	enrollment, err := admin.Init(ctx, s.Pool)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Authenticate(ctx, enrollment.Token)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{s: s, enrollment: enrollment, p: p}
}
func sample(id string, value float64) syncing.Event {
	stamp := int64(1793059200000001)
	return syncing.Event{Op: "upsert", Kind: "sample", Type: kind, UUID: id, StartUs: &stamp, EndUs: &stamp, Quantity: &syncing.Quantity{Value: value, Unit: "count/min"}, Source: &syncing.Source{BundleID: "synthetic.test"}}
}
func deleted(id string) syncing.Event {
	return syncing.Event{Op: "delete", Kind: "sample", Type: kind, UUID: id}
}
func requireCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *syncing.Error
	if !errors.As(err, &e) || e.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
}
func count(t *testing.T, f *fixture, table string) int {
	t.Helper()
	var n int
	if err := f.s.Pool.QueryRow(context.Background(), "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
