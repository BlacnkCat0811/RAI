package hrref

import (
	"math"
	"slices"
	"testing"
	"time"
)

func TestRestingSuggestionRequiresStableRecentIndependentDays(t *testing.T) {
	const now = int64(1800000000000000)
	days := []DailyRHR{}
	for i := 1; i <= 20; i++ {
		v := 50 + float64(i%3)
		days = append(days, DailyRHR{Date: LocalDate(time.UnixMicro(now - int64(i)*DayUs)), Start: now - int64(i)*DayUs, Value: &v, Live: true})
	}
	got := SuggestResting(days, now)
	if got == nil || got.Value != 51 || got.SampleDays != 20 || got.PolicyVersion != SuggestionPolicy {
		t.Fatal("stable observations did not produce a candidate")
	}
	slices.Reverse(days)
	reordered := SuggestResting(append(slices.Clone(days), days...), now)
	if reordered == nil || *reordered != *got {
		t.Fatal("order or duplicate samples changed recommendation")
	}
	for _, tc := range []struct {
		name string
		edit func([]DailyRHR) []DailyRHR
	}{
		{"sparse", func(d []DailyRHR) []DailyRHR { return d[:13] }},
		{"deleted", func(d []DailyRHR) []DailyRHR {
			for i := 0; i < 8; i++ {
				d[i].Live = false
			}
			return d
		}},
		{"stale", func(d []DailyRHR) []DailyRHR {
			for i := range d {
				d[i].Start -= 7 * DayUs
			}
			return d
		}},
		{"variable", func(d []DailyRHR) []DailyRHR {
			for i := range d {
				v := float64(35 + i*2)
				d[i].Value = &v
			}
			return d
		}},
		{"recent_shift", func(d []DailyRHR) []DailyRHR {
			for i := range d {
				if d[i].Start >= now-7*DayUs {
					v := 70.0
					d[i].Value = &v
				}
			}
			return d
		}},
		{"invalid", func(d []DailyRHR) []DailyRHR {
			for i := range d {
				v := math.NaN()
				d[i].Value = &v
			}
			return d
		}},
		{"rounded_out_of_range", func(d []DailyRHR) []DailyRHR {
			for i := range d {
				v := 219.8
				d[i].Value = &v
			}
			return d
		}},
		{"future", func(d []DailyRHR) []DailyRHR {
			for i := range d {
				d[i].Start = now + DayUs
			}
			return d
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if SuggestResting(tc.edit(slices.Clone(days)), now) != nil {
				t.Fatal("unsupported observations produced a recommendation")
			}
		})
	}
}
