package syncing

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

const (
	VO2Contract               = "vo2max-facts/v1"
	VO2Type                   = "HKQuantityTypeIdentifierVO2Max"
	VO2ProvenanceSchema       = "rai.healthkit.provenance/2"
	VO2FactSchema             = "rai.healthkit.vo2max_fact/1"
	VO2Reserved               = "RAI:vo2maxFactV1"
	MaxVO2Changes             = 128
	MaxVO2Bytes               = 524288
	MaxVO2ContextBytes        = 65536
	VO2WindowUs         int64 = 90 * 86400000000
)

type VO2OS struct {
	Major int64 `json:"major"`
	Minor int64 `json:"minor"`
	Patch int64 `json:"patch"`
}
type VO2Source struct {
	BundleIdentifier string `json:"bundleIdentifier"`
	Name             string `json:"name"`
}
type VO2Revision struct {
	Source                 VO2Source `json:"source"`
	Version                *string   `json:"version"`
	ProductType            *string   `json:"productType"`
	OperatingSystemVersion VO2OS     `json:"operatingSystemVersion"`
}
type VO2Device struct {
	Name            *string `json:"name"`
	Manufacturer    *string `json:"manufacturer"`
	Model           *string `json:"model"`
	HardwareVersion *string `json:"hardwareVersion"`
	FirmwareVersion *string `json:"firmwareVersion"`
	SoftwareVersion *string `json:"softwareVersion"`
}
type VO2Provenance struct {
	SchemaVersion  string      `json:"schemaVersion"`
	SourceRevision VO2Revision `json:"sourceRevision"`
	Device         *VO2Device  `json:"device"`
}

// The payload remains raw until the tag is checked. In particular, Int64 never
// passes through float64. No interface{} decoding of HealthKit metadata.
type VO2Metadata struct {
	Type    string          `json:"type"`
	Value   json.RawMessage `json:"value,omitempty"`
	EpochUs *int64          `json:"epochUs,omitempty"`
	Unit    *string         `json:"unit,omitempty"`
}

func (m *VO2Metadata) UnmarshalJSON(raw []byte) error {
	type plain VO2Metadata
	var v plain
	if err := json.Unmarshal(raw, &v); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	keys := []string{"type", "value"}
	if v.Type == "date" {
		keys = []string{"type", "epochUs"}
	}
	if v.Type == "quantity" {
		keys = append(keys, "unit")
	}
	if len(fields) != len(keys) || vo2Required(raw, keys) != nil {
		return Fail(422, "unsupported_metadata_type")
	}
	*m = VO2Metadata(v)
	return nil
}
func (m VO2Metadata) MarshalJSON() ([]byte, error) {
	fields := map[string]any{"type": m.Type}
	if m.EpochUs != nil {
		fields["epochUs"] = *m.EpochUs
	}
	if m.Unit != nil {
		fields["unit"] = *m.Unit
	}
	if m.Value != nil {
		// Invalid values remain representable for negative wire tests; admission is
		// exclusively Validate. Valid numeric union payloads normalize spelling.
		if m.Type == "number" || m.Type == "quantity" {
			var v float64
			if json.Unmarshal(m.Value, &v) == nil {
				if v == 0 {
					v = 0
				}
				fields["value"] = v
			} else {
				fields["value"] = m.Value
			}
		} else {
			fields["value"] = m.Value
		}
	}
	return json.Marshal(fields)
}

