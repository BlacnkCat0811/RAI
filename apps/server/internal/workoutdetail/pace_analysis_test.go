package workoutdetail

import (
	wf "rai/server/internal/workoutfeatures"
	"testing"
)

func TestAbsolutePaceNeedsNoPersonalProfile(t *testing.T) {
	raw := wf.RawWorkoutSnapshot{Start: 0, End: 60e6}
	z := BuildAbsolutePace(raw)
	if z.PaceZoneStatus == "available" || len(z.PaceExposure) != 0 {
		t.Fatal("missing speed fabricated distribution")
	}
	if z.PaceZonePolicyVersion != "absolute-pace-support/1" {
		t.Fatal("missing version")
	}
}

func TestAbsolutePaceIntegratesSupportedLinearSignal(t *testing.T) {
	z := BuildAbsolutePace(paceSnapshot())
	if z.PaceZoneStatus != "available" || z.PaceCoverage != .5 || z.PaceClassifiedSeconds != 10 {
		t.Fatalf("bad supported coverage: %+v", z)
	}
	var seconds, distance float64
	for _, e := range z.PaceExposure {
		seconds += e.DurationSeconds
		distance += e.DistanceMeters
	}
	if seconds < 9.8 || seconds > 10.2 || distance < 34.8 || distance > 35.2 {
		t.Fatalf("integral seconds=%v distance=%v", seconds, distance)
	}
}

func TestAbsolutePaceExactBoundaryAndLongGap(t *testing.T) {
	raw := paceSnapshot()
	for i := range raw.Quantities {
		raw.Quantities[i].Value = 1000.0 / 240
	}
	z := BuildAbsolutePace(raw)
	for _, e := range z.PaceExposure {
		if e.Zone == "4_to_5" && e.DurationSeconds != 10 {
			t.Fatal("4:00 boundary assigned incorrectly")
		}
		if e.Zone == "under_4" && e.DurationSeconds != 0 {
			t.Fatal("4:00 classified faster than 4:00")
		}
	}
	raw.Quantities[1].Start = 16e6
	raw.Quantities[1].End = 16e6
	z = BuildAbsolutePace(raw)
	if z.PaceZoneStatus == "available" {
		t.Fatal("long gap bridged")
	}
}
