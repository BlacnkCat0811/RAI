//go:build integration

package integration

// R2-F27: the strict JSON boundary must keep every wire rule after the
// reflection cache and allocation work in round 2. Each case asserts the exact
// rejection code, field path and reason the production decoder returns, so a
// relaxation (or an accidentally accepted payload) fails the test.
//
// The matrix runs inside the isolated PostgreSQL harness because the same
// package drives the round-2 gates; the decoder itself touches no database.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"rai/server/internal/syncing"
)

const (
	strictDataset  = "0f9a1d4e-1c2b-4a3d-8e5f-0a1b2c3d4e5f"
	strictEpoch    = "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"
	strictCollect  = "2b3c4d5e-6f7a-4b8c-9d0e-1f2a3b4c5d6e"
	strictWorkout  = "3c4d5e6f-7a8b-4c9d-8e0f-2a3b4c5d6e7f"
	strictSampleID = "4d5e6f7a-8b9c-4d0e-9f1a-3b4c5d6e7f80"
)

// strictBaseJSON renders a minimal, fully valid bundle document. Cases mutate
// the text so the decoder — not a Go literal — is what rejects the payload.
func strictBaseJSON(t *testing.T) string {
	t.Helper()
	start, end := int64(1_790_000_000_000_000), int64(1_790_000_060_000_000)
	duration := end - start
	bundle := syncing.WorkoutBundle{
		ProtocolVersion: 3,
		DatasetID:       strictDataset,
		DatasetEpoch:    strictEpoch,
		CollectorEpoch:  strictCollect,
		WorkoutID:       strictWorkout,
		Workout: syncing.Event{
			Op: "upsert", Kind: "workout", Type: syncing.WorkoutType, UUID: strictWorkout,
			StartUs: &start, EndUs: &end,
			Source:  &syncing.Source{BundleID: "synthetic.test"},
			Workout: &syncing.Workout{ActivityType: int64Ptr(37), DurationUs: &duration},
		},
		Samples: []syncing.Event{{
			Op: "upsert", Kind: "sample", Type: kind, UUID: strictSampleID,
			StartUs: &start, EndUs: &end,
			Quantity: &syncing.Quantity{Value: 61, Unit: "count/min"},
			Source:   &syncing.Source{BundleID: "synthetic.test"},
			Metadata: map[string]any{"HKTimeZone": "UTC"},
		}},
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func int64Ptr(value int64) *int64 { return &value }

// strictReplace applies one literal edit and fails when the anchor is missing,
// so a fixture drift cannot silently turn a case into a no-op.
func strictReplace(t *testing.T, document, old, replacement string) string {
	t.Helper()
	if !strings.Contains(document, old) {
		t.Fatalf("anchor %q not present in the fixture", old)
	}
	return strings.Replace(document, old, replacement, 1)
}

func TestStrictWorkoutBundleJSONRejectionMatrix(t *testing.T) {
	base := strictBaseJSON(t)
	sampleObject := `{"op":"upsert","kind":"sample","type":"` + kind + `","uuid":"` + strictSampleID +
		`","startUs":1790000000000000,"endUs":1790000060000000,"quantity":{"value":61,"unit":"count/min"},"source":{"bundleID":"synthetic.test"}}`

	deep := ""
	for i := 0; i < 70; i++ {
		deep += `{"a":`
	}
	deep += "1"
	for i := 0; i < 70; i++ {
		deep += "}"
	}

	wide := "{"
	for i := 0; i < 300; i++ {
		if i > 0 {
			wide += ","
		}
		wide += fmt.Sprintf("%q:1", fmt.Sprintf("k%d", i))
	}
	wide += "}"

	tooManySamples := strings.TrimSuffix(strings.Repeat(sampleObject+",", syncing.MaxWorkoutBundleEvents+1), ",")
	tooManyTypes := make([]string, 0, len(syncing.Units)+1)
	for name := range syncing.Units {
		tooManyTypes = append(tooManyTypes, fmt.Sprintf("%q", name))
	}
	tooManyTypes = append(tooManyTypes, `"HKQuantityTypeIdentifierNotASupportedType"`)
	tooManyRouteDeletions := make([]string, 0, syncing.MaxBundleRoutes+1)
	for i := 0; i <= syncing.MaxBundleRoutes; i++ {
		tooManyRouteDeletions = append(tooManyRouteDeletions, fmt.Sprintf(`"4d5e6f7a-8b9c-4d0e-9f1a-3b4c5d6e7f%02x"`, i))
	}

	cases := []struct {
		name     string
		document string
		field    string
		reason   string
	}{
		{
			name:     "missing_required_field",
			document: strictReplace(t, base, `"protocolVersion":3,`, ``),
			field:    "protocolVersion",
			reason:   "required",
		},
		{
			name:     "missing_sample_source",
			document: strictReplace(t, base, `"source":{"bundleID":"synthetic.test"},"metadata"`, `"metadata"`),
			field:    "samples[0].source",
			reason:   "required",
		},
		{
			name:     "null_not_allowed",
			document: strictReplace(t, base, `"datasetID":"`+strictDataset+`"`, `"datasetID":null`),
			field:    "datasetID",
			reason:   "null_not_allowed",
		},
		{
			name:     "duplicate_field",
			document: strictReplace(t, base, `"protocolVersion":3`, `"protocolVersion":3,"protocolVersion":3`),
			field:    "protocolVersion",
			reason:   "duplicate_field",
		},
		{
			name:     "unknown_field",
			document: strictReplace(t, base, `"protocolVersion":3`, `"protocolVersion":3,"unexpected":1`),
			field:    "unexpected",
			reason:   "unknown_field",
		},
		{
			name:     "wrong_case_field",
			document: strictReplace(t, base, `"protocolVersion":3`, `"ProtocolVersion":3`),
			field:    "ProtocolVersion",
			reason:   "unknown_field",
		},
		{
			name:     "string_where_integer",
			document: strictReplace(t, base, `"protocolVersion":3`, `"protocolVersion":"3"`),
			field:    "protocolVersion",
			reason:   "invalid_type",
		},
		{
			name:     "integer_overflow",
			document: strictReplace(t, base, `"startUs":1790000000000000,"endUs":1790000060000000,"quantity"`, `"startUs":9223372036854775808,"endUs":1790000060000000,"quantity"`),
			field:    "samples[0].startUs",
			reason:   "invalid_type",
		},
		{
			name:     "object_where_scalar",
			document: strictReplace(t, base, `"protocolVersion":3`, `"protocolVersion":{"v":3}`),
			field:    "protocolVersion",
			reason:   "invalid_type",
		},
		{
			name:     "nesting_limit",
			document: strictReplace(t, base, `"metadata":{"HKTimeZone":"UTC"}`, `"metadata":`+deep),
			field:    "*", // the path is the full 65-level chain
			reason:   "nesting_limit",
		},
		{
			name:     "object_field_limit",
			document: strictReplace(t, base, `"metadata":{"HKTimeZone":"UTC"}`, `"metadata":`+wide),
			field:    "samples[0].metadata",
			reason:   "object_field_limit",
		},
		{
			name:     "sample_count_limit",
			document: base[:strings.Index(base, `"samples":[`)] + `"samples":[` + tooManySamples + `]}`,
			field:    "samples",
			reason:   "array_count_limit",
		},
		{
			name:     "complete_sample_types_limit",
			document: strictReplace(t, base, `"protocolVersion":3,`, `"protocolVersion":3,"completeSampleTypes":[`+strings.Join(tooManyTypes, ",")+`],`),
			field:    "completeSampleTypes",
			reason:   "array_count_limit",
		},
		{
			name:     "route_deletions_limit",
			document: strictReplace(t, base, `"protocolVersion":3,`, `"protocolVersion":3,"routeDeletions":[`+strings.Join(tooManyRouteDeletions, ",")+`],`),
			field:    "routeDeletions",
			reason:   "array_count_limit",
		},
		{
			name:     "truncated_document",
			document: base[:len(base)/2],
			field:    "*",
			reason:   "invalid_json_or_schema",
		},
		{
			name:     "trailing_document",
			document: base + `{"protocolVersion":3}`,
			field:    "$",
			reason:   "invalid_json_or_schema",
		},
		{
			name:     "empty_document",
			document: "",
			field:    "$",
			reason:   "invalid_json_or_schema",
		},
		{
			name:     "nonfinite_number",
			document: strictReplace(t, base, `"quantity":{"value":61,`, `"quantity":{"value":1e400,`),
			field:    "*",
			reason:   "invalid_json_or_schema",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var bundle syncing.WorkoutBundle
			err := syncing.DecodeWorkoutBundleJSON([]byte(testCase.document), &bundle)
			if err == nil {
				t.Fatalf("payload was accepted")
			}
			var failure *syncing.Error
			if !errors.As(err, &failure) {
				t.Fatalf("untyped failure: %v", err)
			}
			if failure.Status != 422 || failure.Code != "unsupported_payload" {
				t.Fatalf("status=%d code=%s", failure.Status, failure.Code)
			}
			if testCase.field != "*" && failure.Field != testCase.field {
				t.Fatalf("field=%q want %q", failure.Field, testCase.field)
			}
			if failure.Reason != testCase.reason {
				t.Fatalf("reason=%q want %q", failure.Reason, testCase.reason)
			}
		})
	}
}

// TestStrictWorkoutBundleSemanticRules keeps the semantic half of the boundary
// in the same matrix: unit, source policy and protocol rules still fail with
// their documented reasons, and the Store is still the only place they run.
func TestStrictWorkoutBundleSemanticRules(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*syncing.WorkoutBundle)
		field  string
		reason string
	}{
		{"unsupported_unit", func(b *syncing.WorkoutBundle) { b.Samples[0].Quantity.Unit = "kg" }, "samples[0].quantity.unit", "unsupported_unit"},
		{"unsupported_protocol", func(b *syncing.WorkoutBundle) { b.ProtocolVersion = 2 }, "protocolVersion", "unsupported_protocol"},
		{"invalid_sample_uuid", func(b *syncing.WorkoutBundle) { b.Samples[0].UUID = "not-a-uuid" }, "samples[0].uuid", "invalid_uuid"},
		{"invalid_time_range", func(b *syncing.WorkoutBundle) {
			start := *b.Samples[0].StartUs - 1
			b.Samples[0].EndUs = &start
		}, "samples[0].endUs", "invalid_time_range"},
		{"invalid_dataset_id", func(b *syncing.WorkoutBundle) { b.DatasetID = "not-a-uuid" }, "datasetID", "invalid_uuid"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var bundle syncing.WorkoutBundle
			if err := json.Unmarshal([]byte(strictBaseJSON(t)), &bundle); err != nil {
				t.Fatal(err)
			}
			testCase.mutate(&bundle)
			raw, err := json.Marshal(bundle)
			if err != nil {
				t.Fatal(err)
			}
			var decoded syncing.WorkoutBundle
			if err = syncing.DecodeWorkoutBundleJSON(raw, &decoded); err != nil {
				t.Fatalf("wire decode rejected a structurally valid document: %v", err)
			}
			err = syncing.ValidateWorkoutBundle(decoded)
			if err == nil {
				t.Fatal("semantic rule was not enforced")
			}
			var failure *syncing.Error
			if !errors.As(err, &failure) {
				t.Fatalf("untyped failure: %v", err)
			}
			if failure.Status != 422 || failure.Code != "unsupported_payload" {
				t.Fatalf("status=%d code=%s", failure.Status, failure.Code)
			}
			if failure.Field != testCase.field || failure.Reason != testCase.reason {
				t.Fatalf("field=%q reason=%q want %q/%q", failure.Field, failure.Reason, testCase.field, testCase.reason)
			}
		})
	}
}
