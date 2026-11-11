package httpapi

import (
	"sync"
	"testing"

	"rai/server/internal/syncing"
)

func TestUploadSlotsPerDeviceAndProcessRelease(t *testing.T) {
	a := &API{uploads: make(chan struct{}, ProcessUploadSlots), devices: make(map[uploadSlot]int)}
	p := syncing.Principal{DatasetID: "dataset-a", DeviceID: "device-a"}
	first, err := a.acquireUpload(p, workoutUpload)
	if err != nil {
		t.Fatal(err)
	}
	second, err := a.acquireUpload(p, workoutUpload)
	if err != nil {
		t.Fatal("history could not acquire second slot", err)
	}
	if _, err = a.acquireUpload(p, workoutUpload); err == nil || err.Error() != "device_upload_capacity_exceeded" {
		t.Fatal("device Workout ceiling bypassed", err)
	}
	health, err := a.acquireUpload(p, healthUpload)
	if err != nil {
		t.Fatal("daily health waited for Workout history", err)
	}
	if _, err = a.acquireUpload(p, healthUpload); err == nil || err.Error() != "device_upload_capacity_exceeded" {
		t.Fatal("same device acquired two health uploads", err)
	}
	other := syncing.Principal{DatasetID: "dataset-b", DeviceID: "device-a"}
	if _, err = a.acquireUpload(other, workoutUpload); err == nil || err.Error() != "upload_capacity_exceeded" {
		t.Fatal("process cap bypassed", err)
	}
	first()
	// Releasing one worker must not clear the other worker's device count.
	third, err := a.acquireUpload(p, workoutUpload)
	if err != nil {
		t.Fatal("released slot not reusable", err)
	}
	if _, err = a.acquireUpload(p, workoutUpload); err == nil || err.Error() != "device_upload_capacity_exceeded" {
		t.Fatal("release lost the other worker", err)
	}
	second()
	third()
	health()
	if len(a.devices) != 0 || len(a.uploads) != 0 {
		t.Fatal("active upload state leaked")
	}
}

func TestConcurrentUploadAdmissionNeverExceedsDeviceBudget(t *testing.T) {
	a := &API{uploads: make(chan struct{}, 32), devices: make(map[uploadSlot]int)}
	p := syncing.Principal{DatasetID: "dataset", DeviceID: "device"}
	var attempts sync.WaitGroup
	releases := make(chan func(), 32)
	for range 32 {
		attempts.Go(func() {
			if release, err := a.acquireUpload(p, workoutUpload); err == nil {
				releases <- release
			}
		})
	}
	attempts.Wait()
	close(releases)
	if len(releases) != DeviceWorkoutUploadSlots {
		t.Fatalf("admitted %d", len(releases))
	}
	for release := range releases {
		release()
	}
	if len(a.devices) != 0 || len(a.uploads) != 0 {
		t.Fatal("active upload state leaked")
	}
}