func (m VO2Metadata) Integer() int64 { var v int64; _ = json.Unmarshal(m.Value, &v); return v }
func (m VO2Metadata) Boolean() bool  { var v bool; _ = json.Unmarshal(m.Value, &v); return v }
func (m VO2Metadata) Validate() error {
	bad := Fail(422, "unsupported_metadata_type")
	if m.Type == "date" {
		if m.EpochUs == nil || !vo2Time(*m.EpochUs) || m.Value != nil || m.Unit != nil {
			return bad
		}
		return nil
	}
	if m.EpochUs != nil || len(m.Value) == 0 || bytes.Equal(m.Value, []byte("null")) {
		return bad
	}
	if m.Type != "quantity" && m.Unit != nil {
		return bad
	}
	switch m.Type {
	case "string":
		var v string
		if json.Unmarshal(m.Value, &v) != nil || len(v) > 4096 {
			return bad
		}
	case "boolean":
		var v bool
		if json.Unmarshal(m.Value, &v) != nil {
			return bad
		}
	case "int64":
		var v int64
		if json.Unmarshal(m.Value, &v) != nil {
			return bad
		}
	case "number", "quantity":
		var v float64
		if json.Unmarshal(m.Value, &v) != nil || math.IsNaN(v) || math.IsInf(v, 0) {
			return bad
		}
		if m.Type == "quantity" && (m.Unit == nil || *m.Unit != "mL/kg/min") {
			return Fail(422, "unsupported_unit")
		}
	default:
		return bad
	}
	return nil
}
func validateVO2Metadata(m map[string]VO2Metadata) error {
	if m == nil || len(m) > 64 {
		return Fail(422, "unsupported_metadata")
	}
	registry := map[string]string{"HKVO2MaxTestType": "int64", "HKMetadataKeyAppleDeviceCalibrated": "boolean", "HKWasUserEntered": "boolean", "HKMetadataKeySyncIdentifier": "string", "HKMetadataKeySyncVersion": "int64", "HKTimeZone": "string", "HKAlgorithmVersion": "int64", "HKDateOfEarliestDataUsedForEstimate": "date", "HKVO2MaxValue": "quantity"}
	for key, v := range m {
		tag, ok := registry[key]
		if !ok {
			return Fail(422, "unsupported_metadata")
		}
		if tag != v.Type {
			return Fail(422, "unsupported_metadata_type")
		}
		if err := v.Validate(); err != nil {
			return err
		}
		if key == "HKVO2MaxTestType" && (v.Integer() < 1 || v.Integer() > 4) {
			return Fail(422, "unsupported_test_type")
		}
		if key == "HKMetadataKeySyncIdentifier" || key == "HKTimeZone" {
			var text string
			_ = json.Unmarshal(v.Value, &text)
			limit := 255
			if key == "HKTimeZone" {
				limit = 128
				if _, err := time.LoadLocation(text); err != nil {
					return Fail(422, "unsupported_metadata")
				}
			}
			if text == "" || len(text) > limit {
				return Fail(422, "unsupported_metadata")
			}
		}
	}
	_, id := m["HKMetadataKeySyncIdentifier"]
	_, version := m["HKMetadataKeySyncVersion"]
	if id != version {
		return Fail(422, "unsupported_metadata")
	}
	return nil
}

type VO2Fact struct {
	UUID       string                 `json:"uuid"`
	StartUs    int64                  `json:"startUs"`
	EndUs      int64                  `json:"endUs"`
	Quantity   Quantity               `json:"quantity"`
	Provenance VO2Provenance          `json:"provenance"`
	Metadata   map[string]VO2Metadata `json:"metadata"`
}
type VO2Batch struct {
	Contract                string    `json:"contract"`
	Version                 int       `json:"version"`
	MetadataRegistryVersion int       `json:"metadataRegistryVersion"`
	DatasetID               string    `json:"datasetID"`
	DatasetEpoch            string    `json:"datasetEpoch"`
	CollectorEpoch          string    `json:"collectorEpoch"`
	StreamID                string    `json:"streamID"`
	BatchID                 string    `json:"batchID"`
	ClientSeq               int64     `json:"clientSeq"`
	PreviousBatchID         *string   `json:"previousBatchID"`
	SampleType              string    `json:"sampleType"`
	FlowCreatedAtUs         int64     `json:"flowCreatedAtUs"`
	BootstrapStartUs        int64     `json:"bootstrapStartUs"`
	QueryFingerprint        string    `json:"queryFingerprint"`
	QueryCompletedAtUs      int64     `json:"queryCompletedAtUs"`
	HasMore                 bool      `json:"hasMore"`
	Facts                   []VO2Fact `json:"facts"`
	DeletedUUIDs            []string  `json:"deletedUUIDs"`
}

func VO2Fingerprint(start int64) string {
	return "sha256:" + DailyDigest([]byte(fmt.Sprintf("vo2max-facts/v1|%s|%d|overlap-v1", VO2Type, start)))
}
func vo2Time(v int64) bool { return v >= -62135596800000000 && v <= 253402300799999999 }

