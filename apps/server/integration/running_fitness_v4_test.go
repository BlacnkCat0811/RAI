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
	rf "rai/server/internal/runfitness"
	sr "rai/server/internal/sessionrecovery"
	tl "rai/server/internal/trainingload"
	"strings"
	"testing"
	"time"
)

func TestRunningFitnessV4HTTPNegotiationAndPublication(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	today, read := runStackDay(t, f, []stackDayRun{{id: "f62f735b-fb16-43f9-b415-7a7041bca032", day: -1, version: "11.0", hr: steadyHR}}, stackDayOptions{})
	var session sr.Result
	var focus tl.FocusResult
	var fitness rf.State
	read("session_recovery", &session)
	read("load_focus", &focus)
	read(rf.ArtifactKey, &fitness)
	if session.Schema != sr.Schema || focus.Schema != tl.FocusSchema || len(focus.Windows) != 2 || len(focus.Workouts) != 1 {
		t.Fatal("new calculations were not published")
	}
	if fitness.Schema != rf.Schema || fitness.DatasetID != f.p.DatasetID || fitness.ConfigHash != rf.ConfigHash() || len(fitness.Zones.Zones) != 6 {
		t.Fatalf("running fitness not published: %+v", fitness.Status)
	}
	preview, _, e := f.s.ReadAthleteModelCandidateSnapshot(ctx, f.p.DatasetID, time.Now())
	if e != nil || preview.Snapshot.SchemaVersion != am.Schema4 || preview.Snapshot.SessionRecovery == nil || preview.Snapshot.SessionRecovery.Envelope.TechnicalError != nil ||
		preview.Snapshot.LoadFocus.Envelope.TechnicalError != nil || preview.Snapshot.PhysiologicalRecovery.Envelope.TechnicalError != nil ||
		preview.Snapshot.RunningFitness == nil || preview.Snapshot.RunningFitness.Envelope.TechnicalError != nil {
		t.Fatal("new artifact validation", e)
	}
	if _, e = f.s.ReadAthleteProductV4(ctx, f.p, time.Now()); !errors.Is(e, am.ErrNotActivated) {
		t.Fatal("candidate escaped into product", e)
	}
	hash, _ := modelmanifest.CurrentHash()
	setModelChannel(t, f, "active", modelmanifest.StackID(), hash)
	api := httpapi.New(f.s, slog.New(slog.NewJSONHandler(io.Discard, nil)))
	call := func(method, path, key, body string) *httptest.ResponseRecorder {
		var req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		if method == "POST" {
			req.Header.Set("Content-Type", "application/json")
		}
		res := httptest.NewRecorder()
		api.ServeHTTP(res, req)
		return res
	}
	for _, tc := range []struct {
		query  string
		code   int
		schema string
	}{{"", 200, am.Schema2}, {"?schemaVersion=rai.athlete_model/2", 200, am.Schema2}, {"?schemaVersion=rai.athlete_model/3", 200, am.Schema3}, {"?schemaVersion=rai.athlete_model/4", 200, am.Schema4}, {"?schemaVersion=rai.athlete_model/99", 422, ""}, {"?schemaVersion=rai.athlete_model/4&schemaVersion=rai.athlete_model/4", 422, ""}} {
		res := call("GET", "/v2/analysis/athlete"+tc.query, "", "")
		if res.Code != tc.code {
			t.Fatalf("athlete query %q status=%d want=%d", tc.query, res.Code, tc.code)
		}
		if tc.code != 200 {
			continue
		}
		var s am.AthleteModelSnapshot
		if json.Unmarshal(res.Body.Bytes(), &s) != nil || s.SchemaVersion != tc.schema {
			t.Fatal("schema mismatch")
		}
		// Older Apps get their own schema projected from today's schema4
		// result: the training load they know is still a real value.
		if tc.schema != am.Schema4 && (s.RunningFitness != nil || s.TrainingLoad.Envelope.ModelStatus == "model_update_required" ||
			(tc.schema == am.Schema3) != (s.SessionRecovery != nil)) {
			t.Fatalf("older App projection %s", tc.schema)
		}
		if tc.schema == am.Schema4 && (s.SessionRecovery == nil || s.LoadFocus == nil || s.LoadFocus.Value == nil || s.RunningFitness == nil || s.RunningFitness.Value == nil ||
			s.RunningAbility.Value != nil || s.RunningAbility.Envelope.ModelStatus != "model_update_required") {
			t.Fatal("schema4 fields missing or retired ability served")
		}
	}
	if res := call("GET", "/v2/analysis/running-fitness/current", "", ""); res.Code != 200 || !strings.Contains(res.Body.String(), `"runningFitness"`) {
		t.Fatalf("current %d %s", res.Code, res.Body.String())
	}
	day := today.Format(time.DateOnly)
	res := call("GET", "/v2/analysis/running-fitness/history?from="+day+"&to="+day, "", "")
	var page am.RunningFitnessHistory
	if res.Code != 200 || json.Unmarshal(res.Body.Bytes(), &page) != nil || len(page.Days) != 1 || page.View != "revised" {
		t.Fatalf("history %d %s", res.Code, res.Body.String())
	}
	if res := call("GET", "/v2/analysis/running-fitness/history?view=as_known", "", ""); res.Code != 409 || !strings.Contains(res.Body.String(), "historical_input_unavailable") {
		t.Fatalf("as_known must be unavailable: %d", res.Code)
	}
	published := call("GET", "/v2/analysis/running-fitness/history?view=published&from="+day+"&to="+day, "", "")
	var pub am.RunningFitnessHistory
	if published.Code != 200 || json.Unmarshal(published.Body.Bytes(), &pub) != nil || pub.View != "published" || len(pub.Days) != 1 || pub.Days[0].PublicationID != page.Days[0].PublicationID {
		t.Fatalf("published view %d %s", published.Code, published.Body.String())
	}
	if res := call("GET", "/v2/analysis/running-fitness/evidence?date="+day+"&dimension=endurance", "", ""); res.Code != 200 {
		t.Fatalf("evidence %d", res.Code)
	}
	body := `{"from":"` + today.AddDate(0, 0, -3).Format(time.DateOnly) + `","to":"` + day + `","view":"revised","referencePolicy":"current_effective","algorithmVersion":"v1"}`
	first := call("POST", "/v2/analysis/running-fitness/reanalyses", "synthetic-key-1", body)
	var op am.ReanalysisOperation
	if first.Code != 202 || json.Unmarshal(first.Body.Bytes(), &op) != nil || op.ID == "" || op.Status == "completed" && op.TotalDays > 0 && op.CompletedDays < op.TotalDays {
		t.Fatalf("reanalysis %d %s", first.Code, first.Body.String())
	}
	again := call("POST", "/v2/analysis/running-fitness/reanalyses", "synthetic-key-1", body)
	var op2 am.ReanalysisOperation
	if again.Code != 202 || json.Unmarshal(again.Body.Bytes(), &op2) != nil || op2.ID != op.ID {
		t.Fatal("idempotent replay returned another operation")
	}
	if res := call("POST", "/v2/analysis/running-fitness/reanalyses", "synthetic-key-1", strings.Replace(body, `"v1"`, `"v1" `, 1)+" "); res.Code != 202 {
		// Whitespace-only differences canonicalize to the same request.
		t.Fatalf("canonical replay %d", res.Code)
	}
	other := `{"workoutIDs":["f62f735b-fb16-43f9-b415-7a7041bca032"],"view":"revised","referencePolicy":"current_effective","algorithmVersion":"v1"}`
	if res := call("POST", "/v2/analysis/running-fitness/reanalyses", "synthetic-key-1", other); res.Code != 409 {
		t.Fatalf("idempotency conflict %d", res.Code)
	}
	if res := call("POST", "/v2/analysis/running-fitness/reanalyses", "synthetic-key-2", strings.Replace(body, `"v1"`, `"v99"`, 1)); res.Code != 422 {
		t.Fatalf("arbitrary algorithm accepted %d", res.Code)
	}
	if res := call("GET", "/v2/analysis/running-fitness/reanalyses/"+op.ID, "", ""); res.Code != 200 {
		t.Fatalf("operation status %d", res.Code)
	}
	processStackJobs(t, f)
	done, e := f.s.ReadRunFitnessReanalysis(ctx, f.p, op.ID)
	if e != nil || done.Status != "completed" || done.CompletedDays != done.TotalDays {
		t.Fatalf("reanalysis did not complete: %+v %v", done, e)
	}
	// Revised history now shows the recomputed release of the day.
	after, e := f.s.ReadRunningFitnessHistory(ctx, f.p, day, day, "revised", "", 30)
	if e != nil || len(after.Days) != 1 || after.Days[0].PublicationID == page.Days[0].PublicationID {
		t.Fatalf("revised history did not move to the recomputed day: %v", e)
	}
	// Only today's (post-run) state is published here. The detail never borrows
	// a state the run itself shaped, so pace zones stay unavailable instead of
	// classifying the run with its own boundaries.
	detail, e := f.s.WorkoutDetail(ctx, f.p, "f62f735b-fb16-43f9-b415-7a7041bca032")
	if e != nil || detail.ZoneSummary == nil {
		t.Fatalf("detail zones: %v", e)
	}
	if z := detail.ZoneSummary; z.PaceZoneStatus != "threshold_pace_unavailable" || z.PaceZoneProfileDate != "" || len(z.PaceExposure) != 0 || len(z.PaceZones) != 0 {
		b, _ := json.Marshal(z)
		t.Fatalf("post-run zones leaked into the detail: %s", b)
	}
	// HR-only input retains a visible but unavailable review, never fabricates speed.
	if detail.PaceAnalysis == nil || detail.PaceAnalysis.DefaultView != "summary" {
		t.Fatal("HR-only run fabricated pace")
	}
	// A dirty generation hides all day products together, including new slots.
	if _, e = f.s.Pool.Exec(ctx, `SELECT rai_schedule_model_day($1,$2,$3::date,clock_timestamp())`, f.p.DatasetID, modelmanifest.StackID(), today.Format(time.DateOnly)); e != nil {
		t.Fatal(e)
	}
	s, e := f.s.ReadAthleteProductV4(ctx, f.p, time.Now())
	if e != nil || s.SessionRecovery.Value != nil || s.LoadFocus.Value != nil || s.TrainingReadiness.Value != nil || s.RunningFitness.Value != nil {
		t.Fatal("mixed generation survived invalidation", e)
	}
}

