package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"rai/server/internal/modelrelease"
	"rai/server/internal/syncing"
	"rai/server/internal/training"

	"github.com/jackc/pgx/v5"
)

type trainingDB interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func trainingJSON(v any) []byte { b, _ := json.Marshal(v); return b }
func trainingError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return syncing.Fail(404, "training_not_found")
	}
	return err
}
func trainingID(ids ...string) error {
	for _, id := range ids {
		if !syncing.ValidID(id) {
			return syncing.Fail(422, "invalid_training_id")
		}
	}
	return nil
}
func trainingLock(ctx context.Context, tx pgx.Tx, dataset string) error {
	_, err := lockActiveDataset(ctx, tx, dataset)
	return err
}
func loadTrainingModelBinding(ctx context.Context, q trainingDB, dataset string) (training.ModelBinding, error) {
	var stack, manifest string
	var version int64
	var assigned time.Time
	err := q.QueryRow(ctx, `SELECT stack_id,assignment_version,encode(config_sha256,'hex'),assigned_at FROM analysis_model_channels WHERE dataset_id=$1 AND channel='active'`, dataset).Scan(&stack, &version, &manifest, &assigned)
	if errors.Is(err, pgx.ErrNoRows) {
		return training.ModelBinding{Model: "rai.athlete_model/1", Adapter: "legacy-v1", Missing: []string{"active_assignment"}}, nil
	}
	if err != nil {
		return training.ModelBinding{}, err
	}
	b := training.ModelBinding{Assignment: stack, AssignmentVersion: version, ManifestSHA: manifest, Model: "rai.athlete_model/2", AsOf: assigned.UTC().Format(time.RFC3339), Adapter: "schema2-v2"}
	if release, ok := modelrelease.Lookup(stack, manifest, "UTC"); ok && release.Manifest.ProductSchema == "rai.athlete_model/3" {
		b.Model = "rai.athlete_model/3"
		b.Adapter = "schema3-v1"
	} else if ok && release.Manifest.ProductSchema == "rai.athlete_model/4" {
		b.Model = "rai.athlete_model/4"
		b.Adapter = "schema4-v1"
	}
	if stack == "legacy" {
		b.Model = "rai.athlete_model/1"
		b.Adapter = "legacy-v1"
	}
	return b, nil
}

