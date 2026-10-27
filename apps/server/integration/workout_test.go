//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"rai/server/internal/syncing"
)

func workout(t *testing.T, id string) syncing.Event {
	t.Helper()
	return workoutFromFixture(t, id, "workout.ndjson")
}

func multiWorkout(t *testing.T, id string) syncing.Event {
	t.Helper()
	return workoutFromFixture(t, id, "workout-activities.ndjson")
}

func workoutFromFixture(t *testing.T, id, name string) syncing.Event {
	t.Helper()
	raw, err := os.ReadFile("../../../contracts/fixtures/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var event syncing.Event
	if err := syncing.DecodeJSON(bytes.TrimSpace(raw), &event); err != nil {
		t.Fatal(err)
	}
	event.UUID = id
	return event
}

func workoutStream(t *testing.T, f *fixture) {
	t.Helper()
	stream, err := f.s.RegisterStream(context.Background(), f.p, syncing.StreamRequest{
		DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch,
		StreamID: syncing.NewID(), SampleType: syncing.WorkoutType, QueryFingerprint: "sha256:" + strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	f.stream = stream
}

func workoutDelete(id string) syncing.Event {
	return syncing.Event{Op: "delete", Kind: "workout", Type: syncing.WorkoutType, UUID: id}
}

func TestWorkoutFactsChildrenRetryAndDelete(t *testing.T) {
	f := setup(t)
	workoutStream(t, f)
	ctx := context.Background()
	w := workout(t, syncing.NewID())
	e, r, original := f.commit(t, []syncing.Event{w}, 1, nil)
	if r.Counts.Inserted != 1 || r.CommitRevision != 1 || r.AnalysisStatus != "queued" || len(r.AnalysisJobIDs) != 1 {
		t.Fatal("missing workout receipt or durable work")
	}
	for table, expected := range map[string]int{"workouts": 1, "workout_statistics": 1, "workout_events": 2, "health_samples": 0, "entity_changes": 1, "change_events": 1, "dirty_scopes": 1, "jobs": 1} {
		if count(t, f, table) != expected {
			t.Fatal("unexpected row count in", table)
		}
	}
	var body, statistic, event []byte
	var start, end time.Time
	var duration int64
	err := f.s.Pool.QueryRow(ctx, `SELECT payload,event_start,event_end,duration_us FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2`, f.p.DatasetID, w.UUID).Scan(&body, &start, &end, &duration)
	if err != nil || start.UnixMicro() != *w.StartUs || end.UnixMicro() != *w.EndUs || duration != *w.Workout.DurationUs {
		t.Fatal("microsecond precision lost", err)
	}
	var stored syncing.Event
	if err := syncing.DecodeJSON(body, &stored); err != nil {
		t.Fatal(err)
	}
	a, _ := w.Canonical()
	b, _ := stored.Canonical()
	if !bytes.Equal(a, b) {
		t.Fatal("full source payload lost")
	}
	if err = f.s.Pool.QueryRow(ctx, `SELECT payload FROM workout_statistics WHERE dataset_id=$1 AND workout_uuid=$2`, f.p.DatasetID, w.UUID).Scan(&statistic); err != nil {
		t.Fatal(err)
	}
	var stat syncing.WorkoutStatistic
	if err := json.Unmarshal(statistic, &stat); err != nil || stat.Average.Value != 140 || stat.Minimum.Value != 90 || stat.Maximum.Value != 170 {
		t.Fatal("statistic detail lost", err)
	}
	if err = f.s.Pool.QueryRow(ctx, `SELECT payload FROM workout_events WHERE dataset_id=$1 AND workout_uuid=$2 AND ordinal=0`, f.p.DatasetID, w.UUID).Scan(&event); err != nil {
		t.Fatal(err)
	}
	var ev syncing.WorkoutEvent
	if err := json.Unmarshal(event, &ev); err != nil || *ev.Type != 1 || ev.Metadata["synthetic"] != true {
		t.Fatal("event detail/order lost", err)
	}
	var entityKind, jobKind, scope string
	if err := f.s.Pool.QueryRow(ctx, `SELECT entity_kind FROM change_events`).Scan(&entityKind); err != nil || entityKind != "workout" {
		t.Fatal("wrong changefeed kind", err)
	}
	if err := f.s.Pool.QueryRow(ctx, `SELECT kind,scope_key FROM jobs`).Scan(&jobKind, &scope); err != nil || jobKind != "analysis.workout" || scope != "workout:"+syncing.WorkoutType+":"+w.UUID {
		t.Fatal("wrong analysis scope", err)
	}
	retry, err := f.s.Commit(ctx, f.p, e, []syncing.Event{w})
	if err != nil || !bytes.Equal(retry, original) {
		t.Fatal("retry changed receipt", err)
	}
	state, err := f.s.Batch(ctx, f.p, e.BatchID)
	if err != nil || !bytes.Equal(state.Receipt, original) {
		t.Fatal("GET changed receipt", err)
	}
	e2, r, _ := f.commit(t, []syncing.Event{w}, 2, &e.BatchID)
	if r.Counts.Unchanged != 1 || r.CommitRevision != 1 || r.AnalysisStatus != "not_required" {
		t.Fatal("duplicate workout produced work")
	}
	e3, r, _ := f.commit(t, []syncing.Event{workoutDelete(w.UUID)}, 3, &e2.BatchID)
	if r.Counts.Deleted != 1 || r.CommitRevision != 2 {
		t.Fatal("delete failed")
	}
	for _, table := range []string{"workouts", "workout_statistics", "workout_events"} {
		if count(t, f, table) != 0 {
			t.Fatal("orphaned workout detail in", table)
		}
	}
	var oldStart, oldEnd time.Time
	if err := f.s.Pool.QueryRow(ctx, `SELECT previous_start,previous_end FROM entity_changes WHERE operation='delete'`).Scan(&oldStart, &oldEnd); err != nil || !oldStart.Equal(start) || !oldEnd.Equal(end) {
		t.Fatal("delete lost old impact range", err)
	}
	_, r, _ = f.commit(t, []syncing.Event{w}, 4, &e3.BatchID)
	if r.Counts.Suppressed != 1 || r.CommitRevision != 2 || count(t, f, "workouts") != 0 || count(t, f, "jobs") != 2 {
		t.Fatal("late workout resurrected or fabricated work")
	}
}

func TestWorkoutUnknownDeletionWins(t *testing.T) {
	f := setup(t)
	workoutStream(t, f)
	w := workout(t, syncing.NewID())
	changed := workout(t, w.UUID)
	*changed.Workout.DurationUs--
	e, r, _ := f.commit(t, []syncing.Event{w, changed, workoutDelete(w.UUID)}, 1, nil)
	if r.Counts.Tombstoned != 1 || r.AnalysisStatus != "not_required" || count(t, f, "workouts") != 0 || count(t, f, "jobs") != 0 {
		t.Fatal("delete did not win over conflicting upserts")
	}
	_, r, _ = f.commit(t, []syncing.Event{w}, 2, &e.BatchID)
	if r.Counts.Suppressed != 1 || r.CommitRevision != 1 {
		t.Fatal("unknown deletion barrier missing")
	}
}

func TestWorkoutConflictRollsBackChildren(t *testing.T) {
	f := setup(t)
	workoutStream(t, f)
	w := workout(t, "ffffffff-ffff-4fff-8fff-ffffffffffff")
	e, _, _ := f.commit(t, []syncing.Event{w}, 1, nil)
	*w.Workout.DurationUs--
	other := workout(t, "00000000-0000-4000-8000-000000000001")
	ctx := context.Background()
	bad, _ := f.envelope(t, []syncing.Event{other, w}, 2, &e.BatchID)
	if _, err := f.s.RegisterBatch(ctx, f.p, bad); err != nil {
		t.Fatal(err)
	}
	_, err := f.s.Commit(ctx, f.p, bad, []syncing.Event{other, w})
	requireCode(t, err, "sample_identity_conflict")
	for table, expected := range map[string]int{"workouts": 1, "workout_statistics": 1, "workout_events": 2, "entity_changes": 1, "change_events": 1, "dirty_scopes": 1, "jobs": 1} {
		if count(t, f, table) != expected {
			t.Fatal("partial conflict side effect in", table)
		}
	}
	stream, err := f.s.Stream(ctx, f.p, f.stream.StreamID)
	if err != nil || stream.LastClientSeq != 1 {
		t.Fatal("conflict advanced stream", err)
	}
}

func TestWorkoutReceiptFailureRollsBackInsertAndDelete(t *testing.T) {
	for _, deletion := range []bool{false, true} {
		name := "insert"
		if deletion {
			name = "delete"
		}
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			workoutStream(t, f)
			ctx := context.Background()
			w := workout(t, syncing.NewID())
			seq, initialWorkouts, initialEvents, initialJobs := int64(1), 0, 0, 0
			var previous *string
			events := []syncing.Event{w}
			if deletion {
				e, _, _ := f.commit(t, events, 1, nil)
				previous, seq, initialWorkouts, initialEvents, initialJobs = &e.BatchID, 2, 1, 2, 1
				events = []syncing.Event{workoutDelete(w.UUID)}
			}
			e, _ := f.envelope(t, events, seq, previous)
			if _, err := f.s.RegisterBatch(ctx, f.p, e); err != nil {
				t.Fatal(err)
			}
			_, err := f.s.Pool.Exec(ctx, `CREATE FUNCTION fail_workout_receipt() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic receipt failure'; END $$;
				CREATE TRIGGER fail_workout_receipt BEFORE UPDATE ON sync_batches FOR EACH ROW EXECUTE FUNCTION fail_workout_receipt()`)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.Commit(ctx, f.p, e, events); err == nil {
				t.Fatal("injected failure did not abort")
			}
			for table, expected := range map[string]int{"workouts": initialWorkouts, "workout_statistics": initialWorkouts, "workout_events": initialEvents, "entity_tombstones": 0, "entity_changes": initialJobs, "change_events": initialJobs, "dirty_scopes": initialJobs, "jobs": initialJobs} {
				if count(t, f, table) != expected {
					t.Fatal("partial receipt failure side effect in", table)
				}
			}
			meta, err := f.s.Metadata(ctx, f.p)
			if err != nil || meta.CommitRevision != seq-1 {
				t.Fatal("revision escaped rollback", err)
			}
			stream, err := f.s.Stream(ctx, f.p, f.stream.StreamID)
			if err != nil || stream.LastClientSeq != seq-1 {
				t.Fatal("stream escaped rollback", err)
			}
			state, err := f.s.Batch(ctx, f.p, e.BatchID)
			if err != nil || state.State != "registered" || state.Receipt != nil {
				t.Fatal("receipt escaped rollback", err)
			}
			if _, err := f.s.Pool.Exec(ctx, `DROP TRIGGER fail_workout_receipt ON sync_batches`); err != nil {
				t.Fatal(err)
			}
			if _, err := f.s.Commit(ctx, f.p, e, events); err != nil {
				t.Fatal("original batch could not recover", err)
			}
		})
	}
}
