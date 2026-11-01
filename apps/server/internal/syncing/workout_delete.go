package syncing

// WorkoutDeleteRequest is the explicit v3 deletion command. Deletion is never
// inferred from an absent sample or an empty query: losing HealthKit read
// permission must not erase server facts.
type WorkoutDeleteRequest struct {
	ProtocolVersion int    `json:"protocolVersion"`
	DatasetID       string `json:"datasetID"`
	DatasetEpoch    string `json:"datasetEpoch"`
	CollectorEpoch  string `json:"collectorEpoch"`
	WorkoutID       string `json:"workoutID"`
	Reason          string `json:"reason,omitempty"`
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
	if r.ProtocolVersion != 3 || !ValidID(r.DatasetID) || !ValidID(r.DatasetEpoch) ||
		!ValidID(r.CollectorEpoch) || !ValidID(r.WorkoutID) || len(r.Reason) > 128 {
		return Fail(422, "unsupported_payload")
	}
	return nil
}
