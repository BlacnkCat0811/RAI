//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/admin"
	"rai/server/internal/analysis"
	"rai/server/internal/jobs"
	rc "rai/server/internal/recovery"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
	"rai/server/internal/training"
	tl "rai/server/internal/trainingload"
	wf "rai/server/internal/workoutfeatures"
)

func recoveryFixture(t *testing.T, history int) (*fixture, map[string]syncing.DailyBatch, string, time.Time) {
	t.Helper()
	return recoveryFacts(t, setup(t), history)
}
func recoveryFacts(t *testing.T, f *fixture, history int) (*fixture, map[string]syncing.DailyBatch, string, time.Time) {
	t.Helper()
	ctx := context.Background()
	var tz string
	if e := f.s.Pool.QueryRow(ctx, `SELECT timezone FROM datasets WHERE id=$1`, f.p.DatasetID).Scan(&tz); e != nil {
		t.Fatal(e)
	}
	loc, e := time.LoadLocation(tz)
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().In(loc)
	target := time.Date(now.Year(), now.Month(), now.Day()-1, 0, 0, 0, 0, loc)
	batches := map[string]syncing.DailyBatch{}
	for _, kind := range []string{rc.HRV, rc.RHR, rc.Sleep} {
		b := dailyBatch(f, kind)
		if history > 80 {
			b.BootstrapStartUs = target.AddDate(0, 0, -history-1).UnixMicro()
		}
		if history > 127 {
			b.ObservedAtUs = target.AddDate(0, 0, -history+127).Add(12 * time.Hour).UnixMicro()
		}
		for n := history; n >= 0; n-- {
			d := target.AddDate(0, 0, -n)
			x := dailyFact(kind)
			x.StartUs = d.Add(7 * time.Hour).UnixMicro()
			x.EndUs = x.StartUs
			x.Metadata = map[string]any{"HKAlgorithmVersion": int64(1), "HKTimeZone": tz}
			x.Source = syncing.HealthKitSource{BundleID: "synthetic.m5b", Version: "1", ProductType: "synthetic", Device: &syncing.HealthKitDevice{Manufacturer: "Synthetic", Model: "Fixture"}}
			if kind == rc.Sleep {
				x.StartUs = d.Add(-time.Hour).UnixMicro()
				cat := 1
				x.Category = &cat
			} else if kind == rc.HRV {
				x.Quantity.Value = 50
			} else {
				x.Quantity.Value = 60
			}
			b.Facts = append(b.Facts, x)
			if len(b.Facts) == 128 {
				requireDaily(t, f, b)
				b = nextDaily(b)
				b.ObservedAtUs = time.Now().UTC().UnixMicro()
			}
		}
		requireDaily(t, f, b)
		batches[kind] = b
	}
	day := target.Format("2006-01-02")
	if e = admin.QueueRecoveryCandidate(ctx, f.s, f.p.DatasetID, day, syncing.NewID(), "synthetic M5B"); e != nil {
		t.Fatal(e)
	}
	return f, batches, day, target.AddDate(0, 0, 1).Add(-time.Microsecond)
}
func computeRecovery(t *testing.T, f *fixture, l analysis.Lease) (analysis.Snapshot, string, []analysis.Metric) {
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
	ms, e := analysis.CalculateCandidateRecovery(&in)
	if e != nil {
		t.Fatal(e)
	}
	return in, id, ms
}
func readRecovery(t *testing.T, f *fixture, day string, at time.Time) (rc.Result, string) {
	t.Helper()
	r, id, e := f.s.CandidateRecovery(context.Background(), f.p.DatasetID, day, at)
	if e != nil {
		t.Fatal(e)
	}
	return r, id
}
func runRecoveryLatest(t *testing.T, f *fixture) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 200; i++ {
		l, e := f.s.Claim(ctx, []string{rc.JobKind})
		if errors.Is(e, pgx.ErrNoRows) {
			return
		}
		if e != nil {
			t.Fatal(e)
		}
		jobs.Process(ctx, f.s, l, analysisLog)
	}
	t.Fatal("unbounded recovery queue")
}
func TestRecoveryModelPublicationAndImmutableRaw(t *testing.T) {
	f, _, day, at := recoveryFixture(t, 28)
	ctx := context.Background()
	var before, after string
	if e := f.s.Pool.QueryRow(ctx, `SELECT md5(string_agg(to_jsonb(h)::text,',' ORDER BY sample_type,sample_uuid)) FROM health_samples h WHERE dataset_id=$1`, f.p.DatasetID).Scan(&before); e != nil {
		t.Fatal(e)
	}
	runRecoveryLatest(t, f)
	r, id := readRecovery(t, f, day, at)
	if r.Status != "available" || r.Score == nil || *r.Score != 100 || r.Confidence > .60 {
		t.Fatal("stable publication", r.Status)
	}
	var artifacts int
	if e := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM analysis_artifacts WHERE run_id=$1`, id).Scan(&artifacts); e != nil || artifacts != 3 {
		t.Fatal("three atomic artifacts", e)
	}
	if e := f.s.Pool.QueryRow(ctx, `SELECT md5(string_agg(to_jsonb(h)::text,',' ORDER BY sample_type,sample_uuid)) FROM health_samples h WHERE dataset_id=$1`, f.p.DatasetID).Scan(&after); e != nil || before != after {
		t.Fatal("raw mutated", e)
	}
	old, withdrawn, e := f.s.HistoricalRecovery(ctx, f.p.DatasetID, id)
	if e != nil || withdrawn || rc.Hash(old) != rc.Hash(r) {
		t.Fatal("historical capture", e)
	}
	if !errors.Is(f.s.LoadHistoricalRecovery(ctx, f.p.DatasetID, at), rc.ErrHistorical) {
		t.Fatal("fake historical replay")
	}
	for _, stmt := range []string{`UPDATE analysis_artifacts SET payload='{}' WHERE run_id=$1`, `DELETE FROM analysis_artifacts WHERE run_id=$1`} {
		if _, e = f.s.Pool.Exec(ctx, stmt, id); e == nil {
			t.Fatal("immutable artifact changed")
		}
	}
	var upstreams, futureDirty int
	if e := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM analysis_model_dependencies WHERE dataset_id=$1 AND downstream_scope=$2`, f.p.DatasetID, strings.Replace(rc.Scope(r.Timezone, day), "/recovery/", "/baseline/", 1)).Scan(&upstreams); e != nil || upstreams != 28 {
		t.Fatal("bounded baseline graph", upstreams, e)
	}
	if e := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM dirty_scopes WHERE dataset_id=$1 AND scope_key=$2 AND generation>clean_generation`, f.p.DatasetID, strings.Replace(rc.Scope(r.Timezone, day), "/recovery/", "/readiness/", 1)).Scan(&futureDirty); e != nil || futureDirty != 1 {
		t.Fatal("future readiness dirtiness", e)
	}
	var output bytes.Buffer
	if e = admin.RecoveryCandidateCommand(ctx, f.s.Pool.Config().ConnString(), []string{"inspect", "--dataset", f.p.DatasetID, "--day", day}, &output); e != nil {
		t.Fatal(e)
	}
	for _, forbidden := range []string{f.p.DatasetID, "synthetic.m5b", `"score"`, `"Value"`, `"LastEvent"`} {
		if bytes.Contains(output.Bytes(), []byte(forbidden)) {
			t.Fatal("admin health privacy leak")
		}
	}
}
func TestRecoveryModelPublicationFences(t *testing.T) {
	for _, change := range []string{"delete", "late_rhr_revision", "sleep_revision", "coverage", "config", "assignment", "timezone", "collector", "device", "lease", "takeover"} {
		t.Run(change, func(t *testing.T) {
			f, bs, day, _ := recoveryFixture(t, 28)
			ctx := context.Background()
			l := claimAnalysis(t, f, rc.JobKind)
			in, id, ms := computeRecovery(t, f, l)
			switch change {
			case "delete", "late_rhr_revision", "sleep_revision":
				kind := rc.RHR
				if change == "sleep_revision" {
					kind = rc.Sleep
				}
				b := nextDaily(bs[kind])
				x := bs[kind].Facts[len(bs[kind].Facts)-1]
				b.DeletedUUIDs = []string{x.UUID}
				if change != "delete" {
					x.UUID = syncing.NewID()
					if kind == rc.RHR {
						x.Quantity = &syncing.Quantity{Value: 75, Unit: "count/min"}
					} else {
						x.StartUs += 2 * rc.Hour
					}
					b.Facts = []syncing.DailyFact{x}
				}
				requireDaily(t, f, b)
			case "coverage":
				b := nextDaily(bs[rc.HRV])
				b.HasMore = true
				requireDaily(t, f, b)
			case "config":
				if _, e := f.s.Pool.Exec(ctx, `UPDATE analysis_model_channels SET extension_configs=jsonb_set(extension_configs,ARRAY[$2,'configHash'],to_jsonb(repeat('b',64))) WHERE dataset_id=$1 AND channel='candidate'`, f.p.DatasetID, rc.Model); e != nil {
					t.Fatal(e)
				}
			case "assignment":
				if _, e := f.s.Pool.Exec(ctx, `UPDATE analysis_model_channels SET extension_configs=jsonb_set(extension_configs,ARRAY[$2,'assignmentID'],to_jsonb($3::text)) WHERE dataset_id=$1 AND channel='candidate'`, f.p.DatasetID, rc.Model, syncing.NewID()); e != nil {
					t.Fatal(e)
				}
			case "timezone":
				if _, e := f.s.Pool.Exec(ctx, `UPDATE datasets SET timezone='America/New_York' WHERE id=$1`, f.p.DatasetID); e != nil {
					t.Fatal(e)
				}
			case "collector":
				if _, e := f.s.Pool.Exec(ctx, `UPDATE collector_leases SET collector_epoch=$2 WHERE dataset_id=$1`, f.p.DatasetID, syncing.NewID()); e != nil {
					t.Fatal(e)
				}
			case "device":
				if _, e := f.s.Pool.Exec(ctx, `UPDATE devices SET revoked_at=clock_timestamp() WHERE dataset_id=$1 AND id=$2`, f.p.DatasetID, f.p.DeviceID); e != nil {
					t.Fatal(e)
				}
			case "lease", "takeover":
				if _, e := f.s.Pool.Exec(ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, l.ID); e != nil {
					t.Fatal(e)
				}
				if change == "takeover" {
					if _, e := f.s.RequeueExpired(ctx); e != nil {
						t.Fatal(e)
					}
					if _, e := f.s.Pool.Exec(ctx, `UPDATE jobs SET run_after=clock_timestamp() WHERE id=$1`, l.ID); e != nil {
						t.Fatal(e)
					}
					newLease := claimAnalysis(t, f, rc.JobKind)
					jobs.Process(ctx, f.s, newLease, analysisLog)
				}
			}
			status, e := f.s.PublishAnalysis(ctx, l, id, in, ms)
			if change == "lease" || change == "takeover" {
				if e == nil {
					t.Fatal("expired worker published")
				}
			} else if e != nil || status != "SUPERSEDED" {
				t.Fatal("obsolete input published", status, e)
			}
			var n int
			if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM analysis_artifacts WHERE run_id=$1`, id).Scan(&n); e != nil || n != 0 {
				t.Fatal("obsolete artifact committed", e)
			}
			if change != "takeover" {
				if _, _, e = f.s.CandidateRecovery(ctx, f.p.DatasetID, day, time.Now()); e == nil {
					t.Fatal("obsolete candidate remained current")
				}
			}
		})
	}
}
func TestRecoveryModelDeleteAndRevisionHistory(t *testing.T) {
	f, bs, day, at := recoveryFixture(t, 28)
	ctx := context.Background()
	runRecoveryLatest(t, f)
	old, oldID := readRecovery(t, f, day, at)
	b := nextDaily(bs[rc.RHR])
	x := bs[rc.RHR].Facts[len(bs[rc.RHR].Facts)-1]
	b.DeletedUUIDs = []string{x.UUID}
	x.UUID = syncing.NewID()
	x.Quantity = &syncing.Quantity{Value: 75, Unit: "count/min"}
	b.Facts = []syncing.DailyFact{x}
	requireDaily(t, f, b)
	if _, _, e := f.s.CandidateRecovery(ctx, f.p.DatasetID, day, at); e == nil {
		t.Fatal("deleted input still current")
	}
	runRecoveryLatest(t, f)
	updated, newID := readRecovery(t, f, day, at)
	if newID == oldID || updated.Score == nil || *updated.Score >= *old.Score {
		t.Fatal("revision did not create stressed result")
	}
	hist, withdrawn, e := f.s.HistoricalRecovery(ctx, f.p.DatasetID, oldID)
	if e != nil || !withdrawn || rc.Hash(hist) != rc.Hash(old) {
		t.Fatal("old artifact lost/rewritten", e)
	}
	// Exact receipt replay and a new upload of the tombstoned UUID cannot revive it.
	requireDaily(t, f, bs[rc.RHR])
	d := nextDaily(b)
	d.Facts = []syncing.DailyFact{bs[rc.RHR].Facts[len(bs[rc.RHR].Facts)-1]}
	receipt := requireDaily(t, f, d)
	if receipt.Counts.Suppressed != 1 {
		t.Fatal("old fact resurrected")
	}
}
func TestRecoveryModelBoundedDependenciesAndIsolation(t *testing.T) {
	f, bs, day, at := recoveryFixture(t, 28)
	ctx := context.Background()
	runRecoveryLatest(t, f)
	base, id := readRecovery(t, f, day, at)
	var generation int64
	if e := f.s.Pool.QueryRow(ctx, `SELECT generation FROM dirty_scopes WHERE dataset_id=$1 AND scope_key=$2`, f.p.DatasetID, rc.Scope(base.Timezone, day)).Scan(&generation); e != nil {
		t.Fatal(e)
	}
	// Different load, running ability, external VO2 and workout facts do not enter
	// the recovery input or its dirty graph. Exercise the real existing pipelines.
	b := m3Bundle(t, f, time.Now().Add(-48*time.Hour), 1200)
	importPhase3(t, f, b)
	v := externalBatch(t, f)
	requireVO2(t, f, v)
	if _, e := f.s.Pool.Exec(ctx, `INSERT INTO analysis_parameter_sets(dataset_id,version,resting_hr,max_hr,coefficient_a,coefficient_b,provenance) VALUES($1,1,80,200,2,2,'synthetic different training')`, f.p.DatasetID); e != nil {
		t.Fatal(e)
	}
	r, gotID := readRecovery(t, f, day, at)
	if gotID != id || rc.Hash(r) != rc.Hash(base) {
		t.Fatal("training/VO2/ability polluted recovery")
	}
	// A fact outside D-28 and the sleep context cannot dirty this output.
	old := nextDaily(bs[rc.HRV])
	x := bs[rc.HRV].Facts[0]
	x.UUID = syncing.NewID()
	x.StartUs -= 3 * 24 * rc.Hour
	x.EndUs = x.StartUs
	old.Facts = []syncing.DailyFact{x}
	requireDaily(t, f, old)
	var after int64
	if e := f.s.Pool.QueryRow(ctx, `SELECT generation FROM dirty_scopes WHERE dataset_id=$1 AND scope_key=$2`, f.p.DatasetID, rc.Scope(base.Timezone, day)).Scan(&after); e != nil || generation != after {
		t.Fatal("unbounded invalidation", e)
	}
	r, _ = readRecovery(t, f, day, at)
	if *r.Score != *base.Score {
		t.Fatal("out of window raw affected score")
	}
	var unsupported int
	if e := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE dataset_id=$1 AND kind ILIKE '%readiness%'`, f.p.DatasetID).Scan(&unsupported); e != nil || unsupported != 0 {
		t.Fatal("Readiness success handler registered")
	}
	home, e := f.s.ProductOverview(ctx, f.p)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(home)
	if bytes.Contains(raw, []byte(rc.Model)) || bytes.Contains(raw, []byte("physiological_recovery")) {
		t.Fatal("candidate leaked to product")
	}
}
func TestRecoveryModelRuntimeRoles(t *testing.T) {
	f, bs, day, at := recoveryFixture(t, 28)
	ctx := context.Background()
	suffix := strings.ReplaceAll(syncing.NewID(), "-", "")
	apiRole, workerRole := "rai_api_"+suffix, "rai_worker_"+suffix
	raw, e := os.ReadFile("../../../deploy/postgres/roles.sql")
	if e != nil {
		t.Fatal(e)
	}
	sql := strings.ReplaceAll(strings.ReplaceAll(string(raw), "rai_api", apiRole), "rai_worker", workerRole)
	if _, e = f.s.Pool.Exec(ctx, sql); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if _, e := f.s.Pool.Exec(context.Background(), "DROP OWNED BY "+pgx.Identifier{apiRole}.Sanitize()+", "+pgx.Identifier{workerRole}.Sanitize()+"; DROP ROLE "+pgx.Identifier{apiRole}.Sanitize()+", "+pgx.Identifier{workerRole}.Sanitize()); e != nil {
			t.Error(e)
		}
	})
	open := func(role string) *postgres.Store {
		dsn := f.s.Pool.Config().ConnString()
		sep := "&"
		if !strings.Contains(dsn, "?") {
			sep = "?"
		}
		s, e := postgres.Open(ctx, dsn+sep+"options=-crole%3D"+role, 2)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(s.Pool.Close)
		return s
	}
	api, worker := open(apiRole), open(workerRole)
	for _, stmt := range []string{`SELECT * FROM analysis_artifacts`, `INSERT INTO analysis_results(dataset_id,run_id) VALUES(NULL,NULL)`, `UPDATE analysis_model_channels SET extension_configs='{}'`, `DELETE FROM analysis_publications`} {
		if _, e = api.Pool.Exec(ctx, stmt); e == nil {
			t.Fatal("API wrote candidate")
		}
	}
	for _, stmt := range []string{`UPDATE health_samples SET value_numeric=1`, `DELETE FROM health_samples`, `UPDATE daily_health_coverage SET has_more=true`, `UPDATE analysis_artifacts SET payload='{}'`} {
		if _, e = worker.Pool.Exec(ctx, stmt); e == nil {
			t.Fatal("Worker mutated raw/history")
		}
	}
	l, e := worker.Claim(ctx, []string{rc.JobKind})
	if e != nil {
		t.Fatal(e)
	}
	jobs.Process(ctx, worker, l, analysisLog)
	readRecovery(t, f, day, at)
	orig := f.s
	f.s = api
	b := nextDaily(bs[rc.RHR])
	b.DeletedUUIDs = []string{bs[rc.RHR].Facts[len(bs[rc.RHR].Facts)-1].UUID}
	_, e = commitDaily(t, f, b)
	f.s = orig
	if e != nil {
		t.Fatal("API delete definer", e)
	}
	if _, _, e = f.s.CandidateRecovery(ctx, f.p.DatasetID, day, at); e == nil {
		t.Fatal("API delete left current")
	}
}
func TestRecoveryModelIncrementalEqualsBoundedRebuild(t *testing.T) {
	f, bs, day, at := recoveryFixture(t, 28)
	ctx := context.Background()
	rng := rand.New(rand.NewSource(526041))
	runRecoveryLatest(t, f)
	for step := 0; step < 12; step++ {
		kind := []string{rc.HRV, rc.RHR, rc.Sleep}[step%3]
		b := nextDaily(bs[kind])
		old := bs[kind].Facts[len(bs[kind].Facts)-1]
		x := old
		x.UUID = syncing.NewID()
		b.DeletedUUIDs = []string{old.UUID}
		if kind == rc.Sleep {
			x.StartUs = x.EndUs - int64(4+rng.Intn(5))*rc.Hour
		} else {
			x.Quantity = &syncing.Quantity{Value: float64(40 + rng.Intn(35)), Unit: old.Quantity.Unit}
		}
		b.Facts = []syncing.DailyFact{x}
		requireDaily(t, f, b)
		bs[kind] = b
		runRecoveryLatest(t, f)
		incremental, _ := readRecovery(t, f, day, at)
		if e := admin.QueueRecoveryCandidate(ctx, f.s, f.p.DatasetID, day, syncing.NewID(), "synthetic bounded rebuild"); e != nil {
			t.Fatal(e)
		}
		runRecoveryLatest(t, f)
		rebuilt, _ := readRecovery(t, f, day, at)
		incremental.Generation = 0
		rebuilt.Generation = 0
		incremental.CapturedAt = 0
		rebuilt.CapturedAt = 0
		incremental.InputHash = ""
		rebuilt.InputHash = ""
		if rc.Hash(incremental) != rc.Hash(rebuilt) {
			t.Fatal("incremental != bounded rebuild", step)
		}
	}
}
func TestRecoveryModelSnapshotRepeatableRead(t *testing.T) {
	f, bs, _, _ := recoveryFixture(t, 28)
	ctx := context.Background()
	l := claimAnalysis(t, f, rc.JobKind)
	block, e := f.s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer block.Rollback(ctx)
	if _, e = block.Exec(ctx, `LOCK TABLE health_samples IN ACCESS EXCLUSIVE MODE`); e != nil {
		t.Fatal(e)
	}
	type outcome struct {
		in analysis.Snapshot
		e  error
	}
	done := make(chan outcome, 1)
	go func() { in, e := f.s.LoadAnalysis(ctx, l, time.Now()); done <- outcome{in, e} }()
	waiting := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if e = f.s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '-- name: RecoveryDailyFacts%')`).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !waiting {
		t.Fatal("snapshot barrier not reached")
	}
	b := nextDaily(bs[rc.RHR])
	b.HasMore = true
	requireDaily(t, f, b)
	if e = block.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	got := <-done
	if got.e != nil || got.in.Generation != l.TargetGeneration || got.in.RecoveryInput == nil {
		t.Fatal("mixed snapshot", got.e)
	}
	for _, c := range got.in.RecoveryInput.Coverage {
		if !c.Complete {
			t.Fatal("coverage from future commit leaked")
		}
	}
}
func TestRecoveryModelPerformanceMatrix(t *testing.T) {
	for _, n := range []int{14, 28, 42, 90, 180, 365} {
		t.Run(fmt.Sprintf("history_%dd", n), func(t *testing.T) {
			f, _, day, at := recoveryFixture(t, n)
			ctx := context.Background()
			l := claimAnalysis(t, f, rc.JobKind)
			start := time.Now()
			in, e := f.s.LoadAnalysis(ctx, l, time.Now())
			if e != nil {
				t.Fatal(e)
			}
			loadTime := time.Since(start)
			if len(in.RecoveryInput.Facts) > 31*3 {
				t.Fatal("current query scanned lifetime", len(in.RecoveryInput.Facts))
			}
			start = time.Now()
			ms, e := analysis.CalculateCandidateRecovery(&in)
			if e != nil {
				t.Fatal(e)
			}
			computeTime := time.Since(start)
			id, e := f.s.StartAnalysisRun(ctx, l, in)
			if e != nil {
				t.Fatal(e)
			}
			start = time.Now()
			status, e := f.s.PublishAnalysis(ctx, l, id, in, ms)
			if e != nil || status != "SUCCEEDED" {
				t.Fatal("publish", status, e)
			}
			pubTime := time.Since(start)
			_, _ = readRecovery(t, f, day, at)
			raw, _ := json.Marshal(in.RecoveryArtifact)
			var storedBytes int64
			if e := f.s.Pool.QueryRow(ctx, `SELECT sum(octet_length(payload::text)) FROM analysis_artifacts WHERE dataset_id=$1 AND run_id=$2`, f.p.DatasetID, id).Scan(&storedBytes); e != nil || storedBytes > 3*rc.MaxArtifactBytes {
				t.Fatal("stored artifact size", e)
			}
			t.Logf("stored_artifact_bytes=%d", storedBytes)
			t.Logf("synthetic_history_days=%d input_facts=%d load_us=%d compute_us=%d publish_us=%d artifact_bytes=%d", n, len(in.RecoveryInput.Facts), loadTime.Microseconds(), computeTime.Microseconds(), pubTime.Microseconds(), len(raw))
		})
	}
}

