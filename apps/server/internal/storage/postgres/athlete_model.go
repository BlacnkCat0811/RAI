package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	am "rai/server/internal/athletemodel"
	"rai/server/internal/modelmanifest"
	pf "rai/server/internal/performance"
	rd "rai/server/internal/readiness"
	rc "rai/server/internal/recovery"
	"rai/server/internal/syncing"
	tl "rai/server/internal/trainingload"
	ve "rai/server/internal/vo2maxexternal"

	"github.com/jackc/pgx/v5"
)

var ErrModelProjection = errors.New("athlete_model_technical_failure")

type ModelSnapshotStats struct {
	DBReads   int   `json:"dbReads"`
	ReadNS    int64 `json:"readNS"`
	FreshenNS int64 `json:"freshenNS"`
	MappingNS int64 `json:"mappingNS"`
}
type modelReadContext struct {
	timezone, epoch, collector, configHash string
	revision                               int64
	assignment                             am.Assignment
	extensions                             map[string]json.RawMessage
	validSession                           bool
}

func readModelContext(ctx context.Context, tx pgx.Tx, dataset, channel string, at time.Time) (am.AthleteModelSnapshot, modelReadContext, error) {
	var c modelReadContext
	var raw []byte
	e := tx.QueryRow(ctx, `SELECT d.timezone,d.epoch::text,d.commit_rev,COALESCE(c.collector_epoch::text,''),COALESCE(m.stack_id,CASE WHEN $2='active' THEN 'legacy' ELSE '' END),COALESCE(m.assignment_version,0),COALESCE(m.extension_configs,'{}'::jsonb),COALESCE(encode(m.config_sha256,'hex'),''),NOT EXISTS(SELECT 1 FROM collector_leases l LEFT JOIN devices v ON v.dataset_id=l.dataset_id AND v.id=l.device_id WHERE l.dataset_id=d.id AND (v.id IS NULL OR NOT v.can_collect OR v.revoked_at IS NOT NULL)) FROM datasets d LEFT JOIN collector_leases c ON c.dataset_id=d.id LEFT JOIN analysis_model_channels m ON m.dataset_id=d.id AND m.channel=$2 WHERE d.id=$1 AND d.retired_at IS NULL`, dataset, channel).Scan(&c.timezone, &c.epoch, &c.revision, &c.collector, &c.assignment.StackID, &c.assignment.Version, &raw, &c.configHash, &c.validSession)
	if e != nil {
		return am.AthleteModelSnapshot{}, c, e
	}
	if json.Unmarshal(raw, &c.extensions) != nil {
		return am.AthleteModelSnapshot{}, c, ErrModelProjection
	}
	loc, e := time.LoadLocation(c.timezone)
	if e != nil {
		return am.AthleteModelSnapshot{}, c, ErrModelProjection
	}
	c.assignment.Channel = channel
	return am.NewSnapshot(at.UnixMicro(), c.revision, c.timezone, at.In(loc).Format("2006-01-02"), c.assignment), c, nil
}

type modelSlot struct {
	Metric string `json:"metric"`
	Key    string `json:"key"`
	Scope  string `json:"scope"`
	Family string `json:"family"`
}
type projectionRef struct {
	Metric string `json:"metric"`
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Type   string `json:"type"`
	Scope  string `json:"scope"`
	Run    string `json:"run"`
	Key    string `json:"key"`
	Hash   string `json:"hash"`
}
type candidateModels struct {
	load      *tl.Result
	state     *tl.StateResult
	ability   *pf.Result
	vo2       *ve.Result
	recovery  *rc.Result
	readiness *rd.Result
}

// ReadAthleteModelCandidateSnapshot requires administrative DB authority. It
// cannot be called successfully by the restricted API or worker role. All
// reads, controls, dependency identities and assignments share one RR view.
func (s *Store) ReadAthleteModelCandidateSnapshot(ctx context.Context, dataset string, at time.Time) (am.AthleteModelCandidateSnapshot, ModelSnapshotStats, error) {
	stats := ModelSnapshotStats{}
	start := time.Now()
	if !syncing.ValidID(dataset) || at.IsZero() {
		return am.AthleteModelCandidateSnapshot{}, stats, ErrModelProjection
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, e := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return am.AthleteModelCandidateSnapshot{}, stats, e
	}
	defer tx.Rollback(ctx)
	var admin bool
	e = tx.QueryRow(ctx, `SELECT r.rolsuper OR pg_has_role(current_user,d.datdba,'MEMBER') FROM pg_roles r JOIN pg_database d ON d.datname=current_database() WHERE r.rolname=current_user`).Scan(&admin)
	stats.DBReads++
	if e != nil {
		return am.AthleteModelCandidateSnapshot{}, stats, e
	}
	if !admin {
		return am.AthleteModelCandidateSnapshot{}, stats, errors.New("administrative model preview required")
	}
	out, c, e := readModelContext(ctx, tx, dataset, "candidate", at)
	stats.DBReads++
	if e != nil {
		return am.AthleteModelCandidateSnapshot{}, stats, e
	}
	if c.assignment.StackID == modelmanifest.StackID() {
		manifest, err := modelmanifest.CurrentHash()
		if err != nil || c.configHash != manifest {
			return am.AthleteModelCandidateSnapshot{}, stats, ErrModelProjection
		}
		out = am.NewSnapshotV2(at.UnixMicro(), c.revision, c.timezone, out.CalendarDate, c.assignment)
		out, err = readPublishedModelStack(ctx, tx, dataset, "candidate", c.assignment.StackID, out, c, at)
		stats.DBReads++
		if err != nil {
			return am.AthleteModelCandidateSnapshot{}, stats, err
		}
		stats.ReadNS = time.Since(start).Nanoseconds()
		if err = tx.Commit(ctx); err != nil {
			return am.AthleteModelCandidateSnapshot{}, stats, err
		}
		return am.AthleteModelCandidateSnapshot{CandidateOnly: true, Snapshot: out}, stats, nil
	}
	// The v1 candidate models were retired with schema 53.
	return am.AthleteModelCandidateSnapshot{}, stats, ErrModelProjection
}
