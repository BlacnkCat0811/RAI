package trainingload

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
	"time"
)

func direct(xs []DailyInput, day int, tau float64) (known, mass float64) {
	a := math.Exp(-1 / tau)
	w := -math.Expm1(-1 / tau)
	for k := 0; k < 180 && k <= day; k++ {
		x := xs[day-k]
		known += w * math.Pow(a, float64(k)) * x.Known
		if x.Unknown {
			mass += w * math.Pow(a, float64(k))
		}
	}
	return
}
func TestFiniteKernelProperty400PlusDays(t *testing.T) {
	rng := rand.New(rand.NewSource(180))
	for trial := 0; trial < 25; trial++ {
		xs := make([]DailyInput, 480)
		for i := range xs {
			xs[i] = DailyInput{rng.Float64() * 180, rng.Intn(11) == 0, rng.Intn(7) == 0}
		}
		for _, tau := range []float64{7, 42} {
			k := Kernel{Tau: tau}
			for d, x := range xs {
				v := k.Advance(x)
				known, mass := direct(xs, d, tau)
				close(t, v.KnownContribution, known, 2e-12)
				close(t, v.UnknownWeightMass, mass, 2e-14)
				if (v.Value == nil) != (mass > 0) {
					t.Fatal("unknown lost")
				}
			}
		}
	}
	k := Kernel{Tau: 42}
	k.Advance(DailyInput{Known: 60, Unknown: true})
	for i := 1; i <= 180; i++ {
		v := k.Advance(DailyInput{})
		if i == 180 && (v.KnownContribution != 0 || v.UnknownWeightMass != 0 || v.Value == nil) {
			t.Fatal("infinite EWMA tail")
		}
	}
}
func athlete(kind string, days int) StateInput {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	in := StateInput{DatasetID: "synthetic", Generation: 1, Timezone: "UTC", From: start.Format("2006-01-02"), Through: start.AddDate(0, 0, days-1).Format("2006-01-02"), HistoryFrom: start.Format("2006-01-02"), Loads: []LedgerLoad{}}
	for d := 0; d < days; d++ {
		v := 30.0
		known := true
		recorded := d%3 == 0
		switch kind {
		case "stable_intermediate":
			v = 60
			recorded = d%7 < 5
		case "high_volume":
			v = 110
			recorded = d%7 < 6
		case "interval_heavy":
			v = 90
			if d%2 == 0 {
				v = 25
			}
			recorded = d%7 < 5
		case "returning":
			recorded = d < 45 || d > 160
		case "unknown_load":
			recorded = true
			known = d%13 != 0
		}
		if recorded {
			var value *float64
			if known {
				value = ptr(v)
			}
			in.Loads = append(in.Loads, LedgerLoad{WorkoutID: fmt.Sprint(d), RunID: "synthetic", Day: start.AddDate(0, 0, d).Format("2006-01-02"), Load: value, Quality: map[bool]string{true: "complete", false: "insufficient"}[known]})
		}
	}
	return in
}
func TestGoldenAthleteHistories(t *testing.T) {
	for _, name := range []string{"beginner", "stable_intermediate", "high_volume", "interval_heavy", "returning", "unknown_load"} {
		t.Run(name, func(t *testing.T) {
			r, e := CalculateLoadState(athlete(name, 450))
			if e != nil {
				t.Fatal(e)
			}
			if len(r.Days) != 450 {
				t.Fatal("history truncated")
			}
			for i, d := range r.Days {
				if d.HistoryDaysObserved != i+1 || d.InitialState != "zero_before_recorded_history" {
					t.Fatal("cold start identity")
				}
				if i < 42 && d.Quality == "complete" {
					t.Fatal("cold start fully established")
				}
				if name == "unknown_load" && d.Acute.Value != nil {
					t.Fatal("unknown became zero")
				}
				if d.Acute.Value != nil && d.Chronic.Value != nil {
					close(t, *d.Balance, *d.Chronic.Value-*d.Acute.Value, 1e-12)
					if i >= 7 {
						close(t, *d.Trend7, (*d.Chronic.Value-*r.Days[i-7].Chronic.Value)/7, 1e-12)
					}
				}
			}
		})
	}
}
func TestIncrementalVersusBoundedRebuildEvents(t *testing.T) {
	rng := rand.New(rand.NewSource(57))
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	current := map[int]*float64{}
	for event := 0; event < 180; event++ {
		d := rng.Intn(450)
		switch rng.Intn(4) {
		case 0:
			delete(current, d)
		case 1:
			current[d] = nil
		case 2:
			current[d] = ptr(rng.Float64() * 120)
		case 3:
			for date, v := range current {
				if date >= d && date < d+7 && v != nil {
					current[date] = ptr(*v * 1.1)
				}
			}
		}
		xs := make([]DailyInput, 450)
		for date, v := range current {
			if v == nil {
				xs[date].Unknown = true
			} else {
				xs[date].Known = *v
			}
		}
		k7, k42 := Kernel{Tau: 7}, Kernel{Tau: 42}
		for i, x := range xs {
			a, c := k7.Advance(x), k42.Advance(x)
			ka, ua := direct(xs, i, 7)
			kc, uc := direct(xs, i, 42)
			close(t, a.KnownContribution, ka, 1e-11)
			close(t, c.KnownContribution, kc, 1e-11)
			close(t, a.UnknownWeightMass, ua, 1e-14)
			close(t, c.UnknownWeightMass, uc, 1e-14)
		}
		// Rebuilding only d..d+179 from 186 seed days matches direct history.
		from := max(0, d)
		through := min(449, d+179)
		in := StateInput{DatasetID: "synthetic", Generation: 1, Timezone: "UTC", From: start.AddDate(0, 0, from).Format("2006-01-02"), Through: start.AddDate(0, 0, through).Format("2006-01-02"), HistoryFrom: "2025-01-01"}
		for date, v := range current {
			if date >= from-186 && date <= through {
				in.Loads = append(in.Loads, LedgerLoad{WorkoutID: fmt.Sprint(date), Day: start.AddDate(0, 0, date).Format("2006-01-02"), Load: v, Quality: "complete"})
			}
		}
		r, e := CalculateLoadState(in)
		if e != nil {
			t.Fatal(e)
		}
		for i, v := range r.Days {
			a, _ := direct(xs, from+i, 7)
			c, _ := direct(xs, from+i, 42)
			close(t, v.Acute.KnownContribution, a, 1e-11)
			close(t, v.Chronic.KnownContribution, c, 1e-11)
		}
	}
}
func BenchmarkAthleteState(b *testing.B) {
	for _, days := range []int{30, 90, 180, 365} {
		b.Run(fmt.Sprint(days, "d"), func(b *testing.B) {
			in := athlete("stable_intermediate", days)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, e := CalculateLoadState(in); e != nil {
					b.Fatal(e)
				}
			}
		})
	}
}

