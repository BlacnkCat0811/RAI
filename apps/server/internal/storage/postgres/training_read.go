package postgres

import (
	"context"
	"encoding/json"
	"time"

	"rai/server/internal/syncing"
	"rai/server/internal/training"
)

const trainingSummaryColumns = `e.id::text,e.number,e.plan_id::text,e.base_id::text,e.override_revision,COALESCE(e.effective->>'mergeVersion','training_merge/v1'),e.effective->>'timezone',e.created_at`

// Retain the old aggregate response only while it fits its original budget.
// New product clients use the summary endpoint, then immutable detail pages.
func (s *Store) TrainingHistoryLegacy(ctx context.Context, p syncing.Principal, id string, after int64, limit int) (training.LegacyHistory, error) {
	out := training.LegacyHistory{Versions: []training.Effective{}}
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
	rows, e := s.Pool.Query(ctx, `SELECT effective FROM training_effective_versions WHERE dataset_id=$1 AND plan_id=$2 AND number>$3 ORDER BY number LIMIT $4`, p.DatasetID, id, after, limit+1)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var b []byte
		var v training.Effective
		if e = rows.Scan(&b); e != nil {
			return out, e
		}
		if len(out.Versions) == limit {
			break
		}
		if e = json.Unmarshal(b, &v); e != nil {
			return out, e
		}
		out.Versions = append(out.Versions, v)
		out.Next = v.Number
		if len(trainingJSON(out))+1 > training.SnapshotBudget {
			return out, syncing.Fail(413, "training_response_requires_pagination")
		}
	}
	return out, rows.Err()
}

func scanTrainingSummary(row interface{ Scan(...any) error }) (training.VersionSummary, error) {
	var v training.VersionSummary
	e := row.Scan(&v.ID, &v.Number, &v.PlanID, &v.BaseID, &v.OverrideRevision, &v.MergeVersion, &v.Timezone, &v.CreatedAt)
	return v, e
}
func trainingPageQuery(id string, after int64, limit int) error {
	if e := trainingID(id); e != nil {
		return e
	}
	if after < 0 || limit < 1 || limit > 50 {
		return syncing.Fail(422, "invalid_training_query")
	}
	return nil
}
func (s *Store) trainingSummary(ctx context.Context, p syncing.Principal, plan, id string) (training.VersionSummary, error) {
	if e := trainingID(plan, id); e != nil {
		return training.VersionSummary{}, e
	}
	v, e := scanTrainingSummary(s.Pool.QueryRow(ctx, `SELECT `+trainingSummaryColumns+` FROM training_effective_versions e WHERE dataset_id=$1 AND plan_id=$2 AND id=$3`, p.DatasetID, plan, id))
	return v, trainingError(e)
}

// Audit is pinned to the immutable Effective's revision, including revoke
// status at that revision. Later edits cannot change a page already traversed.
func (s *Store) TrainingAuditPage(ctx context.Context, p syncing.Principal, plan, id string, after int64, limit int) (training.AuditPage, error) {
	out := training.AuditPage{Overrides: []training.Override{}}
	if e := trainingPageQuery(plan, after, limit); e != nil {
		return out, e
	}
	v, e := s.trainingSummary(ctx, p, plan, id)
	if e != nil {
		return out, e
	}
	out.Version = v
	if after > v.OverrideRevision {
		return out, syncing.Fail(422, "invalid_training_query")
	}
	rows, e := s.Pool.Query(ctx, `SELECT o.event,
 (SELECT r.event->>'createdAt' FROM training_overrides r WHERE r.dataset_id=o.dataset_id AND r.plan_id=o.plan_id AND r.revision>o.revision AND r.revision<=$3 AND r.operation='revoke' AND r.event->>'targetOverrideID'=o.id::text ORDER BY r.revision LIMIT 1)
 FROM training_overrides o WHERE o.dataset_id=$1 AND o.plan_id=$2 AND o.revision<=$3 AND o.revision>$4 ORDER BY o.revision LIMIT $5`, p.DatasetID, plan, v.OverrideRevision, after, limit+1)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var revoked *string
		var o training.Override
		if e = rows.Scan(&raw, &revoked); e != nil {
			return out, e
		}
		if len(out.Overrides) == limit {
			out.Next = out.Overrides[len(out.Overrides)-1].Revision
			break
		}
		if e = json.Unmarshal(raw, &o); e != nil {
			return out, e
		}
		o.Status = "active"
		o.UpdatedAt = o.CreatedAt
		if revoked != nil {
			o.Status = "revoked"
			o.UpdatedAt, e = time.Parse(time.RFC3339Nano, *revoked)
			if e != nil {
				return out, e
			}
		}
		out.Overrides = append(out.Overrides, o)
		if len(trainingJSON(out))+32 > training.PageBudget {
			out.Overrides = out.Overrides[:len(out.Overrides)-1]
			if len(out.Overrides) == 0 {
				return out, syncing.Fail(413, "training_audit_entry_limit")
			}
			out.Next = out.Overrides[len(out.Overrides)-1].Revision
			break
		}
	}
	return out, rows.Err()
}

