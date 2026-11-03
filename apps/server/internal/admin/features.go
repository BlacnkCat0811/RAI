package admin

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"time"

	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
	wf "rai/server/internal/workoutfeatures"
)

// QueueFeatureCandidate is explicit and bounded to one Workout. It does not
// activate metrics or scan historical workouts. An operation retry is inert.
func QueueFeatureCandidate(ctx context.Context, s *postgres.Store, dataset, workout, operation, actor string) error {
	if !syncing.ValidID(dataset) || !syncing.ValidID(workout) || !syncing.ValidID(operation) || len(actor) < 1 || len(actor) > 200 {
		return errors.New("explicit dataset, workout, operation and actor required")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockAdministrativeDataset(ctx, tx, dataset); err != nil {
		return err
	}
	details := map[string]string{"workoutID": workout, "stackID": wf.StackID(), "configHash": wf.DefaultPolicy().Hash(), "action": "compute"}
	b, _ := json.Marshal(details)
	var previous []byte
	err = tx.QueryRow(ctx, `SELECT details FROM admin_operations WHERE operation_id=$1 AND operation_type='feature_candidate' AND subject_dataset=$2 AND actor=$3`, operation, dataset, actor).Scan(&previous)
	if err == nil {
		var p map[string]string
		_ = json.Unmarshal(previous, &p)
		pb, _ := json.Marshal(p)
		if string(pb) != string(b) {
			return errors.New("operation conflict")
		}
		return tx.Commit(ctx)
	}
	var from, through time.Time
	err = tx.QueryRow(ctx, `SELECT event_start,event_end FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2`, dataset, workout).Scan(&from, &through)
	if err != nil {
		return err
	}
	legacy := sha256.Sum256([]byte("legacy:workout.bundle/v2:training_state.bundle/v2"))
	_, err = tx.Exec(ctx, `INSERT INTO analysis_model_channels(dataset_id,channel,stack_id,config_sha256,assignment_version) VALUES($1,'active','legacy',$2,1) ON CONFLICT DO NOTHING`, dataset, legacy[:])
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO analysis_model_channels(dataset_id,channel,stack_id,config_sha256,assignment_version) VALUES($1,'candidate',$2,decode($3,'hex'),1) ON CONFLICT DO NOTHING`, dataset, wf.StackID(), wf.DefaultPolicy().Hash())
	if err != nil {
		return err
	}
	var matches bool
	if err = tx.QueryRow(ctx, `SELECT stack_id=$2 AND config_sha256=decode($3,'hex') FROM analysis_model_channels WHERE dataset_id=$1 AND channel='candidate'`, dataset, wf.StackID(), wf.DefaultPolicy().Hash()).Scan(&matches); err != nil {
		return err
	}
	if !matches {
		return errors.New("candidate assignment conflict")
	}
	_, err = tx.Exec(ctx, `INSERT INTO admin_operations(operation_id,operation_type,actor,status,subject_dataset,details) VALUES($1,'feature_candidate',$2,'committed',$3,$4)`, operation, actor, dataset, b)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `SELECT rai_analysis_dirty($1,$2,$3,$4,$5)`, dataset, wf.Scope(workout), wf.JobKind, from, through)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SetFeatureHRReference appends explicit event-time input and dirties only its
// bounded effective range. Legacy parameters and scores are untouched.
func SetFeatureHRReference(ctx context.Context, s *postgres.Store, dataset string, p wf.Parameter) error {
	if !syncing.ValidID(dataset) || !syncing.ValidID(p.ID) || p.EffectiveThrough == nil || *p.EffectiveThrough <= p.EffectiveFrom || *p.EffectiveThrough-p.EffectiveFrom > 366*86400e6 || p.Provenance == "" || len(p.Provenance) > 200 || (!p.Revoked && (p.RestHR <= 0 || p.MaxHR <= p.RestHR || p.MaxHR > 250)) {
		return errors.New("invalid bounded HR reference")
	}
	if p.Supersedes != "" && !syncing.ValidID(p.Supersedes) {
		return errors.New("invalid supersedes")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockAdministrativeDataset(ctx, tx, dataset); err != nil {
		return err
	}
	// Only the typed physiological payload is stored; authoritative identity and
	// effective/recording times are columns, never accepted from payload JSON.
	payload := struct {
		RestHR  float64 `json:"restingHR"`
		MaxHR   float64 `json:"maxHR"`
		Revoked bool    `json:"revoked"`
	}{p.RestHR, p.MaxHR, p.Revoked}
	b, err := json.Marshal(payload)
	if err != nil {
		return errors.New("invalid HR reference")
	}
	canonical, _ := json.Marshal(struct {
		DatasetID  string          `json:"datasetID"`
		ID         string          `json:"id"`
		From       int64           `json:"effectiveFromUs"`
		Through    int64           `json:"effectiveThroughUs"`
		Supersedes string          `json:"supersedes"`
		Provenance string          `json:"provenance"`
		Payload    json.RawMessage `json:"payload"`
	}{dataset, p.ID, p.EffectiveFrom, *p.EffectiveThrough, p.Supersedes, p.Provenance, b})
	h := sha256.Sum256(canonical)
	var supersedes *string
	if p.Supersedes != "" {
		supersedes = &p.Supersedes
	}
	_, err = tx.Exec(ctx, `INSERT INTO athlete_model_inputs(dataset_id,id,kind,subject_key,effective_from,effective_through,supersedes_id,schema_version,content_sha256,payload,provenance) VALUES($1,$2,'hr_reference','athlete',$3,$4,$5,'rai.hr_reference/1',$6,$7,$8)`, dataset, p.ID, time.UnixMicro(p.EffectiveFrom), time.UnixMicro(*p.EffectiveThrough), supersedes, h[:], b, p.Provenance)
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT w.workout_uuid::text,w.event_start,w.event_end FROM workouts w JOIN analysis_model_channels c ON c.dataset_id=w.dataset_id AND c.channel='candidate' WHERE w.dataset_id=$1 AND w.event_start>=$2 AND w.event_start<$3 ORDER BY w.event_start,w.workout_uuid LIMIT 1001`, dataset, time.UnixMicro(p.EffectiveFrom), time.UnixMicro(*p.EffectiveThrough))
	if err != nil {
		return err
	}
	type ref struct {
		id   string
		a, b time.Time
	}
	refs := []ref{}
	for rows.Next() {
		var x ref
		if err = rows.Scan(&x.id, &x.a, &x.b); err != nil {
			rows.Close()
			return err
		}
		refs = append(refs, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(refs) > 1000 {
		return errors.New("parameter range exceeds bounded capacity")
	}
	for _, x := range refs {
		if _, err = tx.Exec(ctx, `SELECT rai_analysis_dirty($1,$2,$3,$4,$5)`, dataset, wf.Scope(x.id), wf.JobKind, x.a, x.b); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func FeatureCommand(ctx context.Context, url string, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: rai analysis features compute|inspect|promotion-plan [explicit flags]")
	}
	fs := flag.NewFlagSet("features", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dataset := fs.String("dataset", "", "dataset")
	workout := fs.String("workout", "", "workout")
	operation := fs.String("operation", "", "idempotent operation UUID")
	actor := fs.String("actor", "", "audit actor")
	inputFile := fs.String("input-file", "", "bounded explicit HR reference JSON file")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		return errors.New("invalid feature flags")
	}
	s, err := postgres.Open(ctx, url, 4)
	if err != nil {
		return errors.New("feature database configuration failed")
	}
	defer s.Pool.Close()
	if err = s.Ready(ctx); err != nil {
		return errors.New("feature schema not ready")
	}
	switch args[0] {
	case "hr-reference":
		if *inputFile == "" {
			return errors.New("explicit input file required")
		}
		file, e := os.Open(*inputFile)
		if e != nil {
			return errors.New("HR reference file unavailable")
		}
		defer file.Close()
		data, e := io.ReadAll(io.LimitReader(file, 4097))
		if e != nil || len(data) > 4096 {
			return errors.New("HR reference file exceeds limit")
		}
		var p wf.Parameter
		if e = syncing.DecodeStrictJSON(data, &p); e != nil {
			return errors.New("invalid HR reference file")
		}
		if e = SetFeatureHRReference(ctx, s, *dataset, p); e != nil {
			return errors.New("HR reference append failed")
		}
		return json.NewEncoder(out).Encode(map[string]string{"status": "input_recorded", "channel": "candidate"})
	case "compute":
		if err = QueueFeatureCandidate(ctx, s, *dataset, *workout, *operation, *actor); err != nil {
			return errors.New("candidate compute request failed")
		}
		return json.NewEncoder(out).Encode(map[string]string{"status": "queued", "channel": "candidate", "stackID": wf.StackID()})
	case "inspect":
		f, run, err := s.CandidateFeature(ctx, *dataset, *workout)
		if err != nil {
			return errors.New("candidate unavailable or invalidated")
		}
		return json.NewEncoder(out).Encode(struct {
			RunID   string             `json:"runID"`
			Feature wf.WorkoutFeatures `json:"feature"`
		}{run, f})
	case "promotion-plan":
		return json.NewEncoder(out).Encode(map[string]any{"stackID": wf.StackID(), "promotable": false, "reason": "feature_only_no_product_consumer", "active": "legacy", "rollback": "legacy_active_unchanged"})
	default:
		return errors.New("unsupported feature action")
	}
}
