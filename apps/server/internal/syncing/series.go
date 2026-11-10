package syncing

import (
	"crypto/sha256"
	"encoding/json"
	"math"
)

// Quantity-series support is deliberately bounded. These limits are initial
// safety caps, not measured phone-import throughput or production tuning.
const (
	MaxSeriesChunks         = 1000
	MaxSeriesPointsPerChunk = 5000
	MaxSeriesPoints         = 1000000
)

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
