package postgres

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"rai/server/internal/hrref"
	"rai/server/internal/modelinput"
	"rai/server/internal/syncing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func (s *Store) CommitModelInput(ctx context.Context, p syncing.Principal, req modelinput.Request) (modelinput.Receipt, error) {
	if req.Kind == "revocation" || req.RevokesID != nil {
		return modelinput.Receipt{}, modelinput.ErrInvalid
	}
	c, err := modelinput.Validate(req, time.Now())
	if err != nil {
		return modelinput.Receipt{}, err
	}
	hash, err := hex.DecodeString(c.ContentHash)
	if err != nil {
		return modelinput.Receipt{}, err
	}
	if c.Schema == modelinput.SchemaHRReferenceV3 {
		tx, err := s.Pool.Begin(ctx)
		if err != nil {
			return modelinput.Receipt{}, err
		}
		defer tx.Rollback(ctx)
		var dataset string
		if err = tx.QueryRow(ctx, `SELECT id::text FROM datasets WHERE id=$1 AND retired_at IS NULL AND EXISTS(SELECT 1 FROM devices WHERE dataset_id=$1 AND id=$2 AND revoked_at IS NULL AND can_collect) FOR UPDATE`, p.DatasetID, p.DeviceID).Scan(&dataset); err != nil {
			return modelinput.Receipt{}, modelinput.ErrNotFound
		}
		// Retries return the original receipt even if later inputs changed the settings.
		var oldHash []byte
		var rawReceipt []byte
		err = tx.QueryRow(ctx, `SELECT content_sha256,receipt FROM model_input_receipts WHERE dataset_id=$1 AND input_id=$2`, p.DatasetID, c.ID).Scan(&oldHash, &rawReceipt)
		if err == nil {
			if hex.EncodeToString(oldHash) != c.ContentHash {
				return modelinput.Receipt{}, modelinput.ErrConflict
			}
			var receipt modelinput.Receipt
			if err = json.Unmarshal(rawReceipt, &receipt); err != nil {
				return receipt, err
			}
			return receipt, tx.Commit(ctx)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return modelinput.Receipt{}, err
		}
		now := time.Now()
		inputs, err := stackInputs(ctx, tx, p.DatasetID, now)
		if err != nil {
			return modelinput.Receipt{}, err
		}
		var patch hrref.Patch
		if json.Unmarshal(c.Payload, &patch) != nil {
			return modelinput.Receipt{}, modelinput.ErrInvalid
		}
		order := int64(1)
		for _, x := range inputs {
			order = max(order, x.Order+1)
		}
		candidate := stackInput{ID: c.ID, Kind: c.Kind, Schema: c.Schema, From: c.EffectiveFrom, Known: now, Through: c.EffectiveThrough, Order: order, Payload: c.Payload, Provenance: c.Provenance, Hash: c.ContentHash}
		inputs = append(inputs, candidate)
		// Check every affected boundary, including future settings; never copy prior
		// values into the immutable patch or make retry hashes depend on current state.
		boundaries := []time.Time{c.EffectiveFrom}
		for _, x := range inputs {
			if !x.From.Before(c.EffectiveFrom) {
				boundaries = append(boundaries, x.From)
			}
			if x.Through != nil && !x.Through.Before(c.EffectiveFrom) {
				boundaries = append(boundaries, *x.Through)
			}
		}
		for _, at := range boundaries {
			saved := hrref.Saved(referenceEvents(inputs, at, now), at, now)
			if saved.MaxHR != nil && saved.RestingHR != nil && saved.RestingHR.ManualValue != nil && saved.MaxHR.Value <= saved.RestingHR.ManualValue.Value {
				return modelinput.Receipt{}, modelinput.ErrInvalid
			}
		}
		if err = tx.QueryRow(ctx, `SELECT rai_commit_typed_model_input($1,$2,$3,$4,$5,$6,$7,NULL,$8,$9,$10,$11)`, p.DatasetID, p.DeviceID, c.ID, c.Kind, c.EffectiveFrom, c.EffectiveThrough, c.SupersedesID, c.Schema, hash, c.Payload, c.Provenance).Scan(&rawReceipt); err != nil {
			return modelinput.Receipt{}, err
		}
		var receipt modelinput.Receipt
		if err = json.Unmarshal(rawReceipt, &receipt); err != nil {
			return receipt, err
		}
		return receipt, tx.Commit(ctx)
	}
	return s.writeModelInput(ctx, p, c.ID, c.Kind, c.EffectiveFrom, c.EffectiveThrough, c.SupersedesID, nil,
		c.Schema, hash, c.Payload, c.Provenance)
}

func (s *Store) RevokeModelInput(ctx context.Context, p syncing.Principal, inputID, reason string) (modelinput.Receipt, error) {
	if !syncing.ValidID(inputID) || len(reason) > 200 {
		return modelinput.Receipt{}, modelinput.ErrInvalid
	}
	payload, err := json.Marshal(map[string]string{"reason": reason})
	if err != nil {
		return modelinput.Receipt{}, err
	}
	return s.writeModelInput(ctx, p, syncing.NewID(), "revocation", nil, nil, nil, &inputID, "", nil, payload, "")
}

func (s *Store) writeModelInput(ctx context.Context, p syncing.Principal, id, kind string, from any, through *time.Time, supersedes, revokes *string, schema string, hash, payload []byte, provenance string) (modelinput.Receipt, error) {
	var out modelinput.Receipt
	if !syncing.ValidID(p.DatasetID) || !syncing.ValidID(p.DeviceID) {
		return out, modelinput.ErrNotFound
	}
	var receipt []byte
	err := s.Pool.QueryRow(ctx, `SELECT rai_commit_typed_model_input($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		p.DatasetID, p.DeviceID, id, kind, from, through, supersedes, revokes, schema, hash, payload, provenance).Scan(&receipt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			switch pgErr.Code {
			case "RA080":
				return out, modelinput.ErrNotFound
			case "RA042", "RA043", "RA081", "23505":
				return out, modelinput.ErrConflict
			case "RA082", "23514", "22P02", "22003":
				return out, modelinput.ErrInvalid
			}
		}
		return out, err
	}
	return out, json.Unmarshal(receipt, &out)
}
