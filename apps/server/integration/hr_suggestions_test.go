//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"
	"time"

	"rai/server/internal/httpapi"
	"rai/server/internal/modelinput"
	"rai/server/internal/syncing"
)

func TestHeartRateSuggestionsReadOnlyAndDeletedSamples(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	now := time.Now().UTC()
	start := now.AddDate(0, 0, -30).UnixMicro()
	batch := syncing.DailyBatch{Version: 1, DatasetID: f.enrollment.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch,
		StreamID: syncing.NewID(), BatchID: syncing.NewID(), ClientSeq: 1, SampleType: syncing.DailyRHR,
		QueryFingerprint: syncing.DailyFingerprint(syncing.DailyRHR, start), BootstrapStartUs: start, ObservedAtUs: now.UnixMicro(),
		Facts: []syncing.DailyFact{}, DeletedUUIDs: []string{}}
	for i := 1; i <= 20; i++ {
		v := float64(50 + i%3)
		at := now.AddDate(0, 0, -i).UnixMicro()
		batch.Facts = append(batch.Facts, syncing.DailyFact{UUID: syncing.NewID(), StartUs: at, EndUs: at, Quantity: &syncing.Quantity{Value: v, Unit: "count/min"},
			Source: syncing.HealthKitSource{BundleID: "com.apple.health", Name: "Synthetic Watch", ProductType: "Watch7,1", Version: "11.0", OperatingSystemVersion: "11.0"}, Metadata: map[string]any{"HKTimeZone": "Asia/Shanghai"}})
		if i%2 == 0 {
			batch.Facts[len(batch.Facts)-1].Source.Version = "12.0"
			batch.Facts[len(batch.Facts)-1].Source.OperatingSystemVersion = "12.0"
		}
	}
	commit := func() {
		t.Helper()
		raw, err := json.Marshal(batch)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = commitDaily(t, f, raw); err != nil {
			t.Fatal(err)
		}
	}
	commit()
	value := func(v float64) map[string]any {
		return map[string]any{"value": v, "basis": "user_setting", "method": "user_entered", "unit": "bpm"}
	}
	if _, err := f.s.CommitModelInput(ctx, f.p, modelinput.Request{ID: syncing.NewID(), Kind: "hr_reference", SchemaVersion: modelinput.SchemaHRReferenceV3,
		SubjectKey: "athlete", EffectiveFrom: now.Add(-time.Hour).Format(time.RFC3339), Provenance: "synthetic suggestion test",
		Payload: modelinput.Payload{"maxHR": value(190), "restingHR": map[string]any{"mode": "manual", "manualValue": value(60)}}}); err != nil {
		t.Fatal(err)
	}
	settings, err := f.s.HRSettings(ctx, f.p)
	if err != nil || settings.ObservedResting == nil || settings.ObservedResting.Value != 51 || settings.ObservedResting.SampleDays != 20 {
		t.Fatal("stable HealthKit observations missing from settings", err)
	}
	if settings.Saved.RestingHR == nil || settings.Saved.RestingHR.ManualValue.Value != 60 || settings.Resolved != nil {
		t.Fatal("suggestion changed the setting or pretended an inactive model used it")
	}
	api := httpapi.New(f.s, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	before := count(t, f, "athlete_model_inputs")
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("GET", "/v2/analysis/parameters", nil)
		req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
		rec := httptest.NewRecorder()
		api.ServeHTTP(rec, req)
		var response map[string]json.RawMessage
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &response) != nil || string(response["observedRestingHR"]) == "null" || len(response["observedRestingHR"]) == 0 {
			t.Fatal("HTTP suggestion contract missing")
		}
	}
	if count(t, f, "athlete_model_inputs") != before {
		t.Fatal("reading a suggestion wrote a setting")
	}
	// A tombstone removes only that observation; it does not block the window.
	deleted := []string{}
	for _, fact := range batch.Facts[:8] {
		deleted = append(deleted, fact.UUID)
	}
	previous := batch.BatchID
	batch.PreviousBatchID = &previous
	batch.BatchID = syncing.NewID()
	batch.ClientSeq++
	batch.DeletedUUIDs = deleted[:1]
	batch.Facts = []syncing.DailyFact{}
	commit()
	settings, err = f.s.HRSettings(ctx, f.p)
	if err != nil || settings.ObservedResting == nil || settings.ObservedResting.SampleDays != 19 {
		t.Fatal("one deletion blocked remaining stable observations", err)
	}
	previous = batch.BatchID
	batch.BatchID = syncing.NewID()
	batch.ClientSeq++
	batch.DeletedUUIDs = deleted[1:]
	commit()
	settings, err = f.s.HRSettings(ctx, f.p)
	if err != nil || settings.ObservedResting != nil || settings.Saved.RestingHR.ManualValue.Value != 60 {
		t.Fatal("deleted observations retained a suggestion or overwrote a manual setting", err)
	}
}

func TestHeartRateSuggestionsDoNotPoolSourcesOrManualSamples(t *testing.T) {
	for _, mode := range []string{"different_devices", "manual_metadata", "manual_source", "unfinished"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t)
			now := time.Now().UTC()
			start := now.AddDate(0, 0, -30).UnixMicro()
			batch := syncing.DailyBatch{Version: 1, DatasetID: f.enrollment.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch,
				StreamID: syncing.NewID(), BatchID: syncing.NewID(), ClientSeq: 1, SampleType: syncing.DailyRHR,
				QueryFingerprint: syncing.DailyFingerprint(syncing.DailyRHR, start), BootstrapStartUs: start, ObservedAtUs: now.UnixMicro(), Facts: []syncing.DailyFact{}, DeletedUUIDs: []string{}}
			for i := 1; i <= 20; i++ {
				at := now.AddDate(0, 0, -i).UnixMicro()
				fact := syncing.DailyFact{UUID: syncing.NewID(), StartUs: at, EndUs: at, Quantity: &syncing.Quantity{Value: 51, Unit: "count/min"},
					Source: syncing.HealthKitSource{BundleID: "com.apple.health", ProductType: "Watch7,1"}, Metadata: map[string]any{}}
				switch mode {
				case "different_devices":
					if i%2 == 0 {
						fact.Source.ProductType = "Watch8,1"
					}
				case "manual_metadata":
					fact.Metadata["HKWasUserEntered"] = true
				case "manual_source":
					fact.Source = syncing.HealthKitSource{BundleID: "com.apple.Health"}
				case "unfinished":
					fact.EndUs = now.Add(time.Hour).UnixMicro()
				}
				batch.Facts = append(batch.Facts, fact)
			}
			raw, err := json.Marshal(batch)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err = commitDaily(t, f, raw); err != nil {
				t.Fatal(err)
			}
			settings, err := f.s.HRSettings(context.Background(), f.p)
			if err != nil || settings.ObservedResting != nil {
				t.Fatal("ineligible samples produced a suggestion", err)
			}
		})
	}
}
