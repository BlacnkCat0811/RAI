package recovery

import (
	"math"
	"sort"
)

const (
	ModelV3  = "rai.recovery.physiological/v3"
	SchemaV3 = "rai.recovery.physiological/3"
	PolicyV3 = "night-block30m;trend7-4;decay3d;weight-cap.35;baseline28-7/14/28;continuous82;single-missing48h;no-deleted-fallback/v3"
)

func DefaultV3Config(tz string) Config { c := DefaultV2Config(tz); c.Policy = PolicyV3; return c }

type NightQuality struct {
	Samples   int     `json:"samples"`
	Blocks    int     `json:"blocks"`
	SpanHours float64 `json:"spanHours"`
	Weight    float64 `json:"weight"`
}
type V3Component struct {
	InputBasis     string       `json:"inputBasis,omitempty"`
	MeanSamples    int          `json:"meanSamples,omitempty"`
	BaselineCenter *float64     `json:"baselineCenter,omitempty"`
	BaselineScale  *float64     `json:"baselineScale,omitempty"`
	State          string       `json:"state"`
	AdverseZ       *float64     `json:"adverseZ,omitempty"`
	Observed       *float64     `json:"observed"`
	Used           *float64     `json:"used"`
	Trend          *float64     `json:"trend"`
	NightQuality   NightQuality `json:"nightQuality"`
	TrendNights    int          `json:"trendNights"`
	BaselineNights int          `json:"baselineNights"`
	EvidenceAt     *int64       `json:"evidenceAt"`
	Source         string       `json:"sourceEpoch"`
}
type V3Result struct {
	V2Result
	Mode              string                 `json:"mode"`
	Components        map[string]V3Component `json:"components"`
	MissingComponents []string               `json:"missingComponents"`
	BaselineNights    int                    `json:"baselineNights"`
	TrendNights       int                    `json:"trendNights"`
	EvidenceAt        *int64                 `json:"evidenceAt"`
	ValidBefore       int64                  `json:"validBefore"`
}

