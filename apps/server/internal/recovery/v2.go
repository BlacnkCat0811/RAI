package recovery

import (
	"math"
	"sort"
	"strings"
)

const (
	ModelV2       = "rai.recovery.physiological/v2"
	SchemaV2      = "rai.recovery.physiological/2"
	InputSchemaV2 = "rai.recovery.input/2"
	PolicyV2      = "night-sdnn-asleep-20-12;ln-median;sleep-regularity-circular-medoid;typed-target;persistent-3d;stable-source-key;source-priority;deviation-levels;evaluation-day-tombstones/v2.3"
)

type SleepTarget struct {
	ID             string  `json:"id"`
	Hours          float64 `json:"hours"`
	EffectiveFrom  int64   `json:"effectiveFromUs"`
	EffectiveUntil *int64  `json:"effectiveUntilUs,omitempty"`
	KnownAt        int64   `json:"knownAtUs"`
	Revoked        bool    `json:"revoked"`
	Source         string  `json:"source"`
}

type V2Input struct {
	Input
	SleepTargets []SleepTarget `json:"sleepTargets"`
}

type Regularity struct {
	MidpointHours    float64  `json:"midpointHours"`
	BaselineHours    float64  `json:"baselineMidpointHours"`
	DeviationHours   float64  `json:"deviationHours"`
	MatureNights     int      `json:"matureNights"`
	RecentNights     int      `json:"recentNights"`
	Quality          string   `json:"quality"`
	Ambiguous        bool     `json:"ambiguous"`
	Reasons          []string `json:"reasonCodes"`
	BaselineRun      string   `json:"baselineRunID,omitempty"`
	BaselineHash     string   `json:"baselineHash,omitempty"`
	BaselineFromDate string   `json:"baselineFromDate,omitempty"`
	BaselineToDate   string   `json:"baselineToDate,omitempty"`
}

type SleepTargetValue struct {
	Hours             float64  `json:"hours"`
	Source            string   `json:"source"`
	RelativeToHabit   bool     `json:"relativeToHabit"`
	EffectiveFrom     int64    `json:"effectiveFromUs,omitempty"`
	EffectiveUntil    *int64   `json:"effectiveUntilUs,omitempty"`
	ID                string   `json:"inputID,omitempty"`
	HabitMedianHours  float64  `json:"habitMedianHours,omitempty"`
	HabitNights       int      `json:"habitNights,omitempty"`
	TargetUnavailable bool     `json:"targetUnavailable"`
	Reasons           []string `json:"reasonCodes"`
}

type V2Result struct {
	Result
	SleepRegularity     Regularity       `json:"sleepRegularity"`
	SleepTarget         SleepTargetValue `json:"sleepTarget"`
	PersistentDeviation []string         `json:"persistentDeviationComponents"`
	ContextShift        bool             `json:"contextShift"`
	AutonomicOnlyV2     *float64         `json:"autonomicOnlyV2,omitempty"`
}

func DefaultV2Config(tz string) Config {
	c := DefaultConfig(tz)
	c.Policy = PolicyV2
	return c
}

func midnightHour(d Day, at int64) float64 {
	frac := float64(at-d.Start) / float64(d.End-d.Start)
	h := math.Mod(frac*24, 24)
	if h < 0 {
		h += 24
	}
	return h
}

func circularDeltaHours(a, b float64) float64 {
	d := math.Mod(math.Abs(a-b), 24)
	if d > 12 {
		d = 24 - d
	}
	return d
}

