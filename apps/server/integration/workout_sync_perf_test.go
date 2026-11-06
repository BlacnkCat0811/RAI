//go:build integration

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"rai/server/internal/storage/postgres"

	"rai/server/internal/httpapi"
	"rai/server/internal/syncing"
)

// TestWorkoutSyncPerfServerHTTP is the R0 normal-build performance entry point
// behind `scripts/local/test.sh syncperf`.
//
// Measurement contract (documented in
// docs/evidence/workout-sync-speed-round2/server/):
//
//   - optimized build, no -race, no -N/-l (asserted below and re-asserted by the
//     shell profile);
//   - real loopback TCP HTTP into the production handler: bearer auth, admission
//     (dataset + device request budgets), per-device and process upload slots,
//     the production body reader, the production strict decoder and the real
//     Store against a real isolated PostgreSQL 18 cluster with default
//     durability;
//   - default budgets: nothing raises pool size, upload slots, request budgets
//     or timeouts; 429 admission waits are retried and counted, never measured
//     as latency;
//   - every iteration is a fresh first import (unique identities, asserted from
//     the ACK counts), warmups are excluded from the statistics;
//   - P50/P95 use the nearest-rank definition ceil(p*n).
func TestWorkoutSyncPerfServerHTTP(t *testing.T) {
	if os.Getenv("RAI_SYNC_PERF") == "" {
		t.Skip("set RAI_SYNC_PERF=1 (scripts/local/test.sh syncperf)")
	}
	if perfRaceEnabled {
		t.Fatal("R0 requires the optimized build: this binary was compiled with -race")
	}
	iterations := perfEnvInt(t, "RAI_SYNC_PERF_ITERATIONS", 30)
	warmups := perfEnvInt(t, "RAI_SYNC_PERF_WARMUPS", 3)
	colds := perfEnvInt(t, "RAI_SYNC_PERF_COLD", 5)
	profiles := perfEnvString("RAI_SYNC_PERF_PROFILES", "S60,S60H,S240")
	if strategy := os.Getenv("RAI_SYNC_PERF_WRITE_STRATEGY"); strategy != "" {
		if err := postgres.SetWriteStrategy(postgres.WriteStrategy(strategy)); err != nil {
			t.Fatalf("%v", err)
		}
	}
	if iterations < 30 {
		t.Logf("NOTE: R2-P08/P09 require n>=30 timed iterations; this run uses n=%d and cannot sign them", iterations)
	}
	for _, name := range strings.Split(profiles, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		profile := name
		t.Run(profile, func(t *testing.T) {
			runSyncPerfProfile(t, profile, iterations, warmups, colds)
		})
	}
}

