//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"rai/server/internal/analysis"
	"rai/server/internal/syncing"
	"rai/server/internal/training"
)

// The original review probe is now a Store + real Worker regression, including
// raw preservation, chunk boundaries, route isolation and unavailable metrics.
func TestFullReviewEqualRouteTimestamp(t *testing.T) {
	for _, name := range []string{"increasing", "equal_first", "equal_last", "all_equal", "chunk_boundary", "multiple_routes", "missing_accuracy", "bad_adjacent"} {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			workoutStream(t, f)
			start := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
			w := workoutAt(start, 60)
			f.commit(t, []syncing.Event{w}, 1, nil)
			runOne(t, f, "analysis.workout")
			old, e := f.s.CurrentWorkoutAnalysis(ctx, f.p, w.UUID)
			if e != nil {
				t.Fatal(e)
			}
			points := testRoutePoints(start, 3, 0)
			for i := range points {
				points[i].HorizontalAccuracy = analysis.Ptr(5)
				points[i].Longitude += float64(i) * .0001
			}
			switch name {
			case "equal_first", "chunk_boundary":
				points[1].RecordedAtUs = points[0].RecordedAtUs
			case "equal_last":
				points[2].RecordedAtUs = points[1].RecordedAtUs
			case "all_equal":
				points[1].RecordedAtUs = points[0].RecordedAtUs
				points[2].RecordedAtUs = points[0].RecordedAtUs
			case "missing_accuracy":
				for i := range points {
					points[i].HorizontalAccuracy = nil
				}
			case "bad_adjacent":
				points[1].HorizontalAccuracy = analysis.Ptr(50)
			}
			chunks := 1
			if name == "chunk_boundary" {
				chunks = 2
			}
			routeCount := 1
			if name == "multiple_routes" {
				routeCount = 2
			}
			for r := 0; r < routeCount; r++ {
				route := syncing.NewID()
				registerTestRoute(t, f, w.UUID, route, start, time.UnixMicro(points[2].RecordedAtUs), chunks, 3)
				if chunks == 2 {
					uploadTestRouteChunk(t, f, route, 0, points[:1])
					uploadTestRouteChunk(t, f, route, 1, points[1:])
				} else {
					uploadTestRouteChunk(t, f, route, 0, points)
				}
				manifest, payload := routeStateChains(t, f, route)
				receipt := sealTestRoute(t, f, route, chunks, 3, manifest, payload)
				if !reflect.DeepEqual(receipt, sealTestRoute(t, f, route, chunks, 3, manifest, payload)) {
					t.Fatal("seal receipt changed")
				}
				// Compare the full canonical raw chunks, not a count-only proxy.
				rows, e := f.s.Pool.Query(ctx, `SELECT points FROM route_chunks WHERE dataset_id=$1 AND route_uuid=$2 ORDER BY chunk_index`, f.p.DatasetID, route)
				if e != nil {
					t.Fatal(e)
				}
				var saved []syncing.RoutePoint
				for rows.Next() {
					var b []byte
					var ps []syncing.RoutePoint
					if e = rows.Scan(&b); e != nil {
						t.Fatal(e)
					}
					if e = json.Unmarshal(b, &ps); e != nil {
						t.Fatal(e)
					}
					saved = append(saved, ps...)
				}
				e = rows.Err()
				rows.Close()
				if e != nil || !reflect.DeepEqual(saved, points) {
					t.Fatal("raw route changed", e)
				}
			}
			runOne(t, f, "analysis.workout")
			current, e := f.s.CurrentWorkoutAnalysis(ctx, f.p, w.UUID)
			if e != nil || current.RunID == old.RunID || current.AlgorithmVersion != analysis.WorkoutVersion {
				t.Fatal("new publication unavailable", e)
			}
			if publishedMetric(current, "duration") == nil {
				t.Fatal("non-route metrics lost")
			}
			unavailable := name == "all_equal" || name == "missing_accuracy" || name == "bad_adjacent"
			if (publishedMetric(current, "route_distance") == nil) != unavailable {
				t.Fatal("route availability incorrect")
			}
			for _, m := range current.Metrics {
				if m.Name == "route_distance" && !unavailable && m.Quality != "partial" {
					t.Fatal("route quality not partial")
				}
			}
		})
	}
}

func TestFullReviewOverrideScheduleTransition(t *testing.T) {
	f, p := trainingFixture(t)
	ctx := context.Background()
	target := p.Effective.Sessions[9]
	if target.Type != "INTERVAL" || target.DurationS != 2880 {
		t.Fatal("unexpected fixture")
	}
	first := editTraining(t, f, p, target.ID, "distanceM", 0, 0)
	ex, e := f.s.StartTrainingExecution(ctx, f.p, training.StartRequest{CommandID: syncing.NewID(), ExecutionID: syncing.NewID(), PlanID: p.ID, EffectiveID: first.ID, SessionID: target.ID})
	if e != nil {
		t.Fatal(e)
	}
	oldAudit, e := f.s.TrainingAudit(ctx, f.p, p.ID, first.ID)
	if e != nil {
		t.Fatal(e)
	}
	cfg := p.Config
	cfg.AvailableDays = []int{1, 2, 3, 5, 7}
	if _, e = f.s.ChangeTrainingPlan(ctx, f.p, p.ID, training.ChangeRequest{CommandID: syncing.NewID(), Operation: "update_config", ExpectedConfigRevision: 1, Config: &cfg}); e != nil {
		t.Fatal(e)
	}
	runTraining(t, f)
	latest, e := f.s.TrainingPlan(ctx, f.p, p.ID)
	if e != nil || latest.GenerationState != "succeeded" || latest.Effective.BaseID == first.BaseID || latest.Effective.OverrideRevision != 1 {
		t.Fatal("new plan not published", e)
	}
	s := sessionID(latest.Effective.Base, target.ID)
	if s.Type != "RECOVERY" || s.DistanceM <= 0 || s.DurationS != 0 || !s.Modified || len(s.Conflicts) != 1 || s.Conflicts[0] != "distanceM_override_incompatible_with_session_structure/v1" || training.ValidateSession(s) != nil {
		t.Fatal("missing legal conflict projection")
	}
	audit, e := f.s.TrainingAudit(ctx, f.p, p.ID, latest.Effective.ID)
	if e != nil || !reflect.DeepEqual(audit.Overrides, oldAudit.Overrides) {
		t.Fatal("override fact changed", e)
	}
	oldAgain, e := f.s.TrainingAudit(ctx, f.p, p.ID, first.ID)
	if e != nil || !reflect.DeepEqual(oldAgain, oldAudit) {
		t.Fatal("old effective changed", e)
	}
	saved, e := f.s.TrainingExecution(ctx, f.p, ex.ID)
	if e != nil || !reflect.DeepEqual(saved.Snapshot, ex.Snapshot) {
		t.Fatal("started execution changed", e)
	}
	o := audit.Overrides[0]
	revoked, e := f.s.PutTrainingOverride(ctx, f.p, p.ID, training.OverrideRequest{CommandID: syncing.NewID(), OverrideID: syncing.NewID(), SessionID: target.ID, Operation: "revoke", TargetID: o.ID, ExpectedOverrideRevision: 1})
	if e != nil || len(sessionID(revoked.Base, target.ID).Conflicts) != 0 || sessionID(revoked.Base, target.ID).DistanceM != sessionID(audit.Base, target.ID).DistanceM {
		t.Fatal("revoke did not clear conflict", e)
	}
}