// A mutable cached state receives only each event's bounded replacement range.
// This is distinct from replaying the entire sequence after every edit.
func TestCachedIncrementalStateEqualsFreshBoundedRebuild(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	loads := map[int]*float64{}
	var cache []DailyState
	build := func(from, through int) []DailyState {
		in := StateInput{DatasetID: "synthetic", Generation: 1, Timezone: "UTC", From: start.AddDate(0, 0, from).Format("2006-01-02"), Through: start.AddDate(0, 0, through).Format("2006-01-02"), HistoryFrom: "2025-01-01"}
		for date, v := range loads {
			if date >= from-186 && date <= through {
				in.Loads = append(in.Loads, LedgerLoad{WorkoutID: fmt.Sprint(date), Day: start.AddDate(0, 0, date).Format("2006-01-02"), Load: v, Quality: "complete"})
			}
		}
		r, e := CalculateLoadState(in)
		if e != nil {
			t.Fatal(e)
		}
		return r.Days
	}
	cache = build(0, 449)
	for event := 0; event < 120; event++ {
		d := rng.Intn(450)
		switch event % 4 {
		case 0:
			loads[d] = ptr(rng.Float64() * 90)
		case 1:
			delete(loads, d)
		case 2:
			loads[d] = nil
		case 3:
			loads[d] = ptr(rng.Float64() * 120)
		}
		tail := min(449, d+186)
		replacement := build(d, tail)
		copy(cache[d:tail+1], replacement)
		fresh := build(0, 449)
		for i := range fresh {
			a, b := cache[i], fresh[i]
			close(t, a.Acute.KnownContribution, b.Acute.KnownContribution, 1e-10)
			close(t, a.Chronic.KnownContribution, b.Chronic.KnownContribution, 1e-10)
			close(t, a.Acute.UnknownWeightMass, b.Acute.UnknownWeightMass, 1e-13)
			if (a.Acute.Value == nil) != (b.Acute.Value == nil) || (a.Trend7 == nil) != (b.Trend7 == nil) {
				t.Fatal("availability differs after bounded edit")
			}
			if a.Trend7 != nil {
				close(t, *a.Trend7, *b.Trend7, 1e-11)
			}
		}
	}
}