func runSyncPerfProfile(t *testing.T, profile string, iterations, warmups, colds int) {
	t.Helper()
	expectedSamples, err := perfExpectedSamples(t, profile)
	if err != nil {
		t.Fatal(err)
	}

	// Cold section: a brand-new database per profile, so the first imports run
	// against empty relations and an unwarmed relation cache.
	cold := setup(t)
	coldLogs := &syncSpeedLogs{}
	coldAPI := httpapi.New(cold.s, slog.New(slog.NewJSONHandler(coldLogs, nil)))
	_, coldBase := startPerfServer(t, coldAPI)
	coldRun := runPerfUploads(t, cold, perfHTTPClient(), coldBase, coldLogs, profile, "cold", colds, 1, expectedSamples)

	// Warm section: a separate fresh database, 3 warmups then n timed imports.
	f := setup(t)
	logs := &syncSpeedLogs{}
	api := httpapi.New(f.s, slog.New(slog.NewJSONHandler(logs, nil)))
	_, base := startPerfServer(t, api)
	client := perfHTTPClient()
	machineBefore := collectPerfMachine(t)
	t.Logf("SYNCPERF_MACHINE phase=before exclusive=%v load=%q competitors=%d",
		machineBefore.Exclusive, machineBefore.LoadAverage, len(machineBefore.CompetingProcesses))
	facts := collectPerfFacts(t, f, client, base)
	warmRun := runPerfUploads(t, f, client, base, logs, profile, "warmup", warmups, 2, expectedSamples)
	timedRun := runPerfUploads(t, f, client, base, logs, profile, "timed", iterations, 1000, expectedSamples)
	collectAdmissionFacts(t, f, &facts)

	report := perfReport{
		Profile:     profile,
		Environment: facts,
		Warmups:     warmRun.summary(),
		Cold:        coldRun.summary(),
		Timed:       timedRun.summary(),
		Phases:      timedRun.phaseStats(),
		Transport:   timedRun.transportStats(),
		Admission: perfAdmission{
			TimedWaits:    timedRun.waits,
			TimedAttempts: timedRun.attempts,
			ColdWaits:     coldRun.waits,
		},
		Machine: machineBefore,
	}
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	t.Logf("SYNC_PERF=%s", payload)
	machineAfter := collectPerfMachine(t)
	t.Logf("SYNCPERF_MACHINE phase=after exclusive=%v load=%q competitors=%d",
		machineAfter.Exclusive, machineAfter.LoadAverage, len(machineAfter.CompetingProcesses))
	t.Logf("profile=%s n=%d failures=%d http_total_p50=%.1fms p95=%.1fms cold_p50=%.1fms cold_p95=%.1fms waits=%d strategy=%s exclusive=%v",
		profile, report.Timed.N, report.Timed.Failures, report.Timed.P50, report.Timed.P95,
		report.Cold.P50, report.Cold.P95, report.Admission.TimedWaits,
		report.Environment.WriteStrategy, machineBefore.Exclusive && machineAfter.Exclusive)
	if !machineBefore.Exclusive || !machineAfter.Exclusive {
		t.Logf("profile %s: NOT an exclusive measurement window (%d competing process(es) before, %d after); these numbers cannot sign R2-P08/P09/P10",
			profile, len(machineBefore.CompetingProcesses), len(machineAfter.CompetingProcesses))
	}

	if timedRun.failures > 0 {
		t.Fatalf("profile %s: %d/%d timed iterations failed: %s", profile, timedRun.failures, timedRun.total(), strings.Join(timedRun.failuresLog, "; "))
	}
	if warmRun.failures > 0 {
		t.Fatalf("profile %s: %d warmup iterations failed: %s", profile, warmRun.failures, strings.Join(warmRun.failuresLog, "; "))
	}
	if coldRun.failures > 0 {
		t.Fatalf("profile %s: %d cold iterations failed: %s", profile, coldRun.failures, strings.Join(coldRun.failuresLog, "; "))
	}
	if len(timedRun.samples) < 30 {
		t.Logf("NOTE: profile %s has n=%d < 30; R2-P08/P09 cannot be signed from this run", profile, len(timedRun.samples))
		return
	}
	switch profile {
	case "S60":
		if report.Timed.P95 > 1000 {
			t.Fatalf("R2-P08 FAIL: S60 normal-build HTTP->PG P95 %.1fms > 1000ms", report.Timed.P95)
		}
	case "S240":
		if report.Timed.P95 > 4000 {
			t.Fatalf("R2-P09 FAIL: S240 normal-build HTTP->PG P95 %.1fms > 4000ms", report.Timed.P95)
		}
	}
}

// perfExpectedSamples returns the number of sample facts one bundle inserts.
func perfExpectedSamples(t *testing.T, profile string) (int, error) {
	t.Helper()
	if profile == "S60H" {
		return perfDenseSamples, nil
	}
	minutes, ok := perfProfileMinutes(profile)
	if !ok {
		return 0, fmt.Errorf("unknown performance profile %q", profile)
	}
	total := 0
	for _, density := range []int{456, 887, 887, 884, 423, 428, 432} {
		total += density * minutes / 40
	}
	return total, nil
}

type perfSample struct {
	httpTotalMS float64
	attempts    int
	waits       int
	status      int
	observation transportObservation
	phases      map[string]float64
	samples     int
	ingest      string
	body        string
	err         string
}

