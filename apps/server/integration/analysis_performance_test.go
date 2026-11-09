//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"rai/server/internal/admin"
	"rai/server/internal/analysis"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
)

// Historical rows are synthetic capacity fixtures inserted with set-based SQL.
// They are not an ingest correctness test; real API/worker behavior is covered
// separately. No existing or production database is ever used.
func seedAnalysisHistory(t *testing.T, f *fixture, days int) string {
	t.Helper()
	return seedAnalysisHistoryDensity(t, f, days, 1)
}
func seedAnalysisHistoryDensity(t *testing.T, f *fixture, days, density int) string {
	t.Helper()
	ctx := context.Background()
	// Bulk synthetic setup is not an online operation. Keep its timeout local
	// to this transaction; the measured API/query pool retains its 2s budget.
	tx, err := f.s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout='120s'; SET LOCAL transaction_timeout='180s'`); err != nil {
		t.Fatal(err)
	}
	if _, e := tx.Exec(ctx, `CREATE TABLE p4_history AS SELECT gen_random_uuid() AS id,gen_random_uuid() AS job,gen_random_uuid() AS run,
 date_trunc('day',clock_timestamp(),'Asia/Shanghai')-(n/$2::integer)*interval '1 day'+interval '2 hours'+(n%$2::integer)*interval '1 minute' AS at FROM generate_series(0,$1::integer-1) n`, days*density, density); e != nil {
		t.Fatal(e)
	}
	if _, e := tx.Exec(ctx, `INSERT INTO workouts(dataset_id,workout_uuid,event_start,event_end,activity_type,duration_us,source_bundle_id,content_sha256,entity_revision,payload)
 SELECT $1,id,at,at+interval '1 hour',37,3600000000,'synthetic.capacity',sha256(convert_to(id::text,'UTF8')),1,
 jsonb_build_object('op','upsert','kind','workout','type','HKWorkoutTypeIdentifier','uuid',id,'startUs',(extract(epoch from at)*1000000)::bigint,'endUs',(extract(epoch from at+interval '1 hour')*1000000)::bigint,'source',jsonb_build_object('bundleID','synthetic.capacity'),'workout',jsonb_build_object('activityType',37,'durationUs',3600000000,'totalDistance',jsonb_build_object('value',10000,'unit','m'))) FROM p4_history`, f.p.DatasetID); e != nil {
		t.Fatal(e)
	}
	if _, e := tx.Exec(ctx, `INSERT INTO dirty_scopes(dataset_id,scope_key,generation,clean_generation,dirty_from,dirty_through) SELECT $1,'workout:HKWorkoutTypeIdentifier:'||id,1,1,at,at+interval '1 hour' FROM p4_history`, f.p.DatasetID); e != nil {
		t.Fatal(e)
	}
	if _, e := tx.Exec(ctx, `INSERT INTO jobs(id,dataset_id,kind,scope_key,target_generation,state) SELECT job,$1,'analysis.workout','workout:HKWorkoutTypeIdentifier:'||id,1,'succeeded' FROM p4_history`, f.p.DatasetID); e != nil {
		t.Fatal(e)
	}
	if _, e := tx.Exec(ctx, `INSERT INTO analysis_runs(dataset_id,id,job_id,fencing_token,scope_key,target_generation,algorithm_name,algorithm_version,input_version,input_hash,input_from,input_through,lineage,status,finished_at)
 SELECT $1,run,job,1,'workout:HKWorkoutTypeIdentifier:'||id,1,'workout.bundle','v1','synthetic.capacity/v1',sha256(convert_to(id::text,'UTF8')),at,at+interval '1 hour','{}','SUCCEEDED',now() FROM p4_history`, f.p.DatasetID); e != nil {
		t.Fatal(e)
	}
	if _, e := tx.Exec(ctx, `INSERT INTO analysis_results(dataset_id,run_id,day,metric,algorithm_name,algorithm_version,value,unit,quality) SELECT $1,run,'0001-01-01','workout_load','training_load.trimp','v1',100,'AU','available' FROM p4_history`, f.p.DatasetID); e != nil {
		t.Fatal(e)
	}
	if _, e := tx.Exec(ctx, `INSERT INTO analysis_publications(dataset_id,scope_key,metric_family,day,published_run_id,generation) SELECT $1,'workout:HKWorkoutTypeIdentifier:'||id,'workout','0001-01-01',run,1 FROM p4_history`, f.p.DatasetID); e != nil {
		t.Fatal(e)
	}
	var latest string
	if e := tx.QueryRow(ctx, `SELECT id::text FROM p4_history ORDER BY at DESC LIMIT 1`).Scan(&latest); e != nil {
		t.Fatal(e)
	}
	if _, e := tx.Exec(ctx, `ANALYZE workouts;ANALYZE dirty_scopes;ANALYZE jobs;ANALYZE analysis_results;ANALYZE analysis_publications;ANALYZE analysis_runs`); e != nil {
		t.Fatal(e)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return latest
}
func TestAnalysisPerformanceAndCapacity(t *testing.T) {
	for _, years := range []int{1, 3, 5} {
		t.Run(fmt.Sprintf("%dy", years), func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			id := seedAnalysisHistory(t, f, 365*years)
			loc, _ := time.LoadLocation("Asia/Shanghai")
			today := time.Now().In(loc)
			observations := map[string]any{"years": years, "syntheticWorkouts": 365 * years, "environment": "fnOS/N100; Go native; PG18.6 disposable; GOMAXPROCS=2", "seed": "set-based synthetic raw+derived capacity fixture"}
			for _, days := range []int{7, 30, 365} {
				start := time.Now()
				r := admin.RecomputeRequest{Dataset: f.p.DatasetID, Scope: analysis.StateScope, Algorithm: "training_state.bundle/v2", From: today.AddDate(0, 0, 1-days).Format("2006-01-02"), Through: today.Format("2006-01-02")}
				if _, e := admin.Recompute(ctx, f.s, r); e != nil {
					t.Fatal(e)
				}
				l := claimAnalysis(t, f, "analysis.training_state")
				loadStart := time.Now()
				in, run, ms := computeAnalysis(t, f, l)
				inputCompute := time.Since(loadStart)
				if len(ms) != days*6 {
					t.Fatal("unexpected output range", len(ms))
				}
				if _, e := f.s.PublishAnalysis(ctx, l, run, in, ms); e != nil {
					t.Fatal(e)
				}
				observations[fmt.Sprintf("state_%dd_ms", days)] = float64(time.Since(start).Microseconds()) / 1000
				observations[fmt.Sprintf("state_%dd_load_compute_ms", days)] = float64(inputCompute.Microseconds()) / 1000
			}
			// Isolate pure feature/load cost from database orchestration. 100 samples
			// provide a labelled median/p95 for this fixed synthetic input only.
			w := analysis.WorkoutInput{ID: "synthetic", Start: 0, End: 3600e6, Duration: 3600, Distance: analysis.Ptr(10000), Parameters: &analysis.Parameters{Version: 1, RestHR: 50, MaxHR: 200, A: .64, B: 1.92, Provenance: "synthetic"}, Observations: []analysis.Observation{{ID: "HR", Type: analysis.HeartRate, Start: 0, End: 3600e6, Value: 150, Unit: "count/min"}}}
			var times []float64
			for i := 0; i < 100; i++ {
				at := time.Now()
				if _, e := analysis.WorkoutFeatures(ctx, w); e != nil {
					t.Fatal(e)
				}
				times = append(times, float64(time.Since(at).Nanoseconds())/1e6)
			}
			sort.Float64s(times)
			observations["workout_feature_and_load_pure_ms"] = map[string]any{"count": 100, "median": times[49], "p95": times[94], "input": "1h, one full-interval HR, no route"}
			var loadTimes []float64
			for i := 0; i < 100; i++ {
				at := time.Now()
				if _, e := analysis.CalculateTrainingLoad(w); e != nil {
					t.Fatal(e)
				}
				loadTimes = append(loadTimes, float64(time.Since(at).Nanoseconds())/1e6)
			}
			sort.Float64s(loadTimes)
			observations["workout_load_pure_ms"] = map[string]any{"count": 100, "median": loadTimes[49], "p95": loadTimes[94]}
			weekStart := time.Now()
			for i := 0; i < 7; i++ {
				if _, e := analysis.CalculateTrainingLoad(w); e != nil {
					t.Fatal(e)
				}
			}
			observations["seven_workout_load_pure_ms"] = float64(time.Since(weekStart).Nanoseconds()) / 1e6

			// Populate real bounded HR/route inputs after the historical state baseline.
			var workoutStart time.Time
			if e := f.s.Pool.QueryRow(ctx, `SELECT event_start FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2`, f.p.DatasetID, id).Scan(&workoutStart); e != nil {
				t.Fatal(e)
			}
			samples := make([]syncing.Event, 1000)
			for i := range samples {
				a := workoutStart.UnixMicro() + int64(i)*3600000
				b := a + 3600000
				samples[i] = seriesParent(syncing.NewID(), a, b, 150)
			}
			f.commit(t, samples, 1, nil)
			if _, e := f.s.Pool.Exec(ctx, `INSERT INTO workout_samples(dataset_id,sample_type,sample_uuid,workout_uuid) SELECT dataset_id,sample_type,sample_uuid,$2 FROM health_samples WHERE dataset_id=$1`, f.p.DatasetID, id); e != nil {
				t.Fatal(e)
			}
			route := syncing.NewID()
			registerTestRoute(t, f, id, route, workoutStart, workoutStart.Add(999*time.Second), 1, 1000)
			points := testRoutePoints(workoutStart, 1000, 0)
			for i := range points {
				points[i].Longitude += float64(i) * .00001
				points[i].HorizontalAccuracy = analysis.Ptr(5)
			}
			uploadTestRouteChunk(t, f, route, 0, points)
			m, p := routeStateChains(t, f, route)
			sealTestRoute(t, f, route, 1, 1000, m, p)
			if _, e := f.s.Pool.Exec(ctx, `ANALYZE health_samples;ANALYZE workout_samples;ANALYZE workout_routes;ANALYZE route_points`); e != nil {
				t.Fatal(e)
			}
			r := admin.RecomputeRequest{Dataset: f.p.DatasetID, Scope: "workout:" + id, Algorithm: analysis.WorkoutAlgorithm + "/" + analysis.WorkoutVersion, From: today.Format("2006-01-02"), Through: today.Format("2006-01-02")}
			if _, e := admin.Recompute(ctx, f.s, r); e != nil {
				t.Fatal(e)
			}
			at := time.Now()
			runOne(t, f, "analysis.workout")
			observations["workout_claim_to_publication_ms"] = float64(time.Since(at).Microseconds()) / 1000
			for _, table := range []string{"workouts", "jobs", "analysis_runs", "analysis_results", "analysis_publications"} {
				observations[table+"_rows"] = count(t, f, table)
			}
			b, _ := json.Marshal(observations)
			t.Log("P4_BASELINE=" + string(b))
			// EXPLAIN uses the actual production query constants and default planner.
			plans := []struct {
				name, query string
				args        []any
			}{{"workout", postgres.AnalysisWorkoutSQL, []any{f.p.DatasetID, id}}, {"associated_HR", postgres.AnalysisSamplesSQL, []any{f.p.DatasetID, id}}, {"sealed_route", postgres.AnalysisRouteSQL, []any{f.p.DatasetID, id}}, {"load_range", postgres.AnalysisStateLoadsSQL, []any{f.p.DatasetID, today.AddDate(0, 0, -179), today.AddDate(0, 0, 1)}}, {"publication", postgres.AnalysisCurrentSQL, []any{f.p.DatasetID, "workout:HKWorkoutTypeIdentifier:" + id, "workout", id}}}
			for _, p := range plans {
				rows, e := f.s.Pool.Query(ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+p.query, p.args...)
				if e != nil {
					t.Fatal(p.name, e)
				}
				var raw []byte
				for rows.Next() {
					if e = rows.Scan(&raw); e != nil {
						t.Fatal(e)
					}
				}
				e = rows.Err()
				rows.Close()
				if e != nil {
					t.Fatal(e)
				}
				var value any
				if e = json.Unmarshal(raw, &value); e != nil {
					t.Fatal(e)
				}
				b, _ := json.Marshal(value)
				t.Logf("P4_PLAN_%s=%s", p.name, b)
				if years == 5 && p.name == "workout" && !strings.Contains(string(b), "workouts_pkey") {
					t.Fatal("identity lookup lost primary key")
				}
			}
		})
	}
}
