package httpapi

import (
	"context"
	"net/http"

	"rai/server/internal/analysis"
	"rai/server/internal/syncing"
)

type analysisReader interface {
	CurrentWorkoutAnalysis(context.Context, syncing.Principal, string) (analysis.Current, error)
	CurrentTrainingState(context.Context, syncing.Principal) (analysis.Current, error)
}

type analysisParametersReader interface {
	LatestAnalysisParameters(context.Context, syncing.Principal) (*analysis.Parameters, error)
}

func (a *API) analysisParameters(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	if len(r.URL.Query()) > 0 {
		return syncing.Fail(422, "invalid_analysis_query")
	}
	store, ok := a.store.(analysisParametersReader)
	if !ok {
		return syncing.Fail(503, "analysis_unavailable")
	}
	v, err := store.LatestAnalysisParameters(r.Context(), p)
	if err != nil {
		return err
	}
	respond(w, 200, map[string]any{"parameters": v})
	return nil
}

func (a *API) workoutAnalysis(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	if len(r.URL.Query()) > 0 {
		return syncing.Fail(422, "invalid_analysis_query")
	}
	store, ok := a.store.(analysisReader)
	if !ok {
		return syncing.Fail(503, "analysis_unavailable")
	}
	v, err := store.CurrentWorkoutAnalysis(r.Context(), p, r.PathValue("id"))
	if err != nil {
		return err
	}
	respond(w, 200, v)
	return nil
}
func (a *API) trainingState(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	if len(r.URL.Query()) > 0 {
		return syncing.Fail(422, "invalid_analysis_query")
	}
	store, ok := a.store.(analysisReader)
	if !ok {
		return syncing.Fail(503, "analysis_unavailable")
	}
	v, err := store.CurrentTrainingState(r.Context(), p)
	if err != nil {
		return err
	}
	respond(w, 200, v)
	return nil
}
