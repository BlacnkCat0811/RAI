package syncing

import (
	"crypto/sha256"
	"encoding/json"
)

type WorkoutEnrichmentRequest struct {
	ProtocolVersion int            `json:"protocolVersion"`
	DatasetID       string         `json:"datasetID"`
	DatasetEpoch    string         `json:"datasetEpoch"`
	CollectorEpoch  string         `json:"collectorEpoch"`
	EnrichmentID    string         `json:"enrichmentID"`
	WorkoutID       string         `json:"workoutID"`
	Source          *Source        `json:"source"`
	SourceVersion   int64          `json:"sourceVersion"`
	ObservedAtUs    int64          `json:"observedAtUs"`
	TotalDistanceM  *float64       `json:"totalDistanceM,omitempty"`
	TotalEnergyKcal *float64       `json:"totalEnergyKcal,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	RouteAvailable  *bool          `json:"routeAvailable,omitempty"`
}

func (r WorkoutEnrichmentRequest) Validate() error {
	if err := validateProtocol(r.ProtocolVersion); err != nil {
		return err
	}
	for _, id := range []string{r.DatasetID, r.DatasetEpoch, r.CollectorEpoch, r.EnrichmentID, r.WorkoutID} {
		if !ValidID(id) {
			return Fail(422, "unsupported_payload")
		}
	}
	if r.Source == nil || r.Source.BundleID == "" || len(r.Source.BundleID) > 255 ||
		r.SourceVersion < 1 || r.ObservedAtUs < -62135596800000000 || r.ObservedAtUs > 253402300799999999 {
		return Fail(422, "unsupported_payload")
	}
	for _, value := range []*float64{r.TotalDistanceM, r.TotalEnergyKcal} {
		if value != nil && (!finite(*value) || *value < 0) {
			return Fail(422, "unsupported_payload")
		}
	}
	if len(r.Metadata) == 0 && r.TotalDistanceM == nil && r.TotalEnergyKcal == nil && r.RouteAvailable == nil {
		return Fail(422, "unsupported_payload")
	}
	return nil
}

func (r WorkoutEnrichmentRequest) Canonical() ([]byte, []byte) {
	if len(r.Metadata) == 0 {
		r.Metadata = nil
	}
	b, _ := json.Marshal(r)
	return b, func() []byte { h := sha256.Sum256(b); return h[:] }()
}

type WorkoutEnrichmentReceipt struct {
	EnrichmentID   string   `json:"enrichmentID"`
	WorkoutID      string   `json:"workoutID"`
	IngestStatus   string   `json:"ingestStatus"`
	SourceVersion  int64    `json:"sourceVersion"`
	ObservedAtUs   int64    `json:"observedAtUs"`
	CommitRevision int64    `json:"commitRevision"`
	AnalysisStatus string   `json:"analysisStatus"`
	AnalysisJobIDs []string `json:"analysisJobIDs"`
}
