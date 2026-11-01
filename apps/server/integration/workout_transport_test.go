//go:build integration

package integration

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"rai/server/internal/httpapi"
	"rai/server/internal/jobs"
	"rai/server/internal/syncing"
	"testing"
	"time"
)

func phase4Upload(t *testing.T, h http.Handler, token string, raw []byte, compressed bool) (int, []byte) {
	t.Helper()
	media := "application/json"
	data := raw
	if compressed {
		var b bytes.Buffer
		gz := gzip.NewWriter(&b)
		_, _ = gz.Write(raw)
		_ = gz.Close()
		data = b.Bytes()
		media = "application/gzip"
	}
	return phase4UploadWire(t, h, token, data, media)
}
func phase4UploadWire(t *testing.T, h http.Handler, token string, data []byte, media string) (int, []byte) {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	p, err := w.CreatePart(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="file"; filename="workout.json"`}, "Content-Type": {media}})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = p.Write(data)
	_ = w.Close()
	r := httptest.NewRequest("POST", "/v2/sync/workout-bundles", &body)
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", w.FormDataContentType())
	out := httptest.NewRecorder()
	h.ServeHTTP(out, r)
	return out.Code, out.Body.Bytes()
}

func TestPhase4GzipFaultsNeverEnterImportAndRecoverACK(t *testing.T) {
	f := setup(t)
	b, _ := testWorkoutBundle(t, f, 3)
	phase3Observation(&b)
	raw, _ := json.Marshal(b)
	h := httpapi.New(f.s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	code, ack := phase4Upload(t, h, f.enrollment.Token, raw, true)
	if code != 200 {
		t.Fatalf("gzip HTTP %d", code)
	}
	code, plain := phase4Upload(t, h, f.enrollment.Token, raw, false)
	if code != 200 || !bytes.Equal(ack, plain) {
		t.Fatal("wire encoding changed business hash/ACK")
	}
	var receipt syncing.WorkoutImportReceipt
	_ = json.Unmarshal(ack, &receipt)
	if receipt.BundleSHA256 != syncing.BundleDigest(raw) {
		t.Fatal("gzip hash identity")
	}
	recovered, err := f.s.WorkoutImport(context.Background(), f.p, b.WorkoutID, receipt.BundleSHA256)
	if err != nil || !bytes.Equal(bytes.TrimSpace(ack), bytes.TrimSpace(recovered)) {
		t.Fatal("ACK lost recovery", err)
	}
	var alternate bytes.Buffer
	gz := gzip.NewWriter(&alternate)
	gz.Header.ModTime = time.Unix(12345, 0)
	gz.Header.Name = "synthetic-alternative-header"
	_, _ = gz.Write(raw)
	_ = gz.Close()
	// Isolate transport/hash behavior from the separately tested two-attempt
	// device burst budget; mutate only this disposable fixture's rate counters.
	resetRate := func() {
		t.Helper()
		if _, err := f.s.Pool.Exec(context.Background(), `DELETE FROM sync_request_budgets WHERE dataset_id=$1`, f.p.DatasetID); err != nil {
			t.Fatal(err)
		}
	}
	resetRate()
	code, alternativeACK := phase4UploadWire(t, h, f.enrollment.Token, alternate.Bytes(), "application/gzip")
	if code != 200 || !bytes.Equal(ack, alternativeACK) {
		t.Fatalf("gzip header changed semantic identity: status=%d body=%s", code, alternativeACK)
	}
	corrupt := append([]byte(nil), alternate.Bytes()...)
	corrupt[len(corrupt)-8] ^= 255
	for _, invalid := range [][]byte{corrupt, alternate.Bytes()[:alternate.Len()-4], []byte("not gzip")} {
		resetRate()
		status, _ := phase4UploadWire(t, h, f.enrollment.Token, invalid, "application/gzip")
		if status != 422 || count(t, f, "workout_imports") != 1 || count(t, f, "health_samples") != 3 {
			t.Fatal("corrupt gzip changed committed facts", status)
		}
	}

	if count(t, f, "workout_imports") != 1 || count(t, f, "jobs") != 1 {
		t.Fatal("duplicate import")
	}
}

func TestPhase4PayloadVariantWorkers(t *testing.T) {
	for _, variant := range []string{"no_route", "high_density_route", "multiple_routes", "many_samples", "many_series"} {
		t.Run(variant, func(t *testing.T) {
			f := setup(t)
			b := performanceBundle(t, f, 20)
			switch variant {
			case "no_route":
				b.Routes = nil
			case "high_density_route":
				r := &b.Routes[0]
				original := append([]syncing.WorkoutBundlePoint(nil), r.Points...)
				for i := 0; i < 9; i++ {
					r.Points = append(r.Points, original...)
				}
			case "multiple_routes":
				r := b.Routes[0]
				r.RouteID = syncing.NewID()
				b.Routes = append(b.Routes, r)
			case "many_samples":
				for len(b.Samples) < 50000 {
					e := b.Samples[len(b.Samples)%2197]
					e.UUID = syncing.NewID()
					b.Samples = append(b.Samples, e)
				}
			case "many_series":
				for i := range b.Samples {
					e := &b.Samples[i]
					end := *e.StartUs + 2
					e.EndUs = &end
					b.Series = append(b.Series, syncing.WorkoutBundleSeries{SampleType: e.Type, SampleUUID: e.UUID, Points: []syncing.SeriesPoint{{StartUs: *e.StartUs, EndUs: *e.StartUs, Value: 1, Unit: e.Quantity.Unit}, {StartUs: end, EndUs: end, Value: 2, Unit: e.Quantity.Unit}}})
				}
			}
			phase3Observation(&b)
			raw, err := json.Marshal(b)
			if err != nil {
				t.Fatal(err)
			}
			code, ack := phase4Upload(t, httpapi.New(f.s, slog.New(slog.NewTextHandler(io.Discard, nil))), f.enrollment.Token, raw, true)
			if code != 200 {
				t.Fatalf("variant HTTP %d", code)
			}
			var receipt syncing.WorkoutImportReceipt
			_ = json.Unmarshal(ack, &receipt)
			if receipt.Counts.SamplesInserted != len(b.Samples) {
				t.Fatal("sample loss")
			}
			points := 0
			for _, r := range b.Routes {
				points += len(r.Points)
			}
			if count(t, f, "route_points") != points {
				t.Fatal("route loss")
			}
			lease, err := f.s.Claim(context.Background(), []string{"analysis.workout"})
			if err != nil {
				t.Fatal(err)
			}
			jobs.Process(context.Background(), f.s, lease, slog.New(slog.NewTextHandler(io.Discard, nil)))
			var state string
			err = f.s.Pool.QueryRow(context.Background(), `SELECT state FROM jobs WHERE id=$1`, lease.ID).Scan(&state)
			if err != nil || state != "succeeded" {
				t.Fatal("variant worker", state, err)
			}
		})
	}
}
