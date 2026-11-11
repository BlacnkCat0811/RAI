package postgres

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"rai/server/internal/syncing"

	"github.com/jackc/pgx/v5"
)

// sampleRow carries one bundle sample with its canonical bytes and content
// digest, computed once and reused by the identity and write phases.
type sampleRow struct {
	event     syncing.Event
	canonical []byte
	hash      []byte
}

// tolerantRewrites reports whether metadata-only differences may describe the
// same fact. v3 carries a separate observation; v4 carries inline metadata and
// no observation, but the same append-only reading applies.
func tolerantRewrites(bundle syncing.WorkoutBundle) bool {
	return bundle.Observation != nil || bundle.SourceTable != nil
}

// importWorkoutBundleHook runs inside the fact transaction after every fact,
// dirty scope, job and legacy receipt has been written and before COMMIT. A
// nonzero error rolls the whole import back. The worker uses it to mark the
// accepted ingest row committed under its claim token.
type importWorkoutBundleHook func(ctx context.Context, tx pgx.Tx, receipt json.RawMessage, revision int64) error

func (s *Store) importWorkoutBundle(ctx context.Context, p syncing.Principal, bundle syncing.WorkoutBundle, raw []byte, after importWorkoutBundleHook) (json.RawMessage, error) {
	defer syncing.PhaseTimer(ctx, "import_total")()
	// Deletion-only bundles commit child deletions through their own nested
	// transaction, which cannot join an ingest commit. The v6 worker never
	// produces one.
	if after != nil && bundle.DeletionOnly {
		return nil, syncing.Fail(422, "unsupported_payload")
	}
	validationStart := time.Now()
	validationDone := syncing.PhaseTimer(ctx, syncing.PhaseSemanticValidation)
	err := syncing.ValidateWorkoutBundle(bundle)
	validationDone()
	if err != nil {
		return nil, err
	}
	if budget, declared := syncing.PhaseBudget(ctx, syncing.PhaseSemanticValidation); declared && time.Since(validationStart) > budget {
		return nil, syncing.Fail(503, "validation_timeout")
	}
	if len(raw) == 0 || len(raw) > syncing.MaxWorkoutBundleBytes {
		return nil, syncing.Fail(413, "bundle_too_large")
	}
	digestText := syncing.BundleDigest(raw)
	digest, _ := hex.DecodeString(digestText)
	importJSON, err := workoutImportJSON(bundle, raw)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	transactionDone := syncing.PhaseTimer(ctx, "import_transaction")
	defer transactionDone()
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// Three transaction-scoped settings, none of which changes a global default:
	//
	// statement_timeout: the connection default is 2s, which bounds one small
	// request statement. A single batch statement here carries up to a thousand
	// facts, so the bound is raised inside this transaction only and kept below
	// the 45s transaction budget.
	//
	// transaction_timeout: the outer bound for the whole import.
	//
	// plan_cache_mode: a pooled connection reuses one prepared statement, and
	// PostgreSQL switches to a generic plan after a few executions. For the
	// set-based identity query the generic plan cannot see the cardinality of
	// unnest($2::text[],$3::text[]) and plans a per-key nested loop: 3.4s per
	// thousand keys against a populated dataset, versus 31ms for a custom plan
	// (measured). Every import on a warm pool would pay that on replay, so the
	// transaction asks for custom plans explicitly. Planning costs ~1.5ms per
	// statement here.
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout='30s'; SET LOCAL transaction_timeout='45s'; SET LOCAL plan_cache_mode='force_custom_plan'`); err != nil {
		return nil, err
	}
	// Lock wait and fencing are their own phase so a slow commit path can be
	// attributed to lock contention rather than to fact writes.
	lockDone := syncing.PhaseTimer(ctx, "dataset_lock")
	rev, err := lockDataset(ctx, tx, p, bundle.DatasetID, bundle.DatasetEpoch, bundle.CollectorEpoch)
	if err != nil {
		return nil, err
	}
	lockDone()
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
		tolerant := tolerantRewrites(bundle)
		if !tolerant || json.Unmarshal(oldWorkoutPayload, &prior) != nil || !syncing.WorkoutCoreEqual(prior, bundle.Workout) {
			return nil, syncing.Fail(409, "workout_identity_conflict")
		}
	}

	rows := make([]sampleRow, 0, len(bundle.Samples))
	for _, sample := range bundle.Samples {
		canonical, hash := sample.Canonical()
		rows = append(rows, sampleRow{event: sample, canonical: canonical, hash: hash})
	}
	// Every identity fact for the whole bundle is read by bounded set query: one
	// statement per batch of keys instead of one statement per sample. The
	// per-key decisions below keep the original ordering and codes.
	identityDone := syncing.PhaseTimer(ctx, "identity_checks")
	stored, err := loadSampleIdentities(ctx, tx, p.DatasetID, rows)
	if err != nil {
		return nil, err
	}
	newSamples := make([]sampleRow, 0)
	missingAssociations := make([]sampleRow, 0)
	tolerant := tolerantRewrites(bundle)
	for index := range rows {
		state := stored[sampleKey{sampleType: rows[index].event.Type, sampleUUID: rows[index].event.UUID}]
		if state.tombstone {
			return nil, syncing.Fail(409, "sample_deleted")
		}
		// New source metadata is an append-only observation. The original
		// sample hash/metadata row is never rewritten, even for older clients.
		if state.exists && !bytes.Equal(state.hash, rows[index].hash) && (!tolerant || !sameSampleCore(state, rows[index].event)) {
			return nil, syncing.Fail(409, "sample_identity_conflict")
		}
		if state.workout != "" && state.workout != bundle.WorkoutID {
			return nil, syncing.Fail(409, "sample_association_conflict")
		}
		if !state.exists {
			newSamples = append(newSamples, rows[index])
		}
		if state.workout == "" {
			missingAssociations = append(missingAssociations, rows[index])
		}
	}

	seriesWrite, err := seriesIdentity(ctx, tx, p.DatasetID, bundle.Series)
	if err != nil {
		return nil, err
	}
	seriesChanged := anyTrue(seriesWrite)
	routeWrite, err := routeIdentity(ctx, tx, p.DatasetID, bundle.WorkoutID, bundle.Routes)
	if err != nil {
		return nil, err
	}
	routeChanged := anyTrue(routeWrite)
	if err = validateBundleDeletionEvidence(ctx, tx, p.DatasetID, bundle); err != nil {
		return nil, err
	}
	deletionsChanged, err := checkBundleDeletions(ctx, tx, p.DatasetID, bundle)
	if err != nil {
		return nil, err
	}
	observationChanged, err := bundleObservationChanged(ctx, tx, p.DatasetID, bundle)
	if err != nil {
		return nil, err
	}
	identityDone()

	// An exact retry may return its original receipt only after the sample and
	// route tombstone checks above. This prevents a deleted child from being
	// reported as committed merely because the old bundle bytes match.
	if priorFound {
		body := json.RawMessage(priorReceipt)
		if after != nil {
			var prior syncing.WorkoutImportReceipt
			revision := rev
			if json.Unmarshal(priorReceipt, &prior) == nil {
				revision = prior.CommitRevision
			}
			if err = after(ctx, tx, body, revision); err != nil {
				return nil, err
			}
			if err = tx.Commit(ctx); err != nil {
				return nil, err
			}
		}
		return body, nil
	}

	// A successful read is not ordered HealthKit deletion evidence. In particular,
	// completeSampleTypes/completeRoutes on a late snapshot cannot remove facts
	// committed by a newer read. Only explicit deletion paths create barriers.
	changed := !workoutExists || len(newSamples) > 0 || len(missingAssociations) > 0 || seriesChanged || routeChanged || deletionsChanged || observationChanged
	nextRev := rev
	if changed {
		nextRev = rev + 1
	}
	deletionsDone := syncing.PhaseTimer(ctx, "deletions")
	samplesDeleted, associationsDeleted, routesDeleted, err := applyBundleDeletions(ctx, tx, p.DatasetID, bundle, nextRev)
	if err != nil {
		return nil, err
	}
	if !workoutExists {
		if err = insertWorkout(ctx, tx, p.DatasetID, bundle.Workout, nextRev, canonicalWorkout, workoutHash); err != nil {
			return nil, err
		}
	}
	deletionsDone()

	samplesDone := syncing.PhaseTimer(ctx, "sample_writes")
	if len(newSamples) > 0 {
		if err = insertSampleRows(ctx, tx, p.DatasetID, newSamples, nextRev); err != nil {
			return nil, err
		}
	}
	samplesDone()

	associationsDone := syncing.PhaseTimer(ctx, "association_writes")
	if len(missingAssociations) > 0 {
		if err = insertAssociationRows(ctx, tx, p.DatasetID, bundle.WorkoutID, missingAssociations, nextRev); err != nil {
			return nil, err
		}
	}
	associationsDone()

	seriesDone := syncing.PhaseTimer(ctx, "series")
	seriesInserted := 0
	for index, series := range bundle.Series {
		if !seriesWrite[index] {
			continue
		}
		if err = insertBundleSeries(ctx, tx, p.DatasetID, bundle, series, nextRev); err != nil {
			return nil, err
		}
		seriesInserted++
	}
	seriesDone()

	routesDone := syncing.PhaseTimer(ctx, "routes")
	routesInserted := 0
	for index, route := range bundle.Routes {
		if !routeWrite[index] {
			continue
		}
		if err = insertBundleRoute(ctx, tx, p.DatasetID, bundle, route, nextRev); err != nil {
			return nil, err
		}
		routesInserted++
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
	} else if bundle.SourceTable != nil {
		receipt.SourceClassification = bundle.SourceTable.Workout.Classification(nil)
	}
	if changed {
		scope := "workout:" + syncing.WorkoutType + ":" + bundle.WorkoutID
		if _, err = tx.Exec(ctx, `INSERT INTO dirty_scopes(dataset_id,scope_key,generation,dirty_from,dirty_through)
			VALUES($1,$2,1,$3,$4)
			ON CONFLICT(dataset_id,scope_key) DO UPDATE SET generation=dirty_scopes.generation+1,
				dirty_from=LEAST(dirty_scopes.dirty_from,EXCLUDED.dirty_from),
				dirty_through=GREATEST(dirty_scopes.dirty_through,EXCLUDED.dirty_through)`, p.DatasetID, scope,
			time.UnixMicro(*bundle.Workout.StartUs).UTC(), time.UnixMicro(*bundle.Workout.EndUs).UTC()); err != nil {
			return nil, err
		}
		// The bumped fact generation invalidates the v2 model stack (its dirty
		// trigger) and the per-Workout cache; no legacy analysis job is queued.
		receipt.AnalysisStatus = "queued"
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
	receiptDone := syncing.PhaseTimer(ctx, "receipt")
	body, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO workout_imports(dataset_id,workout_uuid,bundle_sha256,payload,receipt)
		VALUES($1,$2,$3,$4::jsonb,$5)
		ON CONFLICT(dataset_id,workout_uuid,bundle_sha256) DO NOTHING`,
		p.DatasetID, bundle.WorkoutID, digest, string(importJSON), body); err != nil {
		return nil, err
	}
	receiptDone()
	if after != nil {
		if err = after(ctx, tx, json.RawMessage(body), nextRev); err != nil {
			return nil, err
		}
	}
	commitDone := syncing.PhaseTimer(ctx, "commit")
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	commitDone()
	return json.RawMessage(body), nil
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

