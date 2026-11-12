// Package runfitness is the RAI v1 running-fitness model: single-run fitness,
// long-term fitness, four ability dimensions, per-distance race predictions
// and six training-pace zones. It is pure: no database, HTTP, clock, admin
// tools, VO2 product results or retired running-ability code. Every formula,
// window and threshold here is an unvalidated RAI v1 design parameter, not a
// COROS or RQrun algorithm and not a physiological measurement.
package runfitness

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
)

const (
	Algorithm    = "rai.running_fitness"
	Version      = "v1"
	Schema       = "rai.running_fitness/1"
	SingleSchema = "rai.running_fitness.single/1"
	RatingPolicy = "rai.marathon_equivalent/1"
	ArtifactKey  = "running_fitness"
	// CalendarPolicy reuses the load model's local civil-day policy.
	CalendarPolicy = "local-days/v1"
)

// Fixed neutral scoring policy (section 7.2 / 9.3).
const (
	Beta         = 1.0
	RT           = 0.85
	TT           = 2400.0
	BL0          = 1.10
	BS0          = 1.06
	MarathonM    = 42195.0
	ZeroSeconds  = 28800.0
	RefIntensity = 0.70
	SprintRef    = 4.6
)

// Policy is every tunable parameter of v1. Its hash is the model ConfigHash.
type Policy struct {
	Version          string     `json:"version"`
	Rating           string     `json:"ratingPolicy"`
	Calendar         string     `json:"calendarPolicy"`
	Features         string     `json:"featurePolicy"`
	Beta             float64    `json:"beta"`
	BetaRange        [2]float64 `json:"betaRange"`
	RT               float64    `json:"rT"`
	RTRange          [2]float64 `json:"rTRange"`
	TT               float64    `json:"tT"`
	BL0              float64    `json:"bL0"`
	BLRange          [2]float64 `json:"bLPriorRange"`
	BLDomain         [2]float64 `json:"bLDomain"`
	BLStep           float64    `json:"bLStep"`
	BLPriorSigma     float64    `json:"bLPriorSigma"`
	BS0              float64    `json:"bS0"`
	BSRange          [2]float64 `json:"bSRange"`
	CoreHRR          [2]float64 `json:"coreHRR"`
	LowHRR           float64    `json:"lowHRR"`
	EdgeHRR          float64    `json:"edgeHRR"`
	EdgeQuality      float64    `json:"edgeQuality"`
	LowQuality       float64    `json:"lowQuality"`
	CycleQuality     float64    `json:"cycleQuality"`
	MinSpeed         float64    `json:"minSpeed"`
	Coverage         float64    `json:"coverage"`
	DiagnosticCover  float64    `json:"diagnosticCoverage"`
	SpeedCV          float64    `json:"speedCV"`
	HRTrend          float64    `json:"hrTrend"`
	GradeCore        float64    `json:"gradeCore"`
	GradeMax         float64    `json:"gradeMax"`
	UnadjustedWeight float64    `json:"unadjustedWeight"`
	IndoorWeight     float64    `json:"indoorLongTermWeight"`
	DriftAfter       float64    `json:"driftAfterSeconds"`
	DriftWeight      float64    `json:"driftWeight"`
	LimitedSeconds   float64    `json:"limitedSeconds"`
	SupportedSeconds float64    `json:"supportedSeconds"`
	RestSensitivity  float64    `json:"restSensitivity"`
	MaxSensitivity   float64    `json:"maxSensitivity"`
	AgeMaxSensitive  float64    `json:"ageEstimatedMaxSensitivity"`
	SpeedSensitivity float64    `json:"speedSensitivity"`
	UnknownGradeSens float64    `json:"unknownGradeSpeedSensitivity"`
	DailyWindow      int        `json:"dailyWindowDays"`
	DailyTau         float64    `json:"dailyTauDays"`
	DailyTaper       int        `json:"dailyTaperDays"`
	RaceWindow       int        `json:"raceWindowDays"`
	RaceTau          float64    `json:"raceTauDays"`
	RaceTaper        int        `json:"raceTaperDays"`
	RaceBudget       float64    `json:"raceBudget"`
	MinutesFull      float64    `json:"fullWeightMinutes"`
	HuberDelta       float64    `json:"huberDelta"`
	MinScale         float64    `json:"minScale"`
	SigmaRace        float64    `json:"sigmaRace"`
	SigmaThreshold   float64    `json:"sigmaThresholdPrior"`
	SigmaShape       float64    `json:"sigmaShapePrior"`
	MaxIterations    int        `json:"maxIterations"`
	Tolerance        float64    `json:"tolerance"`
	ChangeScore      float64    `json:"changeScore"`
	ChangeAnchor     float64    `json:"changeAnchor"`
	PreparationDays  int        `json:"preparationDays"`
	Weather          string     `json:"weatherPolicy"`
	Zones            string     `json:"zonePolicy"`
	Prediction       string     `json:"predictionPolicy"`
	Sources          string     `json:"sourcePolicy"`
}

