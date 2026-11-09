package syncing

import (
	"time"
)

// WorkoutIngestMode chooses the one Workout entry for every client that talks
// to this process. A dataset is never served by both idempotency rules at once.
type WorkoutIngestMode string

const (
	WorkoutIngestLegacy WorkoutIngestMode = "legacy"
	WorkoutIngestUUID   WorkoutIngestMode = "uuid"
	WorkoutIngestPaused WorkoutIngestMode = "uuid_paused"
)

func ParseWorkoutIngestMode(value string) (WorkoutIngestMode, error) {
	switch WorkoutIngestMode(value) {
	case WorkoutIngestLegacy:
		return WorkoutIngestLegacy, nil
	case WorkoutIngestUUID:
		return WorkoutIngestUUID, nil
	case WorkoutIngestPaused:
		return WorkoutIngestPaused, nil
	}
	return "", Fail(422, "unsupported_workout_ingest_mode")
}

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

// WorkoutIngestEnvelope is the wire-only identity of a protocol-4 payload. It
// proves UTF-8, no duplicate keys, bounded nesting, protocol 4 and the four
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
	wire, err := decodeV4Envelope(raw)
	if err != nil {
		return WorkoutIngestEnvelope{}, Fail(422, "unsupported_payload")
	}
	if *wire.ProtocolVersion != 4 {
		return WorkoutIngestEnvelope{}, bundleField("protocolVersion", "unsupported_protocol")
	}
	return WorkoutIngestEnvelope{DatasetID: *wire.DatasetID, DatasetEpoch: *wire.DatasetEpoch,
		CollectorEpoch: *wire.CollectorEpoch, WorkoutID: *wire.WorkoutID, ProtocolVersion: 4}, nil
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

// Accepting reports whether new UUID payloads may be accepted. Status queries
// and already accepted work remain available in every mode.
func (m WorkoutIngestMode) Accepting() bool { return m == WorkoutIngestUUID }

// A client that already used the UUID entry must not fall back to the old
// upload path, which would apply a second idempotency rule to the same dataset.
func WorkoutUUIDIngestRequired() error {
	return Fail(409, "workout_uuid_ingest_required")
}

func WorkoutUUIDIngestPaused() error {
	return &Error{Status: 503, Code: "workout_uuid_ingest_paused", RetryAfterSeconds: 30}
}

func WorkoutUUIDIngestDisabled() error {
	return Fail(409, "workout_uuid_ingest_disabled")
}