func TestRecoveryModelQueryPlanIsBounded(t *testing.T) {
	f, _, _, _ := recoveryFixture(t, 365)
	ctx := context.Background()
	l := claimAnalysis(t, f, rc.JobKind)
	in, e := f.s.LoadAnalysis(ctx, l, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile("../queries/recovery.sql")
	if e != nil {
		t.Fatal(e)
	}
	query := strings.Split(string(raw), "-- name: RecoveryDailyTombstones")[0]
	if _, e = f.s.Pool.Exec(ctx, `ANALYZE health_samples`); e != nil {
		t.Fatal(e)
	}
	var plan []byte
	if e = f.s.Pool.QueryRow(ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+query, f.p.DatasetID, []string{rc.HRV, rc.RHR, rc.Sleep}, in.From, in.Through.Add(time.Microsecond)).Scan(&plan); e != nil {
		t.Fatal(e)
	}
	var trees []map[string]any
	if json.Unmarshal(plan, &trees) != nil {
		t.Fatal("plan JSON")
	}
	indexBound := false
	scanRows := 0.0
	var walk func(map[string]any)
	walk = func(n map[string]any) {
		if relation, _ := n["Relation Name"].(string); relation == "health_samples" {
			if n["Node Type"] == "Seq Scan" {
				t.Fatal("lifetime sequential scan")
			}
			rows, _ := n["Actual Rows"].(float64)
			scanRows += rows
		}
		if condition, _ := n["Index Cond"].(string); strings.Contains(condition, "event_end >=") && strings.Contains(condition, "event_end <") {
			indexBound = true
		}
		if children, ok := n["Plans"].([]any); ok {
			for _, c := range children {
				walk(c.(map[string]any))
			}
		}
	}
	walk(trees[0]["Plan"].(map[string]any))
	if !indexBound || scanRows > 100 {
		t.Fatal("range not in index condition", indexBound, scanRows)
	}
	t.Logf("history_days=365 bounded_index=true raw_rows=%.0f execution_ms=%v", scanRows, trees[0]["Execution Time"])
}
func TestRecoveryModelExactDependencyEdges(t *testing.T) {
	for _, tt := range []struct {
		kind  string
		back  int
		dirty bool
	}{{rc.RHR, 28, true}, {rc.RHR, 29, false}, {rc.Sleep, 30, true}, {rc.Sleep, 31, false}} {
		t.Run(fmt.Sprintf("%s_%dd", tt.kind, tt.back), func(t *testing.T) {
			f, bs, day, at := recoveryFixture(t, 42)
			ctx := context.Background()
			runRecoveryLatest(t, f)
			base, _ := readRecovery(t, f, day, at)
			scope := rc.Scope(base.Timezone, day)
			var before, after int64
			if e := f.s.Pool.QueryRow(ctx, `SELECT generation FROM dirty_scopes WHERE dataset_id=$1 AND scope_key=$2`, f.p.DatasetID, scope).Scan(&before); e != nil {
				t.Fatal(e)
			}
			b := nextDaily(bs[tt.kind])
			index := 42 - tt.back
			b.DeletedUUIDs = []string{bs[tt.kind].Facts[index].UUID}
			requireDaily(t, f, b)
			if e := f.s.Pool.QueryRow(ctx, `SELECT generation FROM dirty_scopes WHERE dataset_id=$1 AND scope_key=$2`, f.p.DatasetID, scope).Scan(&after); e != nil {
				t.Fatal(e)
			}
			if (after > before) != tt.dirty {
				t.Fatal("wrong dependency edge", before, after)
			}
		})
	}
}
func TestRecoveryModelRetryDayAdvanceAndTimezoneEpoch(t *testing.T) {
	f, _, day, at := recoveryFixture(t, 28)
	ctx := context.Background()
	runRecoveryLatest(t, f)
	base, id := readRecovery(t, f, day, at)
	op := syncing.NewID()
	for i := 0; i < 2; i++ {
		if e := admin.QueueRecoveryCandidate(ctx, f.s, f.p.DatasetID, day, op, "synthetic exact retry"); e != nil {
			t.Fatal(e)
		}
	}
	var generation int64
	if e := f.s.Pool.QueryRow(ctx, `SELECT generation FROM dirty_scopes WHERE dataset_id=$1 AND scope_key=$2`, f.p.DatasetID, rc.Scope(base.Timezone, day)).Scan(&generation); e != nil || generation != base.Generation+1 {
		t.Fatal("operation retry changed generation", e)
	}
	for i := 0; i < 2; i++ {
		if e := f.s.AdvanceAnalysisDay(ctx, time.Now()); e != nil {
			t.Fatal(e)
		}
	}
	var todayJobs int
	if e := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE dataset_id=$1 AND kind=$2 AND scope_key<>$3`, f.p.DatasetID, rc.JobKind, rc.Scope(base.Timezone, day)).Scan(&todayJobs); e != nil || todayJobs != 1 {
		t.Fatal("day advance duplicate", todayJobs, e)
	}
	if _, e := f.s.Pool.Exec(ctx, `UPDATE datasets SET timezone='America/New_York' WHERE id=$1`, f.p.DatasetID); e != nil {
		t.Fatal(e)
	}
	hist, _, e := f.s.HistoricalRecovery(ctx, f.p.DatasetID, id)
	if e != nil || hist.Timezone != base.Timezone || hist.CalendarHash != base.CalendarHash {
		t.Fatal("history silently reinterpreted", e)
	}
	if _, _, e = f.s.CandidateRecovery(ctx, f.p.DatasetID, day, at); e == nil {
		t.Fatal("timezone epoch remained current")
	}
	if e = admin.QueueRecoveryCandidate(ctx, f.s, f.p.DatasetID, day, syncing.NewID(), "synthetic explicit travel context"); e != nil {
		t.Fatal(e)
	}
	var entryTZ string
	if e = f.s.Pool.QueryRow(ctx, `SELECT extension_configs->$2->>'timezone' FROM analysis_model_channels WHERE dataset_id=$1 AND channel='candidate'`, f.p.DatasetID, rc.Model).Scan(&entryTZ); e != nil || entryTZ != "America/New_York" {
		t.Fatal("new timezone assignment", e)
	}
}
func TestRecoveryModelForgedOutputCannotPublish(t *testing.T) {
	for _, mutate := range []string{"score", "baseline", "metrics", "snapshot"} {
		t.Run(mutate, func(t *testing.T) {
			f, _, _, _ := recoveryFixture(t, 28)
			ctx := context.Background()
			l := claimAnalysis(t, f, rc.JobKind)
			in, id, ms := computeRecovery(t, f, l)
			switch mutate {
			case "score":
				v := 99.
				in.RecoveryArtifact.Recovery.Score = &v
			case "baseline":
				v := .5
				in.RecoveryArtifact.Baseline.SDNN.Center = &v
			case "metrics":
				v := 99.
				ms[0].Value = &v
			case "snapshot":
				in.RecoveryInput.Config.LogFloor = .2
			}
			if status, e := f.s.PublishAnalysis(ctx, l, id, in, ms); e == nil && status != "SUPERSEDED" {
				t.Fatal("forged derived result accepted")
			}
			var n int
			if e := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM analysis_artifacts WHERE run_id=$1`, id).Scan(&n); e != nil || n != 0 {
				t.Fatal("forged result partially committed", e)
			}
		})
	}
}

