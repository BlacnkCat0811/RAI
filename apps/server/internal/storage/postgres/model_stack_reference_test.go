package postgres

import (
	"fmt"
	"testing"

	"rai/server/internal/hrref"
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
