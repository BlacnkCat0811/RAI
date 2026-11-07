// Package research implements the synthetic E10 prospective research utility
// layer: frozen prediction snapshots, known-at and athlete-disjoint splits,
// independent outcome linkage, cluster-aware summaries, withdrawal history and
// study addendum gating.
//
// Everything in this package is deterministic and operates on caller-supplied
// synthetic values. It performs no production export, database access or
// network I/O. A prediction is never treated as an observed outcome, and a
// revised model output can never be presented as a historical prediction.
package research

import "time"

// Prediction states used by the research workflow. An empty State is treated
// as an ordinary (non-withdrawn) prediction for backward compatibility.
const (
	PredictionStateProspective = "prospective"
	PredictionStateWithdrawn   = "withdrawn"
)

// ProspectiveSnapshot is an immutable, known-at-bound prediction snapshot.
// AsOf is the information cutoff the snapshot claims; KnownAt is when the
// snapshot actually became available. KnownAt must not be after AsOf.
type ProspectiveSnapshot struct {
	DatasetID    string
	Model        string
	ManifestSHA  string
	AsOf         time.Time
	KnownAt      time.Time
	CohortID     string
	Athletes     int
	Observations []Prediction
}

// Prediction is one prospective model prediction for an athlete/cluster at a
// known prediction time. FeaturesKnownAt records when every model input was
// known; it must not be after the snapshot AsOf. WithdrawnAt is append-only
// governance history: it is set once and never cleared by this package.
type Prediction struct {
	AthleteKey      string
	ClusterKey      string
	PredictedAt     time.Time
	HorizonDays     int
	Value           *float64
	State           string
	Version         string
	Mode            string
	FeaturesKnownAt time.Time
	WithdrawnAt     *time.Time
	// InputWorkoutIDs and InputFrom/InputTo describe the prediction input
	// window. They are optional; when present, an outcome that reuses the same
	// workout or overlaps the input window is not independent and is excluded.
	InputWorkoutIDs []string
	InputFrom       *time.Time
	InputTo         *time.Time
}

// Outcome is one observed outcome. Censored marks an outcome window without a
// usable value (for example, no event before the horizon); Value is nil in
// that case. Independent distinguishes an outcome source that does not reuse
// the prediction input window.
type Outcome struct {
	AthleteKey  string
	ClusterKey  string
	ObservedAt  time.Time
	Value       *float64
	Censored    bool
	Source      string
	Independent bool
	// WorkoutID and WindowFrom/WindowTo identify the outcome observation
	// window. An independent outcome must not reuse a prediction input workout
	// or an overlapping time window.
	WorkoutID  string
	WindowFrom *time.Time
	WindowTo   *time.Time
}

// StudyAddendum is the per-model research approval record. The gate stays
// closed until every required field is populated and reviewed.
type StudyAddendum struct {
	CohortID                string
	Purpose                 string
	Model                   string
	Version                 string
	PrimaryOutcome          string
	OutcomeSource           string
	ObservationFrom         time.Time
	ObservationTo           time.Time
	AcceptanceThreshold     float64
	SampleSizeJustification string
	MinDetectableEffect     float64
	Alpha                   float64
	Power                   float64
	PreregisteredAt         time.Time
	Reviewer                string
	AuthorizationBasis      string
	WithdrawalProcedure     string
	StoppingRule            string
	PrivacyApproved         bool
	ExpiresAt               time.Time
}
