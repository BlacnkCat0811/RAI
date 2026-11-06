package syncing

import (
	"context"
	"time"
)

type phaseObserverKey struct{}

// PhaseSemanticValidation names the single semantic validation pass. It runs
// inside the Store import entry point, so an HTTP handler and a direct Store
// caller both execute exactly one pass, and the HTTP boundary reports that same
// duration through ObservePhases instead of repeating the work.
const PhaseSemanticValidation = "semantic_validation"

// SemanticValidationBudget bounds that one pass. Exceeding it fails the upload
// instead of committing a bundle that was only partly examined.
const SemanticValidationBudget = 5 * time.Second

type phaseBudgetKey struct{}

// WithPhaseBudget declares a caller-side latency budget for one named phase. The
// Store import path runs the semantic validation pass itself, so a caller that
// owes its own client a latency bound (the HTTP boundary) declares it here
// instead of repeating the work to measure it. Callers that declare nothing keep
// the pre-existing behaviour and never fail on validation duration.
func WithPhaseBudget(ctx context.Context, name string, budget time.Duration) context.Context {
	budgets, _ := ctx.Value(phaseBudgetKey{}).(map[string]time.Duration)
	next := make(map[string]time.Duration, len(budgets)+1)
	for key, value := range budgets {
		next[key] = value
	}
	next[name] = budget
	return context.WithValue(ctx, phaseBudgetKey{}, next)
}

// PhaseBudget reports a budget declared with WithPhaseBudget.
func PhaseBudget(ctx context.Context, name string) (time.Duration, bool) {
	budgets, _ := ctx.Value(phaseBudgetKey{}).(map[string]time.Duration)
	budget, ok := budgets[name]
	return budget, ok
}

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
