package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"rai/server/internal/analysis"
	"rai/server/internal/syncing"
	"sort"
)

// Validate explicit parent membership before changing any child. Unknown IDs
// can establish barriers, but a known child of another Workout never can.
func checkBundleDeletions(ctx context.Context, tx pgx.Tx, dataset string, b syncing.WorkoutBundle) (bool, error) {
	changed := false
	for _, d := range b.SampleDeletions {
		var parent string
		var tombstone bool
		err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT workout_uuid::text FROM workout_samples WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3),''),EXISTS(SELECT 1 FROM entity_tombstones WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3)`, dataset, d.SampleType, d.SampleUUID).Scan(&parent, &tombstone)
		if err != nil {
			return false, err
		}
		if parent != "" && parent != b.WorkoutID {
			return false, syncing.Fail(409, "child_workout_mismatch")
		}
		changed = changed || !tombstone
	}
	for _, id := range b.RouteDeletions {
		var parent string
		var tombstone bool
		err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT workout_uuid::text FROM workout_routes WHERE dataset_id=$1 AND route_uuid=$3),''),EXISTS(SELECT 1 FROM entity_tombstones WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3)`, dataset, syncing.RouteType, id).Scan(&parent, &tombstone)
		if err != nil {
			return false, err
		}
		if parent != "" && parent != b.WorkoutID {
			return false, syncing.Fail(409, "child_workout_mismatch")
		}
		changed = changed || !tombstone
	}
	return changed, nil
}
func applyBundleDeletions(ctx context.Context, tx pgx.Tx, dataset string, b syncing.WorkoutBundle, rev int64) (samples, associations, routes int, err error) {
	for _, d := range b.SampleDeletions {
		var start, end any
		// Preserve the actual old range; unknown deletions have no invented range.
		err = tx.QueryRow(ctx, `SELECT event_start,event_end FROM health_samples WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3`, dataset, d.SampleType, d.SampleUUID).Scan(&start, &end)
		if errors.Is(err, pgx.ErrNoRows) {
			start = nil
			end = nil
		} else if err != nil {
			return
		}
		_, err = tx.Exec(ctx, `INSERT INTO entity_tombstones(dataset_id,sample_type,sample_uuid,deleted_rev,previous_start,previous_end,reason) VALUES($1,$2,$3,$4,$5,$6,'healthkit_deleted_object') ON CONFLICT DO NOTHING`, dataset, d.SampleType, d.SampleUUID, rev, start, end)
		if err != nil {
			return
		}
		var n int
		err = tx.QueryRow(ctx, `SELECT count(*) FROM workout_samples WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3`, dataset, d.SampleType, d.SampleUUID).Scan(&n)
		if err != nil {
			return
		}
		associations += n
		tag, e := tx.Exec(ctx, `DELETE FROM health_samples WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3`, dataset, d.SampleType, d.SampleUUID)
		if e != nil {
			err = e
			return
		}
		samples += int(tag.RowsAffected())
	}
	for _, id := range b.RouteDeletions {
		var existed bool
		err = tx.QueryRow(ctx, `SELECT route_existed FROM rai_route_delete($1,$2,$3,'healthkit_deleted_object')`, dataset, id, rev).Scan(&existed)
		if err != nil {
			err = routeDBError(err)
			return
		}
		if existed {
			routes++
		}
	}
	return
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
	for _, sample := range b.Samples {
		if len(sample.Metadata) > 0 {
			samples = append(samples, sampleMetadata{sample.Type, sample.UUID, sample.Metadata})
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
	rows, err = tx.Query(ctx, `SELECT payload->'observation'->'provenance' FROM workout_imports
        WHERE dataset_id=$1 AND workout_uuid=$2 AND payload ? 'observation'
        ORDER BY (payload->'observation'->>'observedAtUs')::bigint DESC,bundle_sha256 DESC`, dataset, workout)
	if err != nil {
		return err
	}
	defer rows.Close()
	eligible := func(source syncing.HealthKitSource, parent *syncing.HealthKitSource) bool {
		value := source.Classification(parent)
		return value == "apple_watch" || value == "associated_apple_watch"
	}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return err
		}
		var provenance syncing.BundleProvenance
		if json.Unmarshal(raw, &provenance) != nil {
			return analysis.Invalid()
		}
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
