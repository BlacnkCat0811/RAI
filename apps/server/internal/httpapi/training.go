package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"rai/server/internal/syncing"
	"rai/server/internal/training"
)

type trainingStore interface {
	CreateTrainingPlan(context.Context, syncing.Principal, training.CreateRequest) (training.Plan, error)
	ChangeTrainingPlan(context.Context, syncing.Principal, string, training.ChangeRequest) (training.Plan, error)
	TrainingPlan(context.Context, syncing.Principal, string) (training.Plan, error)
	PutTrainingOverride(context.Context, syncing.Principal, string, training.OverrideRequest) (training.Effective, error)
	TrainingAudit(context.Context, syncing.Principal, string, string) (training.Audit, error)
	TrainingHistory(context.Context, syncing.Principal, string, int64, int) (training.History, error)
	StartTrainingExecution(context.Context, syncing.Principal, training.StartRequest) (training.Execution, error)
	ChangeTrainingExecution(context.Context, syncing.Principal, string, training.ExecutionChange) (training.Execution, error)
	TrainingExecution(context.Context, syncing.Principal, string) (training.Execution, error)
}

func trainingBody(w http.ResponseWriter, r *http.Request, out any) error {
	if len(r.URL.Query()) > 0 {
		return syncing.Fail(422, "invalid_training_query")
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return syncing.Fail(422, "invalid_training_request")
	}
	if d.Decode(new(any)) != io.EOF {
		return syncing.Fail(422, "invalid_training_request")
	}
	return nil
}
func (a *API) training(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	s, ok := a.store.(trainingStore)
	if !ok {
		return syncing.Fail(503, "training_unavailable")
	}
	var out any
	var err error
	id := r.PathValue("id")
	if r.Method == "GET" {
		if r.Pattern == "GET /v2/training/plans/{id}/history" {
			q := r.URL.Query()
			for k, v := range q {
				if (k != "after" && k != "limit") || len(v) != 1 {
					return syncing.Fail(422, "invalid_training_query")
				}
			}
			after := int64(0)
			limit := 20
			if q.Has("after") {
				after, err = strconv.ParseInt(q.Get("after"), 10, 64)
				if err != nil {
					return syncing.Fail(422, "invalid_training_query")
				}
			}
			if q.Has("limit") {
				limit, err = strconv.Atoi(q.Get("limit"))
				if err != nil {
					return syncing.Fail(422, "invalid_training_query")
				}
			}
			out, err = s.TrainingHistory(r.Context(), p, id, after, limit)
		} else {
			if len(r.URL.Query()) > 0 {
				return syncing.Fail(422, "invalid_training_query")
			}
			switch r.Pattern {
			case "GET /v2/training/plans/current":
				out, err = s.TrainingPlan(r.Context(), p, "")
			case "GET /v2/training/plans/{id}":
				out, err = s.TrainingPlan(r.Context(), p, id)
			case "GET /v2/training/plans/{id}/effective":
				var plan training.Plan
				plan, err = s.TrainingPlan(r.Context(), p, id)
				if err == nil && plan.Effective == nil {
					return syncing.Fail(409, "training_not_generated")
				}
				out = plan.Effective
			case "GET /v2/training/plans/{id}/effective/{version}":
				out, err = s.TrainingAudit(r.Context(), p, id, r.PathValue("version"))
			case "GET /v2/training/executions/{id}":
				out, err = s.TrainingExecution(r.Context(), p, id)
			}
		}
	} else {
		switch r.Pattern {
		case "POST /v2/training/plans":
			var req training.CreateRequest
			if err = trainingBody(w, r, &req); err == nil {
				out, err = s.CreateTrainingPlan(r.Context(), p, req)
			}
		case "POST /v2/training/plans/{id}/changes":
			var req training.ChangeRequest
			if err = trainingBody(w, r, &req); err == nil {
				out, err = s.ChangeTrainingPlan(r.Context(), p, id, req)
			}
		case "POST /v2/training/plans/{id}/overrides":
			var req training.OverrideRequest
			if err = trainingBody(w, r, &req); err == nil {
				out, err = s.PutTrainingOverride(r.Context(), p, id, req)
			}
		case "POST /v2/training/executions":
			var req training.StartRequest
			if err = trainingBody(w, r, &req); err == nil {
				out, err = s.StartTrainingExecution(r.Context(), p, req)
			}
		case "POST /v2/training/executions/{id}/changes":
			var req training.ExecutionChange
			if err = trainingBody(w, r, &req); err == nil {
				out, err = s.ChangeTrainingExecution(r.Context(), p, id, req)
			}
		}
	}
	if err != nil {
		return err
	}
	respond(w, 200, out)
	return nil
}
