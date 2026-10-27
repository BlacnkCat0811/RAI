package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/storage/postgres/dbgen"
	"rai/server/internal/syncing"
)

// Admit performs at most two indexed bucket writes in a short, separate
// operational transaction, dataset bucket first, then device bucket. A denial
// rolls back both. Business revisions, receipts and job state are untouched;
// the admission transaction finishes before parsing or business work. Foreign
// key checks may briefly take key-share locks on the enrolled identities.
// All replicas use PostgreSQL time and the same durable state. A failed request
// still consumes its admitted slot, preventing invalid-body/refund bypasses.
func (s *Store) Admit(ctx context.Context, p syncing.Principal, lane syncing.AdmissionLane) error {
	policy, ok := syncing.RequestBudgets()[lane]
	if !ok {
		return errors.New("unknown admission lane")
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := dbgen.New(tx)
	stamp, err := q.BudgetClock(ctx)
	if err != nil {
		return err
	}
	for _, bucket := range []struct {
		device *string
		budget syncing.RequestBudget
		code   string
	}{
		{nil, policy.Dataset, "dataset_rate_exceeded"},
		{&p.DeviceID, policy.Device, "device_rate_exceeded"},
	} {
		interval := time.Minute / time.Duration(bucket.budget.PerMinute)
		_, err = q.TryRequestBudget(ctx, dbgen.TryRequestBudgetParams{
			DatasetID: p.DatasetID, DeviceID: bucket.device, Lane: string(lane),
			AdmittedAt: stamp, IntervalUs: interval.Microseconds(), Burst: int32(bucket.budget.Burst),
		})
		if errors.Is(err, pgx.ErrNoRows) {
			at, readErr := q.RequestBudgetTime(ctx, dbgen.RequestBudgetTimeParams{DatasetID: p.DatasetID, DeviceID: bucket.device, Lane: string(lane)})
			if readErr != nil {
				return readErr
			}
			wait := at.Time.Sub(stamp.Time) - time.Duration(bucket.budget.Burst-1)*interval
			seconds := max(1, int((wait+time.Second-1)/time.Second))
			return &syncing.Error{Status: 429, Code: bucket.code, RetryAfterSeconds: seconds}
		}
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
