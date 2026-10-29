//go:build integration

package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pressly/goose/v3"
	"rai/server/internal/admin"
	"rai/server/internal/analysis"
	"rai/server/internal/httpapi"
	"rai/server/internal/jobs"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
	"rai/server/internal/training"
	"rai/server/migrations"
)

func trainingConfig() training.Config {
	now := time.Now().UTC()
	days := (int(now.Weekday()) + 6) % 7
	mon := now.AddDate(0, 0, -days)
	return training.Config{StartDate: mon.Format("2006-01-02"), Weeks: 8, Goal: training.Goal{Type: "running", Date: mon.AddDate(0, 0, 55).Format("2006-01-02"), DistanceM: 21097.5}, AvailableDays: []int{1, 3, 5, 7}, LongRunDay: 7, BaselineWeeklyM: training.Ptr(30000), PreviousLongM: training.Ptr(12000)}
}
func trainingFixture(t *testing.T) (*fixture, training.Plan) {
	t.Helper()
	f := setup(t)
	p, e := f.s.CreateTrainingPlan(context.Background(), f.p, training.CreateRequest{CommandID: syncing.NewID(), PlanID: syncing.NewID(), Config: trainingConfig()})
	if e != nil {
		t.Fatal(e)
	}
	runTraining(t, f)
	p, e = f.s.TrainingPlan(context.Background(), f.p, p.ID)
	if e != nil || p.Effective == nil {
		t.Fatal("no effective", e)
	}
	return f, p
}
func claimTraining(t *testing.T, f *fixture) postgres.Lease {
	t.Helper()
	if _, e := f.s.Pool.Exec(context.Background(), `UPDATE jobs SET run_after=now() WHERE kind='training.plan' AND state='queued'`); e != nil {
		t.Fatal(e)
	}
	return claimAnalysis(t, f, "training.plan")
}
func runTraining(t *testing.T, f *fixture) {
	t.Helper()
	l := claimTraining(t, f)
	jobs.Process(context.Background(), f.s, l, analysisLog)
	var state, code string
	if e := f.s.Pool.QueryRow(context.Background(), `SELECT state,COALESCE(last_error_code,'') FROM jobs WHERE id=$1`, l.ID).Scan(&state, &code); e != nil || state != "succeeded" {
		t.Fatalf("training worker %s %s %v", state, code, e)
	}
}
func computeTraining(t *testing.T, f *fixture, l postgres.Lease) (training.Input, string, training.Base) {
	t.Helper()
	in, e := f.s.LoadTraining(context.Background(), l, time.Now().UTC())
	if e != nil {
		t.Fatal(e)
	}
	id, e := f.s.StartTrainingRun(context.Background(), l, in)
	if e != nil {
		t.Fatal(e)
	}
	b, e := training.Generate(in.Context)
	if e != nil {
		t.Fatal(e)
	}
	return in, id, b
}
func sessionID(b training.Base, id string) training.Session {
	for _, s := range b.Sessions {
		if s.ID == id {
			return s
		}
	}
	return training.Session{}
}
func editTraining(t *testing.T, f *fixture, p training.Plan, session, path string, value any, rev int64) training.Effective {
	t.Helper()
	op := "edit"
	if path == "date" {
		op = "move"
	}
	b, _ := json.Marshal(value)
	out, e := f.s.PutTrainingOverride(context.Background(), f.p, p.ID, training.OverrideRequest{CommandID: syncing.NewID(), OverrideID: syncing.NewID(), SessionID: session, Operation: op, Path: path, Value: b, ExpectedOverrideRevision: rev})
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func recalcTraining(t *testing.T, f *fixture, p training.Plan) {
	t.Helper()
	_, e := f.s.ChangeTrainingPlan(context.Background(), f.p, p.ID, training.ChangeRequest{CommandID: syncing.NewID(), Operation: "recalculate", ExpectedConfigRevision: p.ConfigRevision})
	if e != nil {
		t.Fatal(e)
	}
}
func TestTrainingClosedLoopOverrideSurvivalE2E(t *testing.T) {
	f, p := trainingFixture(t)
	ctx := context.Background()
	mon := p.Effective.Sessions[0]
	wed := p.Effective.Sessions[2]
	sun := p.Effective.Sessions[6]
	sat, _ := time.Parse("2006-01-02", sun.Date)
	sat = sat.AddDate(0, 0, -1)
	editTraining(t, f, p, mon.ID, "distanceM", 7000, 0)
	eff := editTraining(t, f, p, sun.ID, "date", sat.Format("2006-01-02"), 1)
	ex, e := f.s.StartTrainingExecution(ctx, f.p, training.StartRequest{CommandID: syncing.NewID(), ExecutionID: syncing.NewID(), PlanID: p.ID, EffectiveID: eff.ID, SessionID: mon.ID})
	if e != nil {
		t.Fatal(e)
	}
	// Use real P4 publication to drive load-only low readiness. Parameters/raw
	// facts here are synthetic, through the existing ingest and analysis worker.
	workoutStream(t, f)
	w := workoutAt(time.Now().UTC().Add(-time.Hour).Truncate(time.Second), 1800)
	w.Workout.TotalDistance = &syncing.Quantity{Value: 7000, Unit: "m"}
	f.commit(t, []syncing.Event{w}, 1, nil)
	runOne(t, f, "analysis.workout")
	runOne(t, f, "analysis.training_state")
	// Publication lineage for an unavailable load is preserved; no forced cancel.
	runTraining(t, f)
	p2, e := f.s.TrainingPlan(ctx, f.p, p.ID)
	if e != nil {
		t.Fatal(e)
	}
	if sessionID(p2.Effective.Base, mon.ID).DistanceM != 7000 || sessionID(p2.Effective.Base, sun.ID).Date != sat.Format("2006-01-02") {
		t.Fatal("overrides lost")
	}
	saved, e := f.s.TrainingExecution(ctx, f.p, ex.ID)
	if e != nil || !reflect.DeepEqual(saved.Snapshot, ex.Snapshot) || saved.EffectiveID != eff.ID {
		t.Fatal("execution changed", e)
	}
	ex, e = f.s.ChangeTrainingExecution(ctx, f.p, ex.ID, training.ExecutionChange{CommandID: syncing.NewID(), ExpectedRevision: 1, Operation: "complete"})
	if e != nil || ex.WorkoutID != "" || len(ex.Feedback) != 1 || ex.Feedback[0].Status != "completed" {
		t.Fatal("completion before workout", e)
	}
	ex, e = f.s.ChangeTrainingExecution(ctx, f.p, ex.ID, training.ExecutionChange{CommandID: syncing.NewID(), ExpectedRevision: 2, Operation: "associate", WorkoutID: w.UUID})
	if e != nil || len(ex.Feedback) != 2 || ex.Feedback[1].Distance.Actual == nil || *ex.Feedback[1].Distance.Actual != 7000 || ex.Feedback[1].Status != "on_target" {
		t.Fatal("late association feedback", e)
	}
	runTraining(t, f)
	latest, _ := f.s.TrainingPlan(ctx, f.p, p.ID)
	if sessionID(latest.Effective.Base, mon.ID).DistanceM != 7000 || sessionID(latest.Effective.Base, sun.ID).Date != sat.Format("2006-01-02") {
		t.Fatal("feedback recalculation lost override")
	}
	audit, e := f.s.TrainingAudit(ctx, f.p, p.ID, latest.Effective.ID)
	if e != nil {
		t.Fatal(e)
	}
	replay, e := training.ResolveEffective(audit.Base, audit.Overrides)
	if e != nil || !reflect.DeepEqual(replay, audit.Effective.Base) {
		t.Fatal("effective not reproducible", e)
	}
	if sessionID(audit.Base, wed.ID).Modified {
		t.Fatal("base contaminated")
	}
	for _, table := range []string{"training_configs", "training_parameter_sets", "training_sessions", "training_base_versions", "training_effective_versions", "training_overrides", "training_feedback", "training_commands"} {
		if _, e = f.s.Pool.Exec(ctx, `DELETE FROM `+table); e == nil {
			t.Fatal("mutable", table)
		}
	}
	if _, e = f.s.Pool.Exec(ctx, `UPDATE training_executions SET snapshot='{}'`); e == nil {
		t.Fatal("snapshot mutable")
	}
	other := f.p
	other.DatasetID = syncing.NewID()
	if _, e = f.s.TrainingPlan(ctx, other, p.ID); e == nil {
		t.Fatal("dataset leak")
	}
}
func TestTrainingUserEditDuringGenerationAndStaleFence(t *testing.T) {
	f, p := trainingFixture(t)
	ctx := context.Background()
	mon := p.Effective.Sessions[0]
	recalcTraining(t, f, p)
	l := claimTraining(t, f)
	in, id, b := computeTraining(t, f, l)
	editTraining(t, f, p, mon.ID, "distanceM", 7000, 0)
	if status, e := f.s.PublishTraining(ctx, l, id, in, b); e != nil || status != "SUCCEEDED" {
		t.Fatal(status, e)
	}
	v, _ := f.s.TrainingPlan(ctx, f.p, p.ID)
	if v.Effective.BaseID == p.Effective.BaseID || sessionID(v.Effective.Base, mon.ID).DistanceM != 7000 {
		t.Fatal("publish lost concurrent edit")
	}
	recalcTraining(t, f, p)
	old := claimTraining(t, f)
	oi, oid, ob := computeTraining(t, f, old)
	recalcTraining(t, f, p)
	newer := claimTraining(t, f)
	ni, nid, nb := computeTraining(t, f, newer)
	if _, e := f.s.PublishTraining(ctx, newer, nid, ni, nb); e != nil {
		t.Fatal(e)
	}
	current, _ := f.s.TrainingPlan(ctx, f.p, p.ID)
	if status, e := f.s.PublishTraining(ctx, old, oid, oi, ob); e != nil || status != "SUPERSEDED" {
		t.Fatal("old generation", status, e)
	}
	v, _ = f.s.TrainingPlan(ctx, f.p, p.ID)
	if v.Effective.ID != current.Effective.ID {
		t.Fatal("stale overwrite")
	}
	recalcTraining(t, f, p)
	expired := claimTraining(t, f)
	xi, xid, xb := computeTraining(t, f, expired)
	if _, e := f.s.Pool.Exec(ctx, `UPDATE jobs SET lease_until=now()-interval '1 second' WHERE id=$1`, expired.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.RequeueExpired(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.PublishTraining(ctx, expired, xid, xi, xb); e == nil {
		t.Fatal("expired published")
	}
	runTraining(t, f)
}
func TestTrainingConcurrentOverridesIdempotencyAndRevocation(t *testing.T) {
	f, p := trainingFixture(t)
	ctx := context.Background()
	id := p.Effective.Sessions[0].ID
	r := training.OverrideRequest{CommandID: syncing.NewID(), OverrideID: syncing.NewID(), SessionID: id, Operation: "edit", Path: "distanceM", Value: json.RawMessage(`7000`)}
	first, e := f.s.PutTrainingOverride(ctx, f.p, p.ID, r)
	if e != nil {
		t.Fatal(e)
	}
	retry, e := f.s.PutTrainingOverride(ctx, f.p, p.ID, r)
	if e != nil || !reflect.DeepEqual(first, retry) {
		t.Fatal("not idempotent", e)
	}
	otherCommand := r
	otherCommand.CommandID = syncing.NewID()
	otherCommand.ExpectedOverrideRevision = 1
	duplicate, e := f.s.PutTrainingOverride(ctx, f.p, p.ID, otherCommand)
	if e != nil || !reflect.DeepEqual(first, duplicate) {
		t.Fatal("stable overrideID retry with new commandID", e)
	}
	otherCommand.CommandID = syncing.NewID()
	otherCommand.Value = json.RawMessage(`8000`)
	if _, e = f.s.PutTrainingOverride(ctx, f.p, p.ID, otherCommand); e == nil {
		t.Fatal("overrideID reused with different content")
	}
	changed := r
	changed.Value = json.RawMessage(`8000`)
	if _, e = f.s.PutTrainingOverride(ctx, f.p, p.ID, changed); e == nil {
		t.Fatal("id reuse")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, v := range []int{8000, 9000} {
		wg.Add(1)
		go func(v int) {
			defer wg.Done()
			_, e := f.s.PutTrainingOverride(ctx, f.p, p.ID, training.OverrideRequest{CommandID: syncing.NewID(), OverrideID: syncing.NewID(), SessionID: id, Operation: "edit", Path: "distanceM", Value: json.RawMessage(fmt.Sprint(v)), ExpectedOverrideRevision: 1})
			results <- e
		}(v)
	}
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatal("lost-update protection", success)
	}
	// Revoke the winning newer edit, then the first edit; base returns exactly.
	os, e := f.s.TrainingAudit(ctx, f.p, p.ID, first.ID)
	if e != nil {
		t.Fatal(e)
	}
	_ = os
	var event []byte
	if e = f.s.Pool.QueryRow(ctx, `SELECT event FROM training_overrides WHERE dataset_id=$1 AND revision=2`, f.p.DatasetID).Scan(&event); e != nil {
		t.Fatal(e)
	}
	var newer training.Override
	json.Unmarshal(event, &newer)
	for i, target := range []string{newer.ID, r.OverrideID} {
		eff, e := f.s.PutTrainingOverride(ctx, f.p, p.ID, training.OverrideRequest{CommandID: syncing.NewID(), OverrideID: syncing.NewID(), SessionID: id, Operation: "revoke", TargetID: target, ExpectedOverrideRevision: int64(2 + i)})
		if e != nil {
			t.Fatal(e)
		}
		if i == 1 && sessionID(eff.Base, id).DistanceM != p.Effective.Sessions[0].DistanceM {
			t.Fatal("revoke did not restore")
		}
	}
}
func TestTrainingAddMoveSkipDeleteAndScheduleConflict(t *testing.T) {
	f, p := trainingFixture(t)
	ctx := context.Background()
	sid := syncing.NewID()
	user := training.ManualSession{ID: sid, Date: p.Config.StartDate, Type: "EASY", DistanceM: 3000}
	add := training.OverrideRequest{CommandID: syncing.NewID(), OverrideID: syncing.NewID(), SessionID: sid, Operation: "add", Session: &user}
	if _, e := f.s.PutTrainingOverride(ctx, f.p, p.ID, add); e != nil {
		t.Fatal(e)
	}
	recalcTraining(t, f, p)
	runTraining(t, f)
	v, _ := f.s.TrainingPlan(ctx, f.p, p.ID)
	if sessionID(v.Effective.Base, sid).Origin != "user_added" {
		t.Fatal("user add lost")
	}
	for i, op := range []string{"skip", "delete"} {
		eff, e := f.s.PutTrainingOverride(ctx, f.p, p.ID, training.OverrideRequest{CommandID: syncing.NewID(), OverrideID: syncing.NewID(), SessionID: sid, Operation: op, ExpectedOverrideRevision: int64(i + 1)})
		if e != nil {
			t.Fatal(e)
		}
		want := "skipped"
		if op == "delete" {
			want = "cancelled"
		}
		if sessionID(eff.Base, sid).Status != want {
			t.Fatal(op)
		}
	}
	mon := p.Effective.Sessions[0].ID
	editTraining(t, f, p, mon, "distanceM", 7000, 3)
	cfg := p.Config
	cfg.AvailableDays = []int{2, 3, 5, 7}
	if _, e := f.s.ChangeTrainingPlan(ctx, f.p, p.ID, training.ChangeRequest{CommandID: syncing.NewID(), Operation: "update_config", ExpectedConfigRevision: 1, Config: &cfg}); e != nil {
		t.Fatal(e)
	}
	runTraining(t, f)
	v, _ = f.s.TrainingPlan(ctx, f.p, p.ID)
	kept := sessionID(v.Effective.Base, mon)
	if kept.DistanceM != 7000 || len(kept.Conflicts) == 0 {
		t.Fatal("removed slot override lost")
	}
}
func TestTrainingPublicationFaultAtomicity(t *testing.T) {
	for _, tc := range []struct{ table, op string }{{"training_base_versions", "INSERT"}, {"training_plans", "UPDATE"}, {"training_effective_versions", "INSERT"}, {"dirty_scopes", "UPDATE"}, {"jobs", "UPDATE"}, {"job_attempts", "UPDATE"}} {
		t.Run(tc.table, func(t *testing.T) {
			f, p := trainingFixture(t)
			ctx := context.Background()
			recalcTraining(t, f, p)
			l := claimTraining(t, f)
			in, id, b := computeTraining(t, f, l)
			before := count(t, f, "training_base_versions")
			trainingFault(t, f, tc.table, tc.op)
			if _, e := f.s.PublishTraining(ctx, l, id, in, b); e == nil {
				t.Fatal("fault succeeded")
			}
			v, _ := f.s.TrainingPlan(ctx, f.p, p.ID)
			if v.Effective.ID != p.Effective.ID || count(t, f, "training_base_versions") != before {
				t.Fatal("half plan")
			}
			dropTrainingFault(t, f, tc.table)
			if _, e := f.s.PublishTraining(ctx, l, id, in, b); e != nil {
				t.Fatal("retry", e)
			}
		})
	}
}
func trainingFault(t *testing.T, f *fixture, table, op string) {
	t.Helper()
	_, e := f.s.Pool.Exec(context.Background(), `CREATE OR REPLACE FUNCTION p5_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic fault'; END $$; CREATE TRIGGER p5_fail BEFORE `+op+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION p5_fail()`)
	if e != nil {
		t.Fatal(e)
	}
}
func dropTrainingFault(t *testing.T, f *fixture, table string) {
	t.Helper()
	if _, e := f.s.Pool.Exec(context.Background(), `DROP TRIGGER p5_fail ON `+table); e != nil {
		t.Fatal(e)
	}
}
func TestTrainingOverrideAndFeedbackFaultAtomicity(t *testing.T) {
	f, p := trainingFixture(t)
	ctx := context.Background()
	r := training.OverrideRequest{CommandID: syncing.NewID(), OverrideID: syncing.NewID(), SessionID: p.Effective.Sessions[0].ID, Operation: "edit", Path: "distanceM", Value: json.RawMessage(`7000`)}
	for _, table := range []string{"training_overrides", "training_effective_versions"} {
		trainingFault(t, f, table, "INSERT")
		if _, e := f.s.PutTrainingOverride(ctx, f.p, p.ID, r); e == nil {
			t.Fatal("override fault")
		}
		v, _ := f.s.TrainingPlan(ctx, f.p, p.ID)
		if v.OverrideRevision != 0 || v.Effective.ID != p.Effective.ID {
			t.Fatal("half override")
		}
		dropTrainingFault(t, f, table)
	}
	eff, e := f.s.PutTrainingOverride(ctx, f.p, p.ID, r)
	if e != nil {
		t.Fatal(e)
	}
	ex, e := f.s.StartTrainingExecution(ctx, f.p, training.StartRequest{CommandID: syncing.NewID(), ExecutionID: syncing.NewID(), PlanID: p.ID, EffectiveID: eff.ID, SessionID: r.SessionID})
	if e != nil {
		t.Fatal(e)
	}
	trainingFault(t, f, "training_feedback", "INSERT")
	req := training.ExecutionChange{CommandID: syncing.NewID(), ExpectedRevision: 1, Operation: "complete"}
	if _, e = f.s.ChangeTrainingExecution(ctx, f.p, ex.ID, req); e == nil {
		t.Fatal("feedback fault")
	}
	saved, _ := f.s.TrainingExecution(ctx, f.p, ex.ID)
	if saved.Status != "started" || saved.Revision != 1 {
		t.Fatal("half feedback")
	}
	dropTrainingFault(t, f, "training_feedback")
	if _, e = f.s.ChangeTrainingExecution(ctx, f.p, ex.ID, req); e != nil {
		t.Fatal(e)
	}
}
func TestTrainingQueueCoalescingColdStartAndAPI(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	cfg := trainingConfig()
	cfg.BaselineWeeklyM = nil
	cfg.PreviousLongM = nil
	r := training.CreateRequest{CommandID: syncing.NewID(), PlanID: syncing.NewID(), Config: cfg}
	p, e := f.s.CreateTrainingPlan(ctx, f.p, r)
	if e != nil {
		t.Fatal(e)
	}
	for range 12 {
		recalcTraining(t, f, p)
	}
	var queued int
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind='training.plan' AND state='queued'`).Scan(&queued); e != nil || queued != 1 {
		t.Fatal("job storm", queued, e)
	}
	l := claimTraining(t, f)
	jobs.Process(ctx, f.s, l, analysisLog)
	var code string
	if e = f.s.Pool.QueryRow(ctx, `SELECT last_error_code FROM jobs WHERE id=$1 AND state='failed'`, l.ID).Scan(&code); e != nil || code != "insufficient_context" {
		t.Fatal("invented plan", code, e)
	}
	h := httpapi.New(f.s, analysisLog)
	for _, path := range []string{"/v2/training/plans/current", "/v2/training/plans/" + p.ID} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"effective":null`)) {
			t.Fatal("pending API", w.Code, w.Body.String())
		}
	}
	req := httptest.NewRequest("POST", "/v2/training/plans", strings.NewReader(`{"commandID":"`+syncing.NewID()+`","unknown":true}`))
	req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 422 {
		t.Fatal("unknown DTO field", w.Code)
	}
}
func TestTrainingSchema12UpgradeAndReadiness(t *testing.T) {
	f := setupWithMigration(t, func(ctx context.Context, dsn string) error {
		db, e := sql.Open("pgx", dsn)
		if e != nil {
			return e
		}
		defer db.Close()
		p, e := goose.NewProvider(goose.DialectPostgres, db, migrations.Files)
		if e != nil {
			return e
		}
		_, e = p.UpTo(ctx, 12)
		return e
	})
	ctx := context.Background()
	if f.s.Ready(ctx) == nil {
		t.Fatal("accepted schema12")
	}
	before := businessSnapshot(t, f)
	for range 2 {
		if e := admin.Migrate(ctx, f.s.Pool.Config().ConnString()); e != nil {
			t.Fatal(e)
		}
	}
	if e := f.s.Ready(ctx); e != nil {
		t.Fatal(e)
	}
	after := businessSnapshot(t, f)
	for table, b := range before {
		if !bytes.Equal(b, after[table]) {
			t.Fatal("upgrade changed fact", table)
		}
	}
}
func TestTrainingRestrictedRoles(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	suffix := strings.ReplaceAll(syncing.NewID(), "-", "")
	apiRole, workerRole := "rai_api_"+suffix, "rai_worker_"+suffix
	b, e := os.ReadFile("../../../deploy/postgres/roles.sql")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Pool.Exec(ctx, strings.ReplaceAll(strings.ReplaceAll(string(b), "rai_api", apiRole), "rai_worker", workerRole)); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_, e := f.s.Pool.Exec(context.Background(), "DROP OWNED BY "+pgx.Identifier{apiRole}.Sanitize()+", "+pgx.Identifier{workerRole}.Sanitize()+"; DROP ROLE "+pgx.Identifier{apiRole}.Sanitize()+", "+pgx.Identifier{workerRole}.Sanitize())
		if e != nil {
			t.Error(e)
		}
	})
	open := func(role string) *postgres.Store {
		u, _ := url.Parse(f.s.Pool.Config().ConnString())
		q := u.Query()
		q.Set("options", "-crole="+role)
		u.RawQuery = q.Encode()
		s, e := postgres.Open(ctx, u.String(), 2)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(s.Pool.Close)
		return s
	}
	api, worker := open(apiRole), open(workerRole)
	p, e := api.CreateTrainingPlan(ctx, f.p, training.CreateRequest{CommandID: syncing.NewID(), PlanID: syncing.NewID(), Config: trainingConfig()})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Pool.Exec(ctx, `UPDATE jobs SET run_after=now() WHERE kind='training.plan'`); e != nil {
		t.Fatal(e)
	}
	l, e := worker.Claim(ctx, []string{"training.plan"})
	if e != nil {
		t.Fatal(e)
	}
	jobs.Process(ctx, worker, l, analysisLog)
	v, e := api.TrainingPlan(ctx, f.p, p.ID)
	if e != nil || v.Effective == nil {
		t.Fatal("role generation", e)
	}
	if _, e = api.Pool.Exec(ctx, `UPDATE training_effective_versions SET effective='{}'`); e == nil {
		t.Fatal("API can mutate version")
	}
	if _, e = worker.Pool.Exec(ctx, `UPDATE workouts SET payload='{}'`); e == nil {
		t.Fatal("worker raw mutation")
	}
}
func TestTrainingExecutionScheduledSkipAndHistory(t *testing.T) {
	f, p := trainingFixture(t)
	ctx := context.Background()
	r := training.StartRequest{CommandID: syncing.NewID(), ExecutionID: syncing.NewID(), PlanID: p.ID, EffectiveID: p.Effective.ID, SessionID: p.Effective.Sessions[0].ID, Scheduled: true}
	ex, e := f.s.StartTrainingExecution(ctx, f.p, r)
	if e != nil {
		t.Fatal(e)
	}
	ex, e = f.s.ChangeTrainingExecution(ctx, f.p, ex.ID, training.ExecutionChange{CommandID: syncing.NewID(), Operation: "skip", ExpectedRevision: 1})
	if e != nil || ex.Status != "skipped" || ex.Feedback[0].Status != "skipped" {
		t.Fatal("skip", e)
	}
	runTraining(t, f)
	history, e := f.s.TrainingHistory(ctx, f.p, p.ID, 0, 1)
	if e != nil || len(history.Versions) != 1 {
		t.Fatal(e)
	}
	next, e := f.s.TrainingHistory(ctx, f.p, p.ID, history.Next, 50)
	if e != nil || len(next.Versions) != 1 {
		t.Fatal("history", e)
	}
	if _, e = f.s.Claim(ctx, []string{"training.plan"}); !errors.Is(e, pgx.ErrNoRows) {
		t.Fatal("job remained")
	}
}

func TestTrainingPublishedReadinessFieldSurvival(t *testing.T) {
	f, p := trainingFixture(t)
	ctx := context.Background()
	mon, sun := p.Effective.Sessions[0], p.Effective.Sessions[6]
	sat, _ := time.Parse("2006-01-02", sun.Date)
	sat = sat.AddDate(0, 0, -1)
	editTraining(t, f, p, mon.ID, "distanceM", 7000, 0)
	editTraining(t, f, p, sun.ID, "date", sat.Format("2006-01-02"), 1)
	hrStream := f.stream
	workoutStream(t, f)
	w := workoutAt(time.Now().UTC().Add(-time.Hour).Truncate(time.Second), 1800)
	w.Workout.TotalDistance = &syncing.Quantity{Value: 7000, Unit: "m"}
	f.commit(t, []syncing.Event{w}, 1, nil)
	f.stream = hrStream
	hr := sample(syncing.NewID(), 150)
	hr.StartUs = w.StartUs
	hr.EndUs = w.EndUs
	f.commit(t, []syncing.Event{hr}, 1, nil)
	if _, e := f.s.AssociateSample(ctx, f.p, syncing.AssociationRequest{ProtocolVersion: 2, DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch, AssociationID: syncing.NewID(), SampleType: kind, SampleUUID: hr.UUID, WorkoutUUID: w.UUID, Source: &syncing.Source{BundleID: "synthetic.test"}}); e != nil {
		t.Fatal(e)
	}
	if e := admin.SetAnalysisParameters(ctx, f.s, f.p.DatasetID, analysis.Parameters{RestHR: 50, MaxHR: 200, A: .64, B: 1.92, Provenance: "synthetic coach test"}); e != nil {
		t.Fatal(e)
	}
	runOne(t, f, "analysis.sample")
	runOne(t, f, "analysis.workout")
	runOne(t, f, "analysis.training_state")
	runTraining(t, f)
	latest, e := f.s.TrainingPlan(ctx, f.p, p.ID)
	if e != nil {
		t.Fatal(e)
	}
	audit, e := f.s.TrainingAudit(ctx, f.p, p.ID, latest.Effective.ID)
	if e != nil {
		t.Fatal(e)
	}
	monday, long := sessionID(audit.Effective.Base, mon.ID), sessionID(audit.Effective.Base, sun.ID)
	if monday.DistanceM != 7000 || long.Date != sat.Format("2006-01-02") || long.DistanceM != sessionID(audit.Base, sun.ID).DistanceM || long.DistanceM >= sun.DistanceM || sessionID(audit.Base, mon.ID).DistanceM >= mon.DistanceM || audit.Effective.Sessions[2].Type != "RECOVERY" {
		t.Fatal("published fatigue did not preserve just edited fields")
	}
	if len(audit.Context.Signals) == 0 || len(audit.Context.Workouts) != 1 || len(audit.Context.Workouts[0].Metrics) != 3 {
		t.Fatal("missing provenance")
	}
}
func TestTrainingArchiveRetainsStartedExecution(t *testing.T) {
	f, p := trainingFixture(t)
	ctx := context.Background()
	ex, e := f.s.StartTrainingExecution(ctx, f.p, training.StartRequest{CommandID: syncing.NewID(), ExecutionID: syncing.NewID(), PlanID: p.ID, EffectiveID: p.Effective.ID, SessionID: p.Effective.Sessions[0].ID})
	if e != nil {
		t.Fatal(e)
	}
	recalcTraining(t, f, p)
	l := claimTraining(t, f)
	in, id, b := computeTraining(t, f, l)
	newer, e := f.s.CreateTrainingPlan(ctx, f.p, training.CreateRequest{CommandID: syncing.NewID(), PlanID: syncing.NewID(), Config: trainingConfig()})
	if e != nil {
		t.Fatal(e)
	}
	if status, e := f.s.PublishTraining(ctx, l, id, in, b); e != nil || status != "SUPERSEDED" {
		t.Fatal("archived publication", e)
	}
	cur, e := f.s.TrainingPlan(ctx, f.p, "")
	if e != nil || cur.ID != newer.ID {
		t.Fatal("current identity", e)
	}
	if _, e = f.s.ChangeTrainingExecution(ctx, f.p, ex.ID, training.ExecutionChange{CommandID: syncing.NewID(), Operation: "complete", ExpectedRevision: 1}); e != nil {
		t.Fatal("archived execution completion", e)
	}
	if _, e = f.s.TrainingAudit(ctx, f.p, p.ID, p.Effective.ID); e != nil {
		t.Fatal("archived history missing", e)
	}
}
func TestTrainingHTTPMutationAndDefaultEffective(t *testing.T) {
	f, p := trainingFixture(t)
	h := httpapi.New(f.s, analysisLog)
	call := func(method, path string, value any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(value)
		r := httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	r := training.OverrideRequest{CommandID: syncing.NewID(), OverrideID: syncing.NewID(), SessionID: p.Effective.Sessions[0].ID, Operation: "edit", Path: "distanceM", Value: json.RawMessage(`7000`)}
	first := call("POST", "/v2/training/plans/"+p.ID+"/overrides", r)
	if first.Code != 200 {
		t.Fatal("HTTP override", first.Code, first.Body.String())
	}
	again := call("POST", "/v2/training/plans/"+p.ID+"/overrides", r)
	if !bytes.Equal(first.Body.Bytes(), again.Body.Bytes()) {
		t.Fatal("HTTP retry")
	}
	current := call("GET", "/v2/training/plans/current", nil)
	var v training.Plan
	if current.Code != 200 || json.Unmarshal(current.Body.Bytes(), &v) != nil || v.Effective.Sessions[0].DistanceM != 7000 {
		t.Fatal("default effective")
	}
	r.CommandID = syncing.NewID()
	r.OverrideID = syncing.NewID()
	if w := call("POST", "/v2/training/plans/"+p.ID+"/overrides", r); w.Code != 409 {
		t.Fatal("HTTP revision conflict", w.Code)
	}
	for _, path := range []string{"/v2/training/plans/" + p.ID + "/effective", "/v2/training/plans/" + p.ID + "/effective/" + v.Effective.ID, "/v2/training/plans/" + p.ID + "/history?limit=1"} {
		if w := call("GET", path, nil); w.Code != 200 {
			t.Fatal("HTTP read", w.Code, path)
		}
	}
}

func TestTrainingLeaseExpiresDuringPublishRollsBack(t *testing.T) {
	f, p := trainingFixture(t)
	ctx := context.Background()
	recalcTraining(t, f, p)
	l := claimTraining(t, f)
	in, id, b := computeTraining(t, f, l)
	if _, e := f.s.Pool.Exec(ctx, `CREATE FUNCTION p5_pause() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(0.2); RETURN NEW; END $$; CREATE TRIGGER p5_pause BEFORE INSERT ON training_base_versions FOR EACH ROW EXECUTE FUNCTION p5_pause()`); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Pool.Exec(ctx, `UPDATE jobs SET lease_until=clock_timestamp()+interval '100 milliseconds' WHERE id=$1`, l.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.PublishTraining(ctx, l, id, in, b); e == nil {
		t.Fatal("lease expired in transaction published")
	}
	v, _ := f.s.TrainingPlan(ctx, f.p, p.ID)
	if v.Effective.ID != p.Effective.ID || count(t, f, "training_base_versions") != 1 {
		t.Fatal("expired half plan")
	}
}
func TestTrainingContextExcludesUnpublishedAndDirtyFeatures(t *testing.T) {
	f, w := analysisFixture(t)
	ctx := context.Background()
	p, e := f.s.CreateTrainingPlan(ctx, f.p, training.CreateRequest{CommandID: syncing.NewID(), PlanID: syncing.NewID(), Config: trainingConfig()})
	if e != nil {
		t.Fatal(e)
	}
	l := postgresLeaseForContext(p, f)
	in, e := f.s.LoadTraining(ctx, l, time.Now().UTC())
	if e != nil || len(in.Context.Workouts) != 0 {
		t.Fatal("raw fact used before publication", e)
	}
	runOne(t, f, "analysis.workout")
	in, e = f.s.LoadTraining(ctx, l, time.Now().UTC())
	if e != nil || len(in.Context.Workouts) != 1 || in.Context.Workouts[0].Load != nil {
		t.Fatal("publication or nullable load", e)
	}
	if _, e = f.s.Pool.Exec(ctx, `SELECT rai_analysis_dirty($1,$2,'analysis.workout',now(),now())`, f.p.DatasetID, "workout:"+syncing.WorkoutType+":"+w.UUID); e != nil {
		t.Fatal(e)
	}
	in, e = f.s.LoadTraining(ctx, l, time.Now().UTC())
	if e != nil || len(in.Context.Workouts) != 0 {
		t.Fatal("dirty publication used", e)
	}
}

func TestTrainingFrozenTimezoneAndLateAnalysisFeedback(t *testing.T) {
	f, p := trainingFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	at := time.Date(now.Year(), now.Month(), now.Day(), 20, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	if _, e := f.s.Pool.Exec(ctx, `UPDATE datasets SET timezone='America/New_York' WHERE id=$1`, f.p.DatasetID); e != nil {
		t.Fatal(e)
	}
	workoutStream(t, f)
	w := workoutAt(at, 1800)
	w.Workout.TotalDistance = &syncing.Quantity{Value: 7000, Unit: "m"}
	f.commit(t, []syncing.Event{w}, 1, nil)
	ex, e := f.s.StartTrainingExecution(ctx, f.p, training.StartRequest{CommandID: syncing.NewID(), ExecutionID: syncing.NewID(), PlanID: p.ID, EffectiveID: p.Effective.ID, SessionID: p.Effective.Sessions[0].ID})
	if e != nil {
		t.Fatal(e)
	}
	ex, e = f.s.ChangeTrainingExecution(ctx, f.p, ex.ID, training.ExecutionChange{CommandID: syncing.NewID(), Operation: "complete", ExpectedRevision: 1, WorkoutID: w.UUID})
	if e != nil || len(ex.Feedback) != 1 || ex.Feedback[0].SourceRunID != "" {
		t.Fatal("pending analysis feedback", e)
	}
	runOne(t, f, "analysis.workout")
	runOne(t, f, "analysis.training_state")
	runTraining(t, f)
	saved, e := f.s.TrainingExecution(ctx, f.p, ex.ID)
	if e != nil || len(saved.Feedback) != 2 || saved.Feedback[1].SourceRunID == "" || saved.Feedback[1].Distance.Actual == nil {
		t.Fatal("late analysis did not append feedback", e)
	}
	current, _ := f.s.TrainingPlan(ctx, f.p, p.ID)
	audit, e := f.s.TrainingAudit(ctx, f.p, p.ID, current.Effective.ID)
	loc, _ := time.LoadLocation(p.Timezone)
	if e != nil || audit.Context.Timezone != p.Timezone || len(audit.Context.Workouts) != 1 || audit.Context.Workouts[0].Date != at.In(loc).Format("2006-01-02") {
		t.Fatal("dataset timezone moved frozen plan date", e)
	}
	if len(audit.Context.Signals) != 0 {
		t.Fatal("state from a different calendar timezone consumed")
	}
	recalcTraining(t, f, p)
	runTraining(t, f)
	saved, e = f.s.TrainingExecution(ctx, f.p, ex.ID)
	if e != nil || len(saved.Feedback) != 2 {
		t.Fatal("duplicate feedback revision", e)
	}
}
