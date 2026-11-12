//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/modelmanifest"
	"rai/server/internal/syncing"
)

func TestFragmentedWorkoutSeriesPublishesListAndDetail(t *testing.T) {
	f := setup(t)
	id := syncing.NewID()
	runs := []stackDayRun{{id, -1, "11.0", steadyHR}}
	runStackDay(t, f, runs, stackDayOptions{payloads: map[string]func(*fixture, string, time.Time) []byte{
		id: func(f *fixture, id string, start time.Time) []byte {
			var p map[string]any
			if err := json.Unmarshal(stackRun(t, f, id, start, "11.0", steadyHR), &p); err != nil {
				t.Fatal(err)
			}
			series := []any{}
			for i := 0; i < 2433; i++ {
				a, b := int64(i)*4_000_000, int64(i+1)*4_000_000
				series = append(series, map[string]any{"startDeltaUs": a, "endDeltaUs": b, "value": 150, "points": [][]any{{a, b, 150}}})
			}
			p["endUs"] = start.Add(2433 * 4 * time.Second).UnixMicro()
			p["workout"] = map[string]any{"activityType": 37, "durationUs": 2433 * 4_000_000}
			p["streams"] = []any{map[string]any{"type": v6HRType, "unit": "count/min", "status": "complete", "series": series}}
			b, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			return b
		},
	}})
	hash, _ := modelmanifest.CurrentHash()
	setModelChannel(t, f, "active", modelmanifest.StackID(), hash)
	assertWorkoutAnalysisStatus(t, f, id, "published")
	var count int
	if err := f.s.Pool.QueryRow(context.Background(), `SELECT count(*) FROM workout_samples a JOIN series_uploads u USING(dataset_id,sample_type,sample_uuid)
 WHERE a.dataset_id=$1 AND a.workout_uuid=$2 AND u.state='sealed'`, f.p.DatasetID, id).Scan(&count); err != nil || count != 2433 {
		t.Fatalf("sealed series count=%d err=%v", count, err)
	}
}

func assertWorkoutAnalysisStatus(t *testing.T, f *fixture, id, want string) {
	t.Helper()
	page, err := f.s.ProductWorkouts(context.Background(), f.p, "", 50, id)
	if err != nil || len(page.Items) != 1 || page.Items[0].AnalysisStatus != want {
		t.Fatalf("list status want=%s page=%+v err=%v", want, page, err)
	}
	d, err := f.s.WorkoutDetail(context.Background(), f.p, id)
	if err != nil || d.Workout.AnalysisStatus != want {
		t.Fatalf("detail status want=%s got=%s err=%v", want, d.Workout.AnalysisStatus, err)
	}
}

