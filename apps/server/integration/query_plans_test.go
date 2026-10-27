//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"rai/server/internal/syncing"
)

func TestCoreIndexAvailability(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	s := sample(syncing.NewID(), 120)
	f.commit(t, []syncing.Event{s}, 1, nil)
	workoutStream(t, f)
	w := workout(t, syncing.NewID())
	f.commit(t, []syncing.Event{w}, 1, nil)
	route := syncing.NewID()
	start := time.UnixMicro(*w.StartUs).UTC()
	registerTestRoute(t, f, w.UUID, route, start, start, 1, 1)
	uploadTestRouteChunk(t, f, route, 0, testRoutePoints(start, 1, 0))
	conn, err := f.s.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, `SET enable_seqscan=off`); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(ctx, `RESET enable_seqscan`)
	cases := []struct {
		name, sql, index string
		args             []any
	}{
		{"workout", `EXPLAIN SELECT * FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2`, "workouts_pkey", []any{f.p.DatasetID, w.UUID}},
		{"sample", `EXPLAIN SELECT * FROM health_samples WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3`, "health_samples_", []any{f.p.DatasetID, kind, s.UUID}},
		{"route", `EXPLAIN SELECT * FROM route_points WHERE dataset_id=$1 AND route_uuid=$2 ORDER BY sequence`, "route_points_pkey", []any{f.p.DatasetID, route}},
		{"job", `EXPLAIN SELECT id FROM jobs WHERE state='queued' AND run_after <= now() AND kind = ANY($1::text[]) ORDER BY run_after,id FOR UPDATE SKIP LOCKED LIMIT 1`, "jobs_ready", []any{[]string{"analysis.sample"}}},
	}
	for _, c := range cases {
		rows, err := conn.Query(ctx, c.sql, c.args...)
		if err != nil {
			t.Fatal(err)
		}
		var plan strings.Builder
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			plan.WriteString(line)
			plan.WriteByte('\n')
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if !strings.Contains(plan.String(), c.index) {
			t.Errorf("%s cannot use %s: %s", c.name, c.index, plan.String())
		}
	}
}
