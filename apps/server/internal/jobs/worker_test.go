package jobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"rai/server/internal/analysis"
	"sync"
	"testing"
	"time"
)

type canceledRecovery struct {
	Queue
	called   chan struct{}
	advanced chan time.Time
}

func (r canceledRecovery) AdvanceAnalysisDay(_ context.Context, at time.Time) error {
	if r.advanced != nil {
		select {
		case r.advanced <- at:
		default:
		}
	}
	return nil
}
func (r canceledRecovery) Claim(ctx context.Context, _ []string) (analysis.Lease, error) {
	return analysis.Lease{}, ctx.Err()
}
func (r canceledRecovery) RequeueExpired(ctx context.Context) (int, error) {
	close(r.called)
	<-ctx.Done()
	return 0, ctx.Err()
}

func TestWorkerStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	called := make(chan struct{})
	advanced := make(chan time.Time, 1)
	asOf := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, canceledRecovery{called: called, advanced: advanced}, slog.New(slog.NewJSONHandler(io.Discard, nil)), asOf)
	}()
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
	select {
	case got := <-advanced:
		if !got.Equal(asOf) {
			t.Fatalf("advance used %v, want %v", got, asOf)
		}
	default:
		t.Fatal("day advancement did not receive the run as-of instant")
	}
}

func runWorkerOnce(t *testing.T, asOf time.Time) time.Time {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	called := make(chan struct{})
	advanced := make(chan time.Time, 1)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, canceledRecovery{called: called, advanced: advanced}, slog.New(slog.NewJSONHandler(io.Discard, nil)), asOf)
	}()
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
	select {
	case got := <-advanced:
		return got
	default:
		t.Fatal("day advancement did not receive the run as-of instant")
	}
	return time.Time{}
}

// TestWorkerUsesExactExplicitAsOf proves each run hands its caller-supplied
// instant to AdvanceAnalysisDay unchanged and never substitutes the wall clock:
// two runs with different instants must observe exactly those instants.
func TestWorkerUsesExactExplicitAsOf(t *testing.T) {
	first := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	second := first.Add(72 * time.Hour)
	for _, want := range []time.Time{first, second} {
		if got := runWorkerOnce(t, want); !got.Equal(want) {
			t.Fatalf("day advancement got %v, want explicit as-of %v", got, want)
		}
	}
}

// TestRunWithClockAdvancesWithTheClock proves a long-lived worker re-reads the
// clock: after the fake clock crosses midnight the next day advancement must see
// the later instant instead of the one captured at startup.
func TestRunWithClockAdvancesWithTheClock(t *testing.T) {
	previous := recoveryInterval
	recoveryInterval = 20 * time.Millisecond
	defer func() { recoveryInterval = previous }()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	called := make(chan struct{})
	advanced := make(chan time.Time, 8)
	start := time.Date(2026, 11, 10, 23, 59, 0, 0, time.UTC)
	var mu sync.Mutex
	current := start
	now := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return current
	}
	q := &tickingRecovery{advanced: advanced, called: called}
	done := make(chan error, 1)
	go func() {
		done <- RunWithClock(ctx, q, slog.New(slog.NewJSONHandler(io.Discard, nil)), now)
	}()
	select {
	case got := <-advanced:
		if !got.Equal(start) {
			t.Fatalf("first advance used %v, want %v", got, start)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("first day advancement did not happen")
	}
	mu.Lock()
	current = start.Add(2 * time.Minute)
	mu.Unlock()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case got := <-advanced:
			if got.Equal(start.Add(2 * time.Minute)) {
				goto advancedPastMidnight
			}
		case <-deadline:
			t.Fatal("day advancement never observed the moved clock")
		}
	}
advancedPastMidnight:
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker did not stop")
	}
}

type tickingRecovery struct {
	Queue
	called   chan struct{}
	advanced chan time.Time
}

func (r *tickingRecovery) AdvanceAnalysisDay(_ context.Context, at time.Time) error {
	select {
	case r.advanced <- at:
	default:
	}
	return nil
}
func (r *tickingRecovery) Claim(ctx context.Context, _ []string) (analysis.Lease, error) {
	return analysis.Lease{}, errors.New("empty")
}
func (r *tickingRecovery) RequeueExpired(context.Context) (int, error) { return 0, nil }

type renewalQueue struct {
	Queue
	renewed   chan struct{}
	lose      bool
	published bool
}

func (q *renewalQueue) Renew(ctx context.Context, _ analysis.Lease) error {
	select {
	case <-q.renewed:
	default:
		close(q.renewed)
	}
	if q.lose {
		return context.Canceled
	}
	return nil
}
func (q *renewalQueue) LoadAnalysis(ctx context.Context, _ analysis.Lease, _ time.Time) (analysis.Snapshot, error) {
	select {
	case <-ctx.Done():
		return analysis.Snapshot{}, ctx.Err()
	case <-q.renewed:
	}
	if q.lose {
		<-ctx.Done()
		return analysis.Snapshot{}, ctx.Err()
	}
	return analysis.Snapshot{Generation: 1, InputVersion: "synthetic", Workout: &analysis.WorkoutInput{Deleted: true}}, nil
}
func (q *renewalQueue) StartAnalysisRun(context.Context, analysis.Lease, analysis.Snapshot) (string, error) {
	return "synthetic-run", nil
}
func (q *renewalQueue) PublishAnalysis(context.Context, analysis.Lease, string, analysis.Snapshot, []analysis.Metric) (string, error) {
	q.published = true
	return "SUCCEEDED", nil
}
func (q *renewalQueue) FailAnalysis(context.Context, analysis.Lease, string, string, bool) error {
	return nil
}
func TestRenewalAndLostLeaseCancellation(t *testing.T) {
	asOf := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	for _, lost := range []bool{false, true} {
		q := &renewalQueue{renewed: make(chan struct{}), lose: lost}
		ctx, c := context.WithTimeout(context.Background(), time.Second)
		processWithRenewal(ctx, q, analysis.Lease{Kind: "analysis.workout", TargetGeneration: 1}, slog.New(slog.NewJSONHandler(io.Discard, nil)), time.Millisecond, asOf)
		c()
		if q.published == lost {
			t.Fatal("renewal/lost lease publication mismatch")
		}
	}
}
