package postgres

import (
	"math"
	"rai/server/internal/workoutdetail"
	"testing"
	"time"
)

func TestHeartCoverageRegularSamplingAndRealGaps(t *testing.T) {
	for _, tc := range []struct {
		name string
		skip bool
		want float64
	}{
		{"regular_five_second_points", false, 1}, {"real_gap_stays_missing", true, .9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var obs []workoutdetail.Observation
			for sec := 0; sec <= 600; sec += 5 {
				if tc.skip && sec > 270 && sec < 330 {
					continue
				}
				obs = append(obs, workoutdetail.Observation{Type: workoutdetail.HeartRate, StartUs: int64(sec) * 1e6, EndUs: int64(sec) * 1e6, Value: 150})
			}
			got := seriesCoverage(obs, workoutdetail.HeartRate, 0, 600e6)
			if got == nil || math.Abs(*got-tc.want) > 1e-12 {
				t.Fatalf("coverage %v want %v", got, tc.want)
			}
		})
	}
}

func TestHeartCoveragePrefersPublishedActiveSupport(t *testing.T) {
	d := workoutDetailData{Start: time.Unix(0, 0), End: time.Unix(600, 0), FeatureArtifact: &workoutdetail.FeatureAnalysis{HeartRateCoverage: .98}}
	if got := d.heartCoverage(); got == nil || *got != .98 {
		t.Fatalf("coverage %v", got)
	}
	d.FeatureArtifact.HeartRateCoverage = 0
	if got := d.heartCoverage(); got == nil || *got != 0 {
		t.Fatalf("zero coverage lost: %v", got)
	}
}
