//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"testing"
	"time"

	"rai/server/internal/httpapi"
	"rai/server/internal/syncing"
)

// Measures real loopback HTTP and durable PG commits, not HealthKit or Worker
// execution. Small bundles isolate per-Workout transaction overhead. The dense
// workout matrix remains TestPhase4PerformanceMatrix; neither is iPhone evidence.
func TestLocalWorkoutThroughput(t *testing.T) {
	if os.Getenv("RAI_LOCAL_WORKOUT_PERF") != "1" {
		t.Skip("explicit local synthetic performance run")
	}
	// An explicitly reported, finite benchmark policy avoids measuring default
	// token-bucket waiting. Production defaults and other tests stay unchanged.
	prior := syncing.RequestBudgets()
	policies := syncing.RequestBudgets()
	upload := policies[syncing.UploadLane]
	upload.Device = syncing.RequestBudget{PerMinute: 60000, Burst: 1024}
	upload.Dataset = syncing.RequestBudget{PerMinute: 60000, Burst: 1024}
	policies[syncing.UploadLane] = upload
	syncing.SetRequestBudgets(policies)
	t.Cleanup(func() { syncing.SetRequestBudgets(prior) })
	for _, n := range []int{1, 10, 100, 500} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			f := setup(t)
			server := httptest.NewServer(httpapi.New(f.s, slog.New(slog.NewTextHandler(io.Discard, nil))))
			defer server.Close()
			client := &http.Client{Timeout: 30 * time.Second}
			defer client.CloseIdleConnections()
			post := func(raw []byte) []byte {
				t.Helper()
				var body bytes.Buffer
				writer := multipart.NewWriter(&body)
				part, err := writer.CreateFormFile("file", "workout.json")
				if err != nil {
					t.Fatal(err)
				}
				if _, err = part.Write(raw); err != nil {
					t.Fatal(err)
				}
				if err = writer.Close(); err != nil {
					t.Fatal(err)
				}
				request, err := http.NewRequest(http.MethodPost, server.URL+"/v2/sync/workout-bundles", &body)
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
				request.Header.Set("Content-Type", writer.FormDataContentType())
				response, err := client.Do(request)
				if err != nil {
					t.Fatal("local HTTP request failed")
				}
				defer response.Body.Close()
				ack, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
				if err != nil || response.StatusCode != http.StatusOK {
					t.Fatalf("local import status=%d read_failed=%t", response.StatusCode, err != nil)
				}
				var receipt syncing.WorkoutImportReceipt
				if json.Unmarshal(ack, &receipt) != nil || receipt.IngestStatus != "committed" || receipt.Counts.SamplesInserted != 60 {
					t.Fatal("unexpected synthetic import receipt")
				}
				return ack
			}
			latencies := make([]float64, 0, n)
			var lastRaw, lastACK []byte
			started := time.Now()
			for i := 0; i < n; i++ {
				_, raw := testWorkoutBundle(t, f, 60)
				sent := time.Now()
				lastACK = post(raw)
				latencies = append(latencies, float64(time.Since(sent).Microseconds())/1000)
				lastRaw = raw
			}
			elapsed := time.Since(started)
			if !bytes.Equal(lastACK, post(lastRaw)) {
				t.Fatal("replay changed the immutable receipt")
			}
			for table, expected := range map[string]int{"workouts": n, "workout_imports": n, "health_samples": 60 * n, "workout_samples": 60 * n, "route_points": 3 * n} {
				if count(t, f, table) != expected {
					t.Fatalf("incorrect row count in %s", table)
				}
			}
			sort.Float64s(latencies)
			result, err := json.Marshal(map[string]any{
				"environment": "local Mac; synthetic; loopback HTTP; durable PostgreSQL 18",
				"workouts":    n, "samplesPerWorkout": 60, "seriesPointsPerWorkout": 3, "routePointsPerWorkout": 3,
				"elapsedMs": float64(elapsed.Microseconds()) / 1000, "workoutsPerSecond": float64(n) / elapsed.Seconds(),
				"httpMedianMs": latencies[(n-1)/2], "httpP95Ms": latencies[(95*n+99)/100-1],
				"immutableReplay": true, "rowCountsVerified": true, "workerMeasured": false, "healthKitMeasured": false,
				"benchmarkUploadBurst": 1024, "benchmarkUploadPerMinute": 60000,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Log("LOCAL_WORKOUT_PERF=" + string(result))
		})
	}
}
