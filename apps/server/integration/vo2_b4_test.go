//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/admin"
	"rai/server/internal/httpapi"
	"rai/server/internal/jobs"
	pf "rai/server/internal/performance"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
	"rai/server/internal/training"
	tl "rai/server/internal/trainingload"
	ve "rai/server/internal/vo2maxexternal"
	wf "rai/server/internal/workoutfeatures"
)

// Every HTTP transport uses the runtime API role; candidate control/read uses
// owner/admin, and real Claim/Process uses the runtime Worker role.
func b4Roles(t *testing.T, f *fixture) (*postgres.Store, *postgres.Store) {
	t.Helper()
	ctx := context.Background()
	suffix := strings.ReplaceAll(syncing.NewID(), "-", "")
	apiRole, workerRole := "rai_api_"+suffix, "rai_worker_"+suffix
	raw, err := os.ReadFile("../../../deploy/postgres/roles.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.Pool.Exec(ctx, strings.ReplaceAll(strings.ReplaceAll(string(raw), "rai_api", apiRole), "rai_worker", workerRole)); err != nil {
		t.Fatal(err)
	}
	apiLogin, workerLogin := "b4_api_login_"+suffix, "b4_worker_login_"+suffix
	if _, err = f.s.Pool.Exec(ctx, "CREATE ROLE "+pgx.Identifier{apiLogin}.Sanitize()+" LOGIN IN ROLE "+pgx.Identifier{apiRole}.Sanitize()+"; CREATE ROLE "+pgx.Identifier{workerLogin}.Sanitize()+" LOGIN IN ROLE "+pgx.Identifier{workerRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, e := f.s.Pool.Exec(context.Background(), "DROP OWNED BY "+pgx.Identifier{apiRole}.Sanitize()+", "+pgx.Identifier{workerRole}.Sanitize()+"; DROP ROLE "+pgx.Identifier{apiLogin}.Sanitize()+", "+pgx.Identifier{workerLogin}.Sanitize()+", "+pgx.Identifier{apiRole}.Sanitize()+", "+pgx.Identifier{workerRole}.Sanitize()); e != nil {
			t.Error(e)
		}
	})
	open := func(role string) *postgres.Store {
		uri, e := url.Parse(f.s.Pool.Config().ConnString())
		if e != nil {
			t.Fatal(e)
		}
		uri.User = url.User(role)
		query := uri.Query()
		query.Del("options")
		uri.RawQuery = query.Encode()
		s, e := postgres.Open(ctx, uri.String(), 3)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(s.Pool.Close)
		if e = s.Ready(ctx); e != nil {
			t.Fatal("schema readiness", e)
		}
		var realLogin bool
		if e = s.Pool.QueryRow(ctx, `SELECT session_user=current_user AND current_user=$1`, role).Scan(&realLogin); e != nil || !realLogin {
			t.Fatal("runtime must authenticate as its own LOGIN", e)
		}
		if e = s.RuntimeRoleSafe(ctx); e != nil {
			t.Fatal(e)
		}
		return s
	}
	return open(apiLogin), open(workerLogin)
}
func b4Worker(t *testing.T, f *fixture, worker *postgres.Store) {
	t.Helper()
	ctx := context.Background()
	start := time.Now()
	l, e := worker.Claim(ctx, []string{ve.JobKind})
	if e != nil {
		t.Fatal(e)
	}
	var logs bytes.Buffer
	jobs.Process(ctx, worker, l, slog.New(slog.NewJSONHandler(&logs, nil)))
	if regexp.MustCompile(`[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}`).Match(logs.Bytes()) {
		t.Fatal("Worker logs contain full UUID")
	}
	for _, line := range bytes.Split(logs.Bytes(), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var row map[string]any
		if json.Unmarshal(line, &row) != nil {
			t.Fatal("log JSON")
		}
		if row["msg"] == "analysis_execution" {
			t.Logf("B4_WORKER_TIMING snapshot_ms=%v projection_ms=%v publication_ms=%v", row["snapshot_ms"], row["algorithm_ms"], row["publication_ms"])
		}
	}
	var state string
	if e = f.s.Pool.QueryRow(ctx, `SELECT state FROM jobs WHERE id=$1`, l.ID).Scan(&state); e != nil || state != "succeeded" {
		t.Fatal("worker terminal", state, e)
	}
	t.Logf("B4_TIMING phase=worker_claim_through_publication duration_us=%d", time.Since(start).Microseconds())
}
func b4Post(t *testing.T, f *fixture, url string, b syncing.VO2Batch) []byte {
	t.Helper()
	raw, e := json.Marshal(b)
	if e != nil {
		t.Fatal(e)
	}
	req, e := http.NewRequest("POST", url+"/v2/sync/vo2max-facts", bytes.NewReader(raw))
	if e != nil {
		t.Fatal(e)
	}
	req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
	req.Header.Set("Content-Type", "application/json")
	start := time.Now()
	var ack []byte
	for attempt := 0; attempt < 30; attempt++ {
		request := req.Clone(context.Background())
		request.Body = io.NopCloser(bytes.NewReader(raw))
		res, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal("HTTP failure")
		}
		ack, err = io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			t.Fatal("HTTP body failure")
		}
		if res.StatusCode == 200 {
			break
		}
		if res.StatusCode != 429 || attempt == 29 {
			t.Fatal("HTTP status", res.StatusCode)
		}
		time.Sleep(time.Second)
	}
	t.Logf("B4_TIMING phase=http_ack count=%d duration_us=%d", len(b.Facts)+len(b.DeletedUUIDs), time.Since(start).Microseconds())
	return ack
}
func b4Enroll(t *testing.T, f *fixture, epoch string, at int64) ve.Selection {
	t.Helper()
	ctx := context.Background()
	sel := ve.Selection{ID: syncing.NewID(), Epoch: epoch, EffectiveFrom: at}
	if e := admin.SetVO2SourceSelection(ctx, f.s, f.p.DatasetID, syncing.NewID(), "B4 synthetic", sel); e != nil {
		t.Fatal(e)
	}
	if e := admin.QueueVO2ExternalCandidate(ctx, f.s, f.p.DatasetID, syncing.NewID(), "B4 synthetic"); e != nil {
		t.Fatal(e)
	}
	return sel
}
func b4Exact(t *testing.T, f *fixture, value float64) ve.Result {
	t.Helper()
	r, id := externalRead(t, f, "available", &value)
	ctx := context.Background()
	var raw, result, artifact float64
	if e := f.s.Pool.QueryRow(ctx, `SELECT h.value_numeric,r.value,(a.payload->>'value')::double precision FROM health_samples h JOIN analysis_results r ON r.dataset_id=h.dataset_id AND r.run_id=$2 AND r.metric=$3 JOIN analysis_artifacts a ON a.dataset_id=r.dataset_id AND a.run_id=r.run_id WHERE h.dataset_id=$1 AND h.sample_type=$4 ORDER BY h.event_end DESC LIMIT 1`, f.p.DatasetID, id, ve.Metric, syncing.VO2Type).Scan(&raw, &result, &artifact); e != nil {
		t.Fatal(e)
	}
	for _, v := range []float64{raw, result, artifact, *r.Value} {
		if math.Float64bits(v) != math.Float64bits(value) {
			t.Fatal("binary64 preservation failed")
		}
	}
	if r.Method != "external_estimate" || r.Provider != "apple_health" || r.Confidence != nil || r.Quality != "partial" {
		t.Fatal("external identity")
	}
	return r
}
func TestB4HTTPExactFreshnessDeleteReplay(t *testing.T) {
	f := setup(t)
	api, worker := b4Roles(t, f)
	server := httptest.NewServer(httpapi.New(api, analysisLog))
	defer server.Close()
	b := externalBatch(t, f)
	b.Facts[0].Quantity.Value = 47.123456789012344
	ack := b4Post(t, f, server.URL, b)
	b4Enroll(t, f, b.Facts[0].Provenance.EpochID(), b.FlowCreatedAtUs)
	b4Worker(t, f, worker)
	r := b4Exact(t, f, b.Facts[0].Quantity.Value)
	for _, tt := range []struct {
		age    int64
		status string
		value  bool
	}{{30*ve.DayUs - 1, "available", true}, {30 * ve.DayUs, "stale", true}, {90*ve.DayUs - 1, "stale", true}, {90 * ve.DayUs, "expired", false}} {
		got, _, e := f.s.CandidateVO2External(context.Background(), f.p.DatasetID, time.UnixMicro(r.Selected.End+tt.age))
		if e != nil || got.Status != tt.status || (got.Value != nil) != tt.value {
			t.Fatal("reader freshness boundary", tt.status, e)
		}
		if got.Value != nil && math.Float64bits(*got.Value) != math.Float64bits(b.Facts[0].Quantity.Value) {
			t.Fatal("freshness changed numeric value")
		}
	}
	for i := 0; i < 3; i++ {
		if !bytes.Equal(ack, b4Post(t, f, server.URL, b)) {
			t.Fatal("receipt replay changed")
		}
	}
	if count(t, f, "analysis_artifacts") != 1 || count(t, f, "analysis_publications") != 1 {
		t.Fatal("replay duplicated projection")
	}
	d := nextVO2(b)
	d.DeletedUUIDs = []string{b.Facts[0].UUID}
	b4Post(t, f, server.URL, d)
	if _, _, e := f.s.CandidateVO2External(context.Background(), f.p.DatasetID, time.Now()); e == nil {
		t.Fatal("delete did not immediately invalidate")
	}
	b4Worker(t, f, worker)
	externalRead(t, f, "deleted", nil)
	c := nextVO2(d)
	c.Facts = b.Facts
	b4Post(t, f, server.URL, c)
	b4Worker(t, f, worker)
	externalRead(t, f, "deleted", nil)
	if count(t, f, "health_samples") != 0 {
		t.Fatal("stale create resurrected UUID")
	}
}
func TestB4HTTPDeleteWithPredecessor(t *testing.T) {
	for _, days := range []int64{60, 91} {
		t.Run(time.Duration(days*24*time.Hour.Nanoseconds()).String(), func(t *testing.T) {
			f := setup(t)
			api, worker := b4Roles(t, f)
			server := httptest.NewServer(httpapi.New(api, analysisLog))
			defer server.Close()
			b := externalBatch(t, f)
			b.FlowCreatedAtUs -= 3 * ve.DayUs
			b.BootstrapStartUs = b.FlowCreatedAtUs - syncing.VO2WindowUs
			b.QueryFingerprint = syncing.VO2Fingerprint(b.BootstrapStartUs)
			old := b.Facts[0]
			old.UUID = syncing.NewID()
			old.EndUs = time.Now().UnixMicro() - days*ve.DayUs
			old.StartUs = old.EndUs
			b.Facts = append(b.Facts, old)
			b4Post(t, f, server.URL, b)
			b4Enroll(t, f, old.Provenance.EpochID(), b.FlowCreatedAtUs)
			b4Worker(t, f, worker)
			d := nextVO2(b)
			d.DeletedUUIDs = []string{b.Facts[0].UUID}
			b4Post(t, f, server.URL, d)
			if _, _, e := f.s.CandidateVO2External(context.Background(), f.p.DatasetID, time.Now()); e == nil {
				t.Fatal("delete remained current")
			}
			b4Worker(t, f, worker)
			if days == 60 {
				v := old.Quantity.Value
				externalRead(t, f, "stale", &v)
			} else {
				externalRead(t, f, "deleted", nil)
			}
		})
	}
}
func TestB4ProductHTTPAndGenericReadIsolation(t *testing.T) {
	f, _, _ := externalFixture(t)
	api, worker := b4Roles(t, f)
	b4Worker(t, f, worker)
	w := m3Bundle(t, f, time.Now().Add(-48*time.Hour), 1200)
	importPhase3(t, f, w)
	plan, e := f.s.CreateTrainingPlan(context.Background(), f.p, training.CreateRequest{CommandID: syncing.NewID(), PlanID: syncing.NewID(), Config: trainingConfig()})
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(httpapi.New(api, analysisLog))
	defer server.Close()
	paths := []string{"/v2/product/overview", "/v2/analysis/history?days=14", "/v2/workouts", "/v2/workouts/" + w.WorkoutID, "/v2/workouts/" + w.WorkoutID + "/detail", "/v2/analysis/workouts/" + w.WorkoutID, "/v2/analysis/state", "/v2/training/plans/current", "/v2/training/plans/" + plan.ID, "/v2/training/plans/" + plan.ID + "/history"}
	for _, path := range paths {
		req, _ := http.NewRequest("GET", server.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
		res, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal("product HTTP failed")
		}
		body, e := io.ReadAll(res.Body)
		res.Body.Close()
		if e != nil || (res.StatusCode != 200 && res.StatusCode != 404) {
			t.Fatal("product read failed", res.StatusCode)
		}
		for _, key := range []string{ve.Model, ve.Metric, ve.Family, ve.ArtifactKey} {
			if bytes.Contains(body, []byte(key)) {
				t.Fatal("candidate leaked via product HTTP")
			}
		}
	}
	// No public selection/control route. A fabricated metrics/latest query cannot
	// escape the existing explicit legacy family filters.
	for _, path := range []string{"/v2/analysis/latest?metric=" + ve.Metric, "/v2/metrics?metric=" + ve.Metric, "/v2/analysis/vo2max-external/select"} {
		method := "GET"
		if strings.Contains(path, "/select") {
			method = "POST"
		}
		req, _ := http.NewRequest(method, server.URL+path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
		res, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		res.Body.Close()
		if res.StatusCode != 404 && res.StatusCode != 405 {
			t.Fatal("unexpected public control route", res.StatusCode)
		}
	}
	var active string
	if e = f.s.Pool.QueryRow(context.Background(), `SELECT stack_id FROM analysis_model_channels WHERE dataset_id=$1 AND channel='active'`, f.p.DatasetID).Scan(&active); e != nil || active != "legacy" {
		t.Fatal("active changed")
	}
	var out bytes.Buffer
	if e = admin.VO2ExternalCommand(context.Background(), "", []string{"promotion-plan"}, &out); e != nil {
		t.Fatal(e)
	}
	for _, v := range []string{`"promotable":false`, "product_not_activated", "real_device_acceptance_deferred"} {
		if !bytes.Contains(out.Bytes(), []byte(v)) {
			t.Fatal("promotion plan")
		}
	}
}

func TestB4BidirectionalModelIndependence(t *testing.T) {
	f, b, _ := externalFixture(t)
	ctx := context.Background()
	runOne(t, f, ve.JobKind)
	value := b.Facts[0].Quantity.Value
	r, id := externalRead(t, f, "available", &value)
	w := m3Bundle(t, f, time.Now().Add(-time.Hour), 1200)
	importPhase3(t, f, w)
	through := *w.Workout.EndUs + 3600e6
	p := wf.Parameter{ID: syncing.NewID(), EffectiveFrom: *w.Workout.StartUs - 3600e6, EffectiveThrough: &through, RestHR: 50, MaxHR: 200, Provenance: "B4 synthetic HR"}
	if e := admin.SetFeatureHRReference(ctx, f.s, f.p.DatasetID, p); e != nil {
		t.Fatal(e)
	}
	if e := admin.QueueFeatureCandidate(ctx, f.s, f.p.DatasetID, w.WorkoutID, syncing.NewID(), "B4 synthetic M1"); e != nil {
		t.Fatal(e)
	}
	if e := admin.QueueTrainingLoadCandidate(ctx, f.s, f.p.DatasetID, w.WorkoutID, syncing.NewID(), "B4 synthetic M2"); e != nil {
		t.Fatal(e)
	}
	if e := admin.QueuePerformanceCandidate(ctx, f.s, f.p.DatasetID, syncing.NewID(), "B4 synthetic M3"); e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{wf.JobKind, tl.JobKind, tl.StateJobKind, pf.JobKind} {
		runOne(t, f, kind)
	}
	got, gotID := externalRead(t, f, "available", &value)
	if gotID != id || got.Generation != r.Generation {
		t.Fatal("M1/M2/M3 recomputed external")
	}
	var externalJobs int
	if e := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE dataset_id=$1 AND kind=$2`, f.p.DatasetID, ve.JobKind).Scan(&externalJobs); e != nil || externalJobs != 1 {
		t.Fatal("external jobs polluted", e)
	}
	snapshot := func() string {
		var h string
		if e := f.s.Pool.QueryRow(ctx, `SELECT md5(COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x.id)::text,'')) FROM (SELECT id,kind,target_generation,state,payload FROM jobs WHERE dataset_id=$1 AND kind=ANY($2)) x`, f.p.DatasetID, []string{wf.JobKind, tl.JobKind, tl.StateJobKind, pf.JobKind}).Scan(&h); e != nil {
			t.Fatal(e)
		}
		return h
	}
	before := snapshot()
	d := nextVO2(b)
	x := b.Facts[0]
	x.UUID = syncing.NewID()
	x.EndUs++
	x.StartUs = x.EndUs
	d.Facts = []syncing.VO2Fact{x}
	requireVO2(t, f, d)
	d = nextVO2(d)
	d.DeletedUUIDs = []string{x.UUID}
	requireVO2(t, f, d)
	if snapshot() != before {
		t.Fatal("external raw changed M1/M2/M3 jobs")
	}
	runOne(t, f, ve.JobKind)
	externalRead(t, f, "available", &value)
}

func TestB4HTTPRawFaultToRecoveredCandidate(t *testing.T) {
	for _, table := range []string{"health_samples", "entity_tombstones", "daily_health_coverage", "sync_batches"} {
		t.Run(table, func(t *testing.T) {
			f := setup(t)
			api, worker := b4Roles(t, f)
			ctx := context.Background()
			b := externalBatch(t, f)
			b.DeletedUUIDs = []string{syncing.NewID()}
			b4Enroll(t, f, b.Facts[0].Provenance.EpochID(), b.FlowCreatedAtUs)
			op := "INSERT"
			if table == "sync_batches" {
				op = "UPDATE"
			}
			if _, e := f.s.Pool.Exec(ctx, `CREATE FUNCTION b4_raw_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic_b4_fault'; END $$; CREATE TRIGGER b4_raw_fault AFTER `+op+` ON `+table+` FOR EACH ROW EXECUTE FUNCTION b4_raw_fault()`); e != nil {
				t.Fatal(e)
			}
			server := httptest.NewServer(httpapi.New(api, analysisLog))
			defer server.Close()
			raw, _ := json.Marshal(b)
			req, _ := http.NewRequest("POST", server.URL+"/v2/sync/vo2max-facts", bytes.NewReader(raw))
			req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
			req.Header.Set("Content-Type", "application/json")
			res, e := http.DefaultClient.Do(req)
			if e != nil {
				t.Fatal(e)
			}
			res.Body.Close()
			if res.StatusCode < 500 {
				t.Fatal("fault not exercised", res.StatusCode)
			}
			for _, tab := range []string{"health_samples", "entity_tombstones", "daily_health_coverage", "sync_batches", "analysis_artifacts", "analysis_publications"} {
				if count(t, f, tab) != 0 {
					t.Fatal("raw fault escaped transaction", tab)
				}
			}
			if _, e = f.s.Pool.Exec(ctx, `DROP TRIGGER b4_raw_fault ON `+table); e != nil {
				t.Fatal(e)
			}
			b4Post(t, f, server.URL, b)
			b4Worker(t, f, worker)
			value := b.Facts[0].Quantity.Value
			externalRead(t, f, "available", &value)
		})
	}
}