func TestPaceReviewAfterRestNeedsNoHistoricalPublication(t *testing.T) {
	f := setup(t)
	older := "f62f735b-fb16-43f9-b415-7a7041bca031"
	target := "f62f735b-fb16-43f9-b415-7a7041bca032"
	runStackDay(t, f, []stackDayRun{{id: older, day: -10, version: "11.0", hr: steadyHR}, {id: target, day: -1, version: "11.0", hr: steadyHR}}, stackDayOptions{payloads: map[string]func(*fixture, string, time.Time) []byte{older: stackPacePayload(t), target: stackPacePayload(t)}})
	hash, _ := modelmanifest.CurrentHash()
	setModelChannel(t, f, "active", modelmanifest.StackID(), hash)
	d, err := f.s.WorkoutDetail(context.Background(), f.p, target)
	if err != nil {
		t.Fatal(err)
	}
	a := d.PaceAnalysis
	if a == nil || a.Reference == nil || a.Reference.Source != "pre_run" || a.DefaultView != "personal" {
		t.Fatalf("rest gap blocked review: personal=%s codes=%v absolute=%s", a.Personal.PaceZoneStatus, a.Personal.ExplanationCodes, a.Absolute.PaceZoneStatus)
	}
	if a.Reference.IncludesTargetWorkout || a.Reference.LatestEvidenceAtUs == nil || *a.Reference.LatestEvidenceAtUs >= a.Reference.EvidenceCutoffAtUs {
		t.Fatal("future evidence leaked")
	}
}

