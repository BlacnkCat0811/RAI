package jobs

import (
	"context"
	"io"
	"log/slog"
	"rai/server/internal/analysis"
	"testing"
	"time"
)

type canceledRecovery struct {
	Queue
	called chan struct{}
}

func (r canceledRecovery) AdvanceAnalysisDay(context.Context, time.Time) error { return nil }
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
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, canceledRecovery{called: called}, slog.New(slog.NewJSONHandler(io.Discard, nil)))
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
}

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
	for _, lost := range []bool{false, true} {
		q := &renewalQueue{renewed: make(chan struct{}), lose: lost}
		ctx, c := context.WithTimeout(context.Background(), time.Second)
		processWithRenewal(ctx, q, analysis.Lease{Kind: "analysis.workout", TargetGeneration: 1}, slog.New(slog.NewJSONHandler(io.Discard, nil)), time.Millisecond)
		c()
		if q.published == lost {
			t.Fatal("renewal/lost lease publication mismatch")
		}
	}
}
