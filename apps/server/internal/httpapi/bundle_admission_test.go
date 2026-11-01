package httpapi

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"rai/server/internal/syncing"
)

type countingBody struct {
	reads int
}

func (c *countingBody) Read([]byte) (int, error) { c.reads++; return 0, io.EOF }
func (c *countingBody) Close() error             { return nil }

type admissionStore struct{ syncing.Store }

func TestBundleRejectsBeforeReadingBodyWhenDeviceSlotBusy(t *testing.T) {
	p := syncing.Principal{DatasetID: "11111111-1111-4111-8111-111111111111",
		DeviceID: "22222222-2222-4222-8222-222222222222", CanCollect: true}
	a := &API{store: admissionStore{}, log: slog.Default(), uploads: make(chan struct{}, 2),
		devices: map[uploadDevice]bool{{p.DatasetID, p.DeviceID}: true}}
	body := &countingBody{}
	request := httptest.NewRequest("POST", "/v2/sync/workout-bundles", body)
	request.Header.Set("Content-Type", "multipart/form-data; boundary=test")
	response := httptest.NewRecorder()
	err := a.importWorkoutBundle(response, request, p)
	if err == nil {
		t.Fatal("expected busy device admission error")
	}
	typed, ok := err.(*syncing.Error)
	if !ok || typed.Status != 429 || typed.Code != "device_upload_capacity_exceeded" {
		t.Fatalf("unexpected error: %v", err)
	}
	if body.reads != 0 {
		t.Fatalf("body was read before upload admission: %d", body.reads)
	}
}
