package athletemodel

import (
	"errors"

	rf "rai/server/internal/runfitness"
)

// ErrRunningFitnessNotFound: no published state or operation with that key.
var ErrRunningFitnessNotFound = errors.New("running_fitness_not_found")

// ErrReanalysisConflict: the idempotency key was used for a different request.
var ErrReanalysisConflict = errors.New("reanalysis_idempotency_conflict")

// ErrReanalysisInvalid: range outside the allowed window or unknown Workouts.
var ErrReanalysisInvalid = errors.New("reanalysis_invalid")

// RunningFitnessDay is one point of the running-fitness history: the daily
// long-term line and the single-run points of that day.
type RunningFitnessDay struct {
	Date                string           `json:"date"`
	Status              string           `json:"status"`
	Freshness           string           `json:"freshness"`
	Overall             rf.Overall       `json:"overall"`
	ReferenceDailyScore *float64         `json:"referenceDailyScore"`
	Params              rf.Params        `json:"params"`
	Singles             []rf.SingleBrief `json:"singleRuns"`
	PublicationID       string           `json:"publicationID"`
	PublishedAt         int64            `json:"publishedAt"`
	ConfigHash          string           `json:"configHash"`
}

type RunningFitnessHistory struct {
	SchemaVersion    string              `json:"schemaVersion"`
	View             string              `json:"view"`
	AlgorithmVersion string              `json:"algorithmVersion"`
	RatingPolicy     string              `json:"ratingPolicy"`
	StackID          string              `json:"stackID"`
	ManifestSHA      string              `json:"manifestSHA256"`
	From             string              `json:"from"`
	To               string              `json:"to"`
	Days             []RunningFitnessDay `json:"days"`
	NextCursor       *string             `json:"nextCursor"`
}

// RunningFitnessEvidence lists the evidence of one published day state.
type RunningFitnessEvidence struct {
	SchemaVersion string           `json:"schemaVersion"`
	Date          string           `json:"date"`
	Dimension     string           `json:"dimension,omitempty"`
	Evidence      []rf.EvidenceRef `json:"evidence"`
	Dimensions    rf.Dimensions    `json:"dimensions"`
	Support       rf.Support       `json:"support"`
	PublicationID string           `json:"publicationID"`
}

// ReanalysisRequest asks the active stack to recompute running fitness for
// some Workouts or a local date range. Algorithm and reference policy can
// only name the active release; a user cannot select arbitrary code.
type ReanalysisRequest struct {
	WorkoutIDs      []string `json:"workoutIDs,omitempty"`
	From            string   `json:"from,omitempty"`
	To              string   `json:"to,omitempty"`
	View            string   `json:"view"`
	ReferencePolicy string   `json:"referencePolicy"`
	Algorithm       string   `json:"algorithmVersion"`
}

type ReanalysisOperation struct {
	ID               string `json:"operationID"`
	Status           string `json:"status"` // queued | running | publishing | completed | failed | cancelled
	View             string `json:"view"`
	StackID          string `json:"stackID"`
	AlgorithmVersion string `json:"algorithmVersion"`
	ReferencePolicy  string `json:"referencePolicy"`
	From             string `json:"from"`
	Through          string `json:"through"`
	TotalDays        int    `json:"totalDays"`
	CompletedDays    int    `json:"completedDays"`
	FailedDays       int    `json:"failedDays"`
	CreatedAt        int64  `json:"createdAt"`
	ErrorCode        string `json:"errorCode,omitempty"`
}
