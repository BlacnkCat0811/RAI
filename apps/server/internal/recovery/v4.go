package recovery

import "math"

const (
	ModelV4        = "rai.recovery.physiological/v4"
	SchemaV4       = "rai.recovery.physiological/4"
	InputSchemaV4  = "rai.recovery.input/4"
	HistoryDaysV4  = 180
	MeanCountV4    = 7
	DefaultHRV     = 50.0 // SDNN ms; a neutral model reference, not a measurement.
	DefaultRHR     = 60.0 // Used only if neither observations nor a typed value exist.
	DefaultSleep   = 8.0
	PolicyV4Capped = "night-block30m;latest7-valid-day-arithmetic-mean;history180;baseline28-else-latest7;today-sleep-else-mean;typed-rhr-else60;neutral-sdnn50;sleep8;continuous82;estimated75;no-health-absence-veto/v4"
	PolicyV4       = "night-block30m;latest7-valid-day-arithmetic-mean;history180;baseline28-else-latest7;today-sleep-else-mean;typed-rhr-else60;neutral-sdnn50;sleep8;continuous82;estimated-no-score-cap;no-missing-score-penalty;no-health-absence-veto/v4.1"
)

func DefaultV4Config(tz string) Config {
	c := DefaultV3Config(tz)
	c.Window, c.Policy = HistoryDaysV4, PolicyV4
	return c
}

// TypedRHR is an already resolved, effective, non-revoked user input. It never
// becomes a HealthKit fact and is included in the immutable calculation hash.
type TypedRHR struct {
	Value      float64 `json:"value"`
	ID         string  `json:"id"`
	Hash       string  `json:"hash"`
	MeasuredAt int64   `json:"measuredAtUs"`
	KnownAt    int64   `json:"knownAtUs"`
}

type V4Input struct {
	V2Input
	RestingHR         *TypedRHR `json:"restingHR,omitempty"`
	ReferenceConflict bool      `json:"referenceConflict"`
}

// V4 preserves the component-shaped product DTO; the model/schema/config
// identities and explicit inputBasis distinguish it from retained v3 results.
type V4Result = V3Result

func latestValues(fs []DailyHealthFeatures, qs []NightQuality, kind, epoch string) ([]float64, *int64) {
	var values []float64
	var last *int64
	for i := len(fs) - 1; i >= 0 && len(values) < MeanCountV4; i-- {
		c := selectedComponent(fs[i], kind)
		if c.Value == nil || c.Epoch != epoch || kind == HRV && qs[i].Weight <= 0 {
			continue
		}
		values = append(values, *c.Value)
		if c.LastEvent != nil && (last == nil || *c.LastEvent > *last) {
			v := *c.LastEvent
			last = &v
		}
	}
	return values, last
}

func arithmeticMean(xs []float64) float64 {
	v := 0.
	for _, x := range xs {
		v += x
	}
	return v / float64(len(xs))
}

// latestSource keeps unlike sources separate and uses the latest usable night
// rather than requiring evidence in the last seven calendar days.
func latestSource(fs []DailyHealthFeatures, qs []NightQuality, kind string) string {
	for i := len(fs) - 1; i >= 0; i-- {
		c := selectedComponent(fs[i], kind)
		if c.Value != nil && (kind != HRV || qs[i].Weight > 0) {
			return c.Epoch
		}
	}
	return ""
}

