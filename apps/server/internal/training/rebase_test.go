package training

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestOverrideRebaseStructureMatrix(t *testing.T) {
	for _, tc := range []struct {
		name, oldType, newType, path, value          string
		oldDuration, oldDistance, duration, distance float64
		conflict                                     bool
	}{
		{"distance_survives", "EASY", "RECOVERY", "distanceM", "7000", 0, 5000, 0, 4000, false},
		{"duration_survives", "INTERVAL", "RECOVERY", "durationS", "1800", 2880, 5000, 0, 4000, false},
		{"move_survives", "INTERVAL", "RECOVERY", "date", `"2026-10-27"`, 2880, 5000, 0, 4000, false},
		{"interval_to_recovery", "INTERVAL", "RECOVERY", "distanceM", "0", 2880, 5000, 0, 4000, true},
		{"recovery_to_interval", "RECOVERY", "INTERVAL", "durationS", "0", 0, 5000, 2880, 4000, false},
		{"rest_anchor", "EASY", "REST", "distanceM", "7000", 0, 5000, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			anchor := Session{ID: "s", Date: "2026-10-26", Type: tc.oldType, DurationS: tc.oldDuration, DistanceM: tc.oldDistance}
			next := cloneSession(anchor)
			next.Type = tc.newType
			next.DurationS = tc.duration
			next.DistanceM = tc.distance
			operation := "edit"
			if tc.path == "date" {
				operation = "move"
			}
			o := Override{ID: "o", SessionID: "s", Operation: operation, Path: tc.path, Value: json.RawMessage(tc.value), Revision: 1, Anchor: &anchor}
			base := Base{Sessions: []Session{next}}
			before, _ := json.Marshal(base)
			fact, _ := json.Marshal(o)
			out, e := ResolveEffective(base, []Override{o})
			if e != nil || len(out.Sessions) != 1 || ValidateSession(out.Sessions[0]) != nil {
				t.Fatal("invalid projection", e)
			}
			s := out.Sessions[0]
			if tc.conflict {
				if !reflect.DeepEqual(s.Conflicts, []string{tc.path + "_override_incompatible_with_session_structure/v1"}) || s.DistanceM != next.DistanceM {
					t.Fatal("conflict not explained")
				}
			} else {
				x := cloneSession(s)
				if ApplyField(&x, tc.path, o.Value) != nil || !reflect.DeepEqual(x, s) {
					t.Fatal("applicable value lost")
				}
			}
			after, _ := json.Marshal(base)
			afterFact, _ := json.Marshal(o)
			if string(before) != string(after) || string(fact) != string(afterFact) {
				t.Fatal("input mutated")
			}
			cleared, e := ResolveEffective(base, []Override{o, {ID: "r", SessionID: "s", Operation: "revoke", TargetID: "o", Revision: 2}})
			if e != nil || !reflect.DeepEqual(cleared.Sessions, base.Sessions) {
				t.Fatal("revoke failed", e)
			}
		})
	}
}

func TestRebaseConflictLatestFieldAndLegacyReplay(t *testing.T) {
	anchor := Session{ID: "s", Date: "2026-10-26", Type: "INTERVAL", DurationS: 2880, DistanceM: 5000}
	next := cloneSession(anchor)
	next.Type = "RECOVERY"
	next.DurationS = 0
	base := Base{Sessions: []Session{next}}
	old := Override{ID: "a", SessionID: "s", Operation: "edit", Path: "distanceM", Value: json.RawMessage(`0`), Revision: 1, Anchor: &anchor}
	if _, e := ResolveEffectiveVersion(base, []Override{old}, ""); e == nil {
		t.Fatal("legacy merge changed")
	}
	newer := old
	newer.ID = "b"
	newer.Revision = 2
	newer.Value = json.RawMessage(`6000`)
	out, e := ResolveEffective(base, []Override{newer, old})
	if e != nil || out.Sessions[0].DistanceM != 6000 || len(out.Sessions[0].Conflicts) != 0 {
		t.Fatal("superseded field conflict leaked", e)
	}
	old.Value = json.RawMessage(`-1`)
	if _, e := ResolveEffective(base, []Override{old}); e == nil {
		t.Fatal("invalid fact disguised as rebase conflict")
	}
}

func TestRevokedRebaseConflictDoesNotLeakFromLaterAnchor(t *testing.T) {
	anchor := Session{ID: "s", Date: "2026-10-26", Type: "INTERVAL", DurationS: 2880, DistanceM: 5000}
	next := cloneSession(anchor)
	next.Type = "RECOVERY"
	next.DurationS = 0
	distance := Override{ID: "distance", SessionID: "s", Operation: "edit", Path: "distanceM", Value: json.RawMessage(`0`), Revision: 1, Anchor: &anchor}
	first, e := ResolveEffective(Base{Sessions: []Session{next}}, []Override{distance})
	if e != nil {
		t.Fatal(e)
	}
	later := first.Sessions[0]
	note := Override{ID: "note", SessionID: "s", Operation: "edit", Path: "notes", Value: json.RawMessage(`"retained"`), Revision: 2, Anchor: &later}
	before, _ := json.Marshal(later)
	out, e := ResolveEffective(Base{}, []Override{distance, note, {ID: "revoke", SessionID: "s", Operation: "revoke", TargetID: "distance", Revision: 3}})
	if e != nil || len(out.Sessions) != 1 || !reflect.DeepEqual(out.Sessions[0].Conflicts, []string{"base_session_removed_kept"}) || out.Sessions[0].Notes != "retained" {
		t.Fatal("revoked conflict leaked from anchor", e)
	}
	for _, reason := range out.Sessions[0].Reasons {
		if reason == "override_rebase_conflict/v1" {
			t.Fatal("stale rebase reason")
		}
	}
	after, _ := json.Marshal(later)
	if string(before) != string(after) {
		t.Fatal("anchor fact changed")
	}
}
