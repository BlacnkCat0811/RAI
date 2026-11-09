package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/syncing"
)

// lockIngestDataset serializes quota accounting and the insert for one
// dataset. The table has no dataset foreign key, so this gate must not be
// replaced by a datasets row lock; a first upload can arrive while a fact
// import holds that row.
func (s *Store) lockIngestDataset(dataset string) func() {
	s.ingestMu.Lock()
	if s.ingestLocks == nil {
		s.ingestLocks = make(map[string]*sync.Mutex)
	}
	lock := s.ingestLocks[dataset]
	if lock == nil {
		lock = &sync.Mutex{}
		s.ingestLocks[dataset] = lock
	}
	s.ingestMu.Unlock()
	lock.Lock()
	return lock.Unlock
}

// AcceptWorkoutIngest stores the first raw protocol-4 payload for a UUID. It
// intentionally does not decode facts and never takes the dataset row lock.
// The worker owns semantic validation and all fact writes.
func (s *Store) AcceptWorkoutIngest(ctx context.Context, p syncing.Principal, envelope syncing.WorkoutIngestEnvelope, raw []byte) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > syncing.MaxWorkoutBundleBytes {
		return nil, syncing.Fail(413, "bundle_too_large")
	}
	release := s.lockIngestDataset(p.DatasetID)
	defer release()
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var actualEpoch, collectorEpoch string
	var retired, canCollect bool
	err = tx.QueryRow(ctx, `SELECT d.epoch::text, d.retired_at IS NOT NULL,
		COALESCE((SELECT l.collector_epoch::text FROM collector_leases l
			WHERE l.dataset_id=d.id AND l.device_id=$2), ''),
		EXISTS(SELECT 1 FROM devices dv WHERE dv.dataset_id=d.id AND dv.id=$2
			AND dv.can_collect AND dv.revoked_at IS NULL)
		FROM datasets d WHERE d.id=$1`, p.DatasetID, p.DeviceID).
		Scan(&actualEpoch, &retired, &collectorEpoch, &canCollect)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, syncing.Fail(403, "dataset_forbidden")
	}
	if err != nil {
		return nil, err
	}
	if retired {
		return nil, syncing.Fail(409, "dataset_retired")
	}
	if !canCollect || collectorEpoch == "" {
		return nil, syncing.Fail(409, "collector_fenced")
	}
	if envelope.DatasetID != p.DatasetID {
		return nil, syncing.Fail(403, "dataset_forbidden")
	}
	if envelope.DatasetEpoch != actualEpoch {
		return nil, syncing.Fail(409, "dataset_epoch_changed")
	}
	if envelope.CollectorEpoch != collectorEpoch {
		return nil, syncing.Fail(409, "collector_fenced")
	}

	// Tombstone, original acceptance and an already-imported Workout are read
	// before quota. Duplicates never consume capacity or get new bytes.
	var deleted, imported bool
	var acceptedAt *time.Time
	err = tx.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM entity_tombstones t WHERE t.dataset_id=$1 AND t.sample_type=$2 AND t.sample_uuid=$3),
		i.accepted_at,
		EXISTS(SELECT 1 FROM workouts w WHERE w.dataset_id=$1 AND w.workout_uuid=$3)
		FROM (SELECT 1) AS one
		LEFT JOIN workout_ingests i ON i.dataset_id=$1 AND i.workout_uuid=$3`,
		p.DatasetID, syncing.WorkoutType, envelope.WorkoutID).Scan(&deleted, &acceptedAt, &imported)
	if err != nil {
		return nil, err
	}
	if deleted {
		return nil, syncing.Fail(409, "workout_deleted")
	}
	if acceptedAt != nil {
		body, err := ingestAcceptedReceipt(envelope.WorkoutID, *acceptedAt)
		if err != nil {
			return nil, err
		}
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		return body, nil
	}
	if imported {
		body, err := json.Marshal(map[string]string{"workoutID": envelope.WorkoutID, "ingestStatus": "already_imported"})
		if err != nil {
			return nil, err
		}
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		return body, nil
	}

	var pending int
	var pendingBytes int64
	if err = tx.QueryRow(ctx, `SELECT count(*), COALESCE(sum(payload_bytes),0) FROM workout_ingests
		WHERE dataset_id=$1 AND state='accepted'`, p.DatasetID).Scan(&pending, &pendingBytes); err != nil {
		return nil, err
	}
	if pending >= syncing.MaxWorkoutIngestBacklog || pendingBytes+int64(len(raw)) > syncing.MaxWorkoutIngestBacklogBytes {
		return nil, syncing.WorkoutIngestBacklogExceeded()
	}

	var insertedAt time.Time
	err = tx.QueryRow(ctx, `INSERT INTO workout_ingests(
		dataset_id,workout_uuid,raw_json,payload_bytes,device_id,dataset_epoch,collector_epoch,
		state,accepted_at,next_attempt_at)
		SELECT $1::uuid,$2::uuid,$3::bytea,$4::bigint,$5::uuid,$6::uuid,$7::uuid,
			'accepted',clock_timestamp(),clock_timestamp()
		WHERE NOT EXISTS (SELECT 1 FROM entity_tombstones
			WHERE dataset_id=$1::uuid AND sample_type=$8::text AND sample_uuid=$2::uuid)
		ON CONFLICT (dataset_id,workout_uuid) DO NOTHING
		RETURNING accepted_at`, p.DatasetID, envelope.WorkoutID, raw, int64(len(raw)),
		p.DeviceID, envelope.DatasetEpoch, envelope.CollectorEpoch, syncing.WorkoutType).Scan(&insertedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// A concurrent process inserted the same UUID, or a deletion committed
		// between the checks and this statement. Read the authoritative answer.
		var existing *time.Time
		var nowDeleted bool
		if err = tx.QueryRow(ctx, `SELECT
			EXISTS(SELECT 1 FROM entity_tombstones t WHERE t.dataset_id=$1 AND t.sample_type=$2 AND t.sample_uuid=$3),
			i.accepted_at
			FROM (SELECT 1) AS one
			LEFT JOIN workout_ingests i ON i.dataset_id=$1 AND i.workout_uuid=$3`,
			p.DatasetID, syncing.WorkoutType, envelope.WorkoutID).Scan(&nowDeleted, &existing); err != nil {
			return nil, err
		}
		if nowDeleted {
			return nil, syncing.Fail(409, "workout_deleted")
		}
		if existing == nil {
			return nil, syncing.Fail(409, "workout_ingest_conflict")
		}
		insertedAt = *existing
	} else if err != nil {
		return nil, err
	}
	body, err := ingestAcceptedReceipt(envelope.WorkoutID, insertedAt)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return body, nil
}

func ingestAcceptedReceipt(workoutID string, acceptedAt time.Time) (json.RawMessage, error) {
	body, err := json.Marshal(syncing.WorkoutIngestReceipt{WorkoutID: workoutID, IngestStatus: "accepted",
		AcceptedAt: acceptedAt.UTC()})
	if err != nil {
		return nil, err
	}
	return body, nil
}

// WorkoutIngest answers from one statement snapshot. Tombstone priority is
// checked before the ingest row, so a deletion is never hidden by an older
// acceptance or commit.
func (s *Store) WorkoutIngest(ctx context.Context, p syncing.Principal, workoutID string) (syncing.WorkoutIngestStatus, error) {
	if !syncing.ValidID(workoutID) {
		return syncing.WorkoutIngestStatus{}, syncing.Fail(422, "unsupported_payload")
	}
	var deleted bool
	var state *string
	var acceptedAt *time.Time
	var revision *int64
	var receipt []byte
	var errorCode *string
	var imported bool
	err := s.Pool.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM entity_tombstones t WHERE t.dataset_id=$1 AND t.sample_type=$2 AND t.sample_uuid=$3),
		i.state, i.accepted_at, i.commit_revision, i.commit_receipt, i.error_code,
		EXISTS(SELECT 1 FROM workouts w WHERE w.dataset_id=$1 AND w.workout_uuid=$3)
		FROM (SELECT 1) AS one
		LEFT JOIN workout_ingests i ON i.dataset_id=$1 AND i.workout_uuid=$3`,
		p.DatasetID, syncing.WorkoutType, workoutID).
		Scan(&deleted, &state, &acceptedAt, &revision, &receipt, &errorCode, &imported)
	if err != nil {
		return syncing.WorkoutIngestStatus{}, err
	}
	if deleted {
		return syncing.WorkoutIngestStatus{WorkoutID: workoutID, IngestStatus: "deleted"}, nil
	}
	if state != nil {
		status := syncing.WorkoutIngestStatus{WorkoutID: workoutID, IngestStatus: *state, AcceptedAt: acceptedAt}
		switch *state {
		case "committed":
			status.CommitRevision = revision
			status.AnalysisStatus = "not_required"
			status.AnalysisJobIDs = []string{}
			if len(receipt) > 0 {
				var parsed syncing.WorkoutImportReceipt
				if err = json.Unmarshal(receipt, &parsed); err != nil {
					return syncing.WorkoutIngestStatus{}, err
				}
				status.AnalysisStatus = parsed.AnalysisStatus
				status.AnalysisJobIDs = parsed.AnalysisJobIDs
				if status.CommitRevision == nil {
					value := parsed.CommitRevision
					status.CommitRevision = &value
				}
			}
		case "rejected":
			if errorCode != nil {
				status.ErrorCode = *errorCode
			}
		}
		return status, nil
	}
	if imported {
		return syncing.WorkoutIngestStatus{WorkoutID: workoutID, IngestStatus: "already_imported"}, nil
	}
	return syncing.WorkoutIngestStatus{}, syncing.Fail(404, "workout_ingest_not_found")
}

