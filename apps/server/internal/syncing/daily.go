package syncing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"time"
)

const (
	DailyVersion         = 1
	MaxDailyChanges      = 128
	MaxDailyBytes        = 512 << 10
	MaxDailyLookbackDays = 180
	DailyHRV             = "HKQuantityTypeIdentifierHeartRateVariabilitySDNN"
	DailyRHR             = "HKQuantityTypeIdentifierRestingHeartRate"
	DailySleep           = "HKCategoryTypeIdentifierSleepAnalysis"
)

var DailyTypes = []string{DailyHRV, DailyRHR, DailySleep}

// DailyFact is a source sample, never a day aggregate or a Workout child.
type DailyFact struct {
	UUID     string          `json:"uuid"`
	StartUs  int64           `json:"startUs"`
	EndUs    int64           `json:"endUs"`
	Quantity *Quantity       `json:"quantity,omitempty"`
	Category *int            `json:"category,omitempty"`
	Source   HealthKitSource `json:"source"`
	Metadata map[string]any  `json:"metadata,omitempty"`
}

type DailyBatch struct {
	Version          int         `json:"version"`
	DatasetID        string      `json:"datasetID"`
	DatasetEpoch     string      `json:"datasetEpoch"`
	CollectorEpoch   string      `json:"collectorEpoch"`
	StreamID         string      `json:"streamID"`
	BatchID          string      `json:"batchID"`
	ClientSeq        int64       `json:"clientSeq"`
	PreviousBatchID  *string     `json:"previousBatchID"`
	SampleType       string      `json:"sampleType"`
	QueryFingerprint string      `json:"queryFingerprint"`
	BootstrapStartUs int64       `json:"bootstrapStartUs"`
	ObservedAtUs     int64       `json:"observedAtUs"`
	HasMore          bool        `json:"hasMore"`
	Facts            []DailyFact `json:"facts"`
	DeletedUUIDs     []string    `json:"deletedUUIDs"`
}

func DailyFingerprint(sampleType string, start int64) string {
	return "sha256:" + DailyDigest([]byte(fmt.Sprintf("daily-v1|%s|%d", sampleType, start)))
}
func DailyDigest(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }

