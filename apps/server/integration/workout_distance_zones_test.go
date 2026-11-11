//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"rai/server/internal/modelinput"
	"rai/server/internal/syncing"
	"rai/server/internal/workoutdetail"
)

func TestWorkoutDetailDistanceDistributionUsesOriginalIntervalsAndHistoricalReference(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := syncing.NewID()
	var payload map[string]any
	if err := json.Unmarshal(ingestV6Raw(t, f.p.DatasetID, f.enrollment.DatasetEpoch, f.enrollment.CollectorEpoch, id, 140), &payload); err != nil {
		t.Fatal(err)
	}
	payload["workout"].(map[string]any)["totalDistance"] = map[string]any{"value": 210, "unit": "m"}
	payload["streams"] = append(payload["streams"].([]any), map[string]any{
		"type": workoutdetail.RunningSpeed, "unit": "m/s", "status": "complete",
		"samples": [][]float64{{0, 30e6, 5}, {30e6, 60e6, 2}},
	})
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	acceptIngest(t, f, f.p, raw)
	for _, out := range processAllIngests(t, f.s) {
		if out.Status != "committed" {
			t.Fatalf("synthetic ingestion: %s", out.Status)
		}
	}
	read := func() *workoutdetail.DistanceDistribution {
		t.Helper()
		d, err := f.s.WorkoutDetail(ctx, f.p, id)
		if err != nil {
			t.Fatal(err)
		}
		if d.Workout.AnalysisAvailable {
			t.Fatal("fixture unexpectedly published analysis")
		}
		if d.DistanceDistribution == nil {
			t.Fatal("pending analysis hid measured distribution")
		}
		return d.DistanceDistribution
	}
	near := func(got, want float64) {
		t.Helper()
		if math.Abs(got-want) > 1e-8 {
			t.Fatalf("distance got %g want %g", got, want)
		}
	}
	v := read()
	near(v.Pace[0].DistanceMeters, 150)
	near(v.Pace[4].DistanceMeters, 60)
	near(v.HeartRateUnknownMeters, 210)
	for _, spec := range []struct {
		from      string
		rest, max float64
	}{
		{"2026-10-01T00:00:00Z", 50, 200},
		{"2026-11-02T00:00:00Z", 60, 180}, // after the Workout: must not rezone it
	} {
		if _, err := time.Parse(time.RFC3339, spec.from); err != nil {
			t.Fatal(err)
		}
		_, err := f.s.CommitModelInput(ctx, f.p, modelinput.Request{ID: syncing.NewID(), Kind: "hr_reference", SubjectKey: "athlete", EffectiveFrom: spec.from, Provenance: "synthetic integration reference", Payload: modelinput.Payload{
			"maxHR": spec.max, "restingHR": spec.rest, "measuredAt": spec.from, "restingMeasuredAt": spec.from, "method": "user_entered", "unit": "bpm",
		}})
		if err != nil {
			t.Fatal(err)
		}
	}
	v = read()
	if v.HeartRateProfileID != "hrr:50:200" {
		t.Fatalf("wrong historical reference: %s", v.HeartRateProfileID)
	}
	near(v.HeartRate[1].DistanceMeters, 210)
	near(v.HeartRateUnknownMeters, 0)
}