type perfRun struct {
	profile     string
	label       string
	samples     []perfSample
	failures    int
	failuresLog []string
	waits       int
	attempts    int
	fixtureSize int
	fixtureHash string
}

func (r *perfRun) total() int { return len(r.samples) + r.failures }

func (r *perfRun) summary() perfRunSummary {
	values := make([]float64, 0, len(r.samples))
	for _, sample := range r.samples {
		values = append(values, sample.httpTotalMS)
	}
	return perfRunSummary{
		Label:       r.label,
		N:           len(r.samples),
		Failures:    r.failures,
		P50:         perfPercentile(values, 0.50),
		P95:         perfPercentile(values, 0.95),
		Min:         perfMinimum(values),
		Max:         perfMaximum(values),
		Waits:       r.waits,
		Attempts:    r.attempts,
		FixtureSize: r.fixtureSize,
		FixtureHash: r.fixtureHash,
	}
}

func (r *perfRun) phaseStats() map[string]perfRunSummary {
	names := map[string]bool{}
	for _, sample := range r.samples {
		for name := range sample.phases {
			names[name] = true
		}
	}
	out := make(map[string]perfRunSummary, len(names))
	for name := range names {
		values := make([]float64, 0, len(r.samples))
		for _, sample := range r.samples {
			if value, ok := sample.phases[name]; ok {
				values = append(values, value)
			}
		}
		out[name] = perfRunSummary{N: len(values), P50: perfPercentile(values, 0.50), P95: perfPercentile(values, 0.95), Min: perfMinimum(values), Max: perfMaximum(values)}
	}
	return out
}

// transportStats aggregates the handler's own segment timings (multipart read,
// decompression, strict JSON decode, semantic validation, import transaction and
// acknowledgement write) so a slow import can be attributed to a boundary stage
// instead of to the whole request.
func (r *perfRun) transportStats() map[string]perfRunSummary {
	fields := map[string]func(transportObservation) float64{
		"wire_bytes":       func(o transportObservation) float64 { return o.wireBytes },
		"file_bytes":       func(o transportObservation) float64 { return o.fileBytes },
		"multipart_ms":     func(o transportObservation) float64 { return o.multipartMS },
		"decompression_ms": func(o transportObservation) float64 { return o.decompressionMS },
		"json_decode_ms":   func(o transportObservation) float64 { return o.jsonDecodeMS },
		"validation_ms":    func(o transportObservation) float64 { return o.validationMS },
		"import_ms":        func(o transportObservation) float64 { return o.importMS },
		"ack_write_ms":     func(o transportObservation) float64 { return o.ackWriteMS },
		"ack_total_ms":     func(o transportObservation) float64 { return o.ackTotalMS },
	}
	out := make(map[string]perfRunSummary, len(fields))
	for name, pick := range fields {
		values := make([]float64, 0, len(r.samples))
		for _, sample := range r.samples {
			if value := pick(sample.observation); value > 0 {
				values = append(values, value)
			}
		}
		out[name] = perfRunSummary{N: len(values), P50: perfPercentile(values, 0.50), P95: perfPercentile(values, 0.95), Min: perfMinimum(values), Max: perfMaximum(values)}
	}
	return out
}

// runPerfUploads drives count fresh imports. seedBase distinguishes warmup,
// cold and timed identities so no iteration ever replays a prior bundle.
// perfPGStats samples the cluster-level WAL and checkpoint counters so a slow
// iteration can be attributed to a WAL flush, a checkpoint or nothing at all,
// instead of being guessed from the phase name alone.
var perfIterationStats = os.Getenv("RAI_SYNC_PERF_ITERATION_STATS") != ""

type perfPGStats struct {
	walBytes, walFPI, ckptSyncMS, ckptTimed, ckptRequested float64
}

