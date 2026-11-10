package syncing

import (
	"encoding/json"
	"time"
)

// Acceptance keeps only a bounded backlog. Committed/rejected history retains
// its raw payload for the exact-byte record but never counts against this
// parse capacity.
const (
	MaxWorkoutIngestBacklog                      = 200
	MaxWorkoutIngestBacklogBytes           int64 = 512 << 20
	WorkoutIngestLease                           = 90 * time.Second
	WorkoutIngestProcessingBudget                = 60 * time.Second
	WorkoutIngestAttempts                        = 3
	WorkoutIngestDecodeBudget                    = 15 * time.Second
	WorkoutIngestRetryDelay                      = 5 * time.Second
	workoutIngestAcceptBacklogRetrySeconds       = 5
)

// WorkoutIngestEnvelope is the wire-only identity of a protocol-6 payload. It
// proves UTF-8, no duplicate keys, bounded nesting, protocol 6 and the four
// UUIDs; Workout core, source policy and every projected fact stay with the
// worker.
type WorkoutIngestEnvelope struct {
	DatasetID       string
	DatasetEpoch    string
	CollectorEpoch  string
	WorkoutID       string
	ProtocolVersion int
}

func DecodeWorkoutIngestEnvelope(raw []byte) (WorkoutIngestEnvelope, error) {
	var version struct {
		ProtocolVersion *int `json:"protocolVersion"`
	}
	if json.Unmarshal(raw, &version) != nil || version.ProtocolVersion == nil {
		return WorkoutIngestEnvelope{}, Fail(422, "unsupported_payload")
	}
	if *version.ProtocolVersion != 6 {
		return WorkoutIngestEnvelope{}, bundleField("protocolVersion", "unsupported_protocol")
	}
	wire, err := decodeV6Envelope(raw)
	if err != nil {
		return WorkoutIngestEnvelope{}, Fail(422, "unsupported_payload")
	}
	return WorkoutIngestEnvelope{DatasetID: *wire.DatasetID, DatasetEpoch: *wire.DatasetEpoch,
		CollectorEpoch: *wire.CollectorEpoch, WorkoutID: *wire.WorkoutID, ProtocolVersion: 6}, nil
}

// WorkoutIngestReceipt is the acceptance-time replay. Later parse state never
// rewrites these two fields, so a duplicate POST returns the first acceptance.
type WorkoutIngestReceipt struct {
	WorkoutID    string    `json:"workoutID"`
	IngestStatus string    `json:"ingestStatus"`
	AcceptedAt   time.Time `json:"acceptedAt"`
}

// WorkoutIngestStatus is the single-snapshot status answer. The status domain
// is deleted > accepted/committed/rejected > already_imported > not_found.
type WorkoutIngestStatus struct {
	WorkoutID      string     `json:"workoutID"`
	IngestStatus   string     `json:"ingestStatus"`
	AcceptedAt     *time.Time `json:"acceptedAt,omitempty"`
	CommitRevision *int64     `json:"commitRevision,omitempty"`
	AnalysisStatus string     `json:"analysisStatus,omitempty"`
	AnalysisJobIDs []string   `json:"analysisJobIDs,omitempty"`
	ErrorCode      string     `json:"errorCode,omitempty"`
}

// WorkoutIngestOutcome is the worker's bounded log summary. WorkoutID is
// hashed by the caller; raw payload bytes are never part of it.
type WorkoutIngestOutcome struct {
	Claimed   bool
	WorkoutID string
	Attempts  int
	Status    string
	ErrorCode string
}

func WorkoutIngestBacklogExceeded() error {
	return &Error{Status: 429, Code: "workout_ingest_backlog_exceeded",
		RetryAfterSeconds: workoutIngestAcceptBacklogRetrySeconds}
}
