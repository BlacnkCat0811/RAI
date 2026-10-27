package syncing

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
)

// Workout detail packages are the durable transport for one Workout whose
// complete source object cannot fit the single-line inline Workout limit.
// Chunks are immutable staging data; the Workout fact becomes visible only at
// seal. These are initial safety caps, not measured phone-import throughput.
const (
	MaxWorkoutPackageChunks          = 100
	MaxWorkoutPackageDetailsPerChunk = 1000
	MaxWorkoutPackageDetails         = 20000
)

// WorkoutPackageRequest reserves one Workout UUID and carries only the small
// Workout core. Statistics, events and activities arrive as detail chunks.
type WorkoutPackageRequest struct {
	DatasetID      string         `json:"datasetID"`
	DatasetEpoch   string         `json:"datasetEpoch"`
	CollectorEpoch string         `json:"collectorEpoch"`
	WorkoutID      string         `json:"workoutID"`
	StartUs        *int64         `json:"startUs"`
	EndUs          *int64         `json:"endUs"`
	Source         *Source        `json:"source"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	Workout        *Workout       `json:"workout"`
}

// CoreEvent is the exact Event identity that will be committed at seal, before
// any detail is attached. Keeping metadata as decoded json.Number values makes
// the eventual content hash stable across retries and cross-process recovery.
func (r WorkoutPackageRequest) CoreEvent() Event {
	return Event{
		Op: "upsert", Kind: "workout", Type: WorkoutType, UUID: r.WorkoutID,
		StartUs: r.StartUs, EndUs: r.EndUs, Source: r.Source, Metadata: r.Metadata, Workout: r.Workout,
	}
}

func (r WorkoutPackageRequest) Validate() error {
	for _, id := range []string{r.DatasetID, r.DatasetEpoch, r.CollectorEpoch, r.WorkoutID} {
		if !ValidID(id) {
			return Fail(422, "unsupported_payload")
		}
	}
	if r.StartUs == nil || r.EndUs == nil || r.Source == nil || r.Workout == nil ||
		*r.StartUs > *r.EndUs ||
		len(r.Workout.Statistics) != 0 || len(r.Workout.Events) != 0 || len(r.Workout.Activities) != 0 {
		return Fail(422, "unsupported_payload")
	}
	return r.CoreEvent().Validate(WorkoutType)
}

// CoreCanonical is stored byte-for-byte. It contains no detail arrays, so the
// package cannot silently duplicate or lose an inline child.
func (r WorkoutPackageRequest) CoreCanonical() ([]byte, []byte) {
	return r.CoreEvent().Canonical()
}

type WorkoutPackageSeal struct {
	ChunkCount     int    `json:"chunkCount"`
	TotalDetails   int64  `json:"totalDetails"`
	ManifestSHA256 string `json:"manifestSHA256"`
}

func (s WorkoutPackageSeal) Validate() error {
	if s.ChunkCount < 1 || s.ChunkCount > MaxWorkoutPackageChunks ||
		s.TotalDetails < 1 || s.TotalDetails > MaxWorkoutPackageDetails ||
		!digestPattern.MatchString(s.ManifestSHA256) {
		return Fail(422, "unsupported_payload")
	}
	return nil
}

type WorkoutPackageState struct {
	WorkoutID       string          `json:"workoutID"`
	DatasetEpoch    string          `json:"datasetEpoch"`
	CollectorEpoch  string          `json:"collectorEpoch"`
	StartUs         int64           `json:"startUs"`
	EndUs           int64           `json:"endUs"`
	IngestStatus    string          `json:"ingestStatus"`
	CommittedChunks int             `json:"committedChunks"`
	TotalDetails    int64           `json:"totalDetails"`
	ManifestSHA256  string          `json:"manifestSHA256"`
	ChunkCount      *int            `json:"chunkCount,omitempty"`
	Receipt         json.RawMessage `json:"receipt,omitempty"`
}

type WorkoutPackageChunkReceipt struct {
	WorkoutID       string `json:"workoutID"`
	ChunkIndex      int    `json:"chunkIndex"`
	IngestStatus    string `json:"ingestStatus"`
	DetailCount     int    `json:"detailCount"`
	PayloadSHA256   string `json:"payloadSHA256"`
	DetailsSHA256   string `json:"detailsSHA256"`
	ManifestSHA256  string `json:"manifestSHA256"`
	CommittedChunks int    `json:"committedChunks"`
	TotalDetails    int64  `json:"totalDetails"`
}

type WorkoutPackageReceipt struct {
	WorkoutID      string   `json:"workoutID"`
	IngestStatus   string   `json:"ingestStatus"`
	StartUs        int64    `json:"startUs"`
	EndUs          int64    `json:"endUs"`
	ChunkCount     int      `json:"chunkCount"`
	TotalDetails   int64    `json:"totalDetails"`
	ManifestSHA256 string   `json:"manifestSHA256"`
	CommitRevision int64    `json:"commitRevision"`
	AnalysisStatus string   `json:"analysisStatus"`
	AnalysisJobIDs []string `json:"analysisJobIDs"`
}

// WorkoutDetail is one top-level detail entry. Activity entries retain their
// nested statistics/events as one bounded source object; statistic and event
// entries are parent-level arrays. Fields are never reordered by the server.
type WorkoutDetail struct {
	Kind      string            `json:"kind"`
	Ordinal   int               `json:"ordinal"`
	Activity  *WorkoutActivity  `json:"activity,omitempty"`
	Statistic *WorkoutStatistic `json:"statistic,omitempty"`
	Event     *WorkoutEvent     `json:"event,omitempty"`
}

func (d WorkoutDetail) DetailCount() int64 {
	if d.Kind == "activity" && d.Activity != nil {
		return 1 + int64(len(d.Activity.Statistics)) + int64(len(d.Activity.Events))
	}
	return 1
}

func (d WorkoutDetail) Validate(parentStart, parentEnd int64) error {
	bad := func() error { return Fail(422, "unsupported_payload") }
	switch d.Kind {
	case "activity":
		if d.Ordinal < 0 || d.Ordinal >= MaxWorkoutActivities || d.Activity == nil || d.Statistic != nil || d.Event != nil {
			return bad()
		}
		return d.Activity.Validate(parentStart, parentEnd)
	case "statistic":
		if d.Ordinal < 0 || d.Ordinal >= MaxWorkoutStatistics || d.Statistic == nil || d.Activity != nil || d.Event != nil {
			return bad()
		}
		return validateWorkoutDetails([]WorkoutStatistic{*d.Statistic}, nil, parentStart, parentEnd)
	case "event":
		if d.Ordinal < 0 || d.Ordinal >= MaxWorkoutEvents || d.Event == nil || d.Activity != nil || d.Statistic != nil {
			return bad()
		}
		return validateWorkoutDetails(nil, []WorkoutEvent{*d.Event}, parentStart, parentEnd)
	default:
		return bad()
	}
}

type WorkoutDetailChunk struct {
	WorkoutID   string
	ChunkIndex  int
	Entries     []WorkoutDetail
	DetailCount int
	// TotalDetails includes nested activity statistics/events. It is the
	// package-wide budget unit and is never inferred from compressed bytes.
	TotalDetails  int64
	WireSHA256    string
	DetailsSHA256 string
}

// ParseWorkoutDetailChunk performs hash/gzip/schema/bounds work before any
// database lock. Ordinal continuity across chunks is checked in the package
// row lock transaction, where the prior committed counts are known.
func ParseWorkoutDetailChunk(r io.Reader, parentStart, parentEnd int64) (WorkoutDetailChunk, error) {
	wire, err := io.ReadAll(io.LimitReader(r, MaxWireBytes+1))
	if err != nil {
		return WorkoutDetailChunk{}, Fail(400, "body_read_failed")
	}
	if len(wire) > MaxWireBytes {
		return WorkoutDetailChunk{}, Fail(413, "workout_package_chunk_too_large")
	}
	wireHash := sha256.Sum256(wire)
	gz, err := gzip.NewReader(bytes.NewReader(wire))
	if err != nil {
		return WorkoutDetailChunk{}, Fail(422, "unsupported_payload")
	}
	defer gz.Close()
	decoded, err := io.ReadAll(io.LimitReader(gz, MaxDecodedBytes+1))
	if len(decoded) > MaxDecodedBytes {
		return WorkoutDetailChunk{}, Fail(413, "workout_package_chunk_too_large")
	}
	if err != nil {
		return WorkoutDetailChunk{}, Fail(422, "unsupported_payload")
	}
	lines := bytes.Split(decoded, []byte{'\n'})
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return WorkoutDetailChunk{}, Fail(422, "unsupported_payload")
	}
	if len(lines) > MaxWorkoutPackageDetailsPerChunk {
		return WorkoutDetailChunk{}, Fail(413, "workout_package_chunk_too_large")
	}
	entries := make([]WorkoutDetail, 0, len(lines))
	var total int64
	for i, line := range lines {
		if len(line) > MaxLineBytes {
			return WorkoutDetailChunk{}, Fail(413, "workout_detail_too_large")
		}
		var detail WorkoutDetail
		if err = DecodeJSON(line, &detail); err != nil {
			return WorkoutDetailChunk{}, &Error{Status: 422, Code: "unsupported_payload", EventIndex: &i}
		}
		if err = detail.Validate(parentStart, parentEnd); err != nil {
			return WorkoutDetailChunk{}, &Error{Status: 422, Code: "unsupported_payload", EventIndex: &i}
		}
		total += detail.DetailCount()
		if total > MaxWorkoutPackageDetails {
			return WorkoutDetailChunk{}, Fail(413, "workout_package_too_large")
		}
		entries = append(entries, detail)
	}
	_, digest := WorkoutDetailsCanonical(entries)
	return WorkoutDetailChunk{
		Entries: entries, DetailCount: len(entries), TotalDetails: total,
		WireSHA256: hex.EncodeToString(wireHash[:]), DetailsSHA256: hex.EncodeToString(digest),
	}, nil
}

func WorkoutDetailsCanonical(details []WorkoutDetail) ([]byte, []byte) {
	b, _ := json.Marshal(details)
	h := sha256.Sum256(b)
	return b, h[:]
}

func EmptyWorkoutPackageManifest() []byte { return make([]byte, sha256.Size) }

func AdvanceWorkoutPackageManifest(previous []byte, chunkDigest []byte) []byte {
	h := sha256.New()
	_, _ = h.Write(previous)
	_, _ = h.Write(chunkDigest)
	return h.Sum(nil)
}

// DecodeWorkoutDetails decodes the canonical array stored with one immutable
// chunk. Each entry is decoded with the same strict field/null rules as wire
// input; chunk identity is retained as raw bytes elsewhere.
func DecodeWorkoutDetails(raw []byte) ([]WorkoutDetail, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, Fail(422, "unsupported_payload")
	}
	details := make([]WorkoutDetail, 0, len(items))
	for _, item := range items {
		var detail WorkoutDetail
		if err := DecodeJSON(item, &detail); err != nil {
			return nil, err
		}
		details = append(details, detail)
	}
	return details, nil
}

// WorkoutDetailCount is used by the package seal to verify the declared total
// against the decoded entries. It is deliberately not a payload checksum.
func WorkoutDetailCount(details []WorkoutDetail) int64 {
	var total int64
	for _, detail := range details {
		total += detail.DetailCount()
	}
	return total
}
