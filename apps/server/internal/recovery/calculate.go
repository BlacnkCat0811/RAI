package recovery

import "math"

func baseline(history []DailyHealthFeatures, today Component, kind string, c Config) Baseline {
	b := Baseline{BaselineDays: c.Window, State: "collecting", Epoch: today.Epoch}
	xs := []float64{}
	if today.Epoch == "" {
		return b
	}
	for i, f := range history {
		v := f.SDNN
		if kind == RHR {
			v = f.RHR
		}
		if kind == Sleep {
			v = f.Sleep.Component
		}
		if v.Value == nil || v.Epoch != today.Epoch {
			continue
		}
		x := *v.Value
		if kind == HRV {
			x = math.Log(x)
		}
		xs = append(xs, x)
		if i >= len(history)-c.RecentWindow {
			b.RecentDays++
		}
	}
	b.UsableDays = len(xs)
	if len(xs) == 0 {
		return b
	}
	center := median(xs)
	ds := []float64{}
	for _, x := range xs {
		ds = append(ds, math.Abs(x-center))
	}
	mad := median(ds)
	floor := c.LogFloor
	if kind == RHR {
		floor = c.RHRFloor
	}
	if kind == Sleep {
		floor = c.SleepFloor
	}
	sigma := c.MADFactor * mad
	b.FloorApplied = sigma < floor
	sigma = math.Max(sigma, floor)
	b.Center = &center
	b.MAD = &mad
	b.Sigma = &sigma
	if len(xs) >= c.Minimum && b.RecentDays >= c.RecentMinimum {
		b.State = "provisional"
		if len(xs) >= c.Established {
			b.State = "established"
		}
	}
	return b
}
func clip(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }
func Calculate(in Input) (Bundle, error) {
	if e := Validate(in); e != nil {
		return Bundle{}, e
	}
	in = canonical(in)
	fs := eligible(in)
	c := in.Config
	out := Bundle{Features: []DailyHealthFeatures{}}
	calHash := Hash(in.Days)
	for _, d := range in.Days {
		f := DailyHealthFeatures{Schema: FeatureSchema, Date: d.Date, Timezone: c.Timezone, CalendarHash: calHash, SDNN: quantity(fs, d, HRV), RHR: quantity(fs, d, RHR), Sleep: sleepDaily(fs, d, c, in.AsOf), Quality: "complete", Reasons: []string{}}
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
		out.Features = append(out.Features, f)
	}
	today := out.Features[len(out.Features)-1]
	history := out.Features[:len(out.Features)-1]
	b := DailyBaseline{Schema: BaselineSchema, Date: today.Date, Timezone: c.Timezone, ConfigHash: c.Hash(), SDNN: baseline(history, today.SDNN, HRV, c), RHR: baseline(history, today.RHR, RHR, c), Sleep: baseline(history, today.Sleep.Component, Sleep, c)}
	out.Baseline = b
	r := Result{Schema: Schema, Dataset: in.Dataset, DatasetEpoch: in.DatasetEpoch, Collector: in.Collector, Assignment: in.Assignment, Date: today.Date, Timezone: c.Timezone, CalendarHash: calHash, View: in.View, Status: "insufficient_data", Quality: "insufficient", BaselineState: "established", ConfidenceMeaning: "evidence_support_not_probability", ModelVersion: Model, ConfigHash: c.Hash(), Generation: in.Generation, AsOf: in.AsOf, CapturedAt: in.CapturedAt, KnownThroughRevision: in.KnownThroughRevision, InputHash: InputHash(in), FactRefs: []Ref{}, TombstoneRefs: []Ref{}, Coverage: in.Coverage, Reasons: append([]string{}, today.Reasons...)}
	for _, f := range in.Facts {
		r.FactRefs = append(r.FactRefs, Ref{f.Type, f.ID, f.Hash})
	}
	for _, t := range in.Tombstones {
		r.TombstoneRefs = append(r.TombstoneRefs, Ref{t.Type, t.ID, t.Hash})
	}
	if len(in.Tombstones) > 0 {
		r.Reasons = append(r.Reasons, "input_deleted")
	}
	components := []Component{today.SDNN, today.RHR, today.Sleep.Component}
	baselines := []Baseline{b.SDNN, b.RHR, b.Sleep}
	limits := []int64{c.FreshHRVUs, c.FreshRHRUs, c.FreshSleepUs}
	r.SDNNFreshness = freshness(today.SDNN, in.AsOf, c.FreshHRVUs)
	r.RHRFreshness = freshness(today.RHR, in.AsOf, c.FreshRHRUs)
	r.SleepFreshness = freshness(today.Sleep.Component, in.AsOf, c.FreshSleepUs)
	usable := 0
	support := 1.0
	conflict, stale, collecting := false, false, false
	for i, x := range components {
		base := baselines[i]
		if base.State == "collecting" {
			r.BaselineState = "collecting"
			collecting = true
		} else if base.State == "provisional" && r.BaselineState != "collecting" {
			r.BaselineState = "provisional"
		}
		if base.FloorApplied {
			r.Reasons = append(r.Reasons, "baseline_scale_floor")
		}
		if x.Quality == "source_conflict" {
			conflict = true
		}
		if x.Value == nil || base.State == "collecting" {
			support = 0
			continue
		}
		if x.LastEvent == nil || in.AsOf-*x.LastEvent > limits[i] {
			r.Reasons = append(r.Reasons, "input_stale")
			stale = true
			support = 0
			continue
		}
		usable++
		support = math.Min(support, math.Min(1, float64(base.UsableDays)/c.SupportDays)*math.Exp(-float64(in.AsOf-*x.LastEvent)/float64(c.ConfidenceDecayUs)))
		var dv Deviation
		switch i {
		case 0:
			dv.Raw = (*base.Center - math.Log(*x.Value)) / *base.Sigma
			dv.Clipped = clip(dv.Raw, -c.ZClip, c.ZClip)
			dv.Burden = clip((dv.Clipped-c.Deadband)/c.AutonomicScale, 0, c.ComponentClip)
			r.SDNNDeviation = &dv
		case 1:
			dv.Raw = (*x.Value - *base.Center) / *base.Sigma
			dv.Clipped = clip(dv.Raw, -c.ZClip, c.ZClip)
			dv.Burden = clip((dv.Clipped-c.Deadband)/c.AutonomicScale, 0, c.ComponentClip)
			r.RHRDeviation = &dv
		case 2:
			dv.Raw = (*base.Center - *x.Value) / c.SleepDeficitHours
			dv.Clipped = clip(dv.Raw, -c.SleepClip, c.SleepClip)
			dv.Burden = math.Max(0, dv.Clipped)
			r.SleepDeviation = &dv
		}
		if !finite(dv.Raw) {
			return Bundle{}, ErrInvalid
		}
	}
	r.ComponentCoverage = float64(usable) / 3
	coverage := map[string]bool{}
	coverageCounts := map[string]int{}
	for _, x := range in.Coverage {
		coverageCounts[x.Type]++
		coverage[x.Type] = x.Complete && in.AsOf-x.Through <= c.CoverageFreshUs && x.Through <= in.CapturedAt && (in.View != "as_known" || x.Through <= in.AsOf)
	}
	complete := coverage[HRV] && coverage[RHR] && coverage[Sleep] && coverageCounts[HRV] == 1 && coverageCounts[RHR] == 1 && coverageCounts[Sleep] == 1
	if !complete {
		r.Reasons = append(r.Reasons, "daily_coverage_incomplete")
	}
	if collecting {
		r.Status = "collecting_baseline"
		r.Reasons = append(r.Reasons, "baseline_insufficient")
	}
	if r.SDNNDeviation != nil && r.RHRDeviation != nil {
		h, rr := r.SDNNDeviation.Burden, r.RHRDeviation.Burden
		wh, wr := c.SecondaryWeight, c.DominantWeight
		if h >= rr {
			wh, wr = c.DominantWeight, c.SecondaryWeight
		}
		hc, rc := wh*h, wr*rr
		aut := hc + rc
		r.SDNNContribution = &hc
		r.RHRContribution = &rc
		r.AutonomicBurden = &aut
		if r.SleepDeviation != nil && complete {
			sb := c.SleepWeight * r.SleepDeviation.Burden
			total := aut + sb
			r.SleepContribution = &sb
			r.SleepBurden = &sb
			r.TotalBurden = &total
			r.Score = ptr(100 * math.Exp(-total))
			r.Status = "available"
			r.Quality = "complete"
			r.Confidence = c.ConfidenceCap * support
			if r.BaselineState == "provisional" {
				r.Confidence = math.Min(r.Confidence, c.ProvisionalCap)
			}
		} else {
			r.AutonomicOnly = ptr(100 * math.Exp(-aut))
			r.Status = "partial"
			r.Quality = "partial"
			r.Confidence = math.Min(c.PartialCap, c.ConfidenceCap*math.Min(float64(b.SDNN.UsableDays), float64(b.RHR.UsableDays))/c.SupportDays)
			r.Reasons = append(r.Reasons, "autonomic_only_full_score_unavailable")
		}
	}
	contextIncomplete := false
	for _, x := range components {
		contextIncomplete = contextIncomplete || x.ContextIncomplete
	}
	if contextIncomplete {
		r.Confidence *= c.UnknownContextFactor
		r.Reasons = append(r.Reasons, "source_context_incomplete")
	}
	if len(today.Sleep.Reasons) > 0 {
		r.Confidence *= c.SleepUncertaintyFactor
	}
	if conflict {
		r.Status = "source_conflict"
		r.Score = nil
		r.Confidence = 0
	}
	if stale {
		r.Status = "stale"
		r.Score = nil
		r.AutonomicOnly = nil
		r.Confidence = 0
	}
	if r.Score != nil && (!finite(*r.Score) || *r.Score < 0 || *r.Score > 100) {
		return Bundle{}, ErrInvalid
	}
	r.Reasons = reasons(r.Reasons...)
	out.Recovery = r
	return out, nil
}

