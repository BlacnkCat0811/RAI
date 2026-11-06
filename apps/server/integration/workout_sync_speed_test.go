//go:build integration

package integration

// P06/P07 server-side acknowledgement measurement.
//
// The Phase 4 matrix in workout_performance_test.go makes one observation per
// profile, which is enough to see a shape but cannot support a P50/P95 claim.
// This harness repeats the same production handler and Store path over fresh
// object identities and reports nearest-rank percentiles from the server's own
// `workout_bundle_transport` telemetry (T_server_ack = handler entry to ACK
// written) plus the per-phase `workout_import_phase` durations, including the
// dataset row lock wait and COMMIT that P06/P07 require.
//
// RAI_SYNC_SPEED_PERF=1 gates it; RAI_SYNC_SPEED_ITERATIONS overrides the
// default 30 measured imports per profile. Every iteration is a new Workout
// with new sample/route identities, so each one is a real first import rather
// than a replay that would short-circuit on the existing receipt.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"rai/server/internal/httpapi"
	"rai/server/internal/syncing"
)

func serverVersion(t *testing.T, f *fixture) string {
	t.Helper()
	var version string
	if err := f.s.Pool.QueryRow(context.Background(), `SHOW server_version`).Scan(&version); err != nil {
		t.Fatalf("server version: %v", err)
	}
	return version
}

type syncSpeedLogs struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (l *syncSpeedLogs) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *syncSpeedLogs) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf.Reset()
}

func (l *syncSpeedLogs) lines() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Split(strings.TrimSpace(l.buf.String()), "\n")
}

