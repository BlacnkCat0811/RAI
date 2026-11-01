//go:build integration && swiftclient

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"rai/server/internal/admin"
	"rai/server/internal/httpapi"
	"rai/server/internal/syncing"
)

func TestDisasterRecoverySwiftClientGoPostgres(t *testing.T) {
	ctx := context.Background()
	a := setup(t)
	b := freshRecoveryDatabase(t, false)
	old, _ := testWorkoutBundle(t, a, 1)
	phase3Observation(&old)
	raw, _ := json.Marshal(old)
	dir := t.TempDir()
	oldBundle := filepath.Join(dir, "old-bundle.json")
	visible := filepath.Join(dir, "currently-visible-facts.json")
	target := filepath.Join(dir, "new-private.json")
	config := filepath.Join(dir, "private-config.json")
	// Independent synthetic HealthKit reader output contains facts, no old session identities.
	facts, _ := json.Marshal(map[string]any{"workoutID": old.WorkoutID, "workout": old.Workout, "samples": old.Samples, "series": old.Series, "routes": old.Routes, "completeSampleTypes": old.CompleteSampleTypes, "completeRoutes": old.CompleteRoutes, "observation": old.Observation})
	if e := os.WriteFile(oldBundle, raw, 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(visible, facts, 0600); e != nil {
		t.Fatal(e)
	}
	apiA := httpapi.New(a.s, analysisLog)
	var apiB http.Handler
	var recovered atomic.Bool
	var posts atomic.Int64
	var once sync.Once
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/test-disaster" {
			once.Do(func() {
				a.s.Pool.Close() // A's storage is unavailable to the serving process from this point.
				if e := admin.Migrate(ctx, b.s.Pool.Config().ConnString()); e != nil {
					t.Error(e)
					return
				}
				args := []string{"--operation-id", syncing.NewID(), "--actor", "synthetic-cross-language", "--base-url", server.URL, "--credential-file", target}
				if e := admin.InitCommand(ctx, b.s.Pool.Config().ConnString(), args, &bytes.Buffer{}); e != nil {
					t.Error(e)
					return
				}
				readRecoveryCredential(t, b, target)
				apiB = httpapi.New(b.s, analysisLog)
				recovered.Store(true)
			})
			if !recovered.Load() {
				w.WriteHeader(500)
				return
			}
			_, _ = w.Write([]byte(`{"ready":true}`))
			return
		}
		if !recovered.Load() {
			apiA.ServeHTTP(w, r)
			return
		}
		// Kill G: the fresh server commits but the first ACK is dropped.
		if r.Method == "POST" && r.URL.Path == "/v2/sync/workout-bundles" && posts.Add(1) == 1 {
			rec := httptest.NewRecorder()
			apiB.ServeHTTP(rec, r)
			if rec.Code != 200 {
				t.Errorf("fresh commit status %d", rec.Code)
			}
			conn, _, e := w.(http.Hijacker).Hijack()
			if e != nil {
				t.Error(e)
				return
			}
			_ = conn.Close()
			return
		}
		apiB.ServeHTTP(w, r)
	}))
	defer server.Close()
	cfg, _ := json.Marshal(map[string]any{"url": server.URL, "oldEnrollment": a.enrollment, "oldBundle": oldBundle, "visibleFacts": visible, "targetPackage": target})
	if e := os.WriteFile(config, cfg, 0600); e != nil {
		t.Fatal(e)
	}
	root, e := filepath.Abs("../../..")
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command("swift", "test", "--package-path", filepath.Join(root, "ios/RAI"), "--scratch-path", filepath.Join(t.TempDir(), "swift-build"), "--filter", "DisasterRecoveryLiveTests")
	cmd.Env = append(os.Environ(), "RAI_PHASE5B_ISOLATED=synthetic-pg18", "RAI_PHASE5B_PRIVATE_CONFIG="+config)
	output, e := cmd.CombinedOutput()
	if e != nil {
		t.Fatalf("Swift disaster recovery failed: %v\n%s", e, output)
	}
	if !recovered.Load() || count(t, b, "datasets") != 1 || count(t, b, "workouts") != 1 || count(t, b, "workout_imports") != 1 || count(t, b, "health_samples") != 1 || count(t, b, "training_plans") != 0 {
		t.Fatal("fresh import missing/duplicated or training migrated")
	}
	independentRecoveryIdentity(t, a.enrollment, b.enrollment)
	runOne(t, b, "analysis.workout")
	if count(t, b, "analysis_publications") != 1 {
		t.Fatal("new analysis publication missing")
	}
	var foreign int
	if e = b.s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM jobs WHERE dataset_id<>$1)+(SELECT count(*) FROM analysis_runs WHERE dataset_id<>$1)+(SELECT count(*) FROM analysis_publications WHERE dataset_id<>$1)`, b.p.DatasetID).Scan(&foreign); e != nil || foreign != 0 {
		t.Fatal("old analysis lineage inherited")
	}
	t.Log("PHASE5B_CROSS_LANGUAGE old_ACK_GC=true fresh_nil_anchor=true old_bundle_rejected=true fresh_import=1 new_publication=1 lost_ACK_recovered=true")
}