func TestB4HTTPWorkerBackendDeathAndReclaim(t *testing.T) {
	f := setup(t)
	api, worker := b4Roles(t, f)
	ctx := context.Background()
	server := httptest.NewServer(httpapi.New(api, analysisLog))
	defer server.Close()
	b := externalBatch(t, f)
	b4Post(t, f, server.URL, b)
	b4Enroll(t, f, b.Facts[0].Provenance.EpochID(), b.FlowCreatedAtUs)
	blocker, e := f.s.Pool.Acquire(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer blocker.Release()
	var blockerPID int
	if e = blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); e != nil {
		t.Fatal(e)
	}
	if _, e = blocker.Exec(ctx, `SELECT pg_advisory_lock(91725004)`); e != nil {
		t.Fatal(e)
	}
	defer blocker.Exec(ctx, `SELECT pg_advisory_unlock_all()`)
	if _, e = f.s.Pool.Exec(ctx, `CREATE FUNCTION b4_worker_barrier() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(91725004); RETURN NEW; END $$; CREATE TRIGGER b4_worker_barrier BEFORE INSERT ON analysis_results FOR EACH ROW EXECUTE FUNCTION b4_worker_barrier()`); e != nil {
		t.Fatal(e)
	}
	l, e := worker.Claim(ctx, []string{ve.JobKind})
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan struct{})
	go func() { jobs.Process(ctx, worker, l, analysisLog); close(done) }()
	pid := 0
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if e = f.s.Pool.QueryRow(ctx, `SELECT COALESCE((SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)) AND wait_event_type='Lock' LIMIT 1),0)`, blockerPID).Scan(&pid); e != nil {
			t.Fatal(e)
		}
		if pid > 0 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("worker failed to reach post-artifact barrier")
	}
	var killed bool
	if e = f.s.Pool.QueryRow(ctx, `SELECT pg_terminate_backend($1)`, pid).Scan(&killed); e != nil || !killed {
		t.Fatal("backend termination", e)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("dead worker did not finish")
	}
	for _, table := range []string{"analysis_artifacts", "analysis_results", "analysis_publications"} {
		if count(t, f, table) != 0 {
			t.Fatal("partial worker publication", table)
		}
	}
	if _, e = f.s.Pool.Exec(ctx, `DROP TRIGGER b4_worker_barrier ON analysis_results; UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE state='running'`); e != nil {
		t.Fatal(e)
	}
	if _, e = worker.RequeueExpired(ctx); e != nil {
		t.Fatal(e)
	}
	// Deterministically advance only retry scheduling; no raw/publication patch.
	if _, e = f.s.Pool.Exec(ctx, `UPDATE jobs SET run_after=clock_timestamp() WHERE state='queued'`); e != nil {
		t.Fatal(e)
	}
	if _, e = blocker.Exec(ctx, `SELECT pg_advisory_unlock_all()`); e != nil {
		t.Fatal(e)
	}
	b4Worker(t, f, worker)
	value := b.Facts[0].Quantity.Value
	externalRead(t, f, "available", &value)
	if count(t, f, "analysis_artifacts") != 1 || count(t, f, "analysis_publications") != 1 {
		t.Fatal("reclaim duplicated publication")
	}
}

