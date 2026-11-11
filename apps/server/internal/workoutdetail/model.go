// Package workoutdetail owns read-time presentation contracts for one Workout.
// It is a pure package: storage supplies already-validated facts, and the
// functions here never read a clock, database, filesystem or network.
package workoutdetail

// Workout is the fact-first header of the native detail page. Derived fields
// stay nil (or analysisStatus=pending) until an analysis publication exists;
// they are never replaced by a zero that reads like a measured score.
type Workout struct {
	WorkoutID    string   `json:"workoutID"`
	StartedAtUs  int64    `json:"startedAtUs"`
	EndedAtUs    int64    `json:"endedAtUs"`
	DurationS    float64  `json:"durationS"`
	ElapsedS     float64  `json:"elapsedS"`
	DistanceM    *float64 `json:"distanceM"`
	ActivityType int64    `json:"activityType"`

	ActiveEnergyKcal             *float64 `json:"activeEnergyKcal"`
	AveragePaceSPerKm            *float64 `json:"averagePaceSPerKm"`
	AverageHeartRateBpm          *float64 `json:"averageHeartRateBpm"`
	AverageCadenceSpm            *float64 `json:"averageCadenceSpm"`
	AveragePowerW                *float64 `json:"averagePowerW"`
	AverageStrideLengthM         *float64 `json:"averageStrideLengthM"`
	AverageVerticalOscillationCm *float64 `json:"averageVerticalOscillationCm"`
	AverageGroundContactTimeMs   *float64 `json:"averageGroundContactTimeMs"`

	Indoor         *bool  `json:"indoor"`
	SessionType    string `json:"sessionType"`
	SourceCategory string `json:"sourceCategory"`

	AnalysisStatus    string   `json:"analysisStatus"`
	AnalysisAvailable bool     `json:"analysisAvailable"`
	ModelVersion      string   `json:"modelVersion"`
	AssessmentStatus  string   `json:"assessmentStatus"`
	QualityScore      *float64 `json:"qualityScore"`

	HeartRateCoverage    *float64 `json:"heartRateCoverage"`
	HeartRateSampleCount int      `json:"heartRateSampleCount"`
	RouteQuality         *float64 `json:"routeQuality"`
	RoutePointCount      int      `json:"routePointCount"`
	SourceConfidence     *float64 `json:"sourceConfidence"`
	StimulusConfidence   *float64 `json:"stimulusConfidence"`
	StimulusType         string   `json:"stimulusType"`

	MissingReasons   []string `json:"missingReasons"`
	ExplanationCodes []string `json:"explanationCodes"`
}

// Environment is the raw HealthKit observation plus the display-only
// correction. correction/adjusted stay null whenever any input is unknown.
type Environment struct {
	WeatherCondition *int        `json:"weatherCondition"`
	TemperatureC     *float64    `json:"temperatureC"`
	HumidityPercent  *float64    `json:"humidityPercent"`
	Status           string      `json:"status"`
	PolicyVersion    string      `json:"policyVersion"`
	ReasonCodes      []string    `json:"reasonCodes"`
	Correction       *Correction `json:"correction"`
	Adjusted         *Adjusted   `json:"adjusted"`
}

type Correction struct {
	Environment *float64 `json:"environment"`
	Elevation   *float64 `json:"elevation"`
	Total       *float64 `json:"total"`
}

type Adjusted struct {
	DurationS  *float64 `json:"durationS"`
	PaceSPerKm *float64 `json:"paceSPerKm"`
}

type Summary struct {
	AveragePaceSPerKm       *float64 `json:"averagePaceSPerKm"`
	GradeAdjustedPaceSPerKm *float64 `json:"gradeAdjustedPaceSPerKm"`
	CoveredSplits           int      `json:"coveredSplits"`
	TotalSplits             int      `json:"totalSplits"`
	ElevationGainM          *float64 `json:"elevationGainM"`
	RouteQuality            *float64 `json:"routeQuality"`
}

type TrainingLoad struct {
	Load           *float64 `json:"load"`
	Intensity      *float64 `json:"intensity"`
	Confidence     *float64 `json:"confidence"`
	Source         string   `json:"source"`
	StimulusType   string   `json:"stimulusType"`
	RPE            *float64 `json:"rpe"` // Actual reported effort; never populated from a plan target.
	MissingReasons []string `json:"missingReasons"`
}

type ZoneExposure struct {
	Zone            string  `json:"zone"`
	DurationSeconds float64 `json:"durationSeconds"`
	DistanceMeters  float64 `json:"distanceMeters"`
	Fraction        float64 `json:"fraction"`
}

type StimulusScore struct {
	StimulusType      string  `json:"stimulusType"`
	QualifyingSeconds float64 `json:"qualifyingSeconds"`
	TargetSeconds     float64 `json:"targetSeconds"`
	Score             float64 `json:"score"`
	Confidence        float64 `json:"confidence"`
}

