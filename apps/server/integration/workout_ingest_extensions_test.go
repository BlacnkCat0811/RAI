//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"rai/server/internal/admin"
	"rai/server/internal/syncing"
	wf "rai/server/internal/workoutfeatures"
)

func TestWorkoutUUIDIngestV6UnknownExtensions(t *testing.T) {
	for _, tc := range []struct{ name, key, value string }{
		{"large_number", "future", `1e400`},
		{"jsonb_numeric_overflow", "future", `1e1000000`},
		{"nul_string", "future", `"\u0000"`},
		{"observation_string", "observation", `"future-format"`},
		{"nested", "future", `{"array":[1e400,"\u0000",{"observation":false}]}`},
		{"metadata_extensions", "metadata", `{"HKIndoorWorkout":true,"future":{"nul":"\u0000","huge":1e400,"tiny":1e-1000000,"zero":0e1000000,"ok":"retained"},"array":["\u0000",1]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			id := syncing.NewID()
			raw := ingestV6Raw(t, f.p.DatasetID, f.enrollment.DatasetEpoch, f.enrollment.CollectorEpoch, id, 121)
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			fields[tc.key] = json.RawMessage(tc.value)
			raw, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if !json.Valid(raw) {
				t.Fatal("invalid synthetic fixture")
			}
			accepted := acceptIngest(t, f, f.p, raw)
			if outcome := processIngest(t, f.s); outcome.Status != "committed" {
				t.Fatalf("projection status=%s code=%s", outcome.Status, outcome.ErrorCode)
			}
			stored, state, attempts, _ := ingestRow(t, f.s, f.p.DatasetID, id)
			if !bytes.Equal(stored, raw) || state != "committed" || attempts != 1 {
				t.Fatalf("raw retention/state failed: state=%s attempts=%d", state, attempts)
			}
			if count(t, f, "workout_samples") != 3 || count(t, f, "workout_routes") != 1 {
				t.Fatal("known facts were lost")
			}
			// Replay a fresh representation; neither the first bytes nor jobs change.
			replay := acceptIngest(t, f, f.p, append(raw, '\n'))
			if !replay.AcceptedAt.Equal(accepted.AcceptedAt) {
				t.Fatal("acceptance changed")
			}
			stored, _, _, _ = ingestRow(t, f.s, f.p.DatasetID, id)
			if !bytes.Equal(stored, raw) {
				t.Fatal("replay replaced first raw bytes")
			}
			lease := claimAnalysis(t, f, "analysis.workout")
			in, err := f.s.LoadAnalysis(ctx, lease, time.Now().UTC())
			if err != nil {
				t.Fatal("legacy analysis load blocked by extension", err)
			}
			wantLineage := "healthkit-bundle:" + syncing.BundleDigest(raw)
			if !strings.Contains(strings.Join(in.Workout.Lineage, "\n"), wantLineage) {
				t.Fatal("lineage no longer names the original wire digest")
			}
			if err = admin.QueueFeatureCandidate(ctx, f.s, f.p.DatasetID, id, syncing.NewID(), "synthetic extension regression"); err != nil {
				t.Fatal(err)
			}
			featureLease := claimAnalysis(t, f, wf.JobKind)
			if feature, err := f.s.LoadAnalysis(ctx, featureLease, time.Now().UTC()); err != nil || feature.FeatureInput == nil {
				t.Fatalf("feature analysis load failed: %v", err)
			}
		})
	}
}

// Already-persisted v6 wire JSONB must also ignore the old observation field.
// Raw wire always contains source; even a spoofed projectionVersion extension
// cannot be mistaken for the internal server projection.
func TestWorkoutUUIDIngestV6HistoricalObservationExtension(t *testing.T) {
	for _, marker := range []string{`1`, `"future"`, `1e400`} {
		t.Run(marker, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			id := syncing.NewID()
			raw := ingestV6Raw(t, f.p.DatasetID, f.enrollment.DatasetEpoch, f.enrollment.CollectorEpoch, id, 121)
			raw = append([]byte(`{"observation":"future","projectionVersion":`+marker+`,"workoutSource":{"bundleID":"com.example.untrusted"},`), raw[1:]...)
			acceptIngest(t, f, f.p, raw)
			if outcome := processIngest(t, f.s); outcome.Status != "committed" {
				t.Fatal("projection failed", outcome.Status)
			}
			if _, err := f.s.Pool.Exec(ctx, `UPDATE workout_imports SET payload=$3::jsonb WHERE dataset_id=$1 AND workout_uuid=$2`, f.p.DatasetID, id, string(raw)); err != nil {
				t.Fatal(err)
			}
			lease := claimAnalysis(t, f, "analysis.workout")
			if _, err := f.s.LoadAnalysis(ctx, lease, time.Now().UTC()); err != nil {
				t.Fatal("old observation parser blocked v6", err)
			}
			if err := admin.QueueFeatureCandidate(ctx, f.s, f.p.DatasetID, id, syncing.NewID(), "synthetic historical extension"); err != nil {
				t.Fatal(err)
			}
			featureLease := claimAnalysis(t, f, wf.JobKind)
			if feature, err := f.s.LoadAnalysis(ctx, featureLease, time.Now().UTC()); err != nil || feature.FeatureInput == nil {
				t.Fatalf("feature observation parser blocked v6: %v", err)
			}
		})
	}
}
