//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"rai/server/internal/httpapi"
	"rai/server/internal/syncing"
)

func TestWorkoutBundleRequiredFieldsHTTPAtomic(t *testing.T) {
	cases := []struct {
		name, path, reason string
		mutate             func(map[string]any)
	}{
		{"missing workout uuid", "workout.uuid", "required", func(b map[string]any) { delete(b["workout"].(map[string]any), "uuid") }},
		{"missing start", "workout.startUs", "required", func(b map[string]any) { delete(b["workout"].(map[string]any), "startUs") }},
		{"missing end", "workout.endUs", "required", func(b map[string]any) { delete(b["workout"].(map[string]any), "endUs") }},
		{"missing latitude", "routes[0].points[0].latitude", "required", func(b map[string]any) { delete(routePoint(b), "latitude") }},
		{"missing longitude", "routes[0].points[0].longitude", "required", func(b map[string]any) { delete(routePoint(b), "longitude") }},
		{"missing timestamp", "routes[0].points[0].recordedAtUs", "required", func(b map[string]any) { delete(routePoint(b), "recordedAtUs") }},
		{"missing quantity", "samples[0].quantity", "required", func(b map[string]any) { delete(sampleObject(b), "quantity") }},
		{"missing value", "samples[0].quantity.value", "required", func(b map[string]any) { delete(sampleObject(b)["quantity"].(map[string]any), "value") }},
		{"null value", "samples[0].quantity.value", "null_not_allowed", func(b map[string]any) { sampleObject(b)["quantity"].(map[string]any)["value"] = nil }},
		{"null optional", "routes[0].points[0].altitude", "null_not_allowed", func(b map[string]any) { routePoint(b)["altitude"] = nil }},
		{"wrong type", "routes[0].points[0].latitude", "invalid_type", func(b map[string]any) { routePoint(b)["latitude"] = "0" }},
		{"wrong unit", "samples[0].quantity.unit", "unsupported_unit", func(b map[string]any) { sampleObject(b)["quantity"].(map[string]any)["unit"] = "W" }},
		{"invalid range", "workout.endUs", "invalid_time_range", func(b map[string]any) { b["workout"].(map[string]any)["endUs"] = 0 }},
		{"bad latitude", "routes[0].points[0].latitude", "out_of_range", func(b map[string]any) { routePoint(b)["latitude"] = 91 }},
		{"series missing value", "series[0].points[0].value", "required", func(b map[string]any) {
			delete(b["series"].([]any)[0].(map[string]any)["points"].([]any)[0].(map[string]any), "value")
		}},
		{"distance missing value", "workout.workout.totalDistance.value", "required", func(b map[string]any) {
			b["workout"].(map[string]any)["workout"].(map[string]any)["totalDistance"] = map[string]any{"unit": "m"}
		}},
		{"source missing", "workout.source.bundleID", "required", func(b map[string]any) { delete(b["workout"].(map[string]any)["source"].(map[string]any), "bundleID") }},
		{"missing protocol", "protocolVersion", "required", func(b map[string]any) { delete(b, "protocolVersion") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			_, raw := testWorkoutBundle(t, f, 1)
			var object map[string]any
			if err := json.Unmarshal(raw, &object); err != nil {
				t.Fatal(err)
			}
			tc.mutate(object)
			invalid, _ := json.Marshal(object)
			before := phase1Snapshot(t, f)
			status, body := uploadBundleHTTP(t, httpapi.New(f.s, slog.Default()), f.enrollment.Token, invalid)
			var failure syncing.Error
			if err := json.Unmarshal(body, &failure); err != nil {
				t.Fatal(err)
			}
			if status != 422 || failure.Code != "unsupported_payload" || failure.Field != tc.path || failure.Reason != tc.reason {
				t.Fatalf("status=%d error=%s", status, body)
			}
			if !reflect.DeepEqual(before, phase1Snapshot(t, f)) {
				t.Fatal("invalid payload left partial database effects")
			}
			if count(t, f, "workout_imports") != 0 || count(t, f, "jobs") != 0 || count(t, f, "entity_tombstones") != 0 {
				t.Fatal("invalid payload acknowledged/queued/deleted")
			}
		})
	}
}
func routePoint(b map[string]any) map[string]any {
	return b["routes"].([]any)[0].(map[string]any)["points"].([]any)[0].(map[string]any)
}
func sampleObject(b map[string]any) map[string]any { return b["samples"].([]any)[0].(map[string]any) }

