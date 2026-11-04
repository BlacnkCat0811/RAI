package admin

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"rai/server/migrations"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"rai/server/internal/syncing"
)

func validateRecoveryOrigin(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost"))) {
		return errors.New("recovery base-url must be an HTTPS origin (loopback HTTP allowed for synthetic tests)")
	}
	return nil
}

// publishCredential never replaces an existing package. File and directory are
// durable before any database transaction can create a usable identity.
func publishCredential(file string, p preparedDataset) error {
	b, e := json.Marshal(p)
	if e != nil {
		return e
	}
	tmp, e := os.CreateTemp(filepath.Dir(file), ".rai-enrollment-*")
	if e != nil {
		return e
	}
	defer os.Remove(tmp.Name())
	if e = tmp.Chmod(0600); e == nil {
		_, e = tmp.Write(b)
	}
	if e == nil {
		e = tmp.Sync()
	}
	ce := tmp.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	if e = os.Link(tmp.Name(), file); e != nil {
		return e
	}
	return syncCredential(file)
}
func syncCredential(file string) error {
	for _, path := range []string{file, filepath.Dir(file)} {
		f, e := os.Open(path)
		if e != nil {
			return e
		}
		e = f.Sync()
		ce := f.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
	}
	return nil
}

// InitCommand is the operator-only source-lost bootstrap. The private package,
// rather than stdout or a reversible database token, is its recovery authority.
func InitCommand(ctx context.Context, dsn string, args []string, out io.Writer) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var operation, actor, file, baseURL string
	flags.StringVar(&operation, "operation-id", "", "")
	flags.StringVar(&actor, "actor", "", "")
	flags.StringVar(&file, "credential-file", "", "")
	flags.StringVar(&baseURL, "base-url", "", "")
	if e := flags.Parse(args); e != nil || flags.NArg() != 0 || !syncing.ValidID(operation) || len(strings.TrimSpace(actor)) == 0 || len(actor) > 200 || !filepath.IsAbs(file) {
		return errors.New("usage: rai init --operation-id UUID --actor NAME --base-url ORIGIN --credential-file ABSOLUTE_PRIVATE_PATH")
	}
	if e := validateRecoveryOrigin(baseURL); e != nil {
		return e
	}
	pool, e := pgxpool.New(ctx, dsn)
	if e != nil {
		return errors.New("init connection failed")
	}
	defer pool.Close()
	var owner bool
	if e = pool.QueryRow(ctx, `SELECT r.rolsuper OR pg_has_role(current_user,d.datdba,'MEMBER') FROM pg_roles r JOIN pg_database d ON d.datname=current_database() WHERE r.rolname=current_user`).Scan(&owner); e != nil || !owner {
		return errors.New("init requires database owner identity")
	}
	var schema int
	if e = pool.QueryRow(ctx, `SELECT max(version_id) FROM goose_db_version WHERE is_applied`).Scan(&schema); e != nil || int64(schema) != migrations.LatestVersion() {
		return errors.New("init requires current migrated schema")
	}
	p := preparedDataset{OperationID: operation, Actor: actor, OperationType: "disaster_recovery", BaseURL: baseURL, ProtocolVersion: 3}
	info, e := os.Lstat(file)
	if e == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return errors.New("credential file must be regular and mode 0600")
		}
		b, e := os.ReadFile(file)
		if e != nil || json.Unmarshal(b, &p) != nil || p.OperationID != operation || p.Actor != actor || p.SourceDataset != "" || p.OperationType != "disaster_recovery" || p.BaseURL != baseURL || p.ProtocolVersion != 3 {
			return errors.New("prepared recovery identity conflict; preserve file")
		}
	} else if os.IsNotExist(e) {
		var occupied bool
		if e = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM datasets)`).Scan(&occupied); e != nil || occupied {
			return errors.New("already initialized; original private credential file required")
		}
		p.Enrollment, e = PrepareEnrollment()
		if e != nil {
			return e
		}
		if e = publishCredential(file, p); e != nil {
			return errors.New("credential prepare failed; preserve file and retry")
		}
	} else {
		return errors.New("credential inspection failed")
	}
	if e = syncCredential(file); e != nil {
		return errors.New("credential sync failed")
	}
	result, e := initPrepared(ctx, pool, p)
	if e != nil {
		return errors.New("initialization failed; preserve credential file and retry the same operation")
	}
	return json.NewEncoder(out).Encode(result)
}

func initPrepared(ctx context.Context, pool *pgxpool.Pool, p preparedDataset) (NewDatasetResult, error) {
	result := NewDatasetResult{OperationID: p.OperationID, Enrollment: p.Enrollment}
	result.Enrollment.Token = ""
	tx, e := pool.Begin(ctx)
	if e != nil {
		return result, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(72410727)`); e != nil {
		return result, e
	}
	h := sha256.Sum256([]byte(p.Enrollment.Token))
	var matches bool
	e = tx.QueryRow(ctx, `SELECT a.operation_type='disaster_recovery' AND a.source_dataset IS NULL AND a.actor=$2 AND a.target_dataset=$3 AND d.epoch=$4 AND d.service_id=$5 AND v.id=$6 AND v.token_sha256=$7 AND l.collector_epoch=$8
FROM admin_operations a JOIN datasets d ON d.id=a.target_dataset JOIN collector_leases l ON l.dataset_id=d.id JOIN devices v ON v.dataset_id=d.id AND v.id=l.device_id WHERE a.operation_id=$1`, p.OperationID, p.Actor, p.Enrollment.DatasetID, p.Enrollment.DatasetEpoch, p.Enrollment.ServiceID, p.Enrollment.DeviceID, h[:], p.Enrollment.CollectorEpoch).Scan(&matches)
	if e == nil {
		if !matches {
			return result, errors.New("recovery operation conflict")
		}
		result.Replayed = true
		return result, tx.Commit(ctx)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return result, e
	}
	var occupied bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM datasets)`).Scan(&occupied); e != nil || occupied {
		return result, errors.New("already initialized")
	}
	c := p.Enrollment
	if e = validateEnrollment(c); e != nil {
		return result, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO datasets(id,epoch,service_id) VALUES($1,$2,$3)`, c.DatasetID, c.DatasetEpoch, c.ServiceID); e != nil {
		return result, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO devices(dataset_id,id,token_sha256,can_collect) VALUES($1,$2,$3,true)`, c.DatasetID, c.DeviceID, h[:]); e != nil {
		return result, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO collector_leases(dataset_id,device_id,collector_epoch) VALUES($1,$2,$3)`, c.DatasetID, c.DeviceID, c.CollectorEpoch); e != nil {
		return result, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO admin_operations(operation_id,operation_type,source_dataset,target_dataset,actor,status) VALUES($1,'disaster_recovery',NULL,$2,$3,'committed')`, p.OperationID, c.DatasetID, p.Actor); e != nil {
		return result, e
	}
	return result, tx.Commit(ctx)
}
