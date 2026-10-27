package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"rai/server/internal/syncing"
)

type healthStore struct {
	syncing.Store
	err error
}

func (s healthStore) Ready(context.Context) error { return s.err }

func TestHealthAndReadiness(t *testing.T) {
	for _, tc := range []struct {
		err   error
		ready int
	}{{nil, 200}, {errors.New("unavailable"), 503}} {
		h := New(healthStore{err: tc.err}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
		for path, want := range map[string]int{"/health": 200, "/livez": 200, "/ready": tc.ready, "/readyz": tc.ready} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
			if w.Code != want {
				t.Errorf("%s returned %d, want %d", path, w.Code, want)
			}
		}
	}
}