// Enforce presence independently of Go zero values, including explicit null
// provenance descriptors. Called recursively only for this new contract.
func vo2Required(raw []byte, keys []string, nullable ...string) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return Fail(422, "invalid_payload")
	}
	for _, key := range keys {
		v, ok := fields[key]
		if !ok {
			return Fail(422, "invalid_payload")
		}
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			allowed := false
			for _, n := range nullable {
				if n == key {
					allowed = true
				}
			}
			if !allowed {
				return Fail(422, "invalid_payload")
			}
		}
	}
	return nil
}
func DecodeVO2Batch(raw []byte) (VO2Batch, error) {
	var b VO2Batch
	if len(raw) > MaxVO2Bytes {
		return b, Fail(413, "batch_too_large")
	}
	if err := DecodeStrictJSON(raw, &b); err != nil {
		return b, Fail(422, "invalid_payload")
	}
	if err := vo2Required(raw, strings.Fields("contract version metadataRegistryVersion datasetID datasetEpoch collectorEpoch streamID batchID clientSeq previousBatchID sampleType flowCreatedAtUs bootstrapStartUs queryFingerprint queryCompletedAtUs hasMore facts deletedUUIDs"), "previousBatchID"); err != nil {
		return b, err
	}
	var top map[string]json.RawMessage
	_ = json.Unmarshal(raw, &top)
	var facts []map[string]json.RawMessage
	_ = json.Unmarshal(top["facts"], &facts)
	for _, f := range facts {
		fraw, _ := json.Marshal(f)
		if err := vo2Required(fraw, strings.Fields("uuid startUs endUs quantity provenance metadata")); err != nil {
			return b, err
		}
		if err := vo2Required(f["quantity"], []string{"value", "unit"}); err != nil {
			return b, err
		}
		if err := vo2Required(f["provenance"], []string{"schemaVersion", "sourceRevision", "device"}, "device"); err != nil {
			return b, Fail(422, "source_invalid")
		}
		var p map[string]json.RawMessage
		_ = json.Unmarshal(f["provenance"], &p)
		if err := vo2Required(p["sourceRevision"], []string{"source", "version", "productType", "operatingSystemVersion"}, "version", "productType"); err != nil {
			return b, Fail(422, "source_invalid")
		}
		var r map[string]json.RawMessage
		_ = json.Unmarshal(p["sourceRevision"], &r)
		if vo2Required(r["source"], []string{"bundleIdentifier", "name"}) != nil || vo2Required(r["operatingSystemVersion"], []string{"major", "minor", "patch"}) != nil {
			return b, Fail(422, "source_invalid")
		}
		if !bytes.Equal(p["device"], []byte("null")) {
			keys := strings.Fields("name manufacturer model hardwareVersion firmwareVersion softwareVersion")
			if vo2Required(p["device"], keys, keys...) != nil {
				return b, Fail(422, "source_invalid")
			}
		}
	}
	return b, b.Validate()
}
func (b VO2Batch) Validate() error {
	if b.Contract != VO2Contract || b.Version != 1 || b.MetadataRegistryVersion != 1 {
		return Fail(422, "unsupported_version")
	}
	if b.SampleType != VO2Type {
		return Fail(422, "unsupported_type")
	}
	for _, id := range []string{b.DatasetID, b.DatasetEpoch, b.CollectorEpoch, b.StreamID, b.BatchID} {
		if !ValidID(id) {
			return Fail(422, "invalid_payload")
		}
	}
	if b.ClientSeq < 1 || (b.ClientSeq == 1) != (b.PreviousBatchID == nil) || b.PreviousBatchID != nil && !ValidID(*b.PreviousBatchID) {
		return Fail(422, "invalid_payload")
	}
	if !vo2Time(b.BootstrapStartUs) || !vo2Time(b.FlowCreatedAtUs) || !vo2Time(b.QueryCompletedAtUs) || b.FlowCreatedAtUs-b.BootstrapStartUs != VO2WindowUs || b.QueryCompletedAtUs < b.FlowCreatedAtUs || b.QueryFingerprint != VO2Fingerprint(b.BootstrapStartUs) {
		return Fail(422, "invalid_payload")
	}
	if b.Facts == nil || b.DeletedUUIDs == nil {
		return Fail(422, "invalid_payload")
	}
	if len(b.Facts)+len(b.DeletedUUIDs) > MaxVO2Changes {
		return Fail(413, "batch_too_large")
	}
	for _, id := range b.DeletedUUIDs {
		if !ValidID(id) {
			return Fail(422, "invalid_payload")
		}
	}
	for _, f := range b.Facts {
		if !ValidID(f.UUID) || !vo2Time(f.StartUs) || !vo2Time(f.EndUs) || f.EndUs < f.StartUs || f.EndUs < b.BootstrapStartUs {
			return Fail(422, "invalid_payload")
		}
		if f.Quantity.Unit != "mL/kg/min" {
			return Fail(422, "unsupported_unit")
		}
		if f.Quantity.Value <= 0 || math.IsNaN(f.Quantity.Value) || math.IsInf(f.Quantity.Value, 0) {
			return Fail(422, "invalid_value")
		}
		p := f.Provenance
		r := p.SourceRevision
		if p.SchemaVersion != VO2ProvenanceSchema || r.Source.BundleIdentifier == "" || len(r.Source.BundleIdentifier) > 255 || len(r.Source.Name) > 255 || r.OperatingSystemVersion.Major < 0 || r.OperatingSystemVersion.Minor < 0 || r.OperatingSystemVersion.Patch < 0 {
			return Fail(422, "source_invalid")
		}
		strs := []*string{r.Version, r.ProductType}
		if p.Device != nil {
			d := p.Device
			strs = append(strs, d.Name, d.Manufacturer, d.Model, d.HardwareVersion, d.FirmwareVersion, d.SoftwareVersion)
		}
		for _, s := range strs {
			if s != nil && len(*s) > 255 {
				return Fail(422, "source_invalid")
			}
		}
		if err := validateVO2Metadata(f.Metadata); err != nil {
			return err
		}
		if len(VO2Canonical(f.Context())) > MaxVO2ContextBytes {
			return Fail(413, "batch_too_large")
		}
	}
	return nil
}