// AffectedDays is the bounded dependency operator in local-calendar day indices.
func AffectedDays(first, last int, sleep bool, window int) (int, int) {
	if sleep {
		return first - 1, last + 2 + window
	}
	return first, last + window
}

func freshness(c Component, asOf, limit int64) Freshness {
	f := Freshness{LastEvent: c.LastEvent, LimitUs: limit, State: "missing"}
	if c.LastEvent != nil {
		age := asOf - *c.LastEvent
		f.AgeUs = &age
		f.State = "fresh"
		if age > limit {
			f.State = "stale"
		}
	}
	return f
}

// Freshen ages evidence support without improving the observed score. The
// immutable original artifact remains unchanged; each component ages separately.
func Freshen(r Result, at int64) (Result, error) {
	if at < r.AsOf {
		return Result{}, ErrInvalid
	}
	r.Reasons = append([]string{}, r.Reasons...)
	fs := []*Freshness{&r.SDNNFreshness, &r.RHRFreshness, &r.SleepFreshness}
	stale := false
	for _, f := range fs {
		if f.LastEvent == nil {
			continue
		}
		age := at - *f.LastEvent
		f.AgeUs = &age
		if age > f.LimitUs {
			f.State = "stale"
			stale = true
		}
	}
	r.Confidence *= math.Exp(-float64(at-r.AsOf) / float64(DefaultConfig(r.Timezone).ConfidenceDecayUs))
	if stale {
		r.Status = "stale"
		r.Score = nil
		r.AutonomicOnly = nil
		r.Confidence = 0
		r.Reasons = append(r.Reasons, "input_stale")
	}
	r.Reasons = reasons(r.Reasons...)
	return r, nil
}
