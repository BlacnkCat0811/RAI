package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	am "rai/server/internal/athletemodel"
	"rai/server/internal/modelmanifest"
	"rai/server/internal/modelrelease"

	"github.com/jackc/pgx/v5"
)

type AssignmentAudit struct {
	Revision    int64
	Action      string
	FromStack   string
	ToStack     string
	ManifestSHA string
	GateSummary json.RawMessage
	Actor       string
	CreatedAt   time.Time
}

func (s *Store) PromoteModelStack(ctx context.Context, dataset, actor, stack string, gate am.PromotionGate, manifestSHA string) (AssignmentAudit, error) {
	return s.promoteModelStack(ctx, dataset, actor, stack, manifestSHA, &gate, nil, false)
}

// PromoteModelStackAuthorized activates a real dataset only with an explicit
// owner authorization bound to the exact dataset, stack and manifest. It does
// not claim the synthetic acceptance gates were passed.
func (s *Store) PromoteModelStackAuthorized(ctx context.Context, dataset, actor, stack, manifestSHA string, authorization []byte) (AssignmentAudit, error) {
	return s.promoteModelStack(ctx, dataset, actor, stack, manifestSHA, nil, authorization, false)
}

// promoteModelStack moves the prepared candidate to active. Exactly one
// authority applies: a synthetic gate, an owner authorization file, or auto
// (the deployment-time activation run with the owner database role).
func (s *Store) promoteModelStack(ctx context.Context, dataset, actor, stack, manifestSHA string, gate *am.PromotionGate, authorization []byte, auto bool) (AssignmentAudit, error) {
	var out AssignmentAudit
	expected, err := modelmanifest.CurrentHash()
	m := modelmanifest.Current()
	if err != nil || manifestSHA != expected || stack != modelmanifest.StackID() {
		return out, am.ErrGateClosed
	}
	if gate != nil {
		if gate.DatasetID != dataset || gate.ConfigSHA != expected || gate.Model != m.VO2Max.ModelID || gate.Version != m.VO2Max.Version ||
			gate.MappingVersion != m.MappingVersion || gate.ConsumerSchema != m.ProductSchema || gate.FeatureSchema != m.Features.Schema || gate.SourcePolicy != m.VO2Max.SourcePolicy || gate.InputRegistry != m.InputRegistry {
			return out, am.ErrGateClosed
		}
	} else if len(authorization) == 0 && !auto {
		return out, am.ErrOwnerAuthorizationInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL rai.model_assignment='on'`); err != nil {
		return out, err
	}
	var synthetic bool
	if err = tx.QueryRow(ctx, `SELECT synthetic_test FROM datasets WHERE id=$1 FOR UPDATE`, dataset).Scan(&synthetic); err != nil {
		return out, err
	}
	var gateJSON json.RawMessage
	if gate != nil {
		if err := gate.Validate(time.Now().UTC(), manifestSHA, synthetic); err != nil {
			return out, err
		}
		gateJSON, _ = json.Marshal(gate)
	} else if auto {
		gateJSON, _ = json.Marshal(map[string]any{
			"autoActivated":         true,
			"trigger":               "deploy",
			"syntheticDataset":      synthetic,
			"standardGatesVerified": false,
		})
	} else {
		var auth am.OwnerAuthorization
		if synthetic || json.Unmarshal(authorization, &auth) != nil || auth.Validate(time.Now().UTC(), dataset, stack, manifestSHA) != nil {
			return out, am.ErrOwnerAuthorizationInvalid
		}
		h := sha256.Sum256(authorization)
		gateJSON, _ = json.Marshal(map[string]any{
			"ownerAuthorized":       true,
			"authorizationSHA256":   hex.EncodeToString(h[:]),
			"authorizedBy":          auth.AuthorizedBy,
			"authorizedAt":          auth.AuthorizedAt,
			"expiresAt":             auth.ExpiresAt,
			"statement":             auth.Statement,
			"syntheticDataset":      false,
			"standardGatesVerified": false,
		})
	}
	if _, err = tx.Exec(ctx, `INSERT INTO analysis_model_channels(dataset_id,channel,stack_id,config_sha256,assignment_version)
	 VALUES($1,'active','legacy',decode(repeat('00',32),'hex'),1) ON CONFLICT(dataset_id,channel) DO NOTHING`, dataset); err != nil {
		return out, err
	}
	var from string
	if err = tx.QueryRow(ctx, `SELECT stack_id FROM analysis_model_channels WHERE dataset_id=$1 AND channel='active' FOR UPDATE`, dataset).Scan(&from); err != nil {
		return out, err
	}
	var cand, config string
	if err = tx.QueryRow(ctx, `SELECT stack_id,encode(config_sha256,'hex') FROM analysis_model_channels WHERE dataset_id=$1 AND channel='candidate' FOR UPDATE`, dataset).Scan(&cand, &config); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return out, am.ErrGateClosed
		}
		return out, err
	}
	if cand != stack || config != expected || cand == from {
		return out, am.ErrGateClosed
	}
	var backfillIncomplete bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(
 SELECT 1 FROM model_backfill_operations o WHERE o.dataset_id=$1 AND o.stack_id=$2 AND (
   o.status IN ('planned','running','paused') OR (o.status='candidate_ready' AND EXISTS (
     SELECT 1 FROM dirty_scopes d WHERE d.dataset_id=o.dataset_id AND d.generation<>d.clean_generation
       AND d.scope_key BETWEEN 'model/'||o.stack_id||'/athlete/day/v2/'||o.from_date::text
                           AND 'model/'||o.stack_id||'/athlete/day/v2/'||o.through_date::text
   ))
 ))`, dataset, stack).Scan(&backfillIncomplete); err != nil {
		return out, err
	}
	if backfillIncomplete {
		return out, am.ErrGateClosed
	}
	if err = ensureModelStackReady(ctx, tx, dataset, stack, manifestSHA); err != nil {
		return out, err
	}
	var revision int64
	if err = tx.QueryRow(ctx, `SELECT GREATEST(COALESCE((SELECT max(revision) FROM analysis_assignment_audit WHERE dataset_id=$1),0),COALESCE((SELECT max(assignment_version) FROM analysis_model_channels WHERE dataset_id=$1),0))+1`, dataset).Scan(&revision); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM analysis_model_channels WHERE dataset_id=$1 AND channel='previous'`, dataset); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `UPDATE analysis_model_channels SET channel='previous' WHERE dataset_id=$1 AND channel='active'`, dataset); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `UPDATE analysis_model_channels SET channel='active',assignment_version=$2,assigned_at=now() WHERE dataset_id=$1 AND channel='candidate'`, dataset, revision); err != nil {
		return out, err
	}
	hash, _ := hex.DecodeString(manifestSHA)
	if _, err = tx.Exec(ctx, `INSERT INTO analysis_assignment_audit(dataset_id,revision,action,from_stack,to_stack,manifest_sha256,gate_summary,actor) VALUES($1,$2,'promote',$3,$4,$5,$6,$7)`, dataset, revision, from, stack, hash, gateJSON, actor); err != nil {
		return out, err
	}
	if err = tx.Commit(ctx); err != nil {
		return out, err
	}
	out = AssignmentAudit{Revision: revision, Action: "promote", FromStack: from, ToStack: stack, ManifestSHA: manifestSHA, GateSummary: gateJSON, Actor: actor}
	return out, nil
}

func (s *Store) RollbackModelStack(ctx context.Context, dataset, actor, manifestSHA string) (AssignmentAudit, error) {
	return s.rollbackModelStack(ctx, dataset, actor, manifestSHA, true, nil)
}

// RollbackModelStackAuthorized restores the previous active stack on a real
// dataset with the same explicit owner authorization used to activate it.
func (s *Store) RollbackModelStackAuthorized(ctx context.Context, dataset, actor, manifestSHA string, authorization []byte) (AssignmentAudit, error) {
	return s.rollbackModelStack(ctx, dataset, actor, manifestSHA, false, authorization)
}

func (s *Store) rollbackModelStack(ctx context.Context, dataset, actor, manifestSHA string, requireSynthetic bool, authorization []byte) (AssignmentAudit, error) {
	var out AssignmentAudit
	expected, err := modelmanifest.CurrentHash()
	if err != nil || manifestSHA != expected {
		return out, am.ErrGateClosed
	}
	if !requireSynthetic && len(authorization) == 0 {
		return out, am.ErrOwnerAuthorizationInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL rai.model_assignment='on'`); err != nil {
		return out, err
	}
	var synthetic bool
	if err = tx.QueryRow(ctx, `SELECT synthetic_test FROM datasets WHERE id=$1 FOR UPDATE`, dataset).Scan(&synthetic); err != nil {
		return out, err
	}
	gateJSON := json.RawMessage(`{"rollback":true}`)
	if requireSynthetic {
		if !synthetic {
			return out, am.ErrGateClosed
		}
	} else {
		stack := modelmanifest.StackID()
		var auth am.OwnerAuthorization
		if synthetic || json.Unmarshal(authorization, &auth) != nil || auth.Validate(time.Now().UTC(), dataset, stack, manifestSHA) != nil {
			return out, am.ErrOwnerAuthorizationInvalid
		}
		h := sha256.Sum256(authorization)
		gateJSON, _ = json.Marshal(map[string]any{
			"rollback":              true,
			"ownerAuthorized":       true,
			"authorizationSHA256":   hex.EncodeToString(h[:]),
			"authorizedBy":          auth.AuthorizedBy,
			"authorizedAt":          auth.AuthorizedAt,
			"expiresAt":             auth.ExpiresAt,
			"syntheticDataset":      false,
			"standardGatesVerified": false,
		})
	}
	var active, previous, previousManifest string
	if err = tx.QueryRow(ctx, `SELECT stack_id FROM analysis_model_channels WHERE dataset_id=$1 AND channel='active' FOR UPDATE`, dataset).Scan(&active); err != nil {
		return out, err
	}
	if err = tx.QueryRow(ctx, `SELECT stack_id,COALESCE(encode(config_sha256,'hex'),'')
 FROM analysis_model_channels WHERE dataset_id=$1 AND channel='previous' FOR UPDATE`, dataset).Scan(&previous, &previousManifest); err != nil {
		return out, err
	}
	if previous == "legacy" {
		var dirty bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM dirty_scopes WHERE dataset_id=$1 AND scope_key='athlete/default' AND generation<>clean_generation)`, dataset).Scan(&dirty); err != nil {
			return out, err
		}
		if dirty {
			return out, am.ErrGateClosed
		}
	} else if err = ensureModelStackReady(ctx, tx, dataset, previous, previousManifest); err != nil {
		return out, err
	}
	var revision int64
	if err = tx.QueryRow(ctx, `SELECT GREATEST(COALESCE((SELECT max(revision) FROM analysis_assignment_audit WHERE dataset_id=$1),0),COALESCE((SELECT max(assignment_version) FROM analysis_model_channels WHERE dataset_id=$1),0))+1`, dataset).Scan(&revision); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM analysis_model_channels WHERE dataset_id=$1 AND channel='candidate'`, dataset); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `UPDATE analysis_model_channels SET channel='candidate' WHERE dataset_id=$1 AND channel='active'`, dataset); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `UPDATE analysis_model_channels SET channel='active',assignment_version=$2,assigned_at=now() WHERE dataset_id=$1 AND channel='previous'`, dataset, revision); err != nil {
		return out, err
	}
	hash, _ := hex.DecodeString(manifestSHA)
	if _, err = tx.Exec(ctx, `INSERT INTO analysis_assignment_audit(dataset_id,revision,action,from_stack,to_stack,manifest_sha256,gate_summary,actor) VALUES($1,$2,'rollback',$3,$4,$5,$6,$7)`, dataset, revision, active, previous, hash, gateJSON, actor); err != nil {
		return out, err
	}
	if err = tx.Commit(ctx); err != nil {
		return out, err
	}
	return AssignmentAudit{Revision: revision, Action: "rollback", FromStack: active, ToStack: previous, ManifestSHA: manifestSHA, GateSummary: gateJSON, Actor: actor}, nil
}

