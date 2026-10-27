//go:build integration && scale

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"rai/server/internal/syncing"
)

// A full 20,000-detail package uses nested activity events: the separate
// 128-activity and 256-event-per-activity limits still apply at this boundary.
func TestScaleWorkoutPackageMaximumDetails(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	id := syncing.NewID()
	start, end := int64(1793059200000000), int64(1793059260000000)
	if _, err := f.s.RegisterWorkoutPackage(ctx, f.p, workoutPackageRequest(f, id, start, end)); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	chain := make([]byte, sha256.Size)
	const activities = 78
	var firstReceipt []byte
	var firstChunk syncing.WorkoutDetailChunk
	var activityIDs []string
	for i := 0; i < activities; i++ {
		events := 256
		if i == activities-1 {
			events = 210
		}
		line := scaleWorkoutActivityLine(i, events, start, end)
		wire := workoutPackageWire(t, line)
		chunk, err := syncing.ParseWorkoutDetailChunk(bytes.NewReader(wire), start, end)
		if err != nil {
			t.Fatalf("parse chunk %d: %v", i, err)
		}
		chunk.WorkoutID, chunk.ChunkIndex = id, i
		activityIDs = append(activityIDs, chunk.Entries[0].Activity.UUID)
		canonical, _ := syncing.WorkoutDetailsCanonical(chunk.Entries)
		digest := sha256.Sum256(canonical)
		chainInput := append(append([]byte(nil), chain...), digest[:]...)
		next := sha256.Sum256(chainInput)
		chain = next[:]
		lease := takeUpload(t, f.s, f.p)
		body, err := f.s.CommitWorkoutPackageChunk(ctx, f.p, chunk, lease)
		if err != nil {
			t.Fatalf("commit chunk %d: %v", i, err)
		}
		if err = f.s.ReleaseUpload(ctx, f.p, lease); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstReceipt, firstChunk = append([]byte(nil), body...), chunk
		}
		if i == activities/2 {
			nextStore := uploadPool(t, f)
			f.s.Pool.Close()
			f.s = nextStore
			lease = takeUpload(t, f.s, f.p)
			retry, err := f.s.CommitWorkoutPackageChunk(ctx, f.p, firstChunk, lease)
			if err != nil || !bytes.Equal(retry, firstReceipt) {
				t.Fatalf("chunk receipt after reconnect: %v", err)
			}
			if err = f.s.ReleaseUpload(ctx, f.p, lease); err != nil {
				t.Fatal(err)
			}
		}
	}
	state, err := f.s.WorkoutPackage(ctx, f.p, id)
	if err != nil || state.CommittedChunks != activities || state.TotalDetails != syncing.MaxWorkoutPackageDetails || state.ManifestSHA256 != hex.EncodeToString(chain) {
		t.Fatalf("full package state: %+v, %v", state, err)
	}
	if count(t, f, "workouts") != 0 || count(t, f, "jobs") != 0 || count(t, f, "change_events") != 0 || count(t, f, "dirty_scopes") != 0 {
		t.Fatal("pending package published facts or work")
	}
	// An extra valid activity must be rejected without altering the staging state.
	wire := workoutPackageWire(t, scaleWorkoutActivityLine(activities, 0, start, end))
	extra, err := syncing.ParseWorkoutDetailChunk(bytes.NewReader(wire), start, end)
	if err != nil {
		t.Fatal(err)
	}
	extra.WorkoutID, extra.ChunkIndex = id, activities
	lease := takeUpload(t, f.s, f.p)
	if _, err = f.s.CommitWorkoutPackageChunk(ctx, f.p, extra, lease); err == nil {
		t.Fatal("package accepted more than 20,000 details")
	} else {
		requireCode(t, err, "workout_package_too_large")
	}
	if err = f.s.ReleaseUpload(ctx, f.p, lease); err != nil {
		t.Fatal(err)
	}
	after, err := f.s.WorkoutPackage(ctx, f.p, id)
	if err != nil || after.CommittedChunks != state.CommittedChunks || after.TotalDetails != state.TotalDetails || after.ManifestSHA256 != state.ManifestSHA256 {
		t.Fatalf("over-limit attempt changed package: %+v, %v", after, err)
	}
	seal := syncing.WorkoutPackageSeal{ChunkCount: activities, TotalDetails: state.TotalDetails, ManifestSHA256: state.ManifestSHA256}
	sealStarted := time.Now()
	body, err := f.s.SealWorkoutPackage(ctx, f.p, id, seal)
	if err != nil {
		t.Fatal("full package seal:", err)
	}
	sealDuration := time.Since(sealStarted)
	var receipt syncing.WorkoutPackageReceipt
	if err = json.Unmarshal(body, &receipt); err != nil || receipt.IngestStatus != "sealed" || receipt.TotalDetails != syncing.MaxWorkoutPackageDetails || receipt.CommitRevision != 1 {
		t.Fatalf("full package receipt: %+v, %v", receipt, err)
	}
	if retry, err := f.s.SealWorkoutPackage(ctx, f.p, id, seal); err != nil || !bytes.Equal(retry, body) {
		t.Fatalf("seal retry changed receipt: %v", err)
	}
	if count(t, f, "workouts") != 1 || count(t, f, "workout_activities") != activities || count(t, f, "jobs") != 1 || count(t, f, "change_events") != 1 || count(t, f, "dirty_scopes") != 1 {
		t.Fatal("full package publication incomplete")
	}
	var payload []byte
	if err = f.s.Pool.QueryRow(ctx, `SELECT payload FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2`, f.p.DatasetID, id).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var stored syncing.Event
	if err = syncing.DecodeJSON(payload, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Workout == nil || len(stored.Workout.Activities) != activities {
		t.Fatal("full workout activities missing")
	}
	rows, err := f.s.Pool.Query(ctx, `SELECT ordinal,payload FROM workout_activities WHERE dataset_id=$1 AND workout_uuid=$2 ORDER BY ordinal`, f.p.DatasetID, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for i, activity := range stored.Workout.Activities {
		want := 256
		if i == activities-1 {
			want = 210
		}
		if activity.UUID != activityIDs[i] || len(activity.Events) != want {
			t.Fatalf("activity %d identity or event count differs", i)
		}
		for j, event := range activity.Events {
			if event.Type == nil || *event.Type != int64(j+1) || event.StartUs == nil || *event.StartUs != start+int64(j) || event.EndUs == nil || *event.EndUs != start+int64(j)+1 {
				t.Fatalf("activity %d event %d differs", i, j)
			}
		}
		if !rows.Next() {
			t.Fatalf("missing activity projection %d", i)
		}
		var ordinal int
		var projected []byte
		if err = rows.Scan(&ordinal, &projected); err != nil {
			t.Fatal(err)
		}
		var projection syncing.WorkoutActivity
		if err = syncing.DecodeJSON(projected, &projection); err != nil {
			t.Fatal(err)
		}
		wantPayload, err := json.Marshal(activity)
		if err != nil {
			t.Fatal(err)
		}
		gotPayload, err := json.Marshal(projection)
		if err != nil || ordinal != i || !bytes.Equal(gotPayload, wantPayload) {
			t.Fatalf("activity projection %d differs: %v", i, err)
		}
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatalf("unexpected activity projection rows: %v", rows.Err())
	}
	t.Logf("SCALE_WORKOUT_DETAILS total=20000 activities=%d nested_events=19922 chunks=%d staging_and_seal=%s seal=%s", activities, activities, time.Since(started), sealDuration)
}

func scaleWorkoutActivityLine(ordinal, events int, start, end int64) string {
	var b strings.Builder
	fmt.Fprintf(&b, `{"kind":"activity","ordinal":%d,"activity":{"uuid":"%s","startUs":%d,"durationUs":%d,"configuration":{"activityType":37,"locationType":1,"swimmingLocationType":0},"events":[`, ordinal, syncing.NewID(), start, end-start)
	for i := 0; i < events; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"type":%d,"startUs":%d,"endUs":%d}`, i+1, start+int64(i), start+int64(i)+1)
	}
	b.WriteString(`]}}`)
	return b.String()
}
