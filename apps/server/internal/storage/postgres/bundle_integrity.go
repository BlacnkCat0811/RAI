package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"rai/server/internal/analysis"
	"rai/server/internal/syncing"
	"sort"
	"strings"
)

// validateBundleDeletionEvidence makes explicit child deletions conditional on
// a complete v4 read coverage record and a non-stale observation. A successful
// but empty HealthKit query is not deletion authority.
func validateBundleDeletionEvidence(ctx context.Context, tx pgx.Tx, dataset string, b syncing.WorkoutBundle) error {
	if b.ProtocolVersion != 4 || len(b.SampleDeletions) == 0 && len(b.RouteDeletions) == 0 {
		return nil
	}
	if b.Observation == nil {
		return syncing.Fail(422, "deletion_coverage_incomplete")
	}
	complete := func(key string) bool {
		cov, ok := b.Observation.Coverage[key]
		return ok && cov.State == "complete" && cov.QueryCompleted && cov.LocalPersisted && cov.SampleCount > 0
	}
	for _, deletion := range b.SampleDeletions {
		if !complete(deletion.SampleType) {
			return syncing.Fail(422, "deletion_coverage_incomplete")
		}
	}
	if len(b.RouteDeletions) > 0 && !complete("route") {
		return syncing.Fail(422, "deletion_coverage_incomplete")
	}
	var prior int64
	err := tx.QueryRow(ctx, `SELECT (payload->'observation'->>'observedAtUs')::bigint
		FROM workout_imports WHERE dataset_id=$1 AND workout_uuid=$2 AND payload ? 'observation'
		ORDER BY (payload->'observation'->>'observedAtUs')::bigint DESC,bundle_sha256 DESC LIMIT 1`,
		dataset, b.WorkoutID).Scan(&prior)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if b.Observation.ObservedAtUs < prior {
		return syncing.Fail(409, "stale_observation")
	}
	return nil
}

// Validate explicit parent membership before changing any child. Unknown IDs
// can establish barriers, but a known child of another Workout never can.
//
// The check is set-based: one statement resolves every requested child key, so
// the cost no longer grows by one round trip per deleted sample. Decisions are
// still taken in bundle order, so the reported conflict code is the same one
// the scalar loop produced.
func checkBundleDeletions(ctx context.Context, tx pgx.Tx, dataset string, b syncing.WorkoutBundle) (bool, error) {
	if len(b.SampleDeletions) == 0 && len(b.RouteDeletions) == 0 {
		return false, nil
	}
	changed := false
	if len(b.SampleDeletions) > 0 {
		types := make([]string, len(b.SampleDeletions))
		uuids := make([]string, len(b.SampleDeletions))
		for index, deletion := range b.SampleDeletions {
			types[index], uuids[index] = deletion.SampleType, deletion.SampleUUID
		}
		parents, tombstones, err := childDeleteState(ctx, tx, dataset, `SELECT k.sample_type,k.sample_uuid::text,
				COALESCE(w.workout_uuid::text,''),
				EXISTS(SELECT 1 FROM entity_tombstones t
					WHERE t.dataset_id=$1 AND t.sample_type=k.sample_type AND t.sample_uuid=k.sample_uuid::uuid)
			FROM unnest($2::text[],$3::text[]) AS k(sample_type,sample_uuid)
			LEFT JOIN workout_samples w
				ON w.dataset_id=$1 AND w.sample_type=k.sample_type AND w.sample_uuid=k.sample_uuid::uuid`,
			types, uuids)
		if err != nil {
			return false, err
		}
		for _, deletion := range b.SampleDeletions {
			key := sampleKey{sampleType: deletion.SampleType, sampleUUID: deletion.SampleUUID}
			if parent := parents[key]; parent != "" && parent != b.WorkoutID {
				return false, syncing.Fail(409, "child_workout_mismatch")
			}
			changed = changed || !tombstones[key]
		}
	}
	if len(b.RouteDeletions) > 0 {
		parents, tombstones, err := childDeleteState(ctx, tx, dataset, `SELECT $3,k.route_uuid,
				COALESCE(r.workout_uuid::text,''),
				EXISTS(SELECT 1 FROM entity_tombstones t
					WHERE t.dataset_id=$1 AND t.sample_type=$3 AND t.sample_uuid=k.route_uuid::uuid)
			FROM unnest($2::text[]) AS k(route_uuid)
			LEFT JOIN workout_routes r ON r.dataset_id=$1 AND r.route_uuid=k.route_uuid::uuid`,
			b.RouteDeletions, syncing.RouteType)
		if err != nil {
			return false, err
		}
		for _, id := range b.RouteDeletions {
			if parent := parents[sampleKey{sampleType: syncing.RouteType, sampleUUID: id}]; parent != "" && parent != b.WorkoutID {
				return false, syncing.Fail(409, "child_workout_mismatch")
			}
			changed = changed || !tombstones[sampleKey{sampleType: syncing.RouteType, sampleUUID: id}]
		}
	}
	return changed, nil
}

