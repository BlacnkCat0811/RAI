package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	ve "rai/server/internal/vo2maxexternal"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/analysis"
	pf "rai/server/internal/performance"
	rd "rai/server/internal/readiness"
	rc "rai/server/internal/recovery"
	"rai/server/internal/syncing"
	tl "rai/server/internal/trainingload"
	wf "rai/server/internal/workoutfeatures"
)

const maxAnalysisPoints = 250000

func (s *Store) LoadAnalysis(ctx context.Context, l Lease, asOf time.Time) (analysis.Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return analysis.Snapshot{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout='5s'; SET LOCAL transaction_timeout='20s'`); err != nil {
		return analysis.Snapshot{}, err
	}
	in := analysis.Snapshot{InputVersion: "rai.analysis.input/v1"}
	var tz string
	if err = tx.QueryRow(ctx, `SELECT d.commit_rev,d.timezone,x.generation,x.dirty_from,x.dirty_through FROM datasets d JOIN dirty_scopes x ON x.dataset_id=d.id WHERE d.id=$1 AND x.scope_key=$2`, l.DatasetID, l.ScopeKey).Scan(&in.Revision, &tz, &in.Generation, &in.From, &in.Through); err != nil {
		return in, err
	}
	in.From = in.From.UTC()
	in.Through = in.Through.UTC()
	var requested struct {
		Algorithm string `json:"algorithm"`
	}
	if len(l.Payload) > 0 {
		if err = json.Unmarshal(l.Payload, &requested); err != nil {
			return in, analysis.Invalid()
		}
	}
	expected, e := analysis.Algorithm(l.Kind)
	if e != nil {
		return in, e
	}
	if requested.Algorithm != "" && requested.Algorithm != expected+"/"+analysis.BundleVersion(l.Kind) {
		return in, &analysis.Failure{Code: "analysis_unsupported_algorithm"}
	}
	// Old generations cannot be reconstructed from mutable availability. Record
	// an explicit superseded attempt, never label today's facts as an old input.
	if in.Generation != l.TargetGeneration {
		return in, nil
	}
	switch l.Kind {
	case rd.JobKind:
		err = s.loadReadinessInput(ctx, tx, l, &in, asOf)
	case rc.JobKind:
		err = s.loadRecoveryInput(ctx, tx, l, &in, asOf)
	case ve.JobKind:
		err = s.loadVO2ExternalInput(ctx, tx, l, &in, asOf)
	case pf.JobKind:
		err = s.loadPerformanceInput(ctx, tx, l, &in, asOf, tz)
	case tl.JobKind:
		err = s.loadTrainingLoadInput(ctx, tx, l, &in, tz)
	case tl.StateJobKind:
		err = s.loadFiniteStateInput(ctx, tx, l, &in, asOf, tz)
	case wf.JobKind:
		err = s.loadFeatureInput(ctx, tx, l, &in, asOf)
		if err == nil {
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM analysis_model_dependencies WHERE dataset_id=$1 AND upstream_scope=$2 AND downstream_scope LIKE 'model/%/workout_load/%')`, l.DatasetID, l.ScopeKey).Scan(&in.ExposureRequired)
		}
	case "analysis.workout":
		err = s.loadWorkoutInput(ctx, tx, l, &in)
	case "analysis.training_state":
		err = s.loadStateInput(ctx, tx, l, asOf, tz, &in)
	case "analysis.sample", "analysis.series":
		err = s.loadDependencies(ctx, tx, l, &in)
	default:
		err = &analysis.Failure{Code: "analysis_unsupported_algorithm"}
	}
	if err != nil {
		return in, err
	}
	return in, tx.Commit(ctx)
}
func (s *Store) loadWorkoutInput(ctx context.Context, tx pgx.Tx, l Lease, in *analysis.Snapshot) error {
	id := strings.TrimPrefix(l.ScopeKey, "workout:"+syncing.WorkoutType+":")
	if !syncing.ValidID(id) {
		return analysis.Invalid()
	}
	w := &analysis.WorkoutInput{ID: id}
	in.Workout = w
	var body []byte
	var hash string
	var start, end time.Time
	var duration int64
	err := tx.QueryRow(ctx, AnalysisWorkoutSQL, l.DatasetID, id).Scan(&body, &start, &end, &duration, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		w.Deleted = true
		return nil
	}
	if err != nil {
		return err
	}
	w.Start = start.UnixMicro()
	w.End = end.UnixMicro()
	w.Duration = float64(duration) / 1e6
	var payloadRaw []byte
	importErr := tx.QueryRow(ctx, `SELECT payload FROM workout_imports WHERE dataset_id=$1 AND workout_uuid=$2 ORDER BY imported_at DESC,bundle_sha256 DESC LIMIT 1`, l.DatasetID, id).Scan(&payloadRaw)
	if importErr == nil {
		var stored struct {
			ProtocolVersion int                        `json:"protocolVersion"`
			Observation     *syncing.BundleObservation `json:"observation"`
		}
		if json.Unmarshal(payloadRaw, &stored) != nil {
			return analysis.Invalid()
		}
		switch {
		case stored.ProtocolVersion == 4:
			table, err := syncing.WorkoutBundleV4SourceTable(payloadRaw)
			if err != nil {
				return analysis.Invalid()
			}
			if table.Workout.Classification(nil) != "apple_watch" {
				return analysis.Invalid()
			}
			if err := validateAnalysisProvenance(ctx, tx, l.DatasetID, id); err != nil {
				return err
			}
			w.Lineage = append(w.Lineage, "healthkit-bundle:"+syncing.BundleDigest(payloadRaw))
		case stored.Observation != nil:
			var observed syncing.WorkoutBundle
			if json.Unmarshal(payloadRaw, &observed) != nil || observed.Observation == nil {
				return analysis.Invalid()
			}
			if observed.Observation.Provenance.Workout.Classification(nil) != "apple_watch" {
				return analysis.Invalid()
			}
			if err := validateAnalysisProvenance(ctx, tx, l.DatasetID, id); err != nil {
				return err
			}
			w.Lineage = append(w.Lineage, "healthkit-observation:"+syncing.BundleDigest(observationBytes(observed)))
		default:
			return &analysis.Failure{Code: "analysis_source_unclassified"}
		}
	} else if !errors.Is(importErr, pgx.ErrNoRows) {
		return importErr
	}
	in.From = start.UTC()
	in.Through = end.UTC()
	w.Lineage = append(w.Lineage, "workout:"+id+":"+hash)
	var event syncing.Event
	if err = json.Unmarshal(body, &event); err != nil || event.Workout == nil {
		return analysis.Invalid()
	}
	if event.Workout.TotalDistance != nil {
		w.Distance = analysis.Ptr(event.Workout.TotalDistance.Value)
	}
	rows, err := tx.Query(ctx, `SELECT enrichment_id::text,encode(content_sha256,'hex'),total_distance_m FROM workout_enrichments WHERE dataset_id=$1 AND workout_uuid=$2 ORDER BY observed_at_us,source_version,enrichment_id`, l.DatasetID, id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var key, h string
		var distance *float64
		if err = rows.Scan(&key, &h, &distance); err != nil {
			rows.Close()
			return err
		}
		w.Lineage = append(w.Lineage, "enrichment:"+key+":"+h)
		if distance != nil {
			if w.Distance != nil && *w.Distance != *distance {
				rows.Close()
				return analysis.Invalid()
			}
			w.Distance = distance
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var p analysis.Parameters
	err = tx.QueryRow(ctx, `SELECT version,resting_hr,max_hr,coefficient_a,coefficient_b,provenance FROM analysis_parameter_sets WHERE dataset_id=$1 ORDER BY version DESC LIMIT 1`, l.DatasetID).Scan(&p.Version, &p.RestHR, &p.MaxHR, &p.A, &p.B, &p.Provenance)
	if err == nil {
		w.Parameters = &p
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	rows, err = tx.Query(ctx, `SELECT payload FROM workout_statistics WHERE dataset_id=$1 AND workout_uuid=$2 ORDER BY sample_type`, l.DatasetID, id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			rows.Close()
			return err
		}
		var stat syncing.WorkoutStatistic
		if err = json.Unmarshal(b, &stat); err != nil {
			rows.Close()
			return analysis.Invalid()
		}
		s := analysis.Statistic{Type: stat.Type}
		if stat.Average != nil {
			s.Average = analysis.Ptr(stat.Average.Value)
		}
		if stat.Maximum != nil {
			s.Maximum = analysis.Ptr(stat.Maximum.Value)
		}
		w.Statistics = append(w.Statistics, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// Exactly associated facts; pending series is ignored and its durable parent
	// summary remains a summary, not a complete trace.
	rows, err = tx.Query(ctx, AnalysisSamplesSQL, l.DatasetID, id)
	if err != nil {
		return err
	}
	hasSeries := false
	for rows.Next() {
		var o analysis.Observation
		var a, b time.Time
		var h string
		var sid, m *string
		if err = rows.Scan(&o.Type, &o.ID, &a, &b, &o.Value, &o.Unit, &h, &sid, &m); err != nil {
			rows.Close()
			return err
		}
		o.Start = a.UnixMicro()
		o.End = b.UnixMicro()
		w.Lineage = append(w.Lineage, "sample:"+o.Type+":"+o.ID+":"+h)
		if sid != nil {
			hasSeries = true
			w.Lineage = append(w.Lineage, "series:"+*sid+":"+*m)
		} else {
			w.Observations = append(w.Observations, o)
		}
		if len(w.Lineage) > maxAnalysisPoints {
			rows.Close()
			return &analysis.Failure{Code: "analysis_input_limit"}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// One batched query for every sealed series associated with this Workout.
	if hasSeries {
		rows, err = tx.Query(ctx, `SELECT u.series_id::text,u.sample_type,c.chunk_index,c.points FROM workout_samples a JOIN series_uploads u USING(dataset_id,sample_type,sample_uuid) JOIN series_chunks c USING(dataset_id,series_id) WHERE a.dataset_id=$1 AND a.workout_uuid=$2 AND u.state='sealed' ORDER BY u.sample_type,u.sample_uuid,c.chunk_index`, l.DatasetID, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var sid, kind string
			var index int
			var b []byte
			if err = rows.Scan(&sid, &kind, &index, &b); err != nil {
				rows.Close()
				return err
			}
			var points []syncing.SeriesPoint
			if err = json.Unmarshal(b, &points); err != nil {
				rows.Close()
				return analysis.Invalid()
			}
			if len(w.Observations)+len(points) > maxAnalysisPoints {
				rows.Close()
				return &analysis.Failure{Code: "analysis_input_limit"}
			}
			for i, p := range points {
				w.Observations = append(w.Observations, analysis.Observation{ID: fmt.Sprintf("%s:%d:%d", sid, index, i), Type: kind, Start: p.StartUs, End: p.EndUs, Value: p.Value, Unit: p.Unit})
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}

	rows, err = tx.Query(ctx, AnalysisRouteSQL, l.DatasetID, id)
	if err != nil {
		return err
	}
	last := ""
	for rows.Next() {
		var p analysis.Point
		var stamp time.Time
		var h string
		if err = rows.Scan(&p.Route, &h, &stamp, &p.Latitude, &p.Longitude, &p.Altitude, &p.HorizontalAccuracy, &p.VerticalAccuracy); err != nil {
			rows.Close()
			return err
		}
		p.At = stamp.UnixMicro()
		if p.Route != last {
			w.Lineage = append(w.Lineage, "route:"+p.Route+":"+h)
			last = p.Route
		}
		// Registration permits a small GPS clock skew. Keep the immutable
		// route/hash intact, but only analyze points in the Workout window.
		if stamp.Before(start) || stamp.After(end) {
			continue
		}
		w.Route = append(w.Route, p)
		if len(w.Route) > maxAnalysisPoints {
			rows.Close()
			return &analysis.Failure{Code: "analysis_input_limit"}
		}
	}
	err = rows.Err()
	rows.Close()
	return err
}
func (s *Store) loadDependencies(ctx context.Context, tx pgx.Tx, l Lease, in *analysis.Snapshot) error {
	parts := strings.Split(l.ScopeKey, ":")
	if len(parts) != 3 {
		return analysis.Invalid()
	}
	var rows pgx.Rows
	var err error
	if parts[0] == "series" || syncing.ValidID(parts[2]) {
		rows, err = tx.Query(ctx, `SELECT DISTINCT 'workout:HKWorkoutTypeIdentifier:'||workout_uuid::text FROM workout_samples WHERE dataset_id=$1 AND sample_type=$2 AND sample_uuid=$3 ORDER BY 1`, l.DatasetID, parts[1], parts[2])
	} else {
		day, e := time.Parse("20060102", parts[2])
		if e != nil {
			return analysis.Invalid()
		}
		rows, err = tx.Query(ctx, `SELECT DISTINCT 'workout:HKWorkoutTypeIdentifier:'||a.workout_uuid::text FROM health_samples h JOIN workout_samples a USING(dataset_id,sample_type,sample_uuid) WHERE h.dataset_id=$1 AND h.sample_type=$2 AND h.event_start >= $3 AND h.event_start < $4 ORDER BY 1`, l.DatasetID, parts[1], day, day.AddDate(0, 0, 1))
	}
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var scope string
		if err = rows.Scan(&scope); err != nil {
			return err
		}
		in.Dependencies = append(in.Dependencies, scope)
	}
	return rows.Err()
}
func (s *Store) loadStateInput(ctx context.Context, tx pgx.Tx, l Lease, asOf time.Time, tz string, in *analysis.Snapshot) error {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return analysis.Invalid()
	}
	today := asOf.In(loc)
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, loc)
	from := in.From.In(loc)
	from = time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc)
	through := in.Through.In(loc).AddDate(0, 0, 179)
	through = time.Date(through.Year(), through.Month(), through.Day(), 0, 0, 0, 0, loc)
	// A first import remains bounded; explicit history has explicit output dates.
	if from.Before(today.AddDate(0, 0, -179)) {
		from = today.AddDate(0, 0, -179)
	}
	if from.After(today) {
		from = today
	}
	if through.After(today) {
		through = today
	}
	if through.Before(from) {
		through = from
	}
	var payload struct {
		From    string `json:"from"`
		Through string `json:"through"`
	}
	if len(l.Payload) > 0 {
		if err = json.Unmarshal(l.Payload, &payload); err != nil {
			return analysis.Invalid()
		}
	}
	if payload.From != "" {
		a, e := time.ParseInLocation("2006-01-02", payload.From, loc)
		if e != nil {
			return analysis.Invalid()
		}
		b, e := time.ParseInLocation("2006-01-02", payload.Through, loc)
		if e != nil || b.Before(a) || b.After(today) || b.After(a.AddDate(0, 0, 365)) {
			return analysis.Invalid()
		}
		from = a
		through = b
	}
	in.State = &analysis.StateInput{From: from.Format("2006-01-02"), Through: through.Format("2006-01-02"), Timezone: tz}
	in.From = from.AddDate(0, 0, -179).UTC()
	in.Through = through.AddDate(0, 0, 1).Add(-time.Microsecond).UTC()
	rows, err := tx.Query(ctx, AnalysisStateLoadsSQL, l.DatasetID, in.From, in.Through.Add(time.Microsecond))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var load analysis.Load
		var at time.Time
		var run *string
		var current bool
		if err = rows.Scan(&load.WorkoutID, &at, &run, &load.Value, &current); err != nil {
			return err
		}
		load.Day = at.In(loc).Format("2006-01-02")
		if run != nil {
			load.RunID = *run
		}
		if !current {
			return &analysis.Failure{Code: "analysis_dependency_pending", Retryable: true}
		}
		in.State.Loads = append(in.State.Loads, load)
		if len(in.State.Loads) > maxAnalysisPoints {
			return &analysis.Failure{Code: "analysis_input_limit"}
		}
	}
	return rows.Err()
}

// AnalysisWorkoutSQL is shared with actual EXPLAIN/ANALYZE acceptance tests.
const AnalysisWorkoutSQL = `SELECT payload,event_start,event_end,duration_us,encode(content_sha256,'hex') FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2`

// AnalysisSamplesSQL is shared with actual EXPLAIN/ANALYZE acceptance tests.
const AnalysisSamplesSQL = `SELECT h.sample_type,h.sample_uuid::text,h.event_start,h.event_end,h.value_numeric,h.unit,encode(h.content_sha256,'hex'),u.series_id::text,encode(u.manifest_sha256,'hex')
 FROM workout_samples a JOIN health_samples h USING(dataset_id,sample_type,sample_uuid)
 LEFT JOIN series_uploads u ON u.dataset_id=h.dataset_id AND u.sample_type=h.sample_type AND u.sample_uuid=h.sample_uuid AND u.state='sealed'
 WHERE a.dataset_id=$1 AND a.workout_uuid=$2 AND h.value_numeric IS NOT NULL ORDER BY h.sample_type,h.event_start,h.sample_uuid LIMIT 250001`

// AnalysisRouteSQL is shared with actual EXPLAIN/ANALYZE acceptance tests.
const AnalysisRouteSQL = `SELECT r.route_uuid::text,encode(r.manifest_sha256,'hex'),p.recorded_at,p.latitude,p.longitude,p.altitude,p.horizontal_accuracy,p.vertical_accuracy
 FROM workout_routes r JOIN route_points p USING(dataset_id,route_uuid) WHERE r.dataset_id=$1 AND r.workout_uuid=$2 AND r.state='sealed' ORDER BY r.route_uuid,p.sequence LIMIT 250001`

// AnalysisStateLoadsSQL is shared with actual EXPLAIN/ANALYZE acceptance tests.
const AnalysisStateLoadsSQL = `SELECT w.workout_uuid::text,w.event_start,p.published_run_id::text,r.value,
 COALESCE(d.generation=d.clean_generation AND p.generation=d.generation AND COALESCE((ar.lineage->>'parameterVersion')::bigint,0)=COALESCE((SELECT max(version) FROM analysis_parameter_sets WHERE dataset_id=$1),0),false)
 FROM workouts w LEFT JOIN dirty_scopes d ON d.dataset_id=w.dataset_id AND d.scope_key='workout:HKWorkoutTypeIdentifier:'||w.workout_uuid::text
 LEFT JOIN analysis_publications p ON p.dataset_id=w.dataset_id AND p.scope_key=d.scope_key AND p.metric_family='workout'
 LEFT JOIN analysis_runs ar ON ar.dataset_id=p.dataset_id AND ar.id=p.published_run_id
 LEFT JOIN analysis_results r ON r.dataset_id=p.dataset_id AND r.run_id=p.published_run_id AND r.metric='workout_load' AND r.day=p.day
 WHERE w.dataset_id=$1 AND w.event_start >= $2 AND w.event_start < $3 ORDER BY w.event_start,w.workout_uuid LIMIT 250001`
