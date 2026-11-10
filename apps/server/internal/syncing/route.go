package syncing

import (
	"crypto/sha256"
	"encoding/json"
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

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
