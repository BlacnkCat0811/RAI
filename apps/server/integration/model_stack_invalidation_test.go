//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"rai/server/internal/modelmanifest"
	"rai/server/internal/syncing"
)

// sleepPage is a daily sleep page of n facts, the first starting at first and
// one every step. n may be zero: the coverage heartbeat.
func sleepPage(t *testing.T, f *fixture, stream string, seq int64, prev *string, bootstrap, first, step int64, n int) ([]byte, string) {
	t.Helper()
	b := syncing.DailyBatch{Version: 1, DatasetID: f.enrollment.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch,
		StreamID: stream, BatchID: syncing.NewID(), ClientSeq: seq, PreviousBatchID: prev, SampleType: syncing.DailySleep,
		QueryFingerprint: syncing.DailyFingerprint(syncing.DailySleep, bootstrap), BootstrapStartUs: bootstrap, ObservedAtUs: time.Now().UnixMicro(),
		HasMore: false, Facts: []syncing.DailyFact{}, DeletedUUIDs: []string{}}
	for i := 0; i < n; i++ {
		category := i % 6
		at := first + int64(i)*step
		b.Facts = append(b.Facts, syncing.DailyFact{UUID: syncing.NewID(), StartUs: at, EndUs: at + 90*1_000_000, Category: &category,
			Source:   syncing.HealthKitSource{BundleID: "com.apple.health.synthetic", Name: "Synthetic Watch", Version: "26.1", ProductType: "Watch7,1", OperatingSystemVersion: "26.1"},
			Metadata: map[string]any{"HKTimeZone": "Asia/Shanghai"}})
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return raw, b.BatchID
}

// Schema 55: one daily page costs one invalidation and one day job however
// many facts it carries; the coverage row no longer invalidates history; and
// day releases a late fact did invalidate are queued again by the worker tick
// and become readable history once more.
func TestDailyPageInvalidatesOncePerTransactionAndHistoryIsRebuilt(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	today, _ := runStackDay(t, f, []stackDayRun{{id: syncing.NewID(), day: -3, version: "11.0", hr: steadyHR}}, stackDayOptions{})
	stack := modelmanifest.StackID()
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := f.s.Pool.QueryRow(ctx, q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	jobs := func(state string) int {
		return count(`SELECT count(*) FROM jobs WHERE dataset_id=$1 AND kind=$2 AND ($3='' OR state=$3)`, f.p.DatasetID, modelmanifest.StackJobKind, state)
	}
	dirtyHistory := func() int {
		return count(`SELECT count(*) FROM dirty_scopes WHERE dataset_id=$1 AND generation<>clean_generation
 AND scope_key LIKE $2 AND scope_key<>$3`, f.p.DatasetID, "model/"+stack+"/athlete/day/v2/%", modelmanifest.DayScope(stack, today.Format(time.DateOnly)))
	}
	// Two earlier days were published while they were "today".
	for k := 1; k <= 2; k++ {
		if _, err := f.s.Pool.Exec(ctx, `SELECT rai_schedule_model_day($1,$2,$3::date,clock_timestamp())`, f.p.DatasetID, stack, today.AddDate(0, 0, -k).Format(time.DateOnly)); err != nil {
			t.Fatal(err)
		}
	}
	if n := processStackJobs(t, f); n != 2 {
		t.Fatalf("published %d earlier days, want 2", n)
	}
	if dirtyHistory() != 0 || jobs("queued") != 0 {
		t.Fatalf("setup left dirty=%d queued=%d", dirtyHistory(), jobs("queued"))
	}

	// Last night's sleep: 40 facts starting 23:00 yesterday.
	stream, bootstrap := syncing.NewID(), today.AddDate(0, 0, -90).UnixMicro()
	before := jobs("")
	raw, batch := sleepPage(t, f, stream, 1, nil, bootstrap, today.Add(-time.Hour).UnixMicro(), 120*1_000_000, 40)
	if _, _, err := commitDaily(t, f, raw); err != nil {
		t.Fatal(err)
	}
	if added := jobs("") - before; added != 1 {
		t.Fatalf("a 40-fact page queued %d day jobs, want 1", added)
	}
	if left := count(`SELECT count(*) FROM model_stack_pending_invalidations`); left != 0 {
		t.Fatalf("%d pending invalidations survived the commit", left)
	}
	if dirtyHistory() != 2 {
		t.Fatalf("late sleep invalidated %d earlier days, want 2", dirtyHistory())
	}

	// The tick leaves history alone while today's job is waiting, then queues
	// the invalidated days; processing them makes history clean again.
	if err := f.s.AdvanceAnalysisDay(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if jobs("queued") != 1 {
		t.Fatalf("history was queued behind a waiting day job: queued=%d", jobs("queued"))
	}
	if n := processStackJobs(t, f); n != 1 {
		t.Fatalf("today's release: %d succeeded", n)
	}
	if err := f.s.AdvanceAnalysisDay(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if jobs("queued") != 2 {
		t.Fatalf("tick queued %d history days, want 2", jobs("queued"))
	}
	if n := processStackJobs(t, f); n != 2 || dirtyHistory() != 0 {
		t.Fatalf("history rebuild: %d succeeded, %d still dirty", n, dirtyHistory())
	}
	// A second tick has nothing left to queue.
	if err := f.s.AdvanceAnalysisDay(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if jobs("queued") != 0 {
		t.Fatalf("idle tick queued %d jobs", jobs("queued"))
	}

	// The coverage heartbeat (an empty page) reschedules today only.
	before = jobs("")
	raw, _ = sleepPage(t, f, stream, 2, &batch, bootstrap, 0, 0, 0)
	if _, _, err := commitDaily(t, f, raw); err != nil {
		t.Fatal(err)
	}
	if added := jobs("") - before; added != 1 || dirtyHistory() != 0 {
		t.Fatalf("heartbeat queued %d jobs and invalidated %d earlier days, want 1 and 0", added, dirtyHistory())
	}
}

// A full page of history spread over 90 days still commits well inside the
// 5 s transaction budget with an enrolled stack and existing day scopes.
func TestDailyHistoryPageWithEnrolledStackStaysInBudget(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	today, _ := runStackDay(t, f, []stackDayRun{{id: syncing.NewID(), day: -2, version: "11.0", hr: steadyHR}}, stackDayOptions{})
	stack := modelmanifest.StackID()
	for k := 1; k <= 120; k++ {
		if _, err := f.s.Pool.Exec(ctx, `INSERT INTO dirty_scopes(dataset_id,scope_key,generation,clean_generation,dirty_from,dirty_through)
 VALUES($1,$2,1,1,$3,$3) ON CONFLICT DO NOTHING`, f.p.DatasetID, modelmanifest.DayScope(stack, today.AddDate(0, 0, -k).Format(time.DateOnly)), today.AddDate(0, 0, -k)); err != nil {
			t.Fatal(err)
		}
	}
	var before, after int
	q := `SELECT count(*) FROM jobs WHERE dataset_id=$1 AND kind=$2`
	if err := f.s.Pool.QueryRow(ctx, q, f.p.DatasetID, modelmanifest.StackJobKind).Scan(&before); err != nil {
		t.Fatal(err)
	}
	bootstrap := today.AddDate(0, 0, -90).UnixMicro()
	raw, _ := sleepPage(t, f, syncing.NewID(), 1, nil, bootstrap, bootstrap, (90*24*time.Hour).Microseconds()/int64(syncing.MaxDailyChanges), syncing.MaxDailyChanges)
	_, elapsed, err := commitDaily(t, f, raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.Pool.QueryRow(ctx, q, f.p.DatasetID, modelmanifest.StackJobKind).Scan(&after); err != nil {
		t.Fatal(err)
	}
	t.Logf("500 facts over 90 days with an enrolled stack: committed in %v, %d day job(s)", elapsed, after-before)
	if after-before != 1 || elapsed > 2*time.Second {
		t.Fatalf("history page: %d day jobs in %v, want 1 job under 2s", after-before, elapsed)
	}
}