func readPerfPGStats(f *fixture) perfPGStats {
	var stats perfPGStats
	err := f.s.Pool.QueryRow(context.Background(), `SELECT w.wal_bytes::float8, w.wal_fpi::float8,
		COALESCE(c.sync_time::float8, 0), COALESCE(c.num_timed::float8, 0), COALESCE(c.num_requested::float8, 0)
		FROM pg_stat_wal w CROSS JOIN pg_stat_checkpointer c`).
		Scan(&stats.walBytes, &stats.walFPI, &stats.ckptSyncMS, &stats.ckptTimed, &stats.ckptRequested)
	if err != nil {
		return perfPGStats{}
	}
	return stats
}

func (stats perfPGStats) minus(other perfPGStats) perfPGStats {
	return perfPGStats{
		walBytes:      stats.walBytes - other.walBytes,
		walFPI:        stats.walFPI - other.walFPI,
		ckptSyncMS:    stats.ckptSyncMS - other.ckptSyncMS,
		ckptTimed:     stats.ckptTimed - other.ckptTimed,
		ckptRequested: stats.ckptRequested - other.ckptRequested,
	}
}

func logPerfIteration(t *testing.T, profile, label string, index int, sample perfSample, delta perfPGStats) {
	t.Helper()
	names := make([]string, 0, len(sample.phases))
	for name := range sample.phases {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s=%.2f", name, sample.phases[name]))
	}
	t.Logf("SYNCPERF_ITER profile=%s label=%s i=%d ack_ms=%.1f %s wal_bytes=%.0f wal_fpi=%.0f ckpt_sync_ms=%.1f ckpt_timed=%.0f ckpt_requested=%.0f",
		profile, label, index, sample.observation.ackTotalMS, strings.Join(parts, " "),
		delta.walBytes, delta.walFPI, delta.ckptSyncMS, delta.ckptTimed, delta.ckptRequested)
}

func runPerfUploads(t *testing.T, f *fixture, client *http.Client, base string, logs *syncSpeedLogs, profile, label string, count int, seedBase uint64, expectedSamples int) *perfRun {
	t.Helper()
	run := &perfRun{profile: profile, label: label}
	for i := 0; i < count; i++ {
		seed := seedBase*uint64(1<<20) + uint64(i)
		bundle := perfBundle(t, f, profile, seed)
		raw, err := json.Marshal(bundle)
		if err != nil {
			t.Fatalf("marshal bundle: %v", err)
		}
		if i == 0 {
			sum := sha256.Sum256(raw)
			run.fixtureSize = len(raw)
			run.fixtureHash = hex.EncodeToString(sum[:])
		}
		body, contentType := perfMultipart(t, raw)
		statsBefore := perfPGStats{}
		if perfIterationStats {
			statsBefore = readPerfPGStats(f)
		}
		sample := uploadPerfBundle(t, client, base, f.enrollment.Token, body, contentType, logs)
		if perfIterationStats && sample.err == "" {
			logPerfIteration(t, profile, label, i, sample, readPerfPGStats(f).minus(statsBefore))
		}
		run.attempts += sample.attempts
		run.waits += sample.waits
		if sample.err != "" {
			run.failures++
			run.failuresLog = append(run.failuresLog, fmt.Sprintf("iteration %d: %s", i, sample.err))
			continue
		}
		if sample.status != http.StatusOK {
			run.failures++
			run.failuresLog = append(run.failuresLog, fmt.Sprintf("iteration %d: status %d body %s", i, sample.status, sample.body))
			continue
		}
		if sample.ingest != "committed" {
			run.failures++
			run.failuresLog = append(run.failuresLog, fmt.Sprintf("iteration %d: ingest_status %q", i, sample.ingest))
			continue
		}
		if sample.samples != expectedSamples {
			run.failures++
			run.failuresLog = append(run.failuresLog, fmt.Sprintf("iteration %d: samples_inserted %d, want %d (not a fresh first import)", i, sample.samples, expectedSamples))
			continue
		}
		if sample.observation.stage != "complete" {
			run.failures++
			run.failuresLog = append(run.failuresLog, fmt.Sprintf("iteration %d: handler stage %q", i, sample.observation.stage))
			continue
		}
		run.samples = append(run.samples, sample)
	}
	return run
}

