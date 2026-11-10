package postgres

import (
	"context"
	"errors"
	"time"

	"rai/server/internal/syncing"
)

// Admit spends one slot from the dataset bucket and one from the device bucket,
// both in memory. A denial spends nothing. Requests no longer write to
// PostgreSQL just to be admitted; the upload concurrency slot still protects
// memory, and a restart only refills the buckets.
func (s *Store) Admit(_ context.Context, p syncing.Principal, lane syncing.AdmissionLane) error {
	policy, ok := syncing.RequestBudgets()[lane]
	if !ok {
		return errors.New("unknown admission lane")
	}
	now := time.Now()
	if wait, allowed := s.limiter.Allow("d:"+p.DatasetID+":"+string(lane), policy.Dataset, now); !allowed {
		return &syncing.Error{Status: 429, Code: "dataset_rate_exceeded", RetryAfterSeconds: retrySeconds(wait)}
	}
	if wait, allowed := s.limiter.Allow("v:"+p.DeviceID+":"+string(lane), policy.Device, now); !allowed {
		return &syncing.Error{Status: 429, Code: "device_rate_exceeded", RetryAfterSeconds: retrySeconds(wait)}
	}
	return nil
}

func retrySeconds(wait time.Duration) int {
	return max(1, int((wait+time.Second-1)/time.Second))
}
