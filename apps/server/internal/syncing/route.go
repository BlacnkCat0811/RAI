package syncing

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
)

const (
	RouteType              = "HKWorkoutRouteTypeIdentifier"
	MaxRouteChunks         = 1000
	MaxRoutePoints         = 1000000
	MaxRoutePointsPerChunk = 5000
	MaxRouteWireBytes      = MaxWireBytes
	MaxRouteDecodedBytes   = MaxDecodedBytes
)

// RoutePoint is the bounded P3 route representation. All optional source
// measurements stay absent when HealthKit did not provide them; null is rejected
// before this type is constructed.
type RoutePoint struct {
	Sequence           int64    `json:"sequence"`
	RecordedAtUs       int64    `json:"recordedAtUs"`
	Latitude           float64  `json:"latitude"`
	Longitude          float64  `json:"longitude"`
	Altitude           *float64 `json:"altitude,omitempty"`
	HorizontalAccuracy *float64 `json:"horizontalAccuracy,omitempty"`
	VerticalAccuracy   *float64 `json:"verticalAccuracy,omitempty"`
	Speed              *float64 `json:"speed,omitempty"`
	Course             *float64 `json:"course,omitempty"`
}

func (p RoutePoint) Validate() error {
	ok := p.Sequence >= 0 && p.RecordedAtUs >= -62135596800000000 && p.RecordedAtUs <= 253402300799999999 &&
		finite(p.Latitude) && p.Latitude >= -90 && p.Latitude <= 90 &&
		finite(p.Longitude) && p.Longitude >= -180 && p.Longitude <= 180
	for _, value := range []*float64{p.Altitude, p.HorizontalAccuracy, p.VerticalAccuracy, p.Speed, p.Course} {
		if value != nil && !finite(*value) {
			return Fail(422, "unsupported_payload")
		}
	}
	if p.HorizontalAccuracy != nil && *p.HorizontalAccuracy < 0 ||
		p.VerticalAccuracy != nil && *p.VerticalAccuracy < 0 ||
		p.Speed != nil && *p.Speed < 0 ||
		p.Course != nil && (*p.Course < 0 || *p.Course >= 360) {
		ok = false
	}
	if !ok {
		return Fail(422, "unsupported_payload")
	}
	return nil
}

func RoutePointsCanonical(points []RoutePoint) ([]byte, []byte) {
	b, _ := json.Marshal(points)
	h := sha256.Sum256(b)
	return b, h[:]
}

func EmptyRouteManifest() []byte { return make([]byte, sha256.Size) }
func AdvanceRouteManifest(previous, chunkDigest []byte) []byte {
	h := sha256.New()
	_, _ = h.Write(previous)
	_, _ = h.Write(chunkDigest)
	return h.Sum(nil)
}

type RouteManifest struct {
	ProtocolVersion               int     `json:"protocolVersion"`
	DatasetID                     string  `json:"datasetID"`
	DatasetEpoch                  string  `json:"datasetEpoch"`
	CollectorEpoch                string  `json:"collectorEpoch"`
	RouteID                       string  `json:"routeID"`
	WorkoutID                     string  `json:"workoutID"`
	Source                        *Source `json:"source"`
	ExpectedChunkCount            int     `json:"expectedChunkCount"`
	ExpectedPointCount            int64   `json:"expectedPointCount"`
	FirstRecordedAtUs             int64   `json:"firstRecordedAtUs"`
	LastRecordedAtUs              int64   `json:"lastRecordedAtUs"`
	ExpectedPayloadManifestSHA256 string  `json:"expectedPayloadManifestSHA256,omitempty"`
}

