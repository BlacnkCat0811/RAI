package syncing

import (
	"encoding/json"
	"fmt"
	"testing"
)

// TestV4ClientShapeDecodes locks the exact envelope the Swift encoder emits:
// a workout object (not a v3 Event), a source table without device detail,
// columnar sample triples and an observation whose provenance arrays are empty.
func TestV4ClientShapeDecodes(t *testing.T) {
	workoutID := NewID()
	sampleUUID := NewID()
	start := int64(1_800_000_000_000_000)
	end := start + 60_000_000
	raw := fmt.Sprintf(`{
		"protocolVersion":4,
		"metadataRegistryVersion":2,
		"datasetID":%q,"datasetEpoch":%q,"collectorEpoch":%q,"workoutID":%q,
		"startUs":%d,"endUs":%d,
		"workout":{"activityType":37,"durationUs":60000000},
		"sources":[{"bundleID":"com.apple.health","name":"Apple Watch","productType":"Watch7,1"}],
		"streams":[{
			"type":"HKQuantityTypeIdentifierHeartRate","kind":"quantity","unit":"count/min",
			"samples":[[%d,%d,120]],"uuids":[%q],"source":0,"src":[0]
		}],
		"series":[],"routes":[],"deletions":[],
		"observation":{
			"version":1,"observedAtUs":%d,"metadata":{},
			"provenance":{"workout":{"bundleID":"com.apple.health","productType":"Watch7,1"},"samples":[],"routes":[]},
			"coverage":{
				"workout":{"state":"complete","sampleCount":1,"queryCompleted":true,"localPersisted":true},
				"HKQuantityTypeIdentifierHeartRate":{"state":"complete","sampleCount":1,"queryCompleted":true,"localPersisted":true}
			}
		}
	}`, NewID(), NewID(), NewID(), workoutID, start, end, start, end, sampleUUID, start)
	bundle, err := DecodeWorkoutBundleV4JSON([]byte(raw))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if bundle.ProtocolVersion != 4 || bundle.WorkoutID != workoutID || len(bundle.Samples) != 1 {
		t.Fatalf("unexpected bundle: %+v", bundle)
	}
	if bundle.Samples[0].Quantity == nil || bundle.Samples[0].Quantity.Value != 120 {
		t.Fatalf("quantity: %+v", bundle.Samples[0])
	}
	if err := ValidateWorkoutBundle(bundle); err != nil {
		t.Fatalf("validate: %v", err)
	}
	// The converted in-memory shape must marshal back as a legal v3 bundle too.
	if _, err := json.Marshal(bundle); err != nil {
		t.Fatal(err)
	}
}
