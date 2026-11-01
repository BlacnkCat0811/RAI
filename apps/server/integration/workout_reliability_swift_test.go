//go:build integration && swiftclient

package integration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"rai/server/internal/httpapi"
	"rai/server/internal/syncing"
)

// Actual Swift Outbox -> URLSession -> Go HTTP -> isolated PostgreSQL, with
// transport faults outside the API. The fixture is synthetic and loopback only.
func TestWorkoutReliabilitySwiftClientPostgres(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Fatal("Swift/macOS environment required for cross-language acceptance")
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	scratch := filepath.Join(t.TempDir(), "swift-build")
	for _, mode := range []string{"before_send", "during_send", "before_commit_timeout", "lost_ack", "corrupt_ack", "commit_404", "validation_missing", "tombstone", "rate_limit", "status_500", "status_502", "status_503", "auth_refresh", "two_workers", "duplicate_post", "phase3_observation", "phase3_child_delete", "phase3_deleted_parent"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t)
			b, raw := testWorkoutBundle(t, f, 1)
			if mode == "phase3_observation" {
				phase3Observation(&b)
				b.Routes[0].Points = append(b.Routes[0].Points, syncing.WorkoutBundlePoint{RecordedAtUs: b.Routes[0].Points[0].RecordedAtUs})
				raw, _ = json.Marshal(b)
			}
			var parentACK []byte
			if mode == "phase3_child_delete" || mode == "phase3_deleted_parent" {
				importPhase3(t, f, b)
				if mode == "phase3_deleted_parent" {
					parentACK, err = f.s.DeleteWorkoutBundle(context.Background(), f.p, bundleDeleteRequest(f, b.WorkoutID))
					if err != nil {
						t.Fatal(err)
					}
					b.DeletionOnly = true
				}
				b.SampleDeletions = []syncing.SampleDeletion{{SampleType: b.Samples[0].Type, SampleUUID: b.Samples[0].UUID}}
				b.RouteDeletions = []string{b.Routes[0].RouteID}
				b.Samples = []syncing.Event{}
				b.Series = nil
				b.Routes = nil
				raw, _ = json.Marshal(b)
			}
			if mode == "validation_missing" {
				var object map[string]any
				_ = json.Unmarshal(raw, &object)
				delete(routePoint(object), "latitude")
				raw, _ = json.Marshal(object)
			}
			if mode == "tombstone" {
				if _, err := f.s.DeleteWorkoutBundle(context.Background(), f.p, bundleDeleteRequest(f, b.WorkoutID)); err != nil {
					t.Fatal(err)
				}
			}
			input := filepath.Join(t.TempDir(), "synthetic-bundle.json")
			if err := os.WriteFile(input, raw, 0600); err != nil {
				t.Fatal(err)
			}
			api := httpapi.New(f.s, slog.Default())
			var posts atomic.Int64
			drop := func(w http.ResponseWriter) {
				connection, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				_ = connection.Close()
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/test-evidence" {
					// No health values: only synthetic acceptance counts.
					w.Header().Set("Content-Type", "application/json")
					counts := map[string]int64{"posts": posts.Load()}
					for key, table := range map[string]string{"imports": "workout_imports", "samples": "health_samples", "jobs": "jobs", "tombstones": "entity_tombstones"} {
						var n int64
						if err := f.s.Pool.QueryRow(r.Context(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
							t.Error(err)
						}
						counts[key] = n
					}
					_ = json.NewEncoder(w).Encode(counts)
					return
				}
				if r.Method == http.MethodGet && mode == "commit_404" && posts.Load() == 1 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(404)
					_, _ = w.Write([]byte(`{"code":"workout_import_not_found"}`))
					return
				}
				if r.Method == http.MethodPost && posts.Add(1) == 1 {
					switch mode {
					case "rate_limit":
						w.Header().Set("Content-Type", "application/json")
						w.Header().Set("Retry-After", "120")
						w.WriteHeader(429)
						_, _ = w.Write([]byte(`{"code":"device_rate_exceeded"}`))
						return
					case "status_500", "status_502", "status_503":
						status := map[string]int{"status_500": 500, "status_502": 502, "status_503": 503}[mode]
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(status)
						_, _ = w.Write([]byte(`{"code":"temporarily_unavailable"}`))
						return
					case "auth_refresh":
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(401)
						_, _ = w.Write([]byte(`{"code":"unauthorized"}`))
						return
					case "before_send":
						drop(w)
						return
					case "during_send":
						_, _ = io.CopyN(io.Discard, r.Body, 64)
						drop(w)
						return
					case "before_commit_timeout":
						// Hold the real first business lock: request reaches API, then its
						// deadline expires while waiting, so no transaction can commit.
						tx, err := f.s.Pool.Begin(context.Background())
						if err != nil {
							t.Error(err)
							return
						}
						defer tx.Rollback(context.Background())
						if _, err := tx.Exec(context.Background(), "SELECT 1 FROM datasets WHERE id=$1 FOR UPDATE", f.p.DatasetID); err != nil {
							t.Error(err)
							return
						}
						ctx, cancel := context.WithTimeout(r.Context(), 30*time.Millisecond)
						defer cancel()
						rec := httptest.NewRecorder()
						api.ServeHTTP(rec, r.WithContext(ctx))
						drop(w)
						return
					case "lost_ack", "corrupt_ack", "commit_404":
						rec := httptest.NewRecorder()
						api.ServeHTTP(rec, r)
						if rec.Code != 200 {
							t.Errorf("commit status=%d", rec.Code)
						}
						if mode == "lost_ack" {
							drop(w)
							return
						}
						w.Header().Set("Content-Type", "application/json")
						if mode == "corrupt_ack" {
							_, _ = w.Write([]byte(`{"broken"`))
							return
						}
						drop(w)
						return
					}
				}
				api.ServeHTTP(w, r)
			}))
			defer server.Close()
			command := exec.Command("swift", "test", "--package-path", filepath.Join(root, "ios/RAI"), "--scratch-path", scratch, "--filter", "WorkoutReliabilityLiveTests")
			command.Env = append(os.Environ(), "RAI_PHASE2_ISOLATED=synthetic-pg18", "RAI_PHASE2_URL="+server.URL, "RAI_PHASE2_TOKEN="+f.enrollment.Token, "RAI_PHASE2_BUNDLE="+input, "RAI_PHASE2_MODE="+mode, "RAI_PHASE3_PARENT_DELETE_ACK="+string(parentACK))
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("Swift client failed: %v\n%s", err, output)
			}
			t.Logf("actual Swift client/HTTP/PG18 scenario=%s passed", mode)
			if mode != "validation_missing" && mode != "tombstone" && mode != "phase3_child_delete" && mode != "phase3_deleted_parent" {
				if count(t, f, "workout_imports") != 1 || count(t, f, "health_samples") != 1 || count(t, f, "jobs") != 1 {
					t.Fatal("duplicate/missing durable outcomes")
				}
			}
		})
	}
}
