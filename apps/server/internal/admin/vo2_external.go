package admin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
	ve "rai/server/internal/vo2maxexternal"
	wf "rai/server/internal/workoutfeatures"
)

// externalOperation returns the original immutable operation on exact replay.
func externalOperation(ctx context.Context, tx pgx.Tx, dataset, operation, actor, kind string, details any) (bool, error) {
	if !syncing.ValidID(dataset) || !syncing.ValidID(operation) || actor == "" || len(actor) > 200 {
		return false, errors.New("explicit dataset operation actor required")
	}
	var previous []byte
	var oldKind, oldActor, oldDataset string
	body, _ := json.Marshal(details)
	err := tx.QueryRow(ctx, `SELECT operation_type,actor,subject_dataset::text,details FROM admin_operations WHERE operation_id=$1`, operation).Scan(&oldKind, &oldActor, &oldDataset, &previous)
	if err == nil {
		var p any
		var d any
		pd := json.NewDecoder(bytes.NewReader(previous))
		pd.UseNumber()
		dd := json.NewDecoder(bytes.NewReader(body))
		dd.UseNumber()
		if pd.Decode(&p) != nil || dd.Decode(&d) != nil {
			return false, errors.New("operation audit invalid")
		}
		if oldKind != kind || oldActor != actor || oldDataset != dataset || ve.Hash(p) != ve.Hash(d) {
			return false, errors.New("operation conflict")
		}
		return true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO admin_operations(operation_id,operation_type,actor,status,subject_dataset,details) VALUES($1,$2,$3,'committed',$4,$5)`, operation, kind, actor, dataset, body)
	return false, err
}
func QueueVO2ExternalCandidate(ctx context.Context, s *postgres.Store, dataset, operation, actor string) error {
	if !syncing.ValidID(dataset) {
		return errors.New("explicit dataset required")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockAdministrativeDataset(ctx, tx, dataset); err != nil {
		return err
	}
	replay, err := externalOperation(ctx, tx, dataset, operation, actor, "vo2max_external_candidate", map[string]string{"action": "compute", "model": ve.Model, "configHash": ve.DefaultConfig().Hash()})
	if err != nil {
		return err
	}
	if replay {
		return tx.Commit(ctx)
	}
	legacy := sha256.Sum256([]byte("legacy:workout.bundle/v2:training_state.bundle/v2"))
	if _, err = tx.Exec(ctx, `INSERT INTO analysis_model_channels(dataset_id,channel,stack_id,config_sha256,assignment_version) VALUES($1,'active','legacy',$2,1) ON CONFLICT DO NOTHING`, dataset, legacy[:]); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO analysis_model_channels(dataset_id,channel,stack_id,config_sha256,assignment_version,extension_configs) VALUES($1,'candidate',$2,decode($3,'hex'),1,'{"rai.vo2max.external_only/v1":true}') ON CONFLICT DO NOTHING`, dataset, wf.StackID(), wf.DefaultPolicy().Hash()); err != nil {
		return err
	}
	// The slot metadata is platform compatibility only. No M1 compute/enrollment.
	var entry []byte
	if err = tx.QueryRow(ctx, `SELECT extension_configs->$2 FROM analysis_model_channels WHERE dataset_id=$1 AND channel='candidate'`, dataset, ve.Model).Scan(&entry); err != nil {
		return err
	}
	if entry == nil {
		if _, err = tx.Exec(ctx, `UPDATE analysis_model_channels SET extension_configs=extension_configs||jsonb_build_object($2::text,jsonb_build_object('configHash',$3::text,'assignmentID',$4::text)) WHERE dataset_id=$1 AND channel='candidate'`, dataset, ve.Model, ve.DefaultConfig().Hash(), operation); err != nil {
			return err
		}
	} else {
		var current struct {
			Hash string `json:"configHash"`
		}
		if json.Unmarshal(entry, &current) != nil || current.Hash != ve.DefaultConfig().Hash() {
			return errors.New("external candidate config conflict")
		}
		if _, err = tx.Exec(ctx, `SELECT rai_vo2_external_dirty($1)`, dataset); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO analysis_model_dependencies(dataset_id,upstream_scope,downstream_scope) VALUES($1,$2,$4),($1,$3,$4) ON CONFLICT DO NOTHING`, dataset, ve.FactsScope, ve.SelectionScope, ve.Scope); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func SetVO2SourceSelection(ctx context.Context, s *postgres.Store, dataset, operation, actor string, x ve.Selection) error {
	if !syncing.ValidID(dataset) || !syncing.ValidID(x.ID) || len(x.Epoch) != 71 || x.EffectiveFrom <= 0 || x.EffectiveThrough != nil && *x.EffectiveThrough <= x.EffectiveFrom {
		return errors.New("invalid explicit selection")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockAdministrativeDataset(ctx, tx, dataset); err != nil {
		return err
	}
	// recordedAt is the database append time, not a caller's forged past time.
	x.RecordedAt = 0
	x.Hash = ""
	canonical, _ := json.Marshal(struct {
		Dataset, Operation, Actor, Kind, Schema string
		Selection                               ve.Selection
	}{dataset, operation, actor, "vo2max_source_selection", ve.SelectionSchema, x})
	h := sha256.Sum256(canonical)
	x.Hash = hex.EncodeToString(h[:])
	replay, err := externalOperation(ctx, tx, dataset, operation, actor, "vo2max_source_selection", x)
	if err != nil {
		return err
	}
	if replay {
		return tx.Commit(ctx)
	}
	var n int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM athlete_model_inputs WHERE dataset_id=$1 AND kind='vo2max_source_selection' LIMIT 129) t`, dataset).Scan(&n); err != nil {
		return err
	}
	if n >= 128 {
		return errors.New("selection capacity reached")
	}
	body, _ := json.Marshal(map[string]any{"sourceEpoch": x.Epoch, "revoked": x.Revoked})
	var supersedes *string
	if x.Supersedes != "" {
		supersedes = &x.Supersedes
	}
	var through *time.Time
	if x.EffectiveThrough != nil {
		v := time.UnixMicro(*x.EffectiveThrough)
		through = &v
	}
	_, err = tx.Exec(ctx, `INSERT INTO athlete_model_inputs(dataset_id,id,kind,subject_key,effective_from,effective_through,supersedes_id,schema_version,content_sha256,payload,provenance,created_at) VALUES($1,$2,'vo2max_source_selection','athlete',$3,$4,$5,$6,$7,$8,$9,clock_timestamp())`, dataset, x.ID, time.UnixMicro(x.EffectiveFrom), through, supersedes, ve.SelectionSchema, h[:], body, actor)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func VO2ExternalCommand(ctx context.Context, url string, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: rai analysis vo2max-external compute|recompute|select|inspect|history|promotion-plan")
	}
	fs := flag.NewFlagSet("vo2max-external", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dataset := fs.String("dataset", "", "dataset UUID")
	operation := fs.String("operation", "", "operation UUID")
	actor := fs.String("actor", "", "audit actor")
	run := fs.String("run", "", "history run UUID")
	epoch := fs.String("epoch", "", "explicit source epoch")
	input := fs.String("input", "", "selection input UUID")
	supersedes := fs.String("supersedes", "", "superseded input UUID")
	from := fs.Int64("effective-from-us", 0, "effective start")
	through := fs.Int64("effective-through-us", 0, "exclusive end")
	revoke := fs.Bool("revoked", false, "append revocation")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		return errors.New("invalid external projection flags")
	}
	if args[0] == "promotion-plan" {
		return json.NewEncoder(out).Encode(map[string]any{"model": ve.Model, "promotable": false, "active": "legacy", "reasons": []string{"product_not_activated", "real_device_acceptance_deferred"}})
	}
	if !syncing.ValidID(*dataset) {
		return errors.New("explicit dataset required")
	}
	s, err := postgres.Open(ctx, url, 4)
	if err != nil {
		return errors.New("external database configuration failed")
	}
	defer s.Pool.Close()
	if err = s.Ready(ctx); err != nil {
		return errors.New("external schema not ready")
	}
	switch args[0] {
	case "compute", "recompute":
		err = QueueVO2ExternalCandidate(ctx, s, *dataset, *operation, *actor)
	case "select":
		x := ve.Selection{ID: *input, Epoch: *epoch, EffectiveFrom: *from, Supersedes: *supersedes, Revoked: *revoke}
		if *through != 0 {
			x.EffectiveThrough = through
		}
		err = SetVO2SourceSelection(ctx, s, *dataset, *operation, *actor, x)
	case "inspect", "history":
		var r ve.Result
		withdrawn := false
		if args[0] == "inspect" {
			r, _, err = s.CandidateVO2External(ctx, *dataset, time.Now().UTC())
		} else if syncing.ValidID(*run) {
			r, withdrawn, err = s.HistoricalVO2External(ctx, *dataset, *run)
		} else {
			err = errors.New("explicit run required")
		}
		if err == nil {
			return json.NewEncoder(out).Encode(map[string]any{"status": r.Status, "quality": r.Quality, "confidence": r.Confidence, "model": r.ModelVersion, "reasonCodes": r.Reasons, "historical": args[0] == "history", "withdrawn": withdrawn, "valuePresent": r.Value != nil, "asOf": r.AsOf, "evaluationAt": r.EvaluationAt})
		}
	default:
		return errors.New("unsupported external action")
	}
	if err != nil {
		return errors.New("external candidate operation failed")
	}
	return json.NewEncoder(out).Encode(map[string]string{"status": "committed", "channel": "candidate", "model": ve.Model})
}