// nearestRank returns the nearest-rank percentile of an unsorted sample.
// The rank is ceil(p*n) with a minimum of 1, so a small sample never reports a
// percentile below its own maximum by accident.
func nearestRank(values []float64, percentile float64) float64 {
	if len(values) == 0 {
		return math.NaN()
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	rank := int(math.Ceil(percentile * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

func median(values []float64) float64 {
	return nearestRank(values, 0.5)
}

func minMax(values []float64) (float64, float64) {
	if len(values) == 0 {
		return math.NaN(), math.NaN()
	}
	lo, hi := values[0], values[0]
	for _, v := range values {
		lo = math.Min(lo, v)
		hi = math.Max(hi, v)
	}
	return lo, hi
}

// transportObservation is the subset of the server telemetry this harness reads.
type transportObservation struct {
	wireBytes       float64
	fileBytes       float64
	multipartMS     float64
	decompressionMS float64
	jsonDecodeMS    float64
	validationMS    float64
	importMS        float64
	ackWriteMS      float64
	ackTotalMS      float64
	stage           string
}

func parseTransport(lines []string) (transportObservation, map[string]float64) {
	var obs transportObservation
	phases := map[string]float64{}
	for _, line := range lines {
		var record map[string]any
		if json.Unmarshal([]byte(line), &record) != nil {
			continue
		}
		switch record["msg"] {
		case "workout_bundle_transport":
			number := func(key string) float64 {
				v, _ := record[key].(float64)
				return v
			}
			obs = transportObservation{
				wireBytes: number("wire_bytes"), fileBytes: number("file_bytes"),
				multipartMS: number("multipart_ms"), decompressionMS: number("decompression_ms"),
				jsonDecodeMS: number("json_decode_ms"), validationMS: number("validation_ms"),
				importMS: number("import_ms"), ackWriteMS: number("ack_write_ms"),
				ackTotalMS: number("ack_total_ms"),
			}
			obs.stage, _ = record["stage"].(string)
		case "workout_import_phase":
			name, _ := record["phase"].(string)
			if name == "" {
				continue
			}
			v, _ := record["duration_ms"].(float64)
			// import_total wraps every other phase; keep the largest so a
			// repeated name (retries) still reports its worst observation.
			if prior, ok := phases[name]; !ok || v > prior {
				phases[name] = v
			}
		}
	}
	return obs, phases
}

// TestWorkoutSyncSpeedServerAckMatrix measures T_server_ack for S60 and S240
// over repeated fresh imports. It reports only durations, byte counts and
// status codes: no health value, payload or identity is logged.
func TestWorkoutSyncSpeedServerAckMatrix(t *testing.T) {
	if os.Getenv("RAI_SYNC_SPEED_PERF") == "" {
		t.Skip("set RAI_SYNC_SPEED_PERF=1 for repeated server acknowledgement measurement")
	}
	iterations := 30
	warmups := 3
	if raw := os.Getenv("RAI_SYNC_SPEED_ITERATIONS"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 {
			t.Fatalf("RAI_SYNC_SPEED_ITERATIONS must be a positive integer, got %q", raw)
		}
		iterations = parsed
	}
	if raw := os.Getenv("RAI_SYNC_SPEED_WARMUPS"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			t.Fatalf("RAI_SYNC_SPEED_WARMUPS must be a non-negative integer, got %q", raw)
		}
		warmups = parsed
	}

	for _, minutes := range []int{60, 240} {
		t.Run(fmt.Sprintf("S%d", minutes), func(t *testing.T) {
			f := setup(t)
			logs := &syncSpeedLogs{}
			api := httpapi.New(f.s, slog.New(slog.NewJSONHandler(logs, nil)))

			upload := func() (transportObservation, map[string]float64, float64, int, syncing.WorkoutImportReceipt, int, []byte) {
				bundle := performanceBundle(t, f, minutes)
				raw, err := json.Marshal(bundle)
				if err != nil {
					t.Fatal(err)
				}
				logs.reset()
				start := time.Now()
				status, ack := uploadBundleHTTP(t, api, f.enrollment.Token, raw)
				client := float64(time.Since(start).Microseconds()) / 1000
				obs, phases := parseTransport(logs.lines())
				var receipt syncing.WorkoutImportReceipt
				if status == 200 {
					if err = json.Unmarshal(ack, &receipt); err != nil {
						t.Fatalf("invalid receipt: %v", err)
					}
				}
				return obs, phases, client, status, receipt, len(raw), ack
			}

			for i := 0; i < warmups; i++ {
				if _, _, _, status, _, _, _ := upload(); status != 200 {
					t.Fatalf("warmup %d failed with HTTP %d", i, status)
				}
			}

			var ackMS, clientMS, importMS, decodeMS, validationMS, ackWriteMS, wireBytes []float64
			phaseSamples := map[string][]float64{}
			failures := 0
			replays := 0
			// Attempts may exceed the target sample count: a failed import is
			// reported, never retried into the percentile sample.
			for attempt := 0; len(ackMS) < iterations && attempt < iterations+10; attempt++ {
				i := attempt
				obs, phases, client, status, receipt, rawBytes, ack := upload()
				if status != 200 {
					failures++
					var body struct {
						Code string `json:"code"`
					}
					_ = json.Unmarshal(ack, &body)
					t.Logf("iteration %d failed with HTTP %d code=%q stage=%q ack_total_ms=%.3f identity_checks_ms=%.3f",
						i, status, body.Code, obs.stage, obs.ackTotalMS, phases["identity_checks"])
					continue
				}
				// The success path leaves the stage at "complete" after the ACK
				// is flushed; any other value with HTTP 200 would mean the
				// telemetry and the response disagree.
				if obs.stage != "complete" {
					t.Fatalf("iteration %d reported stage %q with HTTP 200", i, obs.stage)
				}
				if receipt.Counts.SamplesInserted == 0 {
					// A replayed receipt would report zero inserted rows; every
					// iteration here must be a first import.
					replays++
				}
				t.Logf("iteration %d ack_total_ms=%.3f import_ms=%.3f identity_checks_ms=%.3f sample_writes_ms=%.3f",
					i, obs.ackTotalMS, obs.importMS, phases["identity_checks"], phases["sample_writes"])
				ackMS = append(ackMS, obs.ackTotalMS)
				clientMS = append(clientMS, client)
				importMS = append(importMS, obs.importMS)
				decodeMS = append(decodeMS, obs.jsonDecodeMS)
				validationMS = append(validationMS, obs.validationMS)
				ackWriteMS = append(ackWriteMS, obs.ackWriteMS)
				wireBytes = append(wireBytes, float64(rawBytes))
				for name, value := range phases {
					phaseSamples[name] = append(phaseSamples[name], value)
				}
			}
			if replays != 0 {
				t.Fatalf("%d iteration(s) replayed an existing receipt instead of importing", replays)
			}
			if len(ackMS) < iterations {
				t.Fatalf("collected %d of %d acknowledgements (%d failures)", len(ackMS), iterations, failures)
			}

			lo, hi := minMax(ackMS)
			report := map[string]any{
				"profile":             fmt.Sprintf("S%d", minutes),
				"iterations":          iterations,
				"warmups":             warmups,
				"failures":            failures,
				"ack_total_ms":        map[string]any{"p50": median(ackMS), "p95": nearestRank(ackMS, 0.95), "min": lo, "max": hi},
				"client_roundtrip_ms": map[string]any{"p50": median(clientMS), "p95": nearestRank(clientMS, 0.95)},
				"import_ms":           map[string]any{"p50": median(importMS), "p95": nearestRank(importMS, 0.95)},
				"json_decode_ms":      map[string]any{"p50": median(decodeMS), "p95": nearestRank(decodeMS, 0.95)},
				"validation_ms":       map[string]any{"p50": median(validationMS), "p95": nearestRank(validationMS, 0.95)},
				"ack_write_ms":        map[string]any{"p50": median(ackWriteMS), "p95": nearestRank(ackWriteMS, 0.95)},
				"json_bytes_p50":      median(wireBytes),
				"pg_version":          serverVersion(t, f),
			}
			phases := map[string]any{}
			for name, values := range phaseSamples {
				phases[name] = map[string]any{"p50": median(values), "p95": nearestRank(values, 0.95), "n": len(values)}
			}
			report["import_phases_ms"] = phases
			out, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			t.Log("SYNC_SPEED_SERVER_ACK=" + string(out))
		})
	}
}