func (s *Store) PrepareModelStack(ctx context.Context, dataset string) (string, error) {
	return s.prepareModelStack(ctx, dataset, nil, false)
}

// PrepareModelStackAuthorized registers the candidate for a real dataset. The
// exact authorization bytes are parsed and validated while the dataset row is
// locked; the candidate remains invisible until an authorized promotion.
func (s *Store) PrepareModelStackAuthorized(ctx context.Context, dataset string, authorization []byte) (string, error) {
	return s.prepareModelStack(ctx, dataset, authorization, false)
}

func (s *Store) prepareModelStack(ctx context.Context, dataset string, ownerAuthorization []byte, auto bool) (string, error) {
	stack := modelmanifest.StackID()
	manifest, err := modelmanifest.CurrentHash()
	if err != nil {
		return "", err
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var synthetic bool
	var today string
	if err = tx.QueryRow(ctx, `SELECT synthetic_test,(clock_timestamp() AT TIME ZONE timezone)::date::text FROM datasets
 WHERE id=$1 AND retired_at IS NULL FOR UPDATE`, dataset).Scan(&synthetic, &today); err != nil {
		return "", err
	}
	if auto {
		// Deployment-time activation: the owner role running it is the
		// authority, for real and synthetic datasets alike.
	} else if ownerAuthorization == nil {
		if !synthetic {
			return "", am.ErrGateClosed
		}
	} else {
		var auth am.OwnerAuthorization
		if synthetic || json.Unmarshal(ownerAuthorization, &auth) != nil || auth.Validate(time.Now().UTC(), dataset, stack, manifest) != nil {
			return "", am.ErrOwnerAuthorizationInvalid
		}
	}
	var existing string
	err = tx.QueryRow(ctx, `SELECT stack_id FROM analysis_model_channels WHERE dataset_id=$1 AND channel='candidate' FOR UPDATE`, dataset).Scan(&existing)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if existing != "" && existing != stack {
		if !auto {
			return "", am.ErrGateClosed
		}
		// A candidate left by an earlier image is never readable; the
		// deployed code replaces it with its own stack.
		if _, err = tx.Exec(ctx, `DELETE FROM analysis_model_channels WHERE dataset_id=$1 AND channel='candidate'`, dataset); err != nil {
			return "", err
		}
		existing = ""
	}
	if existing == "" {
		var alreadyActive bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM analysis_model_channels WHERE dataset_id=$1 AND stack_id=$2 AND channel IN ('active','previous'))`, dataset, stack).Scan(&alreadyActive); err != nil {
			return "", err
		}
		if alreadyActive {
			return "", am.ErrGateClosed
		}
		if _, err = tx.Exec(ctx, `INSERT INTO analysis_model_channels(dataset_id,channel,stack_id,config_sha256,assignment_version,extension_configs)
 VALUES($1,'candidate',$2,decode($3,'hex'),1,jsonb_build_object('rai.model_manifest/1',$3::text))`, dataset, stack, manifest); err != nil {
			return "", err
		}
	}
	scope := modelmanifest.DayScope(stack, today)
	var id string
	if err = tx.QueryRow(ctx, `SELECT j.id::text FROM jobs j JOIN dirty_scopes d USING(dataset_id,scope_key)
 WHERE j.dataset_id=$1 AND j.scope_key=$2 AND j.target_generation=d.generation AND j.kind='analysis.model_stack_v2'
 AND j.state IN ('queued','running','succeeded')`, dataset, scope).Scan(&id); errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, `SELECT rai_schedule_model_day($1,$2,$3::date,clock_timestamp())::text`, dataset, stack, today).Scan(&id)
	}
	if err != nil {
		return "", err
	}
	return id, tx.Commit(ctx)
}

func ensureModelStackReady(ctx context.Context, tx pgx.Tx, dataset, stack, manifest string) error {
	if manifest == "" {
		return am.ErrGateClosed
	}
	var day, tz string
	if err := tx.QueryRow(ctx, `SELECT (clock_timestamp() AT TIME ZONE timezone)::date::text,timezone FROM datasets WHERE id=$1`, dataset).Scan(&day, &tz); err != nil {
		return err
	}
	release, supported := modelrelease.Lookup(stack, manifest, tz)
	if !supported {
		return am.ErrGateClosed
	}
	rows, err := tx.Query(ctx, `SELECT a.artifact_key,a.schema_version,encode(a.content_sha256,'hex'),a.payload,r.algorithm_name,r.algorithm_version,r.target_generation,p.published_at
 FROM analysis_model_channels m
 JOIN dirty_scopes d ON d.dataset_id=m.dataset_id AND d.scope_key=$3 AND d.generation=d.clean_generation
 JOIN analysis_publications p ON p.dataset_id=d.dataset_id AND p.scope_key=d.scope_key AND p.generation=d.generation AND p.day=$4::date
 JOIN analysis_runs r ON r.dataset_id=p.dataset_id AND r.id=p.published_run_id AND r.scope_key=d.scope_key AND r.target_generation=d.generation
 JOIN jobs j ON j.dataset_id=r.dataset_id AND j.id=r.job_id AND j.scope_key=d.scope_key AND j.target_generation=d.generation AND j.kind='analysis.model_stack_v2' AND j.state='succeeded'
 JOIN analysis_artifacts a ON a.dataset_id=r.dataset_id AND a.run_id=r.id AND p.metric_family IN ('model_product_v2/'||a.artifact_key,'model_product_v3/'||a.artifact_key)
 WHERE m.dataset_id=$1 AND m.stack_id=$2 AND m.channel IN ('candidate','previous')
 AND m.config_sha256=decode($5,'hex') AND m.extension_configs->>'rai.model_manifest/1'=$5 AND r.status='SUCCEEDED' AND r.lineage->>'manifestSHA256'=$5`,
		dataset, stack, modelmanifest.DayScope(stack, day), day, manifest)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := make(map[string]bool, modelProductSlots)
	for rows.Next() {
		var a publishedModelArtifact
		if err := rows.Scan(&a.Key, &a.Schema, &a.ContentHash, &a.Payload, &a.Algorithm, &a.Version, &a.Generation, &a.PublishedAt); err != nil {
			return err
		}
		if retiredAbilityKey(a.Key) {
			// The v60 running ability of a retained release is no longer
			// served; switching to that release turns running fitness off.
			continue
		}
		metric := modelProductMetric(a.Key)
		a.ManifestSHA = manifest // The SQL binds this to both assignment and run.
		if seen[a.Key] || !validHistoryArtifactForRelease(metric, dataset, tz, a, release) {
			return am.ErrGateClosed
		}
		seen[a.Key] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// Only schema4 releases are retained: six slots plus session recovery
	// and load focus.
	if release.Manifest.ProductSchema != am.Schema4 || len(seen) != modelProductSlots+2 {
		return am.ErrGateClosed
	}
	return nil
}

// retiredAbilityKey names the v60 running-ability artifacts of retained
// releases. Their rows stay for audit; no reader validates or serves them.
func retiredAbilityKey(key string) bool {
	return key == "performance_state_v2" || key == "running_ability_estimate"
}

func modelProductMetric(key string) string {
	switch key {
	case "load_state":
		return "trainingLoad"
	case "running_fitness":
		return "runningFitness"
	case "vo2max_external_projection":
		return "externalVO2Max"
	case "vo2max_submax":
		return "raiVO2Max"
	case "physiological_recovery":
		return "physiologicalRecovery"
	case "training_readiness":
		return "trainingReadiness"
	case "session_recovery":
		return "sessionRecovery"
	case "load_focus":
		return "loadFocus"
	default:
		return ""
	}
}