// nightSDNN is the ln-median SDNN of samples recorded while asleep in the night
// window around day d: from 20:00 the evening before to 12:00. A sample counts
// only if it overlaps a sleep interval, so late sleepers and early risers keep
// their night and daytime spot checks never enter it.
func nightSDNN(fs []Fact, d Day) Component {
	out := missing("night_sdnn")
	from, to := d.Start-4*Hour, d.Start+12*Hour
	type span struct{ a, b int64 }
	var sleep []span
	for _, f := range fs {
		if f.Type != Sleep || f.End <= f.Start || f.Category == nil || *f.Category == 2 {
			continue
		}
		if f.End > from && f.Start < to {
			sleep = append(sleep, span{f.Start, f.End})
		}
	}
	if len(sleep) == 0 {
		out.Reasons = []string{"night_context_missing"}
		return out
	}
	asleep := func(f Fact) bool {
		for _, s := range sleep {
			if f.Start < s.b && f.End > s.a {
				return true
			}
		}
		return false
	}
	epochs := map[string]bool{}
	uniq := map[string]Fact{}
	var values []float64
	for _, f := range fs {
		if f.Type != HRV || f.End <= f.Start || f.End < from || f.End >= to || !asleep(f) {
			continue
		}
		epochs[f.Epoch] = true
		uniq[f.SemanticHash] = f
	}
	if len(epochs) > 1 {
		out.Quality = "source_conflict"
		out.Reasons = []string{"night_sdnn_source_conflict"}
		return out
	}
	keys := make([]string, 0, len(uniq))
	for k := range uniq {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var last int64
	for _, k := range keys {
		f := uniq[k]
		v := *f.Value
		if v <= 0 || !finite(v) {
			continue
		}
		values = append(values, math.Log(v))
		out.Epoch = f.Epoch
		out.ContextIncomplete = out.ContextIncomplete || f.ContextIncomplete
		if f.End > last {
			last = f.End
		}
	}
	out.Count = len(values)
	if len(values) < 2 {
		out.Reasons = []string{"night_sdnn_insufficient"}
		return out
	}
	out.Value = ptr(math.Exp(median(values)))
	out.LastEvent = &last
	out.Quality = "complete"
	out.Reasons = []string{}
	return out
}

// prioritize keeps, for each fact type and night, only the facts of the most
// trusted source (Apple Watch, then other Apple devices, then third-party
// apps; ties go to the source with more facts, then a stable order), and only
// the latest resting HR of that night. Another wearable or a sleep app no
// longer turns the whole day into a source conflict.
func prioritize(fs []Fact, days []Day) ([]Fact, bool) {
	night := func(f Fact) int {
		// SDNN belongs to the night window ending at noon (see nightSDNN);
		// resting HR and sleep stay on the civil day of their end.
		at := f.End
		if f.Type == HRV {
			at += 4 * Hour
		}
		i := sort.Search(len(days), func(i int) bool { return days[i].End > at })
		if i < len(days) && days[i].Start <= at {
			return i
		}
		return -1
	}
	type key struct {
		typ string
		day int
	}
	type choice struct {
		epoch string
		rank  int
		n     int
	}
	counts := map[key]map[string]int{}
	ranks := map[string]int{}
	for _, f := range fs {
		k := key{f.Type, night(f)}
		if counts[k] == nil {
			counts[k] = map[string]int{}
		}
		counts[k][f.Epoch]++
		ranks[f.Epoch] = f.SourceRank
	}
	chosen := map[key]string{}
	dropped := false
	for k, byEpoch := range counts {
		var best *choice
		for epoch, n := range byEpoch {
			c := choice{epoch, ranks[epoch], n}
			if best == nil || c.rank < best.rank || c.rank == best.rank && (c.n > best.n || c.n == best.n && c.epoch < best.epoch) {
				best = &c
			}
		}
		chosen[k] = best.epoch
		dropped = dropped || len(byEpoch) > 1
	}
	latest := map[key]Fact{}
	out := []Fact{}
	for _, f := range fs {
		k := key{f.Type, night(f)}
		if f.Epoch != chosen[k] {
			continue
		}
		if f.Type == RHR {
			if old, ok := latest[k]; ok {
				dropped = true
				if old.End > f.End || old.End == f.End && (old.KnownAt > f.KnownAt || old.KnownAt == f.KnownAt && old.ID > f.ID) {
					continue
				}
			}
			latest[k] = f
			continue
		}
		out = append(out, f)
	}
	for _, f := range latest {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type+out[i].ID < out[j].Type+out[j].ID })
	return out, dropped
}

// deviationLevel is the three-step presentation of the adverse-only burden:
// normal, mild or marked deviation from the personal baseline. A deviation
// persisting for three nights moves it one step up.
func deviationLevel(burden float64, persistent bool) string {
	level := 0
	switch {
	case burden >= .25:
		level = 2
	case burden >= .05:
		level = 1
	}
	if persistent && level < 2 {
		level++
	}
	return []string{"normal", "mild_deviation", "marked_deviation"}[level]
}

func selectSleepTarget(ts []SleepTarget, asOf int64) (SleepTarget, bool) {
	best := SleepTarget{}
	found := false
	for _, t := range ts {
		if t.Revoked || t.KnownAt > asOf || t.EffectiveFrom > asOf || (t.EffectiveUntil != nil && *t.EffectiveUntil <= asOf) {
			continue
		}
		if !(t.Hours > 0 && t.Hours <= 24) || !finite(t.Hours) {
			continue
		}
		if !found || t.EffectiveFrom > best.EffectiveFrom || (t.EffectiveFrom == best.EffectiveFrom && t.ID > best.ID) {
			best, found = t, true
		}
	}
	return best, found
}

