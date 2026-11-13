package postgres

import (
	"testing"

	rf "rai/server/internal/runfitness"
)

func TestLatestPaceZoneProfileUsesNewestPreRunDayWithinWindow(t *testing.T) {
	state := func(validUntil string) rf.State {
		return rf.State{Zones: rf.Zones{ValidUntil: validUntil, Zones: []rf.Zone{{Name: "Z1"}}}}
	}
	// The run day's own state is never used, even though it is the newest.
	if p := latestPaceZoneProfile(map[string]rf.State{"2026-11-12": state("")}, "2026-11-12"); p != nil {
		t.Fatalf("run-day state used: %+v", p)
	}
	// Yesterday missing: fall back to the newest earlier day.
	p := latestPaceZoneProfile(map[string]rf.State{"2026-11-07": state(""), "2026-11-09": state(""), "2026-11-12": state("")}, "2026-11-12")
	if p == nil || p.Date != "2026-11-09" {
		t.Fatalf("expected 2026-11-09 profile: %+v", p)
	}
	// Zones whose evidence window ended before the run day are stale.
	if p := latestPaceZoneProfile(map[string]rf.State{"2026-11-10": state("2026-11-11")}, "2026-11-12"); p != nil {
		t.Fatalf("stale zones used: %+v", p)
	}
	if p := latestPaceZoneProfile(map[string]rf.State{"2026-11-10": state("2026-11-12")}, "2026-11-12"); p == nil {
		t.Fatal("zones valid through the run day rejected")
	}
	if p := latestPaceZoneProfile(nil, "2026-11-12"); p != nil {
		t.Fatalf("profile from nothing: %+v", p)
	}
}
