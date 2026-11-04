package admin

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"time"

	rc "rai/server/internal/readiness"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
	wf "rai/server/internal/workoutfeatures"
)

// QueueReadinessCandidate requests one candidate instant anchor. It never
// enrolls upstream models, changes active readers or asserts Workout coverage.
func QueueReadinessCandidate(ctx context.Context, s *postgres.Store, dataset, operation, actor string) error {
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
	if e = tx.QueryRow(ctx, `SELECT timezone FROM datasets WHERE id=$1`, dataset).Scan(&tz); e != nil {
		return e
	}
	_, e = time.LoadLocation(tz)
	if e != nil {
		return e
	}
	cfg := rc.DefaultConfig(tz)
	details := map[string]string{"action": "compute", "model": rc.Model, "timezone": tz, "configHash": cfg.Hash()}
	replay, e := externalOperation(ctx, tx, dataset, operation, actor, "readiness_candidate", details)
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
		entry := map[string]string{"configHash": cfg.Hash(), "assignmentID": operation, "timezone": tz, "scope": rc.Scope(tz)}
		b, _ := json.Marshal(entry)
		if _, e = tx.Exec(ctx, `UPDATE analysis_model_channels SET extension_configs=extension_configs||jsonb_build_object($2::text,$3::jsonb) WHERE dataset_id=$1 AND channel='candidate'`, dataset, rc.Model, b); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(ctx, `SELECT rai_readiness_dirty($1)`, dataset); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func ReadinessCandidateCommand(ctx context.Context, url string, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("readiness compute|inspect --dataset; compute also requires --operation --actor")
	}
	action := args[0]
	fs := flag.NewFlagSet("readiness", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dataset := fs.String("dataset", "", "")
	op := fs.String("operation", "", "")
	actor := fs.String("actor", "", "")
	if e := fs.Parse(args[1:]); e != nil {
		return e
	}
	if !syncing.ValidID(*dataset) || fs.NArg() != 0 {
		return errors.New("explicit dataset required")
	}
	s, e := postgres.Open(ctx, url, 2)
	if e != nil {
		return e
	}
	defer s.Pool.Close()
	switch action {
	case "compute":
		if e = QueueReadinessCandidate(ctx, s, *dataset, *op, *actor); e != nil {
			return e
		}
		return json.NewEncoder(out).Encode(map[string]any{"status": "queued", "candidateOnly": true})
	case "inspect":
		r, _, e := s.CandidateReadiness(ctx, *dataset, time.Now().UTC())
		if e != nil {
			return e
		}
		return json.NewEncoder(out).Encode(map[string]any{"status": r.Status, "quality": r.Quality, "baselineState": r.Baseline.State, "knownLoadCount": r.KnownLoads, "unknownLoadCount": r.UnknownLoads, "reasonCodes": r.Reasons, "configHash": r.ConfigHash, "candidateOnly": true, "physiologicallyValidated": false})
	default:
		return errors.New("unsupported readiness action")
	}
}
