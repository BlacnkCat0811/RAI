//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"rai/server/internal/httpapi"
	"testing"
	"time"

	"rai/server/internal/modelmanifest"
	"rai/server/internal/workoutdetail"
)

func paceRunPayload(t *testing.T, f *fixture, id string, start time.Time, heart bool, instant bool) []byte {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(stackRun(t, f, id, start, "11.0", steadyHR), &body); err != nil {
		t.Fatal(err)
	}
	streams := []any{}
	if heart {
		streams = body["streams"].([]any)
	}
	points := []any{}
	for i := 0; i <= 1800; i += 5 {
		end := i + 5
		if instant {
			end = i
		} else if end > 1800 {
			break
		}
		points = append(points, []any{int64(i) * 1e6, int64(end) * 1e6, 3.0})
	}
	streams = append(streams, map[string]any{"type": "HKQuantityTypeIdentifierRunningSpeed", "unit": "m/s", "status": "complete", "samples": points})
	body["streams"] = streams
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestPaceZonesV2ReadsInstantSpeedBeforeWorkoutPublication(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	oldID := "d130a1b2-89b3-4ae3-8022-74a8fabd0101"
	today, _ := runStackDay(t, f, []stackDayRun{{id: oldID, day: -3, version: "11.0", hr: steadyHR}}, stackDayOptions{payloads: map[string]func(*fixture, string, time.Time) []byte{
		oldID: func(f *fixture, id string, start time.Time) []byte {
			return paceRunPayload(t, f, id, start, true, false)
		},
	}})
	previous := today.AddDate(0, 0, -1).Format(time.DateOnly)
	if _, err := f.s.Pool.Exec(ctx, `SELECT rai_schedule_model_day($1,$2,$3::date,clock_timestamp())`, f.p.DatasetID, modelmanifest.StackID(), previous); err != nil {
		t.Fatal(err)
	}
	processStackJobs(t, f)
	if _, err := f.s.Pool.Exec(ctx, `INSERT INTO analysis_model_channels SELECT (jsonb_populate_record(NULL::analysis_model_channels,to_jsonb(c)||'{"channel":"active"}')).* FROM analysis_model_channels c WHERE c.dataset_id=$1 AND c.channel='candidate' ON CONFLICT(dataset_id,channel) DO UPDATE SET stack_id=EXCLUDED.stack_id`, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}
	targetID := "d130a1b2-89b3-4ae3-8022-74a8fabd0102"
	acceptIngest(t, f, f.p, paceRunPayload(t, f, targetID, today.Add(8*time.Hour), false, true))
	for _, out := range processAllIngests(t, f.s) {
		if out.Status != "committed" {
			t.Fatalf("ingest %s", out.Status)
		}
	}
	d, err := f.s.WorkoutDetail(ctx, f.p, targetID)
	if err != nil {
		t.Fatal(err)
	}
	z := d.ZoneSummary
	if d.Workout.AnalysisAvailable || z == nil || z.PaceZoneStatus != workoutdetail.PaceZonesAvailable || z.PaceZoneProfileDate != previous || z.PaceZonePolicyVersion != workoutdetail.PaceZonePolicyV2 || z.PaceReconstructedSeconds == nil || *z.PaceReconstructedSeconds != 1800 || z.PaceObservedSeconds == nil || *z.PaceObservedSeconds != 0 || len(z.HeartRateExposure) != 0 || z.PaceZonePublicationID == "" || len(z.PaceZoneContentHash) != 64 {
		t.Fatalf("independent pace failed: status=%s zones=%+v", d.Workout.AnalysisStatus, z)
	}
	var seconds, distance float64
	for _, e := range z.PaceExposure {
		seconds += e.DurationSeconds
		distance += e.DistanceMeters
	}
	if seconds != 1800 || distance != 5400 || z.PaceUnknownSeconds != 0 {
		t.Fatal(fmt.Sprintf("pace integration totals %v %v", seconds, distance))
	}
	api := httpapi.New(f.s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	request := httptest.NewRequest("GET", "/v2/workouts/"+targetID+"/detail", nil)
	request.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
	response := httptest.NewRecorder()
	api.ServeHTTP(response, request)
	var wire struct {
		ZoneSummary *workoutdetail.ZoneSummary `json:"zoneSummary"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &wire) != nil || wire.ZoneSummary == nil || wire.ZoneSummary.PaceZonePolicyVersion != workoutdetail.PaceZonePolicyV2 || wire.ZoneSummary.PaceReconstructedSeconds == nil || *wire.ZoneSummary.PaceReconstructedSeconds != 1800 {
		t.Fatal("HTTP pace contract failed", response.Code)
	}
	// The later current-day publication does not replace the pre-run profile.
	processStackJobs(t, f)
	again, err := f.s.WorkoutDetail(ctx, f.p, targetID)
	if err != nil || again.ZoneSummary == nil || again.ZoneSummary.PaceZonePublicationID != z.PaceZonePublicationID {
		t.Fatal("run classified by its own day", err)
	}
}
