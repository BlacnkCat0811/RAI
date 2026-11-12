package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	am "rai/server/internal/athletemodel"
	rf "rai/server/internal/runfitness"
	"rai/server/internal/syncing"
)

type runningFitnessStore interface {
	ReadAthleteProductV4(context.Context, syncing.Principal, time.Time) (am.AthleteModelSnapshot, error)
	ReadRunningFitnessHistory(context.Context, syncing.Principal, string, string, string, string, int) (am.RunningFitnessHistory, error)
	ReadRunningFitnessEvidence(context.Context, syncing.Principal, string, string) (am.RunningFitnessEvidence, error)
	RequestRunFitnessReanalysis(context.Context, syncing.Principal, string, am.ReanalysisRequest) (am.ReanalysisOperation, error)
	ReadRunFitnessReanalysis(context.Context, syncing.Principal, string) (am.ReanalysisOperation, error)
}

func runningFitnessError(err error) error {
	switch {
	case errors.Is(err, am.ErrNotActivated):
		return syncing.Fail(409, "not_product_activated")
	case errors.Is(err, am.ErrUnsupportedRelease):
		return syncing.Fail(409, "model_version_unsupported")
	case errors.Is(err, am.ErrHistoricalInputUnavailable):
		return syncing.Fail(409, "historical_input_unavailable")
	case errors.Is(err, am.ErrHistoryCursor):
		return syncing.Fail(409, "history_cursor_mismatch")
	case errors.Is(err, am.ErrReanalysisConflict):
		return syncing.Fail(409, "idempotency_conflict")
	case errors.Is(err, am.ErrReanalysisInvalid):
		return syncing.Fail(422, "invalid_reanalysis_request")
	case errors.Is(err, am.ErrRunningFitnessNotFound):
		return syncing.Fail(404, "not_found")
	}
	return syncing.Fail(503, "running_fitness_unavailable")
}

func (a *API) runningFitness(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	s, ok := a.store.(runningFitnessStore)
	if !ok {
		return syncing.Fail(503, "running_fitness_unavailable")
	}
	q := r.URL.Query()
	allowed := map[string]map[string]bool{
		"GET /v2/analysis/running-fitness/current":                  {},
		"GET /v2/analysis/running-fitness/history":                  {"from": true, "to": true, "view": true, "cursor": true, "limit": true},
		"GET /v2/analysis/running-fitness/evidence":                 {"date": true, "dimension": true},
		"POST /v2/analysis/running-fitness/reanalyses":              {},
		"GET /v2/analysis/running-fitness/reanalyses/{operationID}": {},
	}[r.Pattern]
	for k, v := range q {
		if !allowed[k] || len(v) != 1 {
			return syncing.Fail(422, "invalid_running_fitness_query")
		}
	}
	for _, key := range []string{"from", "to", "date"} {
		if d := q.Get(key); d != "" {
			if _, err := time.Parse(time.DateOnly, d); err != nil {
				return syncing.Fail(422, "invalid_date")
			}
		}
	}
	switch r.Pattern {
	case "GET /v2/analysis/running-fitness/current":
		snapshot, err := s.ReadAthleteProductV4(r.Context(), p, time.Now().UTC())
		if err != nil {
			return runningFitnessError(err)
		}
		if snapshot.RunningFitness == nil {
			return syncing.Fail(409, "model_version_unsupported")
		}
		respond(w, 200, struct {
			SchemaVersion string                            `json:"schemaVersion"`
			Assignment    am.Assignment                     `json:"assignment"`
			Metric        am.Metric[am.RunningFitnessValue] `json:"runningFitness"`
		}{rf.Schema, snapshot.Assignment, *snapshot.RunningFitness})
	case "GET /v2/analysis/running-fitness/history":
		view := q.Get("view")
		if view == "" {
			view = "revised"
		}
		if view != "revised" && view != "published" && view != "as_known" {
			return syncing.Fail(422, "invalid_view")
		}
		limit := 30
		if q.Has("limit") {
			n, err := strconv.Atoi(q.Get("limit"))
			if err != nil || n < 1 || n > 90 {
				return syncing.Fail(422, "invalid_limit")
			}
			limit = n
		}
		page, err := s.ReadRunningFitnessHistory(r.Context(), p, q.Get("from"), q.Get("to"), view, q.Get("cursor"), limit)
		if err != nil {
			return runningFitnessError(err)
		}
		respond(w, 200, page)
	case "GET /v2/analysis/running-fitness/evidence":
		dim := q.Get("dimension")
		if dim != "" && dim != "endurance" && dim != "threshold" && dim != "speed" && dim != "sprint" {
			return syncing.Fail(422, "invalid_dimension")
		}
		out, err := s.ReadRunningFitnessEvidence(r.Context(), p, q.Get("date"), dim)
		if err != nil {
			return runningFitnessError(err)
		}
		respond(w, 200, out)
	case "POST /v2/analysis/running-fitness/reanalyses":
		key := r.Header.Get("Idempotency-Key")
		if key == "" || len(key) > 128 {
			return syncing.Fail(422, "idempotency_key_required")
		}
		b, err := readJSONBytes(r, 16<<10, "reanalysis_request_too_large")
		if err != nil {
			return err
		}
		var req am.ReanalysisRequest
		if syncing.DecodeStrictJSON(b, &req) != nil {
			return syncing.Fail(422, "invalid_reanalysis_request")
		}
		op, err := s.RequestRunFitnessReanalysis(r.Context(), p, key, req)
		if err != nil {
			return runningFitnessError(err)
		}
		// 202 only means the bounded recomputation is queued.
		respond(w, 202, op)
	case "GET /v2/analysis/running-fitness/reanalyses/{operationID}":
		op, err := s.ReadRunFitnessReanalysis(r.Context(), p, r.PathValue("operationID"))
		if err != nil {
			return runningFitnessError(err)
		}
		respond(w, 200, op)
	default:
		return syncing.Fail(404, "not_found")
	}
	return nil
}
