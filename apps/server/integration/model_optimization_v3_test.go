//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	am "rai/server/internal/athletemodel"
	"rai/server/internal/httpapi"
	"rai/server/internal/modelmanifest"
	sr "rai/server/internal/sessionrecovery"
	tl "rai/server/internal/trainingload"
	"testing"
	"time"
)

func TestModelOptimizationV3HTTPNegotiationAndPublication(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	today, read := runStackDay(t, f, []stackDayRun{{id: "f62f735b-fb16-43f9-b415-7a7041bca032", day: -1, version: "11.0", hr: steadyHR}}, stackDayOptions{})
	var session sr.Result
	var focus tl.FocusResult
	read("session_recovery", &session)
	read("load_focus", &focus)
	if session.Schema != sr.Schema || focus.Schema != tl.FocusSchema || len(focus.Windows) != 2 || len(focus.Workouts) != 1 {
		t.Fatal("new calculations were not published")
	}
	preview, _, e := f.s.ReadAthleteModelCandidateSnapshot(ctx, f.p.DatasetID, time.Now())
	if e != nil || preview.Snapshot.SchemaVersion != am.Schema3 || preview.Snapshot.SessionRecovery == nil || preview.Snapshot.SessionRecovery.Envelope.TechnicalError != nil || preview.Snapshot.LoadFocus.Envelope.TechnicalError != nil || preview.Snapshot.PhysiologicalRecovery.Envelope.TechnicalError != nil {
		t.Fatal("new artifact validation", e)
	}
	if _, e = f.s.ReadAthleteProductV3(ctx, f.p, time.Now()); !errors.Is(e, am.ErrNotActivated) {
		t.Fatal("candidate escaped into product", e)
	}
	hash, _ := modelmanifest.CurrentHash()
	setModelChannel(t, f, "active", modelmanifest.StackID(), hash)
	api := httpapi.New(f.s, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	for _, tc := range []struct {
		query  string
		code   int
		schema string
	}{{"", 200, am.Schema2}, {"?schemaVersion=rai.athlete_model/3", 200, am.Schema3}, {"?schemaVersion=rai.athlete_model/99", 422, ""}, {"?schemaVersion=rai.athlete_model/3&schemaVersion=rai.athlete_model/3", 422, ""}} {
		req := httptest.NewRequest("GET", "/v2/analysis/athlete"+tc.query, nil)
		req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
		res := httptest.NewRecorder()
		api.ServeHTTP(res, req)
		if res.Code != tc.code {
			t.Fatalf("schema negotiation status=%d want=%d", res.Code, tc.code)
		}
		if tc.code != 200 {
			continue
		}
		var s am.AthleteModelSnapshot
		if json.Unmarshal(res.Body.Bytes(), &s) != nil || s.SchemaVersion != tc.schema {
			t.Fatal("schema mismatch")
		}
		if tc.schema == am.Schema2 && s.TrainingReadiness.Envelope.ModelStatus != "model_update_required" {
			t.Fatal("new model disguised as old complete")
		}
		if tc.schema == am.Schema3 && (s.SessionRecovery == nil || s.LoadFocus == nil || s.LoadFocus.Value == nil || s.RunningAbility.Value.Progress == nil) {
			t.Fatal("new fields missing")
		}
	}
	// A dirty generation hides all day products together, including new slots.
	if _, e = f.s.Pool.Exec(ctx, `SELECT rai_schedule_model_day($1,$2,$3::date,clock_timestamp())`, f.p.DatasetID, modelmanifest.StackID(), today.Format(time.DateOnly)); e != nil {
		t.Fatal(e)
	}
	s, e := f.s.ReadAthleteProductV3(ctx, f.p, time.Now())
	if e != nil || s.SessionRecovery.Value != nil || s.LoadFocus.Value != nil || s.TrainingReadiness.Value != nil {
		t.Fatal("mixed generation survived invalidation", e)
	}
}
