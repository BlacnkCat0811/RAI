package lactatethreshold

// Anchor is the frozen running-fitness 40 minute working point of the
// evaluation day. The caller maps it from the running-fitness state; this
// package never fits a second pace curve.
type Anchor struct {
	SpeedMPS     float64 `json:"speedMPS"`
	SpeedLowMPS  float64 `json:"speedLowMPS,omitempty"`
	SpeedHighMPS float64 `json:"speedHighMPS,omitempty"`
	Speed60MPS   float64 `json:"speed60MPS"`
	// Basis: prior_based | estimated | field_calibrated | conflict.
	Basis       string `json:"basis"`
	Freshness   string `json:"freshness"`
	EvidenceDay string `json:"evidenceDay,omitempty"`
	InputHash   string `json:"inputHash"`
}

// Reference is the HR reference in effect at a Workout's start. It is used
// for the candidate prefilter, the slope prior and diagnostics only; it never
// produces the threshold heart rate.
type Reference struct {
	MaxHR  float64 `json:"maxHR"`
	RestHR float64 `json:"restHR"`
}

// Window is one neutral 180 second feature window (workout features
// rai.run_fitness_features/1). Means are over seconds with both HR and speed.
type Window struct {
	Kind         string   `json:"kind"`
	Start        int64    `json:"startUs"`
	End          int64    `json:"endUs"`
	Seconds      float64  `json:"seconds"`
	RunningAt    float64  `json:"runningSecondsBefore"`
	Speed        float64  `json:"meanSpeedMPS"`
	FlatSpeed    *float64 `json:"flatSpeedMPS"`
	MaxAbsGrade  *float64 `json:"maxAbsGrade"`
	GradeSupport float64  `json:"gradeCoverage"`
	HR           float64  `json:"meanHR"`
	HRTrend      float64  `json:"hrTrendBPMPerMinute"`
	SpeedCV      float64  `json:"speedCV"`
	Coverage     float64  `json:"coverage"`
	SpeedEpoch   string   `json:"speedEpoch"`
	HREpoch      string   `json:"hrEpoch"`
}

// Span is an absolute main-segment interval in UTC microseconds.
type Span struct {
	Start int64 `json:"startUs"`
	End   int64 `json:"endUs"`
}

type Workout struct {
	ID            string     `json:"workoutID"`
	Day           string     `json:"day"`
	Start         int64      `json:"startUs"`
	End           int64      `json:"endUs"`
	SourceEpoch   string     `json:"sourceEpoch"`
	Indoor        *bool      `json:"indoor"`
	TemperatureC  *float64   `json:"temperatureC"`
	Humidity      *float64   `json:"humidity"`
	DistanceCheck string     `json:"distanceCheck"`
	Reference     *Reference `json:"reference,omitempty"`
	Windows       []Window   `json:"windows"`
	// TimeTrial is a user-confirmed maximal 30 minute time-trial main segment
	// (protocol rai.tt30/1); it feeds method C.
	TimeTrial *Span `json:"timeTrial,omitempty"`
}

type Input struct {
	DatasetID  string `json:"datasetID"`
	Date       string `json:"date"`
	Generation int64  `json:"generation"`
	AsOf       int64  `json:"asOf"`
	View       string `json:"view"`
	// FitnessInputHash identifies the running-fitness state the anchor came
	// from, also when that state offers no anchor.
	FitnessInputHash string    `json:"fitnessInputHash"`
	Anchor           *Anchor   `json:"anchor"`
	Workouts         []Workout `json:"workouts"`
}

type Range struct {
	Low  float64 `json:"low"`
	High float64 `json:"high"`
}

