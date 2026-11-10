//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"rai/server/internal/admin"
	am "rai/server/internal/athletemodel"
	"rai/server/internal/httpapi"
	pf "rai/server/internal/performance"
	rd "rai/server/internal/readiness"
	rc "rai/server/internal/recovery"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
	tl "rai/server/internal/trainingload"
	ve "rai/server/internal/vo2maxexternal"
	wf "rai/server/internal/workoutfeatures"
)

func modelStackFixture(t *testing.T) *fixture {
	t.Helper()
	f, _, _ := readinessFixture(t)
	ctx := context.Background()
	now := time.Now()
	old := m3Bundle(t, f, now.AddDate(0, 0, -60), 300)
	importPhase3(t, f, old)
	if e := admin.QueueTrainingLoadCandidate(ctx, f.s, f.p.DatasetID, old.WorkoutID, syncing.NewID(), "synthetic M7 older load history"); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 7; i++ {
		b := m3Bundle(t, f, now.Add(-time.Hour).AddDate(0, 0, -4*(6-i)), 2400)
		importPhase3(t, f, b)
		if e := admin.QueueTrainingLoadCandidate(ctx, f.s, f.p.DatasetID, b.WorkoutID, syncing.NewID(), "synthetic M7"); e != nil {
			t.Fatal(e)
		}
	}
	if e := admin.QueuePerformanceCandidate(ctx, f.s, f.p.DatasetID, syncing.NewID(), "synthetic M7"); e != nil {
		t.Fatal(e)
	}
	drainReadinessUpstream(t, f, wf.JobKind)
	drainReadinessUpstream(t, f, tl.JobKind)
	drainReadinessUpstream(t, f, tl.StateJobKind)
	drainReadinessUpstream(t, f, pf.JobKind)
	b := externalBatch(t, f)
	requireVO2(t, f, b)
	selection := ve.Selection{ID: syncing.NewID(), Epoch: b.Facts[0].Provenance.EpochID(), EffectiveFrom: b.FlowCreatedAtUs}
	if e := admin.SetVO2SourceSelection(ctx, f.s, f.p.DatasetID, syncing.NewID(), "synthetic M7", selection); e != nil {
		t.Fatal(e)
	}
	if e := admin.QueueVO2ExternalCandidate(ctx, f.s, f.p.DatasetID, syncing.NewID(), "synthetic M7"); e != nil {
		t.Fatal(e)
	}
	drainReadinessUpstream(t, f, ve.JobKind)
	runReadinessLatest(t, f)
	return f
}
func modelSnapshot(t *testing.T, f *fixture, at time.Time) am.AthleteModelCandidateSnapshot {
	t.Helper()
	r, stats, e := f.s.ReadAthleteModelCandidateSnapshot(context.Background(), f.p.DatasetID, at)
	if e != nil {
		t.Fatal(e)
	}
	if stats.DBReads > 5 {
		t.Fatal("unbounded reader queries", stats.DBReads)
	}
	return r
}
func TestAthleteModelFullAndLeakage(t *testing.T) {
	f := modelStackFixture(t)
	ctx := context.Background()
	at := time.Now()
	r := modelSnapshot(t, f, at)
	if r.Snapshot.ResponseState != "complete_response" {
		t.Fatal("technical projection failure", r.Snapshot.TrainingLoad.Envelope.ModelStatus, r.Snapshot.RunningAbility.Envelope.ModelStatus, r.Snapshot.ExternalVO2Max.Envelope.ModelStatus, r.Snapshot.PhysiologicalRecovery.Envelope.ModelStatus, r.Snapshot.TrainingReadiness.Envelope.ModelStatus)
	}
	for _, e := range []am.Envelope{r.Snapshot.TrainingLoad.Envelope, r.Snapshot.RunningAbility.Envelope, r.Snapshot.ExternalVO2Max.Envelope, r.Snapshot.PhysiologicalRecovery.Envelope, r.Snapshot.TrainingReadiness.Envelope} {
		if e.State != am.Available || e.ModelVersion == "" || e.AsOf == nil || e.EvaluationAt != at.UnixMicro() {
			t.Fatal("full fixture unavailable", e.Metric, e.ModelStatus, e.State)
		}
	}
	a, _ := json.Marshal(r)
	b, _ := json.Marshal(modelSnapshot(t, f, at))
	if !bytes.Equal(a, b) {
		t.Fatal("semantic instability")
	}
	active, e := f.s.ReadAthleteModelSnapshot(ctx, f.p, at)
	if e != nil || active.Assignment.Channel != "active" || active.Assignment.StackID != "legacy" || active.RunningAbility.Value != nil || active.TrainingReadiness.Value != nil {
		t.Fatal("active candidate leakage", e)
	}
	legacy := func() []byte {
		t.Helper()
		overview, e := f.s.ProductOverview(ctx, f.p)
		if e != nil {
			t.Fatal(e)
		}
		history, e := f.s.ProductHistory(ctx, f.p, 14)
		if e != nil {
			t.Fatal(e)
		}
		workouts, e := f.s.ProductWorkouts(ctx, f.p, "", 50, "")
		if e != nil {
			t.Fatal(e)
		}
		v, _ := json.Marshal([]any{overview, history, workouts})
		return v
	}
	before := legacy()
	_ = modelSnapshot(t, f, at)
	after := legacy()
	if !bytes.Equal(before, after) {
		t.Fatal("legacy product changed by read")
	}
	for _, bad := range []string{rd.Model, rc.Model, pf.AbilityModel, ve.Model, tl.Algorithm} {
		if bytes.Contains(after, []byte(bad)) {
			t.Fatal("candidate in legacy product")
		}
	}
	var output bytes.Buffer
	if e = admin.ModelStackCommand(ctx, f.s.Pool.Config().ConnString(), []string{"inspect", "--dataset", f.p.DatasetID, "--evaluation-at", at.Format(time.RFC3339Nano)}, &output); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{f.p.DatasetID, `"value"`, `"score"`, `"confidence"`, `"supportingFacts"`, `"parameter"`} {
		if bytes.Contains(output.Bytes(), []byte(bad)) {
			t.Fatal("admin privacy")
		}
	}
	for _, bad := range []string{"rawHRV", "sourceBundle", "deviceName", "factRefs", "configHash", "inputHash", "sigma", "mAD", f.p.DatasetID} {
		if bytes.Contains(a, []byte(bad)) {
			t.Fatal("product privacy")
		}
	}
	handler := httpapi.New(f.s, analysisLog)
	for _, path := range []string{"/v2/athlete-model", "/v2/athlete-model?channel=candidate", "/v2/analysis/model-stack", "/v2/admin/athlete-model"} {
		request := httptest.NewRequest("GET", path, nil)
		request.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 404 {
			t.Fatal("candidate HTTP route was exposed", response.Code)
		}
	}
	output.Reset()
	if e = admin.ModelStackCommand(ctx, f.s.Pool.Config().ConnString(), []string{"inspect", "--dataset", f.p.DatasetID, "--evaluation-at", at.Format(time.RFC3339Nano), "--synthetic-test-values"}, &output); e != nil {
		t.Fatal(e)
	}
	var synthetic am.AthleteModelCandidateSnapshot
	if json.Unmarshal(output.Bytes(), &synthetic) != nil || !synthetic.CandidateOnly || synthetic.Snapshot.RunningAbility.Value == nil {
		t.Fatal("explicit synthetic preview missing")
	}
	rows, e := f.s.Pool.Query(ctx, `SELECT upstream_scope,downstream_scope FROM analysis_model_dependencies WHERE dataset_id=$1 ORDER BY upstream_scope,downstream_scope`, f.p.DatasetID)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	edges := []am.Edge{}
	class := func(scope string) string {
		switch {
		case strings.Contains(scope, "/workout_load/"), strings.HasSuffix(scope, "/athlete/load"):
			return "M2"
		case strings.Contains(scope, "/workout/"):
			return "M1"
		case strings.HasSuffix(scope, "/athlete/performance"):
			return "M3"
		case strings.HasPrefix(scope, "daily/recovery/"):
			return "M5B"
		case strings.HasPrefix(scope, "instant/readiness/"):
			return "M6"
		case strings.HasPrefix(scope, "daily/raw/"):
			return "DailyRaw"
		case strings.HasPrefix(scope, "daily/features/"):
			return "DailyFeatures"
		case strings.HasPrefix(scope, "daily/baseline/"):
			return "DailyBaseline"
		case strings.HasPrefix(scope, "daily/readiness/"):
			return "DormantDailyReadiness"
		case strings.HasPrefix(scope, "readiness/load-coverage/"):
			return "LoadCoverage"
		case strings.HasSuffix(scope, "/athlete/vo2"), strings.HasSuffix(scope, "/athlete/ability"), strings.HasSuffix(scope, "/athlete/endurance"):
			return "DormantM1Future"
		case scope == ve.FactsScope:
			return "VO2Raw"
		case scope == ve.SelectionScope:
			return "VO2Selection"
		case scope == ve.Scope:
			return "M4"
		default:
			return "unknown"
		}
	}
	// Include the frozen pre-M7 internal/control and dormant placeholder edges.
	// No placeholder is promoted into a computation or product input by M7.
	allowed := map[string]bool{"M1:M2": true, "M1:M3": true, "M2:M2": true, "M2:M6": true, "M5B:M6": true,
		"DailyRaw:DailyFeatures": true, "DailyFeatures:DailyBaseline": true, "DailyFeatures:M5B": true, "DailyBaseline:M5B": true,
		"M5B:DormantDailyReadiness": true, "LoadCoverage:M6": true, "M1:DormantM1Future": true, "VO2Raw:M4": true, "VO2Selection:M4": true}
	for rows.Next() {
		var edge am.Edge
		if e = rows.Scan(&edge.From, &edge.To); e != nil {
			t.Fatal(e)
		}
		edges = append(edges, edge)
		if !allowed[class(edge.From)+":"+class(edge.To)] {
			t.Fatal("unapproved model dependency", class(edge.From), class(edge.To))
		}
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	rows.Close()
	if len(edges) == 0 || !am.Acyclic(edges) {
		t.Fatal("actual DB dependency DAG")
	}
	// A newer recorded workout with no published load must not make an older
	// analysed workout appear to be the latest workout load.
	newWorkout := m3Bundle(t, f, time.Now().Add(-time.Minute), 300)
	importPhase3(t, f, newWorkout)
	latest := modelSnapshot(t, f, time.Now())
	if latest.Snapshot.TrainingLoad.Value != nil && latest.Snapshot.TrainingLoad.Value.LatestWorkoutLoad != nil {
		t.Fatal("old analysed workout substituted for pending latest workout")
	}
}
func TestAthleteModelMissingAndCorruption(t *testing.T) {
	f := modelStackFixture(t)
	ctx := context.Background()
	at := time.Now()
	good := modelSnapshot(t, f, at)
	var hash []byte
	if e := f.s.Pool.QueryRow(ctx, `SELECT a.content_sha256 FROM analysis_artifacts a JOIN analysis_publications p ON p.dataset_id=a.dataset_id AND p.published_run_id=a.run_id WHERE p.dataset_id=$1 AND p.metric_family=$2 AND a.artifact_key='physiological_recovery'`, f.p.DatasetID, rc.Family).Scan(&hash); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Pool.Exec(ctx, `ALTER TABLE analysis_artifacts DISABLE TRIGGER immutable_analysis_artifacts; UPDATE analysis_artifacts SET content_sha256=decode(repeat('a',64),'hex') WHERE artifact_key='physiological_recovery'; ALTER TABLE analysis_artifacts ENABLE TRIGGER immutable_analysis_artifacts`); e != nil {
		t.Fatal(e)
	}
	bad := modelSnapshot(t, f, at)
	if bad.Snapshot.ResponseState != "partial_response" || bad.Snapshot.PhysiologicalRecovery.Envelope.TechnicalError == nil || bad.Snapshot.PhysiologicalRecovery.Value != nil || bad.Snapshot.RunningAbility.Value == nil || bad.Snapshot.ExternalVO2Max.Value == nil {
		t.Fatal("corruption fallback or global failure")
	}
	if bad.Snapshot.PhysiologicalRecovery.Envelope.ModelVersion != rc.Model {
		t.Fatal("corrupt model identity hidden")
	}
	if _, e := f.s.Pool.Exec(ctx, `ALTER TABLE analysis_artifacts DISABLE TRIGGER immutable_analysis_artifacts`); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Pool.Exec(ctx, `UPDATE analysis_artifacts SET content_sha256=$1 WHERE artifact_key='physiological_recovery'`, hash); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Pool.Exec(ctx, `ALTER TABLE analysis_artifacts ENABLE TRIGGER immutable_analysis_artifacts; DELETE FROM analysis_publications WHERE metric_family='physiological_recovery_candidate'`); e != nil {
		t.Fatal(e)
	}
	withdrawn := modelSnapshot(t, f, at)
	if withdrawn.Snapshot.TrainingReadiness.Value != nil {
		t.Fatal("one missing dependency hidden among valid references")
	}
	if _, e := f.s.Pool.Exec(ctx, `DELETE FROM analysis_publications WHERE metric_family IN ('training_readiness_candidate','vo2max_external_candidate')`); e != nil {
		t.Fatal(e)
	}
	missing := modelSnapshot(t, f, at)
	if missing.Snapshot.ResponseState != "complete_response" || missing.Snapshot.PhysiologicalRecovery.Value != nil || missing.Snapshot.RunningAbility.Value == nil || good.Snapshot.RunningAbility.Value == nil {
		t.Fatal("business missing became technical failure")
	}
	newUser := setup(t)
	n := modelSnapshot(t, newUser, time.Now())
	if n.Snapshot.RunningAbility.Value != nil || n.Snapshot.TrainingLoad.Value != nil || n.Snapshot.TrainingReadiness.Value != nil {
		t.Fatal("new user zeros")
	}
}

type modelSnapshotBarrier struct {
	once           sync.Once
	ready, release chan struct{}
}

func (b *modelSnapshotBarrier) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, modelBarrierKey{}, strings.Contains(d.SQL, "SELECT d.timezone,d.epoch::text,d.commit_rev"))
}

