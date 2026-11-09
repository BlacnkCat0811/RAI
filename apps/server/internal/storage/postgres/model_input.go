package postgres

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"rai/server/internal/modelinput"
	"rai/server/internal/syncing"
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