func stackPacePayload(t *testing.T) func(*fixture, string, time.Time) []byte {
	return func(f *fixture, id string, start time.Time) []byte {
		var body map[string]any
		if err := json.Unmarshal(stackRun(t, f, id, start, "11.0", steadyHR), &body); err != nil {
			t.Fatal(err)
		}
		streams := body["streams"].([]any)
		streams = append(streams, map[string]any{"type": "HKQuantityTypeIdentifierRunningSpeed", "unit": "m/s", "status": "complete", "samples": [][]any{{int64(0), int64(1800e6), 3.2}}})
		body["streams"] = streams
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
}

func TestPaceReviewFirstRunUsesExplicitPostRunEstimate(t *testing.T) {
	f := setup(t)
	id := "f62f735b-fb16-43f9-b415-7a7041bca032"
	runStackDay(t, f, []stackDayRun{{id: id, day: -1, version: "11.0", hr: steadyHR}}, stackDayOptions{payloads: map[string]func(*fixture, string, time.Time) []byte{id: stackPacePayload(t)}})
	hash, _ := modelmanifest.CurrentHash()
	setModelChannel(t, f, "active", modelmanifest.StackID(), hash)
	d, err := f.s.WorkoutDetail(context.Background(), f.p, id)
	if err != nil {
		t.Fatal(err)
	}
	a := d.PaceAnalysis
	if a == nil || a.Reference == nil || a.Reference.Source != "post_run_estimate" || !a.Reference.IncludesTargetWorkout || a.DefaultView != "personal" {
		t.Fatal("first-run estimate unavailable")
	}
	encoded, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"paceExposure":null`) {
		t.Fatal("required client array encoded as null")
	}
}

func TestPaceReviewSpeedOnlyRetainsAbsoluteDistribution(t *testing.T) {
	f := setup(t)
	id := "f62f735b-fb16-43f9-b415-7a7041bca032"
	speedOnly := func(f *fixture, id string, start time.Time) []byte {
		var body map[string]any
		if err := json.Unmarshal(stackPacePayload(t)(f, id, start), &body); err != nil {
			t.Fatal(err)
		}
		body["streams"] = body["streams"].([]any)[1:]
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	runStackDay(t, f, []stackDayRun{{id: id, day: -1, version: "11.0", hr: steadyHR}}, stackDayOptions{payloads: map[string]func(*fixture, string, time.Time) []byte{id: speedOnly}})
	hash, _ := modelmanifest.CurrentHash()
	setModelChannel(t, f, "active", modelmanifest.StackID(), hash)
	d, err := f.s.WorkoutDetail(context.Background(), f.p, id)
	if err != nil {
		t.Fatal(err)
	}
	a := d.PaceAnalysis
	if a == nil || a.Reference != nil || a.DefaultView != "absolute" || len(a.Absolute.PaceExposure) != 5 {
		t.Fatal("speed-only run lost descriptive distribution")
	}
}
