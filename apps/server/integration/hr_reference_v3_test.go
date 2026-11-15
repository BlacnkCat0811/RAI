//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"rai/server/internal/httpapi"
	"rai/server/internal/modelinput"
	"rai/server/internal/syncing"
	"testing"
	"time"
)

func TestHRReferenceV3EndToEndAndRetry(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := syncing.NewID()
	var request modelinput.Request
	runStackDay(t, f, []stackDayRun{{id: id, day: -3, version: "11.0", hr: steadyHR}}, stackDayOptions{seed: func(today time.Time) {
		at := today.AddDate(0, 0, -180).Format(time.RFC3339)
		value := func(n float64) map[string]any {
			return map[string]any{"value": n, "basis": "user_setting", "method": "user_entered", "measuredAt": at, "unit": "bpm"}
		}
		request = modelinput.Request{ID: syncing.NewID(), SchemaVersion: modelinput.SchemaHRReferenceV3, Kind: "hr_reference", SubjectKey: "athlete", EffectiveFrom: today.AddDate(0, 0, -60).Format(time.RFC3339), Provenance: "synthetic integration", Payload: modelinput.Payload{"maxHR": value(190), "restingHR": map[string]any{"mode": "manual", "manualValue": value(50)}}}
		if _, err := f.s.CommitModelInput(ctx, f.p, request); err != nil {
			t.Fatal(err)
		}
	}})
	if _, err := f.s.Pool.Exec(ctx, `INSERT INTO analysis_model_channels SELECT (jsonb_populate_record(NULL::analysis_model_channels,to_jsonb(c)||'{"channel":"active"}')).* FROM analysis_model_channels c WHERE c.dataset_id=$1 AND c.channel='candidate' ON CONFLICT(dataset_id,channel) DO NOTHING`, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}
	detail, err := f.s.WorkoutDetail(ctx, f.p, id)
	if err != nil {
		t.Fatal(err)
	}
	if detail.TrainingLoad == nil {
		t.Fatal("old manual setting blocked load")
	}
	if detail.HeartRateReference == nil || detail.HeartRateReference.RestingHR.Status != "estimated" || !detail.HeartRateReference.RestingHR.ReviewRecommended {
		t.Fatal("missing published reference evidence")
	}
	if detail.TrainingEffect == nil || detail.TrainingEffect.Aerobic.Value == nil || detail.TrainingEffect.Aerobic.Status != "estimated" {
		t.Fatal("reference quality not propagated to aerobic effect")
	}
	if detail.TrainingEffect.Anaerobic.Value != nil {
		t.Fatal("missing pre-run speed reference became a score")
	}
	api := httpapi.New(f.s, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	for _, path := range []string{"/v2/analysis/parameters", "/v2/workouts/" + id + "/detail"} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("GET %s status=%d", path, rec.Code)
		}
		var response map[string]json.RawMessage
		if json.Unmarshal(rec.Body.Bytes(), &response) != nil {
			t.Fatal("invalid response")
		}
		if path == "/v2/analysis/parameters" {
			if response["savedHRSettings"] == nil || response["resolvedHRReference"] == nil {
				t.Fatal("settings HTTP contract missing")
			}
		} else if response["heartRateReference"] == nil {
			t.Fatal("detail HTTP contract missing")
		}
	}
	first, err := f.s.CommitModelInput(ctx, f.p, request)
	if err != nil {
		t.Fatal(err)
	}
	// Change only maximum, leave resting measurement and selection intact.
	update := request
	update.ID = syncing.NewID()
	update.Payload = modelinput.Payload{"maxHR": map[string]any{"value": 195.0, "basis": "user_setting", "method": "user_entered", "unit": "bpm"}}
	if _, err := f.s.CommitModelInput(ctx, f.p, update); err != nil {
		t.Fatal(err)
	}
	retry, err := f.s.CommitModelInput(ctx, f.p, request)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(retry)
	if string(a) != string(b) {
		t.Fatal("retry receipt changed")
	}
	settings, err := f.s.HRSettings(ctx, f.p)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Saved.RestingHR == nil || settings.Saved.RestingHR.ManualValue == nil || settings.Saved.RestingHR.ManualValue.MeasuredAt == nil {
		t.Fatal("maximum-only update cleared resting date")
	}
	if processStackJobs(t, f) == 0 {
		t.Fatal("reference change did not enqueue analysis")
	}
	rebuilt, err := f.s.WorkoutDetail(ctx, f.p, id)
	if err != nil || rebuilt.HeartRateReference == nil || rebuilt.HeartRateReference.MaxHR.Value == nil || *rebuilt.HeartRateReference.MaxHR.Value != 195 {
		t.Fatal("history did not use updated maximum", err)
	}
	if rebuilt.HeartRateReference.RestingHR.MeasuredAt == nil || !rebuilt.HeartRateReference.RestingHR.MeasuredAt.Equal(*detail.HeartRateReference.RestingHR.MeasuredAt) {
		t.Fatal("maximum update refreshed resting measurement")
	}
	clear := update
	clear.ID = syncing.NewID()
	clear.Payload = modelinput.Payload{"restingHR": map[string]any{"mode": "automatic", "manualValue": nil}}
	if _, err := f.s.CommitModelInput(ctx, f.p, clear); err != nil {
		t.Fatal(err)
	}
	if processStackJobs(t, f) == 0 {
		t.Fatal("clear did not enqueue analysis")
	}
	cleared, err := f.s.WorkoutDetail(ctx, f.p, id)
	if err != nil || cleared.TrainingLoad != nil || cleared.HeartRateReference == nil || cleared.HeartRateReference.RestingHR.Value != nil {
		t.Fatal("clear revived prior manual reference", err)
	}
	if _, err := f.s.RevokeModelInput(ctx, f.p, clear.ID, "synthetic undo"); err != nil {
		t.Fatal(err)
	}
	if processStackJobs(t, f) == 0 {
		t.Fatal("revocation did not enqueue analysis")
	}
	restored, err := f.s.WorkoutDetail(ctx, f.p, id)
	if err != nil || restored.TrainingLoad == nil {
		t.Fatal("revocation did not restore prior setting", err)
	}
	invalid := update
	invalid.ID = syncing.NewID()
	invalid.Payload = modelinput.Payload{"maxHR": map[string]any{"value": 40.0, "basis": "user_setting", "method": "user_entered", "unit": "bpm"}}
	if _, err := f.s.CommitModelInput(ctx, f.p, invalid); err == nil {
		t.Fatal("cross-patch invalid reference admitted")
	}
	var receipts int
	if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM model_input_receipts WHERE dataset_id=$1 AND input_id=$2`, f.p.DatasetID, invalid.ID).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatal("failed commit left a receipt")
	}
}
