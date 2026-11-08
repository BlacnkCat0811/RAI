package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"rai/server/internal/modelinput"
	"rai/server/internal/syncing"
)

type modelInputWriter interface {
	CommitModelInput(ctx context.Context, p syncing.Principal, req modelinput.Request) (modelinput.Receipt, error)
}

type modelInputRevoker interface {
	RevokeModelInput(ctx context.Context, p syncing.Principal, inputID, reason string) (modelinput.Receipt, error)
}

func inputKind(raw string) string {
	switch raw {
	case "hr-references":
		return "hr_reference"
	case "sleep-targets":
		return "sleep_target"
	case "workout-efforts":
		return "workout_effort"
	case "workout-overlap-resolutions":
		return "workout_overlap_resolution"
	case "source-context-selections":
		return "source_context_selection"
	case "vo2max-source-selections":
		return "vo2max_source_selection"
	default:
		return raw
	}
}

func (a *API) modelInput(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	s, ok := a.store.(modelInputWriter)
	if !ok {
		return syncing.Fail(503, "model_input_unavailable")
	}
	if r.Method != http.MethodPost || r.URL.RawQuery != "" {
		return syncing.Fail(422, "invalid_model_input")
	}
	kind := inputKind(r.PathValue("kind"))
	if kind == "" || r.PathValue("id") != "" {
		return syncing.Fail(404, "not_found")
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 65536))
	if err != nil {
		return syncing.Fail(413, "model_input_too_large")
	}
	var req modelinput.Request
	if err = syncing.DecodeStrictJSON(body, &req); err != nil || req.Kind != kind {
		return syncing.Fail(422, "invalid_model_input")
	}
	receipt, err := s.CommitModelInput(r.Context(), p, req)
	switch {
	case errors.Is(err, modelinput.ErrInvalid):
		return syncing.Fail(422, "invalid_model_input")
	case errors.Is(err, modelinput.ErrConflict):
		return syncing.Fail(409, "model_input_conflict")
	case errors.Is(err, modelinput.ErrNotFound):
		return syncing.Fail(404, "model_input_subject_not_found")
	case err != nil:
		return syncing.Fail(503, "model_input_unavailable")
	}
	respond(w, 200, receipt)
	return nil
}

func (a *API) modelInputRevocation(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	s, ok := a.store.(modelInputRevoker)
	if !ok {
		return syncing.Fail(503, "model_input_unavailable")
	}
	if r.Method != http.MethodPost || r.URL.RawQuery != "" {
		return syncing.Fail(422, "invalid_model_input")
	}
	id := r.PathValue("id")
	if id == "" {
		return syncing.Fail(404, "not_found")
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil {
		return syncing.Fail(413, "model_input_too_large")
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if len(body) > 0 {
		if err = syncing.DecodeStrictJSON(body, &req); err != nil {
			return syncing.Fail(422, "invalid_model_input")
		}
	}
	receipt, err := s.RevokeModelInput(r.Context(), p, id, req.Reason)
	switch {
	case errors.Is(err, modelinput.ErrInvalid):
		return syncing.Fail(422, "invalid_model_input")
	case errors.Is(err, modelinput.ErrConflict):
		return syncing.Fail(409, "model_input_conflict")
	case errors.Is(err, modelinput.ErrNotFound):
		return syncing.Fail(404, "model_input_not_found")
	case err != nil:
		return syncing.Fail(503, "model_input_unavailable")
	}
	respond(w, 200, receipt)
	return nil
}
