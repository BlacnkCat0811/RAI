package analysis

import (
	"context"
	"testing"
)

func TestFullReviewDailyLoadIsolation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		loads []Load
		daily *float64
		state bool
	}{
		{"historical_unknown", []Load{{Day: "2026-10-28"}, {Day: "2026-10-29", Value: Ptr(100)}}, Ptr(100), false},
		{"today_unknown", []Load{{Day: "2026-10-29"}}, nil, false},
		{"no_workout_today", []Load{{Day: "2026-10-28"}}, Ptr(0), false},
		{"no_history", nil, Ptr(0), false},
		{"complete", []Load{{Day: "2026-10-28", Value: Ptr(50)}, {Day: "2026-10-29", Value: Ptr(100)}}, Ptr(100), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ms, e := TrainingState(context.Background(), StateInput{From: "2026-10-29", Through: "2026-10-29", Timezone: "Asia/Shanghai", Loads: tc.loads})
			if e != nil {
				t.Fatal(e)
			}
			d := find(ms, "daily_load")
			if (d.Value == nil) != (tc.daily == nil) || (d.Value != nil && *d.Value != *tc.daily) || d.Version != StateVersion {
				t.Fatal("daily availability polluted")
			}
			for _, name := range []string{"fitness", "fatigue", "balance", "recovery", "readiness"} {
				if (find(ms, name).Value != nil) != tc.state {
					t.Fatal("historical missing state lost", name)
				}
			}
		})
	}
}

func TestDailyLoadResolvedHistoryMatchesFullRecompute(t *testing.T) {
	in := StateInput{From: "2026-10-28", Through: "2026-10-29", Timezone: "Asia/Shanghai", Loads: []Load{{Day: "2026-10-28"}, {Day: "2026-10-29", Value: Ptr(100)}}}
	in.Loads[0].Value = Ptr(50)
	full, e := TrainingState(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	in.From = in.Through
	one, e := TrainingState(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	for _, m := range one {
		for _, f := range full {
			if m.Day == f.Day && m.Name == f.Name && (m.Value == nil || f.Value == nil || *m.Value != *f.Value) {
				t.Fatal("incremental mismatch")
			}
		}
	}
}