// insertBundleSeries writes one series whose identity was already resolved by
// seriesIdentity in the same transaction. The identity pass is not repeated
// here: that would restore one query set per series.
func insertBundleSeries(ctx context.Context, tx pgx.Tx, dataset string, bundle syncing.WorkoutBundle, series syncing.WorkoutBundleSeries, rev int64) error {
	var parentStartTime, parentEndTime time.Time
	if err := tx.QueryRow(ctx, `SELECT event_start,event_end
		FROM health_samples WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3`,
		dataset, series.SampleType, series.SampleUUID).Scan(&parentStartTime, &parentEndTime); err != nil {
		return err
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
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO series_uploads(
		dataset_id,series_id,sample_type,sample_uuid,parent_start_us,parent_end_us,dataset_epoch,collector_epoch,
		state,sealed_chunks,total_points,manifest_sha256,chunk_count,receipt,sealed_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,'sealed',$9,$10,$11,$9,$12,now())`,
		dataset, seriesID, series.SampleType, series.SampleUUID, parentStart, parentEnd, bundle.DatasetEpoch,
		bundle.CollectorEpoch, len(chunks), len(series.Points), manifest, body); err != nil {
		if uniqueViolation(err) {
			return syncing.Fail(409, "series_identity_conflict")
		}
		return err
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
			return err
		}
	}
	return nil
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

// insertBundleRoute writes one route whose identity was already resolved by
// routeIdentity in the same transaction; the check is not repeated per route.
func insertBundleRoute(ctx context.Context, tx pgx.Tx, dataset string, bundle syncing.WorkoutBundle, route syncing.WorkoutBundleRoute, rev int64) error {
	chunks := bundleRouteChunks(route)
	if len(chunks) == 0 {
		return syncing.Fail(422, "unsupported_payload")
	}
	var err error
	// The manifest is the true time envelope, not array endpoints. Sequence
	// preserves source order even across timestamp regressions and chunks.
	first, last := route.Points[0].RecordedAtUs, route.Points[0].RecordedAtUs
	for _, point := range route.Points {
		first = min(first, point.RecordedAtUs)
		last = max(last, point.RecordedAtUs)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO workout_routes(
		dataset_id,route_uuid,workout_uuid,source_bundle_id,dataset_epoch,collector_epoch,
		expected_chunk_count,expected_point_count,first_recorded_at,last_recorded_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, dataset, route.RouteID, bundle.WorkoutID, route.Source.BundleID,
		bundle.DatasetEpoch, bundle.CollectorEpoch, len(chunks), len(route.Points),
		time.UnixMicro(first).UTC(), time.UnixMicro(last).UTC()); err != nil {
		if uniqueViolation(err) {
			return syncing.Fail(409, "route_identity_conflict")
		}
		return err
	}
	for index, chunk := range chunks {
		canonical, digest := syncing.RoutePointsCanonical(chunk)
		var ignored []byte
		if err = tx.QueryRow(ctx, `SELECT rai_route_append_chunk($1,$2,$3,$4,$5,$6,$7::jsonb)`,
			dataset, route.RouteID, index, len(chunk), digest, digest, string(canonical)).Scan(&ignored); err != nil {
			return routeDBError(err)
		}
	}
	var manifest, payloadManifest []byte
	if err = tx.QueryRow(ctx, `SELECT manifest_sha256,payload_manifest_sha256 FROM workout_routes WHERE dataset_id=$1 AND route_uuid=$2`,
		dataset, route.RouteID).Scan(&manifest, &payloadManifest); err != nil {
		return err
	}
	receipt := syncing.RouteReceipt{RouteID: route.RouteID, WorkoutID: bundle.WorkoutID, IngestStatus: "sealed",
		ChunkCount: len(chunks), TotalPoints: int64(len(route.Points)), ManifestSHA256: hex.EncodeToString(manifest),
		PayloadManifestSHA256: hex.EncodeToString(payloadManifest), CommitRevision: rev, AnalysisStatus: "queued"}
	body, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	var applied bool
	var sealed []byte
	if err = tx.QueryRow(ctx, `SELECT applied,receipt FROM rai_route_seal($1,$2,$3,$4,$5,$6,$7,$8)`,
		dataset, route.RouteID, len(chunks), len(route.Points), manifest, payloadManifest, body, rev).Scan(&applied, &sealed); err != nil {
		return routeDBError(err)
	}
	if !applied {
		return syncing.Fail(409, "route_identity_conflict")
	}
	return nil
}
