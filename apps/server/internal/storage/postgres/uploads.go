package postgres

import (
	"context"
	"errors"
	"time"

	"rai/server/internal/storage/postgres/dbgen"
	"rai/server/internal/syncing"

	"github.com/jackc/pgx/v5"
)

// AcquireUpload locks only the two operational slots, never a business revision.
// All replicas lock them in slot order. No transaction/connection is held while
// reading the body. A lost acquire/release response costs at most one lease TTL.
func (s *Store) AcquireUpload(ctx context.Context, p syncing.Principal) (syncing.UploadLease, error) {
	if !p.CanCollect {
		return syncing.UploadLease{}, syncing.Fail(403, "read_only_device")
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return syncing.UploadLease{}, err
	}
	defer tx.Rollback(ctx)
	q := dbgen.New(tx)
	if err = q.EnsureUploadSlots(ctx, p.DatasetID); err != nil {
		return syncing.UploadLease{}, err
	}
	slots, err := q.LockUploadSlots(ctx, p.DatasetID)
	if err != nil {
		return syncing.UploadLease{}, err
	}
	stamp, err := q.BudgetClock(ctx)
	if err != nil {
		return syncing.UploadLease{}, err
	}
	candidate := -1
	var earliest time.Time
	for i, slot := range slots {
		active := slot.Token != nil && slot.ExpiresAt.Time.After(stamp.Time)
		if active {
			if slot.DeviceID != nil && *slot.DeviceID == p.DeviceID {
				return syncing.UploadLease{}, uploadCapacity("device_upload_capacity_exceeded", slot.ExpiresAt.Time.Sub(stamp.Time))
			}
			if earliest.IsZero() || slot.ExpiresAt.Time.Before(earliest) {
				earliest = slot.ExpiresAt.Time
			}
		} else if candidate < 0 {
			candidate = i
		}
	}
	if candidate < 0 {
		return syncing.UploadLease{}, uploadCapacity("dataset_upload_capacity_exceeded", earliest.Sub(stamp.Time))
	}
	token := syncing.NewID()
	expires, err := q.AssignUploadSlot(ctx, dbgen.AssignUploadSlotParams{DatasetID: p.DatasetID, Slot: slots[candidate].Slot, DeviceID: &p.DeviceID, Token: &token})
	if err != nil {
		return syncing.UploadLease{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return syncing.UploadLease{}, err
	}
	return syncing.UploadLease{Token: token, ExpiresAt: expires.Time}, nil
}

func uploadCapacity(code string, wait time.Duration) error {
	return &syncing.Error{Status: 429, Code: code, RetryAfterSeconds: max(1, int((wait+time.Second-1)/time.Second))}
}

// Release is idempotent and fenced: a late release cannot clear a replacement.
func (s *Store) ReleaseUpload(ctx context.Context, p syncing.Principal, lease syncing.UploadLease) error {
	if !syncing.ValidID(lease.Token) {
		return syncing.Fail(409, "upload_lease_lost")
	}
	return s.q.ReleaseUploadSlot(ctx, dbgen.ReleaseUploadSlotParams{DatasetID: p.DatasetID, DeviceID: &p.DeviceID, Token: &lease.Token})
}

func lockUploadLease(ctx context.Context, tx pgx.Tx, p syncing.Principal, lease syncing.UploadLease) error {
	if !syncing.ValidID(lease.Token) {
		return syncing.Fail(409, "upload_lease_lost")
	}
	_, err := dbgen.New(tx).LockUploadLease(ctx, dbgen.LockUploadLeaseParams{DatasetID: p.DatasetID, DeviceID: &p.DeviceID, Token: &lease.Token})
	if errors.Is(err, pgx.ErrNoRows) {
		return syncing.Fail(409, "upload_lease_lost")
	}
	if err != nil {
		return err
	}
	return checkUploadLease(ctx, tx, p, lease)
}

func checkUploadLease(ctx context.Context, tx pgx.Tx, p syncing.Principal, lease syncing.UploadLease) error {
	valid, err := dbgen.New(tx).UploadLeaseValid(ctx, dbgen.UploadLeaseValidParams{DatasetID: p.DatasetID, DeviceID: &p.DeviceID, Token: &lease.Token})
	if err != nil {
		return err
	}
	if !valid {
		return syncing.Fail(409, "upload_lease_lost")
	}
	return nil
}

// Exact chunk retries are read-only, but must recheck the lease before exposing
// a durable receipt. The caller already holds the slot lock until rollback.
func uploadRetryReceipt(ctx context.Context, tx pgx.Tx, p syncing.Principal, lease syncing.UploadLease, receipt []byte) ([]byte, error) {
	if err := checkUploadLease(ctx, tx, p, lease); err != nil {
		return nil, err
	}
	return receipt, nil
}
