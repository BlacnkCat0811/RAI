//go:build integration

package integration

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"rai/server/internal/httpapi"
)

func TestModelInputHTTPIdempotencyAndLegacyProduct(t *testing.T) {
	f := setup(t)
	api, _ := b4Roles(t, f)
	handler := httpapi.New(api, slog.New(slog.NewTextHandler(io.Discard, nil)))
	id := "6f9619ff-8b86-4d01-b42d-00cf4fc964ff"
	body := `{"id":"` + id + `","kind":"sleep_target","subjectKey":"athlete","effectiveFrom":"` + time.Now().Add(-time.Hour).UTC().Format(time.RFC3339) + `","provenance":"user_form","payload":{"hours":8}}`
	code, first := postJSONHTTP(t, handler, f.enrollment.Token, "/v2/model-inputs/sleep-targets", []byte(body))
	if code != 200 {
		t.Fatal("model input status", code, string(first))
	}
	code, second := postJSONHTTP(t, handler, f.enrollment.Token, "/v2/model-inputs/sleep-targets", []byte(body))
	if code != 200 || string(first) != string(second) {
		t.Fatal("idempotent receipt mismatch", code, string(second))
	}
	var receipt map[string]any
	_ = json.Unmarshal(first, &receipt)
	if receipt["inputID"] != id || receipt["contentSHA256"] == "" {
		t.Fatal("receipt fields", receipt)
	}
	for _, check := range []struct {
		name, path, body string
		status           int
	}{
		{"unknown payload field", "sleep-targets", strings.Replace(body, `"hours":8`, `"hours":8,"unknown":true`, 1), 422},
		{"duplicate payload field", "sleep-targets", strings.Replace(body, `"hours":8`, `"hours":7,"hours":8`, 1), 422},
		{"changed provenance", "sleep-targets", strings.Replace(body, "user_form", "other_form", 1), 409},
		{"changed supersedes", "sleep-targets", strings.Replace(body, `"payload":`, `"supersedesId":"c9f9619f-8b86-4d01-b42d-00cf4fc964ff","payload":`, 1), 409},
		{"absent workout", "workout-efforts", strings.NewReplacer(id, "b5f9619f-8b86-4d01-b42d-00cf4fc964ff", "sleep_target", "workout_effort", `"hours":8`, `"workoutID":"c9f9619f-8b86-4d01-b42d-00cf4fc964ff","mode":"maximal_supported","userConfirmed":true`).Replace(body), 404},
	} {
		time.Sleep(2100 * time.Millisecond) // preserve the default upload-lane budget
		t.Run(check.name, func(t *testing.T) {
			code, _ := postJSONHTTP(t, handler, f.enrollment.Token, "/v2/model-inputs/"+check.path, []byte(check.body))
			if code != check.status {
				t.Fatalf("status=%d want %d", code, check.status)
			}
		})
	}
	time.Sleep(2100 * time.Millisecond)
	code, rev := postJSONHTTP(t, handler, f.enrollment.Token, "/v2/model-inputs/"+id+"/revocations", []byte(`{"reason":"user correction"}`))
	if code != 200 {
		t.Fatal("revocation status", code, string(rev))
	}
	var revReceipt map[string]any
	_ = json.Unmarshal(rev, &revReceipt)
	if revReceipt["inputID"] == "" || revReceipt["schemaVersion"] != "rai.sleep_target/1" {
		t.Fatal("revocation receipt fields", revReceipt)
	}
	req := httptest.NewRequest(http.MethodGet, "/v2/analysis/athlete", nil)
	req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 409 {
		t.Fatal("legacy product status", rec.Code, rec.Body.String())
	}
}

func postJSONHTTP(t *testing.T, handler http.Handler, token, path string, raw []byte) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(raw)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}