func CalculateV4(in V4Input) (V4Result, error) {
	if err := validateForConfig(in.Input, DefaultV4Config(in.Config.Timezone)); err != nil {
		return V4Result{}, err
	}
	if x := in.RestingHR; x != nil && (!finite(x.Value) || x.Value < 30 || x.Value >= 220 || x.ID == "" || x.Hash == "" || x.MeasuredAt <= 0 || x.MeasuredAt > in.AsOf || x.KnownAt < x.MeasuredAt || x.KnownAt > in.CapturedAt || in.View == "as_known" && x.KnownAt > in.AsOf) {
		return V4Result{}, ErrInvalid
	}
	in.Input = canonical(in.Input)
	fs, dropped := prioritize(eligible(in.Input), in.Days)
	var features []DailyHealthFeatures
	var qs []NightQuality
	for _, d := range in.Days {
		s := sleepDaily(fs, d, in.Config, in.AsOf)
		h, q := blockNight(fs, d, s)
		features = append(features, DailyHealthFeatures{Date: d.Date, SDNN: h, RHR: quantity(fs, d, RHR), Sleep: s})
		qs = append(qs, q)
	}
	n := len(features) - 1
	day, today := in.Days[n], features[n]
	r := V4Result{V2Result: V2Result{Result: Result{Schema: SchemaV4, ModelVersion: ModelV4, ConfigHash: in.Config.Hash(), Dataset: in.Dataset, DatasetEpoch: in.DatasetEpoch, Collector: in.Collector, Assignment: in.Assignment, Generation: in.Generation, AsOf: in.AsOf, CapturedAt: in.CapturedAt, Date: day.Date, Timezone: in.Config.Timezone, CalendarHash: Hash(in.Days), View: in.View, KnownThroughRevision: in.KnownThroughRevision, Status: "available", Quality: "complete", BaselineState: "established", InputHash: Hash(in), ConfidenceMeaning: "evidence_support_not_probability", Confidence: .6, Reasons: []string{}, FactRefs: []Ref{}, TombstoneRefs: []Ref{}, Coverage: in.Coverage}}, Mode: "complete", Components: map[string]V3Component{}, MissingComponents: []string{}, ValidBefore: day.End, BaselineNights: 28, TrendNights: MeanCountV4}
	for _, f := range in.Facts {
		r.FactRefs = append(r.FactRefs, Ref{f.Type, f.ID, f.Hash})
	}
	for _, t := range in.Tombstones {
		r.TombstoneRefs = append(r.TombstoneRefs, Ref{t.Type, t.ID, t.Hash})
	}
	estimated := false
	var zs [2]float64
	for j, kind := range []string{HRV, RHR, Sleep} {
		name := []string{"hrv", "rhr", "sleep"}[j]
		c := selectedComponent(today, kind)
		epoch := latestSource(features, qs, kind)
		x := V3Component{State: "observed", Observed: c.Value, Source: epoch, EvidenceAt: c.LastEvent, InputBasis: "recent_mean"}
		if kind == HRV {
			x.NightQuality = qs[n]
		}
		values, last := latestValues(features, qs, kind, epoch)
		x.MeanSamples, x.TrendNights, x.EvidenceAt = len(values), len(values), last
		if len(values) > 0 {
			x.Trend, x.Used = ptr(arithmeticMean(values)), ptr(arithmeticMean(values))
		}
		missing := c.Value == nil || kind == HRV && qs[n].Weight <= 0
		if missing {
			x.State = "historical_mean"
			r.MissingComponents = append(r.MissingComponents, name)
			estimated = true
			if x.Used != nil {
				r.Reasons = append(r.Reasons, "recent_observation_mean_used")
			}
		}
		if kind == Sleep && !missing {
			x.Used, x.InputBasis, x.EvidenceAt = c.Value, "observed", c.LastEvent
		}
		if x.Used == nil {
			estimated = true
			x.State, x.InputBasis, x.Source = "default_baseline", "default_baseline", "model_reference"
			x.Used = ptr([]float64{DefaultHRV, DefaultRHR, DefaultSleep}[j])
			r.Reasons = append(r.Reasons, "health_default_baseline_used")
			if kind == RHR && in.RestingHR != nil {
				x.State, x.InputBasis, x.Source = "user_entered", "user_entered", in.RestingHR.ID
				x.Used, x.EvidenceAt = ptr(in.RestingHR.Value), &in.RestingHR.MeasuredAt
				r.Reasons = append(r.Reasons, "typed_resting_hr_used")
			}
		}
		if j < 2 {
			// Prefer a personal 28-day baseline; sparse histories use the last
			// seven valid prior observations from the bounded 180-day horizon.
			history := append([]DailyHealthFeatures(nil), features[max(0, n-28):n]...)
			for i := range history {
				if kind == HRV && qs[max(0, n-28)+i].Weight <= 0 {
					history[i].SDNN.Value = nil
				}
			}
			b := baseline(history, Component{Epoch: epoch}, kind, DefaultConfig(in.Config.Timezone))
			prior, _ := latestValues(features[:n], qs[:n], kind, epoch)
			if b.UsableDays < MeanCountV4 {
				history = nil
				for _, v := range prior {
					f := DailyHealthFeatures{}
					if kind == HRV {
						f.SDNN = Component{Epoch: epoch, Value: ptr(v)}
					} else {
						f.RHR = Component{Epoch: epoch, Value: ptr(v)}
					}
					history = append(history, f)
				}
				b = baseline(history, Component{Epoch: epoch}, kind, DefaultConfig(in.Config.Timezone))
			}
			x.BaselineNights = b.UsableDays
			r.BaselineNights = min(r.BaselineNights, b.UsableDays)
			r.TrendNights = min(r.TrendNights, len(values))
			value := *x.Used
			if kind == HRV {
				value = math.Log(value)
			}
			if b.Center == nil {
				// Bootstrap a neutral relative deviation, explicitly estimated.
				b.Center, b.Sigma = ptr(value), ptr([]float64{.1, 3}[j])
			}
			x.BaselineCenter, x.BaselineScale = b.Center, b.Sigma
			zs[j] = (value - *b.Center) / *b.Sigma
			if kind == HRV {
				zs[j] = -zs[j]
			}
			if b.UsableDays < MeanCountV4 || len(values) < MeanCountV4 || missing || c.ContextIncomplete || kind == HRV && qs[n].Weight < 1 {
				estimated = true
			}
			if b.UsableDays < MeanCountV4 || c.ContextIncomplete {
				zs[j] = math.Max(0, zs[j])
			}
			x.AdverseZ = ptr(zs[j])
		}
		if x.EvidenceAt != nil && (r.EvidenceAt == nil || *x.EvidenceAt < *r.EvidenceAt) {
			r.EvidenceAt = x.EvidenceAt
		}
		if c.Quality == "source_conflict" {
			r.Status, r.Mode = "source_conflict", "pending"
		}
		r.Components[name] = x
	}
	if in.ReferenceConflict && r.Components["rhr"].InputBasis != "recent_mean" {
		r.Status, r.Mode = "source_conflict", "pending"
		r.Reasons = append(r.Reasons, "reference_conflict")
	}
	sleepEpoch := r.Components["sleep"].Source
	sleepHistory := append([]DailyHealthFeatures(nil), features[max(0, n-28):n]...)
	for i := range sleepHistory {
		if sleepHistory[i].Sleep.Epoch != sleepEpoch {
			sleepHistory[i].Sleep = SleepSummary{}
		}
	}
	r.SleepTarget = sleepTargetValue(in.SleepTargets, sleepHistory, in.AsOf)
	if r.SleepTarget.TargetUnavailable {
		r.SleepTarget = SleepTargetValue{Hours: DefaultSleep, Source: "default_baseline", Reasons: []string{"sleep_target_default"}}
		estimated = true
	}
	r.SleepRegularity = regularity(sleepHistory, in.Days[max(0, n-28):n], today.Sleep, day)
	dSleep := clip((r.SleepTarget.Hours-*r.Components["sleep"].Used)/1.5, 0, 2)
	dRegularity := 0.
	if r.SleepRegularity.Quality == "complete" {
		dRegularity = clip(r.SleepRegularity.DeviationHours-.5, 0, 2)
	}
	if r.BaselineNights < MeanCountV4 {
		r.BaselineState = "bootstrap"
		r.Reasons = append(r.Reasons, "health_baseline_bootstrap")
	} else if r.BaselineNights < 28 {
		r.BaselineState = "provisional"
	}
	r.ComponentCoverage = float64(3-len(r.MissingComponents)) / 3
	if r.Status != "source_conflict" {
		g := func(z float64) float64 { return clip(2*math.Tanh(z/2), -.5, 2) }
		score := clip(82-14*g(zs[0])-8*g(zs[1])-12*dSleep-4*dRegularity, 0, 100)
		if estimated {
			r.Mode, r.Quality, r.Confidence = "estimated", "estimated", .35
		}
		if r.BaselineNights == 0 {
			r.Confidence = math.Min(r.Confidence, .2)
		}
		r.Score = ptr(score)
		r.DeviationLevel = "normal"
		if score < 70 {
			r.DeviationLevel = "mild_deviation"
		}
		if score < 50 {
			r.DeviationLevel = "marked_deviation"
		}
	} else {
		r.Quality, r.Confidence = "insufficient", 0
	}
	if evaluationDeletion(in.Input, day, today.Sleep) {
		r.Confidence *= .7
		r.Reasons = append(r.Reasons, "input_deleted")
	}
	if dropped {
		r.Reasons = append(r.Reasons, "source_deprioritized")
	}
	for _, kind := range []string{HRV, RHR, Sleep} {
		if !coverageV3(in.Input, kind) {
			if r.Status != "source_conflict" {
				r.Mode, r.Quality = "estimated", "estimated"
			}
			r.Confidence = math.Min(r.Confidence, .35)
			r.Reasons = append(r.Reasons, "daily_coverage_incomplete")
		}
	}
	r.Reasons = reasons(r.Reasons...)
	return r, nil
}

func FreshenV4(r V4Result, at int64) (V4Result, error) {
	legacy := DefaultV4Config(r.Timezone)
	legacy.Policy = PolicyV4Capped
	if at < r.AsOf || r.ModelVersion != ModelV4 || r.ConfigHash != DefaultV4Config(r.Timezone).Hash() && r.ConfigHash != legacy.Hash() {
		return V4Result{}, ErrInvalid
	}
	if at >= r.ValidBefore {
		r.Score, r.Status, r.Mode, r.Confidence = nil, "expired", "unavailable", 0
		r.Reasons = reasons(append(r.Reasons, "estimate_expired")...)
	}
	return r, nil
}
