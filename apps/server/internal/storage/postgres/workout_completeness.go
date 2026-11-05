package postgres

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"rai/server/internal/syncing"
	"sort"
	"time"
)

func workoutCompletenessFactRevision(ctx context.Context, tx pgx.Tx, dataset string) (int64, error) {
	var revision int64
	err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT commit_rev FROM change_events WHERE dataset_id=$1 AND entity_kind IN ('workout_bundle','workout','workout_child_delete') ORDER BY commit_rev DESC LIMIT 1),0)`, dataset).Scan(&revision)
	return revision, err
}
func sameCompletenessPointer(p *string, value string) bool { return p != nil && *p == value }

func (s *Store) CommitWorkoutCompleteness(ctx context.Context, p syncing.Principal, raw []byte, lease syncing.UploadLease) (json.RawMessage, error) {
	m, err := syncing.DecodeWorkoutCompleteness(raw)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout='15s';SET LOCAL transaction_timeout='45s'`); err != nil {
		return nil, err
	}
	rev, err := lockDataset(ctx, tx, p, m.DatasetID, m.DatasetEpoch, m.CollectorEpoch)
	if err != nil {
		return nil, err
	}
	if err = lockUploadLease(ctx, tx, p, lease); err != nil {
		return nil, err
	}
	hash := syncing.BundleDigest(raw)
	digest, _ := hex.DecodeString(hash)
	var oldHash string
	var priorReceipt []byte
	err = tx.QueryRow(ctx, `SELECT encode(request_sha256,'hex'),receipt FROM workout_sync_completeness WHERE dataset_id=$1 AND manifest_id=$2`, p.DatasetID, m.ManifestID).Scan(&oldHash, &priorReceipt)
	if err == nil {
		if oldHash != hash {
			return nil, syncing.Fail(409, "manifest_identity_conflict")
		}
		return uploadRetryReceipt(ctx, tx, p, lease, priorReceipt)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	var generation, from, cutoff int64
	var priorHash string
	var headsRaw, inventoryRaw []byte
	heads := map[string]syncing.CompletenessHead{}
	inventory := map[string]bool{}
	err = tx.QueryRow(ctx, `SELECT generation,window_from_us,known_cutoff_us,encode(request_sha256,'hex'),head_state,inventory FROM workout_sync_completeness WHERE dataset_id=$1 AND collector_epoch=$2 AND dataset_epoch=$3 ORDER BY generation DESC LIMIT 1`, p.DatasetID, m.CollectorEpoch, m.DatasetEpoch).Scan(&generation, &from, &cutoff, &priorHash, &headsRaw, &inventoryRaw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		if m.Generation != generation+1 || m.WindowFromUs != from || m.KnownCutoffUs <= cutoff || !sameCompletenessPointer(m.PreviousManifestSHA256, priorHash) {
			return nil, syncing.Fail(409, "stale_manifest")
		}
		var ids []string
		if json.Unmarshal(headsRaw, &heads) != nil || json.Unmarshal(inventoryRaw, &ids) != nil {
			return nil, errors.New("invalid persisted completeness")
		}
		for _, id := range ids {
			inventory[id] = true
		}
	} else if m.Generation != 1 || m.PreviousManifestSHA256 != nil {
		return nil, syncing.Fail(409, "manifest_generation_conflict")
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return nil, err
	}
	if m.CreatedAtUs > now.Add(5*time.Minute).UnixMicro() {
		return nil, syncing.Fail(422, "invalid_completeness_time")
	}
	types, ids := []string{}, []string{}
	refSeen := map[string]bool{}
	for _, stream := range m.Streams {
		for _, page := range stream.Pages {
			for _, id := range append(append([]string{}, page.Additions...), page.Deletions...) {
				key := stream.SampleType + ":" + id
				if !refSeen[key] {
					types = append(types, stream.SampleType)
					ids = append(ids, id)
					refSeen[key] = true
				}
			}
		}
	}
	for id := range inventory {
		key := syncing.WorkoutType + ":" + id
		if !refSeen[key] {
			types = append(types, syncing.WorkoutType)
			ids = append(ids, id)
		}
	}
	tombstones := map[string]bool{}
	if len(ids) > 0 {
		rows, e := tx.Query(ctx, `SELECT t.sample_type,t.sample_uuid::text FROM unnest($2::text[],$3::uuid[]) q(kind,id) JOIN entity_tombstones t ON t.dataset_id=$1 AND t.sample_type=q.kind AND t.sample_uuid=q.id`, p.DatasetID, types, ids)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var kind, id string
			if e = rows.Scan(&kind, &id); e != nil {
				rows.Close()
				return nil, e
			}
			tombstones[kind+":"+id] = true
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
	}
	streamMap := map[string]syncing.CompletenessStream{}
	newHeads := map[string]syncing.CompletenessHead{}
	for _, stream := range m.Streams {
		head, exists := heads[stream.Key]
		if exists && head.QueryFingerprint != stream.QueryFingerprint {
			return nil, syncing.Fail(409, "query_fingerprint_conflict")
		}
		for _, page := range stream.Pages {
			if page.Sequence != head.Sequence+1 {
				return nil, syncing.Fail(409, "completeness_sequence_gap")
			}
			if head.Sequence == 0 {
				if page.PreviousPageSHA256 != nil || page.PreviousAnchorSHA256 != nil {
					return nil, syncing.Fail(409, "completeness_anchor_conflict")
				}
			} else if !sameCompletenessPointer(page.PreviousPageSHA256, head.PageSHA256) || !sameCompletenessPointer(page.PreviousAnchorSHA256, head.AnchorSHA256) {
				return nil, syncing.Fail(409, "completeness_anchor_conflict")
			}
			for _, id := range page.Deletions {
				if !tombstones[stream.SampleType+":"+id] {
					return nil, syncing.Fail(409, "completeness_fact_not_durable")
				}
			}
			if stream.SampleType == syncing.WorkoutType {
				for _, id := range page.Additions {
					inventory[id] = true
				}
				for _, id := range page.Deletions {
					delete(inventory, id)
				}
			}
			head = syncing.CompletenessHead{Sequence: page.Sequence, PageSHA256: syncing.CompletenessPageDigest(page), AnchorSHA256: page.CandidateAnchorSHA256, QueryFingerprint: stream.QueryFingerprint}
		}
		newHeads[stream.Key] = head
		streamMap[stream.Key] = stream
	}
	for id := range inventory {
		if tombstones[syncing.WorkoutType+":"+id] {
			delete(inventory, id)
		}
	}
	wanted := []string{}
	for id := range inventory {
		wanted = append(wanted, id)
	}
	sort.Strings(wanted)
	proofIDs := []string{}
	for _, w := range m.Workouts {
		proofIDs = append(proofIDs, w.WorkoutID)
	}
	if !syncing.EqualStringSet(wanted, proofIDs) {
		return nil, syncing.Fail(409, "completeness_inventory_conflict")
	}
	rows, err := tx.Query(ctx, `SELECT workout_uuid::text FROM workouts WHERE dataset_id=$1 AND event_end>=$2 AND payload->'workout'->>'activityType'='37' ORDER BY event_end,workout_uuid LIMIT 257`, p.DatasetID, time.UnixMicro(m.WindowFromUs))
	if err != nil {
		return nil, err
	}
	stored := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		stored = append(stored, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if !syncing.EqualStringSet(wanted, stored) {
		return nil, syncing.Fail(409, "completeness_known_workout_missing")
	}
	for _, proof := range m.Workouts {
		if err = verifyCompletenessWorkout(ctx, tx, p, m, proof, streamMap, tombstones); err != nil {
			return nil, err
		}
	}
	factRevision, err := workoutCompletenessFactRevision(ctx, tx, p.DatasetID)
	if err != nil {
		return nil, err
	}
	receipt := syncing.WorkoutCompletenessReceipt{Contract: syncing.WorkoutCompletenessContract, ProtocolVersion: 1, DatasetID: m.DatasetID, DatasetEpoch: m.DatasetEpoch, CollectorEpoch: m.CollectorEpoch, ManifestID: m.ManifestID, Generation: m.Generation, ManifestSHA256: hash, IngestStatus: "committed", CommitRevision: rev + 1, StreamCount: len(m.Streams), WorkoutCount: len(m.Workouts)}
	body, _ := json.Marshal(receipt)
	headBytes, _ := json.Marshal(newHeads)
	inventoryBytes, _ := json.Marshal(wanted)
	if _, err = tx.Exec(ctx, `INSERT INTO workout_sync_completeness(dataset_id,dataset_epoch,collector_epoch,generation,manifest_id,stream_set_version,window_from_us,known_cutoff_us,fact_revision,commit_rev,request_sha256,head_state,inventory,request_bytes,receipt) VALUES($1,$2,$3,$4,$5,1,$6,$7,$8,$9,$10,$11,$12,$13,$14)`, p.DatasetID, m.DatasetEpoch, m.CollectorEpoch, m.Generation, m.ManifestID, m.WindowFromUs, m.KnownCutoffUs, factRevision, rev+1, digest, headBytes, inventoryBytes, raw, body); err != nil {
		return nil, err
	}
	change, _ := json.Marshal(map[string]any{"manifestSHA256": hash, "generation": m.Generation, "streamCount": len(m.Streams), "workoutCount": len(m.Workouts)})
	if _, err = tx.Exec(ctx, `INSERT INTO change_events(dataset_id,commit_rev,ordinal,event_id,entity_kind,entity_key,operation,payload) VALUES($1,$2,0,$3,'workout_completeness',$4,'observed',$5)`, p.DatasetID, rev+1, syncing.NewID(), "workout-completeness:"+m.ManifestID, change); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE datasets SET commit_rev=$2 WHERE id=$1`, p.DatasetID, rev+1); err != nil {
		return nil, err
	}
	if err = checkUploadLease(ctx, tx, p, lease); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return body, nil
}

func completeCoverage(c syncing.BundleCoverage) bool {
	return c.QueryCompleted && c.LocalPersisted && (c.State == "complete" || c.State == "no_records_observed")
}
func verifyCompletenessWorkout(ctx context.Context, tx pgx.Tx, p syncing.Principal, m syncing.WorkoutSyncCompletenessManifest, proof syncing.CompletenessWorkout, streams map[string]syncing.CompletenessStream, tombstones map[string]bool) error {
	var raw, receiptRaw []byte
	err := tx.QueryRow(ctx, `SELECT payload,receipt FROM workout_imports WHERE dataset_id=$1 AND workout_uuid=$2 AND bundle_sha256=decode($3,'hex')`, p.DatasetID, proof.WorkoutID, proof.BundleSHA256).Scan(&raw, &receiptRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return syncing.Fail(409, "completeness_fact_not_durable")
	}
	if err != nil {
		return err
	}
	var b syncing.WorkoutBundle
	var receipt syncing.WorkoutImportReceipt
	if json.Unmarshal(raw, &b) != nil || json.Unmarshal(receiptRaw, &receipt) != nil {
		return errors.New("invalid persisted workout receipt")
	}
	if b.DatasetID != m.DatasetID || b.DatasetEpoch != m.DatasetEpoch || b.CollectorEpoch != m.CollectorEpoch || b.Observation == nil || receipt.IngestStatus != "committed" || receipt.BundleSHA256 != proof.BundleSHA256 || receipt.WorkoutID != proof.WorkoutID {
		return syncing.Fail(409, "completeness_receipt_scope_conflict")
	}
	var latest int64
	if err = tx.QueryRow(ctx, `SELECT COALESCE(max(commit_rev),0) FROM change_events WHERE dataset_id=$1 AND entity_key=$2 AND entity_kind IN ('workout_bundle','workout','workout_child_delete')`, p.DatasetID, "workout:"+proof.WorkoutID).Scan(&latest); err != nil {
		return err
	}
	if receipt.CommitRevision < latest {
		return syncing.Fail(409, "completeness_receipt_stale")
	}
	var deleted bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM jsonb_array_elements(COALESCE(($1::jsonb)->'samples','[]')) x JOIN entity_tombstones t ON t.dataset_id=$2 AND t.sample_type=x->>'type' AND t.sample_uuid=(x->>'uuid')::uuid) OR EXISTS(SELECT 1 FROM jsonb_array_elements(COALESCE(($1::jsonb)->'routes','[]')) x JOIN entity_tombstones t ON t.dataset_id=$2 AND t.sample_type=$3 AND t.sample_uuid=(x->>'routeID')::uuid)`, string(raw), p.DatasetID, syncing.RouteType).Scan(&deleted); err != nil {
		return err
	}
	if deleted {
		return syncing.Fail(409, "completeness_receipt_stale")
	}
	if !b.CompleteRoutes || !completeCoverage(b.Observation.Coverage["route"]) || b.Observation.Provenance.Workout.Classification(nil) != "apple_watch" {
		return syncing.Fail(409, "completeness_stream_pending")
	}
	routeState := "absent"
	if len(b.Routes) > 0 {
		routeState = "present"
	}
	if proof.RouteState != routeState {
		return syncing.Fail(409, "completeness_route_state_conflict")
	}
	members := map[string]bool{}
	for _, sample := range b.Samples {
		members[sample.Type+":"+sample.UUID] = true
	}
	for _, route := range b.Routes {
		members[syncing.RouteType+":"+route.RouteID] = true
	}
	for _, kind := range append(append([]string{}, syncing.WorkoutCompletenessQuantities...), syncing.RouteType) {
		if kind != syncing.RouteType && (!completeCoverage(b.Observation.Coverage[kind]) || !completeCoverage(b.Observation.Coverage["series:"+kind])) {
			return syncing.Fail(409, "completeness_stream_pending")
		}
		for _, page := range streams[syncing.CompletenessKey(kind, proof.WorkoutID)].Pages {
			for _, id := range page.Additions {
				if !members[kind+":"+id] && !tombstones[kind+":"+id] {
					return syncing.Fail(409, "completeness_fact_not_durable")
				}
			}
		}
	}
	for _, series := range proof.Series {
		hash, count := syncing.CompletenessSeriesDigest(b.Series, series.SampleType)
		if hash != series.SHA256 || count != series.PointCount {
			return syncing.Fail(409, "completeness_series_conflict")
		}
	}
	return nil
}
