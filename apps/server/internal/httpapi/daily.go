package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"rai/server/internal/syncing"
)

func (a *API) dailyUpload(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
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
	raw, err := readJSONBytes(r, syncing.MaxDailyBytes, "batch_too_large")
	if err != nil {
		return err
	}
	receipt, err := a.store.CommitDaily(r.Context(), p, raw, lease)
	if err != nil {
		return dailyAPIError(err)
	}
	respond(w, 200, json.RawMessage(receipt))
	return nil
}
func (a *API) dailyCoverage(w http.ResponseWriter, r *http.Request, p syncing.Principal) error {
	c, err := a.store.DailyCoverage(r.Context(), p)
	if err != nil {
		return dailyAPIError(err)
	}
	respond(w, 200, map[string]any{"version": 1, "coverage": c})
	return nil
}

func dailyAPIError(err error) error {
	var expected *syncing.Error
	if errors.As(err, &expected) {
		return err
	}
	return syncing.Fail(500, "internal_error")
}
