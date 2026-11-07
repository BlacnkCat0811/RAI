//go:build integration && modelperf

package integration

import (
	"context"
	"slices"
	"testing"
	"time"

	"rai/server/internal/admin"
	"rai/server/internal/syncing"
	wf "rai/server/internal/workoutfeatures"
)

func TestDenseFeatureSnapshotPerformance(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	const points = 250_000
	b := featureBundle(t, f, 240*60)
	n := points - len(b.Routes[0].Points) - len(b.Samples)
	s := &b.Series[0]
	s.Points = make([]syncing.SeriesPoint, n)
	start, duration := *b.Workout.StartUs, *b.Workout.EndUs-*b.Workout.StartUs
	for i := range s.Points {
		s.Points[i] = syncing.SeriesPoint{StartUs: start + duration*int64(i)/int64(n),
			EndUs: start + duration*int64(i+1)/int64(n), Value: 150, Unit: "count/min"}
	}
	b.Observation.Coverage["series:"+kind] = syncing.BundleCoverage{State: "complete", SampleCount: n, QueryCompleted: true, LocalPersisted: true}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	importPhase3(t, f, b)
	if err := admin.QueueFeatureCandidate(ctx, f.s, f.p.DatasetID, b.WorkoutID, syncing.NewID(), "synthetic dense snapshot"); err != nil {
		t.Fatal(err)
	}
	l := claimAnalysis(t, f, wf.JobKind)
	var elapsed []time.Duration
	for i := 0; i < 33; i++ {
		begin := time.Now()
		in, err := f.s.LoadAnalysis(ctx, l, time.Now())
		if err != nil || in.FeatureInput == nil {
			t.Fatal("bounded snapshot", err)
		}
		var count int
		for _, stream := range in.FeatureInput.Series {
			count += len(stream.Points)
		}
		for _, route := range in.FeatureInput.Routes {
			count += len(route.Points)
		}
		if count+len(in.FeatureInput.Quantities) != points {
			t.Fatal("point count changed", count+len(in.FeatureInput.Quantities))
		}
		result, err := wf.ComputeNormalized(ctx, *in.FeatureInput)
		if err != nil {
			t.Fatal(err)
		}
		artifact, _, err := result.MarshalArtifact()
		if err != nil || len(artifact) > wf.MaxArtifactBytes {
			t.Fatal("bounded artifact", len(artifact), err)
		}
		total := time.Since(begin)
		if total > 20*time.Second {
			t.Fatalf("snapshot and computation exceeded budget: %s", total)
		}
		if i >= 3 {
			elapsed = append(elapsed, total)
		}
		if i == 32 {
			t.Logf("MC_F04 workout_minutes=240 points=%d artifact_bytes=%d", points, len(artifact))
		}
	}
	slices.Sort(elapsed)
	t.Logf("MC_F04 n=%d p95=%s max=%s", len(elapsed), elapsed[28], elapsed[29])
}
