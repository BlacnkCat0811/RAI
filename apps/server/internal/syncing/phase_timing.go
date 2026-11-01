package syncing

import (
	"context"
	"time"
)

type phaseObserverKey struct{}

// ObservePhases carries duration-only telemetry. No payload or identity enters
// the callback. It does not affect transaction/receipt ordering.
func ObservePhases(ctx context.Context, observe func(string, time.Duration)) context.Context {
	return context.WithValue(ctx, phaseObserverKey{}, observe)
}
func PhaseTimer(ctx context.Context, name string) func() {
	start := time.Now()
	return func() {
		if observer, ok := ctx.Value(phaseObserverKey{}).(func(string, time.Duration)); ok {
			observer(name, time.Since(start))
		}
	}
}