func TestRecoveryModelRawCommitRollbackRestoresCandidate(t *testing.T) {
	f, bs, day, at := recoveryFixture(t, 28)
	ctx := context.Background()
	runRecoveryLatest(t, f)
	before, id := readRecovery(t, f, day, at)
	if _, e := f.s.Pool.Exec(ctx, `CREATE FUNCTION m5b_fact_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic daily failure' USING ERRCODE='23514'; END $$; CREATE TRIGGER m5b_fact_fault BEFORE UPDATE ON daily_health_coverage FOR EACH ROW EXECUTE FUNCTION m5b_fact_fault()`); e != nil {
		t.Fatal(e)
	}
	b := nextDaily(bs[rc.RHR])
	b.DeletedUUIDs = []string{bs[rc.RHR].Facts[len(bs[rc.RHR].Facts)-1].UUID}
	if _, e := commitDaily(t, f, b); e == nil {
		t.Fatal("fault did not abort raw commit")
	}
	after, gotID := readRecovery(t, f, day, at)
	if gotID != id || rc.Hash(after) != rc.Hash(before) {
		t.Fatal("failed raw transaction withdrew candidate")
	}
	var receipts int
	if e := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM sync_batches WHERE dataset_id=$1 AND batch_id=$2`, f.p.DatasetID, b.BatchID).Scan(&receipts); e != nil || receipts != 0 {
		t.Fatal("failed raw transaction left receipt", e)
	}
	if _, e := f.s.Pool.Exec(ctx, `DROP TRIGGER m5b_fact_fault ON daily_health_coverage`); e != nil {
		t.Fatal(e)
	}
	requireDaily(t, f, b)
	if _, _, e := f.s.CandidateRecovery(ctx, f.p.DatasetID, day, at); e == nil {
		t.Fatal("committed deletion did not withdraw candidate")
	}
}

func TestRecoveryModelSameFactsDifferentActualTrainingLoad(t *testing.T) {
	f, b := loadFixture(t)
	ctx := context.Background()
	_, _, day, at := recoveryFacts(t, f, 28)
	runRecoveryLatest(t, f)
	base, id := readRecovery(t, f, day, at)
	runOne(t, f, tl.JobKind)
	first, _, e := f.s.CandidateLoad(ctx, f.p.DatasetID, b.WorkoutID, "2026-10-31")
	if e != nil || first.Load == nil {
		t.Fatal("first actual M2 load", e)
	}
	runOne(t, f, tl.StateJobKind)
	var oldID string
	if e = f.s.Pool.QueryRow(ctx, `SELECT id::text FROM athlete_model_inputs WHERE dataset_id=$1 AND kind='hr_reference' ORDER BY created_at DESC LIMIT 1`, f.p.DatasetID).Scan(&oldID); e != nil {
		t.Fatal(e)
	}
	through := *b.Workout.EndUs + 3600e6
	if e = admin.SetFeatureHRReference(ctx, f.s, f.p.DatasetID, wf.Parameter{ID: syncing.NewID(), Supersedes: oldID, EffectiveFrom: *b.Workout.StartUs - 3600e6, EffectiveThrough: &through, RestHR: 80, MaxHR: 200, Provenance: "synthetic alternate M2 context"}); e != nil {
		t.Fatal(e)
	}
	runOne(t, f, wf.JobKind)
	runOne(t, f, tl.JobKind)
	runOne(t, f, tl.StateJobKind)
	second, _, e := f.s.CandidateLoad(ctx, f.p.DatasetID, b.WorkoutID, "2026-10-31")
	if e != nil || second.Load == nil || *second.Load == *first.Load {
		t.Fatal("actual load did not change", e)
	}
	got, gotID := readRecovery(t, f, day, at)
	if gotID != id || rc.Hash(got) != rc.Hash(base) {
		t.Fatal("different actual M2 load changed physiological recovery")
	}
}

func TestRecoveryModelCandidateDoesNotLeakProductDTOs(t *testing.T) {
	f, _, _, _ := recoveryFixture(t, 28)
	ctx := context.Background()
	runRecoveryLatest(t, f)
	b := m3Bundle(t, f, time.Now().Add(-48*time.Hour), 1200)
	importPhase3(t, f, b)
	values := []any{}
	home, e := f.s.ProductOverview(ctx, f.p)
	if e != nil {
		t.Fatal(e)
	}
	values = append(values, home)
	metrics, e := f.s.ProductHistory(ctx, f.p, 14)
	if e != nil {
		t.Fatal(e)
	}
	values = append(values, metrics)
	workouts, e := f.s.ProductWorkouts(ctx, f.p, "", 10, "")
	if e != nil {
		t.Fatal(e)
	}
	values = append(values, workouts)
	detail, e := f.s.WorkoutDetail(ctx, f.p, b.WorkoutID)
	if e != nil {
		t.Fatal(e)
	}
	values = append(values, detail)
	plan, e := f.s.CreateTrainingPlan(ctx, f.p, training.CreateRequest{CommandID: syncing.NewID(), PlanID: syncing.NewID(), Config: trainingConfig()})
	if e != nil {
		t.Fatal(e)
	}
	savedPlan, e := f.s.TrainingPlan(ctx, f.p, plan.ID)
	if e != nil {
		t.Fatal(e)
	}
	values = append(values, savedPlan)
	// Training generation reads only the legacy training-state family. A current
	// candidate must not make the generic state reader succeed by accident.
	if current, e := f.s.CurrentTrainingState(ctx, f.p); e == nil {
		values = append(values, current)
	}
	for _, v := range values {
		raw, _ := json.Marshal(v)
		for _, private := range []string{rc.Model, "physiological_recovery", rc.Family, "daily_health_baseline", "daily_health_features"} {
			if bytes.Contains(raw, []byte(private)) {
				t.Fatal("candidate leaked to product")
			}
		}
	}
}
