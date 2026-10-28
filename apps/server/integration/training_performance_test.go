//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"testing"
	"time"

	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
	"rai/server/internal/training"
)

// Single observations are labelled as such. The seed is a synthetic capacity
// fixture, while correctness uses the actual ingest/worker/API tests separately.
func TestTrainingPerformanceAndCapacity(t *testing.T) {
	for _, tc := range []struct {
		name           string
		days, versions int
	}{{"52weeks", 364, 52}, {"3years", 1095, 156}} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			seedAnalysisHistory(t, f, tc.days)
			if _, e := f.s.Pool.Exec(ctx, `INSERT INTO analysis_results(dataset_id,run_id,day,metric,algorithm_name,algorithm_version,value,unit,quality)
   SELECT $1,run,'0001-01-01',m.metric,'workout.features','v1',m.value,m.unit,'available' FROM p4_history CROSS JOIN (VALUES ('duration',3600::float8,'s'),('distance',10000::float8,'m')) m(metric,value,unit)`, f.p.DatasetID); e != nil {
				t.Fatal(e)
			}
			cfg := trainingConfig()
			cfg.Weeks = 16
			start, _ := time.Parse("2006-01-02", cfg.StartDate)
			cfg.Goal.Date = start.AddDate(0, 0, 111).Format("2006-01-02")
			p, e := f.s.CreateTrainingPlan(ctx, f.p, training.CreateRequest{CommandID: syncing.NewID(), PlanID: syncing.NewID(), Config: cfg})
			if e != nil {
				t.Fatal(e)
			}
			runTraining(t, f)
			p, e = f.s.TrainingPlan(ctx, f.p, p.ID)
			if e != nil {
				t.Fatal(e)
			}
			observation := map[string]any{"history": tc.name, "syntheticWorkouts": tc.days, "environment": "fnOS/N100; native Go; isolated PostgreSQL18.6; GOMAXPROCS2", "kind": "single observation unless sample count explicit"}
			for _, weeks := range []int{8, 16} {
				l := postgresLeaseForContext(p, f)
				in, e := f.s.LoadTraining(ctx, l, time.Now().UTC())
				if e != nil {
					t.Fatal(e)
				}
				in.Context.Config.Weeks = weeks
				in.Context.Config.Goal.Date = start.AddDate(0, 0, weeks*7-1).Format("2006-01-02")
				var times []float64
				for range 100 {
					at := time.Now()
					if _, e := training.Generate(in.Context); e != nil {
						t.Fatal(e)
					}
					times = append(times, float64(time.Since(at).Nanoseconds())/1e6)
				}
				sort.Float64s(times)
				observation[fmt.Sprintf("generate_%dweek_ms", weeks)] = map[string]any{"count": 100, "median": times[49], "p95": times[94]}
			}
			os := []training.Override{}
			for i := range 100 {
				s := p.Effective.Sessions[i]
				os = append(os, training.Override{ID: syncing.NewID(), SessionID: s.ID, Revision: int64(i + 1), Operation: "edit", Path: "notes", Value: json.RawMessage(`"synthetic capacity"`), Anchor: &s})
				editTraining(t, f, p, s.ID, "notes", "synthetic capacity", int64(i))
			}
			base := p.Effective.Base
			base.Sessions = base.Sessions[:100]
			at := time.Now()
			if _, e = training.ResolveEffective(base, os); e != nil {
				t.Fatal(e)
			}
			observation["merge_100sessions_100overrides_ms"] = float64(time.Since(at).Microseconds()) / 1000
			at = time.Now()
			for range tc.versions {
				recalcTraining(t, f, p)
				runTraining(t, f)
			}
			observation["generation_history_build_ms"] = float64(time.Since(at).Microseconds()) / 1000
			if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM training_effective_versions`).Scan(new(int)); e != nil {
				t.Fatal(e)
			}
			at = time.Now()
			latest, e := f.s.TrainingPlan(ctx, f.p, p.ID)
			if e != nil {
				t.Fatal(e)
			}
			observation["effective_query_ms"] = float64(time.Since(at).Microseconds()) / 1000
			at = time.Now()
			h, e := f.s.TrainingHistory(ctx, f.p, p.ID, 0, 50)
			if e != nil || len(h.Versions) != 50 {
				t.Fatal("history page", e)
			}
			observation["history_first50_ms"] = float64(time.Since(at).Microseconds()) / 1000
			at = time.Now()
			audit, e := f.s.TrainingAudit(ctx, f.p, p.ID, latest.Effective.ID)
			if e != nil || len(audit.Overrides) != 100 {
				t.Fatal("override audit", e)
			}
			observation["audit_100overrides_ms"] = float64(time.Since(at).Microseconds()) / 1000
			// Complete a frozen execution against a published synthetic workout, then
			// measure feedback -> dirty -> load/generate/publish as one bounded operation.
			var workoutID string
			if e = f.s.Pool.QueryRow(ctx, `SELECT id::text FROM p4_history ORDER BY at DESC LIMIT 1`).Scan(&workoutID); e != nil {
				t.Fatal(e)
			}
			ex, e := f.s.StartTrainingExecution(ctx, f.p, training.StartRequest{CommandID: syncing.NewID(), ExecutionID: syncing.NewID(), PlanID: p.ID, EffectiveID: latest.Effective.ID, SessionID: latest.Effective.Sessions[0].ID})
			if e != nil {
				t.Fatal(e)
			}
			at = time.Now()
			if _, e = f.s.ChangeTrainingExecution(ctx, f.p, ex.ID, training.ExecutionChange{CommandID: syncing.NewID(), Operation: "complete", ExpectedRevision: 1, WorkoutID: workoutID}); e != nil {
				t.Fatal(e)
			}
			runTraining(t, f)
			observation["complete_feedback_recalculate_ms"] = float64(time.Since(at).Microseconds()) / 1000
			var versions int
			if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM training_effective_versions`).Scan(&versions); e != nil {
				t.Fatal(e)
			}
			observation["effectiveVersions"] = versions
			b, _ := json.Marshal(observation)
			t.Log("P5_BASELINE=" + string(b))
		})
	}
}
func postgresLeaseForContext(p training.Plan, f *fixture) postgres.Lease {
	return postgres.Lease{DatasetID: f.p.DatasetID, ScopeKey: "plan:" + p.ID, TargetGeneration: p.Generation, Kind: "training.plan"}
}