// Sorted UTF-8 JSON keys, exact integer tokens, binary64 shortest round trip.
// Marshal typed values first so input whitespace/number spelling is not identity.
func VO2Canonical(value any) []byte {
	raw, _ := json.Marshal(value)
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	_ = d.Decode(&v)
	out, _ := json.Marshal(v)
	return out
}
func (p VO2Provenance) EpochID() string { return "sha256:" + DailyDigest(VO2Canonical(p)) }

// StableEpochID identifies the recording app and device across software
// updates: bundle, product type and hardware, without app/OS/firmware/software
// versions or the user-editable names. The v2 stack matches the user's VO2
// source selection by this identity, so a watchOS update keeps the selection.
func (p VO2Provenance) StableEpochID() string {
	type device struct {
		Manufacturer, Model, HardwareVersion *string
	}
	id := struct {
		Bundle      string
		ProductType *string
		Device      *device
	}{p.SourceRevision.Source.BundleIdentifier, p.SourceRevision.ProductType, nil}
	if p.Device != nil {
		id.Device = &device{p.Device.Manufacturer, p.Device.Model, p.Device.HardwareVersion}
	}
	return "sha256:" + DailyDigest(VO2Canonical(id))
}
func (f VO2Fact) Context() map[string]any {
	return map[string]any{"schemaVersion": VO2FactSchema, "provenance": f.Provenance, "sourceEpochID": f.Provenance.EpochID(), "typedMetadata": f.Metadata}
}
func (b VO2Batch) Events() []Event {
	events := make([]Event, 0, len(b.Facts)+len(b.DeletedUUIDs))
	for _, f := range b.Facts {
		start, end, q := f.StartUs, f.EndUs, f.Quantity
		events = append(events, Event{Op: "upsert", Kind: "sample", Type: VO2Type, UUID: f.UUID, StartUs: &start, EndUs: &end, Quantity: &q, Source: &Source{BundleID: f.Provenance.SourceRevision.Source.BundleIdentifier}, Metadata: map[string]any{VO2Reserved: f.Context()}})
	}
	for _, id := range b.DeletedUUIDs {
		events = append(events, Event{Op: "delete", Kind: "sample", Type: VO2Type, UUID: id})
	}
	return events
}

