//go:build integration

package integration

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"os"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"rai/server/internal/analysis"
	"rai/server/internal/httpapi"
	"rai/server/internal/jobs"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
)

// The seven scalar densities and GPS density are the Phase 0/RQrun synthetic
// workload, never HealthKit exports. Phase 3 source observations are included.
func performanceBundle(t *testing.T, f *fixture, minutes int) syncing.WorkoutBundle {
	t.Helper()
	b, _ := testWorkoutBundle(t, f, 0)
	start := time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)
	b.Workout = workoutAt(start, minutes*60)
	b.WorkoutID = b.Workout.UUID
	types := []string{kind, "HKQuantityTypeIdentifierRunningPower", "HKQuantityTypeIdentifierStepCount", "HKQuantityTypeIdentifierDistanceWalkingRunning", "HKQuantityTypeIdentifierRunningStrideLength", "HKQuantityTypeIdentifierRunningGroundContactTime", "HKQuantityTypeIdentifierRunningVerticalOscillation"}
	density := []int{456, 887, 887, 884, 423, 428, 432}
	for k, typ := range types {
		n := density[k] * minutes / 40
		for i := 0; i < n; i++ {
			e := bundleSample(b.WorkoutID, start.Add(time.Duration(int64(i)*int64(minutes)*60*1e9/int64(n))), 100+float64(i%50))
			e.Type = typ
			e.Quantity.Unit = syncing.Units[typ]
			b.Samples = append(b.Samples, e)
		}
	}
	b.Routes[0].Points = nil
	for i := 0; i < 2283*minutes/40; i++ {
		b.Routes[0].Points = append(b.Routes[0].Points, syncing.WorkoutBundlePoint{RecordedAtUs: start.UnixMicro() + int64(i)*int64(minutes)*60*1e6/int64(2283*minutes/40), Latitude: 31.2 + float64(i%100)*.00001, Longitude: 121.4})
	}
	phase3Observation(&b)
	return b
}

type phase4Queue struct {
	*postgres.Store
	load, publish time.Duration
}

func (q *phase4Queue) LoadAnalysis(ctx context.Context, l analysis.Lease, at time.Time) (analysis.Snapshot, error) {
	s := time.Now()
	defer func() { q.load += time.Since(s) }()
	return q.Store.LoadAnalysis(ctx, l, at)
}
func (q *phase4Queue) PublishAnalysis(ctx context.Context, l analysis.Lease, id string, in analysis.Snapshot, m []analysis.Metric) (string, error) {
	s := time.Now()
	defer func() { q.publish += time.Since(s) }()
	return q.Store.PublishAnalysis(ctx, l, id, in, m)
}

