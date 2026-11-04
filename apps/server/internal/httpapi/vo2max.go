package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"rai/server/internal/syncing"
)

func (a *API) vo2Upload(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
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
	raw, err := readJSONBytes(r, syncing.MaxVO2Bytes, "batch_too_large")
	if err != nil {
		return err
	}
	receipt, err := a.store.CommitVO2(r.Context(), p, raw, lease)
	if err != nil {
		return dailyAPIError(err)
	}
	respond(w, 200, json.RawMessage(receipt))
	return nil
}
func (a *API) vo2Coverage(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	c, err := a.store.VO2Coverage(r.Context(), p)
	if err != nil {
		return dailyAPIError(err)
	}
	respond(w, 200, c)
	return nil
}