type VO2Receipt struct {
	Contract                string `json:"contract"`
	Version                 int    `json:"version"`
	MetadataRegistryVersion int    `json:"metadataRegistryVersion"`
	BatchID                 string `json:"batchID"`
	DatasetID               string `json:"datasetID"`
	DatasetEpoch            string `json:"datasetEpoch"`
	CollectorEpoch          string `json:"collectorEpoch"`
	StreamID                string `json:"streamID"`
	ClientSeq               int64  `json:"clientSeq"`
	RequestSHA256           string `json:"requestSHA256"`
	IngestStatus            string `json:"ingestStatus"`
	CommitRevision          int64  `json:"commitRevision"`
	Counts                  Counts `json:"counts"`
	DeleteStatus            string `json:"deleteStatus"`
	AnalysisStatus          string `json:"analysisStatus"`
}
type VO2SourceCount struct {
	SourceEpochID string `json:"sourceEpochID"`
	Count         int    `json:"count"`
}
type VO2Coverage struct {
	Contract                   string          `json:"contract"`
	Version                    int             `json:"version"`
	SampleType                 string          `json:"sampleType"`
	State                      string          `json:"state"`
	DatasetGeneration          string          `json:"datasetGeneration"`
	DatasetCommitRevision      int64           `json:"datasetCommitRevision"`
	QueryCompleted             bool            `json:"queryCompleted"`
	AnchorPresent              bool            `json:"anchorPresent"`
	BootstrapComplete          bool            `json:"bootstrapComplete"`
	SyncCompleteForObservation bool            `json:"syncCompleteForObservation"`
	HasMore                    *bool           `json:"hasMore"`
	BootstrapStart             *int64          `json:"bootstrapStart"`
	QueryCompletedAt           *int64          `json:"queryCompletedAt"`
	LastSyncAt                 *int64          `json:"lastSyncAt"`
	LastFactAt                 *int64          `json:"lastFactAt"`
	SampleCount                int             `json:"sampleCount"`
	DeletedCount               int             `json:"deletedCount"`
	StreamSequence             int64           `json:"streamSequence"`
	SummaryObservationSequence int64           `json:"summaryObservationSequence"`
	SourceSummary              json.RawMessage `json:"sourceSummary"`
	ReadAuthorization          string          `json:"readAuthorization"`
	HasLiveFacts               bool            `json:"hasLiveFacts"`
	HasDeletedFacts            bool            `json:"hasDeletedFacts"`
}

var vo2WatchProduct = regexp.MustCompile(`^Watch[0-9]+,[0-9]+$`)
var vo2ReverseDomain = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)+$`)

// Descriptive classification only. No result, selection, current pointer or model.
func (f VO2Fact) Origin() (string, string) {
	if f.Metadata["HKWasUserEntered"].Boolean() {
		return "user_entered", "user_entered"
	}
	t := f.Metadata["HKVO2MaxTestType"].Integer()
	if t == 1 {
		return "unclassified", "max_exercise_assertion_only"
	}
	if t < 2 || t > 4 {
		return "unclassified", "test_type_missing"
	}
	r, d := f.Provenance.SourceRevision, f.Provenance.Device
	bundle := r.Source.BundleIdentifier
	apple := bundle == "com.apple.health" || bundle == "com.apple.Workout" || strings.HasPrefix(bundle, "com.apple.health.") && ValidID(strings.ToLower(strings.TrimPrefix(bundle, "com.apple.health.")))
	val := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	if apple && vo2WatchProduct.MatchString(val(r.ProductType)) && d != nil && (strings.EqualFold(val(d.Manufacturer), "Apple") || strings.EqualFold(val(d.Manufacturer), "Apple Inc.")) && (strings.EqualFold(val(d.Model), "Watch") || strings.EqualFold(val(d.Model), "Apple Watch")) {
		return "apple_watch_estimate", "descriptor_combination"
	}
	if vo2ReverseDomain.MatchString(bundle) && bundle != "com.apple" && !strings.HasPrefix(bundle, "com.apple.") {
		return "third_party_estimate", "third_party_source"
	}
	return "unclassified", "source_evidence_incomplete_or_conflicting"
}
