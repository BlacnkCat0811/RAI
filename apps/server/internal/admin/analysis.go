package admin

import (
	"context"
	"errors"
	"io"
)

type RecomputeRequest struct {
	Dataset   string
	Scope     string
	Algorithm string
	From      string
	Through   string
	DryRun    bool
}
type RecomputePlan struct {
	Scopes []string `json:"scopes"`
	DryRun bool     `json:"dryRun"`
}

func AnalysisCommand(ctx context.Context, url string, args []string, out io.Writer) error {
	if len(args) > 0 && args[0] == "model-stack" {
		return ModelStackCommand(ctx, url, args[1:], out)
	}
	if len(args) > 0 && args[0] == "backfill" {
		return BackfillCommand(ctx, url, args[1:], out)
	}
	// readiness, recovery, vo2max-external, performance, load, features,
	// recompute and parameters drove the retired legacy pipeline (schema 53).
	return errors.New("usage: rai analysis model-stack|backfill [explicit flags]")
}
