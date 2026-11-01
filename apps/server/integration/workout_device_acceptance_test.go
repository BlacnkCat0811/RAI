//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"rai/server/internal/httpapi"
	"rai/server/internal/syncing"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Explicitly gated, user-designated one-Workout acceptance. The normal suite
// uses only synthetic fixtures. Never points at an existing/local production DB.
func TestPhase4DesignatedDeviceBackend(t *testing.T) {
	dir := os.Getenv("RAI_PHASE4_DEVICE_DIR")
	if dir == "" {
		t.Skip("requires explicit designated-device acceptance run")
	}
	if !strings.HasPrefix(dir, "/tmp/rai-phase4-device-") {
		t.Fatal("private acceptance directory required")
	}
	f := setup(t)
	ctx := context.Background()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httpapi.NewHTTPServer("", httpapi.New(f.s, logger))
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	config := map[string]any{"baseURL": "http://rai-dev.local:" + strconv.Itoa(listener.Addr().(*net.TCPAddr).Port), "collectorEpoch": f.enrollment.CollectorEpoch, "token": f.enrollment.Token, "outboxNamespace": syncing.NewID()}
	data, _ := json.Marshal(config)
	if err = os.WriteFile(filepath.Join(dir, "phase4-config.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "ready"), []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	timer := time.NewTimer(12 * time.Minute)
	defer timer.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-timer.C:
			t.Fatal("device acceptance timed out")
		case <-ticker.C:
			if _, err = os.Stat(filepath.Join(dir, "done")); err == nil {
				goto verify
			}
		}
	}
verify:
	if err = server.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	var bundleHash string
	if err = f.s.Pool.QueryRow(ctx, `SELECT payload,encode(bundle_sha256,'hex') FROM workout_imports ORDER BY imported_at DESC LIMIT 1`).Scan(&raw, &bundleHash); err != nil {
		t.Fatal(err)
	}
	var b syncing.WorkoutBundle
	if json.Unmarshal(raw, &b) != nil {
		t.Fatal("stored bundle invalid")
	}
	var imports, samples, points int
	_ = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM workout_imports`).Scan(&imports)
	_ = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM health_samples`).Scan(&samples)
	_ = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM route_points`).Scan(&points)
	if imports != 1 || samples != len(b.Samples) {
		t.Fatal("device import counts")
	}
	total := 0
	routes := []map[string]any{}
	for _, r := range b.Routes {
		total += len(r.Points)
		rows, e := f.s.Pool.Query(ctx, `SELECT sequence,(extract(epoch from recorded_at)*1000000)::bigint,latitude,longitude,altitude,horizontal_accuracy,vertical_accuracy,speed,course FROM route_points WHERE dataset_id=$1 AND route_uuid=$2 ORDER BY sequence`, f.p.DatasetID, r.RouteID)
		if e != nil {
			t.Fatal(e)
		}
		index := 0
		for rows.Next() {
			var seq int64
			var p syncing.WorkoutBundlePoint
			if e = rows.Scan(&seq, &p.RecordedAtUs, &p.Latitude, &p.Longitude, &p.Altitude, &p.HorizontalAccuracy, &p.VerticalAccuracy, &p.Speed, &p.Course); e != nil {
				t.Fatal(e)
			}
			if seq != int64(index) || index >= len(r.Points) || !reflect.DeepEqual(p, r.Points[index]) {
				t.Fatal("device projection mismatch")
			}
			index++
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			t.Fatal(e)
		}
		chunkRows, e := f.s.Pool.Query(ctx, `SELECT points FROM route_chunks WHERE dataset_id=$1 AND route_uuid=$2 ORDER BY chunk_index`, f.p.DatasetID, r.RouteID)
		if e != nil {
			t.Fatal(e)
		}
		var reconstructed []syncing.RoutePoint
		for chunkRows.Next() {
			var data []byte
			if e = chunkRows.Scan(&data); e != nil {
				t.Fatal(e)
			}
			var ps []syncing.RoutePoint
			if json.Unmarshal(data, &ps) != nil {
				t.Fatal("chunk decode")
			}
			reconstructed = append(reconstructed, ps...)
		}
		e = chunkRows.Err()
		chunkRows.Close()
		if e != nil {
			t.Fatal(e)
		}
		for i, p := range r.Points {
			if i >= len(reconstructed) || !reflect.DeepEqual(p.ToRoutePoint(int64(i)), reconstructed[i]) {
				t.Fatal("device chunk mismatch")
			}
		}
		var manifest string
		_ = f.s.Pool.QueryRow(ctx, `SELECT encode(manifest_sha256,'hex') FROM workout_routes WHERE dataset_id=$1 AND route_uuid=$2`, f.p.DatasetID, r.RouteID).Scan(&manifest)
		routes = append(routes, map[string]any{"bundle_points": len(r.Points), "projection_points": index, "chunk_points": len(reconstructed), "manifest_sha256": manifest, "pointwise_equal": true})
	}
	if total != points {
		t.Fatal("device route total mismatch")
	}
	// Transport metrics contain only sizes/timings; discard correlation UUIDs.
	metrics := []map[string]any{}
	for _, line := range strings.Split(logs.String(), "\n") {
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil && m["msg"] == "workout_bundle_transport" {
			delete(m, "time")
			delete(m, "level")
			metrics = append(metrics, m)
		}
	}
	result := map[string]any{"schema_version": 17, "protocol_version": 3, "imports": imports, "samples": samples, "bundle_sha256": bundleHash, "bundle_points": total, "projection_points": points, "routes": routes, "transport": metrics, "health_values_exported": false, "gps_exported": false}
	out, _ := json.MarshalIndent(result, "", "  ")
	if err = os.WriteFile(filepath.Join(dir, "server-evidence.json"), out, 0600); err != nil {
		t.Fatal(err)
	}
}
