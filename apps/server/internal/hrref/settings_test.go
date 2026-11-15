package hrref

import (
	"testing"
	"time"
)

func TestSettingsSeparateDatesAndStableHistoricalIdentity(t *testing.T) {
	target := time.UnixMicro(base)
	measured := target.Add(24 * time.Hour)
	known := measured.Add(time.Hour)
	max := Setting{Value: 190, Basis: "user_setting", Method: "user_entered", MeasuredAt: &measured, Unit: "bpm"}
	rest := Setting{Value: 50, Basis: "user_setting", Method: "user_entered", Unit: "bpm"}
	e := Event{ID: "test", From: target.Add(-time.Hour), Known: known, Provenance: "synthetic", Order: 1, Patch: Patch{MaxHR: &max, RestingHR: &RestSetting{Mode: "manual", ManualValue: &rest}}}
	in := Input{AsOf: known.UnixMicro(), WorkoutStart: base, SourceEpoch: "e"}
	s, a, err := ResolveSettings([]Event{e}, in, "test-stack")
	if err != nil || s.HRMax == nil || s.RestingHR == nil || a.MaxHR.Status != "estimated" || a.RestingHR.MeasuredAt != nil {
		t.Fatalf("settings resolution: %v", err)
	}
	in.AsOf += 300 * day
	_, b, err := ResolveSettings([]Event{e}, in, "test-stack")
	if err != nil || a.InputHash != b.InputHash {
		t.Fatal("audit time changed reference identity")
	}
	max.Basis = "measured"
	e.Patch.MaxHR = &max
	s, _, err = ResolveSettings([]Event{e}, in, "test-stack")
	if err != nil || s.HRMax != nil {
		t.Fatal("future measurement used for historical workout")
	}
}
func TestSettingsClearingAndIndependentComponents(t *testing.T) {
	at := time.UnixMicro(base)
	max := &Setting{Value: 190, Basis: "user_setting", Method: "user_entered", Unit: "bpm"}
	rest := &Setting{Value: 50, Basis: "user_setting", Method: "user_entered", Unit: "bpm"}
	xs := []Event{{ID: "original", Order: 1, From: at.Add(-24 * time.Hour), Known: at, Patch: Patch{MaxHR: max, RestingHR: &RestSetting{Mode: "manual", ManualValue: rest}}}, {ID: "clear", Order: 2, From: at, Known: at, Patch: Patch{RestingHR: &RestSetting{Mode: "automatic"}}}}
	saved := Saved(xs, at, at)
	if saved.MaxHR == nil || saved.RestingHR == nil || saved.RestingHR.ManualValue != nil || saved.RestingHR.Mode != "automatic" {
		t.Fatal("clear revived old setting or erased max")
	}
}

func TestSettingsTiedLegacyConflictFailsClosed(t *testing.T) {
	at := time.UnixMicro(base)
	a := Setting{Value: 190, Basis: "user_setting", Method: "user_entered", Unit: "bpm"}
	b := a
	b.Value = 195
	events := []Event{{ID: "a", From: at, Known: at, Provenance: "synthetic", Patch: Patch{MaxHR: &a}}, {ID: "b", From: at, Known: at, Provenance: "synthetic", Patch: Patch{MaxHR: &b}}}
	_, e, err := ResolveSettings(events, Input{AsOf: base, WorkoutStart: base, SourceEpoch: "e"}, "test")
	if err != ErrConflict || e.MaxHR.Value != nil || e.MaxHR.Status != "unavailable" {
		t.Fatal("conflict silently selected a value")
	}
}