func loadPlan(ctx context.Context, q trainingDB, dataset, id string) (training.Plan, error) {
	var p training.Plan
	var c, e []byte
	err := q.QueryRow(ctx, `SELECT p.id::text,p.timezone,p.config_revision,p.override_revision,c.config,COALESCE(d.generation,0),e.effective,COALESCE(j.state,'pending'),COALESCE(j.last_error_code,''),p.active
 FROM training_plans p JOIN training_configs c ON c.dataset_id=p.dataset_id AND c.plan_id=p.id AND c.revision=p.config_revision
 LEFT JOIN dirty_scopes d ON d.dataset_id=p.dataset_id AND d.scope_key='plan:'||p.id::text
 LEFT JOIN training_effective_versions e ON e.dataset_id=p.dataset_id AND e.plan_id=p.id AND e.id=p.current_effective_id
 LEFT JOIN LATERAL (SELECT state,last_error_code FROM jobs WHERE dataset_id=p.dataset_id AND scope_key='plan:'||p.id::text ORDER BY target_generation DESC LIMIT 1) j ON true
 WHERE p.dataset_id=$1 AND (($2='' AND p.active) OR p.id=NULLIF($2,'')::uuid)`, dataset, id).Scan(&p.ID, &p.Timezone, &p.ConfigRevision, &p.OverrideRevision, &c, &p.Generation, &e, &p.GenerationState, &p.LastErrorCode, &p.Active)
	if err != nil {
		return p, trainingError(err)
	}
	if err = json.Unmarshal(c, &p.Config); err != nil {
		return p, err
	}
	p.Status = "pending"
	if len(e) > 0 {
		p.Effective = &training.Effective{}
		if err = json.Unmarshal(e, p.Effective); err != nil {
			return p, err
		}
		p.Status = "published"
	}
	return p, nil
}
func loadOverrides(ctx context.Context, q trainingDB, dataset, plan string, revision int64) ([]training.Override, error) {
	rows, e := q.Query(ctx, `SELECT event FROM training_overrides WHERE dataset_id=$1 AND plan_id=$2 AND revision<=$3 ORDER BY revision,id`, dataset, plan, revision)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []training.Override{}
	for rows.Next() {
		var b []byte
		var o training.Override
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(b, &o); e != nil {
			return nil, e
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
func materialize(ctx context.Context, tx pgx.Tx, dataset string, p training.Plan, baseID string, base training.Base) (training.Effective, error) {
	os, e := loadOverrides(ctx, tx, dataset, p.ID, p.OverrideRevision)
	if e != nil {
		return training.Effective{}, e
	}
	merged, e := training.ResolveEffective(base, os)
	if e != nil {
		return training.Effective{}, e
	}
	var number int64
	if e = tx.QueryRow(ctx, `SELECT effective_number+1 FROM training_plans WHERE dataset_id=$1 AND id=$2 FOR UPDATE`, dataset, p.ID).Scan(&number); e != nil {
		return training.Effective{}, e
	}
	out := training.Effective{MergeVersion: training.MergeVersion, ID: syncing.NewID(), Number: number, PlanID: p.ID, BaseID: baseID, OverrideRevision: p.OverrideRevision, Timezone: p.Timezone, Base: merged}
	if len(trainingJSON(out)) > training.SnapshotBudget-(64<<10) {
		return out, syncing.Fail(422, "training_snapshot_limit")
	}
	_, e = tx.Exec(ctx, `INSERT INTO training_effective_versions(dataset_id,plan_id,id,number,base_id,override_revision,effective) VALUES($1,$2,$3,$4,$5,$6,$7)`, dataset, p.ID, out.ID, number, baseID, p.OverrideRevision, trainingJSON(out))
	if e != nil {
		return out, e
	}
	_, e = tx.Exec(ctx, `UPDATE training_plans SET current_effective_id=$3,effective_number=$4 WHERE dataset_id=$1 AND id=$2`, dataset, p.ID, out.ID, number)
	return out, e
}

// The dataset lock serializes command deduplication and all business mutations.
func (s *Store) trainingCommand(ctx context.Context, p syncing.Principal, id, kind, target string, request any, fn func(pgx.Tx) (any, string, error), out any) error {
	if e := trainingID(id); e != nil {
		return e
	}
	hash, e := training.Hash(struct {
		Kind, Target string
		Request      any
	}{kind, target, request})
	if e != nil {
		return syncing.Fail(422, "invalid_training_input")
	}
	tx, e := s.begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = trainingLock(ctx, tx, p.DatasetID); e != nil {
		return e
	}
	var oldHash, receipt []byte
	e = tx.QueryRow(ctx, `SELECT request_hash,receipt FROM training_commands WHERE dataset_id=$1 AND id=$2`, p.DatasetID, id).Scan(&oldHash, &receipt)
	if e == nil {
		if !bytes.Equal(oldHash, hash) {
			return syncing.Fail(409, "training_command_conflict")
		}
		return json.Unmarshal(receipt, out)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return e
	}
	value, plan, e := fn(tx)
	if e != nil {
		return trainingError(e)
	}
	receipt, e = json.Marshal(value)
	if e != nil {
		return e
	}
	if len(receipt)+1 > training.SnapshotBudget {
		return syncing.Fail(422, "training_snapshot_limit")
	}
	if _, e = tx.Exec(ctx, `INSERT INTO training_commands(dataset_id,id,plan_id,request_hash,receipt) VALUES($1,$2,$3,$4,$5)`, p.DatasetID, id, plan, hash, receipt); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	return json.Unmarshal(receipt, out)
}
func (s *Store) CreateTrainingPlan(ctx context.Context, p syncing.Principal, r training.CreateRequest) (training.Plan, error) {
	var out training.Plan
	if e := trainingID(r.PlanID); e != nil {
		return out, e
	}
	e := s.trainingCommand(ctx, p, r.CommandID, "create", r.PlanID, r, func(tx pgx.Tx) (any, string, error) {
		var zone string
		if e := tx.QueryRow(ctx, `SELECT timezone FROM datasets WHERE id=$1`, p.DatasetID).Scan(&zone); e != nil {
			return nil, "", e
		}
		binding, e := loadTrainingModelBinding(ctx, tx, p.DatasetID)
		if e != nil {
			return nil, "", e
		}
		r.Config.ModelBinding = &binding
		if r.Config.Validate(zone) != nil {
			return nil, "", syncing.Fail(422, "invalid_training_config")
		}
		var exists bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM training_plans WHERE dataset_id=$1 AND id=$2)`, p.DatasetID, r.PlanID).Scan(&exists); e != nil {
			return nil, "", e
		}
		if exists {
			return nil, "", syncing.Fail(409, "training_plan_exists")
		}
		if _, e := tx.Exec(ctx, `UPDATE training_plans SET active=false WHERE dataset_id=$1 AND active`, p.DatasetID); e != nil {
			return nil, "", e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO training_plans(dataset_id,id,timezone) VALUES($1,$2,$3)`, p.DatasetID, r.PlanID, zone); e != nil {
			return nil, "", e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO training_configs(dataset_id,plan_id,revision,config) VALUES($1,$2,1,$3)`, p.DatasetID, r.PlanID, trainingJSON(r.Config)); e != nil {
			return nil, "", e
		}
		if _, e := tx.Exec(ctx, `SELECT rai_training_dirty($1,$2)`, p.DatasetID, r.PlanID); e != nil {
			return nil, "", e
		}
		v, e := loadPlan(ctx, tx, p.DatasetID, r.PlanID)
		return v, r.PlanID, e
	}, &out)
	return out, e
}
func (s *Store) ChangeTrainingPlan(ctx context.Context, p syncing.Principal, id string, r training.ChangeRequest) (training.Plan, error) {
	var out training.Plan
	if e := trainingID(id); e != nil {
		return out, e
	}
	e := s.trainingCommand(ctx, p, r.CommandID, "change_plan", id, r, func(tx pgx.Tx) (any, string, error) {
		v, e := loadPlan(ctx, tx, p.DatasetID, id)
		if e != nil {
			return nil, "", e
		}
		if !v.Active {
			return nil, "", syncing.Fail(409, "training_plan_archived")
		}
		if v.ConfigRevision != r.ExpectedConfigRevision {
			return nil, "", syncing.Fail(409, "training_revision_conflict")
		}
		if r.Operation == "update_config" {
			if r.Config != nil {
				r.Config.ModelBinding = v.Config.ModelBinding
			}
			if r.Config == nil || r.Config.Validate(v.Timezone) != nil || r.Config.StartDate != v.Config.StartDate || r.Config.Weeks != v.Config.Weeks {
				return nil, "", syncing.Fail(422, "invalid_training_config")
			}
			v.ConfigRevision++
			if _, e = tx.Exec(ctx, `INSERT INTO training_configs(dataset_id,plan_id,revision,config) VALUES($1,$2,$3,$4)`, p.DatasetID, id, v.ConfigRevision, trainingJSON(r.Config)); e != nil {
				return nil, "", e
			}
			if _, e = tx.Exec(ctx, `UPDATE training_plans SET config_revision=$3 WHERE dataset_id=$1 AND id=$2`, p.DatasetID, id, v.ConfigRevision); e != nil {
				return nil, "", e
			}
		} else if r.Operation != "recalculate" || r.Config != nil {
			return nil, "", syncing.Fail(422, "invalid_training_change")
		}
		if _, e = tx.Exec(ctx, `SELECT rai_training_dirty($1,$2)`, p.DatasetID, id); e != nil {
			return nil, "", e
		}
		v, e = loadPlan(ctx, tx, p.DatasetID, id)
		return v, id, e
	}, &out)
	return out, e
}
func (s *Store) TrainingPlan(ctx context.Context, p syncing.Principal, id string) (training.Plan, error) {
	if id != "" {
		if e := trainingID(id); e != nil {
			return training.Plan{}, e
		}
	}
	return loadPlan(ctx, s.Pool, p.DatasetID, id)
}
func (s *Store) PutTrainingOverride(ctx context.Context, p syncing.Principal, id string, r training.OverrideRequest) (training.Effective, error) {
	var out training.Effective
	if e := trainingID(id, r.OverrideID, r.SessionID); e != nil {
		return out, e
	}
	canonical := r
	canonical.CommandID = ""
	canonical.ExpectedOverrideRevision = 0
	contentHash, hashErr := training.Hash(struct {
		Plan     string
		Override training.OverrideRequest
	}{id, canonical})
	if hashErr != nil {
		return out, syncing.Fail(422, "invalid_training_override")
	}
	e := s.trainingCommand(ctx, p, r.CommandID, "override", id, r, func(tx pgx.Tx) (any, string, error) {
		v, e := loadPlan(ctx, tx, p.DatasetID, id)
		if e != nil {
			return nil, "", e
		}
		var oldHash, oldReceipt []byte
		lookupErr := tx.QueryRow(ctx, `SELECT o.request_hash,c.receipt FROM training_overrides o JOIN training_commands c ON c.dataset_id=o.dataset_id AND c.id=o.command_id WHERE o.dataset_id=$1 AND o.id=$2`, p.DatasetID, r.OverrideID).Scan(&oldHash, &oldReceipt)
		if lookupErr == nil {
			if !bytes.Equal(oldHash, contentHash) {
				return nil, "", syncing.Fail(409, "override_identity_conflict")
			}
			var original training.Effective
			if e = json.Unmarshal(oldReceipt, &original); e != nil {
				return nil, "", e
			}
			return original, id, nil
		}
		if !errors.Is(lookupErr, pgx.ErrNoRows) {
			return nil, "", lookupErr
		}
		if !v.Active {
			return nil, "", syncing.Fail(409, "training_plan_archived")
		}
		if v.Effective == nil {
			return nil, "", syncing.Fail(409, "training_not_generated")
		}
		if v.OverrideRevision != r.ExpectedOverrideRevision {
			return nil, "", syncing.Fail(409, "training_revision_conflict")
		}
		os, e := loadOverrides(ctx, tx, p.DatasetID, id, v.OverrideRevision)
		if e != nil {
			return nil, "", e
		}
		for _, o := range os {
			if o.ID == r.OverrideID {
				return nil, "", syncing.Fail(409, "override_identity_conflict")
			}
		}
		event := training.Override{ID: r.OverrideID, SessionID: r.SessionID, Operation: r.Operation, Path: r.Path, Value: r.Value, TargetID: r.TargetID, Revision: v.OverrideRevision + 1, CreatedAt: time.Now().UTC(), Status: "active"}
		for _, s := range v.Effective.Sessions {
			if s.ID == r.SessionID {
				x := s
				event.Anchor = &x
			}
		}
		if r.Operation == "add" {
			if event.Anchor != nil || r.Session == nil || r.Session.ID != r.SessionID {
				return nil, "", syncing.Fail(422, "invalid_training_override")
			}
			var used bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM training_sessions WHERE dataset_id=$1 AND plan_id=$2 AND id=$3)`, p.DatasetID, id, r.SessionID).Scan(&used); e != nil {
				return nil, "", e
			}
			if used {
				return nil, "", syncing.Fail(409, "training_session_identity_conflict")
			}
			session := r.Session.Prescription()
			session.Origin = "user_added"
			session.Slot = -1
			session.Status = "scheduled"
			session.Modified = true
			session.Reasons = []string{"user_added"}
			session.Conflicts = nil
			if training.ValidateSession(session) != nil {
				return nil, "", syncing.Fail(422, "invalid_training_session")
			}
			event.Anchor = &session
			if _, e = tx.Exec(ctx, `INSERT INTO training_sessions(dataset_id,plan_id,id,origin) VALUES($1,$2,$3,'user_added')`, p.DatasetID, id, r.SessionID); e != nil {
				return nil, "", e
			}
		} else if r.Session != nil {
			return nil, "", syncing.Fail(422, "invalid_training_override")
		}
		if r.Operation == "revoke" {
			if e = trainingID(r.TargetID); e != nil {
				return nil, "", e
			}
			found := false
			for _, o := range training.ActiveOverrides(os) {
				if o.ID == r.TargetID && o.SessionID == r.SessionID && o.Status == "active" && o.Operation != "revoke" {
					found = true
				}
			}
			if !found {
				return nil, "", syncing.Fail(409, "override_not_active")
			}
		} else if r.TargetID != "" {
			return nil, "", syncing.Fail(422, "invalid_training_override")
		}
		if event.Anchor == nil && r.Operation != "revoke" {
			return nil, "", syncing.Fail(404, "training_session_not_found")
		}
		// New edits must be applicable to the prescription the user is editing.
		// Conflict projection is reserved for accepted facts rebased later.
		if r.Operation == "edit" || r.Operation == "move" {
			candidate := *event.Anchor
			if candidate.TargetHR != nil {
				candidate.TargetHR = training.Ptr(*candidate.TargetHR)
			}
			if candidate.PaceSPerKM != nil {
				candidate.PaceSPerKM = training.Ptr(*candidate.PaceSPerKM)
			}
			if (r.Operation == "edit" && r.Path == "date") || (r.Operation == "move" && r.Path != "date") || training.ApplyField(&candidate, r.Path, r.Value) != nil {
				return nil, "", syncing.Fail(422, "invalid_training_override")
			}
		}
		if r.Operation != "edit" && r.Operation != "move" && (r.Path != "" || len(r.Value) > 0) {
			return nil, "", syncing.Fail(422, "invalid_training_override")
		}
		var b []byte
		if e = tx.QueryRow(ctx, `SELECT base FROM training_base_versions WHERE dataset_id=$1 AND plan_id=$2 AND id=$3`, p.DatasetID, id, v.Effective.BaseID).Scan(&b); e != nil {
			return nil, "", e
		}
		var base training.Base
		if e = json.Unmarshal(b, &base); e != nil {
			return nil, "", e
		}
		merged, e := training.ResolveEffective(base, append(os, event))
		if e != nil {
			return nil, "", syncing.Fail(422, "invalid_training_override")
		}
		start := v.Config.StartDate
		d, _ := time.Parse("2006-01-02", start)
		end := d.AddDate(0, 0, v.Config.Weeks*7).Format("2006-01-02")
		for _, s := range merged.Sessions {
			if s.Date < start || s.Date >= end {
				return nil, "", syncing.Fail(422, "training_date_out_of_range")
			}
		}
		if len(trainingJSON(event)) > training.AuditEntryBudget {
			return nil, "", syncing.Fail(422, "training_audit_entry_limit")
		}
		if _, e = tx.Exec(ctx, `INSERT INTO training_overrides(dataset_id,plan_id,id,session_id,revision,operation,event,command_id,request_hash) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, p.DatasetID, id, event.ID, event.SessionID, event.Revision, event.Operation, trainingJSON(event), r.CommandID, contentHash); e != nil {
			return nil, "", e
		}
		v.OverrideRevision = event.Revision
		if _, e = tx.Exec(ctx, `UPDATE training_plans SET override_revision=$3 WHERE dataset_id=$1 AND id=$2`, p.DatasetID, id, v.OverrideRevision); e != nil {
			return nil, "", e
		}
		eff, e := materialize(ctx, tx, p.DatasetID, v, v.Effective.BaseID, base)
		return eff, id, e
	}, &out)
	return out, e
}
func (s *Store) TrainingAudit(ctx context.Context, p syncing.Principal, plan, id string) (training.Audit, error) {
	var out training.Audit
	if e := trainingID(plan, id); e != nil {
		return out, e
	}
	var eb, bb, cb []byte
	e := s.Pool.QueryRow(ctx, `SELECT e.effective,b.base,r.context FROM training_effective_versions e JOIN training_base_versions b ON b.dataset_id=e.dataset_id AND b.plan_id=e.plan_id AND b.id=e.base_id JOIN training_generation_runs r ON r.dataset_id=b.dataset_id AND r.id=b.run_id WHERE e.dataset_id=$1 AND e.plan_id=$2 AND e.id=$3`, p.DatasetID, plan, id).Scan(&eb, &bb, &cb)
	if e != nil {
		return out, trainingError(e)
	}
	for _, v := range []struct {
		b []byte
		v any
	}{{eb, &out.Effective}, {bb, &out.Base}, {cb, &out.Context}} {
		if e = json.Unmarshal(v.b, v.v); e != nil {
			return out, e
		}
	}
	out.Overrides, e = loadOverrides(ctx, s.Pool, p.DatasetID, plan, out.Effective.OverrideRevision)
	out.Overrides = training.ActiveOverrides(out.Overrides)
	return out, e
}
func (s *Store) TrainingHistory(ctx context.Context, p syncing.Principal, id string, after int64, limit int) (training.History, error) {
	out := training.History{Versions: []training.VersionSummary{}}
	if e := trainingPageQuery(id, after, limit); e != nil {
		return out, e
	}
	var exists bool
	if e := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM training_plans WHERE dataset_id=$1 AND id=$2)`, p.DatasetID, id).Scan(&exists); e != nil {
		return out, e
	}
	if !exists {
		return out, syncing.Fail(404, "training_not_found")
	}
	rows, e := s.Pool.Query(ctx, `SELECT `+trainingSummaryColumns+` FROM training_effective_versions e WHERE dataset_id=$1 AND plan_id=$2 AND number>$3 ORDER BY number LIMIT $4`, p.DatasetID, id, after, limit+1)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		v, e := scanTrainingSummary(rows)
		if e != nil {
			return out, e
		}
		if len(out.Versions) == limit {
			out.Next = out.Versions[len(out.Versions)-1].Number
			break
		}
		out.Versions = append(out.Versions, v)
	}
	return out, rows.Err()
}
func trainingPlanID(l Lease) string { return strings.TrimPrefix(l.ScopeKey, "plan:") }