func TestWorkoutAnalysisFailureIsPublishedAndClearedOnRecovery(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := syncing.NewID()
	today, _ := runStackDay(t, f, []stackDayRun{{id, -1, "11.0", steadyHR}}, stackDayOptions{})
	hash, _ := modelmanifest.CurrentHash()
	setModelChannel(t, f, "active", modelmanifest.StackID(), hash)
	api, worker, _ := runtimeRoles(t, f)
	apiFixture, workerFixture := *f, *f
	apiFixture.s, workerFixture.s = api, worker
	assertWorkoutAnalysisStatus(t, &apiFixture, id, "published")
	// Synthetic invalid input in a newer fact generation must mask the old
	// successful load, while the other day products continue publishing.
	if _, err := f.s.Pool.Exec(ctx, `UPDATE workouts SET payload=jsonb_set(payload,'{workout,totalDistance}','{"value":-1,"unit":"m"}')
 WHERE dataset_id=$1 AND workout_uuid=$2`, f.p.DatasetID, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(ctx, `SELECT rai_analysis_dirty($1,$2,'analysis.workout',event_start,event_end) FROM workouts WHERE dataset_id=$1 AND workout_uuid=$3`,
		f.p.DatasetID, "workout:HKWorkoutTypeIdentifier:"+id, id); err != nil {
		t.Fatal(err)
	}
	if processStackJobs(t, &workerFixture) == 0 {
		t.Fatal("failed Workout blocked the day release")
	}
	assertWorkoutAnalysisStatus(t, &apiFixture, id, "failed")
	var code string
	if err := f.s.Pool.QueryRow(ctx, `SELECT error_code FROM model_workout_failures WHERE dataset_id=$1 AND workout_uuid=$2`, f.p.DatasetID, id).Scan(&code); err != nil || code != "analysis_invalid_input" {
		t.Fatalf("failure code=%s err=%v", code, err)
	}
	// Restoring these synthetic facts and rerunning today's release resolves
	// the failed generation without a history backfill or resynchronization.
	if _, err := f.s.Pool.Exec(ctx, `UPDATE workouts SET payload=payload #- '{workout,totalDistance}' WHERE dataset_id=$1 AND workout_uuid=$2`, f.p.DatasetID, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Pool.Exec(ctx, `SELECT rai_schedule_model_day($1,$2,$3::date,clock_timestamp())`, f.p.DatasetID, modelmanifest.StackID(), today.Format(time.DateOnly)); err != nil {
		t.Fatal(err)
	}
	if processStackJobs(t, &workerFixture) == 0 {
		t.Fatal("repaired Workout did not publish")
	}
	assertWorkoutAnalysisStatus(t, &apiFixture, id, "published")
	if err := f.s.Pool.QueryRow(ctx, `SELECT error_code FROM model_workout_failures WHERE dataset_id=$1 AND workout_uuid=$2`, f.p.DatasetID, id).Scan(&code); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("resolved failure remains: %v", err)
	}
}

func TestModelQueueFreezesPreviousAndSkipsUnsupportedLiveStack(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	today, _ := runStackDay(t, f, nil, stackDayOptions{})
	if _, err := f.s.Pool.Exec(ctx, `SELECT rai_schedule_model_day($1,$2,$3::date,clock_timestamp())`, f.p.DatasetID, modelmanifest.StackID(), today.AddDate(0, 0, -1).Format(time.DateOnly)); err != nil {
		t.Fatal(err)
	}
	processStackJobs(t, f)
	setModelChannel(t, f, "active", "rai-v2-aaaaaaaaaaaaaaaa", strings.Repeat("a", 64))
	setModelChannel(t, f, "previous", "rai-v2-bbbbbbbbbbbbbbbb", strings.Repeat("b", 64))
	if _, err := f.s.Pool.Exec(ctx, `SELECT rai_model_stack_invalidate($1,clock_timestamp()-interval '1 day',clock_timestamp(),NULL)`, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE dataset_id=$1 AND scope_key LIKE 'model/rai-v2-bbbbbbbbbbbbbbbb/%'`, f.p.DatasetID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("previous stack still queued by invalidation: count=%d err=%v", n, err)
	}
	if _, err := f.s.Pool.Exec(ctx, `SELECT rai_model_stack_tick($1,$2)`, f.p.DatasetID, today); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE dataset_id=$1 AND scope_key LIKE 'model/rai-v2-bbbbbbbbbbbbbbbb/%'`, f.p.DatasetID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("previous stack still queued by tick: count=%d err=%v", n, err)
	}
	// Simulate a queued job from before the fix, plus the 63-style historical
	// failed row. Only the queued obsolete work should be retired.
	for i, state := range []string{"queued", "failed"} {
		if _, err := f.s.Pool.Exec(ctx, `INSERT INTO jobs(id,dataset_id,kind,scope_key,target_generation,state,last_error_code)
 VALUES($1,$2,$3,$4,$5,$6,$7)`, syncing.NewID(), f.p.DatasetID, modelmanifest.StackJobKind,
			fmt.Sprintf("model/rai-v2-bbbbbbbbbbbbbbbb/athlete/day/v2/%s", today.Format(time.DateOnly)), i+1, state, "analysis_unsupported_algorithm"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.s.RequeueExpired(ctx); err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"retired_model_stack", "analysis_unsupported_algorithm"} {
		if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE dataset_id=$1 AND scope_key LIKE 'model/rai-v2-bbbbbbbbbbbbbbbb/%' AND state='failed' AND last_error_code=$2`, f.p.DatasetID, code).Scan(&n); err != nil || n != 1 {
			t.Fatalf("obsolete job history code=%s count=%d err=%v", code, n, err)
		}
	}
	// The current candidate remains executable even while an unsupported active
	// stack has queued jobs. Its jobs must not be converted to unsupported errors.
	if processStackJobs(t, f) == 0 {
		t.Fatal("unsupported active stack blocked the candidate")
	}
	if _, err := f.s.Claim(ctx, []string{modelmanifest.StackJobKind}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("worker claimed unsupported active stack: %v", err)
	}
	if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE dataset_id=$1 AND scope_key LIKE 'model/rai-v2-aaaaaaaaaaaaaaaa/%' AND state='queued' AND attempts=0`, f.p.DatasetID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("unsupported live jobs were lost: count=%d err=%v", n, err)
	}
	if err := f.s.AdvanceAnalysisDay(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if processStackJobs(t, f) != 1 {
		t.Fatal("unsupported active stack blocked candidate history repair")
	}
	// A rollback can remove a stack from live channels while it is computing.
	// Its lease may finish as superseded but must not publish into previous.
	if _, err := f.s.Pool.Exec(ctx, `SELECT rai_schedule_model_day($1,$2,$3::date,clock_timestamp())`, f.p.DatasetID, modelmanifest.StackID(), today.Format(time.DateOnly)); err != nil {
		t.Fatal(err)
	}
	l, err := f.s.Claim(ctx, []string{modelmanifest.StackJobKind})
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := modelmanifest.CurrentHash()
	setModelChannel(t, f, "previous", modelmanifest.StackID(), hash)
	if _, err := f.s.Pool.Exec(ctx, `DELETE FROM analysis_model_channels WHERE dataset_id=$1 AND channel='candidate'`, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}
	if status, err := f.s.ProcessModelStack(ctx, l, time.Now()); err != nil || status != "SUPERSEDED" {
		t.Fatalf("retired in-flight stack: status=%s err=%v", status, err)
	}
}