// uploadPerfBundle performs one timed upload, retrying only admission refusals.
func uploadPerfBundle(t *testing.T, client *http.Client, base, token string, body []byte, contentType string, logs *syncSpeedLogs) perfSample {
	t.Helper()
	sample := perfSample{phases: map[string]float64{}}
	for attempt := 0; attempt < 12; attempt++ {
		sample.attempts++
		logs.reset()
		request, err := http.NewRequest(http.MethodPost, base+"/v2/sync/workout-bundles", bytes.NewReader(body))
		if err != nil {
			sample.err = err.Error()
			return sample
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", contentType)
		start := time.Now()
		response, err := client.Do(request)
		if err != nil {
			sample.err = err.Error()
			return sample
		}
		payload, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		elapsedMS := float64(time.Since(start).Microseconds()) / 1000
		if readErr != nil {
			sample.err = readErr.Error()
			return sample
		}
		if len(payload) > 240 {
			sample.body = string(payload[:240])
		} else {
			sample.body = string(payload)
		}
		if response.StatusCode == http.StatusTooManyRequests {
			sample.waits++
			time.Sleep(perfRetryAfter(response, time.Second))
			continue
		}
		sample.status = response.StatusCode
		sample.httpTotalMS = elapsedMS
		sample.observation, sample.phases = waitPerfTransport(logs, 2*time.Second)
		if response.StatusCode == http.StatusOK {
			var receipt syncing.WorkoutImportReceipt
			if err := json.Unmarshal(payload, &receipt); err != nil {
				sample.err = fmt.Sprintf("decode receipt: %v", err)
				return sample
			}
			sample.ingest = receipt.IngestStatus
			sample.samples = receipt.Counts.SamplesInserted
		}
		return sample
	}
	sample.err = fmt.Sprintf("admission refused after %d attempts", sample.attempts)
	return sample
}

func waitPerfTransport(logs *syncSpeedLogs, budget time.Duration) (transportObservation, map[string]float64) {
	deadline := time.Now().Add(budget)
	for {
		observation, phases := parseTransport(logs.lines())
		if observation.stage != "" || !time.Now().Before(deadline) {
			return observation, phases
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func perfMultipart(t *testing.T, raw []byte) ([]byte, string) {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	part, err := writer.CreateFormFile("file", "workout.json")
	if err != nil {
		t.Fatalf("multipart part: %v", err)
	}
	if _, err := part.Write(raw); err != nil {
		t.Fatalf("multipart write: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("multipart close: %v", err)
	}
	return buffer.Bytes(), writer.FormDataContentType()
}

func perfRetryAfter(response *http.Response, fallback time.Duration) time.Duration {
	value := response.Header.Get("Retry-After")
	if value == "" {
		return fallback
	}
	seconds, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || seconds < 1 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

func perfHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 300 * time.Second,
		Transport: &http.Transport{
			MaxIdleConnsPerHost:   2,
			MaxConnsPerHost:       2,
			IdleConnTimeout:       30 * time.Second,
			DisableCompression:    true,
			ForceAttemptHTTP2:     false,
			ResponseHeaderTimeout: 260 * time.Second,
		},
	}
}

func startPerfServer(t *testing.T, handler http.Handler) (*http.Server, string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := httpapi.NewHTTPServer(listener.Addr().String(), handler)
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			t.Logf("server stopped: %v", err)
		}
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})
	return server, "http://" + listener.Addr().String()
}

type perfEnvironment struct {
	GoVersion     string            `json:"go_version"`
	GOOS          string            `json:"goos"`
	GOARCH        string            `json:"goarch"`
	NumCPU        int               `json:"num_cpu"`
	RaceEnabled   bool              `json:"race_enabled"`
	GoFlags       string            `json:"goflags"`
	BuildSettings map[string]string `json:"build_settings"`
	CPU           string            `json:"cpu"`
	PGVersion     string            `json:"pg_version"`
	PGSettings    map[string]string `json:"pg_settings"`
	PoolMaxConns  int32             `json:"pool_max_conns"`
	UploadSlots   int               `json:"process_upload_slots"`
	DatasetSlots  int               `json:"dataset_upload_slots"`
	Handshake     json.RawMessage   `json:"handshake_limits"`
	AuthProbe     int               `json:"unauthenticated_probe_status"`
	BudgetRows    int               `json:"admission_budget_rows"`
	Budgets       []string          `json:"admission_budgets"`
	WriteStrategy string            `json:"write_strategy"`
}

type perfRunSummary struct {
	Label       string  `json:"label,omitempty"`
	N           int     `json:"n"`
	Failures    int     `json:"failures"`
	P50         float64 `json:"p50_ms"`
	P95         float64 `json:"p95_ms"`
	Min         float64 `json:"min_ms"`
	Max         float64 `json:"max_ms"`
	Waits       int     `json:"admission_waits"`
	Attempts    int     `json:"attempts"`
	FixtureSize int     `json:"fixture_bytes"`
	FixtureHash string  `json:"fixture_sha256"`
}

type perfAdmission struct {
	TimedWaits    int `json:"timed_admission_waits"`
	TimedAttempts int `json:"timed_attempts"`
	ColdWaits     int `json:"cold_admission_waits"`
}

type perfReport struct {
	Profile     string                    `json:"profile"`
	Environment perfEnvironment           `json:"environment"`
	Warmups     perfRunSummary            `json:"warmups"`
	Cold        perfRunSummary            `json:"cold_start"`
	Timed       perfRunSummary            `json:"timed"`
	Phases      map[string]perfRunSummary `json:"phases"`
	Transport   map[string]perfRunSummary `json:"transport"`
	Admission   perfAdmission             `json:"admission"`
	Machine     perfMachine               `json:"machine"`
}

// perfMachine records whether the measurement window was exclusive. A shared
// machine inflates P95 and must be visible in the evidence, never inferred.
type perfMachine struct {
	LoadAverage        string   `json:"load_average"`
	CompetingProcesses []string `json:"competing_processes"`
	Exclusive          bool     `json:"exclusive"`
	TopCPU             []string `json:"top_cpu"`
}

func collectPerfMachine(t *testing.T) perfMachine {
	t.Helper()
	machine := perfMachine{}
	if output, err := exec.Command("uptime").Output(); err == nil {
		machine.LoadAverage = strings.TrimSpace(string(output))
	}
	// Any of these means another lane owns CPU and disk while we measure.
	output, _ := exec.Command("pgrep", "-fl", "xcodebuild|simctl|swiftc|swift-frontend|swift-build").Output()
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		machine.CompetingProcesses = append(machine.CompetingProcesses, line)
	}
	if output, err := exec.Command("ps", "-Aro", "pcpu,comm").Output(); err == nil {
		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		if len(lines) > 4 {
			lines = lines[:4]
		}
		machine.TopCPU = lines
	}
	machine.Exclusive = len(machine.CompetingProcesses) == 0
	return machine
}

