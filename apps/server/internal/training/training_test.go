package training

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
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
		for d := 0; d < 7; d++ {
			s := b.Sessions[w*7+d]
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
		prev = sum
		lastLong = long
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
				in.Signals = []Signal{{Name: "readiness", Algorithm: "readiness", Version: "v1", Quality: "partial", Value: Ptr(40)}}
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
				for _, s := range b.Sessions[:7] {
					if s.Type == "TEMPO" || s.Type == "INTERVAL" {
						t.Fatal("low readiness hard session")
					}
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
