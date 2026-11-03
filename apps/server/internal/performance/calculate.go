package performance

import (
	"fmt"
	"math"
	"sort"
	"time"
)

func Calculate(in Input) (Result, error) {
	es, err := Extract(in)
	if err != nil {
		return Result{}, err
	}
	r := Result{Schema: Schema, DatasetID: in.DatasetID, Generation: in.Generation, Model: AbilityModel, Version: Version, ConfigHash: ConfigHash(), AsOf: in.AsOf, View: in.View, EvidenceMode: Mode, MaximalSupported: "unavailable", Status: "collecting", Freshness: "unknown", Stability: "not_evaluated", Targets: []Target{}, Evidence: []WeightedEvidence{}, ReasonCodes: []string{}}
	if len(es) == 0 {
		r.ReasonCodes = append(r.ReasonCodes, "no_qualified_performance")
		return r, nil
	}
	latest := int64(0)
	for _, e := range es {
		if e.WorkoutEnd > latest {
			latest = e.WorkoutEnd
		}
	}
	anchor := date(time.UnixMicro(latest), in.Calendar)
	latestPool, accepted := pool(es, anchor, in.AsOf, in.View)
	if accepted.LatestEvidence == 0 {
		r.Evidence = latestPool
		r.ReasonCodes = append(r.ReasonCodes, "no_confirmed_performance")
		return r, nil
	}
	anchor = date(time.UnixMicro(accepted.LatestEvidence), in.Calendar)
	r.AnchorDay = anchor.Format("2006-01-02")
	// Loader supplies only [anchor-102d, anchor+1d); reject accidental lifetime input.
	begin := anchor.AddDate(0, 0, -102).UnixMicro()
	filtered := []Evidence{}
	for _, e := range es {
		if e.WorkoutEnd >= begin {
			filtered = append(filtered, e)
		}
	}
	es = filtered
	targetDays := map[string]bool{}
	for _, e := range es {
		d := date(time.UnixMicro(e.WorkoutEnd), in.Calendar)
		if !d.Before(anchor.AddDate(0, 0, -13)) {
			targetDays[d.Format("2006-01-02")] = true
		}
	}
	ds := []string{}
	for d := range targetDays {
		ds = append(ds, d)
	}
	sort.Strings(ds)
	alpha, gamma, sum := 0.0, 0.0, 0.0
	for _, d := range ds {
		h, parseErr := time.ParseInLocation("2006-01-02", d, in.Calendar)
		if parseErr != nil {
			return Result{}, invalid()
		}
		pool, s := pool(es, h, in.AsOf, in.View)
		newEvidence := false
		for _, e := range pool {
			if e.Weight > 0 && date(time.UnixMicro(e.WorkoutEnd), in.Calendar).Equal(h) {
				newEvidence = true
				break
			}
		}
		if !newEvidence {
			continue
		}
		t := Target{Day: d, View: in.View, Status: "collecting", Support: s}
		switch {
		case s.LongestDuration < 2400 || s.LongWorkouts < 2:
			t.Status = "insufficient_long_evidence"
		case s.IndependentWorkouts < 5 || s.ShortWorkouts < 2 || s.HistorySpan < 14:
			t.Status = "collecting"
		case s.LatestEvidence == 0 || h.AddDate(0, 0, 1).UnixMicro()-s.LatestEvidence > 14*86400e6:
			t.Status = "insufficient_data"
		default:
			t.Curve, t.Status = fitCurve(pool, true)
		}
		r.Targets = append(r.Targets, t)
		if t.Status == "available" {
			w := math.Exp(-float64(days(anchor, h)) / 14)
			alpha += w * math.Log(t.Curve.D30)
			gamma += w * t.Curve.Gamma
			sum += w
			r.ValidTargetDays++
		}
		if d == r.AnchorDay {
			r.Support = s
			r.Curve = t.Curve
			r.Evidence = pool
			r.Status = t.Status
		}
	}
	// An isolated PB on a later day must remain inspectable as quarantined,
	// while neither advancing the smoothing anchor nor creating a target day.
	seenEvidence := map[string]bool{}
	for _, e := range r.Evidence {
		seenEvidence[e.WorkoutID+"/"+fmt.Sprint(e.Duration)] = true
	}
	for _, e := range latestPool {
		key := e.WorkoutID + "/" + fmt.Sprint(e.Duration)
		if e.State == "confirmation_required" && !seenEvidence[key] {
			r.Evidence = append(r.Evidence, e)
		}
	}
	sort.Slice(r.Evidence, func(i, j int) bool {
		if r.Evidence[i].WorkoutID != r.Evidence[j].WorkoutID {
			return r.Evidence[i].WorkoutID < r.Evidence[j].WorkoutID
		}
		return r.Evidence[i].Duration < r.Evidence[j].Duration
	})
	if r.Status == "available" {
		r.Stability = "stable"
		if r.ValidTargetDays < 3 {
			r.Status = "collecting"
			r.ReasonCodes = append(r.ReasonCodes, "insufficient_target_days")
		} else {
			a := ability(alpha/sum, gamma/sum)
			r.Ability = &a
		}
	} else {
		if r.Status == "unstable" {
			r.Stability = "unstable"
		}
		r.ReasonCodes = append(r.ReasonCodes, r.Status)
	}
	return Freshen(r, in.AsOf), nil
}