func TestWorkoutBundleZeroAndLegacyOptionalHTTP(t *testing.T) {
	f := setup(t)
	b, _ := testWorkoutBundle(t, f, 1)
	b.Routes[0].Points[0].Latitude = 0
	b.Routes[0].Points[0].Longitude = 0
	b.Samples[0].Quantity.Value = 0
	b.Series[0].Points[0].Value = 0
	raw, _ := json.Marshal(b)
	handler := httpapi.New(f.s, slog.Default())
	code, body := uploadBundleHTTP(t, handler, f.enrollment.Token, raw)
	if code != 200 {
		t.Fatalf("valid existing v3 rejected: %d %s", code, body)
	}
	var receipt syncing.WorkoutImportReceipt
	if json.Unmarshal(body, &receipt) != nil || receipt.CommitRevision <= 0 {
		t.Fatal("no durable receipt")
	}
	var lat, lon, value float64
	if err := f.s.Pool.QueryRow(context.Background(), `SELECT latitude,longitude FROM route_points WHERE route_uuid=$1 ORDER BY sequence LIMIT 1`, b.Routes[0].RouteID).Scan(&lat, &lon); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Pool.QueryRow(context.Background(), `SELECT value_numeric FROM health_samples WHERE sample_uuid=$1`, b.Samples[0].UUID).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if lat != 0 || lon != 0 || value != 0 {
		t.Fatal("zero values not preserved")
	}
	again, err := f.s.ImportWorkoutBundle(context.Background(), f.p, b, raw)
	if err != nil || !bytes.Equal(bytes.TrimSpace(body), again) {
		t.Fatal("unstable receipt", err)
	}
	if count(t, f, "health_samples") != 1 || count(t, f, "workout_imports") != 1 {
		t.Fatal("duplicate raw facts/receipts")
	}
}
func TestWorkoutBundleNonfiniteAndMalformedHTTP(t *testing.T) {
	for _, token := range []string{"NaN", "Infinity", "1e999"} {
		t.Run(token, func(t *testing.T) {
			f := setup(t)
			_, raw := testWorkoutBundle(t, f, 1)
			invalid := strings.Replace(string(raw), `"latitude":31.2304`, `"latitude":`+token, 1)
			before := phase1Snapshot(t, f)
			code, body := uploadBundleHTTP(t, httpapi.New(f.s, slog.Default()), f.enrollment.Token, []byte(invalid))
			if code != 422 || !bytes.Contains(body, []byte(`"code":"unsupported_payload"`)) {
				t.Fatalf("status=%d %s", code, body)
			}
			if !reflect.DeepEqual(before, phase1Snapshot(t, f)) {
				t.Fatal("malformed data changed facts")
			}
		})
	}
}

// The response must be absent when a deferred database trigger rejects COMMIT.
func TestWorkoutBundleCommitFailureHasNoACK(t *testing.T) {
	f := setup(t)
	_, raw := testWorkoutBundle(t, f, 1)
	before := phase1Snapshot(t, f)
	_, err := f.s.Pool.Exec(context.Background(), `CREATE FUNCTION reject_bundle_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic commit failure'; END $$;
 CREATE CONSTRAINT TRIGGER reject_bundle_commit AFTER INSERT ON workout_imports DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_bundle_commit()`)
	if err != nil {
		t.Fatal(err)
	}
	code, body := uploadBundleHTTP(t, httpapi.New(f.s, slog.Default()), f.enrollment.Token, raw)
	if code != http.StatusServiceUnavailable || bytes.Contains(body, []byte(`"ingestStatus":"committed"`)) {
		t.Fatalf("uncommitted ACK: %d %s", code, body)
	}
	if !reflect.DeepEqual(before, phase1Snapshot(t, f)) {
		t.Fatal("failed COMMIT left database effects")
	}
}

func TestWorkoutBundleLostACKAnd404ThenIdempotentPOST(t *testing.T) {
	f := setup(t)
	b, raw := testWorkoutBundle(t, f, 1)
	handler := httpapi.New(f.s, slog.Default())
	// Real COMMIT, deliberately discard its response (transport-side ACK loss).
	code, first := uploadBundleHTTP(t, handler, f.enrollment.Token, raw)
	if code != 200 {
		t.Fatalf("%d %s", code, first)
	}
	before := phase1Snapshot(t, f)
	// An intermediary may emit 404 even though this import is durable.
	proxy := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(404)
			return
		}
		handler.ServeHTTP(w, r)
	})
	req := httptest.NewRequest(http.MethodGet, "/v2/sync/workout-bundles/"+b.WorkoutID+"?sha256="+syncing.BundleDigest(raw), nil)
	rec := httptest.NewRecorder()
	proxy.ServeHTTP(rec, req)
	if rec.Code != 404 {
		t.Fatal(rec.Code)
	}
	// Use the storage call to avoid testing the unrelated HTTP burst budget here.
	second, err := f.s.ImportWorkoutBundle(context.Background(), f.p, b, raw)
	if err != nil || !bytes.Equal(bytes.TrimSpace(first), second) {
		t.Fatal("retry receipt changed", err)
	}
	if !reflect.DeepEqual(before, phase1Snapshot(t, f)) {
		t.Fatal("retry duplicated raw facts/jobs")
	}
	recovered, err := f.s.WorkoutImport(context.Background(), f.p, b.WorkoutID, syncing.BundleDigest(raw))
	if err != nil || !bytes.Equal(second, recovered) {
		t.Fatal("GET cannot recover exact durable receipt", err)
	}
}
