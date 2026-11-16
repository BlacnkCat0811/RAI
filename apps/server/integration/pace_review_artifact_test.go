//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"rai/server/internal/modelmanifest"
	"rai/server/internal/workoutdetail"
)

// The day release precomputes the pace review of each Workout starting that
// day. The stored review must be exactly what the live replay returns, and a
// dirty day must fall back to the replay instead of serving a stale review.
func TestPaceReviewPrecomputedByDayReleaseMatchesReplay(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	older := "f62f735b-fb16-43f9-b415-7a7041bca041"
	target := "f62f735b-fb16-43f9-b415-7a7041bca042"
	today, _ := runStackDay(t, f, []stackDayRun{{id: older, day: -6, version: "11.0", hr: steadyHR}, {id: target, day: -1, version: "11.0", hr: steadyHR}},
		stackDayOptions{payloads: map[string]func(*fixture, string, time.Time) []byte{older: stackPacePayload(t), target: stackPacePayload(t)}})
	hash, _ := modelmanifest.CurrentHash()
	setModelChannel(t, f, "active", modelmanifest.StackID(), hash)
	day := today.AddDate(0, 0, -1).Format(time.DateOnly)
	scope := modelmanifest.DayScope(modelmanifest.StackID(), day)
	// Past days are released by the history rebuild; release this one now.
	if _, err := f.s.Pool.Exec(ctx, `SELECT rai_schedule_model_day($1,$2,$3::date,clock_timestamp())`, f.p.DatasetID, modelmanifest.StackID(), day); err != nil {
		t.Fatal(err)
	}
	for {
		l, err := f.s.Claim(ctx, []string{modelmanifest.StackJobKind})
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.s.ProcessModelStack(ctx, l, time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	var payload []byte
	if err := f.s.Pool.QueryRow(ctx, `SELECT a.payload FROM analysis_publications p
 JOIN analysis_artifacts a ON a.dataset_id=p.dataset_id AND a.run_id=p.published_run_id AND a.artifact_key=$3
 WHERE p.dataset_id=$1 AND p.scope_key=$2`, f.p.DatasetID, scope, workoutdetail.PaceReviewsArtifactKey).Scan(&payload); err != nil {
		t.Fatalf("day %s has no pace reviews: %v", day, err)
	}
	var stored workoutdetail.PaceReviews
	if err := json.Unmarshal(payload, &stored); err != nil || stored.Date != day || stored.Reviews[target] == nil || stored.Reviews[older] != nil {
		t.Fatalf("reviews hold exactly the day's Workouts: %s", payload)
	}
	if stored.Reviews[target].Reference == nil || stored.Reviews[target].Reference.Source != "pre_run" {
		t.Fatalf("precomputed review lost its pre-run reference: %s", payload)
	}
	served, err := f.s.WorkoutPaceAnalysis(ctx, f.p, target)
	if err != nil {
		t.Fatal(err)
	}
	// A dirty day is never served; the replay answers instead.
	if _, err := f.s.Pool.Exec(ctx, `UPDATE dirty_scopes SET generation=generation+1 WHERE dataset_id=$1 AND scope_key=$2`, f.p.DatasetID, scope); err != nil {
		t.Fatal(err)
	}
	replayed, err := f.s.WorkoutPaceAnalysis(ctx, f.p, target)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(stored.Reviews[target])
	b, _ := json.Marshal(served.PaceAnalysis)
	c, _ := json.Marshal(replayed.PaceAnalysis)
	if !bytes.Equal(a, b) {
		t.Fatalf("served review differs from the published one:\n%s\n%s", a, b)
	}
	if !bytes.Equal(a, c) {
		t.Fatalf("precomputed review differs from the live replay:\n%s\n%s", a, c)
	}
}
