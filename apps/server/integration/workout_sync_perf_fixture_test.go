//go:build integration

package integration

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	"rai/server/internal/syncing"
)

// Synthetic fixtures for the R0 normal-build sync performance entry point
// (scripts/local/test.sh syncperf).
//
// Two fixture families exist:
//
//   - the round-1 workload shapes (S20/S60/S120/S240) produced by
//     performanceBundle, reused verbatim so round-1 and round-2 numbers describe
//     the same payload;
//   - the S60H dense shape: 10,824 samples, 10,824 series points and 3,491 route
//     points, carrying every supported source/metadata/provenance/coverage field.
//
// All identities are derived deterministically from (profile, iteration) so a
// fixture digest is reproducible across runs while every timed iteration is
// still a genuinely fresh first import.

// perfDenseTypeCounts fixes the dense sample distribution: 7 sample types,
// 10,824 samples in total.
var perfDenseTypeCounts = []struct {
	Type  string
	Count int
}{
	{kind, 1120}, // HKQuantityTypeIdentifierHeartRate
	{"HKQuantityTypeIdentifierRunningPower", 2180},
	{"HKQuantityTypeIdentifierStepCount", 2180},
	{"HKQuantityTypeIdentifierDistanceWalkingRunning", 2170},
	{"HKQuantityTypeIdentifierRunningStrideLength", 1040},
	{"HKQuantityTypeIdentifierRunningGroundContactTime", 1054},
	{"HKQuantityTypeIdentifierRunningVerticalOscillation", 1080},
}

const (
	perfDenseSamples     = 10824
	perfDenseSeries      = 4
	perfDenseSeriesTotal = 10824
	perfDenseRoutePoints = 3491
	perfDenseSeconds     = 3600
)

// perfProfileMinutes maps a round-1 profile name to its workout length.
func perfProfileMinutes(name string) (int, bool) {
	switch name {
	case "S20":
		return 20, true
	case "S60":
		return 60, true
	case "S120":
		return 120, true
	case "S240":
		return 240, true
	}
	return 0, false
}

// perfBundle builds the fixture for one profile and one deterministic seed.
func perfBundle(t *testing.T, f *fixture, profile string, seed uint64) syncing.WorkoutBundle {
	t.Helper()
	var bundle syncing.WorkoutBundle
	if minutes, ok := perfProfileMinutes(profile); ok {
		bundle = perfWorkloadBundle(t, f, minutes)
		reseedBundle(&bundle, seed)
		phase3Observation(&bundle)
		return bundle
	}
	if profile != "S60H" {
		t.Fatalf("unknown performance profile %q", profile)
	}
	bundle = perfDenseWorkloadBundle(t, f)
	reseedBundle(&bundle, seed)
	perfDenseObservation(&bundle)
	return bundle
}

// perfWorkloadBundle mirrors performanceBundle without its trailing
// phase3Observation call, so identities can be reseeded first.
func perfWorkloadBundle(t *testing.T, f *fixture, minutes int) syncing.WorkoutBundle {
	t.Helper()
	start := time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)
	b, _ := testWorkoutBundle(t, f, 0)
	b.Workout = workoutAt(start, minutes*60)
	b.WorkoutID = b.Workout.UUID
	// A slice, not a map: the sample order decides which deterministic identity
	// each (type, time) pair receives, so map iteration would make two runs of
	// the same seed produce different facts.
	density := []struct {
		Type  string
		Per40 int
	}{
		{kind, 456},
		{"HKQuantityTypeIdentifierRunningPower", 887},
		{"HKQuantityTypeIdentifierStepCount", 887},
		{"HKQuantityTypeIdentifierDistanceWalkingRunning", 884},
		{"HKQuantityTypeIdentifierRunningStrideLength", 423},
		{"HKQuantityTypeIdentifierRunningGroundContactTime", 428},
		{"HKQuantityTypeIdentifierRunningVerticalOscillation", 432},
	}
	for _, entry := range density {
		typ, count := entry.Type, entry.Per40*minutes/40
		for i := 0; i < count; i++ {
			sample := bundleSample(b.WorkoutID, start.Add(time.Duration(i)*time.Second), 100+float64(i%50))
			sample.Type = typ
			sample.Quantity = &syncing.Quantity{Value: 100 + float64(i%50), Unit: syncing.Units[typ]}
			b.Samples = append(b.Samples, sample)
		}
	}
	points := 2283 * minutes / 40
	b.Routes[0].Points = nil
	for i := 0; i < points; i++ {
		b.Routes[0].Points = append(b.Routes[0].Points, syncing.WorkoutBundlePoint{
			RecordedAtUs: start.Add(time.Duration(i) * time.Second).UnixMicro(),
			Latitude:     31.2 + float64(i%100)*.00001,
			Longitude:    121.4,
		})
	}
	return b
}

