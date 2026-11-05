package syncing

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
)

func vo2Fixture() VO2Batch {
	var b VO2Batch
	raw, err := os.ReadFile("../../../../contracts/fixtures/vo2max-v1.json")
	if err != nil {
		panic(err)
	}
	if err = json.Unmarshal(raw, &b); err != nil {
		panic(err)
	}
	return b
}
func TestVO2NativeCarrierSharedGolden(t *testing.T) {
	raw, err := os.ReadFile("../../../../contracts/fixtures/vo2max-native-metadata-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		MetadataRegistryVersion int  `json:"metadataRegistryVersion"`
		SyntheticOnly           bool `json:"syntheticOnly"`
		Cases                   []struct {
			Name              string                 `json:"name"`
			CanonicalMetadata map[string]VO2Metadata `json:"canonicalMetadata"`
		} `json:"cases"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.MetadataRegistryVersion != 1 || !fixture.SyntheticOnly || len(fixture.Cases) != 6 {
		t.Fatal("golden contract mismatch")
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			b := vo2Fixture()
			b.Facts[0].Metadata = tc.CanonicalMetadata
			body, err := json.Marshal(b)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeVO2Batch(body)
			if err != nil {
				t.Fatal(err)
			}
			want, _ := json.Marshal(tc.CanonicalMetadata)
			got, _ := json.Marshal(decoded.Facts[0].Metadata)
			if !bytes.Equal(want, got) {
				t.Fatal("typed metadata changed in strict wire decoder")
			}
		})
	}
}

func TestVO2StrictContract(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		mutate     func(*VO2Batch)
	}{
		{"version", "unsupported_version", func(b *VO2Batch) { b.Version = 2 }},
		{"registry", "unsupported_version", func(b *VO2Batch) { b.MetadataRegistryVersion = 2 }},
		{"unit", "unsupported_unit", func(b *VO2Batch) { b.Facts[0].Quantity.Unit = "L/min" }},
		{"zero", "invalid_value", func(b *VO2Batch) { b.Facts[0].Quantity.Value = 0 }},
		{"negative", "invalid_value", func(b *VO2Batch) { b.Facts[0].Quantity.Value = -1 }},
		{"reverse", "invalid_payload", func(b *VO2Batch) { b.Facts[0].EndUs = b.Facts[0].StartUs - 1 }},
		{"source", "source_invalid", func(b *VO2Batch) { b.Facts[0].Provenance.SourceRevision.Source.BundleIdentifier = "" }},
		{"unknown", "unsupported_metadata", func(b *VO2Batch) {
			b.Facts[0].Metadata["future"] = VO2Metadata{Type: "number", Value: json.RawMessage(`1.25`)}
		}},
		{"reserved", "unsupported_metadata", func(b *VO2Batch) {
			b.Facts[0].Metadata[VO2Reserved] = VO2Metadata{Type: "string", Value: json.RawMessage(`"x"`)}
		}},
		{"tag", "unsupported_metadata_type", func(b *VO2Batch) {
			b.Facts[0].Metadata["HKVO2MaxTestType"] = VO2Metadata{Type: "string", Value: json.RawMessage(`"2"`)}
		}},
		{"test0", "unsupported_test_type", func(b *VO2Batch) {
			b.Facts[0].Metadata["HKVO2MaxTestType"] = VO2Metadata{Type: "int64", Value: json.RawMessage(`0`)}
		}},
		{"test5", "unsupported_test_type", func(b *VO2Batch) {
			b.Facts[0].Metadata["HKVO2MaxTestType"] = VO2Metadata{Type: "int64", Value: json.RawMessage(`5`)}
		}},
		{"limit129", "batch_too_large", func(b *VO2Batch) {
			for i := 0; i < 128; i++ {
				b.DeletedUUIDs = append(b.DeletedUUIDs, NewID())
			}
		}},
		{"1024", "batch_too_large", func(b *VO2Batch) {
			for i := 0; i < 1024; i++ {
				b.DeletedUUIDs = append(b.DeletedUUIDs, NewID())
			}
		}},
		{"window", "invalid_payload", func(b *VO2Batch) { b.FlowCreatedAtUs++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := vo2Fixture()
			tc.mutate(&b)
			raw, _ := json.Marshal(b)
			_, err := DecodeVO2Batch(raw)
			var e *Error
			if !errors.As(err, &e) || e.Code != tc.code {
				t.Fatalf("error=%v want=%s", err, tc.code)
			}
		})
	}
	raw, _ := json.Marshal(vo2Fixture())
	for index, bad := range []string{
		strings.Replace(string(raw), `"hasMore":false`, `"hasMore":false,"hasMore":true`, 1),
		strings.Replace(string(raw), `"hasMore":false`, `"HasMore":false`, 1),
		strings.Replace(string(raw), `"hasMore":false`, `"hasMore":null`, 1),
		strings.Replace(string(raw), `,"device":null`, ``, 1),
		strings.Replace(string(raw), `"version":null,`, ``, 1),
		strings.Replace(string(raw), `"sourceRevision":{`, `"sourceRevision":{"future":true,`, 1),
		strings.Replace(string(raw), `"quantity":{`, `"quantity":{"future":0,`, 1),
		strings.Replace(string(raw), `"value":2`, `"value":2,"epochUs":1`, 1),
	} {
		if _, err := DecodeVO2Batch([]byte(bad)); err == nil {
			t.Fatalf("invalid shape accepted: %d", index)
		}
	}
	if _, err := DecodeVO2Batch(append(raw, bytes.Repeat([]byte(" "), MaxVO2Bytes-len(raw))...)); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeVO2Batch(append(raw, bytes.Repeat([]byte(" "), MaxVO2Bytes+1-len(raw))...)); err == nil {
		t.Fatal("byte limit+1")
	}
	for _, v := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		b := vo2Fixture()
		b.Facts[0].Quantity.Value = v
		if b.Validate() == nil {
			t.Fatal("nonfinite")
		}
	}
	b := vo2Fixture()
	b.Facts[0].Quantity.Value = 1000
	if b.Validate() != nil {
		t.Fatal("provider range became hard limit")
	}
}
func TestVO2MetadataPrecisionAndOrigin(t *testing.T) {
	for _, integer := range []string{"9007199254740993", "-9223372036854775808", "9223372036854775807"} {
		b := vo2Fixture()
		b.Facts[0].Metadata["HKAlgorithmVersion"] = VO2Metadata{Type: "int64", Value: json.RawMessage(integer)}
		raw, _ := json.Marshal(b)
		decoded, err := DecodeVO2Batch(raw)
		if err != nil {
			t.Fatal(err)
		}
		event, _ := decoded.Events()[0].Canonical()
		if !bytes.Contains(event, []byte(integer)) {
			t.Fatal("integer rounded")
		}
	}
	for _, v := range []string{`1e0`, `1.0`, `9223372036854775808`, `true`, `{}`, `[]`, `null`} {
		m := VO2Metadata{Type: "int64", Value: json.RawMessage(v)}
		if m.Validate() == nil {
			t.Fatal("invalid int", v)
		}
	}
	for _, v := range []string{`false`, `true`} {
		if (VO2Metadata{Type: "boolean", Value: json.RawMessage(v)}).Validate() != nil {
			t.Fatal("bool")
		}
	}
	if (VO2Metadata{Type: "number", Value: json.RawMessage(`1.2345678901234567`)}).Validate() != nil {
		t.Fatal("finite union")
	}
	for test := int64(1); test <= 4; test++ {
		f := vo2Fixture().Facts[0]
		raw, _ := json.Marshal(test)
		f.Metadata["HKVO2MaxTestType"] = VO2Metadata{Type: "int64", Value: raw}
		if validateVO2Metadata(f.Metadata) != nil {
			t.Fatal("test type", test)
		}
		origin, _ := f.Origin()
		if test == 1 && origin != "unclassified" || test > 1 && origin != "third_party_estimate" {
			t.Fatal(origin)
		}
	}
	f := vo2Fixture().Facts[0]
	f.Metadata["HKWasUserEntered"] = VO2Metadata{Type: "boolean", Value: json.RawMessage(`true`)}
	if origin, _ := f.Origin(); origin != "user_entered" {
		t.Fatal(origin)
	}
	f = vo2Fixture().Facts[0]
	f.Provenance.SourceRevision.Source.BundleIdentifier = "com.apple.health"
	watch, apple, model := "Watch9,1", "Apple Inc.", "Apple Watch"
	f.Provenance.SourceRevision.ProductType = &watch
	if origin, _ := f.Origin(); origin != "unclassified" {
		t.Fatal("bundle alone")
	}
	f.Provenance.Device = &VO2Device{Manufacturer: &apple, Model: &model}
	if origin, _ := f.Origin(); origin != "apple_watch_estimate" {
		t.Fatal(origin)
	}
	for _, bundle := range []string{"org.-invalid.fixture", "org.invalid-.fixture", "org..fixture"} {
		invalid := vo2Fixture().Facts[0]
		invalid.Provenance.SourceRevision.Source.BundleIdentifier = bundle
		if origin, _ := invalid.Origin(); origin != "unclassified" {
			t.Fatal("invalid reverse-domain source classified")
		}
	}
	f.Provenance.SourceRevision.Source.BundleIdentifier = "com.apple.health." + strings.ToUpper(NewID())
	if origin, _ := f.Origin(); origin != "apple_watch_estimate" {
		t.Fatal("legal source UUID case")
	}
	model = "iPhone"
	if origin, _ := f.Origin(); origin != "unclassified" {
		t.Fatal("conflicting device")
	}
}
func TestVO2CanonicalAndGenericSpoof(t *testing.T) {
	b := vo2Fixture()
	raw, _ := json.Marshal(b)
	if _, err := DecodeVO2Batch(raw); err != nil {
		t.Fatal(err)
	}
	expected, err := os.ReadFile("../../../../contracts/fixtures/vo2max-provenance.canonical.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(expected), VO2Canonical(b.Facts[0].Provenance)) {
		t.Fatal("canonical provenance")
	}
	e := b.Events()[0]
	if e.Validate(VO2Type) == nil {
		t.Fatal("generic reserved spoof")
	}
	old := dailyWireFixture()
	old.QueryFingerprint = DailyFingerprint(old.SampleType, old.BootstrapStartUs)
	event := old.Events()[0]
	before, hash := event.Canonical()
	after, _ := json.Marshal(event) // Frozen Daily v1 encoding before B2.
	expectedHash := sha256.Sum256(after)
	if !bytes.Equal(before, after) || !bytes.Equal(hash, expectedHash[:]) {
		t.Fatal("daily changed")
	}
}
func BenchmarkVO2Decode128(b *testing.B) {
	batch := vo2Fixture()
	for len(batch.Facts) < 128 {
		f := batch.Facts[0]
		f.UUID = NewID()
		batch.Facts = append(batch.Facts, f)
	}
	raw, _ := json.Marshal(batch)
	b.ReportAllocs()
	b.SetBytes(int64(len(raw)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := DecodeVO2Batch(raw); err != nil {
			b.Fatal(err)
		}
	}
}

func TestVO2DescriptorAndMetadataLimitsDoNotTruncate(t *testing.T) {
	for _, n := range []int{255, 256} {
		b := vo2Fixture()
		b.Facts[0].Provenance.SourceRevision.Source.Name = strings.Repeat("x", n)
		if err := b.Validate(); (err == nil) != (n == 255) {
			t.Fatal("descriptor byte bound")
		}
	}
	for _, n := range []int{4096, 4097} {
		raw, _ := json.Marshal(strings.Repeat("x", n))
		m := VO2Metadata{Type: "string", Value: raw}
		if err := m.Validate(); (err == nil) != (n == 4096) {
			t.Fatal("union string bound")
		}
	}
	b := vo2Fixture()
	for i := 0; i < 65; i++ {
		b.Facts[0].Metadata[fmt.Sprintf("future%d", i)] = VO2Metadata{Type: "boolean", Value: json.RawMessage(`true`)}
	}
	if b.Validate() == nil {
		t.Fatal("metadata count+1")
	}
	b = vo2Fixture()
	b.Facts = []VO2Fact{}
	for i := 0; i < 128; i++ {
		b.DeletedUUIDs = append(b.DeletedUUIDs, NewID())
	}
	if b.Validate() != nil {
		t.Fatal("128 deletions rejected")
	}
	b.DeletedUUIDs = append(b.DeletedUUIDs, NewID())
	if b.Validate() == nil {
		t.Fatal("129 deletions truncated")
	}
}
