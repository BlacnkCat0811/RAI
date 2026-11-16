// Package lactatethreshold estimates the RAI working threshold (design
// rai.lactate_threshold/v1, plan v1.1). The pace is the running-fitness 40
// minute anchor handed in as a typed DTO; the heart rate is the locally
// observed response near that anchor, or the last 20 minutes of a confirmed
// 30 minute time trial. The package is pure: no I/O, no clock, and it does not
// import the running-fitness, recovery, training or HTTP packages.
package lactatethreshold

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const (
	Algorithm   = "rai.lactate_threshold"
	Version     = "v1"
	Schema      = "rai.lactate_threshold/1"
	ArtifactKey = "lactate_threshold"
	// Family is the publication family; it is not one of the counted product
	// families, so day completeness rules stay unchanged.
	Family = "model_threshold_v1/lactate_threshold"

	DefinitionWorking = "rai_t40_working_proxy"
	DefinitionFieldTT = "field_tt30_last20"
)

// Policy holds every v1 design parameter (plan section 15). None of them is
// calibrated on real health data.
type Policy struct {
	WindowDays        int        `json:"hrWindowDays"`
	TauDays           float64    `json:"hrTauDays"`
	TaperDays         float64    `json:"hrTaperDays"`
	FreshDays         int        `json:"hrFreshDays"`
	WarmupSeconds     float64    `json:"warmupSeconds"`
	LateSeconds       float64    `json:"lateRunSeconds"`
	MinCoverage       float64    `json:"minCoverage"`
	MaxSpeedCV        float64    `json:"maxSpeedCV"`
	MaxTrend          float64    `json:"maxHRTrendBPMPerMinute"`
	MaxTrendNear      float64    `json:"maxHRTrendNearThreshold"`
	NearTrendFrom     float64    `json:"nearTrendFromVT"`
	MaxGrade          float64    `json:"maxAbsGrade"`
	MinGradeSupport   float64    `json:"minGradeSupport"`
	StageChange       float64    `json:"stageChangeRatio"`
	DirectBand        float64    `json:"directBandRatio"`
	CorrectedBand     float64    `json:"correctedBandRatio"`
	CorrectedWeight   float64    `json:"correctedTierWeight"`
	SlopeHRR          float64    `json:"slopePriorPerHRR"`
	SlopeDefault      float64    `json:"slopePriorDefault"`
	SlopeRange        [2]float64 `json:"slopePriorRange"`
	MinNearSeconds    float64    `json:"minNearSeconds"`
	InterpDomain      [2]float64 `json:"interpolationDomain"`
	MinInterpSeconds  float64    `json:"minInterpolationSeconds"`
	MinInterpSpread   float64    `json:"minInterpolationSpread"`
	SlopeDomain       [2]float64 `json:"localSlopeDomain"`
	WithinConflict    float64    `json:"withinWorkoutConflictBPM"`
	PrefilterSeconds  float64    `json:"prefilterSeconds"`
	PrefilterSpeed    float64    `json:"prefilterSpeedVT"`
	PrefilterHRR      float64    `json:"prefilterHRR"`
	TTMinSeconds      float64    `json:"timeTrialMinSeconds"`
	TTTailSeconds     float64    `json:"timeTrialTailSeconds"`
	TTMinTailSupport  float64    `json:"timeTrialMinTailSupportSeconds"`
	TTMaxTrend        float64    `json:"timeTrialMaxTrend"`
	DefinitionDiff    float64    `json:"definitionDisagreementBPM"`
	SupportMinutes    float64    `json:"supportMinutesForFullDay"`
	UnknownGrade      float64    `json:"unknownGradeQuality"`
	UncheckedDistance float64    `json:"uncheckedDistanceQuality"`
	ErrorFloor        float64    `json:"hrErrorFloorBPM"`
	SupportedDays     int        `json:"supportedDays"`
	SupportedSpan     int        `json:"supportedSpanDays"`
	SupportedSeconds  float64    `json:"supportedSeconds"`
	SupportedNeff     float64    `json:"supportedNeff"`
	SupportedWidth    float64    `json:"supportedRangeWidthBPM"`
	SupportedLOO      float64    `json:"supportedLOOBPM"`
	SupportedRecent   int        `json:"supportedRecentDays"`
	KnownFlatShare    float64    `json:"knownFlatShare"`
	KnownEnvShare     float64    `json:"knownEnvironmentShare"`
	ConflictWidth     float64    `json:"conflictRangeWidthBPM"`
	ChangeBPM         float64    `json:"changeBPM"`
	ChangeRecentDays  int        `json:"changeRecentDays"`
	ConfirmMinDays    int        `json:"confirmMinDaysApart"`
	HuberDelta        float64    `json:"huberDelta"`
	MaxIterations     int        `json:"maxIterations"`
	Tolerance         float64    `json:"tolerance"`
	MaxDiagnostics    int        `json:"maxWorkoutDiagnostics"`
	TrainingMinWidth  float64    `json:"trainingPaceMinWidth"`
}

func DefaultPolicy() Policy {
	return Policy{
		WindowDays: 42, TauDays: 21, TaperDays: 7, FreshDays: 21,
		WarmupSeconds: 600, LateSeconds: 3600,
		MinCoverage: .90, MaxSpeedCV: .03, MaxTrend: 1, MaxTrendNear: 1.5, NearTrendFrom: .90,
		MaxGrade: .03, MinGradeSupport: .90, StageChange: 1.05,
		DirectBand: 1.02, CorrectedBand: 1.05, CorrectedWeight: .7,
		SlopeHRR: .773, SlopeDefault: 100, SlopeRange: [2]float64{.6, 1.2},
		MinNearSeconds: 600, InterpDomain: [2]float64{.90, 1.08}, MinInterpSeconds: 900, MinInterpSpread: .04,
		SlopeDomain: [2]float64{20, 180}, WithinConflict: 5,
		PrefilterSeconds: 900, PrefilterSpeed: .90, PrefilterHRR: .75,
		TTMinSeconds: 1680, TTTailSeconds: 1200, TTMinTailSupport: 900, TTMaxTrend: 2, DefinitionDiff: 6,
		SupportMinutes: 20, UnknownGrade: .8, UncheckedDistance: .7, ErrorFloor: 5,
		SupportedDays: 2, SupportedSpan: 7, SupportedSeconds: 1800, SupportedNeff: 1.8, SupportedWidth: 12, SupportedLOO: 5, SupportedRecent: 21,
		KnownFlatShare: .5, KnownEnvShare: .7, ConflictWidth: 24,
		ChangeBPM: 5, ChangeRecentDays: 14, ConfirmMinDays: 2,
		HuberDelta: 1.5, MaxIterations: 30, Tolerance: 1e-6, MaxDiagnostics: 512, TrainingMinWidth: .02,
	}
}

// PolicyString is hashed into the manifest configuration.
func PolicyString() string {
	b, _ := json.Marshal(DefaultPolicy())
	return "lactate-threshold/v1;plan-1.1;" + string(b)
}

func ConfigHash() string { return digest(PolicyString()) }

func digest(v any) string {
	var b []byte
	if s, ok := v.(string); ok {
		b = []byte(s)
	} else {
		b, _ = json.Marshal(v)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Hash is the canonical content hash of a value.
func Hash(v any) string { return digest(v) }
