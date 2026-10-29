//go:build integration && p7capacity

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

// Explicit read/query capacity fixtures. Historical SQL is not proof of P3
// ingestion, manifest validity, or years of runtime. Formal sync is measured
// separately through HTTPS, with the unchanged 1,000-event batch limit.
func TestP7CapacityHistories(t *testing.T) {
	for _, years := range []int{1, 3, 5} {
		t.Run(fmt.Sprintf("%dy", years), func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			days := 365 * years
			id := seedAnalysisHistory(t, f, days)
			var walBefore string
			if err := f.s.Pool.QueryRow(ctx, `SELECT pg_current_wal_lsn()::text`).Scan(&walBefore); err != nil {
				t.Fatal(err)
			}
			tx, err := f.s.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout='180s'`); err != nil {
				t.Fatal(err)
			}
			queries := []string{
				`INSERT INTO health_samples(dataset_id,sample_type,sample_uuid,event_start,event_end,value_numeric,unit,source_bundle_id,content_sha256,entity_revision)
 SELECT $1,'HKQuantityTypeIdentifierHeartRate',md5(h.id::text||':'||n)::uuid,h.at+n*interval '36 seconds',h.at+(n+1)*interval '36 seconds',150,'count/min','synthetic.p7.capacity',sha256(convert_to(h.id::text||':'||n,'UTF8')),1 FROM p4_history h CROSS JOIN generate_series(0,99) n`,
				`INSERT INTO workout_samples(dataset_id,sample_type,sample_uuid,workout_uuid) SELECT $1,'HKQuantityTypeIdentifierHeartRate',md5(h.id::text||':'||n)::uuid,h.id FROM p4_history h CROSS JOIN generate_series(0,99) n`,
				`INSERT INTO workout_routes(dataset_id,route_uuid,workout_uuid,source_bundle_id,expected_chunk_count,expected_point_count,first_recorded_at,last_recorded_at) SELECT $1,id,id,'synthetic.p7.capacity',1,300,at,at+interval '3588 seconds' FROM p4_history`,
				`INSERT INTO route_chunks(dataset_id,route_uuid,chunk_index,point_count,content_sha256,points)
 SELECT $1,id,0,300,sha256(convert_to(id::text,'UTF8')),(SELECT jsonb_agg(jsonb_build_object('sequence',n,'recordedAtUs',(extract(epoch FROM at+n*interval '12 seconds')*1000000)::bigint,'latitude',30.0,'longitude',120.0+n*0.0001,'horizontalAccuracy',5) ORDER BY n) FROM generate_series(0,299) n) FROM p4_history`,
				`UPDATE workout_routes SET state='sealed',sealed_at=now(),sealed_rev=1,receipt=convert_to('synthetic read-only capacity fixture; not P3 receipt evidence','UTF8'),chunk_count=1,total_points=300,committed_chunks=1,committed_points=300 WHERE dataset_id=$1`,
			}
			for i, q := range queries {
				if _, err = tx.Exec(ctx, q, f.p.DatasetID); err != nil {
					t.Fatalf("seed stage %d: %v", i, err)
				}
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			maintenance, err := f.s.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = maintenance.Exec(ctx, `SET LOCAL statement_timeout='60s'; ANALYZE health_samples;ANALYZE workout_samples;ANALYZE workout_routes;ANALYZE route_points;ANALYZE route_chunks`); err != nil {
				_ = maintenance.Rollback(ctx)
				t.Fatal(err)
			}
			if err = maintenance.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			p, err := f.s.CreateTrainingPlan(ctx, f.p, training.CreateRequest{CommandID: syncing.NewID(), PlanID: syncing.NewID(), Config: trainingConfig()})
			if err != nil {
				t.Fatal(err)
			}
			runTraining(t, f)
			for range 52 * years {
				recalcTraining(t, f, p)
				runTraining(t, f)
			}
			// Two real empty incremental pages per simulated day supply durable
			// batch/page/receipt growth without pretending that the SQL historical
			// raw fixture was uploaded through P3. Nonempty sync is measured over
			// HTTPS separately. These use the unchanged registration/commit path.
			var previous *string
			for i := range days * 2 {
				envelope, _, _ := f.commit(t, []syncing.Event{}, int64(i+1), previous)
				id := envelope.BatchID
				previous = &id
			}
			measurement := map[string]any{"years": years, "workouts": days, "samples": days * 100, "route_points": days * 300, "historical_seed": "SQL read-only capacity fixture; 100 HR intervals and 300 route points per one-hour daily Workout", "sync_history": "two actual empty P3 batch/page commits per simulated day; not nonempty historical ingest", "training_regenerations": 52*years + 1}
			for _, q := range []struct {
				name, sql string
				args      []any
			}{{"associated_samples", postgres.AnalysisSamplesSQL, []any{f.p.DatasetID, id}}, {"sealed_routes", postgres.AnalysisRouteSQL, []any{f.p.DatasetID, id}}} {
				var plan []byte
				if err = f.s.Pool.QueryRow(ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+q.sql, q.args...).Scan(&plan); err != nil {
					t.Fatal(err)
				}
				var parsed []map[string]any
				if err = json.Unmarshal(plan, &parsed); err != nil {
					t.Fatal(err)
				}
				var nodes []map[string]any
				var walk func(map[string]any)
				walk = func(node map[string]any) {
					item := map[string]any{}
					for _, key := range []string{"Node Type", "Index Name", "Actual Rows", "Actual Loops", "Shared Hit Blocks", "Shared Read Blocks"} {
						if v, ok := node[key]; ok {
							item[key] = v
						}
					}
					nodes = append(nodes, item)
					if children, ok := node["Plans"].([]any); ok {
						for _, child := range children {
							walk(child.(map[string]any))
						}
					}
				}
				walk(parsed[0]["Plan"].(map[string]any))
				measurement[q.name+"_plan"] = map[string]any{"nodes": nodes, "execution_ms": parsed[0]["Execution Time"]}
			}
			for _, name := range []string{"product_first50", "effective_current"} {
				var samples []float64
				for range 100 {
					at := time.Now()
					if name == "product_first50" {
						_, err = f.s.ProductWorkouts(ctx, f.p, "", 50, "")
					} else {
						_, err = f.s.TrainingPlan(ctx, f.p, p.ID)
					}
					if err != nil {
						t.Fatal(err)
					}
					samples = append(samples, float64(time.Since(at).Nanoseconds())/1e6)
				}
				sort.Float64s(samples)
				measurement[name+"_ms"] = map[string]any{"count": 100, "median": (samples[49] + samples[50]) / 2, "p95": samples[94]}
			}
			var dbBytes, walBytes int64
			if err = f.s.Pool.QueryRow(ctx, `SELECT pg_database_size(current_database()),pg_wal_lsn_diff(pg_current_wal_lsn(),$1::pg_lsn)::bigint`, walBefore).Scan(&dbBytes, &walBytes); err != nil {
				t.Fatal(err)
			}
			measurement["database_bytes"] = dbBytes
			measurement["wal_bytes_during_fixture_and_training"] = walBytes
			for _, table := range []string{"health_samples", "route_points", "sync_batches", "jobs", "analysis_runs", "training_effective_versions"} {
				measurement[table+"_rows"] = count(t, f, table)
			}
			b, _ := json.Marshal(measurement)
			t.Log("P7_CAPACITY=" + string(b))
		})
	}
}
