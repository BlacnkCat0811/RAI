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

// The lightweight freshness read equals the full-snapshot freshness for a
// Watch-shaped run (series, route, statistics) and a raw-sample run, before
// and after the day release publishes their load.
func TestWorkoutFreshnessMatchesFullLoader(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	loc, _ := time.LoadLocation("Asia/Shanghai")
	day := time.Now().In(loc).AddDate(0, 0, -2)
	rich, plain := syncing.NewID(), syncing.NewID()
	acceptIngest(t, f, f.p, realisticRun(f, rich, time.Date(day.Year(), day.Month(), day.Day(), 7, 0, 0, 0, loc), 35, 1))
	processIngest(t, f.s)
	acceptIngest(t, f, f.p, stackRun(t, f, plain, time.Date(day.Year(), day.Month(), day.Day(), 18, 0, 0, 0, loc), "11.0", steadyHR))
	processIngest(t, f.s)
	compare := func(stage string) {
		for _, id := range []string{rich, plain} {
			light, err := f.s.WorkoutFreshness(ctx, f.p, id)
			if err != nil {
				t.Fatal(err)
			}
			full, err := f.s.ReferenceWorkoutFreshness(ctx, f.p, id)
			if err != nil {
				t.Fatal(err)
			}
			a, _ := json.Marshal(light)
			b, _ := json.Marshal(full)
			if string(a) != string(b) {
				t.Fatalf("%s freshness differs:\n light=%s\n  full=%s", stage, a, b)
			}
		}
	}
	compare("before_release")
	runStackDay(t, f, nil, stackDayOptions{})
	hash, _ := modelmanifest.CurrentHash()
	setModelChannel(t, f, "active", modelmanifest.StackID(), hash)
	compare("after_release")
	if _, err := f.s.WorkoutFreshness(ctx, f.p, syncing.NewID()); err == nil {
		t.Fatal("unknown workout must be not found")
	}
}

// Today's day release is claimed before an older queued history day, and a
// release whose loads did not change rewrites no per-Workout result row.
func TestTodaysReleaseFirstAndUnchangedLoadsNotRewritten(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	today, _ := runStackDay(t, f, []stackDayRun{{id: syncing.NewID(), day: -1, version: "11.0", hr: steadyHR}, {id: syncing.NewID(), day: -3, version: "11.0", hr: steadyHR}}, stackDayOptions{})
	stack := modelmanifest.StackID()
	updated := func() map[string]time.Time {
		rows, err := f.s.Pool.Query(ctx, `SELECT workout_uuid::text,updated_at FROM model_workout_results WHERE dataset_id=$1 AND stack_id=$2`, f.p.DatasetID, stack)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]time.Time{}
		for rows.Next() {
			var id string
			var at time.Time
			if err := rows.Scan(&id, &at); err != nil {
				t.Fatal(err)
			}
			out[id] = at
		}
		return out
	}
	before := updated()
	if len(before) != 2 {
		t.Fatalf("results %d", len(before))
	}
	yesterday := today.AddDate(0, 0, -1).Format(time.DateOnly)
	for _, d := range []string{yesterday, today.Format(time.DateOnly)} {
		if _, err := f.s.Pool.Exec(ctx, `SELECT rai_schedule_model_day($1,$2,$3::date,clock_timestamp())`, f.p.DatasetID, stack, d); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.s.Pool.Exec(ctx, `UPDATE jobs SET run_after=now()-interval '1 minute' WHERE dataset_id=$1 AND state='queued' AND scope_key=$2`, f.p.DatasetID, modelmanifest.DayScope(stack, yesterday)); err != nil {
		t.Fatal(err)
	}
	lease, err := f.s.Claim(ctx, []string{modelmanifest.StackJobKind})
	if err != nil {
		t.Fatal(err)
	}
	if lease.ScopeKey != modelmanifest.DayScope(stack, today.Format(time.DateOnly)) {
		t.Fatalf("claimed %s before today's release", lease.ScopeKey)
	}
	if status, err := f.s.ProcessModelStack(ctx, lease, time.Now()); err != nil || status != "SUCCEEDED" {
		t.Fatal(status, err)
	}
	after := updated()
	for id, at := range before {
		if !after[id].Equal(at) {
			t.Fatalf("unchanged load of %s was rewritten", id)
		}
	}
}

