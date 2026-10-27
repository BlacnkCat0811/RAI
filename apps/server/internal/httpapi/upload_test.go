package httpapi

import (
	"testing"

	"rai/server/internal/syncing"
)

func TestUploadSlotsPerDeviceAndProcessRelease(t *testing.T) {
	a := &API{uploads: make(chan struct{}, 2), devices: make(map[uploadDevice]bool)}
	p := syncing.Principal{DatasetID: "dataset-a", DeviceID: "device-a"}
	first, err := a.acquireUpload(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.acquireUpload(p); err == nil || err.Error() != "device_upload_capacity_exceeded" {
		t.Fatal("same device acquired two uploads", err)
	}
	p.DeviceID = "device-b"
	second, err := a.acquireUpload(p)
	if err != nil {
		t.Fatal(err)
	}
	p.DatasetID = "dataset-b"
	if _, err = a.acquireUpload(p); err == nil || err.Error() != "upload_capacity_exceeded" {
		t.Fatal("process upload cap bypassed", err)
	}
	first()
	third, err := a.acquireUpload(p)
	if err != nil {
		t.Fatal("released slot not reusable", err)
	}
	second()
	third()
	if len(a.devices) != 0 || len(a.uploads) != 0 {
		t.Fatal("active upload state leaked")
	}
}