// blockNight uses independent half-hour blocks relative to the sleep episode.
// Repeated observations in one minute never increase independent support.
func blockNight(fs []Fact, d Day, sleep SleepSummary) (Component, NightQuality) {
	c := missing("night_sdnn")
	q := NightQuality{}
	if sleep.EpisodeStart == nil {
		return c, q
	}
	from, to := d.Start-4*Hour, d.ClockBuckets[2]
	var sleeps []Fact
	for _, s := range fs {
		if s.Type == Sleep && s.Category != nil && *s.Category != 2 && s.End > from && s.Start < to {
			sleeps = append(sleeps, s)
		}
	}
	blocks := map[int64][]float64{}
	seen := map[string]bool{}
	var first, last int64
	for _, f := range fs {
		if f.Type != HRV || f.End <= f.Start || f.End < from || f.End >= to || seen[f.SemanticHash] {
			continue
		}
		overlaps := false
		for _, s := range sleeps {
			if s.Type == Sleep && s.Category != nil && *s.Category != 2 && f.Start < s.End && f.End > s.Start {
				overlaps = true
				break
			}
		}
		if !overlaps {
			continue
		}
		seen[f.SemanticHash] = true
		if c.Epoch != "" && c.Epoch != f.Epoch {
			c.Quality = "source_conflict"
			c.Value = nil
			return c, q
		}
		c.Epoch = f.Epoch
		c.ContextIncomplete = c.ContextIncomplete || f.ContextIncomplete
		b := (f.End - *sleep.EpisodeStart) / (Hour / 2)
		blocks[b] = append(blocks[b], math.Log(*f.Value))
		q.Samples++
		if first == 0 || f.Start < first {
			first = f.Start
		}
		if f.End > last {
			last = f.End
		}
	}
	var keys []int64
	for k := range blocks {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	var xs []float64
	for _, k := range keys {
		xs = append(xs, median(blocks[k]))
	}
	q.Blocks = len(xs)
	q.SpanHours = float64(last-first) / float64(Hour)
	c.Count = q.Samples
	if len(xs) > 0 {
		c.Value = ptr(math.Exp(median(xs)))
		c.LastEvent = &last
		c.Quality = "sparse"
		c.Reasons = []string{"night_sparse"}
	}
	if len(xs) >= 2 {
		q.Weight = .45
		if len(xs) >= 4 && q.SpanHours >= 3 {
			q.Weight = 1
			c.Quality = "complete"
			c.Reasons = []string{}
		}
	}
	return c, q
}

func selectedComponent(f DailyHealthFeatures, kind string) Component {
	switch kind {
	case HRV:
		return f.SDNN
	case RHR:
		return f.RHR
	default:
		return f.Sleep.Component
	}
}
func sourceFor(history []DailyHealthFeatures, today Component, kind string) string {
	if today.Epoch != "" {
		return today.Epoch
	}
	for i := len(history) - 1; i >= 0; i-- {
		c := selectedComponent(history[i], kind)
		if c.Value != nil && c.Epoch != "" {
			return c.Epoch
		}
	}
	return ""
}

func trendV3(fs []DailyHealthFeatures, qs []NightQuality, kind, epoch string) (*float64, int, *int64) {
	var xs, ws []float64
	var last int64
	for i := max(0, len(fs)-7); i < len(fs); i++ {
		c := selectedComponent(fs[i], kind)
		if c.Value == nil || c.Epoch != epoch {
			continue
		}
		q := 1.
		if kind == HRV {
			q = qs[i].Weight
		}
		if q <= 0 {
			continue
		}
		x := *c.Value
		if kind == HRV {
			x = math.Log(x)
		}
		xs = append(xs, x)
		ws = append(ws, q*math.Exp(-float64(len(fs)-1-i)/3))
		if c.LastEvent != nil && *c.LastEvent > last {
			last = *c.LastEvent
		}
	}
	if len(xs) < 4 {
		return nil, len(xs), nil
	}
	// Water filling enforces a genuine .35 maximum after normalization.
	weights := make([]float64, len(ws))
	remaining := 1.
	active := make([]bool, len(ws))
	for i := range active {
		active[i] = true
	}
	for {
		sum := 0.
		for i, w := range ws {
			if active[i] {
				sum += w
			}
		}
		capped := false
		for i, w := range ws {
			if active[i] && remaining*w/sum > .35 {
				weights[i] = .35
				remaining -= .35
				active[i] = false
				capped = true
			}
		}
		if !capped {
			for i, w := range ws {
				if active[i] {
					weights[i] = remaining * w / sum
				}
			}
			break
		}
	}
	x := 0.
	for i := range xs {
		x += xs[i] * weights[i]
	}
	if kind == HRV {
		x = math.Exp(x)
	}
	return &x, len(xs), &last
}
func coverageV3(in Input, kind string) bool {
	n := 0
	ok := false
	for _, x := range in.Coverage {
		if x.Type == kind {
			n++
			ok = x.Complete && x.Through >= in.AsOf-in.Config.CoverageFreshUs && x.Through <= in.CapturedAt && (in.View != "as_known" || x.Through <= in.AsOf)
		}
	}
	return n == 1 && ok
}

func CalculateV3(in V2Input) (V3Result, error) {
	cp := in.Input
	cp.Config = DefaultConfig(in.Config.Timezone)
	if err := Validate(cp); err != nil {
		return V3Result{}, err
	}
	if in.Config != DefaultV3Config(in.Config.Timezone) {
		return V3Result{}, ErrInvalid
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
	today := features[n]
	history := features[:n]
	day := in.Days[n]
	r := V3Result{V2Result: V2Result{Result: Result{Schema: SchemaV3, ModelVersion: ModelV3, ConfigHash: in.Config.Hash(), Dataset: in.Dataset, DatasetEpoch: in.DatasetEpoch, Collector: in.Collector, Assignment: in.Assignment, Generation: in.Generation, AsOf: in.AsOf, CapturedAt: in.CapturedAt, Date: day.Date, Timezone: in.Config.Timezone, CalendarHash: Hash(in.Days), View: in.View, KnownThroughRevision: in.KnownThroughRevision, Status: "insufficient_data", Quality: "insufficient", BaselineState: "established", InputHash: Hash(in), ConfidenceMeaning: "evidence_support_not_probability", Reasons: []string{}, FactRefs: []Ref{}, TombstoneRefs: []Ref{}, Coverage: in.Coverage}}, Mode: "unavailable", Components: map[string]V3Component{}, MissingComponents: []string{}, ValidBefore: day.End, BaselineNights: 28, TrendNights: 7}
	for _, f := range in.Facts {
		r.FactRefs = append(r.FactRefs, Ref{f.Type, f.ID, f.Hash})
	}
	for _, t := range in.Tombstones {
		r.TombstoneRefs = append(r.TombstoneRefs, Ref{t.Type, t.ID, t.Hash})
	}
	sleepEpoch := sourceFor(history, today.Sleep.Component, Sleep)
	sleepHistory := append([]DailyHealthFeatures{}, history...)
	for i := range sleepHistory {
		if sleepHistory[i].Sleep.Epoch != sleepEpoch {
			sleepHistory[i].Sleep = SleepSummary{}
		}
	}
	r.SleepTarget = sleepTargetValue(in.SleepTargets, sleepHistory, in.AsOf)
	r.SleepRegularity = regularity(sleepHistory, in.Days[:n], today.Sleep, day)
	pending, conflict, unavailable, estimated := false, false, false, false
	var zs [2]float64
	var nightZ [2]float64
	var quality [2]float64
	confidence := .60
	oldest := in.AsOf
	observed := 0
	missingCount := 0
	for j, kind := range []string{HRV, RHR, Sleep} {
		name := []string{"hrv", "rhr", "sleep"}[j]
		c := selectedComponent(today, kind)
		epoch := sourceFor(history, c, kind)
		x := V3Component{State: "observed", Observed: c.Value, Used: c.Value, Source: epoch, EvidenceAt: c.LastEvent}
		if j == 0 {
			x.NightQuality = qs[n]
		}
		baselineHistory := append([]DailyHealthFeatures(nil), history...)
		if j == 0 {
			for i := range baselineHistory {
				if qs[i].Weight <= 0 {
					baselineHistory[i].SDNN.Value = nil
				}
			}
		}
		b := baseline(baselineHistory, Component{Epoch: epoch}, kind, in.Config)
		x.BaselineNights = b.UsableDays
		if j < 2 {
			r.BaselineNights = min(r.BaselineNights, b.UsableDays)
			if b.UsableDays < 14 {
				r.BaselineState = "collecting"
				confidence = math.Min(confidence, .35)
				estimated = true
			} else if b.UsableDays < 28 && r.BaselineState != "collecting" {
				r.BaselineState = "provisional"
				confidence = math.Min(confidence, .45)
			}
			if b.UsableDays < 7 || b.RecentDays < 3 {
				unavailable = true
				x.State = "source_changed"
				r.Reasons = append(r.Reasons, "baseline_insufficient")
			}
		}
		x.Trend, x.TrendNights, x.EvidenceAt = trendV3(features, qs, kind, epoch)
		if c.LastEvent != nil {
			x.EvidenceAt = c.LastEvent
		}
		if j < 2 {
			r.TrendNights = min(r.TrendNights, x.TrendNights)
		}
		if !coverageV3(in.Input, kind) {
			x.State = "sync_pending"
			pending = true
			r.Reasons = append(r.Reasons, "daily_coverage_incomplete")
		}
		if c.Quality == "source_conflict" {
			x.State = "conflict"
			conflict = true
		}
		sparse := j == 0 && qs[n].Weight < 1 && c.Value != nil
		if sparse {
			x.State = "sparse"
			estimated = true
			r.Reasons = append(r.Reasons, "night_sparse")
		}
		missing := c.Value == nil || j == 0 && qs[n].Weight == 0
		if missing {
			// A fresh completed discovery must have reached the night being declared
			// missing. Yesterday's complete heartbeat cannot establish today's absence.
			required := max(day.Start, min(day.ClockBuckets[2], in.AsOf-120e6))
			synced := false
			for _, cov := range in.Coverage {
				if cov.Type == kind && cov.Complete && cov.Through >= required {
					synced = true
				}
			}
			if !synced {
				pending = true
				r.Reasons = append(r.Reasons, "daily_coverage_incomplete")
			}
			missingCount++
			r.MissingComponents = append(r.MissingComponents, name)
			estimated = true
			x.State = "missing_after_sync"
			x.Used = x.Trend
			prev := selectedComponent(history[n-1], kind)
			if prev.Value == nil || j == 0 && qs[n-1].Weight == 0 || x.Trend == nil || x.EvidenceAt == nil || in.AsOf >= *x.EvidenceAt+48*Hour {
				unavailable = true
				x.State = "expired"
				r.Reasons = append(r.Reasons, "estimate_expired")
			}
			if x.EvidenceAt != nil {
				r.ValidBefore = min(r.ValidBefore, *x.EvidenceAt+48*Hour)
			}
			r.Reasons = append(r.Reasons, "night_data_missing", "recent_trend_used")
		} else {
			observed++
		}
		if missing && pending && (x.State == "missing_after_sync" || x.State == "expired") {
			x.State = "sync_pending"
		}
		if pending && !coverageV3(in.Input, kind) {
			x.State = "sync_pending"
		}
		if c.Quality == "source_conflict" {
			x.State = "conflict"
		}
		if x.EvidenceAt != nil {
			oldest = min(oldest, *x.EvidenceAt)
		}
		if j < 2 && b.Center != nil && b.Sigma != nil && x.Used != nil {
			z := func(v float64) float64 {
				if j == 0 {
					return (*b.Center - math.Log(v)) / *b.Sigma
				}
				return (v - *b.Center) / *b.Sigma
			}
			nightZ[j] = z(*x.Used)
			zs[j] = nightZ[j]
			quality[j] = 1.
			if j == 0 {
				quality[j] = qs[n].Weight
			}
			if x.Trend != nil {
				lambda := .35 * quality[j]
				if missing {
					lambda = 0
				}
				zs[j] = lambda*nightZ[j] + (1-lambda)*z(*x.Trend)
			} else {
				estimated = true
				r.Reasons = append(r.Reasons, "trend_insufficient")
			}
			if j == 0 && nightZ[j] <= -3 {
				r.ContextShift = true
				r.Reasons = append(r.Reasons, "context_shift")
			}
			if missing || sparse || c.ContextIncomplete || r.ContextShift {
				zs[j] = math.Max(0, zs[j])
			}
		} else if j < 2 {
			unavailable = true
		}
		r.Components[name] = x
	}
	r.ComponentCoverage = float64(observed) / 3
	r.EvidenceAt = &oldest
	// Preserve corroborated acute changes even when a seven-night mean is normal.
	for j := 0; j < 2; j++ {
		if quality[j] >= .9 && nightZ[j] >= 2 && nightZ[1-j] > 0 {
			zs[j] = math.Max(zs[j], nightZ[j])
		}
	}
	for j, name := range []string{"hrv", "rhr"} {
		x := r.Components[name]
		if x.Used != nil {
			v := zs[j]
			x.AdverseZ = &v
		}
		r.Components[name] = x
	}
	var deficits []float64
	for i := n - 1; i >= max(0, n-5) && len(deficits) < 3; i-- {
		s := features[i].Sleep
		if s.Value != nil && s.Epoch == r.Components["sleep"].Source {
			deficits = append(deficits, clip((r.SleepTarget.Hours-*s.Value)/1.5, 0, 2))
		}
	}
	mean := 0.
	for _, x := range deficits {
		mean += x
	}
	if len(deficits) > 0 {
		mean /= float64(len(deficits))
	}
	s := r.Components["sleep"]
	dSleep := mean
	if s.Observed != nil {
		dSleep = .65*clip((r.SleepTarget.Hours-*s.Observed)/1.5, 0, 2) + .35*mean
	} else if len(deficits) == 0 {
		unavailable = true
	}
	if r.SleepTarget.TargetUnavailable {
		unavailable = true
		r.Reasons = append(r.Reasons, "sleep_target_missing")
	}
	dRegularity := 0.
	if r.SleepRegularity.Quality == "complete" {
		dRegularity = clip(r.SleepRegularity.DeviationHours-.5, 0, 2)
	} else {
		estimated = true
		r.Reasons = append(r.Reasons, "regularity_collecting")
	}
	if missingCount == 3 {
		prev := features[n-1]
		if prev.SDNN.Value == nil || prev.RHR.Value == nil || prev.Sleep.Value == nil || qs[n-1].Weight < 1 {
			unavailable = true
		}
	}
	if pending {
		r.Status = "sync_incomplete"
		r.Mode = "pending"
	} else if conflict {
		r.Status = "source_conflict"
	} else if unavailable {
		r.Status = "collecting_baseline"
		if len(r.MissingComponents) > 0 {
			r.Status = "insufficient_data"
		}
	} else {
		g := func(z float64) float64 { return clip(2*math.Tanh(z/2), -.5, 2) }
		score := clip(82-14*g(zs[0])-8*g(zs[1])-12*dSleep-4*dRegularity-5*float64(missingCount), 0, 100)
		r.Mode = "complete"
		if estimated {
			r.Mode = "estimated"
			score = math.Min(75, score)
			confidence = math.Min(confidence, .40)
		}
		r.Status = "available"
		r.Quality = r.Mode
		r.Score = &score
		r.Confidence = confidence * math.Min(1, float64(r.BaselineNights)/in.Config.SupportDays) * (2 + qs[n].Weight) / 3 * math.Exp(-float64(max(int64(0), in.AsOf-oldest))/float64(72*Hour))
		r.SDNNDeviation = &Deviation{Raw: zs[0]}
		r.RHRDeviation = &Deviation{Raw: zs[1]}
		r.DeviationLevel = "normal"
		if score < 70 {
			r.DeviationLevel = "mild_deviation"
		}
		if score < 50 {
			r.DeviationLevel = "marked_deviation"
		}
	}
	// Persistent flags use each night's then-available baseline, so a
	// rolling baseline does not erase a three-night episode immediately.
	var bad [3]int
	for i := n - 2; i <= n; i++ {
		f := features[i]
		hist := features[max(0, i-28):i]
		b := DailyBaseline{SDNN: baseline(hist, f.SDNN, HRV, in.Config), RHR: baseline(hist, f.RHR, RHR, in.Config)}
		h, rhr, sleep := false, false, false
		if f.SDNN.Value != nil && b.SDNN.Center != nil && b.SDNN.Sigma != nil {
			h = (*b.SDNN.Center-math.Log(*f.SDNN.Value)) / *b.SDNN.Sigma > 1
		}
		if f.RHR.Value != nil && b.RHR.Center != nil && b.RHR.Sigma != nil {
			rhr = (*f.RHR.Value-*b.RHR.Center) / *b.RHR.Sigma > 1
		}
		if f.Sleep.Value != nil {
			sleep = r.SleepTarget.Hours-*f.Sleep.Value > .5
		}
		if h && qs[i].Weight > 0 && f.SDNN.Epoch == r.Components["hrv"].Source {
			bad[0]++
		}
		if rhr && f.RHR.Epoch == r.Components["rhr"].Source {
			bad[1]++
		}
		if sleep && f.Sleep.Epoch == sleepEpoch {
			bad[2]++
		}
	}
	for i, name := range []string{"hrv", "rhr", "sleep"} {
		if bad[i] == 3 {
			r.PersistentDeviation = append(r.PersistentDeviation, name)
		}
	}
	if len(r.PersistentDeviation) > 0 && r.Score != nil {
		if r.DeviationLevel == "normal" {
			r.DeviationLevel = "mild_deviation"
		} else {
			r.DeviationLevel = "marked_deviation"
		}
	}
	if evaluationDeletion(in.Input, day, today.Sleep) {
		r.Confidence *= .7
		r.Reasons = append(r.Reasons, "input_deleted")
	}
	if dropped {
		r.Reasons = append(r.Reasons, "source_deprioritized")
	}
	r.Reasons = reasons(r.Reasons...)
	return r, nil
}

func FreshenV3(r V3Result, at int64) (V3Result, error) {
	if at < r.AsOf || r.ModelVersion != ModelV3 || r.ConfigHash != DefaultV3Config(r.Timezone).Hash() {
		return V3Result{}, ErrInvalid
	}
	if at >= r.ValidBefore {
		r.Score = nil
		r.Status = "expired"
		r.Mode = "unavailable"
		r.Confidence = 0
		r.Reasons = reasons(append(r.Reasons, "estimate_expired")...)
	}
	return r, nil
}
