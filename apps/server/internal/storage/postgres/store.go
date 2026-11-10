package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	neturl "net/url"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"rai/server/internal/storage/postgres/dbgen"
	"rai/server/internal/syncing"
)

type Store struct {
	Pool *pgxpool.Pool
	q    *dbgen.Queries

	// Acceptance serializes backlog accounting per dataset inside one process.
	// Multi-process races still resolve through the ingest primary key.
	ingestMu    sync.Mutex
	ingestLocks map[string]*sync.Mutex
}

func Open(ctx context.Context, url string, maxConnections int32) (*Store, error) {
	return OpenWithTracer(ctx, url, maxConnections, nil)
}

// OpenWithTracer is the same production pool contract with an optional pgx
// query tracer. It exists for reproducible SQL-count evidence; callers cannot
// use it to change durability, timeouts or pool sizing.
func OpenWithTracer(ctx context.Context, url string, maxConnections int32, tracer pgx.QueryTracer) (*Store, error) {
	c, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errors.New("invalid database configuration")
	}
	// pgx supports pool_max_conns, pool_min_conns, pool_max_conn_lifetime and
	// pool_max_conn_idle_time in the URL. Apply conservative defaults only when
	// the corresponding option was not explicitly configured.
	u, _ := neturl.Parse(url)
	if u == nil || !u.Query().Has("pool_max_conns") {
		c.MaxConns = maxConnections
	}
	if u == nil || !u.Query().Has("pool_max_conn_lifetime") {
		c.MaxConnLifetime = 30 * time.Minute
	}
	c.ConnConfig.ConnectTimeout = 3 * time.Second
	c.ConnConfig.RuntimeParams["timezone"] = "UTC"
	c.ConnConfig.RuntimeParams["synchronous_commit"] = "on"
	c.ConnConfig.RuntimeParams["statement_timeout"] = "2000"
	c.ConnConfig.RuntimeParams["lock_timeout"] = "500"
	c.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "5000"
	if tracer != nil {
		c.ConnConfig.Tracer = tracer
	}
	p, err := pgxpool.NewWithConfig(ctx, c)
	if err != nil {
		return nil, err
	}
	return &Store{Pool: p, q: dbgen.New(p)}, nil
}
func (s *Store) Ready(ctx context.Context) error {
	// Constant-sized schema evidence, no business table scan and no implicit migration.
	var ok bool
	err := s.Pool.QueryRow(ctx, `SELECT to_regclass('sync_batches') IS NOT NULL AND to_regclass('jobs') IS NOT NULL AND to_regclass('analysis_publications') IS NOT NULL AND to_regclass('training_effective_versions') IS NOT NULL AND to_regclass('daily_health_coverage') IS NOT NULL AND to_regclass('analysis_artifacts') IS NOT NULL AND to_regclass('analysis_model_channels') IS NOT NULL AND to_regclass('athlete_model_inputs') IS NOT NULL AND to_regclass('workout_ingests') IS NOT NULL`).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("schema not ready")
	}
	var version int64
	if err = s.Pool.QueryRow(ctx, `SELECT version_id FROM goose_db_version WHERE is_applied ORDER BY id DESC LIMIT 1`).Scan(&version); err != nil {
		return err
	}
	if version != RequiredSchemaVersion {
		return errors.New("unsupported schema version")
	}
	return nil
}

