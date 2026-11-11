package postgres

import (
	"fmt"
	"testing"

	"rai/server/internal/hrref"
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
