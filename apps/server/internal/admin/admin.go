// Package admin is reachable only through explicit offline CLI modes.
package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"rai/server/internal/syncing"
	"rai/server/migrations"
)

func Migrate(ctx context.Context, url string) error {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return err
	}
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.Files)
	if err != nil {
		return err
	}
	_, err = provider.Up(ctx)
	return err
}

type Enrollment struct {
	DatasetID      string `json:"datasetID"`
	DatasetEpoch   string `json:"datasetEpoch"`
	ServiceID      string `json:"serviceID"`
	DeviceID       string `json:"deviceID"`
	CollectorEpoch string `json:"collectorEpoch"`
	Token          string `json:"token"`
}

// Init creates the first primary collector only. It never replaces an existing dataset.
// The random token is returned once to the operator; only its digest enters PostgreSQL.
func Init(ctx context.Context, pool *pgxpool.Pool) (Enrollment, error) {
	r := Enrollment{DatasetID: syncing.NewID(), DatasetEpoch: syncing.NewID(), ServiceID: syncing.NewID(), DeviceID: syncing.NewID(), CollectorEpoch: syncing.NewID()}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return Enrollment{}, err
	}
	r.Token = base64.RawURLEncoding.EncodeToString(secret[:])
	h := sha256.Sum256([]byte(r.Token))
	tx, err := pool.Begin(ctx)
	if err != nil {
		return Enrollment{}, err
	}
	defer tx.Rollback(ctx)
	// Administrative initialization is serialized even when the dataset table is empty.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(72410727)`); err != nil {
		return Enrollment{}, err
	}
	var occupied bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM datasets LIMIT 1)`).Scan(&occupied); err != nil {
		return Enrollment{}, err
	}
	if occupied {
		return Enrollment{}, errors.New("dataset already initialized")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO datasets(id,epoch,service_id) VALUES($1,$2,$3)`, r.DatasetID, r.DatasetEpoch, r.ServiceID); err != nil {
		return Enrollment{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO devices(dataset_id,id,token_sha256,can_collect) VALUES($1,$2,$3,true)`, r.DatasetID, r.DeviceID, h[:]); err != nil {
		return Enrollment{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO collector_leases(dataset_id,device_id,collector_epoch) VALUES($1,$2,$3)`, r.DatasetID, r.DeviceID, r.CollectorEpoch); err != nil {
		return Enrollment{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Enrollment{}, err
	}
	return r, nil
}