func collectPerfFacts(t *testing.T, f *fixture, client *http.Client, base string) perfEnvironment {
	t.Helper()
	facts := perfEnvironment{
		GoVersion:     runtime.Version(),
		GOOS:          runtime.GOOS,
		GOARCH:        runtime.GOARCH,
		NumCPU:        runtime.NumCPU(),
		RaceEnabled:   perfRaceEnabled,
		GoFlags:       os.Getenv("GOFLAGS"),
		BuildSettings: map[string]string{},
		PGSettings:    map[string]string{},
		UploadSlots:   httpapi.ProcessUploadSlots,
		DatasetSlots:  syncing.DatasetUploads,
		WriteStrategy: string(postgres.CurrentWriteStrategy()),
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "-race", "-gcflags", "-ldflags", "-tags", "CGO_ENABLED", "vcs.revision", "vcs.modified", "GOAMD64":
				facts.BuildSettings[setting.Key] = setting.Value
			}
		}
		facts.BuildSettings["go"] = info.GoVersion
	}
	if output, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output(); err == nil {
		facts.CPU = strings.TrimSpace(string(output))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	settings := map[string]string{
		"server_version":            "",
		"fsync":                     "",
		"full_page_writes":          "",
		"synchronous_commit":        "",
		"wal_level":                 "",
		"shared_buffers":            "",
		"max_wal_size":              "",
		"data_checksums":            "",
		"default_statistics_target": "",
	}
	for name := range settings {
		var value string
		if err := f.s.Pool.QueryRow(ctx, "SHOW "+name).Scan(&value); err != nil {
			t.Fatalf("SHOW %s: %v", name, err)
		}
		settings[name] = value
	}
	facts.PGVersion = settings["server_version"]
	delete(settings, "server_version")
	facts.PGSettings = settings
	facts.PoolMaxConns = f.s.Pool.Config().MaxConns

	request, err := http.NewRequest(http.MethodGet, base+"/v2/handshake", nil)
	if err == nil {
		request.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
		if response, err := client.Do(request); err == nil {
			payload, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				var envelope struct {
					Limits json.RawMessage `json:"limits"`
				}
				if err := json.Unmarshal(payload, &envelope); err == nil {
					facts.Handshake = envelope.Limits
				}
			}
		}
	}
	if request, err := http.NewRequest(http.MethodPost, base+"/v2/sync/workout-bundles", strings.NewReader("")); err == nil {
		request.Header.Set("Content-Type", "multipart/form-data; boundary=x")
		if response, err := client.Do(request); err == nil {
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
			facts.AuthProbe = response.StatusCode
		}
	}
	if facts.AuthProbe != http.StatusUnauthorized {
		t.Fatalf("production auth middleware is not in the measured path: unauthenticated upload returned %d", facts.AuthProbe)
	}
	return facts
}