func TestB4APILogPrivacy(t *testing.T) {
	f := setup(t)
	api, _ := b4Roles(t, f)
	var logs bytes.Buffer
	server := httptest.NewServer(httpapi.New(api, slog.New(slog.NewJSONHandler(&logs, nil))))
	defer server.Close()
	b := externalBatch(t, f)
	b4Post(t, f, server.URL, b)
	if regexp.MustCompile(`[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}`).Match(logs.Bytes()) {
		t.Fatal("API log contains full UUID")
	}
	for _, private := range []string{f.enrollment.Token, b.Facts[0].UUID, b.Facts[0].Provenance.SourceRevision.Source.BundleIdentifier, `"quantity"`, `"metadata"`, `"value"`} {
		if bytes.Contains(logs.Bytes(), []byte(private)) {
			t.Fatal("API ordinary log disclosure")
		}
	}
	if !bytes.Contains(logs.Bytes(), []byte("request_hash")) {
		t.Fatal("missing hashed correlation")
	}
}

func TestB4ArtifactLimitPlusOneFailsClosed(t *testing.T) {
	f, _, _ := externalFixture(t)
	ctx := context.Background()
	l := claimAnalysis(t, f, ve.JobKind)
	in, id, _ := computeExternal(t, f, l)
	raw, e := json.Marshal(in.ExternalArtifact)
	if e != nil || len(raw) >= ve.MaxArtifactBytes {
		t.Fatal("valid artifact size")
	}
	tx, e := f.s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	// Supplemental corruption test only. Main E2E never inserts a result. Form
	// an oversized document exactly one byte above the DB text representation cap.
	var oversized []byte
	if e = tx.QueryRow(ctx, `WITH x AS (SELECT $1::jsonb||jsonb_build_object('padding','') p) SELECT p||jsonb_build_object('padding',repeat('x',$2-octet_length(p::text))) FROM x`, raw, ve.MaxArtifactBytes+1).Scan(&oversized); e != nil {
		t.Fatal(e)
	}
	var n int
	if e = tx.QueryRow(ctx, `SELECT octet_length($1::jsonb::text)`, oversized).Scan(&n); e != nil || n != ve.MaxArtifactBytes+1 {
		t.Fatal("size fixture", n, e)
	}
	_, e = tx.Exec(ctx, `INSERT INTO analysis_artifacts(dataset_id,run_id,artifact_key,schema_version,content_sha256,payload) VALUES($1,$2,$3,$4,decode(repeat('a',64),'hex'),$5)`, f.p.DatasetID, id, ve.ArtifactKey, ve.Schema, oversized)
	var dbErr *pgconn.PgError
	if !errors.As(e, &dbErr) || dbErr.Code != "23514" {
		t.Fatal("artifact limit+1 not rejected by constraint")
	}
	if count(t, f, "analysis_artifacts") != 0 || count(t, f, "analysis_publications") != 0 {
		t.Fatal("oversized artifact became visible")
	}
	t.Logf("B4_ARTIFACT_LIMIT valid_bytes=%d limit_plus_one_bytes=%d fail_closed=true", len(raw), n)
}

