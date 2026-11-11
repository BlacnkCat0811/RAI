// Package recovery is the candidate physiological recovery domain. Its inputs
// are SDNN, source-derived RHR and sleep only; no training model is imported.
package recovery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
)

const (
	Model            = "rai.recovery.physiological/v1"
	Algorithm        = "rai.recovery.physiological"
	JobKind          = "analysis.physiological_recovery"
	Family           = "physiological_recovery_candidate"
	FeatureSchema    = "rai.daily_health_features/1"
	BaselineSchema   = "rai.daily_health_baseline/1"
	Schema           = "rai.recovery.physiological/1"
	InputSchema      = "rai.recovery.input/1"
	HRV              = "HKQuantityTypeIdentifierHeartRateVariabilitySDNN"
	RHR              = "HKQuantityTypeIdentifierRestingHeartRate"
	Sleep            = "HKCategoryTypeIdentifierSleepAnalysis"
	Hour             = int64(3600000000)
	MaxFacts         = 8192
	MaxArtifactBytes = 2 << 20
)

var ErrInvalid = errors.New("recovery_invalid_input")
var ErrLimit = errors.New("recovery_input_limit")
var ErrHistorical = errors.New("recovery_historical_raw_unavailable")

type Config struct {
	UnknownContextFactor                                                       float64
	SupportDays, SleepUncertaintyFactor                                        float64
	ConfidenceDecayUs, CoverageFreshUs                                         int64
	Timezone                                                                   string `json:"timezone"`
	Policy                                                                     string `json:"policy"`
	Window, Minimum, Established, RecentWindow, RecentMinimum                  int
	LogFloor, RHRFloor, SleepFloor, MADFactor                                  float64
	Deadband, AutonomicScale, ComponentClip, ZClip, SleepClip                  float64
	DominantWeight, SecondaryWeight, SleepWeight, SleepDeficitHours            float64
	ConfidenceCap, ProvisionalCap, PartialCap                                  float64
	EpisodeGapUs, MaxEpisodeUs, NapMaxUs, FreshHRVUs, FreshRHRUs, FreshSleepUs int64
	SleepCoverageMinimum                                                       float64
}

