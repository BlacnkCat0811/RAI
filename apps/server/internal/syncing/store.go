package syncing

import (
	"context"
	"encoding/json"
)

type Metadata struct {
	DeviceID       string `json:"deviceID"`
	CollectorEpoch string `json:"collectorEpoch"`
	DatasetStatus  string `json:"datasetStatus"`
	DatasetID      string `json:"datasetID"`
	DatasetEpoch   string `json:"datasetEpoch"`
	ServiceID      string `json:"serviceID"`
	Timezone       string `json:"timezone"`
	CommitRevision int64  `json:"commitRevision"`
}
type StreamState struct {
	StreamID         string  `json:"streamID"`
	CollectorEpoch   string  `json:"collectorEpoch"`
	SampleType       string  `json:"sampleType"`
	QueryFingerprint string  `json:"queryFingerprint"`
	LastClientSeq    int64   `json:"lastClientSeq"`
	LastBatchID      *string `json:"lastBatchID"`
}

type Counts struct {
	Inserted   int `json:"inserted"`
	Unchanged  int `json:"unchanged"`
	Deleted    int `json:"deleted"`
	Tombstoned int `json:"tombstoned"`
	Suppressed int `json:"suppressed"`
}
type Receipt struct {
	BatchID        string   `json:"batchID"`
	IngestStatus   string   `json:"ingestStatus"`
	DatasetEpoch   string   `json:"datasetEpoch"`
	ClientSeq      int64    `json:"clientSeq"`
	PayloadSHA256  string   `json:"payloadSHA256"`
	CommitRevision int64    `json:"commitRevision"`
	Counts         Counts   `json:"counts"`
	AnalysisStatus string   `json:"analysisStatus"`
	AnalysisJobIDs []string `json:"analysisJobIDs"`
}
type Store interface {
	Ready(context.Context) error
	Authenticate(context.Context, string) (Principal, error)
	Admit(context.Context, Principal, AdmissionLane) error
	AcquireUpload(context.Context, Principal) (UploadLease, error)
	ReleaseUpload(context.Context, Principal, UploadLease) error
	Metadata(context.Context, Principal) (Metadata, error)
	Changes(context.Context, Principal, string, int) (ChangePage, error)
	AcceptWorkoutIngest(context.Context, Principal, WorkoutIngestEnvelope, []byte) (json.RawMessage, error)
	WorkoutIngest(context.Context, Principal, string) (WorkoutIngestStatus, error)
	ProcessNextWorkoutIngest(context.Context) (WorkoutIngestOutcome, error)
	RecoverExpiredWorkoutIngests(context.Context) (int, error)
	CommitDaily(context.Context, Principal, []byte, UploadLease) (json.RawMessage, error)
	CommitVO2(context.Context, Principal, []byte, UploadLease) (json.RawMessage, error)
	VO2Coverage(context.Context, Principal) (VO2Coverage, error)
	DailyCoverage(context.Context, Principal) ([]DailyCoverage, error)
	Job(context.Context, Principal, string) (any, error)
}
