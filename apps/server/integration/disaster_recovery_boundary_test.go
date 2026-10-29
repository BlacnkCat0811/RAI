//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	neturl "net/url"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
	"rai/server/internal/training"
)

// A quiesced, real PostgreSQL template copy represents state A in these
// protocol boundary tests. Actual custom-dump/SSH/new-instance SQL restoration
// is independently exercised by scripts/test-monthly-compose.sh.
func recoverySnapshot(t *testing.T, f *fixture) *fixture {
	t.Helper()
	ctx := context.Background()
	sourceURL := f.s.Pool.Config().ConnString()
	source, err := neturl.Parse(sourceURL)
	if err != nil {
		t.Fatal(err)
	}
	f.s.Pool.Close()
	base, err := pgx.Connect(ctx, os.Getenv("RAI_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	name := "rai_test_" + strings.ReplaceAll(syncing.NewID(), "-", "")
	if _, err = base.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" TEMPLATE "+pgx.Identifier{strings.TrimPrefix(source.Path, "/")}.Sanitize()); err != nil {
		base.Close(ctx)
		t.Fatal(err)
	}
	f.s, err = postgres.Open(ctx, sourceURL, 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.s.Pool.Close)
	source.Path = "/" + name
	restored, err := postgres.Open(ctx, source.String(), 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		restored.Pool.Close()
		if _, e := base.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); e != nil {
			t.Error(e)
		}
		base.Close(context.Background())
	})
	return &fixture{s: restored, enrollment: f.enrollment, p: f.p, stream: f.stream}
}

func TestDisasterRecoveryManualHistoryBoundary(t *testing.T) {
	f, plan := trainingFixture(t)
	ctx := context.Background()
	session := plan.Effective.Sessions[0].ID
	before := editTraining(t, f, plan, session, "distanceM", 5000, 0)
	execution, err := f.s.StartTrainingExecution(ctx, f.p, training.StartRequest{CommandID: syncing.NewID(), ExecutionID: syncing.NewID(), PlanID: plan.ID, EffectiveID: before.ID, SessionID: session})
	if err != nil {
		t.Fatal(err)
	}
	a := recoverySnapshot(t, f)
	after := editTraining(t, f, plan, session, "distanceM", 7000, 1)
	if sessionID(after.Base, session).DistanceM != 7000 {
		t.Fatal("post-backup source edit must really exist")
	}
	completed, err := f.s.ChangeTrainingExecution(ctx, f.p, execution.ID, training.ExecutionChange{CommandID: syncing.NewID(), ExpectedRevision: 1, Operation: "complete"})
	if err != nil || completed.Status != "completed" || len(completed.Feedback) == 0 {
		t.Fatal("post-backup execution/feedback must really exist", err)
	}
	restored, err := a.s.TrainingPlan(ctx, a.p, plan.ID)
	if err != nil || restored.Effective == nil || sessionID(restored.Effective.Base, session).DistanceM != 5000 || restored.OverrideRevision != 1 {
		t.Fatal("restore must lose the later 7km command, not silently regenerate it", err)
	}
	restoredExecution, err := a.s.TrainingExecution(ctx, a.p, execution.ID)
	if err != nil || restoredExecution.Status != "started" || len(restoredExecution.Feedback) != 0 || !reflect.DeepEqual(restoredExecution.Snapshot, execution.Snapshot) {
		t.Fatal("backup preserves the frozen snapshot but not later completion/feedback", err)
	}
	recalcTraining(t, a, restored)
	runTraining(t, a)
	latest, err := a.s.TrainingPlan(ctx, a.p, plan.ID)
	if err != nil || latest.Effective.ID == before.ID || sessionID(latest.Effective.Base, session).DistanceM != 5000 {
		t.Fatal("recalculation must append and keep the restored override", err)
	}
	audit, err := a.s.TrainingAudit(ctx, a.p, plan.ID, before.ID)
	if err != nil || !reflect.DeepEqual(audit.Effective, before) {
		t.Fatal("recalculation modified restored immutable Effective history", err)
	}
	again, err := a.s.TrainingExecution(ctx, a.p, execution.ID)
	if err != nil || !reflect.DeepEqual(again, restoredExecution) {
		t.Fatal("recalculation rewrote the restored execution", err)
	}
	t.Log("backup-before-edit: 5km retained, later 7km/completion/feedback absent; restored override and frozen history survive a new Base")
}

