package trainingload

import (
	"math"
	"time"
)

type DailyInput struct {
	Known   float64 `json:"known"`
	Unknown bool    `json:"unknown"`
	Partial bool    `json:"partial"`
}

// Kernel is an O(1) finite daily advance with an explicit 180-day ring.
// Counts reset residual floating point dust exactly when the kernel is empty.
type Kernel struct {
	Tau         float64             `json:"tau"`
	Ring        [Horizon]DailyInput `json:"ring"`
	Position    int                 `json:"position"`
	Known       float64             `json:"known"`
	UnknownMass float64             `json:"unknownMass"`
	UnknownDays int                 `json:"unknownDays"`
	PartialDays int                 `json:"partialDays"`
	NonzeroDays int                 `json:"nonzeroDays"`
}

func (k *Kernel) Advance(x DailyInput) KernelValue {
	a := math.Exp(-1 / k.Tau)
	w := -math.Expm1(-1 / k.Tau)
	tail := w * math.Exp(-Horizon/k.Tau)
	old := k.Ring[k.Position]
	k.Ring[k.Position] = x
	k.Position = (k.Position + 1) % Horizon
	k.Known = a*k.Known + w*x.Known - tail*old.Known
	if old.Known != 0 {
		k.NonzeroDays--
	}
	if x.Known != 0 {
		k.NonzeroDays++
	}
	k.UnknownMass *= a
	if old.Unknown {
		k.UnknownDays--
		k.UnknownMass -= tail
	}
	if x.Unknown {
		k.UnknownDays++
		k.UnknownMass += w
	}
	if old.Partial {
		k.PartialDays--
	}
	if x.Partial {
		k.PartialDays++
	}
	if k.UnknownDays == 0 {
		k.UnknownMass = 0
	}
	if k.NonzeroDays == 0 {
		k.Known = 0
	}
	v := KernelValue{KnownContribution: k.Known, UnknownWeightMass: k.UnknownMass}
	if k.UnknownDays == 0 {
		v.Value = ptr(k.Known)
	}
	return v
}

func CalculateLoadState(in StateInput) (StateResult, error) {
	out := StateResult{Schema: StateSchema, DatasetID: in.DatasetID, Generation: in.Generation, Model: StateAlgorithm, Version: Version, ConfigHash: ConfigHash(), Scope: ScopeName, Unit: Unit + "/day", TrendUnit: Unit + "/day²", Days: []DailyState{}}
	loc, err := resolvedCalendar(in.Timezone, in.Calendar)
	if err != nil {
		return out, invalid()
	}
	from, err := time.ParseInLocation("2006-01-02", in.From, loc)
	if err != nil {
		return out, invalid()
	}
	through, err := time.ParseInLocation("2006-01-02", in.Through, loc)
	if err != nil || through.Before(from) || through.After(from.AddDate(0, 0, MaxRecomputeDays-1)) {
		return out, invalid()
	}
	history, err := time.ParseInLocation("2006-01-02", in.HistoryFrom, loc)
	if err != nil {
		return out, invalid()
	}
	type aggregate struct {
		known      float64
		unknown, n int
		partial    bool
	}
	loads := map[string]aggregate{}
	seen := map[string]bool{}
	for _, x := range in.Loads {
		stamp, e := time.ParseInLocation("2006-01-02", x.Day, loc)
		if e != nil || stamp.Before(from.AddDate(0, 0, -186)) || stamp.After(through) {
			return out, invalid()
		}
		key := x.WorkoutID + ":" + x.Day
		if seen[key] {
			return out, invalid()
		}
		seen[key] = true
		v := loads[x.Day]
		v.n++
		if x.Load == nil {
			v.unknown++
		} else {
			if !finite(*x.Load) || *x.Load < 0 {
				return out, invalid()
			}
			v.known += *x.Load
		}
		if x.Quality != "complete" {
			v.partial = true
		}
		loads[x.Day] = v
	}
	acute, chronic := Kernel{Tau: 7}, Kernel{Tau: 42}
	var prior [7]KernelValue
	var priorSet [7]bool
	index := 0
	for d := from.AddDate(0, 0, -186); !d.After(through); d = d.AddDate(0, 0, 1) {
		date := d.Format("2006-01-02")
		v := loads[date]
		input := DailyInput{v.known, v.unknown > 0, v.partial}
		av, cv := acute.Advance(input), chronic.Advance(input)
		ds := DailyState{Day: date, KnownRecordedLoad: v.known, UnknownWorkouts: v.unknown, RecordedWorkouts: v.n, Acute: av, Chronic: cv, Quality: "complete", InitialState: "zero_before_recorded_history"}
		if !d.Before(history) {
			// Counting calendar dates also handles 23/25-hour days exactly.
			a := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
			b := time.Date(history.Year(), history.Month(), history.Day(), 0, 0, 0, 0, time.UTC)
			ds.HistoryDaysObserved = int(a.Sub(b).Hours()/24) + 1
		}
		if v.unknown == 0 {
			ds.RecordedLoad = ptr(v.known)
		}
		if av.Value == nil || cv.Value == nil {
			ds.Quality = "insufficient"
		} else {
			ds.Balance = ptr(*cv.Value - *av.Value)
			if *cv.Value > 1 {
				ds.Ratio = ptr(*av.Value / *cv.Value)
			}
			if ds.HistoryDaysObserved <= 42 || acute.PartialDays > 0 || chronic.PartialDays > 0 {
				ds.Quality = "partial"
			}
		}
		slot := index % 7
		if priorSet[slot] && cv.Value != nil && prior[slot].Value != nil {
			ds.Trend7 = ptr((*cv.Value - *prior[slot].Value) / 7)
		}
		prior[slot] = cv
		priorSet[slot] = true
		index++
		if !d.Before(from) {
			out.Days = append(out.Days, ds)
		}
	}
	return out, nil
}