// perfDenseWorkloadBundle builds the S60H dense shape.
func perfDenseWorkloadBundle(t *testing.T, f *fixture) syncing.WorkoutBundle {
	t.Helper()
	start := time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)
	b, _ := testWorkoutBundle(t, f, 0)
	b.Workout = perfDenseWorkout(start, perfDenseSeconds)
	b.WorkoutID = b.Workout.UUID
	b.Samples = nil
	b.Series = nil
	seriesParents := make([]int, 0, perfDenseSeries)
	for _, entry := range perfDenseTypeCounts {
		for i := 0; i < entry.Count; i++ {
			stamp := start.Add(time.Duration(int64(perfDenseSeconds)*int64(i)/int64(entry.Count)) * time.Second)
			sample := bundleSample(b.WorkoutID, stamp, 100+float64(i%50))
			sample.Type = entry.Type
			sample.Quantity = &syncing.Quantity{Value: 100 + float64(i%50), Unit: syncing.Units[entry.Type]}
			sample.Metadata = perfSampleMetadata(i)
			b.Samples = append(b.Samples, sample)
			if entry.Type == kind && len(seriesParents) < perfDenseSeries {
				seriesParents = append(seriesParents, len(b.Samples)-1)
			}
		}
	}
	if len(b.Samples) != perfDenseSamples {
		t.Fatalf("dense sample count = %d, want %d", len(b.Samples), perfDenseSamples)
	}
	// Four heart-rate samples each carry an evenly sampled series, so the dense
	// profile keeps the per-parent series shape of the round-1 fixtures.
	perSeries := perfDenseSeriesTotal / perfDenseSeries
	for _, index := range seriesParents {
		parent := &b.Samples[index]
		seriesStart := start.Add(time.Duration(index) * time.Second)
		seriesEnd := start.Add(time.Duration(perfDenseSeconds-1) * time.Second)
		parent.StartUs = ptr(seriesStart.UnixMicro())
		parent.EndUs = ptr(seriesEnd.UnixMicro())
		points := make([]syncing.SeriesPoint, 0, perSeries)
		for i := 0; i < perSeries; i++ {
			at := seriesStart.Add(time.Duration(int64(perfDenseSeconds)*int64(i)/int64(perSeries)) * time.Second)
			if at.After(seriesEnd) {
				at = seriesEnd
			}
			points = append(points, syncing.SeriesPoint{
				StartUs: at.UnixMicro(),
				EndUs:   at.UnixMicro(),
				Value:   100 + float64(i%50),
				Unit:    syncing.Units[kind],
			})
		}
		b.Series = append(b.Series, syncing.WorkoutBundleSeries{
			SampleType: kind,
			SampleUUID: parent.UUID,
			Points:     points,
		})
	}
	b.Routes[0].Points = nil
	for i := 0; i < perfDenseRoutePoints; i++ {
		point := syncing.WorkoutBundlePoint{
			RecordedAtUs: start.Add(time.Duration(i) * time.Second).UnixMicro(),
			Latitude:     31.2 + float64(i%100)*.00001,
			Longitude:    121.4 + float64(i%100)*.00001,
		}
		point.Altitude = ptr(10 + float64(i%50))
		point.HorizontalAccuracy = ptr(3 + float64(i%5))
		point.VerticalAccuracy = ptr(4 + float64(i%5))
		point.Speed = ptr(2.5 + float64(i%3))
		point.Course = ptr(float64(i % 360))
		b.Routes[0].Points = append(b.Routes[0].Points, point)
	}
	b.CompleteSampleTypes = []string{kind, "HKQuantityTypeIdentifierRunningPower"}
	return b
}

