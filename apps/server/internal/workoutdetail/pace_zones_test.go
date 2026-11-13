package workoutdetail

import (
	"slices"
	"testing"

	rf "rai/server/internal/runfitness"
)

// Synthetic zones: boundaries at 2.5, 3.0, 3.5, 4.0, 4.5 m/s.
func syntheticPaceProfile() *PaceZoneProfile {
	labels := []string{"recovery", "aerobic_endurance", "aerobic_power", "threshold", "anaerobic_endurance", "anaerobic_power"}
	speeds := []float64{2.0, 2.5, 3.0, 3.5, 4.0, 4.5, 5.0}
	z := rf.Zones{Reasons: []string{}}
	for i, l := range labels {
		zz := rf.Zone{Name: "Z" + string(rune('1'+i)), Label: l, Support: "supported", Reasons: []string{}}
		slow, fast := speeds[i], speeds[i+1]
		zz.SlowSpeed, zz.FastSpeed = &slow, &fast
		z.Zones = append(z.Zones, zz)
	}
	return &PaceZoneProfile{Date: "2026-11-11", Zones: z}
}

func speedObs(startS, endS int64, v float64) Observation {
	return Observation{Type: RunningSpeed, StartUs: startS * 1e6, EndUs: endS * 1e6, Value: v, Unit: "m/s"}
}

func TestBuildPaceZonesClassifiesDurationAndDistance(t *testing.T) {
	obs := []Observation{
		speedObs(0, 100, 2.2),   // Z1 (slower than Z1 slow bound stays Z1)
		speedObs(100, 300, 2.8), // Z2
		speedObs(300, 400, 4.0), // Z5: exactly on a boundary belongs to the faster zone
		speedObs(400, 500, 6.0), // Z6: open-ended fast side
	}
	r := BuildPaceZones(0, 600e6, 600, obs, nil, syntheticPaceProfile())
	if r.Status != PaceZonesAvailable || r.ProfileDate != "2026-11-11" {
		t.Fatalf("status %+v", r)
	}
	if r.Coverage != 0.8333 {
		t.Fatalf("coverage %v", r.Coverage)
	}
	want := map[string][2]float64{"Z1": {100, 220}, "Z2": {200, 560}, "Z3": {0, 0}, "Z4": {0, 0}, "Z5": {100, 400}, "Z6": {100, 600}}
	if len(r.Exposure) != 6 {
		t.Fatalf("exposure %+v", r.Exposure)
	}
	for _, e := range r.Exposure {
		if w := want[e.Zone]; e.DurationSeconds != w[0] || e.DistanceMeters != w[1] {
			t.Fatalf("%s got %v s %v m want %v", e.Zone, e.DurationSeconds, e.DistanceMeters, w)
		}
	}
	if r.Bands[0].SlowPaceSecondsPerKm != nil || r.Bands[5].FastPaceSecondsPerKm != nil {
		t.Fatalf("open ends %+v", r.Bands)
	}
	if *r.Bands[1].SlowPaceSecondsPerKm != 400 || *r.Bands[1].FastPaceSecondsPerKm != 333.3 {
		t.Fatalf("Z2 band %+v", r.Bands[1])
	}
}

func TestBuildPaceZonesExcludesPausesStationaryAndConflicts(t *testing.T) {
	obs := []Observation{
		speedObs(0, 200, 3.2),
		speedObs(200, 300, 0),   // stationary
		speedObs(300, 400, 3.2), // identical overlap counts once
		speedObs(300, 400, 3.2),
		speedObs(400, 500, 3.2), // conflicting overlap
		speedObs(400, 500, 4.8),
	}
	events := []EventBoundary{{Type: 1, StartUs: 100e6}, {Type: 2, StartUs: 150e6}}
	r := BuildPaceZones(0, 500e6, 500, obs, events, syntheticPaceProfile())
	if r.Status != PaceZonesAvailable {
		t.Fatalf("status %+v", r)
	}
	var total float64
	for _, e := range r.Exposure {
		total += e.DurationSeconds
	}
	if total != 250 || r.Exposure[2].DurationSeconds != 250 {
		t.Fatalf("exposure %+v", r.Exposure)
	}
	for _, c := range []string{"ambiguous_speed_intervals_excluded", "stationary_speed_excluded"} {
		if !slices.Contains(r.Codes, c) {
			t.Fatalf("missing %s in %v", c, r.Codes)
		}
	}
}

func TestBuildPaceZonesUnavailableStates(t *testing.T) {
	if r := BuildPaceZones(0, 60e6, 60, []Observation{speedObs(0, 60, 3)}, nil, nil); r.Status != PaceZonesProfileMissing || len(r.Exposure) != 0 {
		t.Fatalf("no profile %+v", r)
	}
	conflict := syntheticPaceProfile()
	conflict.Zones.Zones[2].FastSpeed = nil
	if r := BuildPaceZones(0, 60e6, 60, []Observation{speedObs(0, 60, 3)}, nil, conflict); r.Status != PaceZonesAnchorConflict || len(r.Exposure) != 0 {
		t.Fatalf("conflict %+v", r)
	}
	inverted := syntheticPaceProfile()
	slow := 2.9
	inverted.Zones.Zones[2].FastSpeed = &slow
	if r := BuildPaceZones(0, 60e6, 60, nil, nil, inverted); r.Status != PaceZonesAnchorConflict {
		t.Fatalf("inverted %+v", r)
	}
	empty := &PaceZoneProfile{Date: "2026-11-11", Zones: rf.Zones{Zones: make([]rf.Zone, 6)}}
	if r := BuildPaceZones(0, 60e6, 60, nil, nil, empty); r.Status != PaceZonesProfileMissing {
		t.Fatalf("empty %+v", r)
	}
	if r := BuildPaceZones(0, 60e6, 60, []Observation{{Type: RunningSpeed, StartUs: 10e6, EndUs: 10e6, Value: 3, Unit: "m/s"}}, nil, syntheticPaceProfile()); r.Status != PaceZonesSpeedUnavailable || len(r.Bands) != 6 {
		t.Fatalf("points only %+v", r)
	}
}

func TestAttachPaceZonesKeepsHeartRateStimulus(t *testing.T) {
	z := NewZoneSummary()
	z.StimulusType = "aerobic_base"
	AttachPaceZones(z, BuildPaceZones(0, 60e6, 60, []Observation{speedObs(0, 60, 3.2)}, nil, syntheticPaceProfile()))
	if z.StimulusType != "aerobic_base" || z.PaceZoneStatus != PaceZonesAvailable || z.PaceCoverage != 1 || len(z.PaceZones) != 6 {
		t.Fatalf("summary %+v", z)
	}
}

// Paused time never carries pace, so coverage is measured against moving time.
// Overlapping pause and motion-pause spans are subtracted once.
func TestBuildPaceZonesCoverageExcludesPausedTime(t *testing.T) {
	obs := []Observation{speedObs(0, 100, 3.2), speedObs(200, 500, 3.2)}
	events := []EventBoundary{
		{Type: 1, StartUs: 100e6}, {Type: 2, StartUs: 200e6}, // pause 100–200
		{Type: 5, StartUs: 150e6}, {Type: 6, StartUs: 250e6}, // motion pause 150–250 overlaps
	}
	r := BuildPaceZones(0, 600e6, 600, obs, events, syntheticPaceProfile())
	// moving = 600 - 150 (union 100–250); covered = 100 + 250 (200–250 paused)
	if r.Status != PaceZonesAvailable || r.Coverage != 0.7778 {
		t.Fatalf("coverage %v status %s", r.Coverage, r.Status)
	}
}