// perfPercentile uses the shared nearest-rank definition ceil(p*n).
// collectAdmissionFacts proves the production admission middleware ran: the
// per-dataset and per-device request-budget rows exist for the upload lane.
func collectAdmissionFacts(t *testing.T, f *fixture, facts *perfEnvironment) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	rows, err := f.s.Pool.Query(ctx, `SELECT lane, device_id IS NULL, count(*) FROM sync_request_budgets WHERE dataset_id = $1::uuid GROUP BY 1, 2 ORDER BY 1, 2`, f.p.DatasetID)
	if err != nil {
		t.Fatalf("admission budget rows: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var lane string
		var datasetScoped bool
		var count int
		if err := rows.Scan(&lane, &datasetScoped, &count); err != nil {
			t.Fatalf("admission budget scan: %v", err)
		}
		scope := "device"
		if datasetScoped {
			scope = "dataset"
		}
		facts.Budgets = append(facts.Budgets, fmt.Sprintf("%s/%s=%d", lane, scope, count))
		facts.BudgetRows += count
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("admission budget rows: %v", err)
	}
	if facts.BudgetRows == 0 {
		t.Fatal("production admission middleware did not record any request budget row")
	}
}

func perfPercentile(values []float64, percentile float64) float64 {
	if len(values) == 0 {
		return 0
	}
	return nearestRank(values, percentile)
}

func perfMinimum(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	minimum := values[0]
	for _, value := range values[1:] {
		if value < minimum {
			minimum = value
		}
	}
	return minimum
}

func perfMaximum(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	maximum := values[0]
	for _, value := range values[1:] {
		if value > maximum {
			maximum = value
		}
	}
	return maximum
}

func perfEnvInt(t *testing.T, name string, fallback int) int {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		t.Fatalf("%s must be a positive integer, got %q", name, raw)
	}
	return value
}

func perfEnvString(name, fallback string) string {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	return raw
}