func perfDenseWorkout(start time.Time, seconds int) syncing.Event {
	workout := workoutAt(start, seconds)
	startUs, endUs := *workout.StartUs, *workout.EndUs
	workout.Workout.TotalDistance = &syncing.Quantity{Value: 10000, Unit: "m"}
	workout.Workout.TotalEnergyBurned = &syncing.Quantity{Value: 700, Unit: "kcal"}
	workout.Workout.Statistics = []syncing.WorkoutStatistic{
		{
			Type:    "HKQuantityTypeIdentifierActiveEnergyBurned",
			StartUs: &startUs, EndUs: &endUs,
			Sum:     &syncing.Quantity{Value: 700, Unit: "kcal"},
			Average: &syncing.Quantity{Value: 11.7, Unit: "kcal"},
			Minimum: &syncing.Quantity{Value: 9.5, Unit: "kcal"},
			Maximum: &syncing.Quantity{Value: 13.1, Unit: "kcal"},
		},
		{
			Type:    "HKQuantityTypeIdentifierDistanceWalkingRunning",
			StartUs: &startUs, EndUs: &endUs,
			Sum: &syncing.Quantity{Value: 10000, Unit: "m"},
		},
		{
			Type:    "HKQuantityTypeIdentifierHeartRate",
			StartUs: &startUs, EndUs: &endUs,
			Average: &syncing.Quantity{Value: 145, Unit: "count/min"},
			Minimum: &syncing.Quantity{Value: 98, Unit: "count/min"},
			Maximum: &syncing.Quantity{Value: 181, Unit: "count/min"},
		},
	}
	workout.Workout.Events = []syncing.WorkoutEvent{
		{Type: ptr(int64(1)), StartUs: &startUs, EndUs: &endUs, Metadata: map[string]any{"HKIndoorWorkout": false}},
	}
	workout.Workout.Activities = []syncing.WorkoutActivity{
		{
			UUID:       syncing.NewID(),
			StartUs:    &startUs,
			EndUs:      &endUs,
			DurationUs: workout.Workout.DurationUs,
			Configuration: &syncing.WorkoutConfiguration{
				ActivityType:         ptr(int64(37)),
				LocationType:         ptr(int64(1)),
				SwimmingLocationType: ptr(int64(0)),
				LapLength:            &syncing.Quantity{Value: 25, Unit: "m"},
			},
			Metadata: map[string]any{"WOIntervalStepKeyPath": "step-1", "WOIntervalStepSuccessful": 1},
			Statistics: []syncing.WorkoutStatistic{
				{
					Type:    "HKQuantityTypeIdentifierHeartRate",
					StartUs: &startUs, EndUs: &endUs,
					Average: &syncing.Quantity{Value: 145, Unit: "count/min"},
				},
			},
			Events: []syncing.WorkoutEvent{
				{Type: ptr(int64(2)), StartUs: &startUs, EndUs: &endUs},
			},
		},
	}
	return workout
}

// perfSampleMetadata returns every supported per-sample metadata key.
func perfSampleMetadata(index int) map[string]any {
	return map[string]any{
		"HKTimeZone":                          "Asia/Shanghai",
		"HKIndoorWorkout":                     false,
		"HKMetadataKeyHeartRateMotionContext": index % 3,
		"HKMetadataKeyDevicePlacementSide":    index % 5,
	}
}

