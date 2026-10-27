package syncing

import (
	"context"
	"encoding/json"
)

type Metadata struct {
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
type BatchState struct {
	Envelope Envelope        `json:"envelope"`
	State    string          `json:"ingestStatus"`
	Receipt  json.RawMessage `json:"receipt,omitempty"`
}

// PageState confirms delivery of the declared fragments, never HealthKit query
// completeness or the client's local contiguous anchor position.
type PageState struct {
	PageID             string          `json:"pageID"`
	StreamID           string          `json:"streamID"`
	DatasetEpoch       string          `json:"datasetEpoch"`
	CollectorEpoch     string          `json:"collectorEpoch"`
	QueryFingerprint   string          `json:"queryFingerprint"`
	FragmentCount      int             `json:"fragmentCount"`
	FirstClientSeq     int64           `json:"firstClientSeq"`
	CommittedFragments int             `json:"committedFragments"`
	IngestStatus       string          `json:"ingestStatus"`
	Receipt            json.RawMessage `json:"receipt,omitempty"`
}
type PageReceipt struct {
	PageID           string `json:"pageID"`
	StreamID         string `json:"streamID"`
	DatasetEpoch     string `json:"datasetEpoch"`
	CollectorEpoch   string `json:"collectorEpoch"`
	QueryFingerprint string `json:"queryFingerprint"`
	IngestStatus     string `json:"ingestStatus"`
	FragmentCount    int    `json:"fragmentCount"`
	FirstClientSeq   int64  `json:"firstClientSeq"`
	LastClientSeq    int64  `json:"lastClientSeq"`
	LastBatchID      string `json:"lastBatchID"`
	ManifestSHA256   string `json:"manifestSHA256"`
	CommitRevision   int64  `json:"commitRevision"`
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
	RegisterStream(context.Context, Principal, StreamRequest) (StreamState, error)
	Stream(context.Context, Principal, string) (StreamState, error)
	RegisterBatch(context.Context, Principal, Envelope) (BatchState, error)
	Batch(context.Context, Principal, string) (BatchState, error)
	Page(context.Context, Principal, string) (PageState, error)
	RegisterSeries(context.Context, Principal, SeriesRequest) (SeriesState, error)
	Series(context.Context, Principal, string) (SeriesState, error)
	CommitSeriesChunk(context.Context, Principal, SeriesChunk, UploadLease) (json.RawMessage, error)
	SealSeries(context.Context, Principal, string, SeriesSeal) (json.RawMessage, error)
	RegisterWorkoutPackage(context.Context, Principal, WorkoutPackageRequest) (WorkoutPackageState, error)
	WorkoutPackage(context.Context, Principal, string) (WorkoutPackageState, error)
	CommitWorkoutPackageChunk(context.Context, Principal, WorkoutDetailChunk, UploadLease) (json.RawMessage, error)
	SealWorkoutPackage(context.Context, Principal, string, WorkoutPackageSeal) (json.RawMessage, error)
	CommitUpload(context.Context, Principal, Envelope, []Event, UploadLease) (json.RawMessage, error)
	Job(context.Context, Principal, string) (any, error)
}
