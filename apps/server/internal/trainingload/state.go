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
	Tau float64 `json:"tau"`
	// MaxUnknownMass is the largest unknown-day weight the bounded (v2) state
	// values by imputation. Zero, the frozen v1 state, keeps any unknown day in
	// the ring fail-closed.
	MaxUnknownMass float64             `json:"maxUnknownMass,omitempty"`
	Ring           [Horizon]DailyInput `json:"ring"`
	Position       int                 `json:"position"`
	Known          float64             `json:"known"`
	UnknownMass    float64             `json:"unknownMass"`
	UnknownDays    int                 `json:"unknownDays"`
	PartialDays    int                 `json:"partialDays"`
	NonzeroDays    int                 `json:"nonzeroDays"`
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
	} else if k.UnknownMass <= k.MaxUnknownMass {
		if mean, ok := k.knownTrainingDayMean(); ok {
			v.ImputedContribution = k.UnknownMass * mean
			v.Value = ptr(k.Known + v.ImputedContribution)
		}
	}
	return v
}

// knownTrainingDayMean is the mean known load of ring days with a nonzero
// known load. It reads only the ring, so a bounded rebuild that seeds the full
// horizon reproduces it exactly; no state outside the horizon is consulted.
func (k *Kernel) knownTrainingDayMean() (float64, bool) {
	sum, n := 0.0, 0
	for _, x := range k.Ring {
		if x.Known != 0 {
			sum += x.Known
			n++
		}
	}
	if n == 0 {
		return 0, false
	}
	return sum / float64(n), true
}

// Unknown-day weight the bounded (v2) state may impute. One unknown training
// day in the last week stays below the acute limit; several do not.
const (
	AcuteUnknownMassMax   = .15
	ChronicUnknownMassMax = .10
)

// CalculateLoadState is the frozen v1 state: any unknown day within the
// 180-day ring leaves acute and chronic unavailable.
func CalculateLoadState(in StateInput) (StateResult, error) {
	return calculateLoadState(in, false)
}

func calculateLoadState(in StateInput, bounded bool) (StateResult, error) {
	out, _, err := runLoadState(in, bounded, nil)
	return out, err
}

type dayAggregate struct {
	known      float64
	unknown, n int
	partial    bool
}

// stateRun is the carried state of a load-state computation: both kernels and
// the 7-day ring of chronic values behind Trend7.
type stateRun struct {
	Acute    Kernel         `json:"acute"`
	Chronic  Kernel         `json:"chronic"`
	Prior    [7]KernelValue `json:"prior"`
	PriorSet [7]bool        `json:"priorSet"`
	Index    int            `json:"index"`
}

func newStateRun(bounded bool) stateRun {
	r := stateRun{Acute: Kernel{Tau: 7}, Chronic: Kernel{Tau: 42}}
	if bounded {
		r.Acute.MaxUnknownMass, r.Chronic.MaxUnknownMass = AcuteUnknownMassMax, ChronicUnknownMassMax
	}
	return r
}

func (r *stateRun) advance(d, history time.Time, v dayAggregate) DailyState {
	date := d.Format("2006-01-02")
	input := DailyInput{v.known, v.unknown > 0, v.partial}
	av, cv := r.Acute.Advance(input), r.Chronic.Advance(input)
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
		if ds.HistoryDaysObserved <= 42 || r.Acute.PartialDays > 0 || r.Chronic.PartialDays > 0 {
			ds.Quality = "partial"
		}
		if av.ImputedContribution > 0 || cv.ImputedContribution > 0 {
			ds.Imputed = true
			ds.Quality = "partial"
		}
	}
	slot := r.Index % 7
	if r.PriorSet[slot] && cv.Value != nil && r.Prior[slot].Value != nil {
		ds.Trend7 = ptr((*cv.Value - *r.Prior[slot].Value) / 7)
	}
	r.Prior[slot] = cv
	r.PriorSet[slot] = true
	r.Index++
	return ds
}

type parsedState struct {
	loc                    *time.Location
	from, through, history time.Time
	loads                  map[string]dayAggregate
}

func parseState(in StateInput) (parsedState, error) {
	var p parsedState
	loc, err := resolvedCalendar(in.Timezone, in.Calendar)
	if err != nil {
		return p, invalid()
	}
	p.loc = loc
	if p.from, err = time.ParseInLocation("2006-01-02", in.From, loc); err != nil {
		return p, invalid()
	}
	p.through, err = time.ParseInLocation("2006-01-02", in.Through, loc)
	if err != nil || p.through.Before(p.from) || p.through.After(p.from.AddDate(0, 0, MaxRecomputeDays-1)) {
		return p, invalid()
	}
	if p.history, err = time.ParseInLocation("2006-01-02", in.HistoryFrom, loc); err != nil {
		return p, invalid()
	}
	p.loads = map[string]dayAggregate{}
	seen := map[string]bool{}
	for _, x := range in.Loads {
		stamp, e := time.ParseInLocation("2006-01-02", x.Day, loc)
		if e != nil || stamp.Before(p.from.AddDate(0, 0, -186)) || stamp.After(p.through) {
			return p, invalid()
		}
		key := x.WorkoutID + ":" + x.Day
		if seen[key] {
			return p, invalid()
		}
		seen[key] = true
		v := p.loads[x.Day]
		v.n++
		if x.Load == nil {
			v.unknown++
		} else {
			if !finite(*x.Load) || *x.Load < 0 {
				return p, invalid()
			}
			v.known += *x.Load
		}
		if x.Quality != "complete" {
			v.partial = true
		}
		p.loads[x.Day] = v
	}
	return p, nil
}

// runLoadState computes the state; from a seed run it advances only the days
// after seedDay, otherwise from 186 days before From.
func runLoadState(in StateInput, bounded bool, seed *StateCheckpoint) (StateResult, stateRun, error) {
	out := StateResult{Schema: StateSchema, DatasetID: in.DatasetID, Generation: in.Generation, Model: StateAlgorithm, Version: Version, ConfigHash: ConfigHash(), Scope: ScopeName, Unit: Unit + "/day", TrendUnit: Unit + "/day²", Days: []DailyState{}}
	p, err := parseState(in)
	if err != nil {
		return out, stateRun{}, err
	}
	run := newStateRun(bounded)
	start := p.from.AddDate(0, 0, -186)
	if seed != nil {
		run = seed.Run
		day, err := time.ParseInLocation("2006-01-02", seed.Day, p.loc)
		if err != nil {
			return out, stateRun{}, invalid()
		}
		start = day.AddDate(0, 0, 1)
	}
	for d := start; !d.After(p.through); d = d.AddDate(0, 0, 1) {
		ds := run.advance(d, p.history, p.loads[d.Format("2006-01-02")])
		if !d.Before(p.from) {
			out.Days = append(out.Days, ds)
		}
	}
	return out, run, nil
}
