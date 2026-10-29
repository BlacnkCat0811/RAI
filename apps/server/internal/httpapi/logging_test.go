package httpapi

import (
	"bytes"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUnmatchedPathIsNeverLogged(t *testing.T) {
	var log bytes.Buffer
	h := New(nil, slog.New(slog.NewJSONHandler(&log, nil)))
	r := httptest.NewRequest("GET", "/private-sensitive-coordinate-token?secret=hidden", nil)
	r.Header.Set("Authorization", "Bearer do-not-log")
	h.ServeHTTP(httptest.NewRecorder(), r)
	for _, value := range []string{"sensitive", "coordinate", "hidden", "do-not-log"} {
		if strings.Contains(log.String(), value) {
			t.Fatal("untrusted path or secret logged")
		}
	}
	if !strings.Contains(log.String(), `"operation":"unmatched"`) {
		t.Fatal("missing bounded operation")
	}
}
