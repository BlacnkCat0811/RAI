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
			if (trainingBody(httptest.NewRecorder(), r, &out) == nil) != tc.valid {
				t.Fatal("wire validation mismatch")
			}
		})
	}
}

func TestTrainingAllRequestRequiredFields(t *testing.T) {
	for _, out := range []any{&training.CreateRequest{}, &training.ChangeRequest{}, &training.StartRequest{}, &training.ExecutionChange{}} {
		r := httptest.NewRequest("POST", "/v2/training/plans", strings.NewReader(`{}`))
		if trainingBody(httptest.NewRecorder(), r, out) == nil {
			t.Fatal("missing fields accepted")
		}
	}
}
