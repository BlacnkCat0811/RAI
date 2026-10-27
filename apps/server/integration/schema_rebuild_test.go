//go:build integration

package integration

import (
	"context"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"rai/server/internal/admin"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
)

func TestFreshDatabaseSchemaRebuild(t *testing.T) {
	ctx := context.Background()
	rootURL := os.Getenv("RAI_TEST_DATABASE_URL")
	cfg, err := pgx.ParseConfig(rootURL)
	if err != nil || cfg.Database != "rai_test" {
		t.Fatal("isolated rai_test root required")
	}
	root, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close(ctx)
	name := "rai_test_" + strings.ReplaceAll(syncing.NewID(), "-", "")
	ident := pgx.Identifier{name}.Sanitize()
	defer root.Exec(ctx, "DROP DATABASE IF EXISTS "+ident+" WITH (FORCE)")
	uri, err := url.Parse(rootURL)
	if err != nil {
		t.Fatal(err)
	}
	uri.Path = "/" + name
	read := func(pool *pgxpool.Pool) []string {
		t.Helper()
		rows, err := pool.Query(context.Background(), `SELECT definition FROM (
			SELECT 'column:'||c.relname||':'||a.attnum||':'||a.attname||':'||format_type(a.atttypid,a.atttypmod)||':'||a.attnotnull||':'||coalesce(pg_get_expr(d.adbin,d.adrelid),'') AS definition
			FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_attribute a ON a.attrelid=c.oid
			LEFT JOIN pg_attrdef d ON d.adrelid=c.oid AND d.adnum=a.attnum
			WHERE n.nspname='public' AND c.relkind IN ('r','p') AND a.attnum>0 AND NOT a.attisdropped
			UNION ALL SELECT 'index:'||tablename||':'||indexname||':'||indexdef FROM pg_indexes WHERE schemaname='public'
			UNION ALL SELECT 'constraint:'||c.relname||':'||x.conname||':'||pg_get_constraintdef(x.oid)
			FROM pg_constraint x JOIN pg_class c ON c.oid=x.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public'
			UNION ALL SELECT 'trigger:'||c.relname||':'||tr.tgname||':'||pg_get_triggerdef(tr.oid)
			FROM pg_trigger tr JOIN pg_class c ON c.oid=tr.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND NOT tr.tgisinternal
			UNION ALL SELECT 'function:'||p.proname||':'||pg_get_functiondef(p.oid)
			FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' AND p.prokind='f'
		) definitions ORDER BY definition`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var result []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				t.Fatal(err)
			}
			result = append(result, s)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return result
	}
	build := func() []string {
		t.Helper()
		if _, err := root.Exec(ctx, "CREATE DATABASE "+ident); err != nil {
			t.Fatal(err)
		}
		if err := admin.Migrate(ctx, uri.String()); err != nil {
			t.Fatal(err)
		}
		s, err := postgres.Open(ctx, uri.String(), 2)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Pool.Close()
		return read(s.Pool)
	}
	first := build()
	if _, err := root.Exec(ctx, "DROP DATABASE "+ident+" WITH (FORCE)"); err != nil {
		t.Fatal(err)
	}
	second := build()
	if len(first) == 0 || !reflect.DeepEqual(first, second) {
		t.Fatal("fresh databases have different schema definitions")
	}
}
