package training

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"
)

func syntheticContext() Context {
	return Context{PlanID: "00000000-0000-4000-8000-000000000001", Timezone: "Asia/Shanghai", AsOf: "2026-10-26", ConfigRevision: 1, Config: Config{StartDate: "2026-10-26", Weeks: 8, Goal: Goal{Type: "running", Date: "2026-12-20", DistanceM: 21097.5}, AvailableDays: []int{1, 3, 5, 7}, LongRunDay: 7, BaselineWeeklyM: Ptr(30000), PreviousLongM: Ptr(12000)}, Parameters: V1Parameters(), Workouts: []Workout{}, Signals: []Signal{}, Feedback: []Feedback{}}
}
func TestGeneratorGoldenDeterminismAndConstraints(t *testing.T) {
	in := syntheticContext()
	base, e := Generate(in)
	if e != nil {
		t.Fatal(e)
	}
	again, e := Generate(in)
	if e != nil || !reflect.DeepEqual(base, again) {
		t.Fatal("not deterministic")
	}
	b, e := json.MarshalIndent(base, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	b = append(b, '\n')
	if os.Getenv("RAI_UPDATE_GOLDEN") == "1" {
		if e = os.WriteFile("testdata/plan.v1.json", b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	want, e := os.ReadFile("testdata/plan.v1.json")
	if e != nil || string(want) != string(b) {
		t.Fatal("golden changed", e)
	}
	checkConstraints(t, base, in)
	in.Config.Weeks = 16
	in.Config.Goal.Date = "2027-02-14"
	base, e = Generate(in)
	if e != nil {
		t.Fatal(e)
	}
	checkConstraints(t, base, in)
}
func checkConstraints(t *testing.T, b Base, in Context) {
	t.Helper()
	if len(b.Sessions) != in.Config.Weeks*7 {
		t.Fatal("horizon")
	}
	prev := *in.Config.BaselineWeeklyM
	lastLong := *in.Config.PreviousLongM
	for w := 0; w < in.Config.Weeks; w++ {
		sum, long, hard, rest := 0.0, 0.0, 0, 0
		event := false
		for d := 0; d < 7; d++ {
			s := b.Sessions[w*7+d]
			for _, code := range s.Reasons {
				event = event || code == "taper" || code == "post_event_recovery" || code == "goal_event"
			}
			if s.Type == "RACE" {
				// The event is not training volume and follows none of the
				// weekly limits.
				continue
			}
			sum += s.DistanceM
			if s.Type == "LONG" {
				long = s.DistanceM
			}
			if s.Type == "REST" {
				rest++
			}
			if s.Type == "TEMPO" || s.Type == "INTERVAL" {
				hard++
				for k := 1; k <= 1; k++ {
					for _, j := range []int{w*7 + d - k, w*7 + d + k} {
						if j >= 0 && j < len(b.Sessions) {
							other := b.Sessions[j].Type
							if other == "TEMPO" || other == "INTERVAL" || other == "LONG" {
								t.Fatal("hard spacing")
							}
						}
					}
				}
			}
			if len(s.Reasons) == 0 {
				t.Fatal("missing explanation")
			}
		}
		if sum > prev*1.05+1 || hard > 1 || rest < 1 || long > sum*.35+2 || long > 30000 || long > lastLong*1.05+1 {
			t.Fatalf("constraint week=%d sum=%f prev=%f long=%f", w, sum, prev, long)
		}
		// A recovery week is a cut, not a new starting point: the next week
		// may return to the level reached before it, never above it. Taper,
		// event and post-event weeks stay below it and never move it either.
		if event {
			if sum > prev+1 || long > lastLong+1 {
				t.Fatalf("event-phase week=%d sum=%f long=%f above %f/%f", w, sum, long, prev, lastLong)
			}
		} else if w%4 != 3 {
			prev = sum
			lastLong = long
		} else if sum > prev*.8+1 || long > lastLong*.8+1 {
			t.Fatalf("recovery week=%d sum=%f long=%f not reduced from %f/%f", w, sum, long, prev, lastLong)
		}
	}
}
func TestSyntheticHistoriesAndMissingInputs(t *testing.T) {
	for _, tc := range []struct {
		name     string
		weekly   float64
		low      bool
		feedback bool
	}{{"Beginner", 6000, false, false}, {"Intermediate", 30000, false, false}, {"High volume", 100000, false, false}, {"Irregular runner", 12000, false, false}, {"Missed workouts", 20000, false, true}, {"Low readiness", 30000, true, false}, {"No HR", 30000, false, false}, {"No GPS", 30000, false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			in := syntheticContext()
			in.Config.BaselineWeeklyM = Ptr(tc.weekly)
			in.Config.PreviousLongM = Ptr(tc.weekly * .3)
			asOf, _ := time.Parse("2006-01-02", in.AsOf)
			pattern := []int{0, 2, 4, 6, 7, 9, 11, 13, 14, 16, 18, 20, 21, 23, 25, 27}
			if tc.name == "Beginner" {
				pattern = []int{0, 4, 7, 11, 14, 18, 21, 25}
			}
			if tc.name == "Irregular runner" {
				pattern = []int{0, 6, 18, 24}
			}
			for j, ago := range pattern {
				distance := tc.weekly / 4
				if tc.name == "Beginner" {
					distance = tc.weekly / 2
				}
				w := Workout{ID: StableID(in.PlanID, j), RunID: StableID(in.PlanID, j+1000), Generation: 1, Date: asOf.AddDate(0, 0, -ago).Format("2006-01-02"), DurationS: Ptr(1800), DistanceM: Ptr(distance), Load: nil}
				if tc.name == "No GPS" {
					w.DistanceM = nil
				}
				in.Workouts = append(in.Workouts, w)
			}
			if tc.low {
				in.ObservedModelBinding = &ModelBinding{Adapter: "schema2-v2"}
				in.Signals = []Signal{{Name: "readiness", Algorithm: "rai.readiness.training", Version: "v2", Quality: "complete", Value: Ptr(40)}}
			}
			if tc.feedback {
				in.Feedback = []Feedback{{Status: "skipped", Date: "2026-10-26"}}
			}
			b, e := Generate(in)
			if e != nil {
				t.Fatal(e)
			}
			checkConstraints(t, b, in)
			if tc.low {
				if hardSession(b.Sessions[0].Type) {
					t.Fatal("low readiness hard session today")
				}
			}
		})
	}
	in := syntheticContext()
	in.Config.BaselineWeeklyM = nil
	in.Config.PreviousLongM = nil
	if _, e := Generate(in); !errors.Is(e, ErrInsufficient) {
		t.Fatal("invented cold start")
	}
	in = syntheticContext()
	b, _ := Generate(in)
	in.Signals = []Signal{{Name: "readiness", Algorithm: "readiness", Version: "v1", Quality: "insufficient", Value: nil}}
	c, _ := Generate(in)
	if !reflect.DeepEqual(b, c) {
		t.Fatal("unavailable treated as zero")
	}
	in.Signals[0].Quality = "partial"
	in.Signals[0].Version = "v99"
	in.Signals[0].Value = Ptr(1)
	c, _ = Generate(in)
	if !reflect.DeepEqual(b, c) {
		t.Fatal("unknown algorithm used")
	}
	in = syntheticContext()
	in.Config.BaselineWeeklyM = nil
	in.Config.PreviousLongM = nil
	for k := 0; k < 4; k++ {
		in.Workouts = append(in.Workouts, Workout{Date: time.Date(2026, 10, 26-k*7, 0, 0, 0, 0, time.UTC).Format("2006-01-02"), DistanceM: Ptr(6000)})
	}
	if _, e := Generate(in); e != nil {
		t.Fatal("published history cold start", e)
	}
}
func simpleBase() Base {
	return Base{Sessions: []Session{{ID: "mon", Date: "2026-10-26", Type: "EASY", DistanceM: 5000, Origin: "generated", Status: "scheduled"}, {ID: "wed", Date: "2026-10-28", Type: "INTERVAL", Origin: "generated", Status: "scheduled"}, {ID: "sun", Date: "2026-11-01", Type: "LONG", DistanceM: 12000, Origin: "generated", Status: "scheduled"}}, Recommendations: []string{}}
}
func byID(b Base, id string) Session {
	for _, s := range b.Sessions {
		if s.ID == id {
			return s
		}
	}
	return Session{}
}
func TestCoreOverrideSurvivalE2ESemantics(t *testing.T) {
	b := simpleBase()
	a := byID(b, "mon")
	z := byID(b, "sun")
	os := []Override{{ID: "one", SessionID: "mon", Operation: "edit", Path: "distanceM", Value: json.RawMessage(`7000`), Revision: 1, Anchor: &a}, {ID: "two", SessionID: "sun", Operation: "move", Path: "date", Value: json.RawMessage(`"2026-10-31"`), Revision: 2, Anchor: &z}}
	first, e := ResolveEffective(b, os)
	if e != nil || byID(first, "mon").DistanceM != 7000 || byID(first, "sun").Date != "2026-10-31" {
		t.Fatal("effective v1", e)
	}
	b.Sessions[0].DistanceM = 4000
	b.Sessions[1].Type = "RECOVERY"
	b.Sessions[2].DistanceM = 10000
	second, e := ResolveEffective(b, os)
	if e != nil || byID(second, "mon").DistanceM != 7000 || byID(second, "wed").Type != "RECOVERY" || byID(second, "sun").Date != "2026-10-31" || byID(second, "sun").DistanceM != 10000 {
		t.Fatal("core survival failed", e)
	}
}
func TestMergeGoldenCases(t *testing.T) {
	base := simpleBase()
	anchor := base.Sessions[0]
	added := Session{ID: "user", Date: "2026-10-30", Type: "EASY", DistanceM: 3000, Origin: "user_added", Status: "scheduled"}
	edits := []Override{{ID: "edit", SessionID: "mon", Revision: 1, Operation: "edit", Path: "distanceM", Value: json.RawMessage(`7000`), Anchor: &anchor}}
	cases := []struct {
		name string
		b    Base
		os   []Override
	}{{"base", base, nil}, {"edit", base, edits}, {"move", base, []Override{{ID: "move", SessionID: "mon", Revision: 1, Operation: "move", Path: "date", Value: json.RawMessage(`"2026-10-27"`), Anchor: &anchor}}}, {"skip", base, []Override{{ID: "skip", SessionID: "mon", Revision: 1, Operation: "skip", Anchor: &anchor}}}, {"add", base, []Override{{ID: "add", SessionID: "user", Revision: 1, Operation: "add", Anchor: &added}}}, {"removed", Base{Sessions: base.Sessions[1:]}, edits}, {"multiple", base, append(append([]Override{}, edits...), Override{ID: "edit2", SessionID: "mon", Revision: 2, Operation: "edit", Path: "distanceM", Value: json.RawMessage(`8000`), Anchor: &anchor})}, {"revoke", base, append(append([]Override{}, edits...), Override{ID: "revoke", SessionID: "mon", Revision: 2, Operation: "revoke", TargetID: "edit"})}}
	outputs := map[string]Base{}
	for _, tc := range cases {
		out, e := ResolveEffective(tc.b, tc.os)
		if e != nil {
			t.Fatal(tc.name, e)
		}
		outputs[tc.name] = out
	}
	b, _ := json.MarshalIndent(outputs, "", "  ")
	b = append(b, '\n')
	if os.Getenv("RAI_UPDATE_GOLDEN") == "1" {
		if e := os.WriteFile("testdata/merge.v1.json", b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	want, e := os.ReadFile("testdata/merge.v1.json")
	if e != nil || string(want) != string(b) {
		t.Fatal("merge golden", e)
	}
	if byID(outputs["multiple"], "mon").DistanceM != 8000 || byID(outputs["revoke"], "mon").DistanceM != 5000 || len(byID(outputs["removed"], "mon").Conflicts) != 1 {
		t.Fatal("precedence/fallback/revoke")
	}
}
func TestFeedbackComparisons(t *testing.T) {
	ex := Execution{ID: "e", Revision: 1, Status: "completed", Snapshot: Session{Date: "2026-10-26", DurationS: 1800, DistanceM: 5000}}
	for _, tc := range []struct {
		distance float64
		want     string
	}{{4000, "under_target"}, {5000, "on_target"}, {6000, "over_target"}} {
		f := Compare(ex, &Workout{RunID: "r", DistanceM: Ptr(tc.distance)})
		if f.Status != tc.want || f.Distance.Ratio == nil {
			t.Fatal("feedback", f)
		}
	}
	if Compare(ex, nil).Status != "completed" {
		t.Fatal("missing inferred missed")
	}
	ex.Status = "skipped"
	if Compare(ex, nil).Status != "skipped" {
		t.Fatal("skip")
	}
}
func BenchmarkGenerate8Weeks(b *testing.B) {
	in := syntheticContext()
	for b.Loop() {
		_, _ = Generate(in)
	}
}
func BenchmarkGenerate16Weeks(b *testing.B) {
	in := syntheticContext()
	in.Config.Weeks = 16
	in.Config.Goal.Date = "2027-02-14"
	for b.Loop() {
		_, _ = Generate(in)
	}
}
func BenchmarkMerge100Sessions(b *testing.B) {
	in := syntheticContext()
	in.Config.Weeks = 16
	in.Config.Goal.Date = "2027-02-14"
	base, _ := Generate(in)
	base.Sessions = base.Sessions[:100]
	os := []Override{}
	for i := range 100 {
		s := base.Sessions[i]
		os = append(os, Override{ID: s.ID, SessionID: s.ID, Revision: int64(i + 1), Operation: "edit", Path: "notes", Value: json.RawMessage(`"synthetic"`), Anchor: &s})
	}
	for b.Loop() {
		_, _ = ResolveEffective(base, os)
	}
}

func TestGeneratorExtremeAvailabilityAndTimezone(t *testing.T) {
	for _, weekly := range []float64{3000, 30000, 160000} {
		for _, days := range [][]int{{1, 7}, {1, 2, 3, 4, 5, 7}, {2, 4, 6}, {1, 3, 5, 7}} {
			for _, long := range days {
				in := syntheticContext()
				in.Config.BaselineWeeklyM = Ptr(weekly)
				in.Config.PreviousLongM = Ptr(max(1000, min(weekly*.3, 50000)))
				in.Config.AvailableDays = days
				in.Config.LongRunDay = long
				base, e := Generate(in)
				if e != nil {
					t.Fatal(weekly, days, long, e)
				}
				checkConstraints(t, base, in)
				for _, s := range base.Sessions {
					if s.DistanceM > 30000 {
						t.Fatal("session ceiling")
					}
				}
			}
		}
	}
	in := syntheticContext()
	in.Timezone = "America/New_York"
	a, e := Generate(in)
	if e != nil {
		t.Fatal(e)
	}
	for i, s := range a.Sessions {
		date, _ := time.Parse("2006-01-02", in.Config.StartDate)
		if s.Date != date.AddDate(0, 0, i).Format("2006-01-02") {
			t.Fatal("DST changed local day")
		}
	}
}

func TestFieldEditsAndAnchorPurity(t *testing.T) {
	base := simpleBase()
	anchor := Session{ID: "user", Date: "2026-10-26", Type: "EASY", DistanceM: 3000, TargetHR: Ptr(130), PaceSPerKM: Ptr(400)}
	events := []Override{{ID: "add", SessionID: "user", Revision: 1, Operation: "add", Anchor: &anchor}, {ID: "hr", SessionID: "user", Revision: 2, Operation: "edit", Path: "targetHR", Value: json.RawMessage(`150`), Anchor: &anchor}}
	out, e := ResolveEffective(base, events)
	if e != nil || *byID(out, "user").TargetHR != 150 {
		t.Fatal(e)
	}
	if *anchor.TargetHR != 130 {
		t.Fatal("merge mutated immutable override anchor")
	}
	for _, tc := range []struct {
		path  string
		value string
	}{{"type", `"TEMPO"`}, {"durationS", `1800`}, {"effort", `6`}, {"targetHR", `155`}, {"paceSPerKM", `350`}, {"notes", `"synthetic adjustment"`}} {
		s := base.Sessions[0]
		if e := ApplyField(&s, tc.path, json.RawMessage(tc.value)); e != nil {
			t.Fatal(tc.path, e)
		}
	}
	s := anchor
	if ApplyField(&s, "notes", json.RawMessage(` null `)) == nil {
		t.Fatal("nonnullable field silently ignored")
	}
	if ApplyField(&s, "targetHR", json.RawMessage(`null`)) != nil || s.TargetHR != nil {
		t.Fatal("nullable target cannot clear")
	}
	in := syntheticContext()
	in.Config.Weeks = 4
	in.Config.Goal.Date = "2026-11-22"
	b, e := Generate(in)
	if e != nil {
		t.Fatal(e)
	}
	for _, s := range b.Sessions {
		if s.Type == "INTERVAL" {
			if ApplyField(&s, "durationS", json.RawMessage(`2400`)) != nil || len(s.Steps) != 0 || s.DurationS != 2400 {
				t.Fatal("duration override structure conflict")
			}
			return
		}
	}
	t.Fatal("fixture has no interval")
}

func TestGeneratorConsumesV2ReadinessSignal(t *testing.T) {
	in := syntheticContext()
	in.Config.BaselineWeeklyM = Ptr(30000)
	in.Config.PreviousLongM = Ptr(12000)
	asOf, _ := time.Parse("2006-01-02", in.AsOf)
	for j := 0; j < 8; j++ {
		in.Workouts = append(in.Workouts, Workout{ID: StableID(in.PlanID, j), RunID: StableID(in.PlanID, j+1000), Generation: 1, Date: asOf.AddDate(0, 0, -j).Format("2006-01-02"), DurationS: Ptr(1800), DistanceM: Ptr(6000), Load: Ptr(45.0)})
	}
	base, e := Generate(in)
	if e != nil {
		t.Fatal(e)
	}
	in.ObservedModelBinding = &ModelBinding{Adapter: "schema2-v2"}
	in.Signals = []Signal{{Name: "readiness", Algorithm: "rai.readiness.training", Version: "v2", Quality: "complete", Value: Ptr(40)}}
	low, e := Generate(in)
	if e != nil {
		t.Fatal(e)
	}
	if containsString(base.Recommendations, "readiness_unavailable") == false {
		t.Fatal("base fixture did not exercise missing readiness")
	}
	if containsString(low.Recommendations, "readiness_unavailable") {
		t.Fatal("v2 readiness signal was ignored")
	}
	if !containsString(low.Recommendations, "load_only_advisory") {
		t.Fatal("v2 advice missing")
	}
	// Low readiness reschedules only today's slot (and the day it moves to).
	if hardSession(low.Sessions[0].Type) {
		t.Fatal("low v2 readiness kept a hard session today")
	}
	if hardSession(base.Sessions[0].Type) && !containsString(low.Sessions[0].Reasons, "daily_readiness_reschedule") {
		t.Fatal("reschedule reason missing")
	}
	for i := 7; i < len(low.Sessions); i++ {
		if !reflect.DeepEqual(low.Sessions[i], base.Sessions[i]) {
			t.Fatal("v2 readiness changed weeks after the first")
		}
	}
	in.Signals[0].Quality = "partial"
	missing, e := Generate(in)
	if e != nil || !containsString(missing.Recommendations, "readiness_unavailable") {
		t.Fatal("partial v2 readiness was consumed", e)
	}

}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func TestGeneratorTargetsAndRampGuard(t *testing.T) {
	in := syntheticContext()
	in.Config.BaselineWeeklyM = Ptr(30000)
	in.Config.PreviousLongM = Ptr(12000)
	in.ObservedModelBinding = &ModelBinding{Adapter: "schema4-v1"}
	in.Signals = []Signal{
		{Name: "pace_z1", Algorithm: PaceAlgorithm, Version: "v1", Quality: "complete", Value: Ptr(372)},
		{Name: "pace_z2", Algorithm: PaceAlgorithm, Version: "v1", Quality: "complete", Value: Ptr(338)},
		{Name: "pace_z4", Algorithm: PaceAlgorithm, Version: "v1", Quality: "complete", Value: Ptr(258)},
		{Name: "pace_z5", Algorithm: PaceAlgorithm, Version: "v1", Quality: "complete", Value: Ptr(232)},
		{Name: "hr_max", Algorithm: "hr_reference", Version: "v2", Quality: "complete", Value: Ptr(190)},
		{Name: "hr_rest", Algorithm: "hr_reference", Version: "v2", Quality: "complete", Value: Ptr(50)},
		{Name: "acute", Algorithm: "rai.load_state.exp_finite", Version: "v2", Quality: "partial", Value: Ptr(70)},
		{Name: "chronic", Algorithm: "rai.load_state.exp_finite", Version: "v2", Quality: "partial", Value: Ptr(50)},
	}
	b, e := Generate(in)
	if e != nil {
		t.Fatal(e)
	}
	seen := map[string]bool{}
	for _, s := range b.Sessions {
		if s.Type == "REST" {
			if s.PaceSPerKM != nil || s.TargetHR != nil {
				t.Fatal("rest day got a target")
			}
			continue
		}
		if s.Type == "RACE" {
			// The event takes its pace only from a stated target time.
			if s.PaceSPerKM != nil || s.TargetHR != nil {
				t.Fatal("event got an invented target")
			}
			continue
		}
		if s.PaceSPerKM == nil || s.TargetHR == nil {
			t.Fatalf("%s without targets", s.Type)
		}
		seen[s.Type] = true
		if s.Type == "EASY" && (*s.PaceSPerKM != 338 || *s.TargetHR != 148) {
			t.Fatalf("easy targets %v %v", *s.PaceSPerKM, *s.TargetHR)
		}
		if s.Type == "TEMPO" && *s.PaceSPerKM >= 260 {
			t.Fatalf("tempo pace %v not near threshold", *s.PaceSPerKM)
		}
	}
	if !seen["EASY"] || !containsString(b.Recommendations, "load_ramp_guard") {
		t.Fatalf("targets/ramp: %v %v", seen, b.Recommendations)
	}
	for _, s := range b.Sessions {
		if s.Slot >= 7 && s.Slot < 14 && s.Type != "REST" && (containsString(s.Reasons, "weekly_progression") || !containsString(s.Reasons, "load_ramp_guard")) {
			t.Fatalf("week 2 progressed under ramp guard: %v", s.Reasons)
		}
	}
}

func TestRescheduleTodayMovesHardSession(t *testing.T) {
	week := []Session{
		{ID: "a", Slot: 0, Date: "2026-10-26", Type: "TEMPO", Effort: 6, DistanceM: 8000, Origin: "generated", Status: "scheduled", Reasons: []string{"goal_specific_tempo"}},
		{ID: "b", Slot: 1, Date: "2026-10-27", Type: "REST", Origin: "generated", Status: "scheduled", Reasons: []string{"minimum_recovery"}},
		{ID: "c", Slot: 2, Date: "2026-10-28", Type: "EASY", Effort: 3, DistanceM: 6000, Origin: "generated", Status: "scheduled", Reasons: []string{"aerobic_base"}},
		{ID: "d", Slot: 3, Date: "2026-10-29", Type: "REST", Origin: "generated", Status: "scheduled", Reasons: []string{"minimum_recovery"}},
	}
	if e := rescheduleToday(week, 0, PaceTargets{"z1": 400, "z2": 360, "z4": 270}, Ptr(190.0), Ptr(50.0)); e != nil {
		t.Fatal(e)
	}
	if week[0].Type != "RECOVERY" || week[0].DistanceM != 6000 || week[0].ID != "a" || week[0].PaceSPerKM == nil {
		t.Fatalf("today not recovery: %+v", week[0])
	}
	if week[2].Type != "TEMPO" || week[2].DistanceM != 8000 || week[2].ID != "c" || week[2].Date != "2026-10-28" || !containsString(week[2].Reasons, "daily_readiness_reschedule") {
		t.Fatalf("tempo not moved: %+v", week[2])
	}
	easyToday := []Session{{ID: "x", Slot: 0, Date: "2026-10-26", Type: "EASY", Effort: 3, DistanceM: 5000, Origin: "generated", Status: "scheduled", Reasons: []string{"aerobic_base"}}}
	if e := rescheduleToday(easyToday, 0, nil, nil, nil); e != nil || easyToday[0].Type != "EASY" {
		t.Fatal("easy day changed")
	}
}

func TestGeneratorV4EstimatesOnlyDownshiftAndRemainVersioned(t *testing.T) {
	in := syntheticContext()
	in.Config.BaselineWeeklyM = Ptr(30000)
	in.Config.PreviousLongM = Ptr(12000)
	in.ObservedModelBinding = &ModelBinding{Adapter: "schema3-v1"}
	base, e := Generate(in)
	if e != nil {
		t.Fatal(e)
	}
	in.Signals = []Signal{{Name: "readiness", Algorithm: "rai.readiness.training", Version: "v4", Mode: "estimated", Quality: "estimated", Value: Ptr(75)}}
	high, e := Generate(in)
	if e != nil || !reflect.DeepEqual(base.Sessions, high.Sessions) {
		t.Fatal("estimated status upgraded planned sessions", e)
	}
	in.Signals[0].Value = Ptr(55)
	low, e := Generate(in)
	if e != nil {
		t.Fatal(e)
	}
	if hardSession(base.Sessions[0].Type) && hardSession(low.Sessions[0].Type) {
		t.Fatal("explicit low estimate failed to downshift")
	}
	in.ObservedModelBinding.Adapter = "schema2-v2"
	old, e := Generate(in)
	if e != nil || !containsString(old.Recommendations, "readiness_unavailable") {
		t.Fatal("old adapter consumed new scale", e)
	}
}

// The plan builds across recovery weeks: each block peaks above the previous
// one and the long run keeps growing, instead of every deload becoming the new
// baseline. Tempo sessions are a warm-up, a block at tempo effort and a
// cool-down, and the block grows with each tempo session.
func TestPlanBuildsAcrossRecoveryWeeksAndStructuresTempo(t *testing.T) {
	in := syntheticContext()
	in.Config.Weeks = 12
	in.Config.Goal.Date = "2027-01-17"
	in.Config.BaselineWeeklyM, in.Config.PreviousLongM = Ptr(40000), Ptr(14000)
	base, e := Generate(in)
	if e != nil {
		t.Fatal(e)
	}
	var sums, longs []float64
	var works []int
	for w := 0; w < 12; w++ {
		sum, long := 0., 0.
		for _, s := range base.Sessions[w*7 : w*7+7] {
			sum += s.DistanceM
			if s.Type == "LONG" {
				long = s.DistanceM
			}
			if s.Type == "TEMPO" {
				if len(s.Steps) != 3 || s.Steps[0].Kind != "warmup" || s.Steps[1].Kind != "work" || s.Steps[2].Kind != "cooldown" || s.DurationS != float64(s.Steps[0].DurationS+s.Steps[1].DurationS+s.Steps[2].DurationS) {
					t.Fatalf("week %d tempo is not structured: %+v", w, s)
				}
				works = append(works, s.Steps[1].DurationS)
			}
		}
		sums, longs = append(sums, sum), append(longs, long)
	}
	for _, w := range []int{3, 7} {
		if sums[w] >= sums[w-1]*.85 || sums[w] <= sums[w-1]*.75 {
			t.Fatalf("recovery week %d: %f after %f", w, sums[w], sums[w-1])
		}
	}
	for _, w := range []int{4, 8} {
		if sums[w] < sums[w-2]-200 || sums[w] > sums[w-2]+1 || longs[w] > longs[w-2]+1 {
			t.Fatalf("week %d should return to week %d: %f vs %f", w, w-2, sums[w], sums[w-2])
		}
	}
	if !(sums[2] < sums[6] && sums[6] < sums[9]) || !(longs[2] < longs[6] && longs[6] < longs[9]) || sums[9] < sums[0]*1.25 {
		t.Fatalf("plan does not build: weekly %v long %v", sums, longs)
	}
	if len(works) < 3 || works[0] != 1200 || works[1] != 1500 || works[len(works)-1] > 2400 {
		t.Fatalf("tempo blocks %v", works)
	}
}

// The weeks before the goal date taper, the event is scheduled on its date,
// and what follows is recovery. Volume comes down while the weekly quality
// session stays; the event takes a pace only from a stated target time.
func TestPlanTapersIntoTheEventAndRecoversAfter(t *testing.T) {
	in := syntheticContext()
	in.Config.Weeks = 12
	in.Config.Goal.Date = "2027-01-03" // Sunday of week 10; two weeks follow
	in.Config.Goal.DistanceM = 42195
	in.Config.Goal.TargetTimeS = Ptr(4 * 3600.)
	in.Config.BaselineWeeklyM, in.Config.PreviousLongM = Ptr(60000), Ptr(24000)
	base, e := Generate(in)
	if e != nil {
		t.Fatal(e)
	}
	week := func(w int) (sum, long float64, types map[string]int) {
		types = map[string]int{}
		for _, s := range base.Sessions[w*7 : w*7+7] {
			types[s.Type]++
			if s.Type == "RACE" {
				continue
			}
			sum += s.DistanceM
			if s.Type == "LONG" {
				long = s.DistanceM
			}
		}
		return
	}
	peak, peakLong, _ := week(6)
	t2, l2, k2 := week(7)
	t1, l1, k1 := week(8)
	raceSum, _, raceTypes := week(9)
	post, _, postTypes := week(10)
	if !(t2 < peak*.85 && t2 > peak*.7 && t1 < t2 && t1 > peak*.55) || !(l2 < peakLong && l1 < l2) {
		t.Fatalf("taper volume %f %f after %f, long %f %f after %f", t2, t1, peak, l2, l1, peakLong)
	}
	if k2["TEMPO"]+k2["INTERVAL"] != 1 || k1["TEMPO"]+k1["INTERVAL"] != 1 {
		t.Fatal("taper dropped the quality session", k2, k1)
	}
	if raceSum > peak*.36 || raceTypes["RACE"] != 1 || raceTypes["LONG"]+raceTypes["TEMPO"]+raceTypes["INTERVAL"] != 0 {
		t.Fatalf("event week %f of %f: %v", raceSum, peak, raceTypes)
	}
	if post > peak*.51 || postTypes["LONG"]+postTypes["TEMPO"]+postTypes["INTERVAL"]+postTypes["EASY"] != 0 || postTypes["RECOVERY"] == 0 {
		t.Fatalf("post-event week %f: %v", post, postTypes)
	}
	race := base.Sessions[9*7+6]
	if race.Type != "RACE" || race.Date != "2027-01-03" || race.DistanceM != 42195 || race.PaceSPerKM == nil || math.Abs(*race.PaceSPerKM-341) > 1 || race.TargetHR != nil || !slices.Contains(race.Reasons, "goal_event") {
		t.Fatalf("event session %+v", race)
	}
	if before := base.Sessions[9*7+5]; before.Type != "REST" && before.Type != "RECOVERY" {
		t.Fatal("day before the event is not easy", before.Type)
	}
	if after := base.Sessions[10*7]; after.Type != "REST" {
		t.Fatal("day after the event is not rest", after.Type)
	}
	// A short event tapers only in its own week, and without a target time
	// the event carries no pace.
	in.Config.Goal.DistanceM, in.Config.Goal.TargetTimeS = 10000, nil
	base, e = Generate(in)
	if e != nil {
		t.Fatal(e)
	}
	for _, s := range base.Sessions[8*7 : 9*7] {
		if slices.Contains(s.Reasons, "taper") {
			t.Fatal("a 10 km event tapered a week early")
		}
	}
	if race := base.Sessions[9*7+6]; race.Type != "RACE" || race.PaceSPerKM != nil {
		t.Fatalf("10 km event %+v", race)
	}
	// Low readiness on the day never turns the event into a recovery run.
	in.AsOf = "2027-01-03"
	in.ObservedModelBinding = &ModelBinding{Adapter: "schema2-v2"}
	in.Signals = []Signal{{Name: "readiness", Algorithm: "rai.readiness.training", Version: "v2", Quality: "complete", Value: Ptr(20.)}}
	base, e = Generate(in)
	if e != nil || base.Sessions[9*7+6].Type != "RACE" {
		t.Fatal("low readiness rescheduled the event", e)
	}
}

func TestGeneratorWithoutSchedulableZoneIsHROnly(t *testing.T) {
	in := syntheticContext()
	in.Config.BaselineWeeklyM = Ptr(30000)
	in.Config.PreviousLongM = Ptr(12000)
	in.ObservedModelBinding = &ModelBinding{Adapter: "schema4-v1"}
	in.Signals = []Signal{
		{Name: "pace_z2", Algorithm: PaceAlgorithm, Version: "v1", Quality: "complete", Value: Ptr(338)},
		{Name: "hr_max", Algorithm: "hr_reference", Version: "v2", Quality: "complete", Value: Ptr(190)},
		{Name: "hr_rest", Algorithm: "hr_reference", Version: "v2", Quality: "complete", Value: Ptr(50)},
		// A retired v60 signal must not produce any pace.
		{Name: "v60", Algorithm: "rai.running_ability", Version: "v2", Quality: "complete", Value: Ptr(4)},
	}
	b, e := Generate(in)
	if e != nil {
		t.Fatal(e)
	}
	for _, s := range b.Sessions {
		switch s.Type {
		case "TEMPO", "INTERVAL", "RECOVERY":
			if s.PaceSPerKM != nil || s.TargetHR == nil || !containsString(s.Reasons, "pace_zone_unavailable_hr_only") {
				t.Fatalf("%s must fall back to HR only: %+v", s.Type, s)
			}
		case "EASY":
			if s.PaceSPerKM == nil || *s.PaceSPerKM != 338 {
				t.Fatalf("easy pace %+v", s.PaceSPerKM)
			}
		}
	}
}
