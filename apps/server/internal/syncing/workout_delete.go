package syncing

// WorkoutDeleteRequest is the explicit deletion command for management and
// server paths. It is no longer a client wire payload: deletion is never
// inferred from an absent sample or an empty query.
type WorkoutDeleteRequest struct {
	DatasetID      string `json:"datasetID"`
	DatasetEpoch   string `json:"datasetEpoch"`
	CollectorEpoch string `json:"collectorEpoch"`
	WorkoutID      string `json:"workoutID"`
	Reason         string `json:"reason,omitempty"`
}

// WorkoutDeleteReceipt describes the original durable deletion. Replays return
// the same receipt and revision, even after unrelated dataset commits.
type WorkoutDeleteReceipt struct {
	WorkoutID      string `json:"workoutID"`
	IngestStatus   string `json:"ingestStatus"`
	AlreadyDeleted bool   `json:"alreadyDeleted"`
	CommitRevision int64  `json:"commitRevision"`
}

func (r WorkoutDeleteRequest) Validate() error {
	if !ValidID(r.DatasetID) || !ValidID(r.DatasetEpoch) ||
		!ValidID(r.CollectorEpoch) || !ValidID(r.WorkoutID) || len(r.Reason) > 128 {
		return Fail(422, "unsupported_payload")
	}
	return nil
}
