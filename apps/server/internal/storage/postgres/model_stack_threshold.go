package postgres

import (
	"math"
	"time"

	lt "rai/server/internal/lactatethreshold"
	rf "rai/server/internal/runfitness"
)

// thresholdAnchor maps the day's running-fitness state to the typed anchor of
// the lactate threshold. The pace stays the running-fitness vT; nothing here
// refits a curve.
func thresholdAnchor(state rf.State, obs []rf.SingleResult, day string) *lt.Anchor {
	pr := state.Params
	if state.Status == "conflict" || state.Overall.Basis == "conflict" {
		if pr.VT == nil {
			return &lt.Anchor{Basis: "conflict", SpeedMPS: 1, InputHash: state.InputHash}
		}
		return &lt.Anchor{Basis: "conflict", SpeedMPS: *pr.VT, InputHash: state.InputHash}
	}
	if pr.VT == nil || !(*pr.VT > 0) || pr.V6 == nil || pr.BL == nil || state.Freshness == "expired" {
		return nil
	}
	curve := rf.Curve{VT: *pr.VT, V6: *pr.V6, BL: *pr.BL}
	a := &lt.Anchor{SpeedMPS: curve.VT, Speed60MPS: curve.Speed(3600), Freshness: state.Freshness, EvidenceDay: state.LatestEvidenceDay, InputHash: state.InputHash, Basis: "prior_based"}
	if r := state.Dimensions.Threshold.Range; r != nil && r.Low > 0 && r.High >= r.Low {
		a.SpeedLowMPS, a.SpeedHighMPS = r.Low, r.High
	}
	for _, o := range obs {
		age, ok := dayAge(day, o.Day)
		if !ok || age < 0 {
			continue
		}
		if o.Race != nil && age < 90 && (o.Race.Status == "supported" || o.Race.Status == "limited") {
			// A 20-60 minute confirmed maximal performance calibrates the
			// working pace (section 8.3); any other confirmed performance at
			// least ties the curve to real running.
			if o.Race.Status == "supported" && o.Race.Seconds >= 1200 && o.Race.Seconds <= 3600 &&
				(o.Race.Kind == "race" || o.Race.Kind == "maximal_time_trial" || o.Race.Kind == "threshold_test") {
				a.Basis = "field_calibrated"
			} else if a.Basis == "prior_based" {
				a.Basis = "estimated"
			}
		}
		if age < 42 && a.Basis == "prior_based" {
			for _, d := range o.Demonstrated {
				if (d.Duration == 1200 || d.Duration == 2400) && d.Speed >= .95*curve.Speed(float64(d.Duration)) {
					a.Basis = "estimated"
				}
			}
		}
	}
	return a
}

func dayAge(evaluation, day string) (int, bool) {
	e, err1 := time.Parse(time.DateOnly, evaluation)
	d, err2 := time.Parse(time.DateOnly, day)
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return int(math.Round(e.Sub(d).Hours() / 24)), true
}

// stackLactateThreshold evaluates the day's working threshold from the same
// Workouts, source and labels the running-fitness state used.
func stackLactateThreshold(dataset string, generation int64, date, at time.Time, pending []stackPendingLoad, ambiguous map[string]bool, state rf.State, detail fitnessDetail) lt.Result {
	loc := date.Location()
	day := date.Format(time.DateOnly)
	first := date.AddDate(0, 0, -(lt.DefaultPolicy().WindowDays - 1)).Format(time.DateOnly)
	in := lt.Input{DatasetID: dataset, Date: day, Generation: generation, AsOf: at.UnixMicro(), View: "revised", FitnessInputHash: state.InputHash, Anchor: thresholdAnchor(state, detail.obs, day)}
	for _, p := range pending {
		w := p.w
		f := w.feature.RunFitness
		if p.r.failure != "" || ambiguous[w.id] || f == nil || detail.source != "" && w.sourceEpoch != detail.source {
			continue
		}
		d := time.UnixMicro(w.start).In(loc).Format(time.DateOnly)
		if d < first || d > day {
			continue
		}
		x := lt.Workout{ID: w.id, Day: d, Start: w.start, End: w.end, SourceEpoch: w.sourceEpoch, Indoor: f.Indoor, TemperatureC: f.TemperatureC, Humidity: f.Humidity,
			DistanceCheck: f.DistanceCheck, Windows: make([]lt.Window, 0, len(f.Segments))}
		if ref, conflict := fitnessReference(p.r.ref, p.r.refConflict); ref != nil && !conflict {
			x.Reference = &lt.Reference{MaxHR: ref.MaxHR, RestHR: ref.RestHR}
		}
		for _, s := range f.Segments {
			x.Windows = append(x.Windows, lt.Window{Kind: s.Kind, Start: s.Start, End: s.End, Seconds: s.Seconds, RunningAt: s.RunningAt, Speed: s.Speed, FlatSpeed: s.FlatSpeed,
				MaxAbsGrade: s.MaxAbsGrade, GradeSupport: s.GradeSupport, HR: s.HR, HRTrend: s.HRTrend, SpeedCV: s.SpeedCV, Coverage: s.Coverage, SpeedEpoch: s.SpeedEpoch, HREpoch: s.HREpoch})
		}
		if e, ok := detail.labels[w.id]; ok && e.Kind == "maximal_time_trial" && e.Segment != nil {
			from := w.start + int64(e.Segment.StartOffset*1e6)
			x.TimeTrial = &lt.Span{Start: from, End: from + int64(e.Segment.Duration*1e6)}
		}
		in.Workouts = append(in.Workouts, x)
	}
	return lt.Calculate(in)
}