func (s *Store) Authenticate(ctx context.Context, token string) (syncing.Principal, error) {
	h := sha256.Sum256([]byte(token))
	r, err := s.q.Authenticate(ctx, h[:])
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return syncing.Principal{}, syncing.Fail(401, "unauthorized")
		}
		return syncing.Principal{}, err
	}
	return syncing.Principal{DatasetID: r.DatasetID, DeviceID: r.ID, CanCollect: r.CanCollect, DatasetRetired: r.DatasetRetired}, nil
}
func (s *Store) Metadata(ctx context.Context, p syncing.Principal) (syncing.Metadata, error) {
	var m syncing.Metadata
	err := s.Pool.QueryRow(ctx, `SELECT d.id::text,d.epoch::text,d.service_id::text,d.timezone,d.commit_rev,
 CASE WHEN to_jsonb(d)->>'retired_at' IS NULL THEN 'active' ELSE 'retired' END, $2::uuid::text,
 COALESCE((SELECT collector_epoch::text FROM collector_leases WHERE dataset_id=d.id AND device_id=$2::uuid),'') FROM datasets d WHERE d.id=$1`, p.DatasetID, p.DeviceID).Scan(&m.DatasetID, &m.DatasetEpoch, &m.ServiceID, &m.Timezone, &m.CommitRevision, &m.DatasetStatus, &m.DeviceID, &m.CollectorEpoch)
	return m, err
}
func (s *Store) Job(ctx context.Context, p syncing.Principal, id string) (any, error) {
	r, err := s.q.Job(ctx, dbgen.JobParams{DatasetID: p.DatasetID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, syncing.Fail(404, "job_not_found")
	}
	return map[string]any{"jobID": r.ID, "kind": r.Kind, "scopeKey": r.ScopeKey, "targetGeneration": r.TargetGeneration, "state": r.State, "attempts": r.Attempts, "lastErrorCode": r.LastErrorCode}, err
}

func (s *Store) begin(ctx context.Context) (pgx.Tx, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout = '5s'`); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

// lockDataset is the first business row lock for fact/stream/batch writes.
// It serializes revision assignment with actual transaction commit order;
// independent operational admission budgets never assign business revisions.
func lockDatasetRow(ctx context.Context, tx pgx.Tx, p syncing.Principal, dataset string) (string, int64, error) {
	if dataset != p.DatasetID {
		return "", 0, syncing.Fail(403, "dataset_forbidden")
	}
	if !p.CanCollect {
		return "", 0, syncing.Fail(403, "read_only_device")
	}
	var actualEpoch string
	var rev int64
	var retired bool
	err := tx.QueryRow(ctx, `SELECT epoch::text, commit_rev, to_jsonb(d)->>'retired_at' IS NOT NULL FROM datasets d WHERE id=$1 FOR UPDATE`, dataset).Scan(&actualEpoch, &rev, &retired)
	if err == nil && retired {
		return "", 0, syncing.Fail(409, "dataset_retired")
	}
	return actualEpoch, rev, err
}

// checkCollectorLease rechecks revocation/role inside the write transaction
// after obtaining the dataset lock.
func checkCollectorLease(ctx context.Context, tx pgx.Tx, p syncing.Principal, collector string) error {
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM collector_leases l JOIN devices d ON d.dataset_id=l.dataset_id AND d.id=l.device_id
		WHERE l.dataset_id=$1 AND l.device_id=$2 AND l.collector_epoch=$3 AND d.can_collect AND d.revoked_at IS NULL)`, p.DatasetID, p.DeviceID, collector).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return syncing.Fail(409, "collector_fenced")
	}
	return nil
}