// Each immutable detail array has its own ordinal cursor. Metadata keeps IDs,
// recommendations, config, parameters and lineage; pages permit full replay
// without shipping all prescriptions, context and overrides together.
func (s *Store) TrainingDetailPage(ctx context.Context, p syncing.Principal, plan, id, section string, after int64, limit int) (training.DetailPage, error) {
	out := training.DetailPage{Section: section, Items: []json.RawMessage{}}
	if e := trainingPageQuery(plan, after, limit); e != nil {
		return out, e
	}
	expressions := map[string]string{"effective_sessions": "e.effective->'sessions'", "base_sessions": "b.base->'sessions'", "context_workouts": "r.context->'workouts'", "context_signals": "r.context->'signals'", "context_feedback": "r.context->'feedback'"}
	expr, ok := expressions[section]
	if !ok {
		return out, syncing.Fail(422, "invalid_training_query")
	}
	v, e := s.trainingSummary(ctx, p, plan, id)
	if e != nil {
		return out, e
	}
	out.Version = v
	const source = ` FROM training_effective_versions e JOIN training_base_versions b ON b.dataset_id=e.dataset_id AND b.plan_id=e.plan_id AND b.id=e.base_id JOIN training_generation_runs r ON r.dataset_id=b.dataset_id AND r.id=b.run_id `
	if e = s.Pool.QueryRow(ctx, `SELECT jsonb_build_object('effective',e.effective-'sessions','base',b.base-'sessions','context',r.context-ARRAY['workouts','signals','feedback'])`+source+`WHERE e.dataset_id=$1 AND e.plan_id=$2 AND e.id=$3`, p.DatasetID, plan, id).Scan(&out.Metadata); e != nil {
		return out, trainingError(e)
	}
	rows, e := s.Pool.Query(ctx, `SELECT x.value,x.ordinality`+source+`CROSS JOIN LATERAL jsonb_array_elements(COALESCE(`+expr+`,'[]'::jsonb)) WITH ORDINALITY x WHERE e.dataset_id=$1 AND e.plan_id=$2 AND e.id=$3 AND x.ordinality>$4 ORDER BY x.ordinality LIMIT $5`, p.DatasetID, plan, id, after, limit+1)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	last := after
	for rows.Next() {
		var item json.RawMessage
		var ordinal int64
		if e = rows.Scan(&item, &ordinal); e != nil {
			return out, e
		}
		if len(out.Items) == limit {
			out.Next = last
			break
		}
		out.Items = append(out.Items, item)
		if len(trainingJSON(out))+32 > training.PageBudget {
			out.Items = out.Items[:len(out.Items)-1]
			if len(out.Items) == 0 {
				return out, syncing.Fail(413, "training_detail_entry_limit")
			}
			out.Next = last
			break
		}
		last = ordinal
	}
	return out, rows.Err()
}