func TestB4MaximumBoundedArtifact(t *testing.T) {
	f := setup(t)
	api, worker := b4Roles(t, f)
	ctx := context.Background()
	server := httptest.NewServer(httpapi.New(api, analysisLog))
	defer server.Close()
	b := externalBatch(t, f)
	base := b.Facts[0]
	b.Facts = nil
	for page := 0; page < 8; page++ {
		if page > 0 {
			b = nextVO2(b)
		}
		for i := 0; i < 128; i++ {
			x := base
			x.UUID = syncing.NewID()
			x.EndUs -= int64(page*128 + i)
			x.StartUs = x.EndUs
			b.Facts = append(b.Facts, x)
		}
		b4Post(t, f, server.URL, b)
	}
	previous := ""
	for i := 0; i < 128; i++ {
		sel := ve.Selection{ID: syncing.NewID(), Epoch: base.Provenance.EpochID(), EffectiveFrom: b.FlowCreatedAtUs, Supersedes: previous}
		if e := admin.SetVO2SourceSelection(ctx, f.s, f.p.DatasetID, syncing.NewID(), "B4 synthetic maximum", sel); e != nil {
			t.Fatal(e)
		}
		previous = sel.ID
	}
	if e := admin.QueueVO2ExternalCandidate(ctx, f.s, f.p.DatasetID, syncing.NewID(), "B4 synthetic maximum"); e != nil {
		t.Fatal(e)
	}
	b4Worker(t, f, worker)
	value := base.Quantity.Value
	r, id := externalRead(t, f, "available", &value)
	raw, e := json.Marshal(r)
	if e != nil || len(raw) >= ve.MaxArtifactBytes || len(r.SelectionRefs) != 128 || len(r.FactRefs) != 1024 {
		t.Fatal("maximum bounded serialization")
	}
	var dbBytes int
	if e = f.s.Pool.QueryRow(ctx, `SELECT octet_length(payload::text) FROM analysis_artifacts WHERE dataset_id=$1 AND run_id=$2`, f.p.DatasetID, id).Scan(&dbBytes); e != nil || dbBytes >= ve.MaxArtifactBytes {
		t.Fatal("DB representation size", e)
	}
	t.Logf("B4_ARTIFACT_MAX refs=1024 selection_refs=128 canonical_bytes=%d database_bytes=%d", len(raw), dbBytes)
}

