// Package admin is reachable only through explicit offline CLI modes.
package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io/fs"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"rai/server/internal/syncing"
	"rai/server/migrations"
)

// Only explicitly reviewed migrations may be rolled back. Each future entry
// must pin the SHA-256 of the complete immutable migration, including Down.
// Versions 1–9 are deliberately irreversible.
var reversibleMigrations = map[int64]string{}

var ErrMigrationIrreversible = errors.New("migration is not approved for rollback")

var sqlComments = regexp.MustCompile(`(?s)/\*.*?\*/|--[^\n]*`)

func hasDownStatements(body []byte) bool {
	parts := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(parts) != 2 {
		return false
	}
	return strings.Trim(sqlComments.ReplaceAllString(parts[1], ""), " \t\r\n;") != ""
}

// Serialize RAI migration commands on one dedicated session, including the
// safety check and the ensuing goose operation. External migration tools must
// not run concurrently with these commands.
func withMigrationLock(ctx context.Context, db *sql.DB, fn func() error) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(72410728)`); err != nil {
		// A canceled acquisition can have reached the server; do not reuse the
		// session unless we know it cannot still hold an advisory lock.
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		return err
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(unlockCtx, `SELECT pg_advisory_unlock(72410728)`); err != nil {
			// Discard the session rather than returning a possibly locked connection.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	return fn()
}

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
	return withMigrationLock(ctx, db, func() error {
		_, err := provider.Up(ctx)
		return err
	})
}

// MigrateDown rolls back one specifically approved migration, never an empty
// Down. It does not downgrade any of the existing fact or protocol migrations.
func MigrateDown(ctx context.Context, url string) error {
	return migrateDown(ctx, url, migrations.Files, reversibleMigrations)
}

func migrateDown(ctx context.Context, url string, files fs.FS, approved map[int64]string) error {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return err
	}
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, files)
	if err != nil {
		return err
	}
	return withMigrationLock(ctx, db, func() error {
		status, err := provider.Status(ctx)
		if err != nil {
			return err
		}
		var current *goose.MigrationStatus
		known := make(map[int64]bool, len(status))
		for _, item := range status {
			if item.Source == nil {
				return ErrMigrationIrreversible
			}
			known[item.Source.Version] = item.State == goose.StateApplied
			if item.State == goose.StateApplied && (current == nil || item.Source.Version > current.Source.Version) {
				current = item
			}
		}
		if current == nil || current.Source == nil || current.Source.Type != goose.TypeSQL {
			return ErrMigrationIrreversible
		}
		rows, err := db.QueryContext(ctx, `SELECT version_id, is_applied FROM (SELECT DISTINCT ON (version_id) version_id, is_applied FROM goose_db_version ORDER BY version_id, id DESC) AS latest WHERE version_id <> 0`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var version int64
			var applied bool
			if err := rows.Scan(&version, &applied); err != nil {
				rows.Close()
				return err
			}
			if applied && !known[version] {
				rows.Close()
				return ErrMigrationIrreversible
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		expected, ok := approved[current.Source.Version]
		if !ok {
			return ErrMigrationIrreversible
		}
		body, err := fs.ReadFile(files, current.Source.Path)
		if err != nil {
			return err
		}
		hash := sha256.Sum256(body)
		if hex.EncodeToString(hash[:]) != expected || !hasDownStatements(body) {
			return ErrMigrationIrreversible
		}
		// Apply the checked version explicitly; goose Down otherwise selects the
		// most recently applied version, which can differ after out-of-order work.
		result, err := provider.ApplyVersion(ctx, current.Source.Version, false)
		if err != nil {
			return err
		}
		if result.Empty || result.Source.Version != current.Source.Version {
			return errors.New("unexpected migration rollback result")
		}
		return nil
	})
}

// MigrationStatus inspects the embedded migration set without applying it.
func MigrationStatus(ctx context.Context, url string) ([]*goose.MigrationStatus, error) {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.Files)
	if err != nil {
		return nil, err
	}
	return provider.Status(ctx)
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
