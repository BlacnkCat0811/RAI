package trainingload

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	wf "rai/server/internal/workoutfeatures"
	"sort"
	"time"
)

const FocusSchema = "rai.load_focus/1"
const FocusAlgorithm = "rai.load_focus"
const FocusPolicy = "hrr-proxy.70;short-repeat3x20-120s;pre-v60x1.15;native5s;measured-grade.03;unclassified-gap;local-days/v1"

type Buckets struct {
	Low          float64 `json:"lowAerobicAU"`
	High         float64 `json:"highAerobicAU"`
	Anaerobic    float64 `json:"anaerobicProxyAU"`
	Unclassified float64 `json:"unclassifiedAU"`
}

func (b Buckets) Total() float64 { return b.Low + b.High + b.Anaerobic + b.Unclassified }
func (b *Buckets) add(x Buckets) {
	b.Low += x.Low
	b.High += x.High
	b.Anaerobic += x.Anaerobic
	b.Unclassified += x.Unclassified
}

type FocusDay struct {
	Day     string  `json:"day"`
	Buckets Buckets `json:"buckets"`
}
type WorkoutFocus struct {
	WorkoutID string     `json:"workoutID"`
	KnownAU   *float64   `json:"knownAU"`
	Buckets   Buckets    `json:"buckets"`
	Seconds   Buckets    `json:"seconds"`
	Days      []FocusDay `json:"days"`
	Method    string     `json:"classificationMethod"`
	Reasons   []string   `json:"reasonCodes"`
	InputHash string     `json:"inputHash"`
}
type AbilityReference struct {
	V60  float64 `json:"v60"`
	At   int64   `json:"asOf"`
	Hash string  `json:"hash"`
	Mode string  `json:"mode"`
}

// ClassifyLoad integrates the same clamped native HR segments as the AU
// ledger. Missing/scaled exposure stays unclassified, even for complete tiers.
func ClassifyLoad(load Result, e wf.HRExposure, f wf.WorkoutFeatures, ref *Reference, pre *AbilityReference, loc *time.Location) (WorkoutFocus, error) {
	r := WorkoutFocus{WorkoutID: load.WorkoutID, KnownAU: load.Load, Method: "hrr_proxy", Reasons: []string{}, Days: []FocusDay{}}
	r.InputHash = Hash(struct {
		Load      Result
		Exposure  wf.HRExposure
		Structure *wf.StructureFeatures
		Reference *Reference
		Pre       *AbilityReference
		Policy    string
	}{load, e, f.Structure, ref, pre, FocusPolicy})
	if loc == nil {
		return r, ErrV2Invalid
	}
	byDay := map[string]*Buckets{}
	for _, d := range load.Days {
		byDay[d.Day] = &Buckets{}
	}
	if load.Load == nil {
		for _, d := range load.Days {
			r.Days = append(r.Days, FocusDay{Day: d.Day})
		}
		r.Method = "unavailable"
		r.Reasons = append(r.Reasons, "load_unknown")
		return r, nil
	}
	if !finite(*load.Load) || *load.Load < 0 {
		return r, ErrV2Invalid
	}
	precise := load.QualityTier != TierApproximate && ref != nil && ref.MaxHR > ref.RestHR && len(e.Segments) > 0
	fast := []wf.Interval{}
	if precise && pre != nil && pre.At < f.Timing.Start && pre.V60 > 0 && finite(pre.V60) && pre.Hash != "" && pre.Mode != "submax_estimated" && f.Structure != nil && f.Structure.Schema == wf.StructureSchema {
		var runs []wf.Interval
		for _, s := range f.Structure.Slices {
			if s.Speed < pre.V60*1.15 {
				continue
			}
			n := len(runs)
			if n > 0 && runs[n-1].End == s.Start {
				runs[n-1].End = s.End
			} else {
				runs = append(runs, wf.Interval{Start: s.Start, End: s.End})
			}
		}
		// Each repetition must have supported low-speed recovery between it and
		// the next one; prolonged high HR alone never creates anaerobic AU.
		var group []wf.Interval
		flush := func() {
			if len(group) >= 3 {
				fast = append(fast, group...)
			}
			group = nil
		}
		for _, run := range runs {
			dur := float64(run.End-run.Start) / 1e6
			if dur < 20 || dur > 120 {
				flush()
				continue
			}
			if len(group) > 0 {
				prev := group[len(group)-1]
				gap := run.Start - prev.End
				covered := int64(0)
				lowHR := false
				for _, s := range f.Structure.Slices {
					if s.Start >= prev.End && s.End <= run.Start && s.Speed < pre.V60*.9 {
						covered += s.End - s.Start
						lowHR = lowHR || s.HR < ref.RestHR+.8*(ref.MaxHR-ref.RestHR)
					}
				}
				if gap < 20e6 || gap > 300e6 || covered < gap*9/10 || !lowHR {
					flush()
				}
			}
			group = append(group, run)
		}
		flush()
	}
	if len(fast) > 0 {
		r.Method = "hrr_proxy+anaerobic_proxy"
	} else {
		r.Reasons = append(r.Reasons, "short_repetition_unsupported")
	}
	if precise {
		var previous int64
		for _, s := range e.Segments {
			if s.Start >= s.End || s.Start < previous || !finite(s.HR0) || !finite(s.HR1) {
				return r, ErrV2Invalid
			}
			previous = s.End
			cuts := []int64{s.Start, s.End}
			if s.HR0 != s.HR1 {
				t := float64(s.Start) + (.7*(ref.MaxHR-ref.RestHR)+ref.RestHR-s.HR0)/(s.HR1-s.HR0)*float64(s.End-s.Start)
				if t > float64(s.Start) && t < float64(s.End) {
					cuts = append(cuts, int64(t))
				}
			}
			for _, x := range fast {
				if x.Start > s.Start && x.Start < s.End {
					cuts = append(cuts, x.Start)
				}
				if x.End > s.Start && x.End < s.End {
					cuts = append(cuts, x.End)
				}
			}
			for a := s.Start; a < s.End; {
				t := time.UnixMicro(a).In(loc)
				b := min(s.End, time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1).UnixMicro())
				if b < s.End {
					cuts = append(cuts, b)
				}
				a = b
			}
			sort.Slice(cuts, func(i, j int) bool { return cuts[i] < cuts[j] })
			value := func(t int64) float64 {
				return (s.HR0 + (s.HR1-s.HR0)*float64(t-s.Start)/float64(s.End-s.Start) - ref.RestHR) / (ref.MaxHR - ref.RestHR)
			}
			for i := 1; i < len(cuts); i++ {
				a, b := cuts[i-1], cuts[i]
				if b <= a {
					continue
				}
				sec := float64(b-a) / 1e6
				v, _, _ := ClampedIntegral(value(a), value(b), sec)
				day := time.UnixMicro(a).In(loc).Format(time.DateOnly)
				bucket := byDay[day]
				if bucket == nil {
					return r, ErrV2Invalid
				}
				anaerobic := false
				for _, x := range fast {
					if a >= x.Start && b <= x.End {
						anaerobic = true
						break
					}
				}
				switch {
				case anaerobic:
					bucket.Anaerobic += v
					r.Seconds.Anaerobic += sec
				case (value(a)+value(b))/2 < .7:
					bucket.Low += v
					r.Seconds.Low += sec
				default:
					bucket.High += v
					r.Seconds.High += sec
				}
			}
		}
	} else {
		r.Method = "unclassified"
		r.Reasons = append(r.Reasons, "session_intensity_unknown")
	}
	for _, d := range load.Days {
		b := byDay[d.Day]
		if d.Load == nil {
			return r, ErrV2Invalid
		}
		extra := *d.Load - b.Total()
		if extra < -1e-8*math.Max(1, *d.Load) {
			return r, ErrV2Invalid
		}
		b.Unclassified = math.Max(0, extra)
		r.Buckets.add(*b)
		r.Days = append(r.Days, FocusDay{d.Day, *b})
	}
	if load.ActiveSeconds != nil {
		r.Seconds.Unclassified = math.Max(0, *load.ActiveSeconds-r.Seconds.Low-r.Seconds.High-r.Seconds.Anaerobic)
	}
	if math.Abs(r.Buckets.Total()-*load.Load) > 1e-8*math.Max(1, *load.Load) {
		return r, ErrV2Invalid
	}
	return r, nil
}