func (m RouteManifest) Validate() error {
	if err := validateProtocol(m.ProtocolVersion); err != nil {
		return err
	}
	for _, id := range []string{m.DatasetID, m.DatasetEpoch, m.CollectorEpoch, m.RouteID, m.WorkoutID} {
		if !ValidID(id) {
			return Fail(422, "unsupported_payload")
		}
	}
	if m.Source == nil || m.Source.BundleID == "" || len(m.Source.BundleID) > 255 ||
		m.ExpectedChunkCount < 1 || m.ExpectedChunkCount > MaxRouteChunks ||
		m.ExpectedPointCount < 1 || m.ExpectedPointCount > MaxRoutePoints ||
		m.FirstRecordedAtUs < -62135596800000000 || m.FirstRecordedAtUs > 253402300799999999 ||
		m.LastRecordedAtUs < m.FirstRecordedAtUs || m.LastRecordedAtUs > 253402300799999999 ||
		(m.ExpectedPayloadManifestSHA256 != "" && !digestPattern.MatchString(m.ExpectedPayloadManifestSHA256)) {
		return Fail(422, "unsupported_payload")
	}
	return nil
}

type RouteState struct {
	RouteID                       string          `json:"routeID"`
	WorkoutID                     string          `json:"workoutID"`
	Source                        Source          `json:"source"`
	DatasetEpoch                  string          `json:"datasetEpoch,omitempty"`
	CollectorEpoch                string          `json:"collectorEpoch,omitempty"`
	IngestStatus                  string          `json:"ingestStatus"`
	ExpectedChunkCount            int             `json:"expectedChunkCount"`
	ExpectedPointCount            int64           `json:"expectedPointCount"`
	CommittedChunks               int             `json:"committedChunks"`
	CommittedPoints               int64           `json:"committedPoints"`
	ManifestSHA256                string          `json:"manifestSHA256"`
	PayloadManifestSHA256         string          `json:"payloadManifestSHA256"`
	ExpectedPayloadManifestSHA256 string          `json:"expectedPayloadManifestSHA256,omitempty"`
	FirstRecordedAtUs             int64           `json:"firstRecordedAtUs"`
	LastRecordedAtUs              int64           `json:"lastRecordedAtUs"`
	ChunkCount                    *int            `json:"chunkCount,omitempty"`
	TotalPoints                   *int64          `json:"totalPoints,omitempty"`
	CommitRevision                *int64          `json:"commitRevision,omitempty"`
	Receipt                       json.RawMessage `json:"receipt,omitempty"`
}

type RouteChunk struct {
	RouteID      string
	ChunkIndex   int
	Points       []RoutePoint
	WireSHA256   string
	PointsSHA256 string
}

type RouteChunkReceipt struct {
	RouteID               string `json:"routeID"`
	ChunkIndex            int    `json:"chunkIndex"`
	IngestStatus          string `json:"ingestStatus"`
	PointCount            int    `json:"pointCount"`
	PayloadSHA256         string `json:"payloadSHA256"`
	ChunkSHA256           string `json:"chunkSHA256"`
	ManifestSHA256        string `json:"manifestSHA256"`
	PayloadManifestSHA256 string `json:"payloadManifestSHA256"`
	CommittedChunks       int    `json:"committedChunks"`
	TotalPoints           int64  `json:"totalPoints"`
}

type RouteSeal struct {
	ProtocolVersion       int    `json:"protocolVersion"`
	ChunkCount            int    `json:"chunkCount"`
	TotalPoints           int64  `json:"totalPoints"`
	ManifestSHA256        string `json:"manifestSHA256"`
	PayloadManifestSHA256 string `json:"payloadManifestSHA256"`
}

func (s RouteSeal) Validate() error {
	if err := validateProtocol(s.ProtocolVersion); err != nil {
		return err
	}
	if s.ChunkCount < 1 || s.ChunkCount > MaxRouteChunks ||
		s.TotalPoints < 1 || s.TotalPoints > MaxRoutePoints ||
		!digestPattern.MatchString(s.ManifestSHA256) ||
		!digestPattern.MatchString(s.PayloadManifestSHA256) {
		return Fail(422, "unsupported_payload")
	}
	return nil
}

