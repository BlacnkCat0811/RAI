package workoutdetail

import (
	"math"
	"slices"
	"testing"

	wf "rai/server/internal/workoutfeatures"
)

func paceSnapshot() wf.RawWorkoutSnapshot {
	source := wf.NewSource("com.apple.health", "apple_watch", []byte(`{"productType":"Watch7,1"}`))
	raw := wf.RawWorkoutSnapshot{Start: 0, End: 20e6, DurationUs: 20e6, Policy: wf.DefaultPolicy(), Provenance: []wf.Source{source}}
	raw.Quantities = []wf.Quantity{
		{Type: wf.Speed, Start: 0, End: 0, Value: 2, Unit: "m/s", Source: source.Epoch, Support: wf.InstantPoint, Fact: wf.FactRef{State: wf.Present}},
		{Type: wf.Speed, Start: 10e6, End: 10e6, Value: 5, Unit: "m/s", Source: source.Epoch, Support: wf.InstantPoint, Fact: wf.FactRef{State: wf.Present}},
	}
	return raw
}

func TestSupportedPaceSplitsLinearCrossingsWithoutExtrapolation(t *testing.T) {
	raw := paceSnapshot()
	result := BuildSupportedPaceZones(raw, syntheticPaceProfile())
	if result.Status != PaceZonesAvailable || result.Coverage != .5 || result.ClassifiedSeconds != 10 || result.ReconstructedSeconds != 10 || result.ObservedSeconds != 0 {
		t.Fatalf("result %+v", result)
	}
	var distance float64
	for _, zone := range result.Exposure {
		if math.Abs(zone.DurationSeconds-1.7) > .01 {
			t.Fatalf("crossing %+v", zone)
		}
		distance += zone.DistanceMeters
	}
	if math.Abs(distance-35) > .2 {
		t.Fatalf("integral %v", distance)
	}
	if slices.Contains(result.Codes, "speed_intervals_unavailable") {
		t.Fatal("initializer diagnostic leaked")
	}
}

func TestSupportedPaceNeverBridgesBarriers(t *testing.T) {
	for _, name := range []string{"long_gap", "pause", "source_change", "conflicting_point", "outlier"} {
		t.Run(name, func(t *testing.T) {
			raw := paceSnapshot()
			switch name {
			case "long_gap":
				raw.Quantities[1].Start, raw.Quantities[1].End = 16e6, 16e6
			case "pause":
				raw.Events = []wf.Event{{Type: 1, Start: 4e6, End: 4e6}, {Type: 2, Start: 6e6, End: 6e6}}
				raw.DurationUs = 18e6
			case "source_change":
				src := wf.NewSource("com.apple.health", "apple_watch", []byte(`{"productType":"Watch8,1"}`))
				raw.Provenance = append(raw.Provenance, src)
				raw.Quantities[1].Source = src.Epoch
			case "conflicting_point":
				q := raw.Quantities[0]
				q.Value = 3
				raw.Quantities = append(raw.Quantities, q)
			case "outlier":
				raw.Quantities[1].Value = 20
			}
			result := BuildSupportedPaceZones(raw, syntheticPaceProfile())
			if result.Status != PaceZonesSpeedUnavailable || result.ClassifiedSeconds != 0 {
				t.Fatalf("barrier ignored %+v", result)
			}
		})
	}
}

func TestSupportedPaceEvidenceAndUnavailableStates(t *testing.T) {
	raw := paceSnapshot()
	profile := syntheticPaceProfile()
	profile.Zones.Zones[5].Support = "insufficient_evidence"
	result := BuildSupportedPaceZones(raw, profile)
	if result.Status != PaceZonesAvailable || math.Abs(result.ClassifiedSeconds-25.0/3) > .00001 || result.Exposure[5].DurationSeconds != 0 || result.Bands[5].SlowPaceSecondsPerKm != nil {
		t.Fatalf("unsupported zone classified %+v", result)
	}
	for i := range profile.Zones.Zones {
		profile.Zones.Zones[i].Support = "insufficient_evidence"
	}
	if result = BuildSupportedPaceZones(raw, profile); result.Status != "zone_evidence_insufficient" || len(result.Exposure) != 0 {
		t.Fatalf("unsupported %+v", result)
	}
	profile.Expired = true
	if result = BuildSupportedPaceZones(raw, profile); result.Status != "profile_expired" || result.AnalyzableSeconds != 20 {
		t.Fatalf("expired %+v", result)
	}
	raw.DurationUs = 1
	if result = BuildSupportedPaceZones(raw, syntheticPaceProfile()); result.Status != "timing_unavailable" {
		t.Fatalf("timing %+v", result)
	}
}

func TestSupportedPaceDeduplicatesAndRejectsSourceOverlap(t *testing.T) {
	raw := paceSnapshot()
	q := raw.Quantities[0]
	q.End = 10e6
	q.Support = wf.IntervalObservation
	raw.Quantities = []wf.Quantity{q, q}
	r := BuildSupportedPaceZones(raw, syntheticPaceProfile())
	if r.ObservedSeconds != 10 || r.ReconstructedSeconds != 0 || r.ClassifiedSeconds != 10 {
		t.Fatalf("duplicates %+v", r)
	}
	other := wf.NewSource("com.apple.health", "apple_watch", []byte(`{"device":"other"}`))
	raw.Provenance = append(raw.Provenance, other)
	q.Source = other.Epoch
	raw.Quantities = append(raw.Quantities, q)
	r = BuildSupportedPaceZones(raw, syntheticPaceProfile())
	if r.ClassifiedSeconds != 0 || !slices.Contains(r.Codes, string(wf.SourceConflict)) {
		t.Fatalf("conflict %+v", r)
	}
}

func TestSupportedPaceBoundaryEqualityAndDescendingSignal(t *testing.T) {
	raw := paceSnapshot()
	raw.Quantities[0].Value, raw.Quantities[1].Value = 5, 2
	r := BuildSupportedPaceZones(raw, syntheticPaceProfile())
	if r.Status != PaceZonesAvailable || r.ClassifiedSeconds != 10 {
		t.Fatalf("descending %+v", r)
	}
	q := raw.Quantities[0]
	q.Value = 4
	q.End = 10e6
	q.Support = wf.IntervalObservation
	raw.Quantities = []wf.Quantity{q}
	r = BuildSupportedPaceZones(raw, syntheticPaceProfile())
	if r.Exposure[4].DurationSeconds != 10 || r.Exposure[3].DurationSeconds != 0 {
		t.Fatalf("exact boundary %+v", r)
	}
	profile := syntheticPaceProfile()
	profile.Zones.Zones[4].SlowSpeed = Ptr(3.9)
	if r = BuildSupportedPaceZones(raw, profile); r.Status != PaceZonesAnchorConflict {
		t.Fatalf("inconsistent shared boundary %+v", r)
	}
}
