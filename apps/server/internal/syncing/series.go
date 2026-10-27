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

// Quantity-series support is deliberately bounded. These limits are initial
// safety caps, not measured phone-import throughput or production tuning.
const (
	MaxSeriesChunks         = 1000
	MaxSeriesPointsPerChunk = 5000
	MaxSeriesPoints         = 1000000
)

// SeriesRequest binds one series upload to one already committed quantity
// sample. A parent sample owns at most one series identity.
type SeriesRequest struct {
	DatasetID      string `json:"datasetID"`
	DatasetEpoch   string `json:"datasetEpoch"`
	CollectorEpoch string `json:"collectorEpoch"`
	SeriesID       string `json:"seriesID"`
	SampleType     string `json:"sampleType"`
	SampleUUID     string `json:"sampleUUID"`
}

func (r SeriesRequest) Validate() error {
	for _, id := range []string{r.DatasetID, r.DatasetEpoch, r.CollectorEpoch, r.SeriesID, r.SampleUUID} {
		if !ValidID(id) {
			return Fail(422, "unsupported_payload")
		}
	}
	if unit, ok := Units[r.SampleType]; !ok || unit == "" || r.SampleType == WorkoutType {
		return Fail(422, "unsupported_payload")
	}
	return nil
}

// SeriesPoint is one source series entry. The unit is explicit so a wrong
// adapter cannot silently store a value under the parent type's unit. Source
// order is preserved; this type is never sorted or merged by the server.
type SeriesPoint struct {
	StartUs int64   `json:"startUs"`
	EndUs   int64   `json:"endUs"`
	Value   float64 `json:"value"`
	Unit    string  `json:"unit"`
}

func (p SeriesPoint) Validate(sampleType string, parentStartUs, parentEndUs int64) error {
	if p.StartUs > p.EndUs || p.StartUs < -62135596800000000 || p.EndUs > 253402300799999999 ||
		math.IsNaN(p.Value) || math.IsInf(p.Value, 0) || p.Unit != Units[sampleType] {
		return Fail(422, "unsupported_payload")
	}
	if p.StartUs < parentStartUs || p.EndUs > parentEndUs {
		return Fail(422, "unsupported_payload")
	}
	return nil
}

// SeriesChunk is the parsed, network-independent body of one immutable chunk.
type SeriesChunk struct {
	SeriesID     string
	ChunkIndex   int
	Points       []SeriesPoint
	PointCount   int
	WireSHA256   string
	PointsSHA256 string
	MinStartUs   int64
	MaxEndUs     int64
}

// ParseSeriesChunk performs hash/gzip/schema/bounds work before any database
// lock. Chunk identity is the raw compressed byte hash; the manifest digest is
// the canonical point-array digest. Both are retained separately.
func ParseSeriesChunk(r io.Reader, sampleType string, parentStartUs, parentEndUs int64) (SeriesChunk, error) {
	wire, err := io.ReadAll(io.LimitReader(r, MaxWireBytes+1))
	if err != nil {
		return SeriesChunk{}, Fail(400, "body_read_failed")
	}
	if len(wire) > MaxWireBytes {
		return SeriesChunk{}, Fail(413, "series_chunk_too_large")
	}
	wireHash := sha256.Sum256(wire)
	gz, err := gzip.NewReader(bytes.NewReader(wire))
	if err != nil {
		return SeriesChunk{}, Fail(422, "unsupported_payload")
	}
	defer gz.Close()
	decoded, err := io.ReadAll(io.LimitReader(gz, MaxDecodedBytes+1))
	if len(decoded) > MaxDecodedBytes {
		return SeriesChunk{}, Fail(413, "series_chunk_too_large")
	}
	if err != nil {
		return SeriesChunk{}, Fail(422, "unsupported_payload")
	}
	lines := bytes.Split(decoded, []byte{'\n'})
	if len(lines) > 0 && len(lines[len(lines)-1]) == 0 {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return SeriesChunk{}, Fail(422, "unsupported_payload")
	}
	if len(lines) > MaxSeriesPointsPerChunk {
		return SeriesChunk{}, Fail(413, "series_chunk_too_large")
	}
	points := make([]SeriesPoint, 0, len(lines))
	minStart, maxEnd := int64(math.MaxInt64), int64(math.MinInt64)
	for i, line := range lines {
		if len(line) > MaxLineBytes {
			return SeriesChunk{}, Fail(413, "series_point_too_large")
		}
		var p SeriesPoint
		if err = DecodeJSON(line, &p); err != nil {
			return SeriesChunk{}, &Error{Status: 422, Code: "unsupported_payload", EventIndex: &i}
		}
		if err = p.Validate(sampleType, parentStartUs, parentEndUs); err != nil {
			return SeriesChunk{}, &Error{Status: 422, Code: "unsupported_payload", EventIndex: &i}
		}
		if p.StartUs < minStart {
			minStart = p.StartUs
		}
		if p.EndUs > maxEnd {
			maxEnd = p.EndUs
		}
		points = append(points, p)
	}
	_, digest := SeriesPointsCanonical(points)
	return SeriesChunk{
		Points: points, PointCount: len(points),
		WireSHA256: hex.EncodeToString(wireHash[:]), PointsSHA256: hex.EncodeToString(digest),
		MinStartUs: minStart, MaxEndUs: maxEnd,
	}, nil
}

