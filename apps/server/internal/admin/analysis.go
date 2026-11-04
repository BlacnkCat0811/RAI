package admin

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"github.com/jackc/pgx/v5"
	"io"
	"strings"
	"time"

	"rai/server/internal/analysis"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
)

type RecomputeRequest struct {
	Dataset   string
	Scope     string
	Algorithm string
	From      string
	Through   string
	DryRun    bool
}
type RecomputePlan struct {
	Scopes []string `json:"scopes"`
	DryRun bool     `json:"dryRun"`
}

// Recompute is CLI-only. It requires explicit dataset, scope, algorithm and
// calendar range; there is no implicit all-dataset/all-history mode.
func Recompute(ctx context.Context, s *postgres.Store, r RecomputeRequest) (RecomputePlan, error) {
	out := RecomputePlan{Scopes: []string{}, DryRun: r.DryRun}
	if !syncing.ValidID(r.Dataset) {
		return out, errors.New("explicit dataset required")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	var tz string
	var retired bool
	datasetSQL := `SELECT timezone,retired_at IS NOT NULL FROM datasets WHERE id=$1`
	if !r.DryRun {
		datasetSQL += ` FOR UPDATE`
	}
	if err = tx.QueryRow(ctx, datasetSQL, r.Dataset).Scan(&tz, &retired); err != nil {
		return out, err
	}
	if retired && !r.DryRun {
		return out, syncing.Fail(409, "dataset_retired")
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return out, errors.New("invalid dataset timezone")
	}
	from, err := time.ParseInLocation("2006-01-02", r.From, loc)
	if err != nil {
		return out, errors.New("explicit from date required")
	}
	through, err := time.ParseInLocation("2006-01-02", r.Through, loc)
	if err != nil || through.Before(from) || through.After(from.AddDate(0, 0, 365)) {
		return out, errors.New("range must be 1 to 366 calendar days")
	}
	kind := "analysis.workout"
	if r.Scope == analysis.StateScope {
		if r.Algorithm != analysis.StateAlgorithm+"/"+analysis.StateVersion {
			return out, errors.New("unsupported algorithm")
		}
		kind = "analysis.training_state"
		today := time.Now().In(loc)
		today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc)
		if through.After(today) {
			return out, errors.New("state range cannot end in the future")
		}
		var df, dt time.Time
		var pending bool
		e := tx.QueryRow(ctx, `SELECT dirty_from,dirty_through,generation>clean_generation FROM dirty_scopes WHERE dataset_id=$1 AND scope_key=$2`, r.Dataset, r.Scope).Scan(&df, &dt, &pending)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return out, e
		}
		if pending && !r.DryRun {
			df = df.In(loc)
			df = time.Date(df.Year(), df.Month(), df.Day(), 0, 0, 0, 0, loc)
			if df.Before(today.AddDate(0, 0, -179)) {
				df = today.AddDate(0, 0, -179)
			}
			if df.After(today) {
				df = today
			}
			dt = dt.In(loc).AddDate(0, 0, 179)
			dt = time.Date(dt.Year(), dt.Month(), dt.Day(), 0, 0, 0, 0, loc)
			if dt.After(today) {
				dt = today
			}
			if dt.Before(df) {
				dt = df
			}
			if from.After(df) || through.Before(dt) {
				return out, errors.New("state recompute range must cover pending dirty output days")
			}
		}
		out.Scopes = append(out.Scopes, r.Scope)
	} else {
		if r.Algorithm != analysis.WorkoutAlgorithm+"/"+analysis.WorkoutVersion {
			return out, errors.New("unsupported algorithm")
		}
		id := strings.TrimPrefix(r.Scope, "workout:")
		if r.Scope != "workouts" && !syncing.ValidID(id) {
			return out, errors.New("scope must be workout:<uuid>, workouts, or athlete/default")
		}
		rows, e := tx.Query(ctx, `SELECT 'workout:HKWorkoutTypeIdentifier:'||workout_uuid::text FROM workouts WHERE dataset_id=$1 AND event_start >= $2 AND event_start < $3 AND ($4='workouts' OR workout_uuid=NULLIF($5,'')::uuid) ORDER BY event_start,workout_uuid LIMIT 1001`, r.Dataset, from, through.AddDate(0, 0, 1), r.Scope, map[bool]string{true: "", false: id}[r.Scope == "workouts"])
		if e != nil {
			return out, e
		}
		for rows.Next() {
			var scope string
			if e = rows.Scan(&scope); e != nil {
				rows.Close()
				return out, e
			}
			out.Scopes = append(out.Scopes, scope)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return out, e
		}
		if len(out.Scopes) > 1000 {
			return out, errors.New("recompute scope limit exceeded; narrow range")
		}
	}
	if r.DryRun {
		return out, nil
	}
	payload, _ := json.Marshal(map[string]string{"from": r.From, "through": r.Through, "algorithm": r.Algorithm})
	for _, scope := range out.Scopes {
		if _, err = tx.Exec(ctx, `SELECT rai_analysis_dirty($1,$2,$3,$4,$5)`, r.Dataset, scope, kind, from, through.AddDate(0, 0, 1).Add(-time.Microsecond)); err != nil {
			return out, err
		}
		if kind == "analysis.training_state" {
			if _, err = tx.Exec(ctx, `UPDATE jobs SET payload=$3 WHERE dataset_id=$1 AND scope_key=$2 AND target_generation=(SELECT generation FROM dirty_scopes WHERE dataset_id=$1 AND scope_key=$2)`, r.Dataset, scope, payload); err != nil {
				return out, err
			}
		}
	}
	return out, tx.Commit(ctx)
}