func DefaultPolicy() Policy {
	return Policy{
		Version: "rai.running_fitness.policy/1", Rating: RatingPolicy, Calendar: CalendarPolicy,
		Features: "rai.run_fitness_features/1",
		Beta:     Beta, BetaRange: [2]float64{.85, 1.15}, RT: RT, RTRange: [2]float64{.80, .90}, TT: TT,
		BL0: BL0, BLRange: [2]float64{1.06, 1.18}, BLDomain: [2]float64{1.02, 1.25}, BLStep: .001, BLPriorSigma: .03,
		BS0: BS0, BSRange: [2]float64{1.03, 1.12},
		CoreHRR: [2]float64{.50, .85}, LowHRR: .40, EdgeHRR: .92, EdgeQuality: .6, LowQuality: .7, CycleQuality: .5,
		MinSpeed: 1.4, Coverage: .90, DiagnosticCover: .70, SpeedCV: .08, HRTrend: 2, GradeCore: .05, GradeMax: .08,
		UnadjustedWeight: .6, IndoorWeight: .5, DriftAfter: 3600, DriftWeight: .5,
		LimitedSeconds: 180, SupportedSeconds: 600,
		RestSensitivity: 5, MaxSensitivity: 10, AgeMaxSensitive: 12, SpeedSensitivity: .02, UnknownGradeSens: .08,
		DailyWindow: 42, DailyTau: 21, DailyTaper: 7, RaceWindow: 90, RaceTau: 42, RaceTaper: 14, RaceBudget: 2, MinutesFull: 20,
		HuberDelta: 1.5, MinScale: .03, SigmaRace: .03, SigmaThreshold: .10, SigmaShape: .12, MaxIterations: 30, Tolerance: 1e-8,
		ChangeScore: 5, ChangeAnchor: .10, PreparationDays: 56,
		Weather:    "cold<5|hot>25:x0.7;humidity>.85:x0.8;missing:x0.8;<0|>30:no-upgrade/v1",
		Zones:      "V(.40)/V(.55)/V(.70)/.95vT/1.03vT/v3min/v90s;z5-three-level;z6-direct/v1",
		Prediction: "two-segment-v6-vT-bL;bisect.1s;360s-8h;corners;no-resampling/v1",
		Sources:    "stable-source-identity;conflict-isolated;missing-.5/v1",
	}
}

func digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ConfigHash identifies the complete v1 policy.
func ConfigHash() string { return digest(DefaultPolicy()) }

// Hash is the canonical content hash of a result.
func Hash(v any) string { return digest(v) }

// Reference anchors (section 9.3).
var (
	VTRef = MarathonM / (TT * math.Pow(14400/TT, 1/BL0))
	ARef  = VTRef * RefIntensity / RT
	V6Ref = VTRef * math.Pow(360/TT, 1/BS0-1)
)

// MarathonSeconds is the neutral-prior marathon-equivalent time for a
// threshold proxy speed under long-distance exponent bL.
func MarathonSeconds(vT, bL float64) float64 {
	return TT * math.Pow(MarathonM/(vT*TT), bL)
}

// ScoreFromTime maps a marathon-equivalent time to the RAI 0-100 scale
// (8 h = 0, 4 h = 50, 2 h = 100). raw is unclamped.
func ScoreFromTime(t float64) (score, raw float64, saturation string) {
	raw = 100 * math.Log(ZeroSeconds/t) / math.Log(4)
	score = raw
	if raw < 0 {
		score, saturation = 0, "low"
	}
	if raw > 100 {
		score, saturation = 100, "high"
	}
	return
}

// ScoreA is the fixed-policy score of a 70% HRR equivalent speed. It equals
// Rating(A, ARef) by construction (test-enforced).
func ScoreA(a float64) (score, raw float64, saturation string) {
	return ScoreFromTime(MarathonSeconds(a*math.Pow(RT/RefIntensity, Beta), BL0))
}

// Rating is the dimension scale: slope 50*bL0 per doubling so that the
// aerobic rating equals the fixed-policy score of the same A.
func Rating(x, ref float64) float64 {
	return clamp(50+50*BL0*math.Log2(x/ref), 0, 100)
}

func clamp(x, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, x)) }
func ptr[T any](v T) *T               { return &v }
func finite(x float64) bool           { return !math.IsNaN(x) && !math.IsInf(x, 0) }

type Range struct {
	Low  float64 `json:"low"`
	High float64 `json:"high"`
}

func spanOf(xs []float64) *Range {
	if len(xs) == 0 {
		return nil
	}
	r := Range{Low: math.Inf(1), High: math.Inf(-1)}
	for _, x := range xs {
		if !finite(x) {
			continue
		}
		r.Low = math.Min(r.Low, x)
		r.High = math.Max(r.High, x)
	}
	if !finite(r.Low) {
		return nil
	}
	return &r
}

// Pace converts m/s to s/km.
func Pace(speed float64) float64 { return 1000 / speed }