type workoutIngestClaim struct {
	datasetID      string
	workoutID      string
	deviceID       string
	datasetEpoch   string
	collectorEpoch string
	attempts       int
	raw            []byte
}

func (s *Store) claimWorkoutIngest(ctx context.Context) (workoutIngestClaim, bool, error) {
	var claim workoutIngestClaim
	tx, err := s.begin(ctx)
	if err != nil {
		return claim, false, err
	}
	defer tx.Rollback(ctx)
	leaseSeconds := int64(syncing.WorkoutIngestLease / time.Second)
	err = tx.QueryRow(ctx, `WITH candidate AS (
		SELECT dataset_id, workout_uuid FROM workout_ingests
		WHERE state='accepted' AND raw_json IS NOT NULL
			AND next_attempt_at <= clock_timestamp()
			AND (lease_until IS NULL OR lease_until <= clock_timestamp())
		ORDER BY next_attempt_at, accepted_at
		FOR UPDATE SKIP LOCKED
		LIMIT 1
	)
	UPDATE workout_ingests i
	SET lease_until=clock_timestamp()+$1::bigint*interval '1 second', attempts=i.attempts+1
	FROM candidate c
	WHERE i.dataset_id=c.dataset_id AND i.workout_uuid=c.workout_uuid
	RETURNING i.dataset_id::text, i.workout_uuid::text, i.raw_json, i.device_id::text,
		i.dataset_epoch::text, i.collector_epoch::text, i.attempts`, leaseSeconds).
		Scan(&claim.datasetID, &claim.workoutID, &claim.raw, &claim.deviceID,
			&claim.datasetEpoch, &claim.collectorEpoch, &claim.attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return claim, false, nil
	}
	if err != nil {
		return claim, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return claim, false, err
	}
	return claim, true, nil
}

