package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"

	"rai/server/internal/training"
)

func TestTrainingStrictWireMatrix(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"canonical", `{"commandID":"c","overrideID":"o","sessionID":"s","operation":"edit","expectedOverrideRevision":0,"path":"distanceM","value":7000}`, true},
		{"missing_revision", `{"commandID":"c","overrideID":"o","sessionID":"s","operation":"skip"}`, false},
		{"case", `{"CommandID":"c","overrideID":"o","sessionID":"s","operation":"skip","expectedOverrideRevision":0}`, false},
		{"duplicate", `{"commandID":"c","overrideID":"o","sessionID":"s","operation":"edit","expectedOverrideRevision":0,"value":1,"value":2}`, false},
		{"unknown", `{"commandID":"c","overrideID":"o","sessionID":"s","operation":"skip","expectedOverrideRevision":0,"extra":1}`, false},
		{"wrong_type", `{"commandID":"c","overrideID":"o","sessionID":"s","operation":"skip","expectedOverrideRevision":"0"}`, false},
		{"null_revision", `{"commandID":"c","overrideID":"o","sessionID":"s","operation":"skip","expectedOverrideRevision":null}`, false},
		{"null_notes", `{"commandID":"c","overrideID":"o","sessionID":"s","operation":"add","expectedOverrideRevision":0,"session":{"sessionID":"s","date":"2026-10-29","type":"EASY","notes":null}}`, false},
		{"nested_case", `{"commandID":"c","overrideID":"o","sessionID":"s","operation":"add","expectedOverrideRevision":0,"session":{"SessionID":"s","date":"2026-10-29","type":"EASY"}}`, false},
		{"nested_duplicate", `{"commandID":"c","overrideID":"o","sessionID":"s","operation":"add","expectedOverrideRevision":0,"session":{"sessionID":"s","date":"2026-10-29","type":"EASY","notes":"a","notes":"b"}}`, false},
		{"missing_step_field", `{"commandID":"c","overrideID":"o","sessionID":"s","operation":"add","expectedOverrideRevision":0,"session":{"sessionID":"s","date":"2026-10-29","type":"INTERVAL","steps":[{"kind":"warmup","durationS":60}]}}`, false},
		{"nullable_manual", `{"commandID":"c","overrideID":"o","sessionID":"s","operation":"add","expectedOverrideRevision":0,"session":{"sessionID":"s","date":"2026-10-29","type":"EASY","targetHR":null,"paceSPerKM":null}}`, true},
		{"clear_value", `{"commandID":"c","overrideID":"o","sessionID":"s","operation":"edit","expectedOverrideRevision":0,"path":"targetHR","value":null}`, true},
		{"trailing", `{} {}`, false},
		{"root_null", `null`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out training.OverrideRequest
			r := httptest.NewRequest("POST", "/v2/training/plans/p/overrides", strings.NewReader(tc.body))
			r.Header.Set("Content-Type", "application/json")
			if (trainingBody(httptest.NewRecorder(), r, &out) == nil) != tc.valid {
				t.Fatal("wire validation mismatch")
			}
		})
	}
}

func TestTrainingAllRequestRequiredFields(t *testing.T) {
	for _, out := range []any{&training.CreateRequest{}, &training.ChangeRequest{}, &training.StartRequest{}, &training.ExecutionChange{}} {
		r := httptest.NewRequest("POST", "/v2/training/plans", strings.NewReader(`{}`))
		r.Header.Set("Content-Type", "application/json")
		if trainingBody(httptest.NewRecorder(), r, out) == nil {
			t.Fatal("missing fields accepted")
		}
	}
}

func TestTrainingConfigRequiredFieldsByVersion(t *testing.T) {
	for _, tc := range []struct {
		name, config string
		valid        bool
	}{
		{"v1", `{"startDate":"2026-11-16","weeks":8,"goal":{"type":"running","date":"2026-12-20","distanceM":10000},"availableDays":[2,4,7],"longRunDay":7}`, true},
		{"v1_no_goal", `{"startDate":"2026-11-16","weeks":8,"availableDays":[2,4,7],"longRunDay":7}`, false},
		{"v2_no_goal", `{"version":"training_config/v2","startDate":"2026-11-16","availableDays":[2,4,7],"longRunDay":7}`, true},
		{"v2_goal", `{"version":"training_config/v2","startDate":"2026-11-16","availableDays":[2,4,7],"longRunDay":7,"goal":{"type":"running","date":"2027-03-21","distanceM":21097.5,"targetTimeS":6300},"baselineWeeklyS":9000}`, true},
		{"v2_partial_goal", `{"version":"training_config/v2","startDate":"2026-11-16","availableDays":[2,4,7],"longRunDay":7,"goal":{"date":"2027-03-21"}}`, false},
		{"v2_missing_days", `{"version":"training_config/v2","startDate":"2026-11-16","longRunDay":7}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out training.CreateRequest
			body := `{"commandID":"c","planID":"p","config":` + tc.config + `}`
			r := httptest.NewRequest("POST", "/v2/training/plans", strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
			if (trainingBody(httptest.NewRecorder(), r, &out) == nil) != tc.valid {
				t.Fatal("config validation mismatch")
			}
		})
	}
}
