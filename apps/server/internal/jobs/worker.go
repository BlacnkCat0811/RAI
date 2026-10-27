package jobs

import (
	"context"
	"log/slog"
	"time"
)

type Recovery interface {
	RequeueExpired(context.Context) (int, error)
}

// Run maintains durable leases. No analysis handler is registered until its
// algorithm has passed legacy semantic comparison (P5). Unhandled jobs stay queued.
func Run(ctx context.Context, q Recovery, log *slog.Logger) error {
	log.Info("worker_started", "analysis_handlers", 0, "reason", "analysis_pending_p5")
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		count, err := q.RequeueExpired(ctx)
		if err != nil && ctx.Err() == nil {
			log.Warn("queue_recovery_failed", "reason", "database_unavailable")
		}
		if count > 0 {
			log.Info("leases_recovered", "count", count)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}