// RAI_PHASE4_PERF gates the expensive matrix explicitly. Both before and after
// execute this same harness and the production SQL/worker, one observation each.
func TestPhase4PerformanceMatrix(t *testing.T) {
	if os.Getenv("RAI_PHASE4_PERF") == "" {
		t.Skip("set RAI_PHASE4_PERF=baseline|after for isolated performance evidence")
	}
	for _, minutes := range []int{20, 40, 60, 120, 240} {
		t.Run(fmt.Sprint(minutes), func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			b := performanceBundle(t, f, minutes)
			s := time.Now()
			raw, err := json.Marshal(b)
			if err != nil {
				t.Fatal(err)
			}
			encode := time.Since(s)
			var zipped bytes.Buffer
			s = time.Now()
			gz := gzip.NewWriter(&zipped)
			_, _ = gz.Write(raw)
			_ = gz.Close()
			compress := time.Since(s)
			s = time.Now()
			reader, _ := gzip.NewReader(bytes.NewReader(zipped.Bytes()))
			_, err = io.Copy(io.Discard, reader)
			if err != nil {
				t.Fatal(err)
			}
			decompress := time.Since(s)
			s = time.Now()
			var decoded syncing.WorkoutBundle
			if err = syncing.DecodeWorkoutBundle(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			decode := time.Since(s)
			s = time.Now()
			if err = b.Validate(); err != nil {
				t.Fatal(err)
			}
			validation := time.Since(s)
			var wire bytes.Buffer
			mw := multipart.NewWriter(&wire)
			p, _ := mw.CreateFormFile("file", "workout.json")
			_, _ = p.Write(raw)
			_ = mw.Close()
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			s = time.Now()
			status, ack := uploadBundleHTTP(t, httpapi.New(f.s, logger), f.enrollment.Token, raw)
			upload := time.Since(s)
			if status != 200 {
				t.Fatalf("import HTTP %d", status)
			}
			if os.Getenv("RAI_PHASE4_PERF") == "after" {
				code, gzipACK := phase4Upload(t, httpapi.New(f.s, logger), f.enrollment.Token, raw, true)
				if code != 200 || !bytes.Equal(ack, gzipACK) {
					t.Fatalf("gzip identity/ACK mismatch: %d", code)
				}
			}

			var receipt syncing.WorkoutImportReceipt
			if json.Unmarshal(ack, &receipt) != nil {
				t.Fatal("invalid receipt")
			}
			if receipt.Counts.SamplesInserted != len(b.Samples) {
				t.Fatal("sample loss")
			}
			var projection int
			if err = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM route_points`).Scan(&projection); err != nil || projection != len(b.Routes[0].Points) {
				t.Fatal("route loss", err)
			}
			plans := map[string]any{}
			// Diagnostic EXPLAIN uses a finite 30s limit on its own connection. The
			// actual worker below retains the production statement_timeout unchanged.
			conn, err := f.s.Pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			_, err = conn.Exec(ctx, `SET statement_timeout='30s'`)
			if err != nil {
				t.Fatal(err)
			}
			queries := map[string]string{"samples": postgres.AnalysisSamplesSQL, "route": postgres.AnalysisRouteSQL, "workout": postgres.AnalysisWorkoutSQL, "series": `SELECT u.series_id::text,u.sample_type,c.chunk_index,c.points FROM workout_samples a JOIN series_uploads u USING(dataset_id,sample_type,sample_uuid) JOIN series_chunks c USING(dataset_id,series_id) WHERE a.dataset_id=$1 AND a.workout_uuid=$2 AND u.state='sealed' ORDER BY u.sample_type,u.sample_uuid,c.chunk_index`}

			scrub := regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
			for name, sql := range queries {
				var plan []byte
				err = conn.QueryRow(ctx, `EXPLAIN (ANALYZE,BUFFERS,WAL,VERBOSE,FORMAT JSON) `+sql, f.p.DatasetID, b.WorkoutID).Scan(&plan)
				if err != nil {
					t.Fatal(err)
				}
				var value any
				if json.Unmarshal([]byte(scrub.ReplaceAllString(string(plan), "SYNTHETIC-ID")), &value) != nil {
					t.Fatal("plan")
				}
				plans[name] = value
			}
			_, _ = conn.Exec(ctx, `RESET statement_timeout`)
			conn.Release()
			q := &phase4Queue{Store: f.s}
			lease, err := f.s.Claim(ctx, []string{"analysis.workout"})
			if err != nil {
				t.Fatal(err)
			}
			s = time.Now()
			jobs.Process(ctx, q, lease, logger)
			worker := time.Since(s)
			var state, code string
			if err = f.s.Pool.QueryRow(ctx, `SELECT state,COALESCE(last_error_code,'') FROM jobs WHERE id=$1`, lease.ID).Scan(&state, &code); err != nil {
				t.Fatal(err)
			}
			var mem runtime.MemStats
			runtime.ReadMemStats(&mem)
			var pgVersion string
			_ = f.s.Pool.QueryRow(ctx, `SHOW server_version`).Scan(&pgVersion)
			var tableBytes, indexBytes, walBytes int64
			_ = f.s.Pool.QueryRow(ctx, `SELECT pg_total_relation_size('health_samples'),pg_relation_size('health_samples_analysis_identity'),(SELECT wal_bytes::bigint FROM pg_stat_wal)`).Scan(&tableBytes, &indexBytes, &walBytes)

			result := map[string]any{"phase": os.Getenv("RAI_PHASE4_PERF"), "samples_table_total_bytes": tableBytes, "analysis_identity_index_bytes": indexBytes, "cluster_wal_bytes_cumulative": walBytes, "minutes": minutes, "samples": len(b.Samples), "gps": projection, "json_bytes": len(raw), "gzip_bytes": zipped.Len(), "multipart_bytes": wire.Len(), "encode_ms": float64(encode.Microseconds()) / 1000, "gzip_ms": float64(compress.Microseconds()) / 1000, "gunzip_ms": float64(decompress.Microseconds()) / 1000, "strict_decode_validation_ms": float64(decode.Microseconds()) / 1000, "validation_ms": float64(validation.Microseconds()) / 1000, "http_ack_ms": float64(upload.Microseconds()) / 1000, "load_analysis_ms": float64(q.load.Microseconds()) / 1000, "publication_ms": float64(q.publish.Microseconds()) / 1000, "worker_total_ms": float64(worker.Microseconds()) / 1000, "worker_state": state, "error_code": code, "go_heap_alloc_after_bytes": mem.HeapAlloc, "go_peak_rss": "NOT MEASURED", "pg_version": pgVersion, "plans": plans, "http_worker_metrics": strings.Split(scrub.ReplaceAllString(strings.TrimSpace(logs.String()), "SYNTHETIC-ID"), "\n")}
			out, _ := json.Marshal(result)
			t.Log("PHASE4_RESULT=" + string(out))
			if os.Getenv("RAI_PHASE4_PERF") == "after" && state != "succeeded" {
				t.Fatalf("worker state=%s code=%s", state, code)
			}
		})
	}
}

func TestPhase4IndexWriteCost(t *testing.T) {
	if os.Getenv("RAI_PHASE4_PERF") != "after" {
		t.Skip("explicit paired index write-cost measurement")
	}
	for _, indexed := range []bool{false, true} {
		t.Run(fmt.Sprint(indexed), func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			if !indexed {
				if _, err := f.s.Pool.Exec(ctx, `DROP INDEX health_samples_analysis_identity`); err != nil {
					t.Fatal(err)
				}
			}
			b := performanceBundle(t, f, 240)
			raw, err := json.Marshal(b)
			if err != nil {
				t.Fatal(err)
			}
			var before string
			_ = f.s.Pool.QueryRow(ctx, `SELECT pg_current_wal_insert_lsn()::text`).Scan(&before)
			s := time.Now()
			_, err = f.s.ImportWorkoutBundle(ctx, f.p, b, raw)
			elapsed := time.Since(s)
			if err != nil {
				t.Fatal(err)
			}
			var wal, index int64
			_ = f.s.Pool.QueryRow(ctx, `SELECT pg_wal_lsn_diff(pg_current_wal_insert_lsn(),$1::pg_lsn)::bigint`, before).Scan(&wal)
			if indexed {
				_ = f.s.Pool.QueryRow(ctx, `SELECT pg_relation_size('health_samples_analysis_identity')`).Scan(&index)
			}
			out, _ := json.Marshal(map[string]any{"index_present": indexed, "minutes": 240, "samples": len(b.Samples), "import_ms": float64(elapsed.Microseconds()) / 1000, "wal_bytes_delta": wal, "index_bytes": index, "observations": 1})
			t.Log("PHASE4_INDEX_COST=" + string(out))
		})
	}
}

func TestPhase4FreshGzipDurationMatrix(t *testing.T) {
	if os.Getenv("RAI_PHASE4_PERF") != "after" {
		t.Skip("explicit fresh gzip import/worker measurements")
	}
	for _, minutes := range []int{20, 40, 60, 120, 240} {
		t.Run(fmt.Sprint(minutes), func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			b := performanceBundle(t, f, minutes)
			raw, err := json.Marshal(b)
			if err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			status, ack := phase4Upload(t, httpapi.New(f.s, logger), f.enrollment.Token, raw, true)
			if status != 200 {
				t.Fatalf("fresh gzip HTTP %d", status)
			}
			var receipt syncing.WorkoutImportReceipt
			if json.Unmarshal(ack, &receipt) != nil || receipt.Counts.SamplesInserted != len(b.Samples) || receipt.BundleSHA256 != syncing.BundleDigest(raw) {
				t.Fatal("fresh gzip identity/counts")
			}
			q := &phase4Queue{Store: f.s}
			l, err := f.s.Claim(ctx, []string{"analysis.workout"})
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			jobs.Process(ctx, q, l, logger)
			total := time.Since(start)
			var state string
			if err = f.s.Pool.QueryRow(ctx, `SELECT state FROM jobs WHERE id=$1`, l.ID).Scan(&state); err != nil || state != "succeeded" {
				t.Fatal("fresh gzip worker", state, err)
			}
			metrics := []map[string]any{}
			for _, line := range strings.Split(logs.String(), "\n") {
				var m map[string]any
				if json.Unmarshal([]byte(line), &m) != nil {
					continue
				}
				if m["msg"] == "workout_bundle_transport" || m["msg"] == "workout_import_phase" {
					delete(m, "time")
					delete(m, "level")
					metrics = append(metrics, m)
				}
			}
			result := map[string]any{"minutes": minutes, "samples": len(b.Samples), "gps": len(b.Routes[0].Points), "json_bytes": len(raw), "fresh_import": true, "worker_state": state, "load_analysis_ms": float64(q.load.Microseconds()) / 1000, "publication_ms": float64(q.publish.Microseconds()) / 1000, "worker_total_ms": float64(total.Microseconds()) / 1000, "metrics": metrics}
			out, _ := json.Marshal(result)
			t.Log("PHASE4_GZIP_RESULT=" + string(out))
		})
	}
}
