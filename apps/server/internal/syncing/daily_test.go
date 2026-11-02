package syncing

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func dailyWireFixture() DailyBatch {
	return DailyBatch{Version: 1, DatasetID: NewID(), DatasetEpoch: NewID(), CollectorEpoch: NewID(), StreamID: NewID(), BatchID: NewID(), ClientSeq: 1,
		SampleType: DailyHRV, BootstrapStartUs: 1700000000000000, ObservedAtUs: 1700001000000000, HasMore: false,
		Facts: []DailyFact{{UUID: NewID(), StartUs: 1700001000000000, EndUs: 1700001000000000, Quantity: &Quantity{Value: 42, Unit: "ms"}, Source: HealthKitSource{BundleID: "synthetic", Version: "1"}, Metadata: map[string]any{"HKAlgorithmVersion": 2}}}, DeletedUUIDs: []string{}}
}
func TestDailyStrictWireAndErrors(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		mutate     func(*DailyBatch)
	}{
		{"version", "unsupported_version", func(b *DailyBatch) { b.Version = 2 }},
		{"type", "unsupported_type", func(b *DailyBatch) { b.SampleType = "future" }},
		{"unit", "unsupported_unit", func(b *DailyBatch) { b.Facts[0].Quantity.Unit = "s" }},
		{"category", "invalid_category", func(b *DailyBatch) {
			b.SampleType = DailySleep
			b.Facts[0].Quantity = nil
			v := 99
			b.Facts[0].Category = &v
		}},
		{"metadata", "unsupported_metadata", func(b *DailyBatch) { b.Facts[0].Metadata["future"] = 1 }},
		{"string_number", "invalid_payload", func(b *DailyBatch) { b.Facts[0].Metadata["HKAlgorithmVersion"] = "2" }},
		{"timezone", "invalid_payload", func(b *DailyBatch) { b.Facts[0].Metadata["HKTimeZone"] = "Invalid/Timezone" }},
		{"source", "source_invalid", func(b *DailyBatch) { b.Facts[0].Source.BundleID = "" }},
		{"sync_pair", "invalid_payload", func(b *DailyBatch) { b.Facts[0].Metadata["HKMetadataKeySyncVersion"] = 2 }},
		{"limit", "batch_too_large", func(b *DailyBatch) {
			for i := 0; i < MaxDailyChanges; i++ {
				b.DeletedUUIDs = append(b.DeletedUUIDs, NewID())
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := dailyWireFixture()
			tc.mutate(&b)
			b.QueryFingerprint = DailyFingerprint(b.SampleType, b.BootstrapStartUs)
			raw, _ := json.Marshal(b)
			_, err := DecodeDailyBatch(raw)
			var e *Error
			if !errors.As(err, &e) || e.Code != tc.code {
				t.Fatalf("code=%v expected=%s", err, tc.code)
			}
		})
	}
	b := dailyWireFixture()
	b.QueryFingerprint = DailyFingerprint(b.SampleType, b.BootstrapStartUs)
	raw, _ := json.Marshal(b)
	if _, err := DecodeDailyBatch(raw); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		strings.Replace(string(raw), `"hasMore":false`, `"hasMore":false,"hasMore":true`, 1),
		strings.Replace(string(raw), `"hasMore":false`, `"hasMore":null`, 1),
		strings.Replace(string(raw), `"hasMore":false`, `"HasMore":false`, 1),
		strings.Replace(string(raw), `"startUs":1700001000000000,`, "", 1),
		strings.Replace(string(raw), `"source":{`, `"source":{"permissionGranted":true,`, 1),
	} {
		if _, err := DecodeDailyBatch([]byte(bad)); err == nil {
			t.Fatal("invalid shape accepted")
		}
	}
}