type Pace struct {
	SpeedMPS         float64 `json:"speedMPS"`
	PaceSecondsPerKm float64 `json:"paceSecondsPerKm"`
	Basis            string  `json:"basis"`
	Freshness        string  `json:"freshness"`
	SpeedModelRange  *Range  `json:"speedModelRange"`
	// TrainingSpeedRange is the conservative threshold-session pace band
	// [v(3600), v(2400)] (plan R8). Display stays at v40.
	TrainingSpeedRange    Range    `json:"trainingSpeedRange"`
	DefinitionSensitivity [2]int   `json:"definitionSensitivitySeconds"`
	Speed60MPS            *float64 `json:"speed60MPS"`
	EvidenceDay           string   `json:"evidenceDay,omitempty"`
}

type HeartRate struct {
	BPM *float64 `json:"bpm"`
	// Basis: collecting | preliminary | supported | conflict.
	Basis                    string   `json:"basis"`
	Method                   string   `json:"method,omitempty"`
	Definition               string   `json:"definition,omitempty"`
	Freshness                string   `json:"freshness"`
	ModelRange               *Range   `json:"modelRange"`
	RangePartiallyIdentified bool     `json:"rangePartiallyIdentified"`
	IndependentDays          int      `json:"independentDays"`
	EffectiveDays            float64  `json:"effectiveDays"`
	SupportSeconds           float64  `json:"supportSeconds"`
	EvidenceDay              string   `json:"evidenceDay,omitempty"`
	ObservedHRR              *float64 `json:"observedHRR"`
	LOOMaxDelta              float64  `json:"looMaxDeltaBPM"`
	Reasons                  []string `json:"reasonCodes"`
}

type TrainingUse struct {
	PaceEligible                      bool     `json:"paceEligible"`
	HeartRateEligible                 bool     `json:"heartRateEligible"`
	AutomaticIntensityIncreaseAllowed bool     `json:"automaticIntensityIncreaseAllowed"`
	Reasons                           []string `json:"reasonCodes"`
}

// DayEvidence is one independent day used by the heart-rate estimate.
type DayEvidence struct {
	Day            string   `json:"day"`
	AgeDays        int      `json:"ageDays"`
	Method         string   `json:"method"`
	BPM            float64  `json:"bpm"`
	Weight         float64  `json:"weight"`
	Support        float64  `json:"support"`
	SupportSeconds float64  `json:"supportSeconds"`
	WorkoutIDs     []string `json:"workoutIDs"`
}

// WorkoutDiagnostic records why a Workout did or did not contribute.
type WorkoutDiagnostic struct {
	WorkoutID      string   `json:"workoutID"`
	Day            string   `json:"day"`
	Status         string   `json:"status"` // used | diagnostic | excluded
	Method         string   `json:"method,omitempty"`
	BPM            *float64 `json:"bpm,omitempty"`
	Slope          *float64 `json:"slopeBPMPerLogSpeed,omitempty"`
	SupportSeconds float64  `json:"supportSeconds"`
	Reasons        []string `json:"reasonCodes"`
}

type Result struct {
	Schema       string              `json:"schemaVersion"`
	Algorithm    string              `json:"algorithm"`
	Version      string              `json:"algorithmVersion"`
	ConfigHash   string              `json:"configHash"`
	DatasetID    string              `json:"datasetID"`
	Generation   int64               `json:"generation"`
	Date         string              `json:"date"`
	AsOf         int64               `json:"asOf"`
	View         string              `json:"view"`
	Definition   string              `json:"definition"`
	Status       string              `json:"status"`       // collecting | prior_based_proxy | estimated_proxy | field_calibrated_proxy | conflict
	UpdateStatus string              `json:"updateStatus"` // confirmed | pending_confirmation | none
	Pace         *Pace               `json:"pace"`
	HeartRate    HeartRate           `json:"heartRate"`
	TrainingUse  TrainingUse         `json:"trainingUse"`
	Evidence     []DayEvidence       `json:"evidence"`
	Diagnostics  []WorkoutDiagnostic `json:"workouts"`
	Reasons      []string            `json:"reasonCodes"`
	AnchorHash   string              `json:"fitnessInputHash"`
	InputHash    string              `json:"inputHash"`
}