type modelBarrierKey struct{}

func (b *modelSnapshotBarrier) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	if match, _ := ctx.Value(modelBarrierKey{}).(bool); match {
		b.once.Do(func() {
			close(b.ready)
			select {
			case <-b.release:
			case <-ctx.Done():
			}
		})
	}
}
func TestAthleteModelStableAssignmentRevisionAndPointerRace(t *testing.T) {
	f := modelStackFixture(t)
	ctx := context.Background()
	at := time.Now()
	before := modelSnapshot(t, f, at)
	barrier := &modelSnapshotBarrier{ready: make(chan struct{}), release: make(chan struct{})}
	cfg, e := pgxpool.ParseConfig(f.s.Pool.Config().ConnString())
	if e != nil {
		t.Fatal(e)
	}
	cfg.ConnConfig.Tracer = barrier
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	reader := &postgres.Store{Pool: pool}
	type result struct {
		snapshot am.AthleteModelCandidateSnapshot
		err      error
	}
	done := make(chan result, 1)
	go func() {
		v, _, err := reader.ReadAthleteModelCandidateSnapshot(ctx, f.p.DatasetID, at)
		done <- result{v, err}
	}()
	select {
	case <-barrier.ready:
	case <-time.After(10 * time.Second):
		t.Fatal("reader did not start stable view")
	}
	tx, e := f.s.Pool.Begin(ctx)
	if e != nil {
		close(barrier.release)
		t.Fatal(e)
	}
	for _, query := range []string{`UPDATE analysis_model_channels SET assignment_version=assignment_version+1 WHERE dataset_id=$1 AND channel='candidate'`, `UPDATE datasets SET commit_rev=commit_rev+1 WHERE id=$1`, `DELETE FROM analysis_publications WHERE dataset_id=$1 AND metric_family='physiological_recovery_candidate'`} {
		if _, e = tx.Exec(ctx, query, f.p.DatasetID); e != nil {
			break
		}
	}
	if e == nil {
		e = tx.Commit(ctx)
	} else {
		_ = tx.Rollback(ctx)
	}
	close(barrier.release)
	if e != nil {
		t.Fatal(e)
	}
	var during result
	select {
	case during = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("reader stuck")
	}
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(during.snapshot)
	if during.err != nil || !bytes.Equal(a, b) {
		t.Fatal("mixed generation/assignment snapshot", during.err)
	}
	after := modelSnapshot(t, f, at)
	if after.Snapshot.Assignment.Version != before.Snapshot.Assignment.Version+1 || after.Snapshot.DatasetRevision != before.Snapshot.DatasetRevision+1 || after.Snapshot.PhysiologicalRecovery.Value != nil {
		t.Fatal("new snapshot did not adopt complete new view")
	}
}
func TestAthleteModelRuntimeRoles(t *testing.T) {
	f := modelStackFixture(t)
	ctx := context.Background()
	suffix := strings.ReplaceAll(syncing.NewID(), "-", "")
	apiRole, workerRole := "rai_api_"+suffix, "rai_worker_"+suffix
	raw, e := os.ReadFile("../../../deploy/postgres/roles.sql")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Pool.Exec(ctx, strings.ReplaceAll(strings.ReplaceAll(string(raw), "rai_api", apiRole), "rai_worker", workerRole)); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if _, e := f.s.Pool.Exec(context.Background(), "DROP OWNED BY "+pgx.Identifier{apiRole}.Sanitize()+", "+pgx.Identifier{workerRole}.Sanitize()+"; DROP ROLE "+pgx.Identifier{apiRole}.Sanitize()+", "+pgx.Identifier{workerRole}.Sanitize()); e != nil {
			t.Error(e)
		}
	})
	for _, role := range []string{apiRole, workerRole} {
		t.Run(strings.Split(role, "_")[1], func(t *testing.T) {
			s, e := postgres.Open(ctx, f.s.Pool.Config().ConnString()+"&options=-crole%3D"+role, 2)
			if e != nil {
				t.Fatal(e)
			}
			defer s.Pool.Close()
			if e = s.RuntimeRoleSafe(ctx); e != nil {
				t.Fatal(e)
			}
			if _, _, e = s.ReadAthleteModelCandidateSnapshot(ctx, f.p.DatasetID, time.Now()); e == nil {
				t.Fatal("runtime got admin preview")
			}
			if _, e = s.ReadAthleteModelSnapshot(ctx, f.p, time.Now()); e != nil {
				t.Fatal("active reader denied", e)
			}
			if _, e = s.Pool.Exec(ctx, `UPDATE analysis_model_channels SET assignment_version=2`); e == nil {
				t.Fatal("runtime switched assignment")
			}
			if role == apiRole {
				if _, e = s.Pool.Exec(ctx, `SELECT payload FROM analysis_artifacts`); e == nil {
					t.Fatal("API raw candidate artifact access")
				}
			}
		})
	}
}

