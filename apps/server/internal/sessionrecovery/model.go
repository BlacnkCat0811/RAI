// Package sessionrecovery models residual fatigue from recorded runs only.
// All time, lineage and structure arrive as frozen DTOs from the day stack.
package sessionrecovery

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
)

const (
	Schema       = "rai.session_recovery/1"
	Algorithm    = "rai.session_recovery"
	Key          = "session_recovery"
	Policy       = "AU/B^.6;u<=4;tau18-72;short-repeat-proxy;threshold.25;binary.25h;max168h;tail180d-4096;material.02;unknown.5-2B-tau48;baseline28-fallback90/v1"
	Hour         = int64(3600e6)
	MaxWorkouts  = 4096
	HorizonHours = 180 * 24
	// MaterialResidual is the residual fatigue below which a past run no longer
	// decides anything: it is under a tenth of the 0.25 readiness threshold.
	// A run whose worst case is below it cannot block the result or downgrade
	// its mode, however little is known about it.
	MaterialResidual = .02
)

var ErrInvalid = errors.New("session_recovery_invalid_input")

func Hash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func ConfigHash() string            { h := sha256.Sum256([]byte(Policy)); return hex.EncodeToString(h[:]) }
func finite(x float64) bool         { return !math.IsNaN(x) && !math.IsInf(x, 0) }
func clamp(x, a, b float64) float64 { return math.Max(a, math.Min(b, x)) }
func ptr(x float64) *float64        { return &x }

type Range struct {
	Low  float64 `json:"low"`
	High float64 `json:"high"`
}
type Workout struct {
	ID       string   `json:"id"`
	Hash     string   `json:"hash"`
	Start    int64    `json:"startUs"`
	End      int64    `json:"endUs"`
	AU       *float64 `json:"au"`
	Baseline *float64 `json:"preWorkoutTypicalAU"`
	LongRef  *float64 `json:"preWorkoutMedianMinutes"`
	// BaselineExtended marks a Baseline taken from the 90-day fallback window
	// because the 28 days before the run held too few training days.
	BaselineExtended  bool    `json:"baselineExtended,omitempty"`
	Minutes           float64 `json:"activeMinutes"`
	HighShare         float64 `json:"highShare"`
	AnaerobicShare    float64 `json:"anaerobicShare"`
	UnclassifiedShare float64 `json:"unclassifiedShare"`
}
type Term struct {
	ID      string `json:"id"`
	Hash    string `json:"hash"`
	End     int64  `json:"endUs"`
	Pulse   Range  `json:"pulse"`
	Tau     Range  `json:"tauHours"`
	Unknown bool   `json:"unknown"`
}
type Input struct {
	Dataset     string    `json:"datasetID"`
	Stack       string    `json:"stackID"`
	Generation  int64     `json:"generation"`
	AsOf        int64     `json:"asOf"`
	ValidBefore int64     `json:"validBefore"`
	Workouts    []Workout `json:"workouts"`
}
type Result struct {
	Schema         string   `json:"schemaVersion"`
	ConfigHash     string   `json:"configHash"`
	Dataset        string   `json:"datasetID"`
	Stack          string   `json:"stackID"`
	Generation     int64    `json:"generation"`
	AsOf           int64    `json:"asOf"`
	EvaluationAt   int64    `json:"evaluationAt"`
	ValidBefore    int64    `json:"validBefore"`
	Mode           string   `json:"mode"`
	Status         string   `json:"status"`
	Scope          string   `json:"scope"`
	RemainingHours *float64 `json:"remainingHours"`
	Range          *Range   `json:"range"`
	Residual       *Range   `json:"residualFatigue"`
	ReadyAfter     *int64   `json:"readyAfter"`
	Overflow       bool     `json:"overflow"`
	Confidence     float64  `json:"confidence"`
	TailBound      float64  `json:"tailResidualBound"`
	TailHoursError float64  `json:"tailHoursError"`
	UnknownBurden  float64  `json:"unknownTypicalDays"`
	Terms          []Term   `json:"terms"`
	Reasons        []string `json:"reasonCodes"`
	InputHash      string   `json:"inputHash"`
}

