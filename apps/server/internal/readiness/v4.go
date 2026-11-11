package readiness

import (
	"math"
	"sort"
)

const (
	ModelV4  = "rai.readiness.training/v4"
	SchemaV4 = "rai.readiness.training/4"
	PolicyV4 = "min-phys-session-weekly;continuous82;unknown.15-days;range<=10;estimated<=75;session-fatigue/v4"
)

func DefaultV4Config(tz string) Config { c := DefaultV3Config(tz); c.Policy = PolicyV4; return c }

type ScoreRange struct {
	Low  float64 `json:"low"`
	High float64 `json:"high"`
}
type Physiological struct {
	Score       *float64 `json:"score"`
	Mode        string   `json:"mode"`
	Status      string   `json:"status"`
	Confidence  float64  `json:"confidence"`
	EvidenceAt  *int64   `json:"evidenceAt"`
	ValidBefore int64    `json:"validBefore"`
	Hash        string   `json:"hash"`
}
type FatigueTerm struct {
	End     int64   `json:"endUs"`
	Low     float64 `json:"low"`
	High    float64 `json:"high"`
	TauLow  float64 `json:"tauLow"`
	TauHigh float64 `json:"tauHigh"`
}
type V4Input struct {
	Snapshot         ReadinessSnapshot `json:"snapshot"`
	Physiology       Physiological     `json:"physiology"`
	SessionAvailable bool              `json:"sessionAvailable"`
	SessionMode      string            `json:"sessionMode"`
	SessionUnknown   float64           `json:"sessionUnknownTypicalDays"`
	Fatigue          []FatigueTerm     `json:"fatigue"`
	Acute            *float64          `json:"acute"`
	Chronic          *float64          `json:"chronic"`
}
type V4Result struct {
	Result
	Mode             string        `json:"mode"`
	Range            *ScoreRange   `json:"range"`
	PhysiologicalCap *float64      `json:"physiologicalCap"`
	SessionCap       *float64      `json:"sessionCap"`
	WeeklyCap        *float64      `json:"weeklyCap"`
	EvidenceAt       *int64        `json:"evidenceAt"`
	Fatigue          []FatigueTerm `json:"fatigueTerms"`
}

