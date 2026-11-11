package postgres

import (
	"context"
	"errors"
	"time"

	"rai/server/internal/syncing"
)

// Admit spends one slot from the device bucket and one from the dataset
// bucket, both in memory, atomically: a denial spends nothing in either. The
// device bucket is checked first, so one phone over its own budget reports
// device_rate_exceeded instead of draining the shared dataset budget. Requests
// do not write to PostgreSQL just to be admitted; the upload concurrency slot
// still protects memory, and a restart only refills the buckets.
func (s *Store) Admit(_ context.Context, p syncing.Principal, lane syncing.AdmissionLane) error {
	policy, ok := syncing.RequestBudgets()[lane]
	if !ok {
		return errors.New("unknown admission lane")
	}
	denied, wait, allowed := s.limiter.AllowAll(time.Now(),
		syncing.Bucket{Key: "v:" + p.DeviceID + ":" + string(lane), Budget: policy.Device},
		syncing.Bucket{Key: "d:" + p.DatasetID + ":" + string(lane), Budget: policy.Dataset})
	if allowed {
		return nil
	}
	code := "device_rate_exceeded"
	if denied == 1 {
		code = "dataset_rate_exceeded"
	}
	return &syncing.Error{Status: 429, Code: code, RetryAfterSeconds: retrySeconds(wait)}
}

func retrySeconds(wait time.Duration) int {
	return max(1, int((wait+time.Second-1)/time.Second))
}
