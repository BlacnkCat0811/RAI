//go:build integration

package integration

import (
	"bytes"
	"context"
	"testing"
	"time"

	"rai/server/internal/syncing"
)

func registerPerWorkoutStream(t *testing.T, f *fixture, sampleType, fingerprint string) syncing.StreamState {
	t.Helper()
	stream, err := f.s.RegisterStream(context.Background(), f.p, syncing.StreamRequest{
		DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch,
		StreamID: syncing.NewID(), SampleType: sampleType, QueryFingerprint: "sha256:" + fingerprint,
	})
	if err != nil {
		t.Fatal(err)
	}
	return stream
}

// TestPerWorkoutIndependentStreamsAndIsolation mirrors the client's per-Workout
// package: each Workout gets its own Workout stream, its own sample-type stream
// and its own route/association identities. The server must never need a
// multi-Workout batch to satisfy this shape.
func TestPerWorkoutIndependentStreamsAndIsolation(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	startA := time.Date(2026, 10, 1, 6, 0, 0, 0, time.UTC)
	startB := time.Date(2026, 10, 1, 18, 0, 0, 0, time.UTC)
	workoutA, workoutB := workoutAt(startA, 60), workoutAt(startB, 60)

	streamWA := registerPerWorkoutStream(t, f, syncing.WorkoutType, "a1"+repeatChar('a', 62))
	f.stream = streamWA
	batchA, receiptA, _ := f.commit(t, []syncing.Event{workoutA}, 1, nil)
	streamWB := registerPerWorkoutStream(t, f, syncing.WorkoutType, "b1"+repeatChar('b', 62))
	f.stream = streamWB
	batchB, receiptB, _ := f.commit(t, []syncing.Event{workoutB}, 1, nil)
	if receiptA.CommitRevision == receiptB.CommitRevision || len(receiptA.AnalysisJobIDs) != 1 || len(receiptB.AnalysisJobIDs) != 1 {
		t.Fatal("two Workouts did not produce independent durable receipts/jobs")
	}

	sampleA, sampleB := sample(syncing.NewID(), 140), sample(syncing.NewID(), 150)
	streamSA := registerPerWorkoutStream(t, f, kind, "a2"+repeatChar('c', 62))
	f.stream = streamSA
	sampleBatchA, _, _ := f.commit(t, []syncing.Event{sampleA}, 1, nil)
	streamSB := registerPerWorkoutStream(t, f, kind, "b2"+repeatChar('d', 62))
	f.stream = streamSB
	f.commit(t, []syncing.Event{sampleB}, 1, nil)

	for _, pair := range []struct{ sample, workout string }{{sampleA.UUID, workoutA.UUID}, {sampleB.UUID, workoutB.UUID}} {
		if _, err := f.s.AssociateSample(ctx, f.p, syncing.AssociationRequest{
			ProtocolVersion: syncing.ProtocolVersion, DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch,
			CollectorEpoch: f.enrollment.CollectorEpoch, AssociationID: syncing.NewID(), SampleType: kind,
			SampleUUID: pair.sample, WorkoutUUID: pair.workout, Source: &syncing.Source{BundleID: "synthetic.test"},
		}); err != nil {
			t.Fatal(err)
		}
	}

	routeA := syncing.NewID()
	registerTestRoute(t, f, workoutA.UUID, routeA, startA, startA.Add(9*time.Second), 1, 10)
	uploadTestRouteChunk(t, f, routeA, 0, testRoutePoints(startA, 10, 0))
	manifest, payloadManifest := routeStateChains(t, f, routeA)
	sealTestRoute(t, f, routeA, 1, 10, manifest, payloadManifest)

	if count(t, f, "workouts") != 2 || count(t, f, "health_samples") != 2 || count(t, f, "workout_samples") != 2 || count(t, f, "workout_routes") != 1 {
		t.Fatalf("per-Workout isolation row counts wrong: workouts=%d samples=%d associations=%d routes=%d",
			count(t, f, "workouts"), count(t, f, "health_samples"), count(t, f, "workout_samples"), count(t, f, "workout_routes"))
	}
	var linkedA, linkedB int
	if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM workout_samples WHERE dataset_id=$1 AND workout_uuid=$2`, f.p.DatasetID, workoutA.UUID).Scan(&linkedA); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM workout_samples WHERE dataset_id=$1 AND workout_uuid=$2`, f.p.DatasetID, workoutB.UUID).Scan(&linkedB); err != nil {
		t.Fatal(err)
	}
	if linkedA != 1 || linkedB != 1 {
		t.Fatal("samples crossed Workout associations")
	}

	rows, err := f.s.Pool.Query(ctx, `SELECT DISTINCT scope_key FROM jobs WHERE dataset_id=$1 AND kind='analysis.workout' ORDER BY scope_key`, f.p.DatasetID)
	if err != nil {
		t.Fatal(err)
	}
	var scopes []string
	for rows.Next() {
		var scope string
		if err = rows.Scan(&scope); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		scopes = append(scopes, scope)
	}
	rows.Close()
	if len(scopes) != 2 {
		t.Fatalf("analysis jobs not scoped per Workout: %v", scopes)
	}
	expectedScopes := map[string]bool{
		"workout:" + syncing.WorkoutType + ":" + workoutA.UUID: true,
		"workout:" + syncing.WorkoutType + ":" + workoutB.UUID: true,
	}
	for _, scope := range scopes {
		if !expectedScopes[scope] {
			t.Fatalf("unexpected analysis scope: %s", scope)
		}
	}

	// Exact retry of Workout A must return its original receipt and create no
	// second Workout, sample, association or job.
	jobsBefore := count(t, f, "jobs")
	f.stream = streamWA
	retryBatch, retryReceipt, retryBody := f.commit(t, []syncing.Event{workoutA}, 2, &batchA.BatchID)
	if retryReceipt.Counts.Unchanged != 1 || retryReceipt.AnalysisStatus != "not_required" || len(retryReceipt.AnalysisJobIDs) != 0 || count(t, f, "jobs") != jobsBefore {
		t.Fatal("Workout retry created new facts or work")
	}
	stored, err := f.s.Batch(ctx, f.p, retryBatch.BatchID)
	if err != nil || !bytes.Equal(stored.Receipt, retryBody) {
		t.Fatal("retry receipt not stable", err)
	}
	if count(t, f, "workouts") != 2 || count(t, f, "workout_samples") != 2 {
		t.Fatal("retry duplicated Workout facts")
	}

	// Associating A's sample with B must fail without touching either package.
	if _, err = f.s.AssociateSample(ctx, f.p, syncing.AssociationRequest{
		ProtocolVersion: syncing.ProtocolVersion, DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch,
		CollectorEpoch: f.enrollment.CollectorEpoch, AssociationID: syncing.NewID(), SampleType: kind,
		SampleUUID: sampleA.UUID, WorkoutUUID: workoutB.UUID, Source: &syncing.Source{BundleID: "synthetic.test"},
	}); err == nil {
		t.Fatal("sample crossed to another Workout")
	} else {
		requireCode(t, err, "sample_association_conflict")
	}
	if count(t, f, "workout_samples") != 2 {
		t.Fatal("cross-Workout association mutated facts")
	}

	// A failure on B's stream must leave A's batch, receipt and route intact.
	f.stream = streamSB
	if _, err = f.s.RegisterStream(ctx, f.p, syncing.StreamRequest{
		DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch,
		StreamID: streamSB.StreamID, SampleType: kind, QueryFingerprint: "sha256:" + "ffff" + repeatChar('e', 60),
	}); err == nil {
		t.Fatal("stream identity accepted a different fingerprint")
	} else {
		requireCode(t, err, "stream_identity_conflict")
	}
	if count(t, f, "sync_batches") < 5 || count(t, f, "workout_routes") != 1 {
		t.Fatal("isolated failure disturbed other Workout facts")
	}
	_ = sampleBatchA
	_ = batchB
}
