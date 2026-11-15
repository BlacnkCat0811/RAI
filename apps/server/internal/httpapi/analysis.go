package httpapi

import (
	"context"
	"net/http"

	"rai/server/internal/analysis"
	"rai/server/internal/hrref"
	"rai/server/internal/modelinput"
	"rai/server/internal/syncing"
	ve "rai/server/internal/vo2maxexternal"
)

type analysisParametersReader interface {
	LatestAnalysisParameters(context.Context, syncing.Principal) (*analysis.Parameters, error)
}

type hrReferenceReader interface {
	CurrentHRReference(context.Context, syncing.Principal) (*modelinput.HRReference, error)
	ObservedMaxHR(context.Context, syncing.Principal) (*modelinput.ObservedMaxHR, error)
}

type hrSettingsReader interface {
	HRSettings(context.Context, syncing.Principal) (hrref.SettingsResponse, error)
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
	out := map[string]any{"parameters": v}
	if hr, ok := a.store.(hrReferenceReader); ok {
		ref, err := hr.CurrentHRReference(r.Context(), p)
		if err != nil {
			return err
		}
		out["hrReference"] = ref
		observed, err := hr.ObservedMaxHR(r.Context(), p)
		if err != nil {
			return err
		}
		out["observedMaxHR"] = observed
	}
	if reader, ok := a.store.(hrSettingsReader); ok {
		settings, err := reader.HRSettings(r.Context(), p)
		if err != nil {
			return err
		}
		out["savedHRSettings"] = settings.Saved
		out["resolvedHRReference"] = settings.Resolved
	}
	respond(w, 200, out)
	return nil
}

type vo2SourceReader interface {
	VO2Sources(context.Context, syncing.Principal) ([]ve.Source, error)
}

// vo2Sources lists the Apple VO2 sources the user can select.
func (a *API) vo2Sources(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	if len(r.URL.Query()) > 0 {
		return syncing.Fail(422, "invalid_analysis_query")
	}
	store, ok := a.store.(vo2SourceReader)
	if !ok {
		return syncing.Fail(503, "analysis_unavailable")
	}
	v, err := store.VO2Sources(r.Context(), p)
	if err != nil {
		return err
	}
	respond(w, 200, map[string]any{"sources": v})
	return nil
}
