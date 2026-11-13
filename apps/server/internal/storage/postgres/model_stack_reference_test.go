package postgres

import (
	"fmt"
	"testing"
	"time"

	"rai/server/internal/hrref"
	rc "rai/server/internal/recovery"
	ve "rai/server/internal/vo2maxexternal"
	wf "rai/server/internal/workoutfeatures"
)

func TestDoseReferenceIdentityIgnoresRollingRestingBaseline(t *testing.T) {
	const day = hrref.DayUs
	base := int64(1_790_000_000_000_000)
	var daily []hrref.DailyRHR
	for k := 0; k < 60; k++ {
		v := 50 + float64(k%5)
		daily = append(daily, hrref.DailyRHR{Date: fmt.Sprint("d", 100+k), Start: base + int64(k)*day, Value: &v, SourceEpoch: "e", RunID: fmt.Sprint("r", k), Hash: fmt.Sprint("h", k)})
	}
	maxOnly := hrref.Reference{Schema: hrref.Schema, ID: "max", DatasetID: "d", SubjectKey: "athlete", EffectiveFrom: base, KnownAt: base, SourceEpoch: "e", Provenance: "synthetic",
		HRMax: &hrref.Measurement{Value: 190, MeasuredAt: base, RecordedAt: base, Method: "user_entered", Unit: "bpm"}}
	resolve := func(dayIndex int, refs ...hrref.Reference) hrref.Snapshot {
		t.Helper()
		s, err := hrref.Resolve(hrref.Input{AsOf: base + 70*day, WorkoutStart: base + int64(dayIndex)*day + 12*hrref.HourUs, SourceEpoch: "e", References: refs, DailyRHR: daily})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	a, b := doseReference(resolve(30, maxOnly)), doseReference(resolve(45, maxOnly))
	if a == nil || b == nil || !a.Baseline {
		t.Fatalf("baseline dose reference missing: %+v %+v", a, b)
	}
	if a.Hash != b.Hash {
		t.Fatal("rolling resting baseline split one HRmax reference into separate VO2 groups")
	}
	if a.BaselineHash == b.BaselineHash {
		t.Fatal("per-Workout resting baseline must stay visible as evidence")
	}
	explicit := maxOnly
	explicit.ID = "explicit"
	explicit.RestingHR = &hrref.Measurement{Value: 48, MeasuredAt: base, RecordedAt: base, Method: "user_entered", Unit: "bpm"}
	c := doseReference(resolve(30, explicit))
	if c == nil || c.Baseline || c.Hash == a.Hash || c.RestHR != 48 {
		t.Fatalf("explicit resting HR is part of the reference identity: %+v", c)
	}
}

func TestStableExternalInputKeepsSelectionAcrossUpdates(t *testing.T) {
	in := &ve.ExternalVO2Snapshot{
		Facts:      []ve.Fact{{ID: "a", Epoch: "sha256:old", StableEpoch: "sha256:watch"}, {ID: "b", Epoch: "sha256:new", StableEpoch: "sha256:watch"}},
		Tombstones: []ve.Tombstone{{ID: "c", Epoch: "sha256:other", StableEpoch: "sha256:app"}},
		Selections: []ve.Selection{{ID: "s", Epoch: "sha256:old"}, {ID: "u", Epoch: "sha256:unknown"}},
	}
	stableExternalInput(in)
	if in.Facts[0].Epoch != "sha256:watch" || in.Facts[1].Epoch != "sha256:watch" || in.Tombstones[0].Epoch != "sha256:app" {
		t.Fatalf("facts not stable: %+v %+v", in.Facts, in.Tombstones)
	}
	if in.Selections[0].Epoch != "sha256:watch" || in.Selections[1].Epoch != "sha256:unknown" {
		t.Fatalf("selection: %+v", in.Selections)
	}
}

func TestVO2PrescreenRequiresNativeSpeedAndGradeSupport(t *testing.T) {
	f := wf.WorkoutFeatures{}
	active, distance := 1800., 5400.
	f.Timing.Active = &active
	f.Distance.SpeedIntegral = &distance
	f.Route.GradeCoverage = 1
	if !stackVO2RawNeeded(f) {
		t.Fatal("a potentially qualifying timeline was skipped")
	}
	f.Distance.SpeedIntegral = nil
	if stackVO2RawNeeded(f) {
		t.Fatal("missing native speed cannot form VO2 windows")
	}
	f.Distance.SpeedIntegral = &distance
	f.Route.GradeCoverage = 0
	if stackVO2RawNeeded(f) {
		t.Fatal("missing grade support cannot form VO2 windows")
	}
}

// A sparse week (resting HR on only two of the seven days before the run)
// still establishes the baseline when the run day's own morning reading,
// taken before the run, is the third recent observation. A reading at or
// after the start never counts.
func TestDailyRHRFactsIncludeRunDayReadingBeforeStart(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	run := time.Date(2026, 3, 9, 20, 7, 0, 0, loc)
	at := func(month time.Month, d, h int) int64 { return time.Date(2026, month, d, h, 0, 0, 0, loc).UnixMicro() }
	in := &rc.Input{}
	add := func(start int64) {
		v := 52.0
		in.Facts = append(in.Facts, rc.Fact{Type: rc.RHR, ID: fmt.Sprint("r", start), Hash: fmt.Sprint("h", start), Start: start, End: start, KnownAt: start, Value: &v})
	}
	for _, d := range []int{10, 11, 12, 13, 14, 16, 17, 18, 19, 20, 21, 24, 25} {
		add(at(time.February, d, 7))
	}
	add(at(time.March, 2, 7)) // outside the exact 7-day window (before 03-02 20:07)
	add(at(time.March, 5, 7))
	add(at(time.March, 6, 7))
	maxOnly := hrref.Reference{Schema: hrref.Schema, ID: "max", DatasetID: "d", SubjectKey: "athlete", EffectiveFrom: at(time.January, 1, 0), KnownAt: at(time.January, 1, 0), SourceEpoch: "e", Provenance: "synthetic",
		HRMax: &hrref.Measurement{Value: 190, MeasuredAt: at(time.January, 1, 0), RecordedAt: at(time.January, 1, 0), Method: "user_entered", Unit: "bpm"}}
	resolve := func() hrref.Snapshot {
		t.Helper()
		s, err := hrref.Resolve(hrref.Input{AsOf: at(time.March, 10, 12), WorkoutStart: run.UnixMicro(), SourceEpoch: "e", References: []hrref.Reference{maxOnly}, DailyRHR: dailyRHRFacts(in, run.UnixMicro(), "e", loc)})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	if s := resolve(); s.RestingHR != nil || doseReference(s) != nil {
		t.Fatalf("two recent days must not establish a baseline: %+v", s.RestingHR)
	}
	add(run.Add(time.Minute).UnixMicro()) // after the start: still excluded
	if s := resolve(); s.RestingHR != nil {
		t.Fatalf("a reading after the start counted: %+v", s.RestingHR)
	}
	// A run-day sample opened at midnight but computed after the start may
	// already include post-run heart rate: it never counts, even as the
	// latest-known version of the day.
	straddle := 49.0
	in.Facts = append(in.Facts, rc.Fact{Type: rc.RHR, ID: "straddle", Hash: "hs", Start: at(time.March, 9, 0), End: run.Add(time.Hour).UnixMicro(), KnownAt: run.Add(2 * time.Hour).UnixMicro(), Value: &straddle})
	if s := resolve(); s.RestingHR != nil {
		t.Fatalf("a run-day sample ending after the start counted: %+v", s.RestingHR)
	}
	add(at(time.March, 9, 7)) // run-day morning, before the start
	s := resolve()
	if s.RestingHR == nil || !s.RestingHR.Baseline || doseReference(s) == nil {
		t.Fatalf("run-day pre-start reading not counted: %+v %v", s.RestingHR, s.Reasons)
	}
	days := dailyRHRFacts(in, run.UnixMicro(), "e", loc)
	if last := days[len(days)-1]; last.Date != "2026-03-09" || last.Start >= run.UnixMicro() {
		t.Fatalf("unexpected last daily reading: %+v", last)
	}
}