// evaluateV4 can run again at any later instant of the same publication.
// Quality keeps the mode the evidence supports (complete or estimated); Mode,
// Status and the wide-range reason are derived from it every time, so a range
// that is too wide at one hour neither sticks for the rest of the day nor
// leaves a stale reason behind once it has narrowed.
func evaluateV4(r *V4Result, at int64) {
	r.Mode, r.Status = r.Quality, "available"
	reasons := make([]string, 0, len(r.Reasons)+1)
	for _, code := range r.Reasons {
		if code != "load_sensitivity_wide" {
			reasons = append(reasons, code)
		}
	}
	r.Reasons = reasons
	low, high := 0., 0.
	for _, t := range r.Fatigue {
		if t.End <= at {
			h := float64(at-t.End) / float64(Hour)
			low += t.Low * math.Exp(-h/t.TauLow)
			high += t.High * math.Exp(-h/t.TauHigh)
		}
	}
	cap := func(v float64) float64 { return 100 / (1 + math.Pow(v/1.4, 2)) }
	r.SessionCap = ptr(cap(high))
	lo, hi := math.Min(*r.PhysiologicalCap, cap(high)), math.Min(*r.PhysiologicalCap, cap(low))
	if r.WeeklyCap != nil {
		lo = math.Min(lo, *r.WeeklyCap)
		hi = math.Min(hi, *r.WeeklyCap)
	}
	if r.Mode == "estimated" {
		lo = math.Min(lo, 75)
		hi = math.Min(hi, 75)
	}
	r.Range = &ScoreRange{lo, hi}
	r.Score = nil
	if hi-lo <= 10+1e-9 {
		r.Score = &lo
	} else {
		r.Mode = "workout_only"
		r.Status = "insufficient_data"
		r.Reasons = append(r.Reasons, "load_sensitivity_wide")
		sort.Strings(r.Reasons)
	}
	r.EvaluationAt = at
}
func CalculateV4(in V4Input) (V4Result, error) {
	s := Canonical(in.Snapshot)
	validation := s
	validation.Config = DefaultV3Config(s.Config.Timezone)
	validation.Recovery = nil
	if s.Config != DefaultV4Config(s.Config.Timezone) {
		return V4Result{}, ErrInvalid
	}
	if err := ValidateV3(validation); err != nil {
		return V4Result{}, err
	}
	in.Snapshot = s
	in.Fatigue = append([]FatigueTerm{}, in.Fatigue...)
	sort.Slice(in.Fatigue, func(i, j int) bool { return Hash(in.Fatigue[i]) < Hash(in.Fatigue[j]) })
	p := in.Physiology
	if !finite(p.Confidence) || p.Confidence < 0 || p.Confidence > 1 || p.Score != nil && (!finite(*p.Score) || *p.Score < 0 || *p.Score > 100) || !finite(in.SessionUnknown) || in.SessionUnknown < 0 {
		return V4Result{}, ErrInvalid
	}
	r := V4Result{Result: Result{Schema: SchemaV4, ModelVersion: ModelV4, Config: s.Config, ConfigHash: s.Config.Hash(), Dataset: s.Dataset, Generation: s.Generation, Date: s.Days[len(s.Days)-1].Date, AsOf: s.AsOf, EvaluationAt: s.AsOf, CapturedAt: s.CapturedAt, ValidBefore: s.Days[len(s.Days)-1].End, Status: "insufficient_data", Quality: "insufficient", InputHash: Hash(in), ConfidenceMeaning: "evidence_support_not_probability", Reasons: []string{}, Scope: "recorded_running_training_readiness", Baseline: LoadBaselineV3(s)}, Mode: "workout_only", EvidenceAt: p.EvidenceAt, Fatigue: append([]FatigueTerm{}, in.Fatigue...)}
	for _, w := range s.Loads {
		if w.End <= s.AsOf {
			if knownLoad(w) {
				r.KnownLoads++
			} else {
				r.UnknownLoads++
			}
		}
		if w.Model != LoadModelV2 {
			r.Status = "source_conflict"
			r.Mode = "pending"
			r.Reasons = append(r.Reasons, "load_model_not_allowed")
			return r, nil
		}
	}
	for _, t := range in.Fatigue {
		if !finite(t.Low) || !finite(t.High) || !finite(t.TauLow) || !finite(t.TauHigh) || t.Low < 0 || t.High < t.Low || t.High > 4 || t.TauLow < 18 || t.TauHigh < t.TauLow || t.TauHigh > 72 {
			return V4Result{}, ErrInvalid
		}
	}
	if in.Acute != nil && (!finite(*in.Acute) || *in.Acute < 0) || in.Chronic != nil && (!finite(*in.Chronic) || *in.Chronic < 0) {
		return V4Result{}, ErrInvalid
	}
	if p.Status == "sync_incomplete" || p.Status == "source_conflict" {
		r.Status = p.Status
		r.Mode = "pending"
		r.Reasons = append(r.Reasons, p.Status)
		return r, nil
	}
	if p.Score == nil || p.ValidBefore <= s.AsOf {
		r.Reasons = append(r.Reasons, "recovery_missing")
		return r, nil
	}
	r.ValidBefore = min(r.ValidBefore, p.ValidBefore)
	r.PhysiologicalCap = p.Score
	r.RecoveryScore = p.Score
	if !in.SessionAvailable {
		r.Reasons = append(r.Reasons, "session_recovery_missing")
		return r, nil
	}
	if in.SessionUnknown > UnknownBurdenMaxV3 {
		r.Reasons = append(r.Reasons, "recent_load_unknown")
		return r, nil
	}
	r.Status = "available"
	r.Mode = "complete"
	r.Quality = "complete"
	r.Confidence = math.Min(.6, p.Confidence)
	if p.Mode != "complete" || in.SessionMode != "complete" || in.SessionUnknown > 0 {
		r.Mode = "estimated"
		r.Quality = "estimated"
		r.Confidence = math.Min(r.Confidence, .4)
	}
	if in.Acute != nil && in.Chronic != nil && *in.Chronic > 1e-6 {
		r.WeeklyCap = ptr(math.Max(40, math.Min(100, 100-25*math.Max(0, (*in.Acute / *in.Chronic - 1.1)/.8))))
	} else {
		r.Mode = "estimated"
		r.Quality = "estimated"
		r.Confidence = math.Min(r.Confidence, .4)
		r.Reasons = append(r.Reasons, "load_baseline_insufficient")
	}
	evaluateV4(&r, s.AsOf)
	sort.Strings(r.Reasons)
	return r, nil
}
func FreshenV4(r V4Result, at int64) (V4Result, error) {
	if at < r.AsOf || r.ModelVersion != ModelV4 || r.Config != DefaultV4Config(r.Config.Timezone) {
		return V4Result{}, ErrInvalid
	}
	if at >= r.ValidBefore {
		r.Score = nil
		r.Range = nil
		r.Status = "expired"
		r.Mode = "workout_only"
		r.Reasons = append(append([]string{}, r.Reasons...), "estimate_expired")
		return r, nil
	}
	// Range is set only by evaluateV4, so it marks a result with all its caps.
	// A result withheld for a wide range is evaluated again as well: the bounds
	// decay at different rates and meet again later in the day.
	if r.Range != nil {
		evaluateV4(&r, at)
	}
	return r, nil
}