// ProcessNextWorkoutIngest is the worker's single parse slot. It claims one
// due row, decodes and imports the stored bytes, and finishes the ingest row in
// the same fact transaction. The caller decides when to poll again.
func (s *Store) ProcessNextWorkoutIngest(ctx context.Context) (syncing.WorkoutIngestOutcome, error) {
	claim, ok, err := s.claimWorkoutIngest(ctx)
	if err != nil || !ok {
		return syncing.WorkoutIngestOutcome{}, err
	}
	outcome := syncing.WorkoutIngestOutcome{Claimed: true, WorkoutID: claim.workoutID, Attempts: claim.attempts}
	processCtx, cancel := context.WithTimeout(ctx, syncing.WorkoutIngestProcessingBudget)
	defer cancel()
	code, permanent := s.processWorkoutIngest(processCtx, claim)
	if code == "" {
		outcome.Status = "committed"
		return outcome, nil
	}
	outcome.ErrorCode = code
	if permanent {
		if code == "workout_ingest_lease_lost" {
			outcome.Status = "lease_lost"
			return outcome, nil
		}
		clearRaw := code == "workout_deleted"
		applied, finishErr := s.finishWorkoutIngest(ctx, claim, "rejected", code, clearRaw, time.Time{})
		if finishErr != nil {
			return outcome, finishErr
		}
		if !applied {
			outcome.Status = "lease_lost"
			return outcome, nil
		}
		outcome.Status = "rejected"
		return outcome, nil
	}
	if claim.attempts >= syncing.WorkoutIngestAttempts {
		code = "processing_attempts_exhausted"
		applied, finishErr := s.finishWorkoutIngest(ctx, claim, "rejected", code, false, time.Time{})
		if finishErr != nil {
			return outcome, finishErr
		}
		outcome.ErrorCode = code
		if !applied {
			outcome.Status = "lease_lost"
			return outcome, nil
		}
		outcome.Status = "rejected"
		return outcome, nil
	}
	next := time.Now().Add(syncing.WorkoutIngestRetryDelay * time.Duration(1<<(claim.attempts-1)))
	applied, finishErr := s.finishWorkoutIngest(ctx, claim, "accepted", code, false, next)
	if finishErr != nil {
		return outcome, finishErr
	}
	if !applied {
		outcome.Status = "lease_lost"
		return outcome, nil
	}
	outcome.Status = "retry"
	return outcome, nil
}

