package admin

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"time"

	rc "rai/server/internal/recovery"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
	wf "rai/server/internal/workoutfeatures"
)

// QueueRecoveryCandidate requests exactly one local output day. A timezone
// change requires this explicit assignment operation; old artifacts retain the
// original aggregation timezone and never become silently reinterpreted.
func QueueRecoveryCandidate(ctx context.Context, s *postgres.Store, dataset, day, operation, actor string) error {
	if !syncing.ValidID(dataset) {
		return errors.New("explicit dataset required")
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lockAdministrativeDataset(ctx, tx, dataset); e != nil {
		return e
	}
	var tz string
	var now time.Time
	if e = tx.QueryRow(ctx, `SELECT timezone,clock_timestamp() FROM datasets WHERE id=$1`, dataset).Scan(&tz, &now); e != nil {
		return e
	}
	loc, e := time.LoadLocation(tz)
	if e != nil {
		return e
	}
	d, e := time.ParseInLocation("2006-01-02", day, loc)
	if e != nil || d.After(now) || d.Before(now.In(loc).AddDate(0, 0, -366)) {
		return errors.New("explicit day within last 366 days required")
	}
	cfg := rc.DefaultConfig(tz)
	details := map[string]string{"action": "compute", "model": rc.Model, "day": day, "timezone": tz, "configHash": cfg.Hash()}
	replay, e := externalOperation(ctx, tx, dataset, operation, actor, "recovery_candidate", details)
	if e != nil {
		return e
	}
	if replay {
		return tx.Commit(ctx)
	}
	legacy := sha256.Sum256([]byte("legacy:workout.bundle/v2:training_state.bundle/v2"))
	if _, e = tx.Exec(ctx, `INSERT INTO analysis_model_channels(dataset_id,channel,stack_id,config_sha256,assignment_version) VALUES($1,'active','legacy',$2,1) ON CONFLICT DO NOTHING`, dataset, legacy[:]); e != nil {
		return e
	}
	// Reuse the platform candidate slot without enrolling M1/M2/M3/M4. The slot's
	// existing opt-in suppression marker is preserved by every explicit M1 entry.
	if _, e = tx.Exec(ctx, `INSERT INTO analysis_model_channels(dataset_id,channel,stack_id,config_sha256,assignment_version,extension_configs) VALUES($1,'candidate',$2,decode($3,'hex'),1,'{"rai.vo2max.external_only/v1":true}') ON CONFLICT DO NOTHING`, dataset, wf.StackID(), wf.DefaultPolicy().Hash()); e != nil {
		return e
	}
	var raw []byte
	if e = tx.QueryRow(ctx, `SELECT extension_configs->$2 FROM analysis_model_channels WHERE dataset_id=$1 AND channel='candidate'`, dataset, rc.Model).Scan(&raw); e != nil {
		return e
	}
	var old struct {
		Hash     string `json:"configHash"`
		Timezone string `json:"timezone"`
	}
	if raw != nil && json.Unmarshal(raw, &old) != nil {
		return errors.New("candidate assignment invalid")
	}
	if old.Hash != cfg.Hash() || old.Timezone != tz {
		entry := map[string]string{"configHash": cfg.Hash(), "assignmentID": operation, "timezone": tz, "scopePrefix": rc.Scope(tz, "")}
		b, _ := json.Marshal(entry)
		if _, e = tx.Exec(ctx, `UPDATE analysis_model_channels SET extension_configs=extension_configs||jsonb_build_object($2::text,$3::jsonb) WHERE dataset_id=$1 AND channel='candidate'`, dataset, rc.Model, b); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(ctx, `SELECT rai_recovery_dirty_day($1,$2::date)`, dataset, day); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func RecoveryCandidateCommand(ctx context.Context, url string, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("recovery compute|inspect --dataset --day; compute also requires --operation --actor")
	}
	action := args[0]
	fs := flag.NewFlagSet("recovery", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dataset := fs.String("dataset", "", "")
	day := fs.String("day", "", "")
	op := fs.String("operation", "", "")
	actor := fs.String("actor", "", "")
	if e := fs.Parse(args[1:]); e != nil {
		return e
	}
	if !syncing.ValidID(*dataset) || *day == "" || fs.NArg() != 0 {
		return errors.New("explicit dataset and day required")
	}
	s, e := postgres.Open(ctx, url, 2)
	if e != nil {
		return e
	}
	defer s.Pool.Close()
	switch action {
	case "compute":
		if e = QueueRecoveryCandidate(ctx, s, *dataset, *day, *op, *actor); e != nil {
			return e
		}
		return json.NewEncoder(out).Encode(map[string]any{"status": "queued", "candidateOnly": true})
	case "inspect":
		r, _, e := s.CandidateRecovery(ctx, *dataset, *day, time.Now().UTC())
		if e != nil {
			return e
		}
		return json.NewEncoder(out).Encode(map[string]any{"status": r.Status, "quality": r.Quality, "baselineState": r.BaselineState, "componentCount": int(r.ComponentCoverage * 3), "reasonCodes": r.Reasons, "configHash": r.ConfigHash, "candidateOnly": true, "physiologicallyValidated": false})
	default:
		return errors.New("unsupported recovery action")
	}
}
