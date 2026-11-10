//go:build integration && modelperf

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	am "rai/server/internal/athletemodel"
	"rai/server/internal/httpapi"
	pf "rai/server/internal/performance"
	rd "rai/server/internal/readiness"
	rc "rai/server/internal/recovery"
	postgres "rai/server/internal/storage/postgres"
	tl "rai/server/internal/trainingload"
	vo2 "rai/server/internal/vo2max"
	ve "rai/server/internal/vo2maxexternal"
)

type queryCounter struct {
	selects atomic.Int64
	mu      sync.Mutex
	readers map[string][]any
}

func (q *queryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(data.SQL)), "SELECT") {
		q.selects.Add(1)
		if strings.Contains(data.SQL, "JOIN analysis_published_artifacts") {
			q.mu.Lock()
			q.readers[data.SQL] = append([]any(nil), data.Args...)
			q.mu.Unlock()
		}
	}
	return ctx
}
func (q *queryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func percentile(xs []time.Duration, p float64) time.Duration {
	if len(xs) == 0 {
		return 0
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
	i := int(float64(len(xs))*p+0.999999) - 1
	if i < 0 {
		i = 0
	}
	if i >= len(xs) {
		i = len(xs) - 1
	}
	return xs[i]
}

// TestAthleteProductHTTPPerformance measures the real auth -> handler -> Store
// -> PostgreSQL path over a loopback TCP HTTP server. It deliberately runs in
// an ordinary optimized integration build (no race, no debug build) and keeps
// the production pool, statement_timeout and synchronous_commit settings.
func TestAthleteProductHTTPPerformance(t *testing.T) {
	if os.Getenv("RAI_MODEL_PERF") != "1" {
		t.Skip("explicit synthetic model performance run")
	}
	f := setup(t)
	api, _ := b4Roles(t, f)
	activateSyntheticV2(t, f)
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	day := now.Format("2006-01-02")
	nowUs := now.UnixMicro()
	end := time.Date(now.Year(), now.Month(), now.Day(), 23, 59, 59, 0, loc).UnixMicro()
	acute, chronic, recorded := 12.0, 20.0, 12.0
	state := tl.StateResult{Schema: tl.StateSchemaV2, DatasetID: f.p.DatasetID, Generation: 1, Model: tl.StateAlgorithmV2, Version: tl.VersionV2, ConfigHash: tl.ConfigHashV2(), Scope: tl.ScopeStateV2For("synthetic-v2"), Unit: tl.Unit, TrendUnit: tl.Unit + "/day", Days: []tl.DailyState{{Day: day, RecordedLoad: &recorded, KnownRecordedLoad: recorded, RecordedWorkouts: 1, Quality: "complete", Acute: tl.KernelValue{Value: &acute}, Chronic: tl.KernelValue{Value: &chronic}, Balance: ptrFloat(acute - chronic)}}}
	for i := 1; i < 90; i++ {
		daily := state.Days[0]
		daily.Day = now.AddDate(0, 0, -i).Format(time.DateOnly)
		state.Days = append(state.Days, daily)
	}
	seedV2Artifact(t, f, tl.ScopeStateV2For("synthetic-v2"), "load_state", tl.StateSchemaV2, tl.StateAlgorithmV2, tl.VersionV2, "load_state", day, state)
	ability := pf.Result{Schema: pf.SchemaV2, DatasetID: f.p.DatasetID, Generation: 1, Model: pf.AbilityModel, Version: pf.VersionV2, ConfigHash: pf.ConfigHashV2(), AsOf: nowUs, View: "latest", EvidenceMode: pf.ModeDemonstratedV2, Status: "available", Freshness: "fresh", Stability: "stable", Support: pf.Support{IndependentWorkouts: 6, EffectiveWorkouts: 6, HistorySpan: 30, LatestEvidence: nowUs, QualitySupport: 1}, Ability: &pf.Ability{Score: 50, V60: 3, Gamma: .9, Retention: .8, DisplayScore: 50}}
	seedV2Artifact(t, f, pf.ScopeFor("synthetic-v2"), "performance_state_v2", pf.SchemaV2, pf.CurveModel, pf.VersionV2, "ability", day, ability)
	externalValue := 49.0
	external := externalResult(f.p.DatasetID, externalValue, nowUs)
	seedV2Artifact(t, f, ve.ScopeFor("synthetic-v2"), "vo2max_external_projection", external.Schema, ve.Algorithm, "v1", "model_product_v2", day, external)
	raiVO2 := syntheticCalculatedVO2(t, f.p.DatasetID, now)
	seedV2Artifact(t, f, vo2.ScopeFor("synthetic-v2"), vo2.ArtifactKey, vo2.Schema, vo2.Model, vo2.Version, vo2.Family, day, raiVO2)
	seedRecoveryReadiness(t, f, day, nowUs, end)

	ctx := context.Background()
	counter := &queryCounter{readers: make(map[string][]any)}
	serve, err := postgres.OpenWithTracer(ctx, api.Pool.Config().ConnString(), 8, counter)
	if err != nil {
		t.Fatal(err)
	}
	defer serve.Pool.Close()
	handler := httpapi.New(serve, analysisLog)
	server := httptest.NewServer(handler)
	defer server.Close()
	client := &http.Client{Timeout: 5 * time.Second}
	do := func(path string) (time.Duration, int64, int) {
		time.Sleep(1100 * time.Millisecond)
		counter.selects.Store(0)
		start := time.Now()
		req, _ := http.NewRequest(http.MethodGet, server.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			t.Logf("HTTP %d body=%s", resp.StatusCode, b)
			return time.Since(start), counter.selects.Load(), resp.StatusCode
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return time.Since(start), counter.selects.Load(), resp.StatusCode
	}
	for i := 0; i < 3; i++ {
		if d, n, code := do("/v2/analysis/athlete"); code != 200 || n > 6 {
			t.Fatalf("warmup product code=%d selects=%d duration=%s", code, n, d)
		}
	}
	product := make([]time.Duration, 0, 30)
	var maxSelects int64
	for i := 0; i < 30; i++ {
		d, n, code := do("/v2/analysis/athlete")
		if code != 200 {
			t.Fatalf("product code=%d", code)
		}
		if n > maxSelects {
			maxSelects = n
		}
		product = append(product, d)
	}
	// A 90-point history page shares one published artifact; this is a real
	// query/page-shape exercise, not 90 separate SQL round trips.
	var runID string
	if err = f.s.Pool.QueryRow(ctx, `SELECT id::text FROM analysis_runs WHERE dataset_id=$1 AND scope_key=$2 ORDER BY started_at DESC LIMIT 1`, f.p.DatasetID, tl.ScopeStateV2For("synthetic-v2")).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 90; i++ {
		dd := now.AddDate(0, 0, -i).Format("2006-01-02")
		if _, err = f.s.Pool.Exec(ctx, `INSERT INTO analysis_publications(dataset_id,scope_key,metric_family,day,published_run_id,generation) VALUES($1,$2,'load_state',$3::date,$4,1) ON CONFLICT(dataset_id,scope_key,metric_family,day) DO UPDATE SET published_run_id=EXCLUDED.published_run_id`, f.p.DatasetID, tl.ScopeStateV2For("synthetic-v2"), dd, runID); err != nil {
			t.Fatal(err)
		}
	}
	from := now.AddDate(0, 0, -89).Format("2006-01-02")
	historyPath := fmt.Sprintf("/v2/analysis/athlete/history?metric=trainingLoad&from=%s&to=%s&limit=90", from, day)
	for i := 0; i < 3; i++ {
		if _, n, code := do(historyPath); code != 200 || n > 8 {
			t.Fatalf("history warmup code=%d selects=%d", code, n)
		}
	}
	history := make([]time.Duration, 0, 30)
	var historyMaxSelects int64
	for i := 0; i < 30; i++ {
		d, n, code := do(historyPath)
		if code != 200 {
			t.Fatalf("history code=%d", code)
		}
		if n > historyMaxSelects {
			historyMaxSelects = n
		}
		history = append(history, d)
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	p50, p95 := percentile(product, .50), percentile(product, .95)
	h50, h95 := percentile(history, .50), percentile(history, .95)
	evidence := map[string]any{"schema": "rai.model_completion.performance/1", "transport": "loopback-tcp", "warmup": 3, "attempts": 30, "product": map[string]any{"p50_ms": p50.Seconds() * 1000, "p95_ms": p95.Seconds() * 1000, "max_ms": product[len(product)-1].Seconds() * 1000, "max_selects": maxSelects, "budget_ms": 500, "budget_selects": 6}, "history90": map[string]any{"p50_ms": h50.Seconds() * 1000, "p95_ms": h95.Seconds() * 1000, "max_ms": history[len(history)-1].Seconds() * 1000, "max_selects": historyMaxSelects, "budget_ms": 750, "budget_selects": 8}, "allocated_bytes": ms.TotalAlloc, "go_version": runtime.Version(), "optimized": true}
	b, _ := json.Marshal(evidence)
	t.Logf("MODEL_PERF %s", b)
	if maxSelects > 6 || p95 > 500*time.Millisecond {
		t.Fatalf("product budget failed selects=%d p95=%s", maxSelects, p95)
	}
	if historyMaxSelects > 8 || h95 > 750*time.Millisecond {
		t.Fatalf("history budget failed selects=%d p95=%s", historyMaxSelects, h95)
	}
	if snap, err := f.s.ReadAthleteProduct(ctx, f.p, now); err != nil || snap.SchemaVersion != am.Schema2 {
		t.Fatalf("product snapshot after perf: %v", err)
	}
	counter.mu.Lock()
	readers := counter.readers
	counter.mu.Unlock()
	if len(readers) != 2 {
		t.Fatalf("expected exact current and history read statements, found %d", len(readers))
	}
	for sql, args := range readers {
		var plan []byte
		if err := serve.Pool.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+sql, args...).Scan(&plan); err != nil {
			t.Fatal("explain product reader", err)
		}
		name := "current"
		if strings.Contains(sql, "p.day BETWEEN") {
			name = "history"
		}
		t.Logf("MC_T10_PRODUCT_READER_PLAN %s %s", name, plan)
	}
}

func externalResult(dataset string, value float64, nowUs int64) ve.Result {
	return ve.Result{Schema: ve.Schema, ModelVersion: ve.Model, Dataset: dataset, DatasetEpoch: "1", Generation: 1, Status: "available", Quality: "available", Value: &value, Unit: ve.Unit, Provider: ve.Apple, Selected: &ve.Selected{Start: nowUs - int64(time.Hour), End: nowUs - int64(time.Minute)}, AsOf: nowUs, CalculatedAt: nowUs, EvaluationAt: nowUs}
}

func seedRecoveryReadiness(t *testing.T, f *fixture, day string, nowUs, end int64) {
	t.Helper()
	score := 82.0
	recovery := rc.V2Result{Result: rc.Result{Schema: rc.SchemaV2, Dataset: f.p.DatasetID, DatasetEpoch: "1", Date: day, Timezone: "Asia/Shanghai", Status: "available", Quality: "complete", BaselineState: "established", Score: &score, ComponentCoverage: 1, Confidence: .5, ModelVersion: rc.ModelV2, ConfigHash: rc.DefaultV2Config("Asia/Shanghai").Hash(), Generation: 1, AsOf: nowUs, CapturedAt: nowUs, SDNNFreshness: rc.Freshness{LastEvent: &nowUs, LimitUs: int64(36 * time.Hour), State: "fresh"}, RHRFreshness: rc.Freshness{LastEvent: &nowUs, LimitUs: int64(36 * time.Hour), State: "fresh"}, SleepFreshness: rc.Freshness{LastEvent: &nowUs, LimitUs: int64(36 * time.Hour), State: "fresh"}}}
	seedV2Artifact(t, f, rc.ScopeV2For("synthetic-v2", day), "physiological_recovery", rc.SchemaV2, rc.Algorithm, "v2", "model_product_v2", day, recovery)
	baseline, recoveryScore, readyScore, penalty, burden := 20.0, 80.0, 70.0, .8, 10.0
	readiness := rd.Result{Date: day, Schema: rd.SchemaV3, Dataset: f.p.DatasetID, Generation: 1, ModelVersion: rd.ModelV3, ConfigHash: rd.DefaultV3Config("Asia/Shanghai").Hash(), Config: rd.DefaultV3Config("Asia/Shanghai"), InputHash: "x", CalendarHash: "x", AsOf: nowUs, EvaluationAt: nowUs, CapturedAt: nowUs, ValidBefore: end, Status: "available", Quality: "complete", Score: &readyScore, RecoveryScore: &recoveryScore, Burden: &burden, Baseline: rd.Baseline{Value: &baseline, Median: &baseline, TrainingDays: 20, SpanDays: 30, State: "established"}, Penalty: &penalty, Sync: rd.Sync{CommittedWorkouts: 6, LastCommitAt: nowUs}, Confidence: .5}
	seedV2Artifact(t, f, rd.ScopeV3For("synthetic-v2"), "training_readiness", rd.SchemaV3, rd.Algorithm, "v3", "model_product_v2", day, readiness)
}