func TestAthleteModelPerformance(t *testing.T) {
	f := modelStackFixture(t)
	ctx := context.Background()
	at := time.Now()
	for _, scenario := range []string{"all_available", "mixed_freshness", "three_unavailable", "all_unavailable"} {
		if scenario == "three_unavailable" {
			if _, e := f.s.Pool.Exec(ctx, `DELETE FROM analysis_publications WHERE metric_family IN ('physiological_recovery_candidate','training_readiness_candidate','vo2max_external_candidate')`); e != nil {
				t.Fatal(e)
			}
		}
		if scenario == "all_unavailable" {
			if _, e := f.s.Pool.Exec(ctx, `DELETE FROM analysis_publications`); e != nil {
				t.Fatal(e)
			}
		}
		evaluation := at
		if scenario == "mixed_freshness" {
			evaluation = at.Add(40 * 24 * time.Hour)
		}
		var reads, read, fresh, mapns, serialization, bytesCount int64
		for range 10 {
			r, stats, e := f.s.ReadAthleteModelCandidateSnapshot(ctx, f.p.DatasetID, evaluation)
			if e != nil {
				t.Fatal(e)
			}
			started := time.Now()
			b, e := json.Marshal(r)
			if e != nil {
				t.Fatal(e)
			}
			serialization += time.Since(started).Nanoseconds()
			bytesCount = int64(len(b))
			reads += int64(stats.DBReads)
			read += stats.ReadNS
			fresh += stats.FreshenNS
			mapns += stats.MappingNS
		}
		allocs := testing.AllocsPerRun(3, func() {
			r, _, e := f.s.ReadAthleteModelCandidateSnapshot(ctx, f.p.DatasetID, evaluation)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = json.Marshal(r); e != nil {
				t.Fatal(e)
			}
		})
		t.Logf("M7 synthetic %s samples=10 dbReads=%d read_ns=%d freshen_ns=%d map_ns=%d serialization_ns=%d bytes=%d reader_plus_json_allocs=%.0f", scenario, reads/10, read/10, fresh/10, mapns/10, serialization/10, bytesCount, allocs)
	}
}
