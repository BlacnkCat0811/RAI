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

// CommitWorkoutCompletenessV2 commits a workout-sync-completeness/v2 manifest.
//
// It is the v1 transaction with the inventory invariant widened exactly as the
// frozen source policy requires: every visible running Workout must be either
// an eligible Workout with a durable Protocol 3 receipt, or an explicitly
// excluded Workout with re-verifiable source evidence. Counts alone never
// commit: membership is proved by hashed identities on both sides, and the
// server re-runs the frozen classifier over the uploaded evidence.
func (s *Store) CommitWorkoutCompletenessV2(ctx context.Context, p syncing.Principal, raw []byte, lease syncing.UploadLease) (json.RawMessage, error) {
	m, err := syncing.DecodeWorkoutCompletenessV2(raw)
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
	var priorProtocol int
	var headsRaw, inventoryRaw []byte
	heads := map[string]syncing.CompletenessHead{}
	inventory := map[string]bool{}
	err = tx.QueryRow(ctx, `SELECT generation,window_from_us,known_cutoff_us,encode(request_sha256,'hex'),protocol_version,head_state,inventory FROM workout_sync_completeness WHERE dataset_id=$1 AND collector_epoch=$2 AND dataset_epoch=$3 ORDER BY generation DESC LIMIT 1`, p.DatasetID, m.CollectorEpoch, m.DatasetEpoch).Scan(&generation, &from, &cutoff, &priorHash, &priorProtocol, &headsRaw, &inventoryRaw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		// A v1 manifest is never reinterpreted as v2: v2 may only extend a v2
		// lineage. A dataset with committed v1 manifests must fail closed here
		// rather than silently inherit their inventory semantics.
		if priorProtocol != syncing.WorkoutCompletenessV2Version {
			return nil, syncing.Fail(409, "manifest_generation_conflict")
		}
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
	// The visible inventory the server derived from the page chain must be
	// exactly the union of eligible and excluded hashed identities.
	visibleHashes := make([]string, 0, len(wanted))
	for _, id := range wanted {
		visibleHashes = append(visibleHashes, syncing.WorkoutIdentitySHA256(id))
	}
	if len(wanted) != m.Inventory.VisibleCount || !syncing.EqualStringSet(visibleHashes, m.VisibleIdentitySHA256()) {
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
	// Every stored running Workout is eligible and must be proved; every
	// excluded Workout must be absent from the imported fact domain.
	storedHashes := make([]string, 0, len(stored))
	for _, id := range stored {
		storedHashes = append(storedHashes, syncing.WorkoutIdentitySHA256(id))
	}
	if len(stored) != m.Inventory.EligibleCount || !syncing.EqualStringSet(storedHashes, syncing.EligibleIdentitySHA256(m)) {
		return nil, syncing.Fail(409, "completeness_known_workout_missing")
	}
	scope := completenessScope{datasetID: m.DatasetID, datasetEpoch: m.DatasetEpoch, collectorEpoch: m.CollectorEpoch}
	for _, proof := range m.Workouts {
		if err = verifyCompletenessWorkout(ctx, tx, p, scope, proof, streamMap, tombstones); err != nil {
			return nil, err
		}
	}
	factRevision, err := workoutCompletenessFactRevision(ctx, tx, p.DatasetID)
	if err != nil {
		return nil, err
	}
	receipt := syncing.WorkoutCompletenessReceiptV2{Contract: syncing.WorkoutCompletenessV2Contract, ProtocolVersion: syncing.WorkoutCompletenessV2Version, SourcePolicyVersion: syncing.WorkoutSourcePolicyVersion, DatasetID: m.DatasetID, DatasetEpoch: m.DatasetEpoch, CollectorEpoch: m.CollectorEpoch, ManifestID: m.ManifestID, Generation: m.Generation, ManifestSHA256: hash, IngestStatus: "committed", CommitRevision: rev + 1, StreamCount: len(m.Streams), WorkoutCount: len(m.Workouts), VisibleCount: m.Inventory.VisibleCount, EligibleCount: m.Inventory.EligibleCount, ExcludedCount: m.Inventory.ExcludedCount, UnclassifiedCount: m.Inventory.UnclassifiedCount, VisibleSHA256: m.Inventory.VisibleSHA256, EligibleSHA256: m.Inventory.EligibleSHA256, ExcludedSHA256: m.Inventory.ExcludedSHA256}
	body, _ := json.Marshal(receipt)
	headBytes, _ := json.Marshal(newHeads)
	inventoryBytes, _ := json.Marshal(wanted)
	summaryBytes, _ := json.Marshal(m.Inventory)
	exclusionBytes, _ := json.Marshal(m.Exclusions)
	if _, err = tx.Exec(ctx, `INSERT INTO workout_sync_completeness(dataset_id,dataset_epoch,collector_epoch,generation,manifest_id,stream_set_version,window_from_us,known_cutoff_us,fact_revision,commit_rev,request_sha256,head_state,inventory,request_bytes,receipt,protocol_version,source_policy_version,source_policy_sha256,classification_version,inventory_summary,exclusions) VALUES($1,$2,$3,$4,$5,1,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`, p.DatasetID, m.DatasetEpoch, m.CollectorEpoch, m.Generation, m.ManifestID, m.WindowFromUs, m.KnownCutoffUs, factRevision, rev+1, digest, headBytes, inventoryBytes, raw, body, syncing.WorkoutCompletenessV2Version, syncing.WorkoutSourcePolicyVersion, syncing.WorkoutSourcePolicySHA256, syncing.WorkoutSourceClassificationVer, summaryBytes, exclusionBytes); err != nil {
		return nil, err
	}
	change, _ := json.Marshal(map[string]any{"manifestSHA256": hash, "generation": m.Generation, "streamCount": len(m.Streams), "workoutCount": len(m.Workouts), "sourcePolicyVersion": syncing.WorkoutSourcePolicyVersion, "visibleCount": m.Inventory.VisibleCount, "eligibleCount": m.Inventory.EligibleCount, "excludedCount": m.Inventory.ExcludedCount, "unclassifiedCount": m.Inventory.UnclassifiedCount})
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
