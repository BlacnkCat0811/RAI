//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"rai/server/internal/admin"
	am "rai/server/internal/athletemodel"
	"rai/server/internal/httpapi"
	"rai/server/internal/modelmanifest"
	"rai/server/internal/syncing"
	vo2 "rai/server/internal/vo2max"
	wf "rai/server/internal/workoutfeatures"
)

// m4Bundle is a qualified synthetic submax run: 35 minutes outdoors at
// 134 m/min with a stable 182 bpm HR. The values are deliberately near the
// HR-ratio ceiling so the eight-corner sensitivity width stays under 30%.
func m4Bundle(t *testing.T, f *fixture, end time.Time, seconds int) syncing.WorkoutBundle {
	t.Helper()
	b := m3Bundle(t, f, end, seconds)
	const hr = 182.0
	speed := 134.0 / 60.0
	b.Workout.Workout.TotalDistance = &syncing.Quantity{Value: speed * float64(seconds), Unit: "m"}
	for i := range b.Workout.Workout.Statistics {
		if b.Workout.Workout.Statistics[i].Type == kind {
			b.Workout.Workout.Statistics[i].Average = &syncing.Quantity{Value: hr, Unit: "count/min"}
		}
	}
	for i := range b.Samples {
		switch b.Samples[i].Type {
		case kind:
			b.Samples[i].Quantity = &syncing.Quantity{Value: hr, Unit: "count/min"}
		case wf.Speed:
			b.Samples[i].Quantity = &syncing.Quantity{Value: speed, Unit: "m/s"}
		}
	}
	for i := range b.Series {
		for j := range b.Series[i].Points {
			b.Series[i].Points[j].Value = hr
		}
	}
	// Route distance must match the recorded distance so the performance and
	// VO2 qualifications do not reject the workout as distance_mismatch.
	for i := range b.Routes {
		for j := range b.Routes[i].Points {
			b.Routes[i].Points[j].Longitude = float64(j) * speed / 6371008.8 * 180 / math.Pi
		}
	}
	if err := b.Validate(); err != nil {
		t.Fatalf("M4 synthetic bundle: %#v", err)
	}
	return b
}

func seedNativeHRReference(t *testing.T, f *fixture, firstStart, maxAt, restAt, known time.Time) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"maxHR": 209.0, "restingHR": 35.0,
		"measuredAt": maxAt.UTC().Format(time.RFC3339), "restingMeasuredAt": restAt.UTC().Format(time.RFC3339),
		"method": "field_max_test", "unit": "bpm",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(context.Background(), `INSERT INTO athlete_model_inputs(
 dataset_id,id,kind,subject_key,effective_from,effective_through,supersedes_id,schema_version,content_sha256,
 payload,provenance,revokes_id,input_schema,created_at)
 VALUES($1,$2,'hr_reference','athlete',$3,NULL,NULL,'rai.hr_reference/2',sha256('synthetic-m4-reference'::bytea),
 $4::jsonb,'synthetic-m4-fixture',NULL,'rai.hr_reference/2',$5)`,
		f.p.DatasetID, syncing.NewID(), firstStart.AddDate(0, 0, -100), payload, known); err != nil {
		t.Fatal(err)
	}
}

