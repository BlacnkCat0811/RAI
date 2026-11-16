package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"rai/server/internal/storage/postgres"
)

// autoActivationActor is recorded in the assignment audit for activations
// performed by the deployment step rather than by a named person.
const autoActivationActor = "auto-deploy"

type autoActivator interface {
	AutoActivationDatasets(context.Context) ([]string, error)
	AutoActivateModelStack(context.Context, string, string) (postgres.AutoActivation, error)
}

type autoActivationResult struct {
	Dataset string `json:"datasetHash"`
	postgres.AutoActivation
	Error string `json:"error,omitempty"`
}

// autoActivate runs after a deployment with the owner database role. It
// registers the deployed stack as candidate for every dataset, waits for the
// Worker to publish today's release and recompute up to 365 days of history,
// then promotes it. Datasets that already
// run the deployed stack are left alone, so it is safe to run on every
// deployment. It fails (non-zero exit) when a dataset could not be activated
// within the timeout; the old stack then stays active and readable.
func autoActivate(ctx context.Context, s autoActivator, timeout, interval time.Duration, out io.Writer) error {
	datasets, err := s.AutoActivationDatasets(ctx)
	if err != nil {
		return errors.New("auto-activate dataset listing failed")
	}
	results := make([]autoActivationResult, len(datasets))
	for i, id := range datasets {
		h := sha256.Sum256([]byte(id))
		results[i].Dataset = hex.EncodeToString(h[:8])
	}
	deadline := time.Now().Add(timeout)
	for {
		waiting := false
		for i, id := range datasets {
			if done(results[i].Status) {
				continue
			}
			r, e := s.AutoActivateModelStack(ctx, id, autoActivationActor)
			results[i].AutoActivation, results[i].Error = r, ""
			if e != nil {
				results[i].Error = "activation_refused"
				waiting = true
				continue
			}
			if !done(r.Status) {
				waiting = true
			}
		}
		if !waiting || !time.Now().Before(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(results); err != nil {
		return err
	}
	for _, r := range results {
		if r.Status != postgres.AutoActivationActive && r.Status != postgres.AutoActivationActivated {
			return errors.New("auto-activate incomplete: previous model stack stays active")
		}
	}
	return nil
}

// done reports a final outcome for this run: activated, nothing to do, or a
// state that waiting cannot change.
func done(status string) bool {
	switch status {
	case postgres.AutoActivationActive, postgres.AutoActivationActivated, postgres.AutoActivationFailed,
		postgres.AutoActivationRollbackRequired, postgres.AutoActivationBackfillPaused:
		return true
	}
	return false
}
