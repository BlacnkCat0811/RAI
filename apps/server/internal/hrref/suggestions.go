package hrref

import (
	"math"
	"sort"
	"time"
)

const SuggestionPolicy = "rai.hr_suggestions/1"

type RestingSuggestion struct {
	Value          float64   `json:"value"`
	SampleDays     int       `json:"sampleDays"`
	LatestSampleAt time.Time `json:"latestSampleAt"`
	PolicyVersion  string    `json:"policyVersion"`
}

// SuggestResting is a confirmation candidate, never an automatic setting. The
// stability thresholds are product heuristics, independent of model baselines.
// Each caller supplies live, completed observations from one stable source.
func SuggestResting(days []DailyRHR, asOf int64) *RestingSuggestion {
	byDate := map[string]DailyRHR{}
	for _, d := range days {
		if !d.Live || d.Value == nil || !finite(*d.Value) || *d.Value < MinBPM || *d.Value >= MaxBPM || d.Start < asOf-BaselineDays*DayUs || d.Start >= asOf {
			continue
		}
		old, ok := byDate[d.Date]
		if !ok || d.Start > old.Start || d.Start == old.Start && d.Hash > old.Hash {
			byDate[d.Date] = d
		}
	}
	if len(byDate) < MinBaseline {
		return nil
	}
	values, recent := []float64{}, []float64{}
	latest := int64(0)
	for _, d := range byDate {
		values = append(values, *d.Value)
		latest = max(latest, d.Start)
		if d.Start >= asOf-RecentDays*DayUs {
			recent = append(recent, *d.Value)
		}
	}
	if len(recent) < RecentMin || latest < asOf-3*DayUs {
		return nil
	}
	median := func(v []float64) float64 {
		sort.Float64s(v)
		return (v[(len(v)-1)/2] + v[len(v)/2]) / 2
	}
	v := median(values)
	stable := 0
	for _, x := range values {
		if math.Abs(x-v) <= 5 {
			stable++
		}
	}
	if stable*5 < len(values)*4 || math.Abs(median(recent)-v) > 3 {
		return nil
	}
	v = math.Round(v)
	if v >= MaxBPM {
		return nil
	}
	return &RestingSuggestion{Value: v, SampleDays: len(values), LatestSampleAt: time.UnixMicro(latest).UTC(), PolicyVersion: SuggestionPolicy}
}
