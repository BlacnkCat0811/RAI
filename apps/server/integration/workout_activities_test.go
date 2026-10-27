//go:build integration

package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"rai/server/internal/admin"
	"rai/server/internal/syncing"
	"rai/server/migrations"
)

func activitySnapshot(t *testing.T, f *fixture) map[string][]byte {
	t.Helper()
	out := businessSnapshot(t, f)
	var raw []byte
	if err := f.s.Pool.QueryRow(context.Background(), `SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text),'[]') FROM workout_activities x`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	out["workout_activities"] = raw
	return out
}
func sameActivityBusiness(t *testing.T, f *fixture, before map[string][]byte) {
	t.Helper()
	for table, after := range activitySnapshot(t, f) {
		if !bytes.Equal(before[table], after) {
			t.Fatal("partial activity transaction in", table)
		}
	}
}

func TestWorkoutActivitiesDurabilityAndDeletion(t *testing.T) {
	f := setup(t)
	workoutStream(t, f)
	ctx := context.Background()
	w := multiWorkout(t, syncing.NewID())
	e, r, original := f.commit(t, []syncing.Event{w}, 1, nil)
	if r.Counts.Inserted != 1 || r.CommitRevision != 1 || len(r.AnalysisJobIDs) != 1 || count(t, f, "workout_activities") != 2 {
		t.Fatal("activity insert or receipt lost")
	}
	// Assert projected UUID, exact microseconds, nullable end, source order and full nested content.
	rows, err := f.s.Pool.Query(ctx, `SELECT ordinal,activity_uuid::text,event_start,event_end,duration_us,payload FROM workout_activities ORDER BY ordinal`)
	if err != nil {
		t.Fatal(err)
	}
	ordinal := 0
	for rows.Next() {
		var n int
		var id string
		var start time.Time
		var end *time.Time
		var duration int64
		var raw []byte
		if err := rows.Scan(&n, &id, &start, &end, &duration, &raw); err != nil {
			t.Fatal(err)
		}
		a := w.Workout.Activities[ordinal]
		if n != ordinal || id != a.UUID || start.UnixMicro() != *a.StartUs || duration != *a.DurationUs || (end == nil) != (a.EndUs == nil) {
			t.Fatal("projection lost source fields")
		}
		if end != nil && end.UnixMicro() != *a.EndUs {
			t.Fatal("projection lost end precision")
		}
		var stored syncing.WorkoutActivity
		if err := json.Unmarshal(raw, &stored); err != nil {
			t.Fatal(err)
		}
		expected, _ := json.Marshal(a)
		actual, _ := json.Marshal(stored)
		if !bytes.Equal(expected, actual) {
			t.Fatal("configuration/metadata/statistics/events lost")
		}
		ordinal++
	}
	rows.Close()
	if rows.Err() != nil || ordinal != 2 {
		t.Fatal("projection incomplete", rows.Err())
	}
	var raw []byte
	if err := f.s.Pool.QueryRow(ctx, `SELECT payload FROM workouts`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var stored syncing.Event
	if err := syncing.DecodeJSON(raw, &stored); err != nil {
		t.Fatal(err)
	}
	expected, _ := w.Canonical()
	actual, _ := stored.Canonical()
	if !bytes.Equal(expected, actual) {
		t.Fatal("full parent payload lost")
	}
	retry, err := f.s.Commit(ctx, f.p, e, []syncing.Event{w})
	if err != nil || !bytes.Equal(retry, original) {
		t.Fatal("activity retry changed receipt", err)
	}
	e2, r, _ := f.commit(t, []syncing.Event{w}, 2, &e.BatchID)
	if r.Counts.Unchanged != 1 || r.CommitRevision != 1 || count(t, f, "workout_activities") != 2 {
		t.Fatal("duplicate activity created side effects")
	}
	e3, r, _ := f.commit(t, []syncing.Event{workoutDelete(w.UUID)}, 3, &e2.BatchID)
	if r.Counts.Deleted != 1 || count(t, f, "workout_activities") != 0 {
		t.Fatal("cascade left orphan activities")
	}
	_, r, _ = f.commit(t, []syncing.Event{w}, 4, &e3.BatchID)
	if r.Counts.Suppressed != 1 || r.CommitRevision != 2 || count(t, f, "workout_activities") != 0 {
		t.Fatal("late activity resurrected")
	}
}

func TestWorkoutActivityConflictRollsBackWholeBatch(t *testing.T) {
	f := setup(t)
	workoutStream(t, f)
	w := multiWorkout(t, "ffffffff-ffff-4fff-8fff-ffffffffffff")
	e, _, _ := f.commit(t, []syncing.Event{w}, 1, nil)
	// A nested configuration change is immutable content, even if parent totals match.
	*w.Workout.Activities[0].Configuration.LocationType = 2
	other := multiWorkout(t, "00000000-0000-4000-8000-000000000001")
	events := []syncing.Event{other, w}
	bad, _ := f.envelope(t, events, 2, &e.BatchID)
	if _, err := f.s.RegisterBatch(context.Background(), f.p, bad); err != nil {
		t.Fatal(err)
	}
	before := activitySnapshot(t, f)
	_, err := f.s.Commit(context.Background(), f.p, bad, events)
	requireCode(t, err, "sample_identity_conflict")
	sameActivityBusiness(t, f, before)
}

func TestWorkoutActivityDeleteWinsAndUnknownBarrier(t *testing.T) {
	f := setup(t)
	workoutStream(t, f)
	w := multiWorkout(t, syncing.NewID())
	changed := multiWorkout(t, w.UUID)
	*changed.Workout.Activities[0].DurationUs--
	e, r, _ := f.commit(t, []syncing.Event{w, changed, workoutDelete(w.UUID)}, 1, nil)
	if r.Counts.Tombstoned != 1 || r.AnalysisStatus != "not_required" || count(t, f, "workout_activities") != 0 {
		t.Fatal("same batch delete did not win")
	}
	_, r, _ = f.commit(t, []syncing.Event{w}, 2, &e.BatchID)
	if r.Counts.Suppressed != 1 || count(t, f, "workout_activities") != 0 {
		t.Fatal("unknown tombstone lost")
	}
}

func TestWorkoutActivitiesAtomicFailures(t *testing.T) {
	for _, point := range []string{"activity_insert", "receipt", "seal", "lease"} {
		for _, op := range []string{"upsert", "delete"} {
			if point == "activity_insert" && op == "delete" {
				continue
			}
			t.Run(point+"_"+op, func(t *testing.T) {
				f := setup(t)
				workoutStream(t, f)
				ctx := context.Background()
				event := multiWorkout(t, syncing.NewID())
				var previous *string
				seq := int64(1)
				if op == "delete" {
					e, _, _ := f.commit(t, []syncing.Event{event}, seq, nil)
					previous, seq, event = &e.BatchID, 2, workoutDelete(event.UUID)
				}
				events := []syncing.Event{event}
				e, wire := f.envelope(t, events, seq, previous)
				if _, err := f.s.RegisterBatch(ctx, f.p, e); err != nil {
					t.Fatal(err)
				}
				parsed, err := syncing.ParseBody(bytes.NewReader(wire), e, syncing.WorkoutType)
				if err != nil {
					t.Fatal(err)
				}
				before := activitySnapshot(t, f)
				lease := takeUpload(t, f.s, f.p)
				// Owner-only deterministic triggers inject faults after partial child writes
				// or after all fact/log/receipt writes. They are never installed by the API.
				var trigger, table, body, condition string
				switch point {
				case "activity_insert":
					trigger, table, condition = "fail_activity", "workout_activities", " WHEN (NEW.ordinal=1)"
					body = "RAISE EXCEPTION 'synthetic child failure';"
				case "receipt":
					trigger, table = "fail_receipt", "sync_batches"
					body = "RAISE EXCEPTION 'synthetic receipt failure';"
				case "seal":
					trigger, table, condition = "fail_seal", "sync_pages", " WHEN (NEW.receipt IS NOT NULL)"
					body = "RAISE EXCEPTION 'synthetic seal failure';"
				case "lease":
					trigger, table = "expire_lease", "sync_pages"
					body = "UPDATE sync_upload_slots SET expires_at=clock_timestamp()-interval '1 second' WHERE dataset_id=NEW.dataset_id; RETURN NEW;"
				}
				action := "UPDATE"
				if point == "activity_insert" {
					action = "INSERT"
				}
				_, err = f.s.Pool.Exec(ctx, `CREATE FUNCTION `+trigger+`() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `+body+` END $$; CREATE TRIGGER `+trigger+` BEFORE `+action+` ON `+table+` FOR EACH ROW`+condition+` EXECUTE FUNCTION `+trigger+`() `)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.s.CommitUpload(ctx, f.p, e, parsed, lease)
				if err == nil {
					t.Fatal("fault did not abort")
				}
				if point == "lease" {
					requireCode(t, err, "upload_lease_lost")
				}
				sameActivityBusiness(t, f, before)
				if _, err := f.s.Pool.Exec(ctx, `DROP TRIGGER `+trigger+` ON `+table); err != nil {
					t.Fatal(err)
				}
				original, err := f.s.CommitUpload(ctx, f.p, e, parsed, lease)
				if err != nil {
					t.Fatal("original batch could not recover", err)
				}
				retry, err := f.s.CommitUpload(ctx, f.p, e, parsed, lease)
				if err != nil || !bytes.Equal(original, retry) {
					t.Fatal("retry changed original receipt", err)
				}
				state, err := f.s.Batch(ctx, f.p, e.BatchID)
				if err != nil || !bytes.Equal(state.Receipt, original) {
					t.Fatal("GET receipt changed", err)
				}
				pageState(t, f, e.PageID, 1, "sealed")
			})
		}
	}
}

func TestSchema6UpgradePreservesV1IdentityAndRuntimeState(t *testing.T) {
	f := setupWithMigration(t, func(ctx context.Context, url string) error {
		db, err := sql.Open("pgx", url)
		if err != nil {
			return err
		}
		defer db.Close()
		provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.Files)
		if err != nil {
			return err
		}
		_, err = provider.UpTo(ctx, 6)
		return err
	})
	workoutStream(t, f)
	ctx := context.Background()
	w := workout(t, syncing.NewID())
	e, _, original := f.commit(t, []syncing.Event{w}, 1, nil)
	pending := workout(t, syncing.NewID())
	next, wire := f.envelope(t, []syncing.Event{pending}, 2, &e.BatchID)
	if _, err := f.s.RegisterBatch(ctx, f.p, next); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Admit(ctx, f.p, syncing.UploadLane); err != nil {
		t.Fatal(err)
	}
	debt := budgetTime(t, f, &f.p.DeviceID, syncing.UploadLane)
	lease := takeUpload(t, f.s, f.p)
	before := businessSnapshot(t, f)
	var slots []byte
	if err := f.s.Pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(x) ORDER BY slot) FROM sync_upload_slots x`).Scan(&slots); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Ready(ctx); err == nil {
		t.Fatal("schema 6 accepted by schema 9 binary")
	}
	for range 2 {
		if err := admin.Migrate(ctx, f.s.Pool.Config().ConnString()); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.s.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	sameBusiness(t, f, before)
	if count(t, f, "workout_activities") != 0 {
		t.Fatal("migration fabricated activities")
	}
	var after []byte
	if err := f.s.Pool.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(x) ORDER BY slot) FROM sync_upload_slots x`).Scan(&after); err != nil || !bytes.Equal(slots, after) {
		t.Fatal("upgrade reset upload leases", err)
	}
	if !budgetTime(t, f, &f.p.DeviceID, syncing.UploadLane).Equal(debt) {
		t.Fatal("upgrade reset debt")
	}
	retry, err := f.s.CommitUpload(ctx, f.p, e, []syncing.Event{w}, lease)
	if err != nil || !bytes.Equal(original, retry) {
		t.Fatal("upgrade changed original v1 receipt", err)
	}
	parsed, err := syncing.ParseBody(bytes.NewReader(wire), next, syncing.WorkoutType)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.CommitUpload(ctx, f.p, next, parsed, lease); err != nil {
		t.Fatal("registered v1 batch could not finish", err)
	}
	if count(t, f, "workout_activities") != 0 {
		t.Fatal("v1 upload fabricated activities")
	}
	// Adding activities later is an explicit identity conflict, never an overwrite.
	enriched := multiWorkout(t, w.UUID)
	replacement, _ := f.envelope(t, []syncing.Event{enriched}, 3, &next.BatchID)
	if _, err := f.s.RegisterBatch(ctx, f.p, replacement); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.CommitUpload(ctx, f.p, replacement, []syncing.Event{enriched}, lease)
	requireCode(t, err, "sample_identity_conflict")
	if count(t, f, "workout_activities") != 0 {
		t.Fatal("upgrade enriched immutable v1 fact")
	}
}

func TestMaximumWorkoutActivitiesCommitAndCascade(t *testing.T) {
	f := setup(t)
	workoutStream(t, f)
	event := multiWorkout(t, syncing.NewID())
	activity := event.Workout.Activities[0]
	activity.Statistics, activity.Events = nil, nil
	event.Workout.Activities = make([]syncing.WorkoutActivity, syncing.MaxWorkoutActivities)
	for i := range event.Workout.Activities {
		event.Workout.Activities[i] = activity
		event.Workout.Activities[i].UUID = syncing.NewID()
	}
	e, r, _ := f.commit(t, []syncing.Event{event}, 1, nil)
	if r.Counts.Inserted != 1 || count(t, f, "workout_activities") != syncing.MaxWorkoutActivities {
		t.Fatal("maximum activity projection incomplete")
	}
	_, r, _ = f.commit(t, []syncing.Event{workoutDelete(event.UUID)}, 2, &e.BatchID)
	if r.Counts.Deleted != 1 || count(t, f, "workout_activities") != 0 {
		t.Fatal("maximum cascade incomplete")
	}
}
