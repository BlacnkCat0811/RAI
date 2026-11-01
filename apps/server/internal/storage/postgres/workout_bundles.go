package postgres

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/syncing"
)

// ImportWorkoutBundle is the one-request Workout write path. Validation has
// already happened at the HTTP boundary; this transaction rechecks identity
// against committed facts, writes the complete normalized projection, and
// publishes exactly one Workout analysis generation.
func (s *Store) ImportWorkoutBundle(ctx context.Context, p syncing.Principal, bundle syncing.WorkoutBundle, raw []byte) (json.RawMessage, error) {
	defer syncing.PhaseTimer(ctx, "import_total")()
	if err := bundle.Validate(); err != nil {
		return nil, err
	}
	if len(raw) == 0 || len(raw) > syncing.MaxWorkoutBundleBytes {
		return nil, syncing.Fail(413, "bundle_too_large")
	}
	digestText := syncing.BundleDigest(raw)
	digest, _ := hex.DecodeString(digestText)
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	transactionDone := syncing.PhaseTimer(ctx, "import_transaction")
	defer transactionDone()
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL transaction_timeout='45s'`); err != nil {
		return nil, err
	}
	rev, err := lockDataset(ctx, tx, p, bundle.DatasetID, bundle.DatasetEpoch, bundle.CollectorEpoch)
	if err != nil {
		return nil, err
	}
	var priorReceipt []byte
	err = tx.QueryRow(ctx, `SELECT receipt FROM workout_imports
		WHERE dataset_id=$1 AND workout_uuid=$2 AND bundle_sha256=$3`,
		p.DatasetID, bundle.WorkoutID, digest).Scan(&priorReceipt)
	priorFound := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var workoutTombstone bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM entity_tombstones WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3)`,
		p.DatasetID, syncing.WorkoutType, bundle.WorkoutID).Scan(&workoutTombstone); err != nil {
		return nil, err
	}
	if workoutTombstone {
		if bundle.DeletionOnly {
			return importDeletedParentChildren(ctx, tx, p, bundle, raw, rev)
		}
		return nil, syncing.Fail(409, "workout_deleted")
	}
	if bundle.DeletionOnly {
		return nil, syncing.Fail(409, "deletion_only_requires_workout_barrier")
	}

	canonicalWorkout, workoutHash := bundle.Workout.Canonical()
	var oldWorkoutHash, oldWorkoutPayload []byte
	err = tx.QueryRow(ctx, `SELECT content_sha256,payload FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2`,
		p.DatasetID, bundle.WorkoutID).Scan(&oldWorkoutHash, &oldWorkoutPayload)
	workoutExists := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if workoutExists && !bytes.Equal(oldWorkoutHash, workoutHash) {
		var prior syncing.Event
		if bundle.Observation == nil || json.Unmarshal(oldWorkoutPayload, &prior) != nil || !syncing.WorkoutCoreEqual(prior, bundle.Workout) {
			return nil, syncing.Fail(409, "workout_identity_conflict")
		}
	}

	type sampleRow struct {
		event      syncing.Event
		canonical  []byte
		hash       []byte
		assocFound bool
	}
	rows := make([]sampleRow, 0, len(bundle.Samples))
	for _, sample := range bundle.Samples {
		canonical, hash := sample.Canonical()
		rows = append(rows, sampleRow{event: sample, canonical: canonical, hash: hash})
	}
	checks := &pgx.Batch{}
	for _, row := range rows {
		var value *float64
		var unit *string
		if row.event.Quantity != nil {
			value = &row.event.Quantity.Value
			unit = &row.event.Quantity.Unit
		}
		checks.Queue(`SELECT EXISTS(SELECT 1 FROM entity_tombstones WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3),
            (SELECT content_sha256 FROM health_samples WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3),
            COALESCE((SELECT workout_uuid::text FROM workout_samples WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3),''),
            COALESCE((SELECT event_start=$4 AND event_end=$5 AND unit IS NOT DISTINCT FROM $6::text
                AND float8send(value_numeric) IS NOT DISTINCT FROM float8send($7::float8)
                AND value_category IS NOT DISTINCT FROM $8::integer AND source_bundle_id=$9
                FROM health_samples WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3),false)`,
			p.DatasetID, row.event.Type, row.event.UUID, time.UnixMicro(*row.event.StartUs).UTC(), time.UnixMicro(*row.event.EndUs).UTC(), unit, value, row.event.Category, row.event.Source.BundleID)
	}
	results := tx.SendBatch(ctx, checks)
	newSamples := make([]sampleRow, 0)
	missingAssociations := make([]sampleRow, 0)
	for index := range rows {
		var tombstone bool
		var oldHash []byte
		var associatedWorkout string
		var sameCore bool
		if err = results.QueryRow().Scan(&tombstone, &oldHash, &associatedWorkout, &sameCore); err != nil {
			_ = results.Close()
			return nil, err
		}
		if tombstone {
			_ = results.Close()
			return nil, syncing.Fail(409, "sample_deleted")
		}
		// New source metadata is an append-only observation. The original
		// sample hash/metadata row is never rewritten, even for older clients.
		if len(oldHash) > 0 && !bytes.Equal(oldHash, rows[index].hash) && (bundle.Observation == nil || !sameCore) {
			_ = results.Close()
			return nil, syncing.Fail(409, "sample_identity_conflict")
		}
		if associatedWorkout != "" && associatedWorkout != bundle.WorkoutID {
			_ = results.Close()
			return nil, syncing.Fail(409, "sample_association_conflict")
		}
		if len(oldHash) == 0 {
			newSamples = append(newSamples, rows[index])
		}
		if associatedWorkout == "" {
			missingAssociations = append(missingAssociations, rows[index])
		}
	}
	if err = results.Close(); err != nil {
		return nil, err
	}

	seriesChanged := false
	for _, series := range bundle.Series {
		changed, err := bundleSeriesChanged(ctx, tx, p.DatasetID, series)
		if err != nil {
			return nil, err
		}
		if changed {
			seriesChanged = true
		}
	}

	routeChanged := false
	for _, route := range bundle.Routes {
		changed, err := bundleRouteChanged(ctx, tx, p.DatasetID, bundle.WorkoutID, route)
		if err != nil {
			return nil, err
		}
		if changed {
			routeChanged = true
		}
	}

	deletionsChanged, err := checkBundleDeletions(ctx, tx, p.DatasetID, bundle)
	if err != nil {
		return nil, err
	}
	observationChanged, err := bundleObservationChanged(ctx, tx, p.DatasetID, bundle)
	if err != nil {
		return nil, err
	}

	// An exact retry may return its original receipt only after the sample and
	// route tombstone checks above. This prevents a deleted child from being
	// reported as committed merely because the old bundle bytes match.
	if priorFound {
		return json.RawMessage(priorReceipt), nil
	}

	// A successful read is not ordered HealthKit deletion evidence. In particular,
	// completeSampleTypes/completeRoutes on a late snapshot cannot remove facts
	// committed by a newer read. Only explicit deletion paths create barriers.
	changed := !workoutExists || len(newSamples) > 0 || len(missingAssociations) > 0 || seriesChanged || routeChanged || deletionsChanged || observationChanged
	nextRev := rev
	if changed {
		nextRev = rev + 1
	}
	samplesDeleted, associationsDeleted, routesDeleted, err := applyBundleDeletions(ctx, tx, p.DatasetID, bundle, nextRev)
	if err != nil {
		return nil, err
	}
	if !workoutExists {
		if err = insertWorkout(ctx, tx, p.DatasetID, bundle.Workout, nextRev, canonicalWorkout, workoutHash); err != nil {
			return nil, err
		}
	}

	samplesDone := syncing.PhaseTimer(ctx, "samples_and_associations")
	if len(newSamples) > 0 {
		writes := &pgx.Batch{}
		for _, row := range newSamples {
			e := row.event
			var value *float64
			var unit *string
			if e.Quantity != nil {
				value, unit = &e.Quantity.Value, &e.Quantity.Unit
			}
			metadata := e.Metadata
			if metadata == nil {
				metadata = map[string]any{}
			}
			meta, err := json.Marshal(metadata)
			if err != nil {
				return nil, err
			}
			writes.Queue(`INSERT INTO health_samples(dataset_id,sample_type,sample_uuid,event_start,event_end,value_numeric,value_category,unit,source_bundle_id,content_sha256,entity_revision,metadata)
				VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, p.DatasetID, e.Type, e.UUID,
				time.UnixMicro(*e.StartUs).UTC(), time.UnixMicro(*e.EndUs).UTC(), value, e.Category, unit,
				e.Source.BundleID, row.hash, nextRev, meta)
		}
		results = tx.SendBatch(ctx, writes)
		for range newSamples {
			if _, err = results.Exec(); err != nil {
				_ = results.Close()
				return nil, err
			}
		}
		if err = results.Close(); err != nil {
			return nil, err
		}
	}

	if len(missingAssociations) > 0 {
		writes := &pgx.Batch{}
		for _, row := range missingAssociations {
			writes.Queue(`INSERT INTO workout_samples(dataset_id,sample_type,sample_uuid,workout_uuid,association_id,source_bundle_id,commit_rev)
				VALUES($1,$2,$3,$4,$5,$6,$7)`, p.DatasetID, row.event.Type, row.event.UUID, bundle.WorkoutID,
				syncing.NewID(), row.event.Source.BundleID, nextRev)
		}
		results = tx.SendBatch(ctx, writes)
		for range missingAssociations {
			if _, err = results.Exec(); err != nil {
				_ = results.Close()
				return nil, err
			}
		}
		if err = results.Close(); err != nil {
			return nil, err
		}
	}

	samplesDone()
	seriesInserted := 0
	for _, series := range bundle.Series {
		inserted, err := insertBundleSeries(ctx, tx, p.DatasetID, bundle, series, nextRev)
		if err != nil {
			return nil, err
		}
		if inserted {
			seriesInserted++
		}
	}

	routesDone := syncing.PhaseTimer(ctx, "route_projection")
	routesInserted := 0
	for _, route := range bundle.Routes {
		inserted, err := insertBundleRoute(ctx, tx, p.DatasetID, bundle, route, nextRev)
		if err != nil {
			return nil, err
		}
		if inserted {
			routesInserted++
		}
	}

	routesDone()
	dirtyDone := syncing.PhaseTimer(ctx, "dirty_job")
	receipt := syncing.WorkoutImportReceipt{
		WorkoutID:      bundle.WorkoutID,
		BundleSHA256:   digestText,
		IngestStatus:   "committed",
		CommitRevision: nextRev,
		Counts: syncing.WorkoutImportCounts{
			WorkoutInserted: !workoutExists,
			SamplesDeleted:  samplesDeleted, AssociationsDeleted: associationsDeleted, RoutesDeleted: routesDeleted,
			SamplesInserted:       len(newSamples),
			SamplesUnchanged:      len(rows) - len(newSamples),
			AssociationsInserted:  len(missingAssociations),
			AssociationsUnchanged: len(rows) - len(missingAssociations),
			SeriesInserted:        seriesInserted,
			SeriesUnchanged:       len(bundle.Series) - seriesInserted,
			RoutesInserted:        routesInserted,
			RoutesUnchanged:       len(bundle.Routes) - routesInserted,
		},
		RouteDiagnostics:     bundle.RouteTimeDiagnostics(),
		SourceClassification: "source_unknown", SourceObservation: bundle.ObservationSummary(),
		AnalysisStatus: "not_required",
		AnalysisJobIDs: []string{},
	}
	if bundle.Observation != nil {
		receipt.SourceClassification = bundle.Observation.Provenance.Workout.Classification(nil)
	}
	if changed {
		jobID := syncing.NewID()
		scope := "workout:" + syncing.WorkoutType + ":" + bundle.WorkoutID
		var generation int64
		if err = tx.QueryRow(ctx, `INSERT INTO dirty_scopes(dataset_id,scope_key,generation,dirty_from,dirty_through)
			VALUES($1,$2,1,$3,$4)
			ON CONFLICT(dataset_id,scope_key) DO UPDATE SET generation=dirty_scopes.generation+1,
				dirty_from=LEAST(dirty_scopes.dirty_from,EXCLUDED.dirty_from),
				dirty_through=GREATEST(dirty_scopes.dirty_through,EXCLUDED.dirty_through)
			RETURNING generation`, p.DatasetID, scope,
			time.UnixMicro(*bundle.Workout.StartUs).UTC(), time.UnixMicro(*bundle.Workout.EndUs).UTC()).Scan(&generation); err != nil {
			return nil, err
		}
		jobPayload, _ := json.Marshal(map[string]any{"inputRevision": nextRev, "bundleSHA256": digestText,
			"sampleCount": len(rows), "routeCount": len(bundle.Routes)})
		if _, err = tx.Exec(ctx, `INSERT INTO jobs(id,dataset_id,kind,scope_key,target_generation,payload)
			VALUES($1,$2,'analysis.workout',$3,$4,$5)`, jobID, p.DatasetID, scope, generation, jobPayload); err != nil {
			return nil, err
		}
		receipt.AnalysisStatus = "queued"
		receipt.AnalysisJobIDs = []string{jobID}
		if _, err = tx.Exec(ctx, `UPDATE datasets SET commit_rev=$2 WHERE id=$1`, p.DatasetID, nextRev); err != nil {
			return nil, err
		}
		changePayload, _ := json.Marshal(map[string]any{"workoutID": bundle.WorkoutID, "sampleCount": len(rows),
			"routeCount": len(bundle.Routes), "samplesDeleted": samplesDeleted, "routesDeleted": routesDeleted,
			"sampleDeletions": bundle.SampleDeletions, "routeDeletions": bundle.RouteDeletions,
			"bundleSHA256": digestText})
		if _, err = tx.Exec(ctx, `INSERT INTO change_events(dataset_id,commit_rev,ordinal,event_id,entity_kind,entity_key,operation,payload)
			VALUES($1,$2,0,$3,'workout_bundle',$4,'upsert',$5)`, p.DatasetID, nextRev, syncing.NewID(),
			"workout:"+bundle.WorkoutID, changePayload); err != nil {
			return nil, err
		}
	}
	dirtyDone()
	persistDone := syncing.PhaseTimer(ctx, "snapshot_receipt_commit")
	body, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO workout_imports(dataset_id,workout_uuid,bundle_sha256,payload,receipt)
		VALUES($1,$2,$3,$4::jsonb,$5)
		ON CONFLICT(dataset_id,workout_uuid,bundle_sha256) DO NOTHING`,
		p.DatasetID, bundle.WorkoutID, digest, string(raw), body); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	persistDone()
	return json.RawMessage(body), nil
}

func (s *Store) WorkoutImport(ctx context.Context, p syncing.Principal, workoutID, bundleSHA256 string) (json.RawMessage, error) {
	if !syncing.ValidID(workoutID) || (bundleSHA256 != "" && !syncing.ValidDigest(bundleSHA256)) {
		return nil, syncing.Fail(422, "unsupported_payload")
	}
	query := `SELECT receipt,payload FROM workout_imports WHERE dataset_id=$1 AND workout_uuid=$2`
	args := []any{p.DatasetID, workoutID}
	if bundleSHA256 != "" {
		query += ` AND bundle_sha256=decode($3,'hex')`
		args = append(args, bundleSHA256)
	}
	query += ` ORDER BY imported_at DESC, bundle_sha256 DESC LIMIT 1`
	var receipt, payload []byte
	err := s.Pool.QueryRow(ctx, query, args...).Scan(&receipt, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		if bundleSHA256 != "" {
			var body []byte
			e := s.Pool.QueryRow(ctx, `SELECT decode(payload->>'receiptBytes','base64') FROM change_events WHERE dataset_id=$1 AND event_id=$2 AND entity_kind='workout_child_delete' AND payload->>'bundleSHA256'=$3`, p.DatasetID, childDeleteEventID(p.DatasetID, workoutID, bundleSHA256), bundleSHA256).Scan(&body)
			if e == nil {
				return json.RawMessage(body), nil
			}
			if !errors.Is(e, pgx.ErrNoRows) {
				return nil, e
			}
		}
		return nil, syncing.Fail(404, "workout_import_not_found")
	}
	if err != nil {
		return nil, err
	}
	var sampleDeleted, routeDeleted bool
	if err = s.Pool.QueryRow(ctx, `SELECT
		EXISTS (
			SELECT 1 FROM jsonb_array_elements(COALESCE(($1::jsonb)->'samples','[]'::jsonb)) s
			JOIN entity_tombstones t ON t.dataset_id=$2
				AND t.sample_type=s->>'type' AND t.sample_uuid=(s->>'uuid')::uuid
		),
		EXISTS (
			SELECT 1 FROM jsonb_array_elements(COALESCE(($1::jsonb)->'routes','[]'::jsonb)) r
			JOIN entity_tombstones t ON t.dataset_id=$2
				AND t.sample_type=$3 AND t.sample_uuid=(r->>'routeID')::uuid
		)`, string(payload), p.DatasetID, syncing.RouteType).Scan(&sampleDeleted, &routeDeleted); err != nil {
		return nil, err
	}
	if sampleDeleted {
		return nil, syncing.Fail(409, "sample_deleted")
	}
	if routeDeleted {
		return nil, syncing.Fail(409, "route_deleted")
	}
	return json.RawMessage(receipt), nil
}

// DeleteWorkoutBundle is the explicit v3 workout deletion. It is deliberately
// separate from bundle import: absence of a Workout from a read is not
// evidence of deletion (for example after permission revocation).
func (s *Store) DeleteWorkoutBundle(ctx context.Context, p syncing.Principal, req syncing.WorkoutDeleteRequest) (json.RawMessage, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rev, err := lockDataset(ctx, tx, p, req.DatasetID, req.DatasetEpoch, req.CollectorEpoch)
	if err != nil {
		return nil, err
	}
	// Fencing precedes receipt replay. The permanent identity is dataset + UUID,
	// independent of the importing collector or any later bundle digest.
	var deletedRev int64
	var priorReceipt []byte
	err = tx.QueryRow(ctx, `SELECT t.deleted_rev,c.payload->'receipt'
		FROM entity_tombstones t LEFT JOIN change_events c
		ON c.dataset_id=t.dataset_id AND c.commit_rev=t.deleted_rev AND c.ordinal=0
		AND c.entity_kind='workout' AND c.entity_key=$4 AND c.operation='delete'
		WHERE t.dataset_id=$1 AND t.sample_type=$2 AND t.sample_uuid=$3`,
		p.DatasetID, syncing.WorkoutType, req.WorkoutID, "workout:"+req.WorkoutID).Scan(&deletedRev, &priorReceipt)
	if err == nil {
		// Older explicit deletion paths may have no v3 receipt. Their immutable
		// tombstone revision still supplies a stable acknowledgement.
		receipt := syncing.WorkoutDeleteReceipt{WorkoutID: req.WorkoutID, IngestStatus: "deleted",
			AlreadyDeleted: true, CommitRevision: deletedRev}
		if len(priorReceipt) > 0 {
			if err = json.Unmarshal(priorReceipt, &receipt); err != nil {
				return nil, err
			}
		}
		body, err := json.Marshal(receipt)
		if err != nil {
			return nil, err
		}
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		return json.RawMessage(body), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var oldStart, oldEnd *time.Time
	err = tx.QueryRow(ctx, `SELECT event_start,event_end FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2`,
		p.DatasetID, req.WorkoutID).Scan(&oldStart, &oldEnd)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	nextRev := rev + 1
	reason := req.Reason
	if reason == "" {
		reason = "healthkit_deleted_object"
	}
	if _, err = tx.Exec(ctx, `INSERT INTO entity_tombstones(dataset_id,sample_type,sample_uuid,deleted_rev,previous_start,previous_end,reason)
		VALUES($1,$2,$3,$4,$5,$6,$7)`,
		p.DatasetID, syncing.WorkoutType, req.WorkoutID, nextRev, oldStart, oldEnd, reason); err != nil {
		return nil, err
	}
	routes, err := tx.Query(ctx, `SELECT route_uuid::text FROM workout_routes WHERE dataset_id=$1 AND workout_uuid=$2`,
		p.DatasetID, req.WorkoutID)
	if err != nil {
		return nil, err
	}
	var routeIDs []string
	for routes.Next() {
		var id string
		if err = routes.Scan(&id); err != nil {
			routes.Close()
			return nil, err
		}
		routeIDs = append(routeIDs, id)
	}
	if err = routes.Err(); err != nil {
		routes.Close()
		return nil, err
	}
	routes.Close()
	for _, routeID := range routeIDs {
		if _, err = tx.Exec(ctx, `SELECT route_existed FROM rai_route_delete($1,$2,$3,$4)`,
			p.DatasetID, routeID, nextRev, reason); err != nil {
			return nil, routeDBError(err)
		}
	}
	if _, err = tx.Exec(ctx, `DELETE FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2`, p.DatasetID, req.WorkoutID); err != nil {
		return nil, err
	}
	// A known deletion invalidates the old time range in this fact transaction.
	// The existing P4 dependency trigger dirties Training State immediately;
	// the Worker later removes only the current Workout publication pointer.
	// Unknown deletes have no time range and create no fabricated analysis work.
	if oldStart != nil {
		if _, err = tx.Exec(ctx, `SELECT rai_analysis_dirty($1,$2,'analysis.workout',$3,$4)`,
			p.DatasetID, "workout:"+syncing.WorkoutType+":"+req.WorkoutID, oldStart, oldEnd); err != nil {
			return nil, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE datasets SET commit_rev=$2 WHERE id=$1`, p.DatasetID, nextRev); err != nil {
		return nil, err
	}
	receipt := syncing.WorkoutDeleteReceipt{WorkoutID: req.WorkoutID, IngestStatus: "deleted", CommitRevision: nextRev}
	// change_events supplies deletion time (created_at), original fencing epochs
	// and the durable receipt, linked by the tombstone's dataset + deleted_rev.
	changePayload, err := json.Marshal(map[string]any{"workoutID": req.WorkoutID, "reason": reason,
		"datasetEpoch": req.DatasetEpoch, "collectorEpoch": req.CollectorEpoch, "receipt": receipt})
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO change_events(dataset_id,commit_rev,ordinal,event_id,entity_kind,entity_key,operation,payload)
		VALUES($1,$2,0,$3,'workout',$4,'delete',$5)`, p.DatasetID, nextRev, syncing.NewID(),
		"workout:"+req.WorkoutID, changePayload); err != nil {
		return nil, err
	}
	body, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}

func bundleSeriesChanged(ctx context.Context, tx pgx.Tx, dataset string, series syncing.WorkoutBundleSeries) (bool, error) {
	var state string
	var totalPoints int64
	var chunkCount int
	err := tx.QueryRow(ctx, `SELECT state,total_points,COALESCE(chunk_count,0) FROM series_uploads
		WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3`, dataset, series.SampleType, series.SampleUUID).
		Scan(&state, &totalPoints, &chunkCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if state != "sealed" || totalPoints != int64(len(series.Points)) || chunkCount != bundleSeriesChunkCount(series.Points) {
		return false, syncing.Fail(409, "series_identity_conflict")
	}
	var seriesID string
	if err = tx.QueryRow(ctx, `SELECT series_id::text FROM series_uploads WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3`,
		dataset, series.SampleType, series.SampleUUID).Scan(&seriesID); err != nil {
		return false, err
	}
	rows, err := tx.Query(ctx, `SELECT points_sha256 FROM series_chunks WHERE dataset_id=$1 AND series_id=$2 ORDER BY chunk_index`, dataset, seriesID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	var hashes [][]byte
	for rows.Next() {
		var hash []byte
		if err = rows.Scan(&hash); err != nil {
			return false, err
		}
		hashes = append(hashes, hash)
	}
	if err = rows.Err(); err != nil {
		return false, err
	}
	chunks := bundleSeriesChunks(series.Points)
	if len(hashes) != len(chunks) {
		return false, syncing.Fail(409, "series_identity_conflict")
	}
	for index, chunk := range chunks {
		_, digest := syncing.SeriesPointsCanonical(chunk)
		if !bytes.Equal(hashes[index], digest) {
			return false, syncing.Fail(409, "series_identity_conflict")
		}
	}
	return false, nil
}

func bundleSeriesChunkCount(points []syncing.SeriesPoint) int {
	return (len(points) + 4999) / 5000
}

func bundleSeriesChunks(points []syncing.SeriesPoint) [][]syncing.SeriesPoint {
	chunks := make([][]syncing.SeriesPoint, 0, bundleSeriesChunkCount(points))
	for start := 0; start < len(points); start += 5000 {
		end := start + 5000
		if end > len(points) {
			end = len(points)
		}
		chunks = append(chunks, points[start:end])
	}
	return chunks
}

func insertBundleSeries(ctx context.Context, tx pgx.Tx, dataset string, bundle syncing.WorkoutBundle, series syncing.WorkoutBundleSeries, rev int64) (bool, error) {
	changed, err := bundleSeriesChanged(ctx, tx, dataset, series)
	if err != nil || !changed {
		return false, err
	}
	var parentStartTime, parentEndTime time.Time
	if err = tx.QueryRow(ctx, `SELECT event_start,event_end
		FROM health_samples WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3`,
		dataset, series.SampleType, series.SampleUUID).Scan(&parentStartTime, &parentEndTime); err != nil {
		return false, err
	}
	parentStart, parentEnd := parentStartTime.UnixMicro(), parentEndTime.UnixMicro()
	chunks := bundleSeriesChunks(series.Points)
	manifest := syncing.EmptySeriesManifest()
	for _, chunk := range chunks {
		_, digest := syncing.SeriesPointsCanonical(chunk)
		manifest = syncing.AdvanceSeriesManifest(manifest, digest)
	}
	seriesID := syncing.NewID()
	body, err := json.Marshal(syncing.SeriesReceipt{SeriesID: seriesID, IngestStatus: "sealed", SampleType: series.SampleType,
		SampleUUID: series.SampleUUID, ChunkCount: len(chunks), TotalPoints: int64(len(series.Points)),
		ManifestSHA256: hex.EncodeToString(manifest), CommitRevision: rev, AnalysisStatus: "queued", AnalysisJobIDs: []string{}})
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO series_uploads(
		dataset_id,series_id,sample_type,sample_uuid,parent_start_us,parent_end_us,dataset_epoch,collector_epoch,
		state,sealed_chunks,total_points,manifest_sha256,chunk_count,receipt,sealed_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,'sealed',$9,$10,$11,$9,$12,now())`,
		dataset, seriesID, series.SampleType, series.SampleUUID, parentStart, parentEnd, bundle.DatasetEpoch,
		bundle.CollectorEpoch, len(chunks), len(series.Points), manifest, body); err != nil {
		if uniqueViolation(err) {
			return false, syncing.Fail(409, "series_identity_conflict")
		}
		return false, err
	}
	for index, chunk := range chunks {
		canonical, digest := syncing.SeriesPointsCanonical(chunk)
		minStart, maxEnd := chunk[0].StartUs, chunk[0].EndUs
		for _, point := range chunk[1:] {
			if point.StartUs < minStart {
				minStart = point.StartUs
			}
			if point.EndUs > maxEnd {
				maxEnd = point.EndUs
			}
		}
		chunkReceipt, _ := json.Marshal(syncing.SeriesChunkReceipt{SeriesID: seriesID, ChunkIndex: index,
			IngestStatus: "sealed", PointCount: len(chunk), PayloadSHA256: hex.EncodeToString(digest),
			ChunkSHA256: hex.EncodeToString(digest), ManifestSHA256: hex.EncodeToString(manifest), CommittedChunks: index + 1})
		if _, err = tx.Exec(ctx, `INSERT INTO series_chunks(
			dataset_id,series_id,chunk_index,point_count,min_start_us,max_end_us,wire_sha256,points_sha256,points,receipt)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::jsonb,$10)`, dataset, seriesID, index, len(chunk),
			minStart, maxEnd, digest, digest, string(canonical), chunkReceipt); err != nil {
			return false, err
		}
	}
	return true, nil
}

func bundleRouteChanged(ctx context.Context, tx pgx.Tx, dataset, workoutID string, route syncing.WorkoutBundleRoute) (bool, error) {
	var tombstone bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM entity_tombstones WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3)`,
		dataset, syncing.RouteType, route.RouteID).Scan(&tombstone); err != nil {
		return false, err
	}
	if tombstone {
		return false, syncing.Fail(409, "route_deleted")
	}
	var existingWorkout, source, state string
	var totalPoints int64
	err := tx.QueryRow(ctx, `SELECT workout_uuid::text,source_bundle_id,state,COALESCE(total_points,0)
		FROM workout_routes WHERE dataset_id=$1 AND route_uuid=$2`, dataset, route.RouteID).
		Scan(&existingWorkout, &source, &state, &totalPoints)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if existingWorkout != workoutID || source != route.Source.BundleID || state != "sealed" || totalPoints != int64(len(route.Points)) {
		return false, syncing.Fail(409, "route_identity_conflict")
	}
	rows, err := tx.Query(ctx, `SELECT content_sha256 FROM route_chunks WHERE dataset_id=$1 AND route_uuid=$2 ORDER BY chunk_index`, dataset, route.RouteID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	var hashes [][]byte
	for rows.Next() {
		var h []byte
		if err = rows.Scan(&h); err != nil {
			return false, err
		}
		hashes = append(hashes, h)
	}
	if err = rows.Err(); err != nil {
		return false, err
	}
	chunks := bundleRouteChunks(route)
	if len(hashes) != len(chunks) {
		return false, syncing.Fail(409, "route_identity_conflict")
	}
	for index, chunk := range chunks {
		_, digest := syncing.RoutePointsCanonical(chunk)
		if !bytes.Equal(hashes[index], digest) {
			return false, syncing.Fail(409, "route_identity_conflict")
		}
	}
	return false, nil
}

func bundleRouteChunks(route syncing.WorkoutBundleRoute) [][]syncing.RoutePoint {
	chunks := make([][]syncing.RoutePoint, 0, (len(route.Points)+4999)/5000)
	for start := 0; start < len(route.Points); start += 5000 {
		end := start + 5000
		if end > len(route.Points) {
			end = len(route.Points)
		}
		chunk := make([]syncing.RoutePoint, 0, end-start)
		for index, point := range route.Points[start:end] {
			chunk = append(chunk, point.ToRoutePoint(int64(start+index)))
		}
		chunks = append(chunks, chunk)
	}
	return chunks
}

func insertBundleRoute(ctx context.Context, tx pgx.Tx, dataset string, bundle syncing.WorkoutBundle, route syncing.WorkoutBundleRoute, rev int64) (bool, error) {
	changed, err := bundleRouteChanged(ctx, tx, dataset, bundle.WorkoutID, route)
	if err != nil || !changed {
		return false, err
	}
	chunks := bundleRouteChunks(route)
	if len(chunks) == 0 {
		return false, syncing.Fail(422, "unsupported_payload")
	}
	// The manifest is the true time envelope, not array endpoints. Sequence
	// preserves source order even across timestamp regressions and chunks.
	first, last := route.Points[0].RecordedAtUs, route.Points[0].RecordedAtUs
	for _, point := range route.Points {
		first = min(first, point.RecordedAtUs)
		last = max(last, point.RecordedAtUs)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO workout_routes(
		dataset_id,route_uuid,workout_uuid,source_bundle_id,dataset_epoch,collector_epoch,
		expected_chunk_count,expected_point_count,first_recorded_at,last_recorded_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, dataset, route.RouteID, bundle.WorkoutID, route.Source.BundleID,
		bundle.DatasetEpoch, bundle.CollectorEpoch, len(chunks), len(route.Points),
		time.UnixMicro(first).UTC(), time.UnixMicro(last).UTC()); err != nil {
		if uniqueViolation(err) {
			return false, syncing.Fail(409, "route_identity_conflict")
		}
		return false, err
	}
	for index, chunk := range chunks {
		canonical, digest := syncing.RoutePointsCanonical(chunk)
		var ignored []byte
		if err = tx.QueryRow(ctx, `SELECT rai_route_append_chunk($1,$2,$3,$4,$5,$6,$7::jsonb)`,
			dataset, route.RouteID, index, len(chunk), digest, digest, string(canonical)).Scan(&ignored); err != nil {
			return false, routeDBError(err)
		}
	}
	var manifest, payloadManifest []byte
	if err = tx.QueryRow(ctx, `SELECT manifest_sha256,payload_manifest_sha256 FROM workout_routes WHERE dataset_id=$1 AND route_uuid=$2`,
		dataset, route.RouteID).Scan(&manifest, &payloadManifest); err != nil {
		return false, err
	}
	receipt := syncing.RouteReceipt{RouteID: route.RouteID, WorkoutID: bundle.WorkoutID, IngestStatus: "sealed",
		ChunkCount: len(chunks), TotalPoints: int64(len(route.Points)), ManifestSHA256: hex.EncodeToString(manifest),
		PayloadManifestSHA256: hex.EncodeToString(payloadManifest), CommitRevision: rev, AnalysisStatus: "queued"}
	body, err := json.Marshal(receipt)
	if err != nil {
		return false, err
	}
	var applied bool
	var sealed []byte
	if err = tx.QueryRow(ctx, `SELECT applied,receipt FROM rai_route_seal($1,$2,$3,$4,$5,$6,$7,$8)`,
		dataset, route.RouteID, len(chunks), len(route.Points), manifest, payloadManifest, body, rev).Scan(&applied, &sealed); err != nil {
		return false, routeDBError(err)
	}
	if !applied {
		return false, syncing.Fail(409, "route_identity_conflict")
	}
	return true, nil
}
