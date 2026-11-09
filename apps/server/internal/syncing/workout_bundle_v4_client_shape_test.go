package syncing

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestV4ClientShapeDecodes locks the exact envelope the Swift raw encoder
// emits: one Workout object, inline per-sample sources and metadata, series
// triples keyed by their parent sample and eight-element route tuples. There is
// no sources table, uuids column, observation, deletion list or registry
// declaration; the server must decode this shape without the v3 sidecars.
func TestV4ClientShapeDecodes(t *testing.T) {
	workoutID, sampleUUID, routeID := NewID(), NewID(), NewID()
	start := int64(1_800_000_000_000_000)
	end := start + 60_000_000
	source := `{"bundleID":"com.apple.health","name":"Apple Watch","productType":"Watch7,1","version":"11.0"}`
	raw := fmt.Sprintf(`{
		"collectorEpoch":%q,"datasetEpoch":%q,"datasetID":%q,
		"endUs":%d,
		"metadata":{"HKTimeZone":"Asia/Shanghai"},
		"protocolVersion":4,
		"routes":[{"points":[[%d,31.2,121.4,null,null,null,null,null]],"routeID":%q,"source":%s}],
		"series":[{"points":[[%d,%d,121]],"sampleUUID":%q,"type":"HKQuantityTypeIdentifierHeartRate","unit":"count/min"}],
		"source":%s,
		"startUs":%d,
		"streams":[{"error":null,"samples":[{"endUs":%d,"metadata":{"HKAverageMETs":10.5},"source":%s,"startUs":%d,"uuid":%q,"value":120}],"status":"complete","type":"HKQuantityTypeIdentifierHeartRate","unit":"count/min"}],
		"workout":{"activityType":37,"durationUs":60000000},
		"workoutID":%q
	}`, NewID(), NewID(), NewID(), end, start, routeID, source, start, end, sampleUUID, source, start, end, source, start, sampleUUID, workoutID)

	for _, legacy := range []string{`"sources"`, `"uuids"`, `"observation"`, `"deletions"`, `"routeDeletions"`, `"metadataRegistryVersion"`} {
		if strings.Contains(raw, legacy) {
			t.Fatalf("fixture still carries the v3 sidecar %s", legacy)
		}
	}
	bundle, err := DecodeWorkoutBundleV4JSON([]byte(raw))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if bundle.ProtocolVersion != 4 || bundle.WorkoutID != workoutID || len(bundle.Samples) != 1 ||
		len(bundle.Series) != 1 || len(bundle.Routes) != 1 {
		t.Fatalf("unexpected bundle: %+v", bundle)
	}
	if bundle.Samples[0].Quantity == nil || bundle.Samples[0].Quantity.Value != 120 ||
		bundle.Samples[0].Source == nil || bundle.Samples[0].Source.BundleID != "com.apple.health" {
		t.Fatalf("quantity: %+v", bundle.Samples[0])
	}
	if bundle.SourceTable == nil || bundle.SourceTable.Workout.ProductType != "Watch7,1" {
		t.Fatalf("source descriptor not projected: %+v", bundle.SourceTable)
	}
	if bundle.Observation != nil || len(bundle.CompleteSampleTypes) != 0 {
		t.Fatal("v4 must not invent an observation")
	}
	if err := ValidateWorkoutBundle(bundle); err != nil {
		t.Fatalf("validate: %v", err)
	}
	// The converted in-memory shape must marshal back as a legal v3 bundle too.
	if _, err := json.Marshal(bundle); err != nil {
		t.Fatal(err)
	}
}
