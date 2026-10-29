//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"rai/server/internal/httpapi"
	"rai/server/internal/syncing"
	"rai/server/internal/training"
)

func TestFinalMaintenanceTrainingHTTPWire(t *testing.T) {
	for _, endpoint := range []string{"create", "change", "override", "start", "execution_change"} {
		t.Run(endpoint, func(t *testing.T) {
			f, p := trainingFixture(t)
			path := "/v2/training/plans"
			var request any = training.CreateRequest{CommandID: syncing.NewID(), PlanID: syncing.NewID(), Config: trainingConfig()}
			start := training.StartRequest{CommandID: syncing.NewID(), ExecutionID: syncing.NewID(), PlanID: p.ID, EffectiveID: p.Effective.ID, SessionID: p.Effective.Sessions[0].ID}
			switch endpoint {
			case "change":
				path += "/" + p.ID + "/changes"
				request = training.ChangeRequest{CommandID: syncing.NewID(), Operation: "recalculate", ExpectedConfigRevision: p.ConfigRevision}
			case "override":
				path += "/" + p.ID + "/overrides"
				request = training.OverrideRequest{CommandID: syncing.NewID(), OverrideID: syncing.NewID(), SessionID: start.SessionID, Operation: "edit", Path: "distanceM", Value: json.RawMessage("5000"), ExpectedOverrideRevision: 0}
			case "start":
				path = "/v2/training/executions"
				request = start
			case "execution_change":
				if _, err := f.s.StartTrainingExecution(context.Background(), f.p, start); err != nil {
					t.Fatal(err)
				}
				path = "/v2/training/executions/" + start.ExecutionID + "/changes"
				request = training.ExecutionChange{CommandID: syncing.NewID(), ExpectedRevision: 1, Operation: "complete"}
			}
			valid, _ := json.Marshal(request)
			for _, tc := range []struct {
				name, media, encoding string
				body                  []byte
				status                int
			}{
				{"missing_type", "", "", valid, 415},
				{"text", "text/plain", "", valid, 415},
				{"octet", "application/octet-stream", "", valid, 415},
				{"gzip", "application/json", "gzip", valid, 415},
				{"other_encoding", "application/json", "br", valid, 415},
				{"oversize", "application/json", "", append(bytes.Repeat([]byte(" "), 64<<10), valid...), 413},
				{"malformed", "application/json", "", []byte(`{"`), 422},
				{"duplicate", "application/json", "", append([]byte(`{"commandID":"duplicate",`), valid[1:]...), 422},
				{"unknown", "application/json", "", append([]byte(`{"unexpected":true,`), valid[1:]...), 422},
				{"valid", "application/json", "", valid, 200},
				{"charset", "application/json; charset=utf-8", "", valid, 200},
			} {
				t.Run(tc.name, func(t *testing.T) {
					want := tc.status
					if os.Getenv("RAI_MAINTENANCE_BASELINE") == "1" {
						if tc.status == 415 {
							want = 200
						}
						if tc.status == 413 {
							want = 422
						}
					}
					var w *httptest.ResponseRecorder
					for attempt := 0; attempt < 5; attempt++ {
						r := httptest.NewRequest("POST", path, bytes.NewReader(tc.body))
						r.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
						if tc.media != "" {
							r.Header.Set("Content-Type", tc.media)
						}
						if tc.encoding != "" {
							r.Header.Set("Content-Encoding", tc.encoding)
						}
						w = httptest.NewRecorder()
						httpapi.New(f.s, analysisLog).ServeHTTP(w, r)
						if w.Code != 429 {
							break
						}
						seconds, err := strconv.Atoi(w.Header().Get("Retry-After"))
						if err != nil || seconds < 1 || seconds > 10 {
							t.Fatal("invalid admission retry")
						}
						time.Sleep(time.Duration(seconds) * time.Second)
					}
					if w.Code != want {
						t.Fatalf("status=%d want=%d", w.Code, want)
					}
					if tc.status == 415 || tc.status == 413 {
						t.Logf("MAINTENANCE_HTTP endpoint=%s case=%s status=%d", endpoint, tc.name, w.Code)
					}
				})
			}
			// Authentication retains precedence over body validation.
			r := httptest.NewRequest("POST", path, strings.NewReader("{}"))
			w := httptest.NewRecorder()
			httpapi.New(f.s, analysisLog).ServeHTTP(w, r)
			if w.Code != 401 {
				t.Fatal("authentication contract changed")
			}
		})
	}
}