// SetAnalysisParameters accepts explicit coefficients and provenance; there is
// no inferred sex or demographic HRmax. Reanalysis is bounded to current state.
func SetAnalysisParameters(ctx context.Context, s *postgres.Store, dataset string, p analysis.Parameters) error {
	if !syncing.ValidID(dataset) || p.Provenance == "" || len(p.Provenance) > 200 || !analysis.Finite(p.RestHR) || !analysis.Finite(p.MaxHR) || !analysis.Finite(p.A) || !analysis.Finite(p.B) || p.RestHR <= 0 || p.MaxHR <= p.RestHR || p.A <= 0 || p.A > 10 || p.B <= 0 || p.B > 10 {
		return errors.New("invalid explicit parameters")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockAdministrativeDataset(ctx, tx, dataset); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO analysis_parameter_sets(dataset_id,version,resting_hr,max_hr,coefficient_a,coefficient_b,provenance) SELECT $1,COALESCE(max(version),0)+1,$2,$3,$4,$5,$6 FROM analysis_parameter_sets WHERE dataset_id=$1`, dataset, p.RestHR, p.MaxHR, p.A, p.B, p.Provenance); err != nil {
		return err
	}
	// This is an explicit administrative operation, never a sync request.
	rows, err := tx.Query(ctx, `SELECT workout_uuid::text,event_start,event_end FROM workouts w JOIN datasets d ON d.id=w.dataset_id WHERE dataset_id=$1 AND event_start >= date_trunc('day',clock_timestamp(),d.timezone)-interval '179 days' ORDER BY event_start,workout_uuid LIMIT 1001`, dataset)
	if err != nil {
		return err
	}
	type ref struct {
		id         string
		start, end time.Time
	}
	var refs []ref
	for rows.Next() {
		var r ref
		if err = rows.Scan(&r.id, &r.start, &r.end); err != nil {
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
	if len(refs) > 1000 {
		return errors.New("parameter update exceeds bounded reanalysis capacity")
	}
	for _, r := range refs {
		if _, err = tx.Exec(ctx, `SELECT rai_analysis_dirty($1,$2,'analysis.workout',$3,$4)`, dataset, "workout:"+syncing.WorkoutType+":"+r.id, r.start, r.end); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
func AnalysisCommand(ctx context.Context, url string, args []string, out io.Writer) error {
	if len(args) > 0 && args[0] == "model-stack" {
		return ModelStackCommand(ctx, url, args[1:], out)
	}
	if len(args) > 0 && args[0] == "readiness" {
		return ReadinessCandidateCommand(ctx, url, args[1:], out)
	}
	if len(args) > 0 && args[0] == "recovery" {
		return RecoveryCandidateCommand(ctx, url, args[1:], out)
	}
	if len(args) > 0 && args[0] == "vo2max-external" {
		return VO2ExternalCommand(ctx, url, args[1:], out)
	}
	if len(args) > 0 && args[0] == "performance" {
		return PerformanceCommand(ctx, url, args[1:], out)
	}
	if len(args) > 0 && args[0] == "load" {
		return LoadCommand(ctx, url, args[1:], out)
	}
	if len(args) > 0 && args[0] == "features" {
		return FeatureCommand(ctx, url, args[1:], out)
	}
	if len(args) == 0 {
		return errors.New("usage: rai analysis recompute|parameters [explicit flags]")
	}
	fs := flag.NewFlagSet("analysis", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dataset := fs.String("dataset", "", "dataset UUID")
	scope := fs.String("scope", "", "explicit scope")
	algorithm := fs.String("algorithm", "", "bundle/version")
	from := fs.String("from", "", "calendar date")
	through := fs.String("through", "", "calendar date")
	dry := fs.Bool("dry-run", false, "list scopes")
	rest := fs.Float64("resting-hr", 0, "explicit measured resting HR")
	maxHR := fs.Float64("max-hr", 0, "explicit measured max HR")
	a := fs.Float64("coefficient-a", 0, "explicit TRIMP coefficient")
	b := fs.Float64("coefficient-b", 0, "explicit TRIMP coefficient")
	provenance := fs.String("provenance", "", "parameter source")
	if err := fs.Parse(args[1:]); err != nil || len(fs.Args()) != 0 {
		return errors.New("invalid analysis flags")
	}
	s, err := postgres.Open(ctx, url, 4)
	if err != nil {
		return errors.New("analysis database configuration failed")
	}
	defer s.Pool.Close()
	if err = s.Ready(ctx); err != nil {
		return errors.New("analysis schema not ready")
	}
	switch args[0] {
	case "recompute":
		v, e := Recompute(ctx, s, RecomputeRequest{*dataset, *scope, *algorithm, *from, *through, *dry})
		if e != nil {
			return errors.New("analysis recompute failed; check explicit scope, range, algorithm and database")
		}
		return json.NewEncoder(out).Encode(v)
	case "parameters":
		if *dry {
			return errors.New("parameters does not accept dry-run")
		}
		if err = SetAnalysisParameters(ctx, s, *dataset, analysis.Parameters{RestHR: *rest, MaxHR: *maxHR, A: *a, B: *b, Provenance: *provenance}); err != nil {
			return errors.New("analysis parameter update failed")
		}
		return json.NewEncoder(out).Encode(map[string]string{"status": "parameters_recorded"})
	}
	return errors.New("unknown analysis command")
}

func lockAdministrativeDataset(ctx context.Context, tx pgx.Tx, dataset string) error {
	var retired bool
	if e := tx.QueryRow(ctx, `SELECT retired_at IS NOT NULL FROM datasets WHERE id=$1 FOR UPDATE`, dataset).Scan(&retired); e != nil {
		return e
	}
	if retired {
		return syncing.Fail(409, "dataset_retired")
	}
	return nil
}