func DefaultConfig(tz string) Config {
	return Config{UnknownContextFactor: .7,
		SupportDays:            21,
		SleepUncertaintyFactor: .8,
		ConfidenceDecayUs:      72 * Hour,
		CoverageFreshUs:        24 * Hour,
		Timezone:               tz,
		Policy:                 "daily-sdnn-median-source-epoch;wake-day-union;duration-only-sleep/v1",
		Window:                 28,
		Minimum:                14,
		Established:            28,
		RecentWindow:           7,
		RecentMinimum:          3,
		LogFloor:               .10,
		RHRFloor:               3,
		SleepFloor:             .5,
		MADFactor:              1.4826,
		Deadband:               1,
		AutonomicScale:         2,
		ComponentClip:          3,
		ZClip:                  7,
		SleepClip:              2,
		DominantWeight:         .25,
		SecondaryWeight:        .10,
		SleepWeight:            .15,
		SleepDeficitHours:      1.5,
		ConfidenceCap:          .60,
		ProvisionalCap:         .45,
		PartialCap:             .10,
		EpisodeGapUs:           90 * 60 * 1000000,
		MaxEpisodeUs:           24 * Hour,
		NapMaxUs:               90 * 60 * 1000000,
		FreshHRVUs:             24 * Hour,
		FreshRHRUs:             24 * Hour,
		FreshSleepUs:           24 * Hour,
		SleepCoverageMinimum:   .80,
	}
}
func Hash(v any) string {
	b, e := json.Marshal(v)
	if e != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (c Config) Hash() string     { return Hash(c) }
func Scope(tz, day string) string { return "daily/recovery/v1/" + Hash(tz)[:16] + "/" + day }

type Day struct {
	Date         string   `json:"date"`
	Start        int64    `json:"startUs"`
	End          int64    `json:"endUs"`
	ClockBuckets [4]int64 `json:"localSixHourStartsUs"`
}
type Fact struct {
	ContextIncomplete                            bool
	Type, ID, Hash, SemanticHash, Epoch, SyncKey string
	// StableEpoch is the loader's software-version-free source identity. It
	// is not part of the frozen v1 input hash; the v2 stack substitutes it for
	// Epoch so baselines survive watchOS updates.
	StableEpoch string `json:"-"`
	// SourceRank orders sources for the v2 per-night choice: 0 Apple Watch,
	// 1 other Apple device, 2 third-party app, 3 manual. Not in the v1 hash.
	SourceRank                    int `json:"-"`
	Start, End, KnownAt, Revision int64
	Value                         *float64
	Category                      *int
	Manual                        bool
}
type Tombstone struct {
	Type, ID, Hash    string
	Revision, KnownAt int64
	Start, End        *int64
}
type Coverage struct {
	Type     string `json:"type"`
	Identity string `json:"identity"`
	Complete bool   `json:"complete"`
	Through  int64  `json:"through"`
}
type Input struct {
	Dataset              string      `json:"datasetID"`
	DatasetEpoch         string      `json:"datasetGeneration"`
	Collector            string      `json:"collectorEpoch"`
	Assignment           string      `json:"assignmentID"`
	Generation           int64       `json:"generation"`
	KnownThroughRevision int64       `json:"knownThroughRevision"`
	CapturedAt           int64       `json:"capturedAt"`
	AsOf                 int64       `json:"asOf"`
	View                 string      `json:"view"`
	Config               Config      `json:"config"`
	Days                 []Day       `json:"days"`
	Facts                []Fact      `json:"facts"`
	Tombstones           []Tombstone `json:"tombstones"`
	Coverage             []Coverage  `json:"coverage"`
}
type Ref struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Hash string `json:"hash"`
}
type Component struct {
	ContextIncomplete bool     `json:"sourceContextIncomplete"`
	Value             *float64 `json:"value"`
	Epoch             string   `json:"sourceEpoch"`
	Count             int      `json:"sampleCount"`
	TimeOfDayCounts   [4]int   `json:"timeOfDayCounts"`
	LastEvent         *int64   `json:"lastEventUs"`
	Quality           string   `json:"quality"`
	Reasons           []string `json:"reasonCodes"`
}
type SleepSummary struct {
	Component
	AwakeHours           *float64 `json:"awakeHours"`
	UnknownHours         *float64 `json:"unknownHours"`
	Continuity           *float64 `json:"continuity"`
	Coverage             *float64 `json:"coverage"`
	EpisodeStart         *int64   `json:"episodeStartUs"`
	EpisodeEnd           *int64   `json:"episodeEndUs"`
	EpisodeCount         int      `json:"episodeCount"`
	NapCount             int      `json:"napCount"`
	StageOverlapSegments int      `json:"stageOverlapSegments"`
}
type DailyHealthFeatures struct {
	Schema       string       `json:"schemaVersion"`
	Date         string       `json:"date"`
	Timezone     string       `json:"timezone"`
	CalendarHash string       `json:"calendarHash"`
	SDNN         Component    `json:"sdnn"`
	RHR          Component    `json:"rhr"`
	Sleep        SleepSummary `json:"sleep"`
	Quality      string       `json:"quality"`
	Reasons      []string     `json:"reasons"`
	InputHash    string       `json:"inputHash"`
}
type Baseline struct {
	Center       *float64 `json:"center"`
	MAD          *float64 `json:"mAD"`
	Sigma        *float64 `json:"sigma"`
	UsableDays   int      `json:"usableDays"`
	RecentDays   int      `json:"recentDays"`
	BaselineDays int      `json:"baselineDays"`
	State        string   `json:"state"`
	Epoch        string   `json:"sourceEpoch"`
	FloorApplied bool     `json:"floorApplied"`
}
type DailyBaseline struct {
	Schema     string   `json:"schemaVersion"`
	Date       string   `json:"date"`
	Timezone   string   `json:"timezone"`
	ConfigHash string   `json:"configHash"`
	SDNN       Baseline `json:"sdnn"`
	RHR        Baseline `json:"rhr"`
	Sleep      Baseline `json:"sleep"`
}
type Deviation struct {
	Raw     float64 `json:"raw"`
	Clipped float64 `json:"clipped"`
	Burden  float64 `json:"burden"`
}
type Freshness struct {
	LastEvent *int64 `json:"lastEventUs"`
	AgeUs     *int64 `json:"ageUs"`
	LimitUs   int64  `json:"limitUs"`
	State     string `json:"state"`
}
type Result struct {
	SDNNFreshness  Freshness `json:"sdnnFreshness"`
	RHRFreshness   Freshness `json:"rhrFreshness"`
	SleepFreshness Freshness `json:"sleepFreshness"`
	Schema         string    `json:"schemaVersion"`
	Dataset        string    `json:"datasetID"`
	DatasetEpoch   string    `json:"datasetGeneration"`
	Collector      string    `json:"collectorEpoch"`
	Assignment     string    `json:"assignmentID"`
	Date           string    `json:"date"`
	Timezone       string    `json:"timezone"`
	CalendarHash   string    `json:"calendarHash"`
	View           string    `json:"view"`
	Status         string    `json:"status"`
	Quality        string    `json:"quality"`
	BaselineState  string    `json:"baselineState"`
	Score          *float64  `json:"score"`
	// DeviationLevel (v2 only) presents the adverse burden in three steps:
	// normal, mild_deviation or marked_deviation. The score itself is unchanged.
	DeviationLevel string `json:"deviationLevel,omitempty"`
	// AutonomicOnly is a differently named partial reference, never the full score.
	AutonomicOnly        *float64   `json:"autonomicOnly"`
	SDNNDeviation        *Deviation `json:"sdnnDeviation"`
	RHRDeviation         *Deviation `json:"rhrDeviation"`
	SleepDeviation       *Deviation `json:"sleepDeviation"`
	SDNNContribution     *float64   `json:"sdnnContribution"`
	RHRContribution      *float64   `json:"rhrContribution"`
	SleepContribution    *float64   `json:"sleepContribution"`
	AutonomicBurden      *float64   `json:"autonomicBurden"`
	SleepBurden          *float64   `json:"sleepBurden"`
	TotalBurden          *float64   `json:"totalBurden"`
	ComponentCoverage    float64    `json:"componentCoverage"`
	Confidence           float64    `json:"confidence"`
	ConfidenceMeaning    string     `json:"confidenceMeaning"`
	ModelVersion         string     `json:"modelVersion"`
	ConfigHash           string     `json:"configHash"`
	Generation           int64      `json:"generation"`
	AsOf                 int64      `json:"asOf"`
	CapturedAt           int64      `json:"capturedAt"`
	KnownThroughRevision int64      `json:"knownThroughRevision"`
	FactRefs             []Ref      `json:"factRefs"`
	TombstoneRefs        []Ref      `json:"tombstoneRefs"`
	Coverage             []Coverage `json:"coverage"`
	InputHash            string     `json:"inputHash"`
	Reasons              []string   `json:"reasonCodes"`
}
type Bundle struct {
	Features []DailyHealthFeatures `json:"features"`
	Baseline DailyBaseline         `json:"baseline"`
	Recovery Result                `json:"recovery"`
}

