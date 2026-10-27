package syncing

import (
	"crypto/sha256"
	"encoding/json"
)

type AssociationRequest struct {
	ProtocolVersion int     `json:"protocolVersion"`
	DatasetID       string  `json:"datasetID"`
	DatasetEpoch    string  `json:"datasetEpoch"`
	CollectorEpoch  string  `json:"collectorEpoch"`
	AssociationID   string  `json:"associationID"`
	SampleType      string  `json:"sampleType"`
	SampleUUID      string  `json:"sampleUUID"`
	WorkoutUUID     string  `json:"workoutUUID"`
	Source          *Source `json:"source"`
}

func (r AssociationRequest) Validate() error {
	if err := validateProtocol(r.ProtocolVersion); err != nil {
		return err
	}
	for _, id := range []string{r.DatasetID, r.DatasetEpoch, r.CollectorEpoch, r.AssociationID, r.SampleUUID, r.WorkoutUUID} {
		if !ValidID(id) {
			return Fail(422, "unsupported_payload")
		}
	}
	if _, ok := Units[r.SampleType]; !ok || r.SampleType == WorkoutType ||
		r.Source == nil || r.Source.BundleID == "" || len(r.Source.BundleID) > 255 {
		return Fail(422, "unsupported_payload")
	}
	return nil
}

func (r AssociationRequest) Canonical() ([]byte, []byte) {
	b, _ := json.Marshal(r)
	h := func() []byte { h := sha256.Sum256(b); return h[:] }()
	return b, h
}

type AssociationReceipt struct {
	AssociationID  string   `json:"associationID"`
	SampleType     string   `json:"sampleType"`
	SampleUUID     string   `json:"sampleUUID"`
	WorkoutUUID    string   `json:"workoutUUID"`
	IngestStatus   string   `json:"ingestStatus"`
	CommitRevision int64    `json:"commitRevision"`
	AnalysisStatus string   `json:"analysisStatus"`
	AnalysisJobIDs []string `json:"analysisJobIDs"`
}