// processWorkoutIngest returns (error code, permanent). An empty code means the
// fact transaction committed and the ingest row is already committed.
func (s *Store) processWorkoutIngest(ctx context.Context, claim workoutIngestClaim) (string, bool) {
	decodeStart := time.Now()
	bundle, err := syncing.DecodeWorkoutBundlePayloadJSON(claim.raw)
	if err != nil {
		return ingestErrorCode(err), true
	}
	if time.Since(decodeStart) > syncing.WorkoutIngestDecodeBudget {
		return "json_decode_timeout", true
	}
	if bundle.DatasetID != claim.datasetID || bundle.WorkoutID != claim.workoutID {
		return "workout_ingest_mismatch", true
	}
	principal := syncing.Principal{DatasetID: claim.datasetID, DeviceID: claim.deviceID, CanCollect: true}
	_, err = s.importWorkoutBundle(ctx, principal, bundle, claim.raw, func(ctx context.Context, tx pgx.Tx, receipt json.RawMessage, revision int64) error {
		tag, err := tx.Exec(ctx, `UPDATE workout_ingests
			SET state='committed', commit_revision=$4, commit_receipt=$5,
				finished_at=clock_timestamp(), error_code=NULL, lease_until=NULL
			WHERE dataset_id=$1 AND workout_uuid=$2 AND state='accepted' AND attempts=$3
				AND lease_until > clock_timestamp()`,
			claim.datasetID, claim.workoutID, claim.attempts, revision, string(receipt))
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return syncing.Fail(409, "workout_ingest_lease_lost")
		}
		return nil
	})
	if err == nil {
		return "", false
	}
	code, permanent := ingestFailure(err)
	if code == "workout_ingest_lease_lost" {
		// Another attempt owns the row now; this attempt must not finalize it.
		return code, true
	}
	return code, permanent
}

func ingestFailure(err error) (string, bool) {
	var failure *syncing.Error
	if errors.As(err, &failure) {
		// A missing status means a semantic failure constructed by Fail().
		if failure.Status == 0 || (failure.Status >= 400 && failure.Status < 500 && failure.Status != 408 && failure.Status != 429) {
			return failure.Code, true
		}
		return failure.Code, false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "processing_timeout", false
	}
	return "processing_failed", false
}

func ingestErrorCode(err error) string {
	var failure *syncing.Error
	if errors.As(err, &failure) && failure.Code != "" {
		return failure.Code
	}
	return "invalid_payload"
}

func (s *Store) finishWorkoutIngest(ctx context.Context, claim workoutIngestClaim, state, code string, clearRaw bool, next time.Time) (bool, error) {
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	tag, err := s.Pool.Exec(finishCtx, `UPDATE workout_ingests
		SET state=$4,
			error_code=$5,
			finished_at=CASE WHEN $4='rejected' THEN clock_timestamp() ELSE NULL END,
			next_attempt_at=CASE WHEN $4='accepted' AND $6::timestamptz IS NOT NULL THEN $6::timestamptz ELSE next_attempt_at END,
			lease_until=NULL,
			raw_json=CASE WHEN $7::boolean THEN NULL ELSE raw_json END
		WHERE dataset_id=$1 AND workout_uuid=$2 AND state='accepted' AND attempts=$3
			AND lease_until > clock_timestamp()`,
		claim.datasetID, claim.workoutID, claim.attempts, state, code, nullableTime(next), clearRaw)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

func nullableTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

// RecoverExpiredWorkoutIngests rejects attempts whose last claim is gone. A
// claimed row that is still inside its lease is never touched.
func (s *Store) RecoverExpiredWorkoutIngests(ctx context.Context) (int, error) {
	tag, err := s.Pool.Exec(ctx, `UPDATE workout_ingests
		SET state='rejected', error_code='processing_attempts_exhausted',
			finished_at=clock_timestamp(), lease_until=NULL
		WHERE state='accepted' AND attempts >= $1
			AND lease_until IS NOT NULL AND lease_until <= clock_timestamp()`,
		syncing.WorkoutIngestAttempts)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// clearWorkoutIngestRaw runs inside an explicit Workout deletion transaction.
// It keeps the acceptance identity and receipt while removing the source bytes;
// an accepted row becomes a terminal rejection so the worker cannot import it.
func clearWorkoutIngestRaw(ctx context.Context, tx pgx.Tx, dataset, workoutID string) error {
	_, err := tx.Exec(ctx, `UPDATE workout_ingests
		SET raw_json=NULL,
			state=CASE WHEN state='accepted' THEN 'rejected' ELSE state END,
			error_code=CASE WHEN state='accepted' THEN 'workout_deleted' ELSE error_code END,
			finished_at=CASE WHEN state='accepted' THEN clock_timestamp() ELSE finished_at END,
			lease_until=NULL
		WHERE dataset_id=$1 AND workout_uuid=$2 AND raw_json IS NOT NULL`,
		dataset, workoutID)
	return err
}
