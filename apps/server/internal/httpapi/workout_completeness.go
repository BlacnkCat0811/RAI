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
	receipt, err := a.store.CommitWorkoutCompleteness(r.Context(), p, raw, lease)
	if err != nil {
		return dailyAPIError(err)
	}
	respond(w, 200, json.RawMessage(receipt))
	return nil
}
