package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/syncing"
	"rai/server/internal/training"
)

func loadExecution(ctx context.Context, q trainingDB, dataset, id string) (training.Execution, error) {
	var e training.Execution
	var b []byte
	err := q.QueryRow(ctx, `SELECT id::text,plan_id::text,effective_id::text,session_id::text,revision,status,snapshot,started_at,completed_at,COALESCE(workout_id::text,''),notes FROM training_executions WHERE dataset_id=$1 AND id=$2`, dataset, id).Scan(&e.ID, &e.PlanID, &e.EffectiveID, &e.SessionID, &e.Revision, &e.Status, &b, &e.StartedAt, &e.CompletedAt, &e.WorkoutID, &e.Notes)
	if err != nil {
		return e, trainingError(err)
	}
	if err = json.Unmarshal(b, &e.Snapshot); err != nil {
		return e, err
	}
	rows, err := q.Query(ctx, `SELECT feedback FROM training_feedback WHERE dataset_id=$1 AND execution_id=$2 ORDER BY created_at,id`, dataset, id)
	if err != nil {
		return e, err
	}
	defer rows.Close()
	e.Feedback = []training.Feedback{}
	for rows.Next() {
		var f training.Feedback
		if err = rows.Scan(&b); err != nil {
			return e, err
		}
		if err = json.Unmarshal(b, &f); err != nil {
			return e, err
		}
		e.Feedback = append(e.Feedback, f)
	}
	return e, rows.Err()
}
func (s *Store) TrainingExecution(ctx context.Context, p syncing.Principal, id string) (training.Execution, error) {
	if e := trainingID(id); e != nil {
		return training.Execution{}, e
	}
	return loadExecution(ctx, s.Pool, p.DatasetID, id)
}
func (s *Store) StartTrainingExecution(ctx context.Context, p syncing.Principal, r training.StartRequest) (training.Execution, error) {
	var out training.Execution
	if e := trainingID(r.ExecutionID, r.PlanID, r.EffectiveID, r.SessionID); e != nil {
		return out, e
	}
	e := s.trainingCommand(ctx, p, r.CommandID, "start_execution", r.ExecutionID, r, func(tx pgx.Tx) (any, string, error) {
		v, e := loadPlan(ctx, tx, p.DatasetID, r.PlanID)
		if e != nil {
			return nil, "", e
		}
		if !v.Active {
			return nil, "", syncing.Fail(409, "training_plan_archived")
		}
		if v.Effective == nil || v.Effective.ID != r.EffectiveID {
			return nil, "", syncing.Fail(409, "training_effective_conflict")
		}
		var session *training.Session
		for _, s := range v.Effective.Sessions {
			if s.ID == r.SessionID {
				x := s
				session = &x
			}
		}
		if session == nil {
			return nil, "", syncing.Fail(404, "training_session_not_found")
		}
		if session.Status != "scheduled" || session.Type == "REST" {
			return nil, "", syncing.Fail(409, "training_session_not_executable")
		}
		var exists bool
		if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM training_executions WHERE dataset_id=$1 AND (id=$2 OR (plan_id=$3 AND session_id=$4)))`, p.DatasetID, r.ExecutionID, r.PlanID, r.SessionID).Scan(&exists); e != nil {
			return nil, "", e
		}
		if exists {
			return nil, "", syncing.Fail(409, "training_execution_exists")
		}
		status := "started"
		var at *time.Time
		t := time.Now().UTC()
		at = &t
		if r.Scheduled {
			status = "scheduled"
			at = nil
		}
		if _, e = tx.Exec(ctx, `INSERT INTO training_executions(dataset_id,id,plan_id,effective_id,session_id,status,snapshot,started_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, p.DatasetID, r.ExecutionID, r.PlanID, r.EffectiveID, r.SessionID, status, trainingJSON(session), at); e != nil {
			return nil, "", e
		}
		ex, e := loadExecution(ctx, tx, p.DatasetID, r.ExecutionID)
		return ex, r.PlanID, e
	}, &out)
	return out, e
}
func (s *Store) ChangeTrainingExecution(ctx context.Context, p syncing.Principal, id string, r training.ExecutionChange) (training.Execution, error) {
	var out training.Execution
	if e := trainingID(id); e != nil {
		return out, e
	}
	if len(r.Notes) > 2000 {
		return out, syncing.Fail(422, "invalid_training_notes")
	}
	e := s.trainingCommand(ctx, p, r.CommandID, "execution_change", id, r, func(tx pgx.Tx) (any, string, error) {
		ex, e := loadExecution(ctx, tx, p.DatasetID, id)
		if e != nil {
			return nil, "", e
		}
		if ex.Revision != r.ExpectedRevision {
			return nil, "", syncing.Fail(409, "training_revision_conflict")
		}
		if r.WorkoutID != "" {
			if e = trainingID(r.WorkoutID); e != nil {
				return nil, "", e
			}
			var exists bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2)`, p.DatasetID, r.WorkoutID).Scan(&exists); e != nil {
				return nil, "", e
			}
			if !exists {
				return nil, "", syncing.Fail(409, "training_workout_not_ready")
			}
		}
		now := time.Now().UTC()
		switch r.Operation {
		case "start":
			if ex.Status != "scheduled" || r.WorkoutID != "" || r.Notes != "" {
				return nil, "", syncing.Fail(409, "training_execution_transition")
			}
			ex.Status = "started"
			ex.StartedAt = &now
		case "complete":
			if ex.Status != "started" {
				return nil, "", syncing.Fail(409, "training_execution_transition")
			}
			ex.Status = "completed"
			ex.CompletedAt = &now
			ex.WorkoutID = r.WorkoutID
			ex.Notes = r.Notes
		case "skip":
			if (ex.Status != "scheduled" && ex.Status != "started") || r.WorkoutID != "" {
				return nil, "", syncing.Fail(409, "training_execution_transition")
			}
			ex.Status = "skipped"
			ex.CompletedAt = &now
			ex.Notes = r.Notes
		case "associate":
			if ex.Status != "completed" || ex.WorkoutID != "" || r.WorkoutID == "" || r.Notes != "" {
				return nil, "", syncing.Fail(409, "training_execution_transition")
			}
			ex.WorkoutID = r.WorkoutID
		default:
			return nil, "", syncing.Fail(422, "invalid_training_change")
		}
		ex.Revision++
		if _, e = tx.Exec(ctx, `UPDATE training_executions SET revision=$3,status=$4,started_at=$5,completed_at=$6,workout_id=NULLIF($7,'')::uuid,notes=$8 WHERE dataset_id=$1 AND id=$2`, p.DatasetID, id, ex.Revision, ex.Status, ex.StartedAt, ex.CompletedAt, ex.WorkoutID, ex.Notes); e != nil {
			return nil, "", e
		}
		if ex.Status == "completed" || ex.Status == "skipped" {
			var w *training.Workout
			if ex.WorkoutID != "" {
				plan, e := loadPlan(ctx, tx, p.DatasetID, ex.PlanID)
				if e != nil {
					return nil, "", e
				}
				ws, e := loadTrainingWorkouts(ctx, tx, p.DatasetID, plan.Timezone, "", ex.WorkoutID)
				if e != nil {
					return nil, "", e
				}
				if len(ws) > 0 {
					w = &ws[0]
				}
			}
			if e = writeTrainingFeedback(ctx, tx, p.DatasetID, ex, w); e != nil {
				return nil, "", e
			}
			if _, e = tx.Exec(ctx, `SELECT rai_training_dirty($1,$2)`, p.DatasetID, ex.PlanID); e != nil {
				return nil, "", e
			}
		}
		ex, e = loadExecution(ctx, tx, p.DatasetID, id)
		return ex, ex.PlanID, e
	}, &out)
	return out, e
}
func writeTrainingFeedback(ctx context.Context, tx pgx.Tx, dataset string, e training.Execution, w *training.Workout) error {
	f := training.Compare(e, w)
	f.ID = syncing.NewID()
	_, err := tx.Exec(ctx, `INSERT INTO training_feedback(dataset_id,id,execution_id,execution_revision,source_run_id,feedback) VALUES($1,$2,$3,$4,NULLIF($5,'')::uuid,$6) ON CONFLICT DO NOTHING`, dataset, f.ID, e.ID, e.Revision, f.SourceRunID, trainingJSON(f))
	return err
}
