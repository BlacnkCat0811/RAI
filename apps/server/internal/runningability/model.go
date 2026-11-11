// Package runningability selects independent ability branches. It never imports
// performance or VO2, and never infers an endurance curve from a submax prior.
package runningability

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
)

const (
	Schema    = "rai.running_ability_estimate/1"
	Algorithm = "rai.running_ability_estimate"
	Key       = "running_ability_estimate"
	Day       = int64(86400e6)
	Policy    = "vo2-product-gates;f60-prior.85/.75-.92;point-vo2-width.20-or-propagated.25;range-propagated.45;no-gamma;source-selection/v1"
)

var ErrInvalid = errors.New("running_ability_invalid_input")

func Hash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func ConfigHash() string     { h := sha256.Sum256([]byte(Policy)); return hex.EncodeToString(h[:]) }
func finite(x float64) bool  { return !math.IsNaN(x) && !math.IsInf(x, 0) }
func ptr(x float64) *float64 { return &x }

type Range struct {
	Low  float64 `json:"low"`
	High float64 `json:"high"`
}
type Branch struct {
	V60              *float64 `json:"v60"`
	Range            *Range   `json:"range"`
	Score            *float64 `json:"score"`
	Gamma            *float64 `json:"gamma"`
	Mode             string   `json:"mode"`
	Status           string   `json:"status"`
	EvidenceAt       int64    `json:"evidenceAt"`
	Source           string   `json:"source"`
	Confidence       float64  `json:"confidence"`
	SensitivityWidth *float64 `json:"sensitivityRelativeWidth"`
}
type VO2 struct {
	Value         *float64 `json:"value"`
	Range         *Range   `json:"range"`
	CandidateOnly bool     `json:"candidateOnly"`
	SupportOK     bool     `json:"supportOK"`
	Status        string   `json:"status"`
	Stable        bool     `json:"stable"`
	EvidenceAt    int64    `json:"evidenceAt"`
	Count         int      `json:"count"`
	Source        string   `json:"source"`
	Confidence    float64  `json:"confidence"`
}
type Calibration struct {
	Day       string  `json:"day,omitempty"`
	WorkoutID string  `json:"workoutID"`
	At        int64   `json:"at"`
	KnownAt   int64   `json:"knownAt"`
	V60       float64 `json:"v60"`
	VO2       float64 `json:"vo2"`
	Source    string  `json:"source"`
}
type Progress struct {
	Workouts      int `json:"workouts"`
	SpanDays      int `json:"spanDays"`
	ShortWorkouts int `json:"shortWorkouts"`
	LongWorkouts  int `json:"longWorkouts"`
	TargetDays    int `json:"targetDays"`
}
type Input struct {
	Observed60       *float64      `json:"observed60MPS,omitempty"`
	Observed60Source string        `json:"observed60Source,omitempty"`
	Dataset          string        `json:"datasetID"`
	Stack            string        `json:"stackID"`
	Generation       int64         `json:"generation"`
	AsOf             int64         `json:"asOf"`
	Demonstrated     Branch        `json:"demonstrated"`
	Maximal          Branch        `json:"maximal"`
	VO2              VO2           `json:"vo2"`
	Calibrations     []Calibration `json:"calibrations"`
	Progress         Progress      `json:"progress"`
}
type Result struct {
	Calibrations []Calibration `json:"calibrations,omitempty"`
	F60          *float64      `json:"f60,omitempty"`
	F60Range     *Range        `json:"f60Range,omitempty"`
	Schema       string        `json:"schemaVersion"`
	ConfigHash   string        `json:"configHash"`
	Dataset      string        `json:"datasetID"`
	Stack        string        `json:"stackID"`
	Generation   int64         `json:"generation"`
	AsOf         int64         `json:"asOf"`
	InputHash    string        `json:"inputHash"`
	Demonstrated Branch        `json:"demonstrated"`
	Maximal      Branch        `json:"maximalSupported"`
	Submax       Branch        `json:"submaxEstimated"`
	SelectedMode string        `json:"selectedMode"`
	Prior        string        `json:"prior"`
	Progress     Progress      `json:"progress"`
	Reasons      []string      `json:"reasonCodes"`
}