type RouteReceipt struct {
	RouteID               string   `json:"routeID"`
	WorkoutID             string   `json:"workoutID"`
	IngestStatus          string   `json:"ingestStatus"`
	ChunkCount            int      `json:"chunkCount"`
	TotalPoints           int64    `json:"totalPoints"`
	ManifestSHA256        string   `json:"manifestSHA256"`
	PayloadManifestSHA256 string   `json:"payloadManifestSHA256"`
	CommitRevision        int64    `json:"commitRevision"`
	AnalysisStatus        string   `json:"analysisStatus"`
	AnalysisJobIDs        []string `json:"analysisJobIDs"`
}

type RouteDeleteRequest struct {
	ProtocolVersion int    `json:"protocolVersion"`
	DatasetEpoch    string `json:"datasetEpoch"`
	CollectorEpoch  string `json:"collectorEpoch"`
	Reason          string `json:"reason,omitempty"`
}

func (r RouteDeleteRequest) Validate() error {
	if err := validateProtocol(r.ProtocolVersion); err != nil {
		return err
	}
	if !ValidID(r.DatasetEpoch) || !ValidID(r.CollectorEpoch) || len(r.Reason) > 128 {
		return Fail(422, "unsupported_payload")
	}
	return nil
}

type RouteDeleteReceipt struct {
	RouteID          string   `json:"routeID"`
	WorkoutID        string   `json:"workoutID,omitempty"`
	IngestStatus     string   `json:"ingestStatus"`
	RouteExisted     bool     `json:"routeExisted"`
	TombstoneCreated bool     `json:"tombstoneCreated"`
	CommitRevision   int64    `json:"commitRevision"`
	AnalysisStatus   string   `json:"analysisStatus"`
	AnalysisJobIDs   []string `json:"analysisJobIDs"`
}

func validateProtocol(v int) error {
	if v != ProtocolVersion {
		return Fail(422, "unsupported_protocol")
	}
	return nil
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

// ParseRouteChunk performs wire/hash/gzip/schema work before any database lock.
// Sequence continuity and route/workout bounds are checked when the storage
// method holds the route lock.
func ParseRouteChunk(r io.Reader) (RouteChunk, error) {
	wire, err := io.ReadAll(io.LimitReader(r, MaxRouteWireBytes+1))
	if err != nil {
		return RouteChunk{}, Fail(400, "body_read_failed")
	}
	if len(wire) > MaxRouteWireBytes {
		return RouteChunk{}, Fail(413, "route_chunk_too_large")
	}
	wireHash := sha256.Sum256(wire)
	gz, err := gzip.NewReader(bytes.NewReader(wire))
	if err != nil {
		return RouteChunk{}, Fail(422, "unsupported_payload")
	}
	defer gz.Close()
	decoded, err := io.ReadAll(io.LimitReader(gz, MaxRouteDecodedBytes+1))
	if len(decoded) > MaxRouteDecodedBytes {
		return RouteChunk{}, Fail(413, "route_chunk_too_large")
	}
	if err != nil {
		return RouteChunk{}, Fail(422, "unsupported_payload")
	}
	lines := bytes.Split(decoded, []byte{'\n'})
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 || len(lines) > MaxRoutePointsPerChunk {
		return RouteChunk{}, Fail(422, "unsupported_payload")
	}
	points := make([]RoutePoint, 0, len(lines))
	for i, line := range lines {
		if len(line) > MaxLineBytes {
			return RouteChunk{}, Fail(413, "route_point_too_large")
		}
		var point RoutePoint
		if err = DecodeJSON(line, &point); err != nil {
			return RouteChunk{}, &Error{Status: 422, Code: "unsupported_payload", EventIndex: &i}
		}
		if err = point.Validate(); err != nil {
			return RouteChunk{}, &Error{Status: 422, Code: "unsupported_payload", EventIndex: &i}
		}
		points = append(points, point)
	}
	_, digest := RoutePointsCanonical(points)
	return RouteChunk{Points: points, WireSHA256: hex.EncodeToString(wireHash[:]), PointsSHA256: hex.EncodeToString(digest)}, nil
}
