package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"rai/server/internal/syncing"
	"time"
)

func (a *API) workoutCompleteness(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	release, err := a.acquireUpload(p)
	if err != nil {
		return err
	}
	defer release()
	lease, err := a.store.AcquireUpload(r.Context(), p)
	if err != nil {
		return dailyAPIError(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = a.store.ReleaseUpload(ctx, p, lease)
	}()
	raw, err := readJSONBytes(r, syncing.MaxCompletenessBytes, "batch_too_large")
	if err != nil {
		return err
	}
	var envelope struct {
		Contract string `json:"contract"`
	}
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return syncing.Fail(422, "invalid_payload")
	}
	var receipt json.RawMessage
	switch envelope.Contract {
	case syncing.WorkoutCompletenessContract:
		receipt, err = a.store.CommitWorkoutCompleteness(r.Context(), p, raw, lease)
	case syncing.WorkoutCompletenessV2Contract:
		receipt, err = a.store.CommitWorkoutCompletenessV2(r.Context(), p, raw, lease)
	default:
		return syncing.Fail(422, "unsupported_version")
	}
	if err != nil {
		return dailyAPIError(err)
	}
	respond(w, 200, json.RawMessage(receipt))
	return nil
}
