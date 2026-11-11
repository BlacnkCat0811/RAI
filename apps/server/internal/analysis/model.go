// Package analysis holds the shared job and input contracts of the v2 model
// stack worker. The legacy workout-bundle / training_state algorithms were
// retired with schema 53. No database, HTTP, filesystem or wall-clock access.
package analysis

import (
	"errors"
	"time"

	"rai/server/internal/modelmanifest"
	rc "rai/server/internal/recovery"
	ve "rai/server/internal/vo2maxexternal"
	wf "rai/server/internal/workoutfeatures"
)

type Lease struct {
	ID               string
	DatasetID        string
	Kind             string
	ScopeKey         string
	TargetGeneration int64
	FencingToken     int64
	Payload          []byte
}

// Metric is a named scalar as product readers expose it.
type Metric struct {
	Day       string         `json:"day,omitempty"`
	Name      string         `json:"metric"`
	Algorithm string         `json:"algorithm"`
	Version   string         `json:"algorithmVersion"`
	Value     *float64       `json:"value"`
	Unit      string         `json:"unit"`
	Quality   string         `json:"quality"`
	Details   map[string]any `json:"details"`
}

// Snapshot carries the bounded fact inputs the model stack loads in one
// repeatable-read transaction.
type Snapshot struct {
	RecoveryInput *rc.Input               `json:"recoveryInput,omitempty"`
	ExternalInput *ve.ExternalVO2Snapshot `json:"externalInput,omitempty"`
	FeatureInput  *wf.RawWorkoutSnapshot  `json:"featureInput,omitempty"`
	Generation    int64                   `json:"generation"`
	Revision      int64                   `json:"revision"`
	From          time.Time               `json:"from"`
	Through       time.Time               `json:"through"`
	InputVersion  string                  `json:"inputVersion"`
}

type Failure struct {
	Code      string
	Retryable bool
}

func (e *Failure) Error() string { return e.Code }
func Invalid() error             { return &Failure{Code: "analysis_invalid_input"} }

// Algorithm names the handler of a job kind; retired kinds are unsupported.
func Algorithm(kind string) (string, error) {
	if kind == modelmanifest.StackJobKind {
		return "rai.model_stack", nil
	}
	return "", &Failure{Code: "analysis_unsupported_algorithm"}
}
func ErrorCode(err error) (string, bool) {
	var e *Failure
	if errors.As(err, &e) {
		return e.Code, e.Retryable
	}
	return "analysis_storage_transient", true
}