// childDeleteState runs one parent/tombstone statement and indexes the result
// by child key. The key is always (sample_type,sample_uuid).
func childDeleteState(ctx context.Context, tx pgx.Tx, dataset, query string, args ...any) (map[sampleKey]string, map[sampleKey]bool, error) {
	rows, err := tx.Query(ctx, query, append([]any{dataset}, args...)...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	parents := make(map[sampleKey]string)
	tombstones := make(map[sampleKey]bool)
	for rows.Next() {
		var key sampleKey
		var parent string
		var tombstone bool
		if err = rows.Scan(&key.sampleType, &key.sampleUUID, &parent, &tombstone); err != nil {
			return nil, nil, err
		}
		parents[key] = parent
		tombstones[key] = tombstone
	}
	if err = rows.Err(); err != nil {
		return nil, nil, err
	}
	return parents, tombstones, nil
}

// applyBundleDeletions records barriers and removes child facts with one
// statement per bounded batch. Repeated keys are collapsed: the scalar loop's
// second pass over the same key counted nothing and deleted nothing, so the
// totals stay identical while the statements stop growing with the bundle.
func applyBundleDeletions(ctx context.Context, tx pgx.Tx, dataset string, b syncing.WorkoutBundle, rev int64) (samples, associations, routes int, err error) {
	deletions := uniqueSampleDeletions(b.SampleDeletions)
	for _, batch := range batchIndexes(len(deletions), func(i int) int {
		return len(deletions[i].SampleType) + len(deletions[i].SampleUUID) + 32
	}) {
		types := make([]string, len(batch))
		uuids := make([]string, len(batch))
		for position, index := range batch {
			types[position], uuids[position] = deletions[index].SampleType, deletions[index].SampleUUID
		}
		// Preserve the actual old range; unknown deletions have no invented range.
		if _, err = tx.Exec(ctx, `INSERT INTO entity_tombstones(dataset_id,sample_type,sample_uuid,deleted_rev,previous_start,previous_end,reason)
			SELECT $1,k.sample_type,k.sample_uuid::uuid,$4,h.event_start,h.event_end,'healthkit_deleted_object'
			FROM unnest($2::text[],$3::text[]) AS k(sample_type,sample_uuid)
			LEFT JOIN health_samples h
				ON h.dataset_id=$1 AND h.sample_type=k.sample_type AND h.sample_uuid=k.sample_uuid::uuid
			ON CONFLICT DO NOTHING`, dataset, types, uuids, rev); err != nil {
			return 0, 0, 0, err
		}
		var n int
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM workout_samples w
			JOIN unnest($2::text[],$3::text[]) AS k(sample_type,sample_uuid)
				ON w.sample_type=k.sample_type AND w.sample_uuid=k.sample_uuid::uuid
			WHERE w.dataset_id=$1`, dataset, types, uuids).Scan(&n); err != nil {
			return 0, 0, 0, err
		}
		associations += n
		var tag pgconn.CommandTag
		tag, err = tx.Exec(ctx, `DELETE FROM health_samples h
			USING unnest($2::text[],$3::text[]) AS k(sample_type,sample_uuid)
			WHERE h.dataset_id=$1 AND h.sample_type=k.sample_type AND h.sample_uuid=k.sample_uuid::uuid`,
			dataset, types, uuids)
		if err != nil {
			return 0, 0, 0, err
		}
		samples += int(tag.RowsAffected())
	}
	for _, id := range b.RouteDeletions {
		var existed bool
		err = tx.QueryRow(ctx, `SELECT route_existed FROM rai_route_delete($1,$2,$3,'healthkit_deleted_object')`, dataset, id, rev).Scan(&existed)
		if err != nil {
			err = routeDBError(err)
			return 0, 0, 0, err
		}
		if existed {
			routes++
		}
	}
	return samples, associations, routes, nil
}

// uniqueSampleDeletions keeps the first occurrence of each child key.
func uniqueSampleDeletions(deletions []syncing.SampleDeletion) []syncing.SampleDeletion {
	seen := make(map[sampleKey]bool, len(deletions))
	unique := make([]syncing.SampleDeletion, 0, len(deletions))
	for _, deletion := range deletions {
		key := sampleKey{sampleType: deletion.SampleType, sampleUUID: deletion.SampleUUID}
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, deletion)
	}
	return unique
}

// Only RAI observation time is ignored. Child metadata is included so late
// Sample metadata updates invalidate the snapshot without rewriting raw rows.
func observationBytes(b syncing.WorkoutBundle) []byte {
	if b.Observation == nil {
		return nil
	}
	observation := *b.Observation
	observation.ObservedAtUs = 0
	type sampleMetadata struct {
		Type     string         `json:"type"`
		UUID     string         `json:"uuid"`
		Metadata map[string]any `json:"metadata"`
	}
	samples := []sampleMetadata{}
	// v4 has no per-sample objects in the stored payload; its stream metadata
	// is already part of the immutable bundle bytes. Reconstructing v3 sample
	// metadata here makes every retry look changed, so compare only the v4
	// observation (which is where v4's coverage/provenance lives).
	if b.ProtocolVersion != 4 {
		for _, sample := range b.Samples {
			if len(sample.Metadata) > 0 {
				samples = append(samples, sampleMetadata{sample.Type, sample.UUID, sample.Metadata})
			}
		}
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].Type+":"+samples[i].UUID < samples[j].Type+":"+samples[j].UUID })
	raw, _ := json.Marshal(struct {
		Observation syncing.BundleObservation `json:"observation"`
		Samples     []sampleMetadata          `json:"sampleMetadata"`
	}{observation, samples})
	return raw
}
func bundleObservationChanged(ctx context.Context, tx pgx.Tx, dataset string, b syncing.WorkoutBundle) (bool, error) {
	if b.Observation == nil {
		return false, nil
	}
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT payload FROM workout_imports WHERE dataset_id=$1 AND workout_uuid=$2 AND payload ? 'observation' ORDER BY (payload->'observation'->>'observedAtUs')::bigint DESC,bundle_sha256 DESC LIMIT 1`, dataset, b.WorkoutID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	var prior syncing.WorkoutBundle
	if err = json.Unmarshal(raw, &prior); err != nil {
		return false, err
	}
	if prior.Observation == nil {
		return false, fmt.Errorf("missing persisted observation")
	}
	if b.Observation.ObservedAtUs < prior.Observation.ObservedAtUs {
		return false, nil
	}
	return string(observationBytes(prior)) != string(observationBytes(b)), nil
}

// A later eligible Workout cannot confer provenance on retained legacy
// children. Reuse positive evidence from immutable observation history, but
// never infer a child's source/device from its time or from the Workout alone.
func validateAnalysisProvenance(ctx context.Context, tx pgx.Tx, dataset, workout string) error {
	needed := map[string]string{}
	rows, err := tx.Query(ctx, `SELECT 'sample:'||s.sample_type||':'||s.sample_uuid::text,s.source_bundle_id
        FROM health_samples s JOIN workout_samples ws USING(dataset_id,sample_type,sample_uuid)
        WHERE ws.dataset_id=$1 AND ws.workout_uuid=$2
        UNION ALL SELECT 'route:'||route_uuid::text,source_bundle_id FROM workout_routes
        WHERE dataset_id=$1 AND workout_uuid=$2 AND state='sealed'`, dataset, workout)
	if err != nil {
		return err
	}
	for rows.Next() {
		var key, source string
		if err = rows.Scan(&key, &source); err != nil {
			rows.Close()
			return err
		}
		needed[key] = source
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(needed) == 0 {
		return nil
	}
	// Both protocols are immutable history: v3 carries an observation, v4 keeps
	// the source inline next to every fact. Either can establish provenance.
	rows, err = tx.Query(ctx, `SELECT payload FROM workout_imports
        WHERE dataset_id=$1 AND workout_uuid=$2
        ORDER BY imported_at DESC,bundle_sha256 DESC`, dataset, workout)
	if err != nil {
		return err
	}
	defer rows.Close()
	eligible := func(source syncing.HealthKitSource, parent *syncing.HealthKitSource) bool {
		value := source.Classification(parent)
		return value == "apple_watch" || value == "associated_apple_watch"
	}
	for rows.Next() {
		var payloadRaw []byte
		if err = rows.Scan(&payloadRaw); err != nil {
			return err
		}
		var stored struct {
			ProtocolVersion int                        `json:"protocolVersion"`
			Observation     *syncing.BundleObservation `json:"observation"`
		}
		if json.Unmarshal(payloadRaw, &stored) != nil {
			return analysis.Invalid()
		}
		if stored.ProtocolVersion >= 4 {
			table, err := syncing.WorkoutBundlePayloadSourceTable(payloadRaw)
			if err != nil {
				return analysis.Invalid()
			}
			if table.Workout.Classification(nil) != "apple_watch" {
				continue
			}
			for key, expected := range needed {
				var source syncing.HealthKitSource
				var ok bool
				switch {
				case strings.HasPrefix(key, "sample:"):
					sampleType, uuid, found := strings.Cut(strings.TrimPrefix(key, "sample:"), ":")
					if found {
						source, ok = table.SampleSource(sampleType, uuid)
					}
				case strings.HasPrefix(key, "route:"):
					source, ok = table.RouteSource(strings.TrimPrefix(key, "route:"))
				}
				if ok && expected == source.BundleID && eligible(source, &table.Workout) {
					delete(needed, key)
				}
			}
		} else if stored.Observation != nil {
			provenance := stored.Observation.Provenance
			if provenance.Workout.Classification(nil) != "apple_watch" {
				continue
			}
			for _, sample := range provenance.Samples {
				key := "sample:" + sample.SampleType + ":" + sample.SampleUUID
				if source, ok := needed[key]; ok && source == sample.Source.BundleID && eligible(sample.Source, &provenance.Workout) {
					delete(needed, key)
				}
			}
			for _, route := range provenance.Routes {
				key := "route:" + route.RouteID
				if source, ok := needed[key]; ok && source == route.Source.BundleID && eligible(route.Source, &provenance.Workout) {
					delete(needed, key)
				}
			}
		}
		if len(needed) == 0 {
			return nil
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return &analysis.Failure{Code: "analysis_source_unclassified"}
}

// The existing UNIQUE event_id index supplies receipt identity after the parent
// FK cascade removed workout_imports. No deleted Workout is recreated.
func childDeleteEventID(dataset, workout, digest string) string {
	sum := sha256.Sum256([]byte("rai.child-delete/v1:" + dataset + ":" + workout + ":" + digest))
	b := sum[:16]
	b[6] = (b[6] & 15) | 80
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func importDeletedParentChildren(ctx context.Context, tx pgx.Tx, p syncing.Principal, b syncing.WorkoutBundle, raw []byte, rev int64) (json.RawMessage, error) {
	digest := syncing.BundleDigest(raw)
	eventID := childDeleteEventID(p.DatasetID, b.WorkoutID, digest)
	var prior []byte
	err := tx.QueryRow(ctx, `SELECT decode(payload->>'receiptBytes','base64') FROM change_events WHERE dataset_id=$1 AND event_id=$2 AND entity_kind='workout_child_delete' AND payload->>'bundleSHA256'=$3`, p.DatasetID, eventID, digest).Scan(&prior)
	if err == nil {
		return json.RawMessage(prior), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if _, err = checkBundleDeletions(ctx, tx, p.DatasetID, b); err != nil {
		return nil, err
	}
	next := rev + 1
	samples, associations, routes, err := applyBundleDeletions(ctx, tx, p.DatasetID, b, next)
	if err != nil {
		return nil, err
	}
	receipt := syncing.WorkoutImportReceipt{WorkoutID: b.WorkoutID, BundleSHA256: digest, IngestStatus: "committed", CommitRevision: next, Counts: syncing.WorkoutImportCounts{SamplesDeleted: samples, AssociationsDeleted: associations, RoutesDeleted: routes}, AnalysisStatus: "not_required", AnalysisJobIDs: []string{}, SourceClassification: "source_unknown"}
	body, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(map[string]any{"workoutID": b.WorkoutID, "sampleDeletions": b.SampleDeletions, "routeDeletions": b.RouteDeletions, "bundleSHA256": digest, "receipt": receipt, "receiptBytes": body})
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE datasets SET commit_rev=$2 WHERE id=$1`, p.DatasetID, next); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO change_events(dataset_id,commit_rev,ordinal,event_id,entity_kind,entity_key,operation,payload) VALUES($1,$2,0,$3,'workout_child_delete',$4,'delete',$5)`, p.DatasetID, next, eventID, "workout:"+b.WorkoutID, payload); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}