func TestB4RuntimeRoleDenials(t *testing.T) {
	f := setup(t)
	api, worker := b4Roles(t, f)
	ctx := context.Background()
	for _, test := range []struct {
		s       *postgres.Store
		queries []string
	}{
		{api, []string{`SELECT * FROM analysis_artifacts LIMIT 0`, `INSERT INTO athlete_model_inputs DEFAULT VALUES`, `UPDATE analysis_model_channels SET assignment_version=assignment_version WHERE false`, `INSERT INTO analysis_publications DEFAULT VALUES`}},
		{worker, []string{`UPDATE health_samples SET value_numeric=value_numeric WHERE false`, `DELETE FROM entity_tombstones WHERE false`, `UPDATE athlete_model_inputs SET provenance=provenance WHERE false`, `UPDATE analysis_model_channels SET assignment_version=assignment_version WHERE false`}},
	} {
		for _, query := range test.queries {
			_, e := test.s.Pool.Exec(ctx, query)
			var dbErr *pgconn.PgError
			if !errors.As(e, &dbErr) || dbErr.Code != "42501" {
				t.Fatal("expected permission denied", e)
			}
		}
	}
}

func TestB4HTTPDeleteBeforeCreate(t *testing.T) {
	f := setup(t)
	api, worker := b4Roles(t, f)
	server := httptest.NewServer(httpapi.New(api, analysisLog))
	defer server.Close()
	b := externalBatch(t, f)
	fact := b.Facts[0]
	b.Facts = []syncing.VO2Fact{}
	b.DeletedUUIDs = []string{fact.UUID}
	b4Post(t, f, server.URL, b)
	b4Enroll(t, f, fact.Provenance.EpochID(), b.FlowCreatedAtUs)
	b4Worker(t, f, worker)
	externalRead(t, f, "no_sample_observed", nil)
	next := nextVO2(b)
	next.Facts = []syncing.VO2Fact{fact}
	ack := b4Post(t, f, server.URL, next)
	var receipt syncing.VO2Receipt
	if json.Unmarshal(ack, &receipt) != nil || receipt.Counts.Suppressed != 1 {
		t.Fatal("unknown delete did not suppress create")
	}
	b4Worker(t, f, worker)
	externalRead(t, f, "no_sample_observed", nil)
	if count(t, f, "health_samples") != 0 || count(t, f, "analysis_publications") != 1 {
		t.Fatal("delete-before-create resurrected current")
	}
}
