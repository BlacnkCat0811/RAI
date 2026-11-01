package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"rai/server/internal/syncing"
)

type NewDatasetRequest struct {
	OperationID   string
	SourceDataset string
	Actor         string
	OperationType string
}
type NewDatasetResult struct {
	OperationID string     `json:"operationID"`
	Enrollment  Enrollment `json:"identity"` // Token is always empty; credential file is the sole output.
	Replayed    bool       `json:"replayed"`
}
type DatasetPlan struct {
	SourceExists     bool   `json:"sourceExists"`
	Retired          bool   `json:"retired"`
	Workouts         int64  `json:"workouts"`
	Samples          int64  `json:"samples"`
	Routes           int64  `json:"routes"`
	PendingJobs      int64  `json:"pendingJobs"`
	Devices          int64  `json:"devices"`
	CollectorPresent bool   `json:"collectorPresent"`
	PlannedTarget    string `json:"plannedTargetIdentity"`
}

func (r NewDatasetRequest) validate() error {
	if r.OperationType != "" && r.OperationType != "new_dataset" && r.OperationType != "disaster_recovery" {
		return errors.New("invalid operation type")
	}
	if !syncing.ValidID(r.OperationID) || !syncing.ValidID(r.SourceDataset) || len(strings.TrimSpace(r.Actor)) == 0 || len(r.Actor) > 200 {
		return errors.New("invalid new-dataset request")
	}
	return nil
}
func PrepareEnrollment() (Enrollment, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return Enrollment{}, errors.New("credential generation failed")
	}
	return Enrollment{DatasetID: syncing.NewID(), DatasetEpoch: syncing.NewID(), ServiceID: syncing.NewID(), DeviceID: syncing.NewID(), CollectorEpoch: syncing.NewID(), Token: base64.RawURLEncoding.EncodeToString(b[:])}, nil
}

// PlanNewDataset is a consistent read-only inventory, never a reset or a prepare.
func PlanNewDataset(ctx context.Context, pool *pgxpool.Pool, source string) (DatasetPlan, error) {
	p := DatasetPlan{PlannedTarget: "server-generated fresh dataset/epoch/device/token/collector; no records created"}
	if !syncing.ValidID(source) {
		return p, errors.New("invalid source dataset")
	}
	tx, e := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return p, e
	}
	defer tx.Rollback(ctx)
	e = tx.QueryRow(ctx, `SELECT true,retired_at IS NOT NULL,
 (SELECT count(*) FROM workouts WHERE dataset_id=$1),(SELECT count(*) FROM health_samples WHERE dataset_id=$1),
 (SELECT count(*) FROM workout_routes WHERE dataset_id=$1),(SELECT count(*) FROM jobs WHERE dataset_id=$1 AND state IN ('queued','running')),
 (SELECT count(*) FROM devices WHERE dataset_id=$1),EXISTS(SELECT 1 FROM collector_leases WHERE dataset_id=$1) FROM datasets WHERE id=$1`, source).Scan(&p.SourceExists, &p.Retired, &p.Workouts, &p.Samples, &p.Routes, &p.PendingJobs, &p.Devices, &p.CollectorPresent)
	if errors.Is(e, pgx.ErrNoRows) {
		return p, errors.New("source dataset not found")
	}
	if e != nil {
		return p, e
	}
	return p, tx.Commit(ctx)
}