// TailBound proves truncation for at most MaxWorkouts omitted pulses: every
// pulse <=4 and tau<=72h. The same count cap applies to the loader's universe.
func TailBound() float64 { return MaxWorkouts * 4 * math.Exp(-HorizonHours/72.) }
func fatigue(terms []Term, at int64, high bool) float64 {
	out := 0.
	for _, t := range terms {
		if t.End > at {
			continue
		}
		u, tau := t.Pulse.Low, t.Tau.Low
		if high {
			u, tau = t.Pulse.High, t.Tau.High
		}
		out += u * math.Exp(-float64(at-t.End)/float64(Hour)/tau)
	}
	return out
}
func hours(terms []Term, at int64, high bool) (float64, bool) {
	if fatigue(terms, at, high) <= .25 {
		return 0, false
	}
	if fatigue(terms, at+168*Hour, high) > .25 {
		return 168, true
	}
	lo, hi := 0., 168.
	for hi-lo > .25 {
		mid := (lo + hi) / 2
		if fatigue(terms, at+int64(mid*float64(Hour)), high) > .25 {
			lo = mid
		} else {
			hi = mid
		}
	}
	return hi, false
}
func project(r Result, at int64) Result {
	r.EvaluationAt = at
	r.UnknownBurden = 0
	for _, t := range r.Terms {
		if t.Unknown {
			r.UnknownBurden += math.Exp(-float64(at-t.End) / float64(36*Hour))
		}
	}
	lo, overLo := hours(r.Terms, at, false)
	hi, overHi := hours(r.Terms, at, true)
	r.Range = &Range{lo, hi}
	r.Residual = &Range{fatigue(r.Terms, at, false), fatigue(r.Terms, at, true)}
	r.Overflow = overLo || overHi
	if !r.Overflow {
		r.RemainingHours = ptr(hi)
		ready := at + int64(hi*float64(Hour))
		r.ReadyAfter = &ready
	} else {
		r.RemainingHours = nil
		r.ReadyAfter = nil
	}
	return r
}
func Calculate(in Input) (Result, error) {
	if in.AsOf <= 0 || in.ValidBefore <= in.AsOf || in.Generation < 1 || in.Dataset == "" || len(in.Workouts) > MaxWorkouts {
		return Result{}, ErrInvalid
	}
	in.Workouts = append([]Workout{}, in.Workouts...)
	sort.Slice(in.Workouts, func(i, j int) bool {
		if in.Workouts[i].End != in.Workouts[j].End {
			return in.Workouts[i].End < in.Workouts[j].End
		}
		return in.Workouts[i].ID < in.Workouts[j].ID
	})
	r := Result{Schema: Schema, ConfigHash: ConfigHash(), Dataset: in.Dataset, Stack: in.Stack, Generation: in.Generation, AsOf: in.AsOf, ValidBefore: in.ValidBefore, Mode: "complete", Status: "available", Scope: "recorded_running_next_hard_session", Confidence: .6, TailBound: TailBound(), TailHoursError: 72 * math.Log1p(TailBound()/.25), Terms: []Term{}, Reasons: []string{}, InputHash: Hash(in)}
	seen := map[string]bool{}
	blocked := false
	for _, w := range in.Workouts {
		if seen[w.ID] || w.ID == "" || w.Start >= w.End || !finite(w.Minutes) || w.Minutes < 0 {
			return Result{}, ErrInvalid
		}
		seen[w.ID] = true
		if w.End > in.AsOf {
			r.ValidBefore = min(r.ValidBefore, w.End)
			continue
		}
		if in.AsOf-w.End > HorizonHours*Hour {
			continue
		}
		if w.AU != nil && (!finite(*w.AU) || *w.AU < 0) {
			return Result{}, ErrInvalid
		}
		if w.AU != nil && *w.AU == 0 {
			continue
		}
		age := float64(in.AsOf-w.End) / float64(Hour)
		if w.Baseline == nil || !finite(*w.Baseline) || *w.Baseline <= 0 {
			// Without a baseline the pulse is unbounded up to the cap. It only
			// withholds the result while that worst case can still matter; a
			// run from a first week or from before a long break stops blocking
			// after about 16 days instead of for the whole 180-day horizon.
			if 4*math.Exp(-age/72) > MaterialResidual {
				blocked = true
				r.Reasons = append(r.Reasons, "load_baseline_insufficient")
			}
			continue
		}
		if !finite(w.HighShare) || !finite(w.AnaerobicShare) || !finite(w.UnclassifiedShare) || w.HighShare < 0 || w.AnaerobicShare < 0 || w.UnclassifiedShare < 0 || w.HighShare+w.AnaerobicShare+w.UnclassifiedShare > 1+1e-8 {
			return Result{}, ErrInvalid
		}
		var reasons []string
		long, longKnown := 0., false
		if w.LongRef != nil && finite(*w.LongRef) && *w.LongRef > 0 {
			long, longKnown = clamp(w.Minutes / *w.LongRef - 1.5, 0, 1), true
		} else {
			reasons = append(reasons, "duration_baseline_insufficient")
		}
		if w.BaselineExtended {
			reasons = append(reasons, "load_baseline_extended")
		}
		unknown := w.AU == nil
		var pulse, tau Range
		if unknown {
			// The load is unknown but the run is not unbounded: it is valued
			// between half and twice a typical training day, with every
			// intensity structure from all-easy to all-unclassified. An unknown
			// duration reference takes the full long-run term in the upper bound.
			upper := 1.
			if longKnown {
				upper = long
			}
			pulse = Range{math.Pow(.5, .6) * (1 + .4*long), clamp(math.Pow(2, .6)*(1.8+.4*upper), 0, 4)}
			tau = Range{clamp(18+12*long, 18, 72), clamp(48+12*upper, 18, 72)}
		} else {
			base := math.Pow(*w.AU / *w.Baseline, .6)
			pulse.Low = clamp(base*(1+.6*w.HighShare+.8*w.AnaerobicShare+.4*long), 0, 4)
			tau.Low = clamp(18+18*w.HighShare+30*w.AnaerobicShare+12*long, 18, 72)
			pulse.High = clamp(pulse.Low+base*.8*w.UnclassifiedShare, 0, 4)
			tau.High = clamp(tau.Low+30*w.UnclassifiedShare, 18, 72)
		}
		if unknown || w.UnclassifiedShare > 1e-8 {
			reasons = append(reasons, "session_intensity_unknown")
		}
		// Only a run that can still move the residual downgrades the result.
		if len(reasons) > 0 && pulse.High*math.Exp(-age/tau.High) > MaterialResidual {
			r.Mode = "estimated"
			r.Reasons = append(r.Reasons, reasons...)
		}
		r.Terms = append(r.Terms, Term{w.ID, w.Hash, w.End, pulse, tau, unknown})
	}
	sort.Strings(r.Reasons)
	r.Reasons = unique(r.Reasons)
	if r.Mode == "estimated" {
		r.Confidence = .35
	}
	if blocked {
		r.Status = "insufficient_data"
		r.Mode = "unavailable"
		r.Confidence = 0
		return r, nil
	}
	return project(r, in.AsOf), nil
}
func unique(xs []string) []string {
	out := []string{}
	for _, s := range xs {
		if len(out) == 0 || out[len(out)-1] != s {
			out = append(out, s)
		}
	}
	return out
}
func Freshen(r Result, at int64) (Result, error) {
	if r.Schema != Schema || r.ConfigHash != ConfigHash() || at < r.AsOf {
		return Result{}, ErrInvalid
	}
	if at >= r.ValidBefore {
		r.Status = "expired"
		r.Mode = "unavailable"
		r.RemainingHours = nil
		r.Range = nil
		r.Residual = nil
		r.ReadyAfter = nil
		return r, nil
	}
	if r.Residual == nil {
		return r, nil
	}
	return project(r, at), nil
}