// SeriesPointsCanonical fixes the cross-language chunk identity. The point
// array is marshaled in source order with the struct's field order and no
// whitespace; empty arrays are invalid at the call sites.
func SeriesPointsCanonical(points []SeriesPoint) ([]byte, []byte) {
	b, _ := json.Marshal(points)
	h := sha256.Sum256(b)
	return b, h[:]
}

// AdvanceSeriesManifest is the same fixed-size chained digest as page delivery:
// H(i+1) = SHA256(H(i) || chunkSHA256(i)), starting from 32 zero bytes.
func AdvanceSeriesManifest(previous []byte, chunkDigest []byte) []byte {
	h := sha256.New()
	_, _ = h.Write(previous)
	_, _ = h.Write(chunkDigest)
	return h.Sum(nil)
}

func EmptySeriesManifest() []byte { return make([]byte, sha256.Size) }

// SeriesSeal declares the fixed chunk identity only after every chunk exists.
// The server verifies counts and the digest chain; the client cannot seal a
// subset or ask the server to scan/fabricate missing fragments.
type SeriesSeal struct {
	ChunkCount     int    `json:"chunkCount"`
	TotalPoints    int64  `json:"totalPoints"`
	ManifestSHA256 string `json:"manifestSHA256"`
}

func (s SeriesSeal) Validate() error {
	if s.ChunkCount < 1 || s.ChunkCount > MaxSeriesChunks || s.TotalPoints < 1 || s.TotalPoints > MaxSeriesPoints ||
		!digestPattern.MatchString(s.ManifestSHA256) {
		return Fail(422, "unsupported_payload")
	}
	return nil
}

type SeriesState struct {
	SeriesID        string          `json:"seriesID"`
	SampleType      string          `json:"sampleType"`
	SampleUUID      string          `json:"sampleUUID"`
	SampleStartUs   int64           `json:"sampleStartUs"`
	SampleEndUs     int64           `json:"sampleEndUs"`
	DatasetEpoch    string          `json:"datasetEpoch"`
	CollectorEpoch  string          `json:"collectorEpoch"`
	IngestStatus    string          `json:"ingestStatus"`
	CommittedChunks int             `json:"committedChunks"`
	TotalPoints     int64           `json:"totalPoints"`
	ManifestSHA256  string          `json:"manifestSHA256"`
	ChunkCount      *int            `json:"chunkCount,omitempty"`
	Receipt         json.RawMessage `json:"receipt,omitempty"`
}

type SeriesChunkReceipt struct {
	SeriesID        string `json:"seriesID"`
	ChunkIndex      int    `json:"chunkIndex"`
	IngestStatus    string `json:"ingestStatus"`
	PointCount      int    `json:"pointCount"`
	PayloadSHA256   string `json:"payloadSHA256"`
	ChunkSHA256     string `json:"chunkSHA256"`
	ManifestSHA256  string `json:"manifestSHA256"`
	CommittedChunks int    `json:"committedChunks"`
}

type SeriesReceipt struct {
	SeriesID       string   `json:"seriesID"`
	IngestStatus   string   `json:"ingestStatus"`
	SampleType     string   `json:"sampleType"`
	SampleUUID     string   `json:"sampleUUID"`
	ChunkCount     int      `json:"chunkCount"`
	TotalPoints    int64    `json:"totalPoints"`
	ManifestSHA256 string   `json:"manifestSHA256"`
	CommitRevision int64    `json:"commitRevision"`
	AnalysisStatus string   `json:"analysisStatus"`
	AnalysisJobIDs []string `json:"analysisJobIDs"`
}