// NewDataset serializes operation identity, then source dataset, then child rows.
// No fact/history is copied. A prepared credential can resume after rollback.
func NewDataset(ctx context.Context, pool *pgxpool.Pool, r NewDatasetRequest, credential Enrollment) (NewDatasetResult, error) {
	result := NewDatasetResult{OperationID: r.OperationID}
	if e := r.validate(); e != nil {
		return result, e
	}
	tx, e := pool.Begin(ctx)
	if e != nil {
		return result, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,72410729))`, r.OperationID); e != nil {
		return result, e
	}
	var source, actor, operationType string
	e = tx.QueryRow(ctx, `SELECT source_dataset::text,target_dataset::text,actor,operation_type FROM admin_operations WHERE operation_id=$1`, r.OperationID).Scan(&source, &result.Enrollment.DatasetID, &actor, &operationType)
	if e == nil {
		if source != r.SourceDataset || actor != r.Actor || operationType != r.operationType() {
			return result, errors.New("operation identity conflict")
		}
		e = tx.QueryRow(ctx, `SELECT d.epoch::text,d.service_id::text,v.id::text,l.collector_epoch::text FROM datasets d JOIN collector_leases l ON l.dataset_id=d.id JOIN devices v ON v.dataset_id=d.id AND v.id=l.device_id WHERE d.id=$1`, result.Enrollment.DatasetID).Scan(&result.Enrollment.DatasetEpoch, &result.Enrollment.ServiceID, &result.Enrollment.DeviceID, &result.Enrollment.CollectorEpoch)
		if e != nil {
			return result, e
		}
		if credential.Token != "" {
			candidate := credential
			candidate.Token = ""
			if candidate != result.Enrollment {
				return result, errors.New("prepared credential does not match committed operation")
			}
			hash := sha256.Sum256([]byte(credential.Token))
			var matches bool
			if e = tx.QueryRow(ctx, `SELECT token_sha256=$3 FROM devices WHERE dataset_id=$1 AND id=$2`, credential.DatasetID, credential.DeviceID, hash[:]).Scan(&matches); e != nil || !matches {
				return result, errors.New("prepared credential does not match committed operation")
			}
		}
		result.Replayed = true
		return result, tx.Commit(ctx)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return result, e
	}
	var retired bool
	if e = tx.QueryRow(ctx, `SELECT retired_at IS NOT NULL FROM datasets WHERE id=$1 FOR UPDATE`, r.SourceDataset).Scan(&retired); e != nil {
		return result, errors.New("source dataset not found")
	}
	if retired {
		return result, errors.New("source dataset retired; recover its original operation")
	}
	c := credential
	if e = validateEnrollment(c); e != nil {
		return result, e
	}
	// Verify every identity against the source, including a prepared file supplied on retry.
	var independent bool
	if e = tx.QueryRow(ctx, `SELECT $2::uuid<>d.id AND $3::uuid<>d.epoch AND NOT EXISTS(SELECT 1 FROM devices WHERE dataset_id=d.id AND id=$4) AND NOT EXISTS(SELECT 1 FROM collector_leases WHERE dataset_id=d.id AND collector_epoch=$5) FROM datasets d WHERE d.id=$1`, r.SourceDataset, c.DatasetID, c.DatasetEpoch, c.DeviceID, c.CollectorEpoch).Scan(&independent); e != nil {
		return result, e
	}
	if !independent {
		return result, errors.New("prepared identity must be independent")
	}
	h := sha256.Sum256([]byte(c.Token))
	if _, e = tx.Exec(ctx, `INSERT INTO datasets(id,epoch,service_id) VALUES($1,$2,$3)`, c.DatasetID, c.DatasetEpoch, c.ServiceID); e != nil {
		return result, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO devices(dataset_id,id,token_sha256,can_collect) VALUES($1,$2,$3,true)`, c.DatasetID, c.DeviceID, h[:]); e != nil {
		return result, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO collector_leases(dataset_id,device_id,collector_epoch) VALUES($1,$2,$3)`, c.DatasetID, c.DeviceID, c.CollectorEpoch); e != nil {
		return result, e
	}
	if _, e = tx.Exec(ctx, `UPDATE datasets SET retired_at=clock_timestamp() WHERE id=$1`, r.SourceDataset); e != nil {
		return result, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO admin_operations(operation_id,operation_type,source_dataset,target_dataset,actor,status) VALUES($1,$5,$2,$3,$4,'committed')`, r.OperationID, r.SourceDataset, c.DatasetID, r.Actor, r.operationType()); e != nil {
		return result, e
	}
	if e = tx.Commit(ctx); e != nil {
		return result, e
	}
	c.Token = ""
	result.Enrollment = c
	return result, nil
}

func (r NewDatasetRequest) operationType() string {
	if r.OperationType == "" {
		return "new_dataset"
	}
	return r.OperationType
}

type preparedDataset struct {
	OperationType   string     `json:"operationType,omitempty"`
	BaseURL         string     `json:"baseURL,omitempty"`
	ProtocolVersion int        `json:"protocolVersion,omitempty"`
	OperationID     string     `json:"operationID"`
	SourceDataset   string     `json:"sourceDataset"`
	Actor           string     `json:"actor"`
	Enrollment      Enrollment `json:"enrollment"`
}

