//go:build integration

package integration

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
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
	"rai/server/migrations"
)

var analysisLog = slog.New(slog.NewJSONHandler(io.Discard, nil))

func analysisFixture(t *testing.T) (*fixture, syncing.Event) {
	t.Helper()
	f := setup(t)
	workoutStream(t, f)
	w := workoutAt(time.Now().UTC().Add(-2*time.Hour).Truncate(time.Second), 3600)
	w.Workout.TotalDistance = &syncing.Quantity{Value: 10000, Unit: "m"}
	f.commit(t, []syncing.Event{w}, 1, nil)
	return f, w
}
func claimAnalysis(t *testing.T, f *fixture, kind string) postgres.Lease {
	t.Helper()
	l, e := f.s.Claim(context.Background(), []string{kind})
	if e != nil {
		t.Fatal(e)
	}
	return l
}
func computeAnalysis(t *testing.T, f *fixture, l postgres.Lease) (analysis.Snapshot, string, []analysis.Metric) {
	t.Helper()
	ctx := context.Background()
	in, e := f.s.LoadAnalysis(ctx, l, time.Now().UTC())
	if e != nil {
		t.Fatal(e)
	}
	id, e := f.s.StartAnalysisRun(ctx, l, in)
	if e != nil {
		t.Fatal(e)
	}
	ms, e := analysis.Analyze(ctx, l.Kind, in)
	if e != nil {
		t.Fatal(e)
	}
	return in, id, ms
}
func runOne(t *testing.T, f *fixture, kind string) {
	t.Helper()
	l := claimAnalysis(t, f, kind)
	jobs.Process(context.Background(), f.s, l, analysisLog)
	var state, code string
	if e := f.s.Pool.QueryRow(context.Background(), `SELECT state,COALESCE(last_error_code,'') FROM jobs WHERE id=$1`, l.ID).Scan(&state, &code); e != nil || state != "succeeded" {
		t.Fatalf("handler %s state=%s code=%s error=%v", kind, state, code, e)
	}
}
func publishedMetric(v analysis.Current, key string) *float64 {
	for _, m := range v.Metrics {
		if m.Name == key {
			return m.Value
		}
	}
	return nil
}
func TestAnalysisPublishImmutableQueryAndDelete(t *testing.T) {
	f, w := analysisFixture(t)
	ctx := context.Background()
	before := businessSnapshot(t, f)
	runOne(t, f, "analysis.workout")
	v, e := f.s.CurrentWorkoutAnalysis(ctx, f.p, w.UUID)
	if e != nil {
		t.Fatal(e)
	}
	if *publishedMetric(v, "distance") != 10000 || publishedMetric(v, "workout_load") != nil {
		t.Fatal("feature or missing load incorrect")
	}
	// Analysis must not alter any raw, sync receipt, stream or change-log row.
	after := businessSnapshot(t, f)
	for table, rows := range before {
		if table == "jobs" || table == "dirty_scopes" {
			continue
		}
		if !bytes.Equal(after[table], rows) {
			t.Fatal("analysis changed raw business table", table)
		}
	}
	for _, stmt := range []string{`UPDATE analysis_runs SET algorithm_version='v2'`, `UPDATE analysis_results SET value=0`, `DELETE FROM analysis_runs`, `DELETE FROM analysis_results`} {
		if _, e = f.s.Pool.Exec(ctx, stmt); e == nil {
			t.Fatal("immutable history mutated")
		}
	}
	h := httpapi.New(f.s, analysisLog)
	r := httptest.NewRequest("GET", "/v2/analysis/workouts/"+w.UUID, nil)
	r.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
	out := httptest.NewRecorder()
	h.ServeHTTP(out, r)
	if out.Code != 200 {
		t.Fatal("analysis API", out.Code)
	}
	other := f.p
	other.DatasetID = syncing.NewID()
	if _, e = f.s.CurrentWorkoutAnalysis(ctx, other, w.UUID); e == nil {
		t.Fatal("cross dataset result exposed")
	}
	runOne(t, f, "analysis.training_state")
	v, e = f.s.CurrentTrainingState(ctx, f.p)
	if e != nil || publishedMetric(v, "readiness") != nil {
		t.Fatal("missing load fabricated readiness", e)
	}
	var batch string
	if e = f.s.Pool.QueryRow(ctx, `SELECT last_batch_id::text FROM sync_streams WHERE dataset_id=$1 AND id=$2`, f.p.DatasetID, f.stream.StreamID).Scan(&batch); e != nil {
		t.Fatal(e)
	}
	f.commit(t, []syncing.Event{workoutDelete(w.UUID)}, 2, &batch)
	if _, e = f.s.CurrentWorkoutAnalysis(ctx, f.p, w.UUID); e == nil {
		t.Fatal("deleted workout exposed")
	}
	if _, e = f.s.CurrentTrainingState(ctx, f.p); e == nil {
		t.Fatal("dirty state exposed")
	}
	runOne(t, f, "analysis.workout")
	if count(t, f, "analysis_publications") != 1 {
		t.Fatal("workout pointer not removed")
	}
}
func TestAnalysisGenerationCASAndLostFence(t *testing.T) {
	f, w := analysisFixture(t)
	ctx := context.Background()
	l := claimAnalysis(t, f, "analysis.workout")
	in, id, ms := computeAnalysis(t, f, l)
	// A true second transaction dirties the scope while the old worker calculates.
	if _, e := f.s.EnrichWorkout(ctx, f.p, syncing.WorkoutEnrichmentRequest{ProtocolVersion: 2, DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch, EnrichmentID: syncing.NewID(), WorkoutID: w.UUID, Source: &syncing.Source{BundleID: "synthetic.test"}, SourceVersion: 1, ObservedAtUs: *w.StartUs, Metadata: map[string]any{"synthetic": true}}); e != nil {
		t.Fatal(e)
	}
	status, e := f.s.PublishAnalysis(ctx, l, id, in, ms)
	if e != nil || status != "SUPERSEDED" {
		t.Fatal("stale generation published", status, e)
	}
	if count(t, f, "analysis_publications") != 0 {
		t.Fatal("stale pointer exists")
	}
	var dirty bool
	if e = f.s.Pool.QueryRow(ctx, `SELECT generation>clean_generation FROM dirty_scopes WHERE dataset_id=$1 AND scope_key=$2`, f.p.DatasetID, l.ScopeKey).Scan(&dirty); e != nil || !dirty {
		t.Fatal("new generation cleaned")
	}
	newer := claimAnalysis(t, f, "analysis.workout")
	newIn, newID, newMs := computeAnalysis(t, f, newer)
	if _, e = f.s.Pool.Exec(ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, newer.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.RequeueExpired(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Pool.Exec(ctx, `UPDATE jobs SET run_after=now() WHERE id=$1`, newer.ID); e != nil {
		t.Fatal(e)
	}
	recovered := claimAnalysis(t, f, "analysis.workout")
	if recovered.FencingToken <= newer.FencingToken {
		t.Fatal("token not advanced")
	}
	_, e = f.s.PublishAnalysis(ctx, newer, newID, newIn, newMs)
	requireCode(t, e, "lease_lost")
	jobs.Process(ctx, f.s, recovered, analysisLog)
	if _, e = f.s.CurrentWorkoutAnalysis(ctx, f.p, w.UUID); e != nil {
		t.Fatal("recovered worker could not publish", e)
	}
}
func TestAnalysisPublishFaultAtomicity(t *testing.T) {
	for _, table := range []string{"analysis_results", "analysis_publications", "dirty_scopes", "jobs", "job_attempts"} {
		t.Run(table, func(t *testing.T) {
			f, _ := analysisFixture(t)
			ctx := context.Background()
			l := claimAnalysis(t, f, "analysis.workout")
			in, id, ms := computeAnalysis(t, f, l)
			stmt := `CREATE FUNCTION p4_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic fault'; END $$; CREATE TRIGGER p4_fail BEFORE `
			op := "INSERT"
			if table == "dirty_scopes" || table == "jobs" || table == "job_attempts" {
				op = "UPDATE"
			}
			stmt += op + ` ON ` + table + ` FOR EACH ROW EXECUTE FUNCTION p4_fail()`
			if _, e := f.s.Pool.Exec(ctx, stmt); e != nil {
				t.Fatal(e)
			}
			if _, e := f.s.PublishAnalysis(ctx, l, id, in, ms); e == nil {
				t.Fatal("fault accepted")
			}
			if count(t, f, "analysis_results") != 0 || count(t, f, "analysis_publications") != 0 {
				t.Fatal("half publication escaped transaction")
			}
			var state, status string
			if e := f.s.Pool.QueryRow(ctx, `SELECT j.state,a.status FROM jobs j JOIN analysis_runs a ON a.job_id=j.id WHERE a.id=$1`, id).Scan(&state, &status); e != nil || state != "running" || status != "RUNNING" {
				t.Fatal("run/job escaped rollback", e)
			}
			if _, e := f.s.Pool.Exec(ctx, `DROP TRIGGER p4_fail ON `+table); e != nil {
				t.Fatal(e)
			}
			if _, e := f.s.PublishAnalysis(ctx, l, id, in, ms); e != nil {
				t.Fatal("retry failed", e)
			}
			// COMMIT before process death: succeeded job cannot be claimed/published again.
			if _, e := f.s.Claim(ctx, []string{"analysis.workout"}); !errors.Is(e, pgx.ErrNoRows) {
				t.Fatal("committed job reclaimed", e)
			}
		})
	}
}
func TestAnalysisLateSamplesRouteEnrichmentAndSeries(t *testing.T) {
	f, w := analysisFixture(t)
	ctx := context.Background()
	sampleStream := f.stream
	_ = sampleStream
	runOne(t, f, "analysis.workout")
	old, e := f.s.CurrentWorkoutAnalysis(ctx, f.p, w.UUID)
	if e != nil {
		t.Fatal(e)
	}
	// Register a separate HR stream, then use real P3 association, not time overlap.
	stream, e := f.s.RegisterStream(ctx, f.p, syncing.StreamRequest{DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch, StreamID: syncing.NewID(), SampleType: kind, QueryFingerprint: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"})
	if e != nil {
		t.Fatal(e)
	}
	f.stream = stream
	hr := sample(syncing.NewID(), 150)
	hr.StartUs = w.StartUs
	hr.EndUs = w.EndUs
	b, _, _ := f.commit(t, []syncing.Event{hr}, 1, nil)
	_, e = f.s.AssociateSample(ctx, f.p, syncing.AssociationRequest{ProtocolVersion: 2, DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch, AssociationID: syncing.NewID(), SampleType: kind, SampleUUID: hr.UUID, WorkoutUUID: w.UUID, Source: &syncing.Source{BundleID: "synthetic.test"}})
	if e != nil {
		t.Fatal(e)
	}
	if e = admin.SetAnalysisParameters(ctx, f.s, f.p.DatasetID, analysis.Parameters{RestHR: 50, MaxHR: 200, A: .64, B: 1.92, Provenance: "synthetic measured fixture"}); e != nil {
		t.Fatal(e)
	}
	runOne(t, f, "analysis.sample")
	runOne(t, f, "analysis.workout")
	v, e := f.s.CurrentWorkoutAnalysis(ctx, f.p, w.UUID)
	if e != nil || v.Generation <= old.Generation || publishedMetric(v, "workout_load") == nil {
		t.Fatal("late HR did not republish load", e)
	}
	// Pending route must never supply route features.
	route := syncing.NewID()
	start := time.UnixMicro(*w.StartUs)
	registerTestRoute(t, f, w.UUID, route, start, start.Add(time.Second), 1, 2)
	points := testRoutePoints(start, 2, 0)
	points[1].Longitude += .00001
	for i := range points {
		points[i].HorizontalAccuracy = analysis.Ptr(5)
	}
	uploadTestRouteChunk(t, f, route, 0, points)
	plan, e := admin.Recompute(ctx, f.s, admin.RecomputeRequest{Dataset: f.p.DatasetID, Scope: "workout:" + w.UUID, Algorithm: "workout.bundle/v1", From: start.In(time.FixedZone("UTC+8", 8*3600)).Format("2006-01-02"), Through: start.In(time.FixedZone("UTC+8", 8*3600)).Format("2006-01-02")})
	if e != nil || len(plan.Scopes) != 1 {
		t.Fatal("explicit recompute", e)
	}
	runOne(t, f, "analysis.workout")
	v, e = f.s.CurrentWorkoutAnalysis(ctx, f.p, w.UUID)
	if e != nil || publishedMetric(v, "route_distance") != nil {
		t.Fatal("pending route used", e)
	}
	m, p := routeStateChains(t, f, route)
	sealTestRoute(t, f, route, 1, 2, m, p)
	runOne(t, f, "analysis.workout")
	v, e = f.s.CurrentWorkoutAnalysis(ctx, f.p, w.UUID)
	if e != nil || publishedMetric(v, "route_distance") == nil {
		t.Fatal("late sealed route unused", e)
	}
	f.commit(t, []syncing.Event{deleted(hr.UUID)}, 2, &b.BatchID)
	runOne(t, f, "analysis.workout")
	v, e = f.s.CurrentWorkoutAnalysis(ctx, f.p, w.UUID)
	if e != nil || publishedMetric(v, "average_heart_rate") != nil {
		t.Fatal("sample deletion did not invalidate workout", e)
	}
}
func TestAnalysisSchema11UpgradeReadiness(t *testing.T) {
	f := setupWithMigration(t, func(ctx context.Context, url string) error {
		db, e := sql.Open("pgx", url)
		if e != nil {
			return e
		}
		defer db.Close()
		p, e := goose.NewProvider(goose.DialectPostgres, db, migrations.Files)
		if e != nil {
			return e
		}
		_, e = p.UpTo(ctx, 11)
		return e
	})
	ctx := context.Background()
	w := sample(syncing.NewID(), 120)
	batch, _, body := f.commit(t, []syncing.Event{w}, 1, nil)
	if e := f.s.Ready(ctx); e == nil {
		t.Fatal("new binary accepted schema 11")
	}
	if e := admin.Migrate(ctx, f.s.Pool.Config().ConnString()); e != nil {
		t.Fatal(e)
	}
	if e := f.s.Ready(ctx); e != nil {
		t.Fatal(e)
	}
	again, e := f.s.Commit(ctx, f.p, batch, []syncing.Event{w})
	if e != nil || string(again) != string(body) {
		t.Fatal("upgrade altered original receipt")
	}
}

func TestAnalysisSeriesSealAndFailureClassification(t *testing.T) {
	f, w := analysisFixture(t)
	ctx := context.Background()
	stream, e := f.s.RegisterStream(ctx, f.p, syncing.StreamRequest{DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch, StreamID: syncing.NewID(), SampleType: kind, QueryFingerprint: "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"})
	if e != nil {
		t.Fatal(e)
	}
	f.stream = stream
	hr := seriesParent(syncing.NewID(), *w.StartUs, *w.EndUs, 130)
	f.commit(t, []syncing.Event{hr}, 1, nil)
	if _, e = f.s.AssociateSample(ctx, f.p, syncing.AssociationRequest{ProtocolVersion: 2, DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch, AssociationID: syncing.NewID(), SampleType: kind, SampleUUID: hr.UUID, WorkoutUUID: w.UUID, Source: &syncing.Source{BundleID: "synthetic.test"}}); e != nil {
		t.Fatal(e)
	}
	series := syncing.NewID()
	if _, e = f.s.RegisterSeries(ctx, f.p, seriesRequest(f, series, hr.UUID)); e != nil {
		t.Fatal(e)
	}
	// A pending trace with a different mean is not yet an analysis input.
	r, _ := commitSeries(t, f, series, 0, []syncing.SeriesPoint{{StartUs: *w.StartUs, EndUs: *w.EndUs, Value: 160, Unit: "count/min"}})
	runOne(t, f, "analysis.workout")
	v, e := f.s.CurrentWorkoutAnalysis(ctx, f.p, w.UUID)
	if e != nil || *publishedMetric(v, "average_heart_rate") != 130 {
		t.Fatal("pending series used", e)
	}
	if _, e = f.s.SealSeries(ctx, f.p, series, syncing.SeriesSeal{ChunkCount: 1, TotalPoints: 1, ManifestSHA256: r.ManifestSHA256}); e != nil {
		t.Fatal(e)
	}
	runOne(t, f, "analysis.workout")
	v, e = f.s.CurrentWorkoutAnalysis(ctx, f.p, w.UUID)
	if e != nil || *publishedMetric(v, "average_heart_rate") != 160 {
		t.Fatal("sealed series failed to replace parent summary", e)
	}
	// Unsupported algorithm is a permanent stable error, no endless retries.
	if _, e = f.s.Pool.Exec(ctx, `SELECT rai_analysis_dirty($1,$2,'analysis.workout',$3,$4)`, f.p.DatasetID, "workout:"+syncing.WorkoutType+":"+w.UUID, time.UnixMicro(*w.StartUs), time.UnixMicro(*w.EndUs)); e != nil {
		t.Fatal(e)
	}
	l := claimAnalysis(t, f, "analysis.workout")
	l.Payload = []byte(`{"algorithm":"workout.bundle/v999"}`)
	jobs.Process(ctx, f.s, l, analysisLog)
	var state, code string
	if e = f.s.Pool.QueryRow(ctx, `SELECT state,last_error_code FROM jobs WHERE id=$1`, l.ID).Scan(&state, &code); e != nil || state != "failed" || code != "analysis_unsupported_algorithm" {
		t.Fatal("permanent failure not classified", state, code, e)
	}
	// Retryable dependency failure remains durable and bounded by existing queue.
	stateLease := claimAnalysis(t, f, "analysis.training_state")
	jobs.Process(ctx, f.s, stateLease, analysisLog)
	if e = f.s.Pool.QueryRow(ctx, `SELECT state,last_error_code FROM jobs WHERE id=$1`, stateLease.ID).Scan(&state, &code); e != nil || state != "queued" || code != "analysis_dependency_pending" {
		t.Fatal("transient dependency failure not classified", state, code, e)
	}
}
func TestAnalysisLeaseExpiryDuringPublishAndRecomputeDryRun(t *testing.T) {
	f, w := analysisFixture(t)
	ctx := context.Background()
	l := claimAnalysis(t, f, "analysis.workout")
	in, id, ms := computeAnalysis(t, f, l)
	if _, e := f.s.Pool.Exec(ctx, `UPDATE jobs SET lease_until=clock_timestamp()+interval '100 milliseconds' WHERE id=$1`, l.ID); e != nil {
		t.Fatal(e)
	}
	if _, e := f.s.Pool.Exec(ctx, `CREATE FUNCTION p4_pause() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_sleep(0.2);RETURN NEW; END $$;CREATE TRIGGER p4_pause BEFORE UPDATE ON job_attempts FOR EACH ROW EXECUTE FUNCTION p4_pause()`); e != nil {
		t.Fatal(e)
	}
	_, e := f.s.PublishAnalysis(ctx, l, id, in, ms)
	requireCode(t, e, "lease_lost")
	if count(t, f, "analysis_results") != 0 || count(t, f, "analysis_publications") != 0 {
		t.Fatal("expired during transaction escaped rollback")
	}
	loc, _ := time.LoadLocation("Asia/Shanghai")
	day := time.UnixMicro(*w.StartUs).In(loc).Format("2006-01-02")
	before := count(t, f, "jobs")
	p, e := admin.Recompute(ctx, f.s, admin.RecomputeRequest{Dataset: f.p.DatasetID, Scope: "workouts", Algorithm: "workout.bundle/v1", From: day, Through: day, DryRun: true})
	if e != nil || len(p.Scopes) != 1 || count(t, f, "jobs") != before {
		t.Fatal("dry-run mutated queue", e)
	}
	for _, r := range []admin.RecomputeRequest{{Dataset: f.p.DatasetID, Scope: "workouts", Algorithm: "workout.bundle/v1"}, {Dataset: f.p.DatasetID, Scope: "workouts", Algorithm: "workout.bundle/v999", From: day, Through: day}} {
		if _, e = admin.Recompute(ctx, f.s, r); e == nil {
			t.Fatal("unbounded/unsupported recompute accepted")
		}
	}
}
func TestAnalysisActualWorkerLoopAndDayRollover(t *testing.T) {
	f, w := analysisFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- jobs.Run(ctx, f.s, analysisLog) }()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(25 * time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-deadline.C:
			cancel()
			<-done
			t.Fatal("real worker loop did not publish")
		case <-poll.C:
			if _, e := f.s.CurrentTrainingState(context.Background(), f.p); e == nil {
				if _, e = f.s.CurrentWorkoutAnalysis(context.Background(), f.p, w.UUID); e != nil {
					continue
				}
				cancel()
				if e = <-done; e != nil {
					t.Fatal(e)
				}
				goto published
			}
		}
	}
published:
	// If canceled before the state claim, finish it through the same handler.
	var n int
	if e := f.s.Pool.QueryRow(context.Background(), `SELECT count(*) FROM jobs WHERE kind='analysis.training_state' AND state='queued'`).Scan(&n); e != nil {
		t.Fatal(e)
	}
	if n > 0 {
		runOne(t, f, "analysis.training_state")
	}
	if e := f.s.AdvanceAnalysisDay(context.Background(), time.Now().UTC().Add(24*time.Hour)); e != nil {
		t.Fatal(e)
	}
	var before, after int64
	if e := f.s.Pool.QueryRow(context.Background(), `SELECT generation FROM dirty_scopes WHERE dataset_id=$1 AND scope_key='athlete/default'`, f.p.DatasetID).Scan(&before); e != nil {
		t.Fatal(e)
	}
	if e := f.s.AdvanceAnalysisDay(context.Background(), time.Now().UTC().Add(24*time.Hour)); e != nil {
		t.Fatal(e)
	}
	if e := f.s.Pool.QueryRow(context.Background(), `SELECT generation FROM dirty_scopes WHERE dataset_id=$1 AND scope_key='athlete/default'`, f.p.DatasetID).Scan(&after); e != nil || before != after {
		t.Fatal("rollover produced job storm", e)
	}
}

func TestAnalysisExplicitRangeCannotCleanUncoveredDirtyDays(t *testing.T) {
	f, _ := analysisFixture(t)
	ctx := context.Background()
	runOne(t, f, "analysis.workout")
	runOne(t, f, "analysis.training_state")
	loc, _ := time.LoadLocation("Asia/Shanghai")
	today := time.Now().In(loc)
	from := today.AddDate(0, 0, -20)
	if _, e := f.s.Pool.Exec(ctx, `SELECT rai_analysis_dirty($1,'athlete/default','analysis.training_state',$2,$3)`, f.p.DatasetID, from, today); e != nil {
		t.Fatal(e)
	}
	r := admin.RecomputeRequest{Dataset: f.p.DatasetID, Scope: analysis.StateScope, Algorithm: "training_state.bundle/v1", From: today.AddDate(0, 0, -10).Format("2006-01-02"), Through: today.Format("2006-01-02")}
	before := count(t, f, "jobs")
	if _, e := admin.Recompute(ctx, f.s, r); e == nil || count(t, f, "jobs") != before {
		t.Fatal("partial explicit range could clean uncovered dirty history")
	}
	r.From = from.Format("2006-01-02")
	if _, e := admin.Recompute(ctx, f.s, r); e != nil {
		t.Fatal(e)
	}
	runOne(t, f, "analysis.training_state")
}

func TestAnalysisCLIExplicitDryRun(t *testing.T) {
	f, _ := analysisFixture(t)
	var out bytes.Buffer
	args := []string{"recompute", "--dataset", f.p.DatasetID, "--scope", "workouts", "--algorithm", "workout.bundle/v1", "--from", "2020-01-01", "--through", "2020-01-02", "--dry-run"}
	before := count(t, f, "jobs")
	if e := admin.AnalysisCommand(context.Background(), f.s.Pool.Config().ConnString(), args, &out); e != nil {
		t.Fatal(e)
	}
	if count(t, f, "jobs") != before || !bytes.Contains(out.Bytes(), []byte(`"dryRun":true`)) {
		t.Fatal("CLI dry-run changed durable jobs")
	}
}

func TestAnalysisWorkerRoleBoundary(t *testing.T) {
	f, w := analysisFixture(t)
	ctx := context.Background()
	suffix := strings.ReplaceAll(syncing.NewID(), "-", "")
	apiRole, workerRole := "rai_api_"+suffix, "rai_worker_"+suffix
	b, e := os.ReadFile("../../../deploy/postgres/roles.sql")
	if e != nil {
		t.Fatal(e)
	}
	sqlText := strings.ReplaceAll(strings.ReplaceAll(string(b), "rai_api", apiRole), "rai_worker", workerRole)
	if _, e = f.s.Pool.Exec(ctx, sqlText); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_, e := f.s.Pool.Exec(context.Background(), "DROP OWNED BY "+pgx.Identifier{apiRole}.Sanitize()+", "+pgx.Identifier{workerRole}.Sanitize()+"; DROP ROLE "+pgx.Identifier{apiRole}.Sanitize()+", "+pgx.Identifier{workerRole}.Sanitize())
		if e != nil {
			t.Error(e)
		}
	})
	openRole := func(role string) *postgres.Store {
		u, e := url.Parse(f.s.Pool.Config().ConnString())
		if e != nil {
			t.Fatal(e)
		}
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
	worker := openRole(workerRole)
	api := openRole(apiRole)
	for _, statement := range []string{`UPDATE health_samples SET value_numeric=0`, `UPDATE workouts SET payload='{}'`, `UPDATE route_points SET altitude=0`} {
		if _, e = worker.Pool.Exec(ctx, statement); e == nil {
			t.Fatal("Worker can mutate raw fact")
		}
	}
	for _, statement := range []string{`DELETE FROM analysis_publications`, `UPDATE analysis_runs SET status='FAILED'`, `INSERT INTO analysis_parameter_sets(dataset_id,version) VALUES(NULL,1)`} {
		if _, e = api.Pool.Exec(ctx, statement); e == nil {
			t.Fatal("API can write derived/admin data")
		}
	}
	l, e := worker.Claim(ctx, []string{"analysis.workout"})
	if e != nil {
		t.Fatal(e)
	}
	jobs.Process(ctx, worker, l, analysisLog)
	if _, e = api.CurrentWorkoutAnalysis(ctx, f.p, w.UUID); e != nil {
		t.Fatal("restricted Worker publication/API read failed", e)
	}
	l, e = worker.Claim(ctx, []string{"analysis.training_state"})
	if e != nil {
		t.Fatal(e)
	}
	jobs.Process(ctx, worker, l, analysisLog)
	if _, e = api.CurrentTrainingState(ctx, f.p); e != nil {
		t.Fatal("restricted state publication/API read failed", e)
	}
}

func TestAnalysisOldGenerationCannotReplaceNewPublication(t *testing.T) {
	f, w := analysisFixture(t)
	ctx := context.Background()
	old := claimAnalysis(t, f, "analysis.workout")
	in, id, ms := computeAnalysis(t, f, old)
	if _, e := f.s.Pool.Exec(ctx, `SELECT rai_analysis_dirty($1,$2,'analysis.workout',$3,$4)`, f.p.DatasetID, old.ScopeKey, in.From, in.Through); e != nil {
		t.Fatal(e)
	}
	runOne(t, f, "analysis.workout")
	newer, e := f.s.CurrentWorkoutAnalysis(ctx, f.p, w.UUID)
	if e != nil {
		t.Fatal(e)
	}
	status, e := f.s.PublishAnalysis(ctx, old, id, in, ms)
	if e != nil || status != "SUPERSEDED" {
		t.Fatal("old run not superseded", e)
	}
	after, e := f.s.CurrentWorkoutAnalysis(ctx, f.p, w.UUID)
	if e != nil || after.RunID != newer.RunID || after.Generation != newer.Generation {
		t.Fatal("old run replaced current publication", e)
	}
}