// perfDenseObservation attaches full provenance, every metadata key and complete
// coverage for the dense fixture.
func perfDenseObservation(b *syncing.WorkoutBundle) {
	source := syncing.HealthKitSource{
		BundleID:               "com.apple.health",
		Name:                   "Synthetic Dense Watch source",
		Version:                "2",
		ProductType:            "Watch7,1",
		OperatingSystemVersion: "11.0",
		Device: &syncing.HealthKitDevice{
			Manufacturer:    "Apple Inc.",
			Model:           "Watch",
			HardwareVersion: "Watch7,1",
			FirmwareVersion: "11.0",
			SoftwareVersion: "11.0",
		},
	}
	b.Workout.Source.BundleID = source.BundleID
	provenance := syncing.BundleProvenance{
		Workout: source,
		Samples: make([]syncing.SampleProvenance, 0, len(b.Samples)),
		Routes:  make([]syncing.RouteProvenance, 0, len(b.Routes)),
	}
	counts := make(map[string]int, len(perfDenseTypeCounts))
	for i := range b.Samples {
		b.Samples[i].Source.BundleID = source.BundleID
		provenance.Samples = append(provenance.Samples, syncing.SampleProvenance{
			SampleType: b.Samples[i].Type,
			SampleUUID: b.Samples[i].UUID,
			Source:     source,
		})
		counts[b.Samples[i].Type]++
	}
	for i := range b.Routes {
		b.Routes[i].Source.BundleID = source.BundleID
		provenance.Routes = append(provenance.Routes, syncing.RouteProvenance{
			RouteID: b.Routes[i].RouteID,
			Source:  source,
		})
	}
	coverage := make(map[string]syncing.BundleCoverage, len(counts)+5)
	coverage["workout"] = syncing.BundleCoverage{State: "complete", SampleCount: 1, QueryCompleted: true, LocalPersisted: true}
	for typ, n := range counts {
		coverage[typ] = syncing.BundleCoverage{State: "complete", SampleCount: n, QueryCompleted: true, LocalPersisted: true}
	}
	seriesPoints := 0
	for _, series := range b.Series {
		seriesPoints += len(series.Points)
	}
	coverage["series:"+kind] = syncing.BundleCoverage{State: "complete", SampleCount: seriesPoints, QueryCompleted: true, LocalPersisted: true}
	routePoints := 0
	for _, route := range b.Routes {
		routePoints += len(route.Points)
		coverage["route:"+route.RouteID] = syncing.BundleCoverage{State: "complete", SampleCount: len(route.Points), QueryCompleted: true, LocalPersisted: true}
	}
	coverage["route"] = syncing.BundleCoverage{State: "complete", SampleCount: routePoints, QueryCompleted: true, LocalPersisted: true}
	coverage["HKQuantityTypeIdentifierVO2Max"] = syncing.BundleCoverage{State: "no_records_observed", SampleCount: 0, QueryCompleted: true, LocalPersisted: true}
	b.Observation = &syncing.BundleObservation{
		Version:      1,
		ObservedAtUs: 1760000000000000,
		Metadata: map[string]any{
			"HKTimeZone":           "Asia/Shanghai",
			"HKIndoorWorkout":      false,
			"HKAverageMETs":        map[string]any{"value": 9.5, "unit": "kcal/(kg*hr)"},
			"HKElevationAscended":  map[string]any{"value": 120.0, "unit": "m"},
			"HKElevationDescended": map[string]any{"value": 118.0, "unit": "m"},
			"HKWeatherHumidity":    map[string]any{"value": 55.0, "unit": "1"},
			"HKWeatherTemperature": map[string]any{"value": 18.5, "unit": "degC"},
			"HKAverageSpeed":       map[string]any{"value": 2.9, "unit": "m/s"},
			"HKMaximumSpeed":       map[string]any{"value": 3.7, "unit": "m/s"},
		},
		Provenance: provenance,
		Coverage:   coverage,
	}
}

// reseedBundle replaces every generated identity with a deterministic one, so a
// fixture digest is reproducible while each iteration stays a fresh import.
func reseedBundle(b *syncing.WorkoutBundle, seed uint64) {
	next := perfIDGenerator(seed)
	workoutID := next("workout", 0)
	b.Workout.UUID = workoutID
	b.WorkoutID = workoutID
	if b.Workout.Workout != nil {
		for i := range b.Workout.Workout.Activities {
			b.Workout.Workout.Activities[i].UUID = next("activity", i)
		}
	}
	replaced := make(map[string]string, len(b.Samples))
	for i := range b.Samples {
		id := next("sample", i)
		replaced[b.Samples[i].UUID] = id
		b.Samples[i].UUID = id
	}
	for i := range b.Series {
		if id, ok := replaced[b.Series[i].SampleUUID]; ok {
			b.Series[i].SampleUUID = id
		}
	}
	for i := range b.Routes {
		b.Routes[i].RouteID = next("route", i)
	}
	for i := range b.SampleDeletions {
		if id, ok := replaced[b.SampleDeletions[i].SampleUUID]; ok {
			b.SampleDeletions[i].SampleUUID = id
		}
	}
	for i := range b.RouteDeletions {
		b.RouteDeletions[i] = next("route-deletion", i)
	}
}

// perfIDGenerator returns a deterministic RFC 4122 version 4 shaped identifier
// generator keyed by seed, category and ordinal.
func perfIDGenerator(seed uint64) func(string, int) string {
	return func(category string, index int) string {
		var prefix [16]byte
		binary.BigEndian.PutUint64(prefix[:8], seed)
		binary.BigEndian.PutUint64(prefix[8:], uint64(index))
		sum := sha256.Sum256(append(prefix[:], category...))
		var id [16]byte
		copy(id[:], sum[:16])
		id[6] = (id[6] & 0x0f) | 0x40
		id[8] = (id[8] & 0x3f) | 0x80
		return fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:])
	}
}