func sleepTargetValue(ts []SleepTarget, history []DailyHealthFeatures, asOf int64) SleepTargetValue {
	out := SleepTargetValue{Source: "unavailable", Reasons: []string{}}
	if t, ok := selectSleepTarget(ts, asOf); ok {
		out.Hours = t.Hours
		out.Source = "typed"
		out.ID = t.ID
		out.EffectiveFrom = t.EffectiveFrom
		out.EffectiveUntil = t.EffectiveUntil
		return out
	}
	var hs []float64
	for _, f := range history {
		if f.Sleep.Value != nil && *f.Sleep.Value > 0 && *f.Sleep.Value <= 24 {
			hs = append(hs, *f.Sleep.Value)
		}
	}
	if len(hs) >= 7 {
		out.Hours = median(hs)
		out.Source = "habit_median_28d"
		out.RelativeToHabit = true
		out.HabitMedianHours = out.Hours
		out.HabitNights = len(hs)
		return out
	}
	out.TargetUnavailable = true
	out.Reasons = append(out.Reasons, "sleep_target_missing")
	return out
}

func regularity(history []DailyHealthFeatures, days []Day, today SleepSummary, todayDay Day) Regularity {
	out := Regularity{Quality: "missing", Reasons: []string{}}
	if today.EpisodeStart == nil || today.EpisodeEnd == nil {
		out.Reasons = append(out.Reasons, "sleep_missing")
		return out
	}
	mid := float64(*today.EpisodeStart+*today.EpisodeEnd) / 2
	out.MidpointHours = midnightHour(todayDay, int64(mid))
	var histMid []float64
	var recent int
	for i := len(history) - 1; i >= 0 && i >= len(history)-28; i-- {
		s := history[i].Sleep
		if s.EpisodeStart == nil || s.EpisodeEnd == nil || len(days) <= i {
			continue
		}
		m := float64(*s.EpisodeStart+*s.EpisodeEnd) / 2
		histMid = append(histMid, midnightHour(days[i], int64(m)))
		if len(history)-i <= 7 {
			recent++
		}
	}
	out.MatureNights = len(histMid)
	out.RecentNights = recent
	if len(histMid) < 14 || recent < 3 {
		out.Reasons = append(out.Reasons, "regularity_insufficient")
		return out
	}
	// Deterministic circular medoid.
	best, bestSum := 0, math.Inf(1)
	sums := make([]float64, len(histMid))
	for i := range histMid {
		s := 0.0
		for j := range histMid {
			s += circularDeltaHours(histMid[i], histMid[j])
		}
		sums[i] = s
		if s < bestSum {
			bestSum, best = s, i
		}
	}
	out.BaselineHours = histMid[best]
	out.DeviationHours = circularDeltaHours(out.MidpointHours, out.BaselineHours)
	for i := range sums {
		if i != best && math.Abs(sums[i]-bestSum) < 1e-9 && circularDeltaHours(histMid[i], histMid[best]) > 11 {
			out.Ambiguous = true
		}
	}
	if out.Ambiguous {
		out.Reasons = append(out.Reasons, "regularity_ambiguous")
		return out
	}
	out.Quality = "complete"
	out.BaselineRun = "sleep-regularity:" + Hash(histMid)[:16]
	out.BaselineHash = Hash(histMid)
	if len(history) > 0 {
		out.BaselineFromDate = history[maxi(0, len(history)-len(histMid))].Date
		out.BaselineToDate = history[len(history)-1].Date
	}
	return out
}