func Score(v float64) float64 { return math.Max(0, math.Min(100, 100*math.Log(v/1.5)/math.Log(4))) }
func Calculate(in Input) (Result, error) {
	if in.Dataset == "" || in.Generation < 1 || in.AsOf <= 0 {
		return Result{}, ErrInvalid
	}
	for _, b := range []Branch{in.Demonstrated, in.Maximal} {
		if b.V60 != nil && (!finite(*b.V60) || *b.V60 <= 0) || b.Score != nil && !finite(*b.Score) {
			return Result{}, ErrInvalid
		}
	}
	if in.Observed60 != nil && (!finite(*in.Observed60) || *in.Observed60 <= 0) {
		return Result{}, ErrInvalid
	}
	in.Calibrations = append([]Calibration{}, in.Calibrations...)
	sort.Slice(in.Calibrations, func(i, j int) bool { return Hash(in.Calibrations[i]) < Hash(in.Calibrations[j]) })
	in.Demonstrated = freshBranch(in.Demonstrated, in.AsOf)
	in.Maximal = freshBranch(in.Maximal, in.AsOf)
	r := Result{Schema: Schema, ConfigHash: ConfigHash(), Dataset: in.Dataset, Stack: in.Stack, Generation: in.Generation, AsOf: in.AsOf, InputHash: Hash(in), Demonstrated: in.Demonstrated, Maximal: in.Maximal, Progress: in.Progress, SelectedMode: "unavailable", Reasons: []string{}}
	v := in.VO2
	r.Submax = Branch{Mode: "submax_estimated", Status: "insufficient_data", Source: v.Source, EvidenceAt: v.EvidenceAt}
	if v.Value != nil && (!finite(*v.Value) || *v.Value <= 3.5) {
		return Result{}, ErrInvalid
	}
	if v.Range != nil && (!finite(v.Range.Low) || !finite(v.Range.High) || v.Range.Low <= 3.5 || v.Range.High < v.Range.Low) {
		return Result{}, ErrInvalid
	}
	valid := v.Value != nil && v.Range != nil && !v.CandidateOnly && v.SupportOK && v.Stable && v.Count >= 4 && (v.Status == "preliminary" || v.Status == "established") && v.EvidenceAt <= in.AsOf && in.AsOf-v.EvidenceAt <= 14*Day
	if valid && (v.Range.High-v.Range.Low) / *v.Value <= .30 {
		f, lo, hi := .85, .75, .92
		r.Prior = "population_prior"
		confidence := math.Min(.35, v.Confidence)
		// Calibration observations must precede the VO2 evidence being assessed,
		// have three independent dates/workouts, and match its source and era.
		var fs []float64
		ids, days := map[string]bool{}, map[string]bool{}
		for _, c := range in.Calibrations {
			if c.At >= v.EvidenceAt || c.KnownAt >= v.EvidenceAt || v.EvidenceAt-c.At > 42*Day || c.Source != v.Source || ids[c.WorkoutID] || c.V60 <= 0 || c.VO2 <= 3.5 || !finite(c.V60) || !finite(c.VO2) {
				continue
			}
			ids[c.WorkoutID] = true
			day := c.Day
			if day == "" {
				day = fmt.Sprint(c.At / Day)
			}
			days[day] = true
			fs = append(fs, 12*c.V60/(c.VO2-3.5))
			r.Calibrations = append(r.Calibrations, c)
		}
		if len(fs) >= 3 && len(days) >= 3 {
			sort.Float64s(fs)
			f = fs[len(fs)/2]
			if f < .75 || f > .92 {
				r.Reasons = append(r.Reasons, "ability_calibration_conflict")
				valid = false
			} else {
				lo, hi = math.Max(.75, f-.04), math.Min(.92, f+.04)
				r.Prior = "individual_calibration"
			}
		}
		if valid {
			r.F60 = &f
			r.F60Range = &Range{lo, hi}
			speed := (*v.Value - 3.5) * f / 12
			low, high := (v.Range.Low-3.5)*lo/12, (v.Range.High-3.5)*hi/12
			width := (high - low) / speed
			r.Submax.Range = &Range{low, high}
			r.Submax.SensitivityWidth = &width
			r.Submax.Confidence = confidence
			r.Submax.Status = "experimental_range"
			// The population prior alone spans 20% of the estimate, so a gate
			// on the propagated width would withhold the point from nearly
			// every runner without a calibration. The prior's spread is a known
			// constant, already carried by the range, the capped support and
			// the population_prior reason. The point therefore depends on the
			// part the runner's own data controls: a VO2 sensitivity width of
			// at most 20%. A calibrated estimate also qualifies through the
			// propagated width. Beyond 45% propagated nothing is shown.
			vo2Width := (v.Range.High - v.Range.Low) / *v.Value
			switch {
			case width > .45:
				r.Submax.Range = nil
				r.Submax.Status = "insufficient_data"
			case vo2Width <= .20 || width <= .25:
				r.Submax.V60 = &speed
				r.Submax.Score = ptr(Score(speed))
				r.Submax.Status = "limited"
			}
			r.Reasons = append(r.Reasons, "submax_prior_used")
		}
	} else {
		r.Reasons = append(r.Reasons, "submax_evidence_insufficient")
	}
	switch {
	case r.Maximal.V60 != nil:
		r.SelectedMode = "maximal_supported"
	case r.Submax.V60 != nil:
		r.SelectedMode = "submax_estimated"
	case r.Demonstrated.V60 != nil:
		r.SelectedMode = "demonstrated"
	}
	if r.Submax.Range != nil && r.Maximal.V60 != nil && r.Submax.Source == r.Maximal.Source {
		m := *r.Maximal.V60
		if m > r.Submax.Range.High*1.1 || m < r.Submax.Range.Low/1.1 {
			r.Reasons = append(r.Reasons, "ability_source_disagreement")
			r.SelectedMode = "maximal_supported"
		}
	}
	if r.Submax.Range != nil && in.Observed60 != nil && in.Observed60Source == r.Submax.Source && r.Submax.Range.High < *in.Observed60*.9 {
		r.Reasons = append(r.Reasons, "ability_source_disagreement")
		r.SelectedMode = "source_conflict"
	}
	sort.Strings(r.Reasons)
	return r, nil
}
func Freshen(r Result, at int64) (Result, error) {
	if r.Schema != Schema || r.ConfigHash != ConfigHash() || at < r.AsOf {
		return Result{}, ErrInvalid
	}
	r.Demonstrated = freshBranch(r.Demonstrated, at)
	r.Maximal = freshBranch(r.Maximal, at)
	if r.SelectedMode == "maximal_supported" && r.Maximal.V60 == nil || r.SelectedMode == "demonstrated" && r.Demonstrated.V60 == nil {
		r.SelectedMode = "unavailable"
	}
	if r.Submax.EvidenceAt > 0 && at-r.Submax.EvidenceAt > 14*Day {
		r.Submax.Status = "stale"
		if at-r.Submax.EvidenceAt > 28*Day {
			r.Submax.V60 = nil
			r.Submax.Score = nil
			r.Submax.Range = nil
		}
		if r.SelectedMode == "submax_estimated" {
			r.SelectedMode = "unavailable"
			if r.Demonstrated.V60 != nil {
				r.SelectedMode = "demonstrated"
			}
		}
	}
	return r, nil
}

func freshBranch(b Branch, at int64) Branch {
	if b.Status != "available" && b.Status != "limited" || b.EvidenceAt > 0 && at-b.EvidenceAt >= 28*Day {
		b.V60 = nil
		b.Score = nil
		b.Gamma = nil
	}
	return b
}
