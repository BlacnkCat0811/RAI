package jobs

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

type canceledRecovery struct{ called chan struct{} }

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
	go func() { done <- Run(ctx, canceledRecovery{called}, slog.New(slog.NewJSONHandler(io.Discard, nil))) }()
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