func lockDataset(ctx context.Context, tx pgx.Tx, p syncing.Principal, dataset, epoch, collector string) (int64, error) {
	actualEpoch, rev, err := lockDatasetRow(ctx, tx, p, dataset)
	if err != nil {
		return 0, err
	}
	if actualEpoch != epoch {
		return 0, syncing.Fail(409, "dataset_epoch_changed")
	}
	if err = checkCollectorLease(ctx, tx, p, collector); err != nil {
		return 0, err
	}
	return rev, nil
}
func sequenceError(expected int64) error {
	return &syncing.Error{Status: 409, Code: "sequence_gap", ExpectedSeq: &expected}
}
func uniqueViolation(err error) bool {
	var e *pgconn.PgError
	return errors.As(err, &e) && e.Code == "23505"
}
func lockedStream(ctx context.Context, tx pgx.Tx, p syncing.Principal, e syncing.Envelope) (syncing.StreamState, error) {
	var r syncing.StreamState
	err := tx.QueryRow(ctx, `SELECT id::text,collector_epoch::text,sample_type,query_fingerprint,last_client_seq,last_batch_id::text
		FROM sync_streams WHERE dataset_id=$1 AND id=$2 FOR UPDATE`, p.DatasetID, e.StreamID).Scan(&r.StreamID, &r.CollectorEpoch, &r.SampleType, &r.QueryFingerprint, &r.LastClientSeq, &r.LastBatchID)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, syncing.Fail(404, "stream_not_found")
	}
	if err != nil {
		return r, err
	}
	if r.CollectorEpoch != e.CollectorEpoch || r.QueryFingerprint != e.QueryFingerprint {
		return r, syncing.Fail(409, "stream_identity_conflict")
	}
	return r, nil
}
func checkSequence(r syncing.StreamState, e syncing.Envelope) error {
	if e.ClientSeq != r.LastClientSeq+1 {
		return sequenceError(r.LastClientSeq + 1)
	}
	if (r.LastBatchID == nil) != (e.PreviousBatchID == nil) || (r.LastBatchID != nil && *r.LastBatchID != *e.PreviousBatchID) {
		return syncing.Fail(409, "previous_batch_mismatch")
	}
	return nil
}
func applyEvent(ctx context.Context, tx pgx.Tx, dataset, batch string, e syncing.Event, rev int64, ordinal int, counts *syncing.Counts, sampleScopes sampleDirtyScopes) (bool, string, error) {
	q := dbgen.New(tx)
	tombstone, err := q.TombstoneExists(ctx, dbgen.TombstoneExistsParams{DatasetID: dataset, SampleType: e.Type, SampleUuid: e.UUID})
	if err != nil {
		return false, "", err
	}
	if tombstone {
		if e.Op == "upsert" {
			counts.Suppressed++
		} else {
			counts.Unchanged++
		}
		return false, "", nil
	}
	var oldStart, oldEnd *time.Time
	var oldHash []byte
	if e.Kind == "workout" {
		var old dbgen.WorkoutIdentityRow
		old, err = q.WorkoutIdentity(ctx, dbgen.WorkoutIdentityParams{DatasetID: dataset, WorkoutUuid: e.UUID})
		if err == nil {
			oldStart, oldEnd, oldHash = &old.EventStart.Time, &old.EventEnd.Time, old.ContentSha256
		}
	} else {
		var old dbgen.SampleIdentityRow
		old, err = q.SampleIdentity(ctx, dbgen.SampleIdentityParams{DatasetID: dataset, SampleType: e.Type, SampleUuid: e.UUID})
		if err == nil {
			oldStart, oldEnd, oldHash = &old.EventStart.Time, &old.EventEnd.Time, old.ContentSha256
		}
	}
	exists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, "", err
	}
	var start, end *time.Time
	canonical, hash := e.Canonical()
	if e.Op == "upsert" {
		if e.Kind == "workout" {
			packaged, err := workoutPackageExists(ctx, tx, dataset, e.UUID)
			if err != nil {
				return false, "", err
			}
			if packaged {
				return false, "", syncing.Fail(409, "workout_package_conflict")
			}
		}
		if exists {
			if !bytes.Equal(oldHash, hash) {
				return false, "", syncing.Fail(409, "sample_identity_conflict")
			}
			counts.Unchanged++
			return false, "", nil
		}
		a, b := time.UnixMicro(*e.StartUs).UTC(), time.UnixMicro(*e.EndUs).UTC()
		start, end = &a, &b
		if e.Kind == "workout" {
			err = insertWorkout(ctx, tx, dataset, e, rev, canonical, hash)
		} else {
			var value *float64
			var unit *string
			if e.Quantity != nil {
				value = &e.Quantity.Value
				unit = &e.Quantity.Unit
			}
			metadata := e.Metadata
			if metadata == nil {
				metadata = map[string]any{}
			}
			meta, _ := json.Marshal(metadata)
			_, err = tx.Exec(ctx, `INSERT INTO health_samples(dataset_id,sample_type,sample_uuid,event_start,event_end,value_numeric,value_category,unit,source_bundle_id,content_sha256,entity_revision,metadata)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, dataset, e.Type, e.UUID, start, end, value, e.Category, unit, e.Source.BundleID, hash, rev, meta)
		}
		if err != nil {
			return false, "", err
		}
		counts.Inserted++
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO entity_tombstones(dataset_id,sample_type,sample_uuid,deleted_rev,previous_start,previous_end,reason) VALUES($1,$2,$3,$4,$5,$6,'healthkit_deleted_object')`, dataset, e.Type, e.UUID, rev, oldStart, oldEnd)
		if err != nil {
			return false, "", err
		}
		if e.Kind == "workout" {
			if err = deleteWorkoutPackage(ctx, tx, dataset, e.UUID); err != nil {
				return false, "", err
			}
			// A deleted Workout also permanently fences every route UUID that
			// belonged to it. The route rows themselves are removed by the
			// existing workout FK cascade.
			_, err = tx.Exec(ctx, `INSERT INTO entity_tombstones(dataset_id,sample_type,sample_uuid,deleted_rev,reason)
				SELECT dataset_id,$3,route_uuid,$4,'workout_deleted_object'
				FROM workout_routes WHERE dataset_id=$1 AND workout_uuid=$2
				ON CONFLICT(dataset_id,sample_type,sample_uuid) DO NOTHING`,
				dataset, e.UUID, syncing.RouteType, rev)
			if err != nil {
				return false, "", err
			}
		}
		if exists {
			if e.Kind == "workout" {
				_, err = tx.Exec(ctx, `DELETE FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2`, dataset, e.UUID)
			} else {
				_, err = tx.Exec(ctx, `DELETE FROM health_samples WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3`, dataset, e.Type, e.UUID)
			}
			if err != nil {
				return false, "", err
			}
			counts.Deleted++
		} else {
			counts.Tombstoned++
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO entity_changes(dataset_id,batch_id,ordinal,commit_rev,sample_type,sample_uuid,operation,previous_start,previous_end,new_start,new_end)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, dataset, batch, ordinal, rev, e.Type, e.UUID, e.Op, oldStart, oldEnd, start, end)
	if err != nil {
		return false, "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO change_events(dataset_id,commit_rev,ordinal,event_id,entity_kind,entity_key,operation,payload) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, dataset, rev, ordinal, syncing.NewID(), e.Kind, e.Type+":"+e.UUID, e.Op, canonical)
	if err != nil {
		return false, "", err
	}
	// Unknown deletes create only a barrier, no fabricated analysis range.
	if start == nil {
		start, end = oldStart, oldEnd
	}
	if start == nil {
		return true, "", nil
	}
	scope := e.Kind + ":" + e.Type + ":" + e.UUID
	if e.Kind == "sample" {
		sampleScopes.add(e.Type, *start, *end)
		return true, "", nil
	}
	var generation int64
	err = tx.QueryRow(ctx, `INSERT INTO dirty_scopes(dataset_id,scope_key,generation,dirty_from,dirty_through) VALUES($1,$2,1,$3,$4)
		ON CONFLICT(dataset_id,scope_key) DO UPDATE SET generation=dirty_scopes.generation+1,
		dirty_from=LEAST(dirty_scopes.dirty_from,EXCLUDED.dirty_from),dirty_through=GREATEST(dirty_scopes.dirty_through,EXCLUDED.dirty_through)
		RETURNING generation`, dataset, scope, start, end).Scan(&generation)
	if err != nil {
		return false, "", err
	}
	job := syncing.NewID()
	_, err = tx.Exec(ctx, `INSERT INTO jobs(id,dataset_id,kind,scope_key,target_generation,payload) VALUES($1,$2,$3,$4,$5,$6)`, job, dataset, "analysis."+e.Kind, scope, generation, fmt.Sprintf(`{"inputRevision":%d}`, rev))
	return true, job, err
}

// lockActiveDataset is the first business lock for worker/training mutations.
func lockActiveDataset(ctx context.Context, tx pgx.Tx, dataset string) (int64, error) {
	var rev int64
	var retired bool
	err := tx.QueryRow(ctx, `SELECT commit_rev,retired_at IS NOT NULL FROM datasets WHERE id=$1 FOR UPDATE`, dataset).Scan(&rev, &retired)
	if err == nil && retired {
		return 0, syncing.Fail(409, "dataset_retired")
	}
	return rev, err
}