// Cached VO2 evidence and training effects reproduce the uncached day release:
// a warm release, and a release after the caches are cleared, publish the
// same VO2 result and the same per-Workout training effects as the first.
func TestWorkoutCachesReproduceUncachedResults(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	for d := 20; d >= 1; d -= 2 {
		at := time.Date(now.Year(), now.Month(), now.Day(), 7, 0, 0, 0, loc).AddDate(0, 0, -d)
		acceptIngest(t, f, f.p, realisticRun(f, syncing.NewID(), at, 40+d, d))
		processIngest(t, f.s)
	}
	today, _ := runStackDay(t, f, nil, stackDayOptions{})
	stack := modelmanifest.StackID()
	scope := modelmanifest.DayScope(stack, today.Format(time.DateOnly))
	capture := func() (string, string) {
		var vo2 string
		if err := f.s.Pool.QueryRow(ctx, `SELECT (a.payload-'generation'-'asOfUs'-'computedAtUs')::text FROM analysis_publications p
 JOIN analysis_artifacts a ON a.dataset_id=p.dataset_id AND a.run_id=p.published_run_id AND a.artifact_key='vo2max_submax'
 WHERE p.dataset_id=$1 AND p.scope_key=$2`, f.p.DatasetID, scope).Scan(&vo2); err != nil {
			t.Fatal(err)
		}
		var effects string
		if err := f.s.Pool.QueryRow(ctx, `SELECT COALESCE(jsonb_object_agg(workout_uuid::text,load->'trainingEffect' ORDER BY workout_uuid),'{}')::text
 FROM model_workout_results WHERE dataset_id=$1 AND stack_id=$2`, f.p.DatasetID, stack).Scan(&effects); err != nil {
			t.Fatal(err)
		}
		return vo2, effects
	}
	rerun := func() {
		if _, err := f.s.Pool.Exec(ctx, `SELECT rai_schedule_model_day($1,$2,$3::date,clock_timestamp())`, f.p.DatasetID, stack, today.Format(time.DateOnly)); err != nil {
			t.Fatal(err)
		}
		lease, err := f.s.Claim(ctx, []string{modelmanifest.StackJobKind})
		if err != nil {
			t.Fatal(err)
		}
		if status, err := f.s.ProcessModelStack(ctx, lease, time.Now()); err != nil || status != "SUCCEEDED" {
			t.Fatal(status, err)
		}
	}
	vo2A, effectsA := capture()
	var withEvidence, withEffect int
	if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE vo2_evidence IS NOT NULL AND vo2_evidence<>'null'::jsonb),count(*) FILTER (WHERE effect_key IS NOT NULL)
 FROM model_workout_results WHERE dataset_id=$1 AND stack_id=$2`, f.p.DatasetID, stack).Scan(&withEvidence, &withEffect); err != nil {
		t.Fatal(err)
	}
	if withEvidence == 0 || withEffect == 0 {
		t.Fatalf("fixture does not exercise the caches: vo2=%d effect=%d", withEvidence, withEffect)
	}
	rerun()
	vo2B, effectsB := capture()
	if _, err := f.s.Pool.Exec(ctx, `UPDATE model_workout_results SET vo2_key=NULL,vo2_evidence=NULL,effect_key=NULL,effect=NULL WHERE dataset_id=$1`, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}
	rerun()
	vo2C, effectsC := capture()
	if vo2A != vo2B || vo2A != vo2C {
		t.Fatalf("VO2 differs with caches:\n A=%s\n B=%s\n C=%s", vo2A, vo2B, vo2C)
	}
	if effectsA != effectsB || effectsA != effectsC {
		t.Fatalf("training effects differ with caches:\n A=%s\n B=%s\n C=%s", effectsA, effectsB, effectsC)
	}
}

// A day scope tracks the earliest change since its last clean publication,
// and only trusts it when every generation bump went through invalidation.
func TestDayScopeStaleFromTracksChangesSincePublication(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	today, _ := runStackDay(t, f, []stackDayRun{{id: syncing.NewID(), day: -1, version: "11.0", hr: steadyHR}}, stackDayOptions{})
	scope := modelmanifest.DayScope(modelmanifest.StackID(), today.Format(time.DateOnly))
	read := func() (bool, *time.Time) {
		var trusted bool
		var from *time.Time
		if err := f.s.Pool.QueryRow(ctx, `SELECT stale_mark IS NOT DISTINCT FROM generation,stale_from FROM dirty_scopes WHERE dataset_id=$1 AND scope_key=$2`, f.p.DatasetID, scope).Scan(&trusted, &from); err != nil {
			t.Fatal(err)
		}
		return trusted, from
	}
	late := time.Now().Add(-time.Minute).Truncate(time.Microsecond)
	early := late.Add(-2 * time.Hour)
	invalidate := func(at time.Time) {
		if _, err := f.s.Pool.Exec(ctx, `SELECT rai_model_stack_invalidate($1,$2,$2,NULL)`, f.p.DatasetID, at); err != nil {
			t.Fatal(err)
		}
	}
	invalidate(late)
	if trusted, from := read(); !trusted || from == nil || !from.Equal(late) {
		t.Fatalf("first change since publication: trusted=%v from=%v", trusted, from)
	}
	invalidate(early)
	if trusted, from := read(); !trusted || from == nil || !from.Equal(early) {
		t.Fatalf("earliest change wins: trusted=%v from=%v", trusted, from)
	}
	invalidate(late)
	if _, from := read(); from == nil || !from.Equal(early) {
		t.Fatalf("a later change must not move it forward: %v", from)
	}
	// A bump that bypasses invalidation (an explicit reanalysis) is untrusted.
	if _, err := f.s.Pool.Exec(ctx, `UPDATE dirty_scopes SET generation=generation+1 WHERE dataset_id=$1 AND scope_key=$2`, f.p.DatasetID, scope); err != nil {
		t.Fatal(err)
	}
	if trusted, _ := read(); trusted {
		t.Fatal("bypassing bump must leave the marker untrusted")
	}
	invalidate(late)
	if _, from := read(); from != nil {
		t.Fatalf("untrusted marker must stay unknown until the next publication: %v", from)
	}
}

// The statistics batch returns, for every running Workout in the window, the
// same distance distribution as the Workout's detail, and serves it again
// from memory with the same result.
func TestDistanceDistributionsMatchDetail(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	loc, _ := time.LoadLocation("Asia/Shanghai")
	now := time.Now().In(loc)
	var ids []string
	for d := 6; d >= 2; d -= 2 {
		id := syncing.NewID()
		ids = append(ids, id)
		acceptIngest(t, f, f.p, realisticRun(f, id, time.Date(now.Year(), now.Month(), now.Day(), 7, 0, 0, 0, loc).AddDate(0, 0, -d), 30+d, d))
		processIngest(t, f.s)
	}
	runStackDay(t, f, nil, stackDayOptions{})
	hash, _ := modelmanifest.CurrentHash()
	setModelChannel(t, f, "active", modelmanifest.StackID(), hash)
	from, to := now.AddDate(0, 0, -10), now
	for round := 0; round < 2; round++ {
		items, err := f.s.WorkoutDistanceDistributions(ctx, f.p, from, to)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != len(ids) {
			t.Fatalf("items %d want %d", len(items), len(ids))
		}
		for i, item := range items {
			detail, err := f.s.WorkoutDetail(ctx, f.p, item.WorkoutID)
			if err != nil {
				t.Fatal(err)
			}
			a, _ := json.Marshal(item.Distribution)
			b, _ := json.Marshal(detail.DistanceDistribution)
			if item.WorkoutID != ids[i] || item.Status != "available" || string(a) != string(b) {
				t.Fatalf("round %d workout %d: status=%s\n batch=%s\ndetail=%s", round, i, item.Status, a, b)
			}
		}
	}
	if _, err := f.s.WorkoutDistanceDistributions(ctx, f.p, from, from.AddDate(0, 0, 60)); err == nil {
		t.Fatal("window above the bound must be rejected")
	}
}

// While a sync has today's release recomputed, the athlete read serves the
// last complete release whole and marks it updating; the new publication
// clears the mark.
func TestAthleteReadServesPreviousReleaseWhileRecomputing(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	today, _ := runStackDay(t, f, []stackDayRun{{id: syncing.NewID(), day: -1, version: "11.0", hr: steadyHR}, {id: syncing.NewID(), day: -3, version: "11.0", hr: steadyHR}}, stackDayOptions{})
	hash, _ := modelmanifest.CurrentHash()
	stack := modelmanifest.StackID()
	setModelChannel(t, f, "active", stack, hash)
	clean, err := f.s.ReadAthleteProduct(ctx, f.p, time.Now().UTC())
	if err != nil || clean.Updating || clean.TrainingLoad.Value == nil {
		t.Fatalf("clean release: updating=%v value=%v err=%v", clean.Updating, clean.TrainingLoad.Value, err)
	}
	if _, err := f.s.Pool.Exec(ctx, `SELECT rai_model_stack_invalidate($1,clock_timestamp(),clock_timestamp(),NULL)`, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}
	stale, err := f.s.ReadAthleteProduct(ctx, f.p, time.Now().UTC())
	if err != nil || !stale.Updating {
		t.Fatalf("recomputing release not marked: %v %v", stale.Updating, err)
	}
	a, _ := json.Marshal(clean.TrainingLoad.Value)
	b, _ := json.Marshal(stale.TrainingLoad.Value)
	if string(a) != string(b) {
		t.Fatalf("served release differs:\n%s\n%s", a, b)
	}
	// History and training inputs stay clean-only.
	if page, err := f.s.ReadAthleteHistory(ctx, f.p, "trainingLoad", today.Format(time.DateOnly), today.Format(time.DateOnly), 10, "", "revised"); err != nil || len(page.Points) != 0 && page.Points[0].Value != nil {
		t.Fatalf("history must not serve the invalidated day: %+v %v", page.Points, err)
	}
	lease, err := f.s.Claim(ctx, []string{modelmanifest.StackJobKind})
	if err != nil {
		t.Fatal(err)
	}
	if status, err := f.s.ProcessModelStack(ctx, lease, time.Now()); err != nil || status != "SUCCEEDED" {
		t.Fatal(status, err)
	}
	fresh, err := f.s.ReadAthleteProduct(ctx, f.p, time.Now().UTC())
	if err != nil || fresh.Updating {
		t.Fatalf("published release still updating: %v %v", fresh.Updating, err)
	}
}
