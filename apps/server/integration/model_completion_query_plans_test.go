//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"
)

func TestModelCompletionIndexAvailability(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	for _, name := range []string{
		"athlete_model_inputs_kind_subject_time",
		"analysis_load_ledger_day",
		"analysis_runs_scope",
		"workouts_performance_end",
		"readiness_workout_end",
	} {
		var exists bool
		if err := f.s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_indexes WHERE schemaname='public' AND indexname=$1)`, name).Scan(&exists); err != nil || !exists {
			t.Fatalf("index %s missing: %v", name, err)
		}
	}
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
		{"reference", `EXPLAIN SELECT id FROM athlete_model_inputs WHERE dataset_id=$1 AND kind='hr_reference' AND subject_key='athlete' ORDER BY effective_from DESC, id DESC LIMIT 1`, "athlete_model_inputs_", []any{f.p.DatasetID}},
		{"overlap", `EXPLAIN SELECT id FROM athlete_model_inputs WHERE dataset_id=$1 AND kind='workout_overlap_resolution' AND subject_key='athlete' ORDER BY effective_from DESC, id DESC LIMIT 1`, "athlete_model_inputs_", []any{f.p.DatasetID}},
		{"artifact", `EXPLAIN SELECT content_sha256 FROM analysis_artifacts WHERE dataset_id=$1 AND run_id=$2 AND artifact_key='workout_features'`, "analysis_artifacts_pkey", []any{f.p.DatasetID, f.p.DatasetID}},
		{"history", `EXPLAIN SELECT published_run_id FROM analysis_publications WHERE dataset_id=$1 AND scope_key=$2 AND metric_family='trainingLoad' AND day < $3::date ORDER BY day DESC LIMIT 90`, "analysis_publications_pkey", []any{f.p.DatasetID, "model/synthetic-v2/athlete/load/v2", "2026-11-07"}},
		{"load-range", `EXPLAIN SELECT workout_uuid,local_date FROM analysis_load_ledger WHERE dataset_id=$1 AND local_date >= $2::date AND local_date < $3::date`, "analysis_load_ledger_day", []any{f.p.DatasetID, "2026-01-01", "2026-07-01"}},
	}
	for _, c := range cases {
		rows, err := conn.Query(ctx, c.sql, c.args...)
		if err != nil {
			t.Fatal(c.name, err)
		}
		var plan strings.Builder
		for rows.Next() {
			var line string
			if err = rows.Scan(&line); err != nil {
				t.Fatal(c.name, err)
			}
			plan.WriteString(line)
			plan.WriteByte('\n')
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			t.Fatal(c.name, err)
		}
		if !strings.Contains(plan.String(), c.index) {
			t.Errorf("%s cannot use %s: %s", c.name, c.index, plan.String())
		}
	}
}
