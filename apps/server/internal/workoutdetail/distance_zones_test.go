package workoutdetail

import (
	"math"
	"slices"
	"testing"
)

func distanceObservation(kind string, a, b int64, v float64) Observation {
	unit := "m/s"
	if kind == HeartRate {
		unit = "count/min"
	}
	return Observation{Type: kind, StartUs: a * 1e6, EndUs: b * 1e6, Value: v, Unit: unit}
}

func nearDistance(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-8 {
		t.Fatalf("distance got %g want %g", got, want)
	}
}

func TestDistanceDistributionAlignsIntervalsWithoutAveragingIntervals(t *testing.T) {
	// A fast/slow interval workout has 500 m in each pace band. HR changes
	// halfway through the fast interval; it must split distance, not time.
	obs := []Observation{
		distanceObservation(RunningSpeed, 0, 100, 5),
		distanceObservation(RunningSpeed, 100, 350, 2),
		distanceObservation(HeartRate, 0, 50, 140),
		distanceObservation(HeartRate, 50, 350, 185),
	}
	v := BuildDistanceDistribution(0, 350e6, Ptr(1000), obs, nil, 50, 200)
	nearDistance(t, v.Pace[0].DistanceMeters, 500)
	nearDistance(t, v.Pace[4].DistanceMeters, 500)
	nearDistance(t, v.HeartRate[1].DistanceMeters, 250)
	nearDistance(t, v.HeartRate[4].DistanceMeters, 750)
	nearDistance(t, v.PaceUnknownMeters, 0)
	nearDistance(t, v.HeartRateUnknownMeters, 0)
}

func TestDistanceDistributionPreservesGapsAndConflictingOverlaps(t *testing.T) {
	obs := []Observation{
		distanceObservation(RunningSpeed, 0, 100, 4),
		distanceObservation(RunningSpeed, 0, 100, 4), // identical duplicate
		distanceObservation(RunningSpeed, 20, 40, 5), // conflict: 80 m excluded
		distanceObservation(HeartRate, 0, 60, 150),
		distanceObservation(HeartRate, 0, 60, 150),        // HR duplicate
		distanceObservation(HeartRate, 40, 50, 170),       // HR conflict: 40 m excluded
		distanceObservation(RunningSpeed, 100, 100, 1000), // no extrapolation
	}
	v := BuildDistanceDistribution(0, 200e6, Ptr(800), obs, nil, 50, 200)
	nearDistance(t, v.Pace[1].DistanceMeters, 320)
	nearDistance(t, v.PaceUnknownMeters, 480)
	nearDistance(t, v.HeartRate[1].DistanceMeters, 120)
	nearDistance(t, v.HeartRateUnknownMeters, 680)
	for _, code := range []string{"ambiguous_speed_intervals_excluded", "ambiguous_heart_rate_intervals_excluded"} {
		if !slices.Contains(v.ReasonCodes, code) {
			t.Fatalf("missing reason %s", code)
		}
	}
}

func TestDistanceDistributionFixedPaceAndHRRBoundaries(t *testing.T) {
	for i, pace := range []float64{239, 240, 300, 360, 420} {
		obs := []Observation{distanceObservation(RunningSpeed, 0, 100, 1000/pace), distanceObservation(HeartRate, 0, 100, []float64{139, 140, 155, 170, 185}[i])}
		v := BuildDistanceDistribution(0, 100e6, Ptr(1000), obs, nil, 50, 200)
		nearDistance(t, v.Pace[i].DistanceMeters, 100000/pace)
		nearDistance(t, v.HeartRate[i].DistanceMeters, 100000/pace)
	}
}

func TestDistanceDistributionOfficialDriftScalesOnlyDown(t *testing.T) {
	obs := []Observation{distanceObservation(RunningSpeed, 0, 100, 4), distanceObservation(HeartRate, 0, 50, 140)}
	for _, test := range []struct{ total, pace, hr, paceUnknown, hrUnknown float64 }{{500, 400, 200, 100, 300}, {300, 300, 150, 0, 150}, {0, 0, 0, 0, 0}} {
		v := BuildDistanceDistribution(0, 100e6, Ptr(test.total), obs, nil, 50, 200)
		nearDistance(t, v.Pace[1].DistanceMeters, test.pace)
		nearDistance(t, v.HeartRate[1].DistanceMeters, test.hr)
		nearDistance(t, v.PaceUnknownMeters, test.paceUnknown)
		nearDistance(t, v.HeartRateUnknownMeters, test.hrUnknown)
	}
}

func TestDistanceDistributionPausesMissingReferenceAndUnits(t *testing.T) {
	obs := []Observation{distanceObservation(RunningSpeed, -10, 110, 4), distanceObservation(HeartRate, 0, 100, 140)}
	obs[0].Value, obs[0].Unit = 14.4, "km/h"
	events := []EventBoundary{{Type: 1, StartUs: 20e6, EndUs: 20e6}, {Type: 2, StartUs: 40e6, EndUs: 40e6}, {Type: 5, StartUs: 60e6, EndUs: 70e6}}
	v := BuildDistanceDistribution(0, 100e6, Ptr(400), obs, events, 0, 0)
	nearDistance(t, v.Pace[1].DistanceMeters, 280)
	nearDistance(t, v.PaceUnknownMeters, 120)
	nearDistance(t, v.HeartRateUnknownMeters, 400)
	if v.HeartRateProfileID != "" {
		t.Fatal("invented HR reference")
	}
	if BuildDistanceDistribution(0, 100, nil, obs, nil, 50, 200) != nil {
		t.Fatal("missing distance must stay missing")
	}
}

func TestDistanceDistributionZeroSpeedConflictsAndExtremeInputs(t *testing.T) {
	obs := []Observation{distanceObservation(RunningSpeed, 0, 100, 4), distanceObservation(RunningSpeed, 20, 40, 0)}
	v := BuildDistanceDistribution(0, 100e6, Ptr(400), obs, nil, 50, 200)
	nearDistance(t, v.Pace[1].DistanceMeters, 320)
	nearDistance(t, v.PaceUnknownMeters, 80)
	for _, value := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		v := BuildDistanceDistribution(0, 100e6, Ptr(400), []Observation{distanceObservation(RunningSpeed, 0, 100, value)}, nil, 50, 200)
		nearDistance(t, v.PaceUnknownMeters, 400)
		if BuildDistanceDistribution(0, 100e6, Ptr(value), nil, nil, 50, 200) != nil {
			t.Fatal("non-finite total exposed")
		}
	}
	for _, obs := range [][]Observation{
		{distanceObservation(RunningSpeed, 0, 100, math.MaxFloat64)},                                                         // product overflow
		{distanceObservation(RunningSpeed, 0, 1, math.MaxFloat64), distanceObservation(RunningSpeed, 1, 2, math.MaxFloat64)}, // sum overflow
	} {
		if BuildDistanceDistribution(0, 100e6, Ptr(400), obs, nil, 50, 200) != nil {
			t.Fatal("overflow must be unavailable")
		}
	}
}