type ZoneSummary struct {
	ZoneProfileID          string          `json:"zoneProfileID"`
	StimulusType           string          `json:"stimulusType"`
	SecondaryStimulusTypes []string        `json:"secondaryStimulusTypes"`
	StimulusConfidence     *float64        `json:"stimulusConfidence"`
	HeartRateCoverage      float64         `json:"heartRateCoverage"`
	PaceCoverage           float64         `json:"paceCoverage"`
	HeartRateExposure      []ZoneExposure  `json:"heartRateExposure"`
	PaceExposure           []ZoneExposure  `json:"paceExposure"`
	StimulusScores         []StimulusScore `json:"stimulusScores"`
	ExplanationCodes       []string        `json:"explanationCodes"`
	PaceZoneStatus         string          `json:"paceZoneStatus"`
}

type RoutePoint struct {
	Latitude    float64  `json:"latitude"`
	Longitude   float64  `json:"longitude"`
	ElevationM  *float64 `json:"elevationM"`
	SpeedMPS    *float64 `json:"speedMPS"`
	TimestampUs int64    `json:"timestampUs"`
}

type KilometerMarker struct {
	Kilometer int     `json:"kilometer"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	OffsetS   float64 `json:"offsetS"`
}

type Route struct {
	Points           []RoutePoint      `json:"points"`
	KilometerMarkers []KilometerMarker `json:"kilometerMarkers"`
	PointCount       int               `json:"pointCount"`
}

// Override is the real manual-adjustment evidence this dataset has: an
// execution whose immutable session snapshot was edited by the runner. Fields
// the legacy product has no source for stay absent rather than invented.
type Override struct {
	Revision                int64    `json:"revision"`
	EligibilityOverride     *string  `json:"eligibilityOverride"`
	StatisticsParticipation *string  `json:"statisticsParticipation"`
	SessionType             *string  `json:"sessionType"`
	PerformanceProtocol     *string  `json:"performanceProtocol"`
	RPE                     *float64 `json:"rpe"`
	ConditionTags           []string `json:"conditionTags"`
	Note                    *string  `json:"note"`
	CreatedAt               string   `json:"createdAt"`
	Source                  string   `json:"source"`
}

type FeatureAnalysis struct {
	Schema            string   `json:"schemaVersion"`
	Algorithm         string   `json:"algorithm"`
	Version           string   `json:"algorithmVersion"`
	StackID           string   `json:"stackID"`
	PublishedAtUs     int64    `json:"publishedAtUs"`
	ActiveSeconds     *float64 `json:"activeSeconds"`
	HeartRateCoverage float64  `json:"heartRateCoverage"`
	SpeedCoverage     float64  `json:"speedCoverage"`
	RouteCoverage     float64  `json:"routeCoverage"`
	Reasons           []string `json:"reasonCodes"`
}

// ModelQualification says whether the active model stack admits this run as
// running-ability evidence and as a known training load, with the reasons when
// it does not. Status is eligible, conditional or ineligible; training load is
// "pending" until the stack has computed this run's load (eligible = complete
// heart-rate tier, conditional = estimated/approximate). Admission is
// necessary, not sufficient: running ability also keeps only the fastest
// recent efforts per duration.
type ModelQualification struct {
	RunningAbility Qualification `json:"runningAbility"`
	TrainingLoad   Qualification `json:"trainingLoad"`
}
type Qualification struct {
	Status      string   `json:"status"`
	ReasonCodes []string `json:"reasonCodes"`
}

type Detail struct {
	Workout            Workout             `json:"workout"`
	Analysis           *FeatureAnalysis    `json:"analysis,omitempty"`
	ModelQualification *ModelQualification `json:"modelQualification,omitempty"`
	Environment        *Environment        `json:"environment"`
	Summary            Summary             `json:"summary"`
	TrainingLoad       *TrainingLoad       `json:"trainingLoad"`
	ZoneSummary        *ZoneSummary        `json:"zoneSummary"`
	Route              Route               `json:"route"`
	Override           *Override           `json:"override"`
}

type MetricPoint struct {
	OffsetS float64 `json:"offsetS"`
	Value   float64 `json:"value"`
}

type MetricSeries struct {
	Unit         string        `json:"unit"`
	TotalSamples int           `json:"totalSamples"`
	Points       []MetricPoint `json:"points"`
}

type SeriesBundle struct {
	Pace                MetricSeries `json:"pace"`
	HeartRate           MetricSeries `json:"heartRate"`
	Cadence             MetricSeries `json:"cadence"`
	StrideLength        MetricSeries `json:"strideLength"`
	Power               MetricSeries `json:"power"`
	GroundContactTime   MetricSeries `json:"groundContactTime"`
	VerticalOscillation MetricSeries `json:"verticalOscillation"`
	Elevation           MetricSeries `json:"elevation"`
}

type SplitGrade struct {
	PolicyVersion           string   `json:"policyVersion"`
	ElevationChangeMeters   *float64 `json:"elevationChangeMeters"`
	GainMeters              *float64 `json:"gainMeters"`
	LossMeters              *float64 `json:"lossMeters"`
	GradePercent            *float64 `json:"gradePercent"`
	GradeAdjustedPaceSPerKm *float64 `json:"gradeAdjustedPaceSPerKm"`
	ReasonCodes             []string `json:"reasonCodes"`
}

type Split struct {
	Index               int         `json:"index"`
	DistanceM           float64     `json:"distanceM"`
	DurationS           float64     `json:"durationS"`
	AveragePaceSPerKm   float64     `json:"averagePaceSPerKm"`
	AverageHeartRateBpm *float64    `json:"averageHeartRateBpm"`
	AverageCadenceSpm   *float64    `json:"averageCadenceSpm"`
	Grade               *SplitGrade `json:"grade"`
}

type GradeSummary struct {
	PolicyVersion           string   `json:"policyVersion"`
	CoveredSplits           int      `json:"coveredSplits"`
	TotalSplits             int      `json:"totalSplits"`
	GainMeters              *float64 `json:"gainMeters"`
	LossMeters              *float64 `json:"lossMeters"`
	GradeAdjustedPaceSPerKm *float64 `json:"gradeAdjustedPaceSPerKm"`
	ReasonCodes             []string `json:"reasonCodes"`
}

type Segment struct {
	Index               int      `json:"index"`
	Label               string   `json:"label"`
	Purpose             string   `json:"purpose"`
	GoalType            string   `json:"goalType"`
	GoalMeters          *float64 `json:"goalMeters"`
	GoalSeconds         *float64 `json:"goalSeconds"`
	AlertKind           string   `json:"alertKind"`
	AlertLower          *float64 `json:"alertLower"`
	AlertUpper          *float64 `json:"alertUpper"`
	StartOffsetSeconds  float64  `json:"startOffsetSeconds"`
	DurationSeconds     float64  `json:"durationSeconds"`
	DistanceMeters      float64  `json:"distanceMeters"`
	AveragePaceSPerKm   *float64 `json:"averagePaceSPerKm"`
	AverageHeartRateBpm *float64 `json:"averageHeartRateBpm"`
	AverageCadenceSpm   *float64 `json:"averageCadenceSpm"`
}

type SegmentPlanMatch struct {
	LocalDate         string   `json:"localDate"`
	Kind              string   `json:"kind"`
	Revision          int64    `json:"revision"`
	MatchedBy         string   `json:"matchedBy"`
	PlannedDistanceM  *float64 `json:"plannedDistanceM"`
	ExplainedFraction float64  `json:"explainedFraction"`
	Reason            string   `json:"reason"`
}

type DynamicsMetric struct {
	Key           string   `json:"key"`
	Label         string   `json:"label"`
	Unit          string   `json:"unit"`
	Average       *float64 `json:"average"`
	DriftFraction *float64 `json:"driftFraction"`
	Coverage      float64  `json:"coverage"`
	Samples       int      `json:"samples"`
	ReasonCodes   []string `json:"reasonCodes"`
}

type DynamicsFlag struct {
	Code              string `json:"code"`
	Detail            string `json:"detail"`
	ComparableSamples int    `json:"comparableSamples"`
}

type Dynamics struct {
	PolicyVersion string           `json:"policyVersion"`
	Analysable    bool             `json:"analysable"`
	Metrics       []DynamicsMetric `json:"metrics"`
	Flags         []DynamicsFlag   `json:"flags"`
	Note          string           `json:"note"`
}

type SeriesResponse struct {
	WorkoutID               string            `json:"workoutID"`
	StartedAtUs             int64             `json:"startedAtUs"`
	EndedAtUs               int64             `json:"endedAtUs"`
	DurationS               float64           `json:"durationS"`
	Splits                  []Split           `json:"splits"`
	Grade                   *GradeSummary     `json:"grade"`
	Segments                []Segment         `json:"segments"`
	SegmentPlan             *SegmentPlanMatch `json:"segmentPlan"`
	SegmentNote             string            `json:"segmentNote"`
	HasMeasuredWorkoutSteps bool              `json:"hasMeasuredWorkoutSteps"`
	Dynamics                *Dynamics         `json:"dynamics"`
	Series                  SeriesBundle      `json:"series"`
}

type FreshnessFacts struct {
	CommittedAt string `json:"committedAt"`
	LastBatchAt string `json:"lastBatchAt"`
	Batches     int    `json:"batches"`
}

type FreshnessAnalysis struct {
	Status      string `json:"status"`
	Available   bool   `json:"available"`
	JobID       string `json:"jobID"`
	PublishedAt string `json:"publishedAt"`
}

type FreshnessMetric struct {
	Metric          string `json:"metric"`
	Samples         int    `json:"samples"`
	State           string `json:"state"`
	LastAvailableAt string `json:"lastAvailableAt"`
}

type Freshness struct {
	WorkoutID      string            `json:"workoutID"`
	Facts          FreshnessFacts    `json:"facts"`
	Analysis       FreshnessAnalysis `json:"analysis"`
	Series         []FreshnessMetric `json:"series"`
	Completeness   string            `json:"completeness"`
	DeliveryState  string            `json:"deliveryState"`
	AnalysisState  string            `json:"analysisState"`
	StabilityState string            `json:"stabilityState"`
}