type FocusResult struct {
	Schema     string         `json:"schemaVersion"`
	Dataset    string         `json:"datasetID"`
	Stack      string         `json:"stackID"`
	Generation int64          `json:"generation"`
	AsOf       int64          `json:"asOf"`
	ConfigHash string         `json:"configHash"`
	InputHash  string         `json:"inputHash"`
	Windows    []FocusWindow  `json:"windows"`
	Workouts   []WorkoutFocus `json:"workouts"`
}
type FocusWindow struct {
	WindowDays         int      `json:"windowDays"`
	Buckets            Buckets  `json:"buckets"`
	UnknownWorkouts    int      `json:"unknownWorkouts"`
	ClassifiedCoverage *float64 `json:"classifiedCoverage"`
	Denominator        string   `json:"denominator"`
	Method             string   `json:"classificationMethod"`
}

func AggregateFocus(dataset, stack string, generation, at int64, day time.Time, workouts []WorkoutFocus) FocusResult {
	r := FocusResult{Schema: FocusSchema, Dataset: dataset, Stack: stack, Generation: generation, AsOf: at, ConfigHash: FocusConfigHash(), Workouts: workouts, InputHash: Hash(workouts)}
	for _, n := range []int{7, 28} {
		w := FocusWindow{WindowDays: n, Denominator: "known_load", Method: "hrr_proxy;anaerobic_proxy"}
		from, to := day.AddDate(0, 0, -n+1).Format(time.DateOnly), day.Format(time.DateOnly)
		for _, x := range workouts {
			unknown := false
			for _, d := range x.Days {
				if d.Day >= from && d.Day <= to {
					w.Buckets.add(d.Buckets)
					unknown = unknown || x.KnownAU == nil
				}
			}
			if unknown {
				w.UnknownWorkouts++
			}
		}
		if total := w.Buckets.Total(); total > 0 {
			v := (total - w.Buckets.Unclassified) / total
			w.ClassifiedCoverage = &v
		}
		r.Windows = append(r.Windows, w)
	}
	return r
}

func FocusConfigHash() string {
	h := sha256.Sum256([]byte(FocusPolicy))
	return hex.EncodeToString(h[:])
}
