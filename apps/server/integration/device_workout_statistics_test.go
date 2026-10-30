//go:build integration

package integration

import (
	"bytes"
	"context"
	"testing"
	"time"

	"rai/server/internal/syncing"
)

func TestDeviceRunningDynamicsDurableRetryAnalysisAndDelete(t *testing.T) {
	f := setup(t)
	workoutStream(t, f)
	w := workoutAt(time.Now().UTC().Add(-2*time.Hour).Truncate(time.Second), 3600)
	w.Workout.TotalDistance = &syncing.Quantity{Value: 10000, Unit: "m"}
	w.Metadata = map[string]any{
		"HKTimeZone": "Asia/Shanghai", "HKIndoorWorkout": false,
		"HKAverageMETs":        map[string]any{"value": 8, "unit": "kcal/(kg*hr)"},
		"HKElevationAscended":  map[string]any{"value": 100, "unit": "m"},
		"HKWeatherHumidity":    map[string]any{"value": 0.6, "unit": "1"},
		"HKWeatherTemperature": map[string]any{"value": 20, "unit": "degC"},
	}
	w.Workout.Statistics = []syncing.WorkoutStatistic{{
		Type: "HKQuantityTypeIdentifierRunningGroundContactTime", StartUs: w.StartUs, EndUs: w.EndUs,
		Average: &syncing.Quantity{Value: 250, Unit: "ms"}, Minimum: &syncing.Quantity{Value: 200, Unit: "ms"}, Maximum: &syncing.Quantity{Value: 300, Unit: "ms"},
	}, {
		Type: "HKQuantityTypeIdentifierRunningStrideLength", StartUs: w.StartUs, EndUs: w.EndUs,
		Average: &syncing.Quantity{Value: 1.2, Unit: "m"}, Minimum: &syncing.Quantity{Value: 1, Unit: "m"}, Maximum: &syncing.Quantity{Value: 1.5, Unit: "m"},
	}, {
		Type: "HKQuantityTypeIdentifierRunningVerticalOscillation", StartUs: w.StartUs, EndUs: w.EndUs,
		Average: &syncing.Quantity{Value: 0.08, Unit: "m"}, Minimum: &syncing.Quantity{Value: 0.06, Unit: "m"}, Maximum: &syncing.Quantity{Value: 0.1, Unit: "m"},
	}}
	e, receipt, original := f.commit(t, []syncing.Event{w}, 1, nil)
	if receipt.Counts.Inserted != 1 || count(t, f, "workout_statistics") != 3 {
		t.Fatal("workout statistic was not committed")
	}
	ctx := context.Background()
	retry, err := f.s.Commit(ctx, f.p, e, []syncing.Event{w})
	if err != nil || !bytes.Equal(retry, original) {
		t.Fatal("retry changed original receipt", err)
	}
	runOne(t, f, "analysis.workout")
	current, err := f.s.CurrentWorkoutAnalysis(ctx, f.p, w.UUID)
	if err != nil || publishedMetric(current, "distance") == nil || *publishedMetric(current, "distance") != 10000 || publishedMetric(current, "workout_load") != nil {
		t.Fatal("additional raw statistic broke existing features or fabricated load", err)
	}
	var payload []byte
	if err := f.s.Pool.QueryRow(ctx, `SELECT payload FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2`, f.p.DatasetID, w.UUID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var stored syncing.Event
	if err := syncing.DecodeJSON(payload, &stored); err != nil {
		t.Fatal(err)
	}
	want, _ := w.Canonical()
	got, _ := stored.Canonical()
	if !bytes.Equal(want, got) {
		t.Fatal("analysis changed source statistic")
	}
	del, _, _ := f.commit(t, []syncing.Event{workoutDelete(w.UUID)}, 2, &e.BatchID)
	_, replay, _ := f.commit(t, []syncing.Event{w}, 3, &del.BatchID)
	if replay.Counts.Suppressed != 1 || count(t, f, "workouts") != 0 || count(t, f, "workout_statistics") != 0 {
		t.Fatal("old workout replay revived a deleted statistic")
	}
}
