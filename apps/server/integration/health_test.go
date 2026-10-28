//go:build integration

package integration

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"rai/server/internal/admin"
	"rai/server/internal/httpapi"
)

func TestHealthEndpointsWithPostgres(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	// Reapplying the complete migration set must be safe on an already migrated database.
	if err := admin.Migrate(ctx, f.s.Pool.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
	h := httpapi.New(f.s, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	check := func(wantLive, wantReady int) {
		t.Helper()
		for path, want := range map[string]int{"/health": wantLive, "/livez": wantLive, "/ready": wantReady, "/readyz": wantReady} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
			if w.Code != want {
				t.Errorf("%s: got %d, want %d", path, w.Code, want)
			}
		}
	}
	check(200, 200)
	if _, err := f.s.Pool.Exec(ctx, `INSERT INTO goose_db_version (version_id, is_applied) VALUES (999, true)`); err != nil {
		t.Fatal(err)
	}
	check(200, 503)
	if _, err := f.s.Pool.Exec(ctx, `DELETE FROM goose_db_version WHERE version_id = 999`); err != nil {
		t.Fatal(err)
	}
	check(200, 200)
	f.s.Pool.Close()
	check(200, 503)
}