// AdminCommand has no HTTP entry. The owner/operator database identity is required.
func AdminCommand(ctx context.Context, url string, args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "new-dataset" {
		return errors.New("usage: rai admin new-dataset --source-dataset UUID --operation-id UUID --actor NAME [--dry-run | --confirm-source UUID --confirm-phrase NEW-DATASET --credential-file ABSOLUTE_PATH]")
	}
	f := flag.NewFlagSet("new-dataset", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	var r NewDatasetRequest
	var confirm, phrase, file, baseURL string
	var dry bool
	f.StringVar(&r.SourceDataset, "source-dataset", "", "")
	f.StringVar(&r.OperationID, "operation-id", "", "")
	f.StringVar(&r.Actor, "actor", "", "")
	f.StringVar(&r.OperationType, "operation-type", "new_dataset", "")
	f.StringVar(&baseURL, "base-url", "", "")
	f.StringVar(&confirm, "confirm-source", "", "")
	f.StringVar(&phrase, "confirm-phrase", "", "")
	f.StringVar(&file, "credential-file", "", "")
	f.BoolVar(&dry, "dry-run", false, "")
	if e := f.Parse(args[1:]); e != nil || f.NArg() != 0 {
		return errors.New("invalid new-dataset arguments")
	}
	if !dry {
		if e := r.validate(); e != nil {
			return e
		}
		if confirm != r.SourceDataset || phrase != "NEW-DATASET" || !filepath.IsAbs(file) {
			return errors.New("explicit source confirmation, NEW-DATASET phrase and absolute credential-file required")
		}
	}
	if r.OperationType == "disaster_recovery" && !dry {
		if e := validateRecoveryOrigin(baseURL); e != nil {
			return e
		}
	}
	pool, e := pgxpool.New(ctx, url)
	if e != nil {
		return errors.New("admin connection failed")
	}
	defer pool.Close()
	var owner bool
	if e = pool.QueryRow(ctx, `SELECT r.rolsuper OR pg_has_role(current_user,d.datdba,'MEMBER') FROM pg_roles r JOIN pg_database d ON d.datname=current_database() WHERE r.rolname=current_user`).Scan(&owner); e != nil || !owner {
		return errors.New("admin requires database owner identity")
	}
	if dry {
		p, e := PlanNewDataset(ctx, pool, r.SourceDataset)
		if e != nil {
			return errors.New("source inspection failed")
		}
		return json.NewEncoder(out).Encode(p)
	}
	p := preparedDataset{OperationID: r.OperationID, SourceDataset: r.SourceDataset, Actor: r.Actor, OperationType: r.operationType(), BaseURL: baseURL, ProtocolVersion: 3}
	info, e := os.Lstat(file)
	if e == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return errors.New("credential file must be regular and mode 0600")
		}
		b, e := os.ReadFile(file)
		if e != nil || json.Unmarshal(b, &p) != nil {
			return errors.New("prepared credential unreadable; preserve file")
		}
		if p.OperationID != r.OperationID || p.SourceDataset != r.SourceDataset || p.Actor != r.Actor || (p.OperationType != "" && p.OperationType != r.operationType()) || p.BaseURL != baseURL {
			return errors.New("prepared operation identity conflict")
		}
	} else if os.IsNotExist(e) {
		// A committed retry needs no new credential, and never emits the old token.
		var exists bool
		if e = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM admin_operations WHERE operation_id=$1)`, r.OperationID).Scan(&exists); e != nil {
			return errors.New("operation lookup failed")
		}
		if !exists {
			p.Enrollment, e = PrepareEnrollment()
			if e != nil {
				return e
			}
			if e = publishCredential(file, p); e != nil {
				return errors.New("credential prepare failed; preserve file and retry")
			}
		}
	} else {
		return errors.New("credential file inspection failed")
	}
	if p.Enrollment.Token != "" {
		if e = syncCredential(file); e != nil {
			return errors.New("credential sync failed")
		}
	}
	result, e := NewDataset(ctx, pool, r, p.Enrollment)
	if e != nil {
		return errors.New("new-dataset failed; preserve credential file and retry the same operation")
	}
	return json.NewEncoder(out).Encode(result)
}

func validateEnrollment(c Enrollment) error {
	for _, id := range []string{c.DatasetID, c.DatasetEpoch, c.ServiceID, c.DeviceID, c.CollectorEpoch} {
		if !syncing.ValidID(id) {
			return errors.New("invalid prepared identity")
		}
	}
	b, err := base64.RawURLEncoding.DecodeString(c.Token)
	if err != nil || len(b) != 32 {
		return errors.New("invalid prepared credential")
	}

	return nil
}
