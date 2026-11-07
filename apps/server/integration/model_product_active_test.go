//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	am "rai/server/internal/athletemodel"
	"rai/server/internal/httpapi"
	"rai/server/internal/syncing"
	tl "rai/server/internal/trainingload"
)

// TestActiveSchema2ProductWithoutArtifacts proves the formal product route is
// reachable only through an explicitly activated non-legacy stack. Missing
// model slots stay typed nulls; the fixture never fabricates a score.
func TestActiveSchema2ProductWithoutArtifacts(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	tx, err := f.s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `SET LOCAL rai.model_assignment='on'`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE datasets SET synthetic_test=true WHERE id=$1`, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO analysis_model_channels(dataset_id,channel,stack_id,config_sha256,assignment_version)
VALUES($1,'active','synthetic-v2',decode(repeat('a',64),'hex'),1)
ON CONFLICT(dataset_id,channel) DO UPDATE SET stack_id=EXCLUDED.stack_id,config_sha256=EXCLUDED.config_sha256`, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	snap, err := f.s.ReadAthleteProduct(ctx, f.p, at)
	if err != nil {
		t.Fatal(err)
	}
	if snap.SchemaVersion != am.Schema2 || snap.Assignment.Channel != "active" || snap.Assignment.StackID != "synthetic-v2" {
		t.Fatalf("unexpected projection identity: %+v", snap.Assignment)
	}
	if snap.ResponseState != "unavailable_response" {
		t.Fatalf("empty projection must not claim complete response: %q", snap.ResponseState)
	}
	if snap.RAIVO2Max == nil || snap.RAIVO2Max.Envelope.MappingVersion != am.MappingVersion2 {
		t.Fatal("rai VO2 slot missing from schema2 response")
	}
	for _, e := range []am.Envelope{snap.TrainingLoad.Envelope, snap.RunningAbility.Envelope, snap.ExternalVO2Max.Envelope, snap.PhysiologicalRecovery.Envelope, snap.TrainingReadiness.Envelope, snap.RAIVO2Max.Envelope} {
		if e.State == am.Available {
			t.Fatalf("missing artifact projected as available: %s", e.Metric)
		}
	}
	handler := httpapi.New(f.s, analysisLog)
	req := httptest.NewRequest(http.MethodGet, "/v2/analysis/athlete", nil)
	req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("active product status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err = json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["schemaVersion"] != am.Schema2 || body["raiVO2Max"] == nil || body["candidateOnly"] != nil {
		t.Fatalf("unexpected active response: %s", rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/v2/analysis/athlete/history?metric=trainingReadiness&limit=10", nil)
	req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("active history status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err = json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["metric"] != "trainingReadiness" || len(body["points"].([]any)) != 0 {
		t.Fatalf("unexpected empty history: %s", rec.Body.String())
	}
}

func TestActiveSchema2ProjectsV2LoadArtifact(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	tx, err := f.s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `SET LOCAL rai.model_assignment='on'`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE datasets SET synthetic_test=true WHERE id=$1`, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO analysis_model_channels(dataset_id,channel,stack_id,config_sha256,assignment_version)
VALUES($1,'active','synthetic-v2',decode(repeat('a',64),'hex'),1)
ON CONFLICT(dataset_id,channel) DO UPDATE SET stack_id=EXCLUDED.stack_id,config_sha256=EXCLUDED.config_sha256`, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	day := time.Now().In(loc).Format("2006-01-02")
	acute, chronic, recorded := 12.0, 20.0, 12.0
	state := tl.StateResult{Schema: tl.StateSchemaV2, DatasetID: f.p.DatasetID, Generation: 1, Model: tl.StateAlgorithmV2, Version: tl.VersionV2, ConfigHash: tl.ConfigHashV2(), Scope: tl.ScopeStateV2For("synthetic-v2"), Unit: tl.Unit, TrendUnit: tl.Unit + "/day", Days: []tl.DailyState{{Day: day, RecordedLoad: &recorded, KnownRecordedLoad: recorded, RecordedWorkouts: 1, Quality: "complete", Acute: tl.KernelValue{Value: &acute}, Chronic: tl.KernelValue{Value: &chronic}, Balance: ptrFloat(acute - chronic)}}}
	payload, _ := json.Marshal(state)
	h := tl.Hash(state)
	jobID, runID := syncing.NewID(), syncing.NewID()
	scope := tl.ScopeStateV2For("synthetic-v2")
	if _, err = tx.Exec(ctx, `INSERT INTO jobs(id,dataset_id,kind,scope_key,target_generation,state,lease_until,fencing_token)
VALUES($1,$2,'analysis.load_state',$3,1,'running',clock_timestamp()+interval '1 hour',1)`, jobID, f.p.DatasetID, scope); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO analysis_runs(dataset_id,id,job_id,fencing_token,scope_key,target_generation,algorithm_name,algorithm_version,input_version,input_hash,input_from,input_through,lineage,status)
VALUES($1,$2,$3,1,$4,1,'rai.load_state.exp_finite','v2','test',decode(repeat('01',32),'hex'),clock_timestamp()-interval '1 day',clock_timestamp(),'{}','RUNNING')`, f.p.DatasetID, runID, jobID, scope); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO analysis_artifacts(dataset_id,run_id,artifact_key,schema_version,content_sha256,payload)
VALUES($1,$2,'load_state','rai.load_state/2',decode($3,'hex'),$4)`, f.p.DatasetID, runID, h, payload); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE analysis_runs SET status='SUCCEEDED',finished_at=clock_timestamp() WHERE dataset_id=$1 AND id=$2`, f.p.DatasetID, runID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO analysis_publications(dataset_id,scope_key,metric_family,day,published_run_id,generation)
VALUES($1,$2,'load_state',$3::date,$4,1)`, f.p.DatasetID, scope, day, runID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO dirty_scopes(dataset_id,scope_key,generation,dirty_from,dirty_through,clean_generation)
VALUES($1,$2,1,clock_timestamp()-interval '2 days',clock_timestamp(),1)`, f.p.DatasetID, scope); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	snap, err := f.s.ReadAthleteProduct(ctx, f.p, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if snap.TrainingLoad.Envelope.State != am.Available || snap.TrainingLoad.Value == nil || snap.TrainingLoad.Value.Acute == nil {
		t.Fatalf("v2 load artifact not projected: %+v", snap.TrainingLoad)
	}
	if snap.TrainingLoad.Envelope.ModelVersion != tl.AlgorithmV2+"/"+tl.VersionV2 || snap.TrainingLoad.Value.StateModelVersion != tl.StateAlgorithmV2+"/"+tl.VersionV2 {
		t.Fatalf("v1 label leaked into schema2: %+v %+v", snap.TrainingLoad.Envelope, snap.TrainingLoad.Value)
	}
	if snap.ResponseState != "partial_response" {
		t.Fatalf("one of six available must be partial: %s", snap.ResponseState)
	}
	page, err := f.s.ReadAthleteHistory(ctx, f.p, "trainingLoad", day, day, 10, "", "revised")
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Points) != 1 || page.Points[0].Date != day {
		t.Fatalf("v2 load history not readable: %+v", page)
	}
}

func ptrFloat(v float64) *float64 { return &v }
