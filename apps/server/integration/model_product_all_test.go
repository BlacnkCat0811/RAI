//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	am "rai/server/internal/athletemodel"
	"rai/server/internal/httpapi"
	pf "rai/server/internal/performance"
	rd "rai/server/internal/readiness"
	rc "rai/server/internal/recovery"
	"rai/server/internal/syncing"
	tl "rai/server/internal/trainingload"
	vo2 "rai/server/internal/vo2max"
	ve "rai/server/internal/vo2maxexternal"
)

func activateSyntheticV2(t *testing.T, f *fixture) {
	t.Helper()
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
}

func seedV2Artifact(t *testing.T, f *fixture, scope, key, schema, algorithm, version, family, day string, payload any) string {
	return seedV2ArtifactHash(t, f, scope, key, schema, algorithm, version, family, day, payload, "")
}

func seedV2ArtifactHash(t *testing.T, f *fixture, scope, key, schema, algorithm, version, family, day string, payload any, hashOverride string) string {
	t.Helper()
	ctx := context.Background()
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	h := ""
	switch v := payload.(type) {
	case tl.StateResult:
		h = tl.Hash(v)
	case pf.Result:
		h = pf.Hash(v)
	case vo2.Result:
		h = vo2.Hash(v)
	case ve.Result:
		h = ve.Hash(v)
	case rc.V2Result:
		h = rc.Hash(v)
	case rd.Result:
		h = rd.Hash(v)
	default:
		t.Fatalf("unknown payload %T", payload)
	}
	if hashOverride != "" {
		h = hashOverride
	}
	tx, err := f.s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	jobID, runID := syncing.NewID(), syncing.NewID()
	if _, err = tx.Exec(ctx, `INSERT INTO jobs(id,dataset_id,kind,scope_key,target_generation,state,lease_until,fencing_token) VALUES($1,$2,'analysis.test',$3,1,'running',clock_timestamp()+interval '1 hour',1)`, jobID, f.p.DatasetID, scope+"-job-"+day); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO analysis_runs(dataset_id,id,job_id,fencing_token,scope_key,target_generation,algorithm_name,algorithm_version,input_version,input_hash,input_from,input_through,lineage,status) VALUES($1,$2,$3,1,$4,1,$5,$6,'test',$7,clock_timestamp()-interval '2 days',clock_timestamp(),'{}','RUNNING')`, f.p.DatasetID, runID, jobID, scope, algorithm, version, dayHash(day)); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO analysis_artifacts(dataset_id,run_id,artifact_key,schema_version,content_sha256,payload) VALUES($1,$2,$3,$4,decode($5,'hex'),$6)`, f.p.DatasetID, runID, key, schema, h, b); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE analysis_runs SET status='SUCCEEDED',finished_at=clock_timestamp() WHERE dataset_id=$1 AND id=$2`, f.p.DatasetID, runID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO analysis_publications(dataset_id,scope_key,metric_family,day,published_run_id,generation) VALUES($1,$2,$3,$4::date,$5,1)`, f.p.DatasetID, scope, family, day, runID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO dirty_scopes(dataset_id,scope_key,generation,dirty_from,dirty_through,clean_generation) VALUES($1,$2,1,clock_timestamp()-interval '2 days',clock_timestamp(),1) ON CONFLICT(dataset_id,scope_key) DO UPDATE SET generation=1,clean_generation=1`, f.p.DatasetID, scope); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestActiveSchema2ProjectsAllSixTypedMetrics(t *testing.T) {
	f := setup(t)
	activateSyntheticV2(t, f)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	nowUs := now.UnixMicro()
	day := now.Format("2006-01-02")
	end := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, loc).UnixMicro()

	acute, chronic, recorded := 12.0, 20.0, 12.0
	state := tl.StateResult{Schema: tl.StateSchemaV2, DatasetID: f.p.DatasetID, Generation: 1, Model: tl.StateAlgorithmV2, Version: tl.VersionV2, ConfigHash: tl.ConfigHashV2(), Scope: tl.ScopeStateV2For("synthetic-v2"), Unit: tl.Unit, TrendUnit: tl.Unit + "/day", Days: []tl.DailyState{{Day: day, RecordedLoad: &recorded, KnownRecordedLoad: recorded, RecordedWorkouts: 1, Quality: "complete", Acute: tl.KernelValue{Value: &acute}, Chronic: tl.KernelValue{Value: &chronic}, Balance: ptrFloat(acute - chronic)}}}
	t.Log("seeding load")
	seedV2Artifact(t, f, tl.ScopeStateV2For("synthetic-v2"), "load_state", tl.StateSchemaV2, tl.StateAlgorithmV2, tl.VersionV2, "load_state", day, state)

	ability := pf.Result{Schema: pf.SchemaV2, DatasetID: f.p.DatasetID, Generation: 1, Model: pf.AbilityModel, Version: pf.VersionV2, ConfigHash: pf.ConfigHashV2(), AsOf: nowUs, View: "latest", EvidenceMode: pf.ModeDemonstratedV2, Status: "available", Freshness: "fresh", Stability: "stable", Support: pf.Support{IndependentWorkouts: 6, EffectiveWorkouts: 6, HistorySpan: 30, LatestEvidence: nowUs, QualitySupport: 1}, Ability: &pf.Ability{Score: 50, V60: 3, Gamma: .9, Retention: .8, DisplayScore: 50}}
	t.Log("seeding ability")
	seedV2Artifact(t, f, pf.ScopeFor("synthetic-v2"), "performance_state_v2", pf.SchemaV2, pf.CurveModel, pf.VersionV2, "ability", day, ability)

	externalValue := 49.0
	external := ve.Result{Schema: ve.Schema, ModelVersion: ve.Model, Dataset: f.p.DatasetID, DatasetEpoch: "1", Generation: 1, Status: "available", Quality: "available", Value: &externalValue, Unit: ve.Unit, Provider: ve.Apple, Selected: &ve.Selected{Start: nowUs - int64(time.Hour), End: nowUs - int64(time.Minute)}, AsOf: nowUs, CalculatedAt: nowUs, EvaluationAt: nowUs}
	t.Log("seeding external")
	seedV2Artifact(t, f, ve.ScopeFor("synthetic-v2"), "vo2max_external_projection", ve.Schema, ve.Algorithm, "v1", "model_product_v2", day, external)

	t.Log("seeding computed rai vo2")
	raiVO2 := syntheticCalculatedVO2(t, f.p.DatasetID, now)
	seedV2Artifact(t, f, vo2.ScopeFor("synthetic-v2"), vo2.ArtifactKey, vo2.Schema, vo2.Model, vo2.Version, vo2.Family, day, raiVO2)

	score := 82.0
	recovery := rc.V2Result{Result: rc.Result{Schema: rc.SchemaV2, Dataset: f.p.DatasetID, DatasetEpoch: "1", Date: day, Timezone: "Asia/Shanghai", Status: "available", Quality: "complete", BaselineState: "established", Score: &score, ComponentCoverage: 1, Confidence: .5, ModelVersion: rc.ModelV2, ConfigHash: rc.DefaultV2Config("Asia/Shanghai").Hash(), Generation: 1, AsOf: nowUs, CapturedAt: nowUs, SDNNFreshness: rc.Freshness{LastEvent: &nowUs, LimitUs: int64(36 * time.Hour), State: "fresh"}, RHRFreshness: rc.Freshness{LastEvent: &nowUs, LimitUs: int64(36 * time.Hour), State: "fresh"}, SleepFreshness: rc.Freshness{LastEvent: &nowUs, LimitUs: int64(36 * time.Hour), State: "fresh"}}}
	t.Log("seeding recovery")
	seedV2Artifact(t, f, rc.ScopeV2For("synthetic-v2", day), "physiological_recovery", rc.SchemaV2, rc.Algorithm, "v2", "model_product_v2", day, recovery)

	baseline, recoveryScore, readyScore, penalty, burden := 20.0, 80.0, 70.0, .8, 10.0
	readiness := rd.Result{Date: day, Schema: rd.SchemaV2, Dataset: f.p.DatasetID, Generation: 1, ModelVersion: rd.ModelV2, ConfigHash: rd.DefaultV2Config("Asia/Shanghai").Hash(), Config: rd.DefaultV2Config("Asia/Shanghai"), InputHash: "x", CalendarHash: "x", AsOf: nowUs, EvaluationAt: nowUs, CapturedAt: nowUs, ValidBefore: end, Status: "available", Quality: "complete", Score: &readyScore, RecoveryScore: &recoveryScore, Burden: &burden, Baseline: rd.Baseline{Value: &baseline, Median: &baseline, TrainingDays: 20, SpanDays: 30, State: "established"}, Penalty: &penalty, Coverage: rd.Coverage{Through: nowUs, CaughtUp: true}, Confidence: .5}
	t.Log("seeding readiness")
	seedV2Artifact(t, f, rd.ScopeV2For("synthetic-v2"), "training_readiness", rd.SchemaV2, rd.Algorithm, "v2", "model_product_v2", day, readiness)

	t.Log("reading product")
	snap, err := f.s.ReadAthleteProduct(context.Background(), f.p, now)
	if err != nil {
		t.Fatal(err)
	}
	if snap.ResponseState != "complete_response" {
		t.Fatalf("response=%s metrics=%+v", snap.ResponseState, snap)
	}
	for name, state := range map[string]am.ProductMetricState{"load": snap.TrainingLoad.Envelope.State, "ability": snap.RunningAbility.Envelope.State, "external": snap.ExternalVO2Max.Envelope.State, "raiVO2": snap.RAIVO2Max.Envelope.State, "recovery": snap.PhysiologicalRecovery.Envelope.State, "readiness": snap.TrainingReadiness.Envelope.State} {
		if state != am.Available {
			t.Fatalf("%s state=%s", name, state)
		}
	}
	handler := httpapi.New(f.s, analysisLog)
	req := httptest.NewRequest(http.MethodGet, "/v2/analysis/athlete", nil)
	req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("active product http=%d body=%s", rec.Code, rec.Body.String())
	}
	var httpSnap am.AthleteModelSnapshot
	if err = json.Unmarshal(rec.Body.Bytes(), &httpSnap); err != nil {
		t.Fatal(err)
	}
	envelopes := map[string]am.Envelope{"load": httpSnap.TrainingLoad.Envelope, "ability": httpSnap.RunningAbility.Envelope, "external": httpSnap.ExternalVO2Max.Envelope, "recovery": httpSnap.PhysiologicalRecovery.Envelope, "readiness": httpSnap.TrainingReadiness.Envelope}
	if httpSnap.RAIVO2Max != nil {
		envelopes["raiVO2"] = httpSnap.RAIVO2Max.Envelope
	}
	for name, e := range envelopes {
		validation := "unvalidated"
		if name == "raiVO2" {
			validation = "experimental"
		}
		if e.ComputedAt == nil || e.PublishedAt == nil || e.ValidationStatus != validation {
			t.Fatalf("%s publication metadata incomplete: %+v", name, e)
		}
	}
	var body map[string]any
	if err = json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["schemaVersion"] != am.Schema2 || body["responseState"] != "complete_response" {
		t.Fatalf("unexpected product body: %s", rec.Body.String())
	}
	for _, key := range []string{"trainingLoad", "runningAbility", "externalVO2Max", "raiVO2Max", "physiologicalRecovery", "trainingReadiness"} {
		if body[key] == nil {
			t.Fatalf("missing typed metric %s: %s", key, rec.Body.String())
		}
	}
	req = httptest.NewRequest(http.MethodGet, "/v2/analysis/athlete/history?metric=trainingLoad&from="+day+"&to="+day+"&limit=10", nil)
	req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("history http=%d body=%s", rec.Code, rec.Body.String())
	}
	if err = json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body["points"].([]any)) != 1 {
		t.Fatalf("history points: %s", rec.Body.String())
	}
}

func TestSchema2ReaderRejectsTamperedArtifactAndDirtyScope(t *testing.T) {
	seedState := func(t *testing.T, f *fixture, hashOverride string) (time.Time, string) {
		t.Helper()
		loc, _ := time.LoadLocation("Asia/Shanghai")
		now := time.Now().In(loc)
		day := now.Format("2006-01-02")
		acute, chronic, recorded := 12.0, 20.0, 12.0
		state := tl.StateResult{Schema: tl.StateSchemaV2, DatasetID: f.p.DatasetID, Generation: 1, Model: tl.StateAlgorithmV2, Version: tl.VersionV2, ConfigHash: tl.ConfigHashV2(), Scope: tl.ScopeStateV2For("synthetic-v2"), Unit: tl.Unit, TrendUnit: tl.Unit + "/day", Days: []tl.DailyState{{Day: day, RecordedLoad: &recorded, KnownRecordedLoad: recorded, RecordedWorkouts: 1, Quality: "complete", Acute: tl.KernelValue{Value: &acute}, Chronic: tl.KernelValue{Value: &chronic}, Balance: ptrFloat(acute - chronic)}}}
		seedV2ArtifactHash(t, f, tl.ScopeStateV2For("synthetic-v2"), "load_state", tl.StateSchemaV2, tl.StateAlgorithmV2, tl.VersionV2, "load_state", day, state, hashOverride)
		return now, f.p.DatasetID
	}
	t.Run("tampered content hash", func(t *testing.T) {
		f := setup(t)
		activateSyntheticV2(t, f)
		now, _ := seedState(t, f, strings.Repeat("00", 32))
		snap, err := f.s.ReadAthleteProduct(context.Background(), f.p, now)
		if err != nil {
			t.Fatal(err)
		}
		if snap.TrainingLoad.Envelope.State != am.Unavailable || snap.TrainingLoad.Envelope.ModelStatus != "technical_failure" {
			t.Fatalf("tampered artifact accepted: %+v", snap.TrainingLoad.Envelope)
		}
	})
	t.Run("unclean generation", func(t *testing.T) {
		f := setup(t)
		activateSyntheticV2(t, f)
		now, _ := seedState(t, f, "")
		snap, err := f.s.ReadAthleteProduct(context.Background(), f.p, now)
		if err != nil || snap.TrainingLoad.Envelope.State != am.Available {
			t.Fatalf("baseline read %+v err=%v", snap.TrainingLoad.Envelope, err)
		}
		if _, err = f.s.Pool.Exec(context.Background(), `UPDATE dirty_scopes SET generation=2,clean_generation=1 WHERE dataset_id=$1 AND scope_key=$2`, f.p.DatasetID, tl.ScopeStateV2For("synthetic-v2")); err != nil {
			t.Fatal(err)
		}
		snap, err = f.s.ReadAthleteProduct(context.Background(), f.p, now)
		if err != nil {
			t.Fatal(err)
		}
		if snap.TrainingLoad.Envelope.State != am.Unavailable {
			t.Fatalf("unclean scope served: %+v", snap.TrainingLoad.Envelope)
		}
	})
}

func TestHistoryCursorBindsMetricAndReturnsRealV2Values(t *testing.T) {
	f := setup(t)
	activateSyntheticV2(t, f)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	today := now.Format("2006-01-02")
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")
	monthAgo := now.AddDate(0, 0, -28).Format("2006-01-02")
	scope := tl.ScopeStateV2For("synthetic-v2")
	for _, tc := range []struct {
		day  string
		load float64
	}{{monthAgo, 10}, {yesterday, 12}, {today, 15}} {
		value := tc.load
		state := tl.StateResult{Schema: tl.StateSchemaV2, DatasetID: f.p.DatasetID, Generation: 1, Model: tl.StateAlgorithmV2, Version: tl.VersionV2, ConfigHash: tl.ConfigHashV2(), Scope: scope, Unit: tl.Unit, TrendUnit: tl.Unit + "/day", Days: []tl.DailyState{{Day: tc.day, RecordedLoad: &value, KnownRecordedLoad: value, RecordedWorkouts: 1, Quality: "complete", Acute: tl.KernelValue{Value: &value}, Chronic: tl.KernelValue{Value: &value}, Balance: ptrFloat(0)}}}
		seedV2Artifact(t, f, scope, "load_state", tl.StateSchemaV2, tl.StateAlgorithmV2, tl.VersionV2, "load_state", tc.day, state)
	}
	handler := httpapi.New(f.s, analysisLog)
	do := func(url string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	first := do("/v2/analysis/athlete/history?metric=trainingLoad&limit=1&from=" + yesterday + "&to=" + today)
	if first.Code != http.StatusOK {
		t.Fatalf("first page=%d body=%s", first.Code, first.Body.String())
	}
	var p1 am.HistoryPage
	if err := json.Unmarshal(first.Body.Bytes(), &p1); err != nil {
		t.Fatal(err)
	}
	if len(p1.Points) != 1 || p1.Points[0].Date != today || p1.Points[0].Value == nil || *p1.Points[0].Value != 15 || p1.Points[0].Unit != tl.Unit+"/day" || p1.NextCursor == nil {
		t.Fatalf("first page=%+v", p1)
	}
	second := do("/v2/analysis/athlete/history?metric=trainingLoad&limit=1&from=" + yesterday + "&to=" + today + "&cursor=" + *p1.NextCursor)
	if second.Code != http.StatusOK {
		t.Fatalf("second page=%d body=%s", second.Code, second.Body.String())
	}
	var p2 am.HistoryPage
	if err := json.Unmarshal(second.Body.Bytes(), &p2); err != nil {
		t.Fatal(err)
	}
	if len(p2.Points) != 1 || p2.Points[0].Date != yesterday || p2.Points[0].Value == nil || *p2.Points[0].Value != 12 {
		t.Fatalf("second page=%+v", p2)
	}
	mismatch := do("/v2/analysis/athlete/history?metric=trainingReadiness&limit=1&from=" + yesterday + "&to=" + today + "&cursor=" + *p1.NextCursor)
	if mismatch.Code != http.StatusConflict || !strings.Contains(mismatch.Body.String(), "history_cursor_mismatch") {
		t.Fatalf("mismatched cursor=%d body=%s", mismatch.Code, mismatch.Body.String())
	}
	if tooLarge := do("/v2/analysis/athlete/history?metric=trainingLoad&limit=91"); tooLarge.Code != http.StatusUnprocessableEntity {
		t.Fatalf("limit 91=%d body=%s", tooLarge.Code, tooLarge.Body.String())
	}
	trendPage := do("/v2/analysis/athlete/history?metric=trainingLoad&limit=30&from=" + monthAgo + "&to=" + today)
	if trendPage.Code != http.StatusOK {
		t.Fatalf("trend page=%d body=%s", trendPage.Code, trendPage.Body.String())
	}
	var trendBody am.HistoryPage
	if err := json.Unmarshal(trendPage.Body.Bytes(), &trendBody); err != nil {
		t.Fatal(err)
	}
	if trendBody.Trend == nil || trendBody.Trend.Days != 28 || trendBody.Trend.Delta == nil || *trendBody.Trend.Delta != 5 || trendBody.Trend.FromDate != monthAgo || trendBody.Trend.ToDate != today {
		t.Fatalf("trend=%+v", trendBody.Trend)
	}
}

func dayHash(day string) []byte { h := sha256.Sum256([]byte(day)); return h[:] }

func syntheticCalculatedVO2(t *testing.T, dataset string, now time.Time) vo2.Result {
	t.Helper()
	const maxHR, rest, hr, speed = 209.0, 35.0, 182.0, 134.0
	const center = 3.5 + .2*speed*(maxHR-rest)/(hr-rest)
	evidence := make([]vo2.Evidence, 5)
	for i := range evidence {
		at := now.AddDate(0, 0, -17+4*i)
		evidence[i] = vo2.Evidence{WorkoutID: syncing.NewID(), Day: at.Format(time.DateOnly), At: at.UnixMicro(),
			KnownAt: at.UnixMicro(), ReferenceHash: "reference", SourceEpoch: "epoch", Q: 1, V: center,
			MaxHR: maxHR, RestHR: rest, HRMean: hr, SpeedMPS: speed / 60, EnvironmentKnown: true}
	}
	r, err := vo2.Aggregate(vo2.AggregateInput{DatasetID: dataset, Generation: 1, AsOf: now.UnixMicro(), Timezone: now.Location().String(),
		ReferenceHash: "reference", SourceEpoch: "epoch", Evidence: evidence})
	if err != nil || r.Status != "established" || r.Value == nil || r.Sensitivity.Corners != 8 || !r.LOO.SupportOK {
		t.Fatalf("invalid VO2 fixture: status=%s reasons=%v err=%v", r.Status, r.Reasons, err)
	}
	return r
}