func maxi(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func adverse(f DailyHealthFeatures, b DailyBaseline, target SleepTargetValue) (bool, bool, bool) {
	if f.SDNN.Value == nil || f.RHR.Value == nil || b.SDNN.Center == nil || b.SDNN.Sigma == nil || b.RHR.Center == nil || b.RHR.Sigma == nil {
		return false, false, false
	}
	zh := (*b.SDNN.Center - math.Log(*f.SDNN.Value)) / *b.SDNN.Sigma
	zr := (*f.RHR.Value - *b.RHR.Center) / *b.RHR.Sigma
	sh := false
	if f.Sleep.Value != nil && target.Hours > 0 {
		sh = target.Hours-*f.Sleep.Value > 0.5
	}
	return zh > 1, zr > 1, sh
}

func ScopeV2For(stack, day string) string { return "model/" + stack + "/athlete/recovery/v2/" + day }

// v2CoverageComplete applies the same daily-coverage gate as v1. A missing or
// stale complete row must not produce a full recovery score even when the
// individual feature values happen to be present.
func v2CoverageComplete(in Input, c Config) bool {
	counts := map[string]int{}
	covered := map[string]bool{}
	for _, x := range in.Coverage {
		counts[x.Type]++
		covered[x.Type] = x.Complete && in.AsOf-x.Through <= c.CoverageFreshUs &&
			x.Through <= in.CapturedAt && (in.View != "as_known" || x.Through <= in.AsOf)
	}
	return covered[HRV] && covered[RHR] && covered[Sleep] &&
		counts[HRV] == 1 && counts[RHR] == 1 && counts[Sleep] == 1
}

// evaluationDeletion reports whether a deletion effective in this view removed
// a sample from the evaluation night or day: from 20:00 the evening before (or
// the start of today's sleep episode, if earlier) to the end of the day. The
// deleted facts are already excluded by eligible(), and a rewrite that is still
// syncing is held back by the daily coverage gate, so a deletion never blocks
// the score; inside this window it only lowers confidence. Deletions that only
// touched history change the baseline through the remaining facts and need no
// flag. Every tombstone still enters TombstoneRefs and the input hash.
func evaluationDeletion(in Input, d Day, sleep SleepSummary) bool {
	from, to := d.Start-4*Hour, d.End
	if sleep.EpisodeStart != nil && *sleep.EpisodeStart < from {
		from = *sleep.EpisodeStart
	}
	for _, t := range in.Tombstones {
		if t.Revision > in.KnownThroughRevision || t.KnownAt > in.CapturedAt || in.View != "revised" && t.KnownAt > in.AsOf {
			continue
		}
		if t.Start == nil || t.End == nil || *t.End >= from && *t.Start < to {
			return true
		}
	}
	return false
}

func CalculateV2(in V2Input) (V2Result, error) {
	cp := in.Input
	cp.Config = DefaultConfig(cp.Config.Timezone)
	if e := Validate(cp); e != nil {
		return V2Result{}, e
	}
	if in.Config != DefaultV2Config(in.Config.Timezone) {
		return V2Result{}, ErrInvalid
	}
	in.Input = canonical(in.Input)
	fs, deprioritized := prioritize(eligible(in.Input), in.Days)
	c := in.Config
	coverageComplete := v2CoverageComplete(in.Input, c)
	calHash := Hash(in.Days)
	features := make([]DailyHealthFeatures, 0, len(in.Days))
	for _, d := range in.Days {
		f := DailyHealthFeatures{Schema: FeatureSchema, Date: d.Date, Timezone: c.Timezone, CalendarHash: calHash,
			SDNN: nightSDNN(fs, d), RHR: quantity(fs, d, RHR), Sleep: sleepDaily(fs, d, c, in.AsOf),
			Quality: "complete", Reasons: []string{}}
		for _, x := range []Component{f.SDNN, f.RHR, f.Sleep.Component} {
			if x.Value == nil {
				f.Quality = "partial"
			}
			f.Reasons = append(f.Reasons, x.Reasons...)
		}
		f.Reasons = reasons(f.Reasons...)
		f.InputHash = Hash(struct {
			Day       Day
			SDNN, RHR Component
			Sleep     SleepSummary
		}{d, f.SDNN, f.RHR, f.Sleep})
		features = append(features, f)
	}
	today := features[len(features)-1]
	todayDay := in.Days[len(in.Days)-1]
	history := features[:len(features)-1]
	historyDays := in.Days[:len(in.Days)-1]
	inputDeleted := evaluationDeletion(in.Input, todayDay, today.Sleep)
	b := DailyBaseline{Schema: BaselineSchema, Date: today.Date, Timezone: c.Timezone, ConfigHash: c.Hash(),
		SDNN: baseline(history, today.SDNN, HRV, c), RHR: baseline(history, today.RHR, RHR, c), Sleep: baseline(history, today.Sleep.Component, Sleep, c)}
	r := V2Result{Result: Result{Schema: SchemaV2, Dataset: in.Dataset, DatasetEpoch: in.DatasetEpoch, Collector: in.Collector,
		Assignment: in.Assignment, Date: today.Date, Timezone: c.Timezone, CalendarHash: calHash, View: in.View,
		Status: "insufficient_data", Quality: "insufficient", BaselineState: "established", ConfidenceMeaning: "evidence_support_not_probability",
		ModelVersion: ModelV2, ConfigHash: c.Hash(), Generation: in.Generation, AsOf: in.AsOf, CapturedAt: in.CapturedAt,
		KnownThroughRevision: in.KnownThroughRevision, InputHash: InputHash(in.Input), FactRefs: []Ref{}, TombstoneRefs: []Ref{},
		Coverage: in.Coverage, Reasons: append([]string{}, today.Reasons...)}}
	for _, f := range in.Facts {
		r.FactRefs = append(r.FactRefs, Ref{f.Type, f.ID, f.Hash})
	}
	for _, t := range in.Tombstones {
		r.TombstoneRefs = append(r.TombstoneRefs, Ref{t.Type, t.ID, t.Hash})
	}
	if !coverageComplete {
		r.Reasons = append(r.Reasons, "daily_coverage_incomplete")
	}
	if inputDeleted {
		r.Reasons = append(r.Reasons, "input_deleted")
	}
	r.SDNNFreshness = freshness(today.SDNN, in.AsOf, c.FreshHRVUs)
	r.RHRFreshness = freshness(today.RHR, in.AsOf, c.FreshRHRUs)
	r.SleepFreshness = freshness(today.Sleep.Component, in.AsOf, c.FreshSleepUs)
	r.SleepRegularity = regularity(history, historyDays, today.Sleep, todayDay)
	r.SleepTarget = sleepTargetValue(in.SleepTargets, history, in.AsOf)
	usable := 0
	if today.SDNN.Value != nil {
		usable++
	}
	if today.RHR.Value != nil {
		usable++
	}
	if today.Sleep.Value != nil {
		usable++
	}
	if b.SDNN.State == "collecting" || b.RHR.State == "collecting" {
		r.BaselineState = "collecting"
		r.Status = "collecting_baseline"
		r.Reasons = append(r.Reasons, "baseline_insufficient")
	}
	var zh, zr float64
	autAvailable := today.SDNN.Value != nil && today.RHR.Value != nil && b.SDNN.State != "collecting" && b.RHR.State != "collecting"
	if autAvailable {
		zh = (*b.SDNN.Center - math.Log(*today.SDNN.Value)) / *b.SDNN.Sigma
		zr = (*today.RHR.Value - *b.RHR.Center) / *b.RHR.Sigma
		if !finite(zh) || !finite(zr) {
			return V2Result{}, ErrInvalid
		}
		if zh <= -3 {
			r.ContextShift = true
			r.Reasons = append(r.Reasons, "context_shift")
		}
		aH := clip((zh-1)/2, 0, c.ComponentClip)
		aR := clip((zr-1)/2, 0, c.ComponentClip)
		if r.ContextShift {
			aH = 0
		}
		hc := c.SecondaryWeight * aH
		rc := c.SecondaryWeight * aR
		if aH >= aR {
			hc, rc = c.DominantWeight*aH, c.SecondaryWeight*aR
		} else {
			hc, rc = c.SecondaryWeight*aH, c.DominantWeight*aR
		}
		aut := hc + rc
		r.SDNNDeviation = &Deviation{Raw: zh, Clipped: clip(zh, -c.ZClip, c.ZClip), Burden: aH}
		r.RHRDeviation = &Deviation{Raw: zr, Clipped: clip(zr, -c.ZClip, c.ZClip), Burden: aR}
		r.SDNNContribution, r.RHRContribution = &hc, &rc
		r.AutonomicBurden = &aut
		sleepReady := today.Sleep.Value != nil && r.SleepRegularity.Quality == "complete" && !r.SleepTarget.TargetUnavailable && coverageComplete
		if sleepReady {
			aS := clip(math.Max(0, r.SleepTarget.Hours-*today.Sleep.Value)/c.SleepDeficitHours+0.25*math.Max(0, r.SleepRegularity.DeviationHours-1), 0, c.SleepClip)
			sb := c.SleepWeight * aS
			total := aut + sb
			r.SleepDeviation = &Deviation{Raw: aS, Clipped: aS, Burden: aS}
			r.SleepContribution, r.SleepBurden, r.TotalBurden = &sb, &sb, &total
			r.Score = ptr(100 * math.Exp(-total))
			r.Status = "available"
			r.Quality = "complete"
			support := math.Min(1, float64(usable)/3) * math.Min(1, math.Min(float64(b.SDNN.UsableDays), float64(b.RHR.UsableDays))/c.SupportDays)
			if r.SDNNFreshness.LastEvent != nil {
				support *= math.Exp(-float64(in.AsOf-*r.SDNNFreshness.LastEvent) / float64(c.ConfidenceDecayUs))
			}
			r.Confidence = c.ConfidenceCap * support
			if r.BaselineState == "provisional" {
				r.Confidence = math.Min(r.Confidence, c.ProvisionalCap)
			}
		} else {
			r.AutonomicOnlyV2 = ptr(100 * math.Exp(-aut))
			r.AutonomicOnly = r.AutonomicOnlyV2
			r.Status = "partial"
			r.Quality = "partial"
			r.Confidence = math.Min(0.5, c.ConfidenceCap*math.Min(1, float64(usable)/3))
			r.Reasons = append(r.Reasons, "sleep_component_unavailable")
		}
	} else {
		r.Reasons = append(r.Reasons, "autonomic_inputs_unavailable")
	}
	// Persistent adverse deviation: exactly the last three consecutive valid
	// local days for the same component.
	var pSDNN, pRHR, pSleep []bool
	for i := len(features) - 3; i < len(features); i++ {
		if i < 0 || i >= len(features) {
			continue
		}
		adverseH, adverseR, adverseS := adverse(features[i], b, r.SleepTarget)
		if features[i].SDNN.Value == nil || features[i].RHR.Value == nil {
			continue
		}
		pSDNN = append(pSDNN, adverseH)
		pRHR = append(pRHR, adverseR)
		pSleep = append(pSleep, adverseS)
	}
	if len(pSDNN) == 3 && pSDNN[0] && pSDNN[1] && pSDNN[2] {
		r.PersistentDeviation = append(r.PersistentDeviation, "sdnn")
	}
	if len(pRHR) == 3 && pRHR[0] && pRHR[1] && pRHR[2] {
		r.PersistentDeviation = append(r.PersistentDeviation, "rhr")
	}
	if len(pSleep) == 3 && pSleep[0] && pSleep[1] && pSleep[2] {
		r.PersistentDeviation = append(r.PersistentDeviation, "sleep")
	}
	sort.Strings(r.PersistentDeviation)
	if r.ContextShift {
		r.Confidence *= c.UnknownContextFactor
	}
	if inputDeleted {
		r.Confidence *= c.UnknownContextFactor
	}
	if today.SDNN.ContextIncomplete || today.Sleep.ContextIncomplete {
		r.Confidence *= c.UnknownContextFactor
		r.Reasons = append(r.Reasons, "source_context_incomplete")
	}
	if r.SleepRegularity.Ambiguous {
		r.Reasons = append(r.Reasons, "regularity_ambiguous")
	}
	if len(today.Sleep.Reasons) > 0 {
		r.Confidence *= c.SleepUncertaintyFactor
	}
	for _, x := range []Component{today.SDNN, today.RHR, today.Sleep.Component} {
		if x.Quality == "source_conflict" {
			r.Status = "source_conflict"
			r.Score = nil
			r.AutonomicOnlyV2 = nil
			r.AutonomicOnly = nil
			r.Confidence = 0
		}
		if x.Value == nil && x.Quality != "source_conflict" {
			r.Reasons = append(r.Reasons, x.Reasons...)
		}
	}
	if r.Score != nil && (!finite(*r.Score) || *r.Score < 0 || *r.Score > 100) {
		return V2Result{}, ErrInvalid
	}
	if deprioritized {
		r.Reasons = append(r.Reasons, "source_deprioritized")
	}
	persistent := len(r.PersistentDeviation) > 0
	if r.Score != nil && r.TotalBurden != nil {
		r.DeviationLevel = deviationLevel(*r.TotalBurden, persistent)
	} else if r.AutonomicOnlyV2 != nil && r.AutonomicBurden != nil {
		r.DeviationLevel = deviationLevel(*r.AutonomicBurden, persistent)
	}
	// Deduplicate and sort reason codes.
	seen := map[string]bool{}
	var rs []string
	for _, x := range r.Reasons {
		if x != "" && !seen[x] && !strings.HasPrefix(x, "sleep_missing") {
			seen[x] = true
			rs = append(rs, x)
		}
	}
	sort.Strings(rs)
	r.Reasons = rs
	return r, nil
}