func TestNativeRAIVO2PublishesNumericSyntheticValue(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().In(loc)
	anchor := now.AddDate(0, 0, -1)
	latest := time.Date(anchor.Year(), anchor.Month(), anchor.Day(), 7, 0, 0, 0, loc)

	first := latest.AddDate(0, 0, -24)
	// Known-at must be after both measurements and still before the first
	// workout so historical target-day cutoffs can use the reference.
	seedNativeHRReference(t, f, first, first.AddDate(0, 0, -60), first.AddDate(0, 0, -40), first.AddDate(0, 0, -39))
	ends := make([]time.Time, 0, 7)
	for i := 0; i < 7; i++ {
		end := latest.AddDate(0, 0, -4*(6-i)).Add(2100 * time.Second)
		b := m4Bundle(t, f, end, 2100)
		importPhase3(t, f, b)
		// Historical known-at is what makes the six earlier target-day cutoffs
		// eligible without any lookahead. Values are fixed synthetic fixtures.
		if _, err := f.s.Pool.Exec(ctx, `UPDATE workouts SET first_received_at=$3,available_at=$3 WHERE dataset_id=$1 AND workout_uuid=$2`, f.p.DatasetID, b.WorkoutID, end.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		ends = append(ends, end)
	}
	if _, err := f.s.Pool.Exec(ctx, `UPDATE datasets SET synthetic_test=true WHERE id=$1`, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.PrepareModelStack(ctx, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}
	lease, err := f.s.Claim(ctx, []string{modelmanifest.StackJobKind})
	if err != nil {
		t.Fatal(err)
	}
	status, err := f.s.ProcessModelStack(ctx, lease, time.Now())
	if err != nil || status != "SUCCEEDED" {
		t.Fatalf("M4 day stack: %q %v", status, err)
	}

	stack := modelmanifest.StackID()
	day := now.Format(time.DateOnly)
	var payload []byte
	var schema, algorithm, version, family string
	if err := f.s.Pool.QueryRow(ctx, `SELECT a.payload,a.schema_version,r.algorithm_name,r.algorithm_version,p.metric_family
 FROM analysis_publications p
 JOIN analysis_runs r ON r.dataset_id=p.dataset_id AND r.id=p.published_run_id AND r.scope_key=p.scope_key AND r.target_generation=p.generation
 JOIN analysis_artifacts a ON a.dataset_id=r.dataset_id AND a.run_id=r.id AND a.artifact_key='vo2max_submax'
 WHERE p.dataset_id=$1 AND p.scope_key=$2 AND p.day=$3::date`, f.p.DatasetID, modelmanifest.DayScope(stack, day), day).
		Scan(&payload, &schema, &algorithm, &version, &family); err != nil {
		t.Fatal(err)
	}
	var result vo2.Result
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatal(err)
	}
	if schema != vo2.Schema || algorithm != vo2.Model || version != vo2.Version || family != "model_product_v2/vo2max_submax" {
		t.Fatalf("M4 identity schema=%s algorithm=%s version=%s family=%s", schema, algorithm, version, family)
	}
	if result.Status != "established" || result.Value == nil || math.Abs(*result.Value-35.22244897959183) > 1e-6 {
		t.Fatalf("M4 numeric publication: status=%s value=%v reasons=%v", result.Status, result.Value, result.Reasons)
	}
	if result.CandidateOnly || result.Sensitivity.Corners != 8 || result.Sensitivity.Unbounded || result.Sensitivity.Width == nil || *result.Sensitivity.Width > .30 {
		t.Fatalf("M4 sensitivity not publishable: %+v", result.Sensitivity)
	}
	if !result.LOO.SupportOK || result.LOO.Folds != 7 || result.LOO.Range == nil || !result.Experimental || result.Unit != vo2.Unit {
		t.Fatalf("M4 support: %+v experimental=%v unit=%s", result.LOO, result.Experimental, result.Unit)
	}

	// The administrative candidate preview reads the managed day stack and
	// exposes the experimental RAI slot without leaking numeric values in its
	// default summary.
	var inspect bytes.Buffer
	evaluation := time.Now().Format(time.RFC3339Nano)
	if err := admin.ModelStackCommand(ctx, f.s.Pool.Config().ConnString(), []string{"inspect", "--dataset", f.p.DatasetID, "--evaluation-at", evaluation, "--synthetic-test-values"}, &inspect); err != nil {
		t.Fatal(err)
	}
	var candidate am.AthleteModelCandidateSnapshot
	if err := json.Unmarshal(inspect.Bytes(), &candidate); err != nil || !candidate.CandidateOnly || candidate.Snapshot.RAIVO2Max == nil ||
		candidate.Snapshot.RAIVO2Max.Envelope.Metric != "raiVO2Max" || candidate.Snapshot.RAIVO2Max.Value == nil ||
		candidate.Snapshot.RAIVO2Max.Value.Source != "rai_owned_submax" || candidate.Snapshot.RAIVO2Max.Value.Unit != vo2.Unit {
		t.Fatalf("admin managed candidate preview: %s err=%v", inspect.String(), err)
	}
	inspect.Reset()
	if err := admin.ModelStackCommand(ctx, f.s.Pool.Config().ConnString(), []string{"inspect", "--dataset", f.p.DatasetID, "--evaluation-at", evaluation}, &inspect); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(inspect.Bytes(), []byte(`"raiVO2Max"`)) || bytes.Contains(inspect.Bytes(), []byte(`"value"`)) {
		t.Fatalf("admin summary privacy: %s", inspect.String())
	}

	manifest, err := modelmanifest.CurrentHash()
	if err != nil {
		t.Fatal(err)
	}
	m := modelmanifest.Current()
	report := string(bytes.Repeat([]byte("c"), 64))
	gate := am.PromotionGate{Model: m.VO2Max.ModelID, Version: m.VO2Max.Version, ManifestSHA: manifest, ConfigSHA: manifest,
		FeatureSchema: m.Features.Schema, SourcePolicy: m.VO2Max.SourcePolicy, InputRegistry: m.InputRegistry,
		MappingVersion: am.MappingVersion2, ConsumerSchema: am.Schema2, DatasetID: f.p.DatasetID, SyntheticDataset: true,
		EngineeringReport: report, DeviceDataReport: report, ApplicabilityReport: report, UXPrivacyReport: report, RollbackReport: report, Authorization: report,
		Reviewer: "synthetic-m4", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
	if _, err = f.s.PromoteModelStack(ctx, f.p.DatasetID, "synthetic-m4", stack, gate, manifest); err != nil {
		t.Fatal(err)
	}
	snap, err := f.s.ReadAthleteProduct(ctx, f.p, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if snap.RAIVO2Max == nil || snap.RAIVO2Max.Envelope.State != am.Available || snap.RAIVO2Max.Value == nil {
		t.Fatalf("RAI VO2 product slot: %+v", snap.RAIVO2Max)
	}
	if snap.RAIVO2Max.Value.Source != "rai_owned_submax" || !snap.RAIVO2Max.Value.Experimental || snap.RAIVO2Max.Value.Unit != vo2.Unit || snap.RAIVO2Max.Value.EvidenceCount != 7 {
		t.Fatalf("RAI VO2 value identity: %+v", *snap.RAIVO2Max.Value)
	}
	if snap.RAIVO2Max.Envelope.EvidenceSupport != "experimental_candidate" || snap.RAIVO2Max.Envelope.ValidationStatus != "experimental" {
		t.Fatalf("RAI VO2 envelope: %+v", snap.RAIVO2Max.Envelope)
	}
	history, err := f.s.ReadAthleteHistory(ctx, f.p, "raiVO2Max", "", "", 7, "", "revised")
	if err != nil || len(history.Points) != 1 {
		t.Fatalf("RAI VO2 history: %+v %v", history, err)
	}
	point := history.Points[0]
	if point.Value == nil || math.Abs(*point.Value-35.22244897959183) > 1e-6 || point.Unit != vo2.Unit || point.Mode != "experimental" || point.State != "established" {
		t.Fatalf("RAI VO2 history point: %+v", point)
	}
	// The external Apple path remains independent and must not be averaged or
	// relabelled by the RAI-owned value.
	ext := snap.ExternalVO2Max
	if ext.Value == nil || ext.Value.Value != nil || !ext.Value.ExternalEstimate || ext.Envelope.Metric != "externalVO2Max" || ext.Envelope.State != am.Unavailable {
		t.Fatalf("external VO2 cross-contamination: %+v", ext)
	}
	handler := httpapi.New(f.s, analysisLog)
	req := httptest.NewRequest(http.MethodGet, "/v2/analysis/athlete", nil)
	req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("M4 product HTTP=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	metric, ok := body["raiVO2Max"].(map[string]any)
	if !ok {
		t.Fatalf("raiVO2Max missing: %s", rec.Body.String())
	}
	value, ok := metric["value"].(map[string]any)
	if !ok || value["unit"] != vo2.Unit || value["source"] != "rai_owned_submax" {
		t.Fatalf("raiVO2Max value: %v", metric["value"])
	}
}