func TestDisasterRecoveryExistingContractBoundary(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	lostDelete, knownDelete, retained, missing := syncing.NewID(), syncing.NewID(), syncing.NewID(), syncing.NewID()
	one, _, _ := f.commit(t, []syncing.Event{sample(lostDelete, 120), sample(knownDelete, 121), sample(retained, 122)}, 1, nil)
	two, _, _ := f.commit(t, []syncing.Event{deleted(knownDelete)}, 2, &one.BatchID)
	a := recoverySnapshot(t, f)
	commitAndRetainWire := func(events []syncing.Event, seq int64, previous *string) (syncing.Envelope, []byte) {
		e, wire := f.envelope(t, events, seq, previous)
		if _, err := f.s.RegisterBatch(ctx, f.p, e); err != nil {
			t.Fatal(err)
		}
		parsed, err := syncing.ParseBody(bytes.NewReader(wire), e, f.stream.SampleType)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.s.Commit(ctx, f.p, e, parsed); err != nil {
			t.Fatal(err)
		}
		return e, wire
	}
	three, wire3 := commitAndRetainWire([]syncing.Event{deleted(lostDelete)}, 3, &two.BatchID)
	four, wire4 := commitAndRetainWire([]syncing.Event{sample(missing, 123)}, 4, &three.BatchID)

	old, err := a.s.Metadata(ctx, a.p)
	if err != nil {
		t.Fatal(err)
	}
	current, err := f.s.Metadata(ctx, f.p)
	if err != nil {
		t.Fatal(err)
	}
	if old.DatasetID != current.DatasetID || old.DatasetEpoch != current.DatasetEpoch || old.ServiceID != current.ServiceID || old.CommitRevision >= current.CommitRevision {
		t.Fatal("snapshot must retain identities but lose newer revisions")
	}
	stream, err := a.s.Stream(ctx, a.p, a.stream.StreamID)
	if err != nil || stream.LastClientSeq != 2 {
		t.Fatal("restored stream must be behind client delivered position 4", err)
	}
	_, err = a.s.Batch(ctx, a.p, four.BatchID)
	requireCode(t, err, "batch_not_found")
	next, _ := a.envelope(t, []syncing.Event{sample(syncing.NewID(), 124)}, 5, &four.BatchID)
	_, err = a.s.RegisterBatch(ctx, a.p, next)
	requireCode(t, err, "sequence_gap")
	var staleFact, missingTombstone int
	if err = a.s.Pool.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM health_samples WHERE dataset_id=$1 AND sample_uuid=$2),
 (SELECT count(*) FROM entity_tombstones WHERE dataset_id=$1 AND sample_uuid=$2)`, a.p.DatasetID, lostDelete).Scan(&staleFact, &missingTombstone); err != nil {
		t.Fatal(err)
	}
	if staleFact != 1 || missingTombstone != 0 {
		t.Fatal("Create -> Backup -> Delete -> Restore loses the deletion proof")
	}
	t.Log("BLOCKER: unchanged identities + sequence gap + lost receipt + lost tombstone; an empty current HealthKit enumeration cannot prove permission-complete deletion")

	// If exact immutable payloads DID survive, P3 can replay them in their
	// original contiguous order. P7 GC makes their availability unprovable for
	// already-delivered pages; this is not an automatic recovery algorithm.
	for _, replay := range []struct {
		envelope syncing.Envelope
		wire     []byte
	}{{three, wire3}, {four, wire4}} {
		if _, err = a.s.RegisterBatch(ctx, a.p, replay.envelope); err != nil {
			t.Fatal(err)
		}
		events, e := syncing.ParseBody(bytes.NewReader(replay.wire), replay.envelope, a.stream.SampleType)
		if e != nil {
			t.Fatal(e)
		}
		first, e := a.s.Commit(ctx, a.p, replay.envelope, events)
		if e != nil {
			t.Fatal(e)
		}
		retry, e := a.s.Commit(ctx, a.p, replay.envelope, events)
		if e != nil || !bytes.Equal(first, retry) {
			t.Fatal("original retry must return the restored instance's immutable receipt", e)
		}
	}
	_, receipt, _ := a.commit(t, []syncing.Event{sample(retained, 122), sample(missing, 123), sample(lostDelete, 120), sample(knownDelete, 121)}, 5, &four.BatchID)
	if receipt.Counts.Unchanged != 2 || receipt.Counts.Suppressed != 2 || receipt.Counts.Inserted != 0 {
		b, _ := json.Marshal(receipt.Counts)
		t.Fatalf("known barriers and canonical identities must survive replay: %s", b)
	}
	var total int
	if err = a.s.Pool.QueryRow(ctx, `SELECT count(*) FROM health_samples WHERE dataset_id=$1`, a.p.DatasetID).Scan(&total); err != nil || total != 2 {
		t.Fatal("duplicate import multiplied facts", err)
	}
}
