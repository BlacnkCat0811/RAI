package admin

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	pf "rai/server/internal/performance"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
	wf "rai/server/internal/workoutfeatures"
)

// QueuePerformanceCandidate enrolls only a bounded synthetic/development dataset
// when explicitly requested by an administrator. It never changes active models.
func QueuePerformanceCandidate(ctx context.Context, s *postgres.Store, dataset, operation, actor string) error {
	if !syncing.ValidID(dataset) || !syncing.ValidID(operation) || actor == "" || len(actor) > 200 {
		return errors.New("explicit dataset operation actor required")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockAdministrativeDataset(ctx, tx, dataset); err != nil {
		return err
	}
	details := map[string]string{"action": "performance_compute", "stackID": wf.StackID(), "model": pf.CurveModel + "/" + pf.Version, "configHash": pf.ConfigHash()}
	body, _ := json.Marshal(details)
	var previous []byte
	err = tx.QueryRow(ctx, `SELECT details FROM admin_operations WHERE operation_id=$1 AND operation_type='feature_candidate' AND subject_dataset=$2 AND actor=$3`, operation, dataset, actor).Scan(&previous)
	if err == nil {
		var p map[string]string
		if json.Unmarshal(previous, &p) != nil || pf.Hash(p) != pf.Hash(details) {
			return errors.New("operation conflict")
		}
		return tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	legacy := sha256.Sum256([]byte("legacy:workout.bundle/v2:training_state.bundle/v2"))
	if _, err = tx.Exec(ctx, `INSERT INTO analysis_model_channels(dataset_id,channel,stack_id,config_sha256,assignment_version) VALUES($1,'active','legacy',$2,1) ON CONFLICT DO NOTHING`, dataset, legacy[:]); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO analysis_model_channels(dataset_id,channel,stack_id,config_sha256,assignment_version) VALUES($1,'candidate',$2,decode($3,'hex'),1) ON CONFLICT DO NOTHING`, dataset, wf.StackID(), wf.DefaultPolicy().Hash()); err != nil {
		return err
	}
	var matches bool
	if err = tx.QueryRow(ctx, `SELECT stack_id=$2 AND config_sha256=decode($3,'hex') AND (NOT extension_configs ? 'rai.performance_curve/v1' OR extension_configs->>'rai.performance_curve/v1'=$4) FROM analysis_model_channels WHERE dataset_id=$1 AND channel='candidate'`, dataset, wf.StackID(), wf.DefaultPolicy().Hash(), pf.ConfigHash()).Scan(&matches); err != nil {
		return err
	}
	if !matches {
		return errors.New("candidate assignment conflict")
	}
	// Explicit enrollment ends an external-only slot's M1 suppression.
	if _, err = tx.Exec(ctx, `UPDATE analysis_model_channels SET extension_configs=extension_configs-'rai.vo2max.external_only/v1' WHERE dataset_id=$1 AND channel='candidate' AND extension_configs ? 'rai.vo2max.external_only/v1'`, dataset); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE analysis_model_channels SET extension_configs=extension_configs||jsonb_build_object('rai.performance_curve/v1',$2::text) WHERE dataset_id=$1 AND channel='candidate'`, dataset, pf.ConfigHash()); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO admin_operations(operation_id,operation_type,actor,status,subject_dataset,details) VALUES($1,'feature_candidate',$2,'committed',$3,$4)`, operation, actor, dataset, body); err != nil {
		return err
	}
	// Features already published are reused byte for byte. Enrollment queues only
	// missing/dirty feature generations. No M2 load enrollment is implied.
	at := time.Now().UTC()
	var newest *time.Time
	if err = tx.QueryRow(ctx, `SELECT max(event_end) FROM workouts WHERE dataset_id=$1 AND event_end>=$2 AND event_end<=$3`, dataset, at.AddDate(0, 0, -90), at).Scan(&newest); err != nil {
		return err
	}
	begin := at.AddDate(0, 0, -103)
	if newest != nil {
		begin = newest.AddDate(0, 0, -103)
	}
	rows, err := tx.Query(ctx, `SELECT w.workout_uuid::text,w.event_start,w.event_end,COALESCE(d.generation=d.clean_generation AND p.published_run_id IS NOT NULL,false) FROM workouts w
 LEFT JOIN dirty_scopes d ON d.dataset_id=w.dataset_id AND d.scope_key=$2||w.workout_uuid::text
 LEFT JOIN analysis_publications p ON p.dataset_id=d.dataset_id AND p.scope_key=d.scope_key AND p.metric_family='workout_model' AND p.generation=d.generation
 WHERE w.dataset_id=$1 AND w.event_end>=$3 AND w.event_end<=$4 ORDER BY w.workout_uuid LIMIT 97`, dataset, "model/"+wf.StackID()+"/workout/", begin, at)
	if err != nil {
		return err
	}
	type ref struct {
		id            string
		from, through time.Time
		fresh         bool
	}
	refs := []ref{}
	for rows.Next() {
		var r ref
		if err = rows.Scan(&r.id, &r.from, &r.through, &r.fresh); err != nil {
			rows.Close()
			return err
		}
		refs = append(refs, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(refs) > pf.MaxWorkouts {
		return errors.New("performance input limit")
	}
	for _, r := range refs {
		if r.fresh {
			continue
		}
		if err = postgres.MarkAnalysisDirty(ctx, tx, dataset, wf.Scope(r.id), wf.JobKind, r.from, r.through); err != nil {
			return err
		}
	}
	if err = postgres.MarkAnalysisDirty(ctx, tx, dataset, pf.Scope(), pf.JobKind, at.AddDate(0, 0, -90), at); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func PerformanceCommand(ctx context.Context, url string, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: rai analysis performance compute|recompute|inspect --dataset UUID [--operation UUID --actor NAME]")
	}
	fs := flag.NewFlagSet("performance", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dataset := fs.String("dataset", "", "dataset")
	operation := fs.String("operation", "", "operation UUID")
	actor := fs.String("actor", "", "audit actor")
	run := fs.String("run", "", "historical run UUID")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		return errors.New("invalid performance flags")
	}
	s, err := postgres.Open(ctx, url, 4)
	if err != nil {
		return errors.New("performance database configuration failed")
	}
	defer s.Pool.Close()
	if err = s.Ready(ctx); err != nil {
		return errors.New("performance schema not ready")
	}
	switch args[0] {
	case "promotion-plan":
		return json.NewEncoder(out).Encode(map[string]any{"models": []string{pf.CurveModel + "/" + pf.Version, pf.AbilityModel + "/" + pf.Version, pf.EnduranceModel + "/" + pf.Version}, "promotable": false, "reason": "candidate_only_no_product_consumer", "active": "legacy", "rollback": "legacy_active_unchanged_immutable_history_retained"})
	case "compute", "recompute":
		if err = QueuePerformanceCandidate(ctx, s, *dataset, *operation, *actor); err != nil {
			return errors.New("candidate performance request failed")
		}
		return json.NewEncoder(out).Encode(map[string]string{"status": "queued", "channel": "candidate", "model": pf.CurveModel + "/" + pf.Version})
	case "history":
		r, e := s.HistoricalPerformance(ctx, *dataset, *run)
		if e != nil {
			return errors.New("historical performance unavailable")
		}
		return json.NewEncoder(out).Encode(map[string]any{"runID": *run, "historical": true, "result": r})
	case "inspect":
		r, id, e := s.CandidatePerformance(ctx, *dataset, time.Now().UTC())
		if e != nil {
			return errors.New("candidate performance unavailable")
		}
		return json.NewEncoder(out).Encode(map[string]any{"runID": id, "result": r})
	}
	return errors.New("unsupported performance action")
}
