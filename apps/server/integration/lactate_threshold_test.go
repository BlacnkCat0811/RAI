//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	am "rai/server/internal/athletemodel"
	"rai/server/internal/httpapi"
	lt "rai/server/internal/lactatethreshold"
	"rai/server/internal/modelinput"
	"rai/server/internal/modelmanifest"
	rf "rai/server/internal/runfitness"
	"rai/server/internal/syncing"
)

// Synthetic data only: the day release publishes the lactate threshold with
// the running-fitness state of the same generation, migration 64 admits race
// and test labels with a main segment, and the schema4 product serves the
// threshold inside runningFitness.
func TestLactateThresholdPublishedWithRunningFitness(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	trial, labelled := "0d7cbd0c-5a3e-4b6f-9a59-0c2f1f6c8a11", "4c1f7d43-3f0b-4f5e-8b8a-3d6a3c1e9b22"
	effort := func(today time.Time, workout string, payload modelinput.Payload) error {
		payload["workoutID"], payload["userConfirmed"] = workout, true
		_, err := f.s.CommitModelInput(ctx, f.p, modelinput.Request{ID: syncing.NewID(), Kind: "workout_effort", SubjectKey: "athlete",
			EffectiveFrom: today.AddDate(0, 0, -1).Format(time.RFC3339), Provenance: "synthetic integration effort", Payload: payload})
		return err
	}
	today, read := runStackDay(t, f, []stackDayRun{{id: trial, day: -3, version: "11.0", hr: steadyHR}, {id: labelled, day: -1, version: "11.0", hr: steadyHR}},
		stackDayOptions{payloads: map[string]func(*fixture, string, time.Time) []byte{
			// 30 minutes at 3.0 m/s and 150 bpm: HR and speed windows exist.
			trial: func(f *fixture, id string, start time.Time) []byte {
				return paceRunPayload(t, f, id, start, true, false)
			},
		}, seed: func(today time.Time) {
			// The database used to admit only the legacy maximal_supported mode.
			if err := effort(today, labelled, modelinput.Payload{"mode": "threshold_test"}); err != nil {
				t.Fatalf("threshold_test label rejected: %v", err)
			}
			if err := effort(today, trial, modelinput.Payload{"mode": "maximal_time_trial",
				"segment": map[string]any{"startOffsetSeconds": 0.0, "durationSeconds": 1800.0, "protocol": modelinput.ProtocolTT30}}); err != nil {
				t.Fatalf("time trial with main segment rejected: %v", err)
			}
			if err := effort(today, trial, modelinput.Payload{"mode": "maximal_time_trial"}); err == nil {
				t.Fatal("time trial without main segment accepted")
			}
		}})
	var fitness rf.State
	read(rf.ArtifactKey, &fitness)
	var raw []byte
	var generation int64
	scope := modelmanifest.DayScope(modelmanifest.StackID(), today.Format(time.DateOnly))
	if err := f.s.Pool.QueryRow(ctx, `SELECT a.payload,p.generation FROM analysis_publications p
 JOIN analysis_artifacts a ON a.dataset_id=p.dataset_id AND a.run_id=p.published_run_id AND a.artifact_key=$3
 WHERE p.dataset_id=$1 AND p.scope_key=$2 AND p.metric_family=$4`, f.p.DatasetID, scope, lt.ArtifactKey, lt.Family).Scan(&raw, &generation); err != nil {
		t.Fatalf("lactate threshold not published: %v", err)
	}
	var threshold lt.Result
	if err := json.Unmarshal(raw, &threshold); err != nil {
		t.Fatal(err)
	}
	if threshold.Schema != lt.Schema || threshold.DatasetID != f.p.DatasetID || threshold.Generation != fitness.Generation || threshold.Generation != generation ||
		threshold.ConfigHash != lt.ConfigHash() || threshold.AnchorHash != fitness.InputHash || threshold.Definition != lt.DefinitionWorking {
		t.Fatalf("threshold identity: %+v", threshold)
	}
	// Method C: the last 20 minutes of the confirmed time trial, one day of
	// evidence, so a preliminary field estimate (definition field_tt30_last20).
	h := threshold.HeartRate
	if h.BPM == nil || *h.BPM < 149 || *h.BPM > 151 || h.Definition != lt.DefinitionFieldTT || h.Method != "field_time_trial" || h.Basis != "preliminary" || h.IndependentDays != 1 {
		t.Fatalf("time-trial HR: %+v %+v", h, threshold.Diagnostics)
	}
	// The marked 30 minute main segment is the only performance evidence; the
	// whole-record threshold test is not one.
	if fitness.Support.Races != 1 {
		t.Fatalf("performance evidence count %d, want only the time-trial main segment", fitness.Support.Races)
	}
	// The test label without a main segment never becomes a performance.
	for _, s := range fitness.Singles {
		if s.WorkoutID == labelled && s.Race != nil {
			t.Fatal("whole-record threshold test calibrated the curve")
		}
	}

	hash, _ := modelmanifest.CurrentHash()
	setModelChannel(t, f, "active", modelmanifest.StackID(), hash)
	api := httpapi.New(f.s, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	req := httptest.NewRequest("GET", "/v2/analysis/athlete?schemaVersion=rai.athlete_model/4", strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
	res := httptest.NewRecorder()
	api.ServeHTTP(res, req)
	var s am.AthleteModelSnapshot
	if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &s) != nil || s.RunningFitness == nil || s.RunningFitness.Value == nil {
		t.Fatalf("athlete %d %s", res.Code, res.Body.String())
	}
	v := s.RunningFitness.Value.LactateThreshold
	if v == nil || v.SchemaVersion != lt.Schema || v.Status != threshold.Status || v.HeartRate.BPM == nil || *v.HeartRate.BPM != *h.BPM || v.TrainingUse.HeartRateEligible {
		t.Fatalf("product threshold: %+v", v)
	}
	// Older Apps never see the new field.
	req = httptest.NewRequest("GET", "/v2/analysis/athlete?schemaVersion=rai.athlete_model/3", strings.NewReader(""))
	req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
	res = httptest.NewRecorder()
	api.ServeHTTP(res, req)
	if res.Code != 200 || strings.Contains(res.Body.String(), "lactateThreshold") {
		t.Fatalf("schema3 leaked the threshold: %d", res.Code)
	}
}