func DecodeDailyBatch(raw []byte) (DailyBatch, error) {
	var b DailyBatch
	if len(raw) > MaxDailyBytes {
		return b, Fail(413, "batch_too_large")
	}
	if err := DecodeStrictJSON(raw, &b); err != nil {
		return b, Fail(422, "invalid_payload")
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	for _, key := range []string{"version", "datasetID", "datasetEpoch", "collectorEpoch", "streamID", "batchID", "clientSeq", "previousBatchID", "sampleType", "queryFingerprint", "bootstrapStartUs", "observedAtUs", "hasMore", "facts", "deletedUUIDs"} {
		v, ok := fields[key]
		if !ok || key != "previousBatchID" && string(v) == "null" {
			return b, Fail(422, "invalid_payload")
		}
	}
	var facts []map[string]json.RawMessage
	_ = json.Unmarshal(fields["facts"], &facts)
	for _, fact := range facts {
		for _, key := range []string{"uuid", "startUs", "endUs", "source"} {
			if v, ok := fact[key]; !ok || string(v) == "null" {
				return b, Fail(422, "invalid_payload")
			}
		}
		for _, v := range fact {
			if string(v) == "null" {
				return b, Fail(422, "invalid_payload")
			}
		}
	}
	return b, b.Validate()
}

func (b DailyBatch) Validate() error {
	if b.Version != DailyVersion {
		return Fail(422, "unsupported_version")
	}
	if b.SampleType != DailyHRV && b.SampleType != DailyRHR && b.SampleType != DailySleep {
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
	if b.BootstrapStartUs < -62135596800000000 || b.ObservedAtUs > 253402300799999999 || b.ObservedAtUs < b.BootstrapStartUs || b.QueryFingerprint != DailyFingerprint(b.SampleType, b.BootstrapStartUs) {
		return Fail(422, "invalid_payload")
	}
	if b.ClientSeq == 1 && b.ObservedAtUs-b.BootstrapStartUs > int64(MaxDailyLookbackDays)*24*int64(time.Hour/time.Microsecond) {
		return Fail(422, "invalid_payload")
	}
	if b.Facts == nil || b.DeletedUUIDs == nil {
		return Fail(422, "invalid_payload")
	}
	if len(b.Facts)+len(b.DeletedUUIDs) > MaxDailyChanges {
		return Fail(413, "batch_too_large")
	}
	for _, id := range b.DeletedUUIDs {
		if !ValidID(id) {
			return Fail(422, "invalid_payload")
		}
	}
	for _, f := range b.Facts {
		if !ValidID(f.UUID) || f.StartUs < -62135596800000000 || f.EndUs > 253402300799999999 || f.EndUs < f.StartUs || f.EndUs < b.BootstrapStartUs {
			return Fail(422, "invalid_payload")
		}
		if f.Source.Validate() != nil {
			return Fail(422, "source_invalid")
		}
		if b.SampleType == DailySleep {
			// SDK 26.5 HKCategoryValues.h. Unknown values fail closed; no anchor ACK.
			if f.Category == nil || *f.Category < 0 || *f.Category > 5 || f.Quantity != nil {
				return Fail(422, "invalid_category")
			}
		} else {
			if f.Quantity == nil || f.Category != nil {
				return Fail(422, "invalid_payload")
			}
			if f.Quantity.Unit != Units[b.SampleType] {
				return Fail(422, "unsupported_unit")
			}
			if math.IsNaN(f.Quantity.Value) || math.IsInf(f.Quantity.Value, 0) {
				return Fail(422, "invalid_payload")
			}
		}
		if err := validateDailyMetadata(f.Metadata); err != nil {
			return err
		}
	}
	return nil
}

func validateDailyMetadata(m map[string]any) error {
	for k, v := range m {
		switch k {
		case "HKTimeZone":
			s, ok := v.(string)
			if !ok || len(s) > 128 {
				return Fail(422, "invalid_payload")
			}
			if _, err := time.LoadLocation(s); err != nil || s == "" {
				return Fail(422, "invalid_payload")
			}
		case "HKMetadataKeySyncIdentifier":
			s, ok := v.(string)
			if !ok || s == "" || len(s) > 255 {
				return Fail(422, "invalid_payload")
			}
		case "HKWasUserEntered":
			if _, ok := v.(bool); !ok {
				return Fail(422, "invalid_payload")
			}
		case "HKAlgorithmVersion", "HKMetadataKeySyncVersion", "HKMetadataKeyHeartRateMotionContext", "HKMetadataKeyHeartRateSensorLocation", "HKMetadataKeyUserMotionContext", "HKMetadataKeyDevicePlacementSide":
			switch v.(type) {
			case json.Number, float64, int, int64:
			default:
				return Fail(422, "invalid_payload")
			}
			raw, _ := json.Marshal(v)
			var n json.Number
			if json.Unmarshal(raw, &n) != nil {
				return Fail(422, "invalid_payload")
			}
			if _, err := n.Int64(); err != nil {
				return Fail(422, "invalid_payload")
			}
		default:
			return Fail(422, "unsupported_metadata")
		}
	}
	_, id := m["HKMetadataKeySyncIdentifier"]
	_, version := m["HKMetadataKeySyncVersion"]
	if id != version {
		return Fail(422, "invalid_payload")
	}
	return nil
}

// Keep the shared immutable sample/hash/delete machinery. Reserved provenance
// is part of the identity hash and change log, not a mutable supplement.
func (b DailyBatch) Events() []Event {
	events := make([]Event, 0, len(b.Facts)+len(b.DeletedUUIDs))
	for _, f := range b.Facts {
		m := make(map[string]any, len(f.Metadata)+1)
		for k, v := range f.Metadata {
			m[k] = v
		}
		m["RAI:dailySource"] = f.Source
		s, e := f.StartUs, f.EndUs
		events = append(events, Event{Op: "upsert", Kind: "sample", Type: b.SampleType, UUID: f.UUID, StartUs: &s, EndUs: &e, Quantity: f.Quantity, Category: f.Category, Source: &Source{BundleID: f.Source.BundleID}, Metadata: m})
	}
	for _, id := range b.DeletedUUIDs {
		events = append(events, Event{Op: "delete", Kind: "sample", Type: b.SampleType, UUID: id})
	}
	return events
}

type DailyReceipt struct {
	Version        int    `json:"version"`
	BatchID        string `json:"batchID"`
	DatasetID      string `json:"datasetID"`
	DatasetEpoch   string `json:"datasetEpoch"`
	CollectorEpoch string `json:"collectorEpoch"`
	StreamID       string `json:"streamID"`
	ClientSeq      int64  `json:"clientSeq"`
	RequestSHA256  string `json:"requestSHA256"`
	IngestStatus   string `json:"ingestStatus"`
	CommitRevision int64  `json:"commitRevision"`
	Counts         Counts `json:"counts"`
	DeleteStatus   string `json:"deleteStatus"`
	AnalysisStatus string `json:"analysisStatus"`
}

type DailyCoverage struct {
	SampleType        string          `json:"sampleType"`
	State             string          `json:"state"`
	QueryCompleted    bool            `json:"queryCompleted"`
	AnchorPresent     bool            `json:"anchorPresent"`
	BootstrapComplete bool            `json:"bootstrapComplete"`
	HasMore           bool            `json:"hasMore"`
	SampleCount       int             `json:"sampleCount"`
	DeletedCount      int             `json:"deletedCount"`
	HasLiveFacts      bool            `json:"hasLiveFacts"`
	HasDeletedFacts   bool            `json:"hasDeletedFacts"`
	LastDiscoveryAtUs *int64          `json:"lastDiscoveryAtUs"`
	LastSyncAt        *time.Time      `json:"lastSyncAt"`
	LastFactAtUs      *int64          `json:"lastFactAtUs"`
	SourceContext     json.RawMessage `json:"sourceContext"`
}