func ptr(v float64) *float64 { return &v }
func finite(v float64) bool  { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func canonical(in Input) Input {
	in.Facts = append([]Fact{}, in.Facts...)
	sort.Slice(in.Facts, func(i, j int) bool { return in.Facts[i].Type+in.Facts[i].ID < in.Facts[j].Type+in.Facts[j].ID })
	in.Tombstones = append([]Tombstone{}, in.Tombstones...)
	sort.Slice(in.Tombstones, func(i, j int) bool {
		return in.Tombstones[i].Type+in.Tombstones[i].ID < in.Tombstones[j].Type+in.Tombstones[j].ID
	})
	in.Coverage = append([]Coverage{}, in.Coverage...)
	sort.Slice(in.Coverage, func(i, j int) bool { return Hash(in.Coverage[i]) < Hash(in.Coverage[j]) })
	return in
}
func Canonical(in Input) Input  { return canonical(in) }
func InputHash(in Input) string { return Hash(canonical(in)) }
func Validate(in Input) error {
	if len(in.Facts)+len(in.Tombstones) > MaxFacts {
		return ErrLimit
	}
	if in.Config != DefaultConfig(in.Config.Timezone) || in.Config.Timezone == "" || len(in.Days) != in.Config.Window+1 || in.AsOf <= 0 || in.CapturedAt <= 0 || in.KnownThroughRevision < 0 || (in.View != "revised" && in.View != "as_known") {
		return ErrInvalid
	}
	for i, d := range in.Days {
		if d.ClockBuckets[0] != d.Start {
			return ErrInvalid
		}
		for j := 1; j < 4; j++ {
			if d.ClockBuckets[j] <= d.ClockBuckets[j-1] || d.ClockBuckets[j] >= d.End {
				return ErrInvalid
			}
		}
		if len(d.Date) != 10 || d.End <= d.Start || d.End-d.Start < 22*Hour || d.End-d.Start > 26*Hour || i > 0 && (d.Start != in.Days[i-1].End || d.Date <= in.Days[i-1].Date) {
			return ErrInvalid
		}
	}
	seen := map[string]bool{}
	for _, f := range in.Facts {
		k := f.Type + f.ID
		if seen[k] || f.ID == "" || f.Epoch == "" || f.Hash == "" || f.SemanticHash == "" || f.End < f.Start || f.Start < -62135596800000000 || f.End > 253402300799999999 || f.KnownAt <= 0 || f.Revision < 1 {
			return ErrInvalid
		}
		seen[k] = true
		switch f.Type {
		case HRV, RHR:
			if f.Value == nil || !finite(*f.Value) || *f.Value <= 0 || f.Category != nil {
				return ErrInvalid
			}
		case Sleep:
			if f.Category == nil || *f.Category < 0 || *f.Category > 5 || f.Value != nil || f.End == f.Start {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	}
	seen = map[string]bool{}
	for _, t := range in.Tombstones {
		if seen[t.Type+t.ID] {
			return ErrInvalid
		}
		seen[t.Type+t.ID] = true
		if t.Type != HRV && t.Type != RHR && t.Type != Sleep || t.ID == "" || t.Hash == "" || t.KnownAt <= 0 || t.Revision < 1 {
			return ErrInvalid
		}
	}
	return nil
}
func eligible(in Input) []Fact {
	dead := map[string]bool{}
	for _, t := range in.Tombstones {
		if t.Revision <= in.KnownThroughRevision && t.KnownAt <= in.CapturedAt && (in.View == "revised" || t.KnownAt <= in.AsOf) {
			dead[t.Type+t.ID] = true
		}
	}
	out := []Fact{}
	for _, f := range in.Facts {
		if f.End <= in.AsOf && f.KnownAt <= in.CapturedAt && f.Revision <= in.KnownThroughRevision && (in.View == "revised" || f.KnownAt <= in.AsOf) && !dead[f.Type+f.ID] && !f.Manual {
			out = append(out, f)
		}
	}
	return out
}
func median(xs []float64) float64 {
	x := append([]float64{}, xs...)
	sort.Float64s(x)
	n := len(x)
	if n%2 == 1 {
		return x[n/2]
	}
	return x[n/2-1]/2 + x[n/2]/2
}
func reasons(xs ...string) []string {
	sort.Strings(xs)
	out := []string{}
	for _, x := range xs {
		if x != "" && (len(out) == 0 || out[len(out)-1] != x) {
			out = append(out, x)
		}
	}
	return out
}
func missing(kind string) Component {
	return Component{Quality: "missing", Reasons: []string{kind + "_missing"}}
}
func quantity(fs []Fact, d Day, kind string) Component {
	name := "hrv"
	if kind == RHR {
		name = "rhr"
	}
	c := missing(name)
	epochs := map[string]bool{}
	uniq := map[string]Fact{}
	syncs := map[string]int{}
	vs := []float64{}
	for _, f := range fs {
		if f.Type != kind || f.End < d.Start || f.End >= d.End {
			continue
		}
		epochs[f.Epoch] = true
		uniq[f.SemanticHash] = f
		if f.SyncKey != "" {
			syncs[f.SyncKey]++
		}
	}
	if len(epochs) > 1 {
		c.Quality = "source_conflict"
		c.Reasons = []string{name + "_source_conflict"}
		return c
	}
	if len(uniq) == 0 {
		return c
	}
	keys := []string{}
	for k := range uniq {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	last := int64(0)
	for _, k := range keys {
		f := uniq[k]
		c.Epoch = f.Epoch
		c.ContextIncomplete = c.ContextIncomplete || f.ContextIncomplete
		vs = append(vs, *f.Value)
		if f.End > last {
			last = f.End
		}
		q := 0
		for j := 1; j < 4; j++ {
			if f.End >= d.ClockBuckets[j] {
				q = j
			}
		}
		c.TimeOfDayCounts[q]++
	}
	c.Count = len(vs)
	c.LastEvent = &last
	// RHR is already a source statistic: concurrent live revisions or independent
	// daily estimates are ambiguous. Never average those estimates.
	if kind == RHR && len(vs) > 1 {
		c.Quality = "source_conflict"
		c.Reasons = []string{"rhr_source_conflict"}
		return c
	}
	// SDNN sync identifiers with different live semantics are also ambiguous.
	for key, n := range syncs {
		if key != "" && n > 1 {
			unique := map[string]bool{}
			for _, f := range uniq {
				if f.SyncKey == key {
					unique[f.SemanticHash] = true
				}
			}
			if len(unique) > 1 {
				c.Quality = "source_conflict"
				c.Reasons = []string{name + "_source_conflict"}
				return c
			}
		}
	}
	c.Value = ptr(median(vs))
	c.Quality = "complete"
	c.Reasons = []string{}
	return c
}
