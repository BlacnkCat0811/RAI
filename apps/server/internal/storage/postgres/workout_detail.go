package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"rai/server/internal/analysis"
	"rai/server/internal/syncing"
	"rai/server/internal/training"
	tl "rai/server/internal/trainingload"
	"rai/server/internal/workoutdetail"
	wf "rai/server/internal/workoutfeatures"

	"github.com/jackc/pgx/v5"
)

// Detail and series reads are deliberately one bounded snapshot: a page that
// mixes facts, a sealed route and a sealed series must not show a torn view
// while a route seal or a late enrichment commits.
type workoutDetailData struct {
	ID           string
	Start, End   time.Time
	DurationUs   int64
	ActivityType int64
	Source       string
	Payload      syncing.Event
	ReceivedAt   time.Time

	Enrichments []enrichmentRow
	Statistics  []syncing.WorkoutStatistic
	Events      []syncing.WorkoutEvent
	Activities  []syncing.WorkoutActivity

	Observations       []workoutdetail.Observation
	SampleCounts       map[string]int
	SeriesCounts       map[string]int
	LastObservedByType map[string]time.Time

	Route       []workoutdetail.RouteSample
	RoutePoints []workoutdetail.RoutePoint
	RouteCount  int
	RouteSealed []time.Time

	FeatureArtifact   *workoutdetail.FeatureAnalysis
	LoadFocus         *tl.WorkoutFocus
	Qualification     *workoutdetail.ModelQualification
	Published         []analysis.Metric
	AnalysisStatus    string
	AnalysisJobID     string
	PublishedAt       time.Time
	AnalysisAvailable bool

	Parameters *analysis.Parameters
	// HR zone bounds: the effective HR reference, else the legacy set.
	ZoneRestHR, ZoneMaxHR float64
	// Distance statistics require a single historical HRR reference, never a
	// current legacy parameter set or a mixture of two references.
	HistoricalRestHR, HistoricalMaxHR float64
	Execution                         *executionRow

	Batches     int
	LastBatchAt time.Time
	LastFactAt  time.Time
}

type enrichmentRow struct {
	Metadata     map[string]any
	DistanceM    *float64
	EnergyKcal   *float64
	ObservedAtUs int64
}

type executionRow struct {
	ID          string
	Revision    int64
	Status      string
	Snapshot    training.Session
	WorkoutID   string
	Notes       string
	CompletedAt *time.Time
	Version     int64
}

func (s *Store) loadWorkoutDetailData(ctx context.Context, p syncing.Principal, id string) (*workoutDetailData, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	d := &workoutDetailData{ID: id, SampleCounts: map[string]int{}, SeriesCounts: map[string]int{}, LastObservedByType: map[string]time.Time{}}
	var payload []byte
	err = tx.QueryRow(ctx, `SELECT event_start,event_end,duration_us,activity_type,source_bundle_id,payload,first_received_at
		FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2`, p.DatasetID, id).
		Scan(&d.Start, &d.End, &d.DurationUs, &d.ActivityType, &d.Source, &payload, &d.ReceivedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, syncing.Fail(404, "workout_not_found")
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(payload, &d.Payload); err != nil || d.Payload.Workout == nil {
		return nil, syncing.Fail(500, "workout_payload_invalid")
	}
	if d.Source == "" && d.Payload.Source != nil {
		d.Source = d.Payload.Source.BundleID
	}

	// Append-only enrichment metadata is merged in observation order. Conflicts
	// are rejected at write time; the read does not choose a winner silently.
	rows, err := tx.Query(ctx, `SELECT total_distance_m,total_energy_kcal,metadata,observed_at_us
		FROM workout_enrichments WHERE dataset_id=$1 AND workout_uuid=$2 ORDER BY observed_at_us,source_version,enrichment_id`, p.DatasetID, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var r enrichmentRow
		var metadata []byte
		if err = rows.Scan(&r.DistanceM, &r.EnergyKcal, &metadata, &r.ObservedAtUs); err != nil {
			rows.Close()
			return nil, err
		}
		_ = json.Unmarshal(metadata, &r.Metadata)
		d.Enrichments = append(d.Enrichments, r)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	if err = tx.QueryRow(ctx, `SELECT count(DISTINCT ec.batch_id),COALESCE(max(b.committed_at),'epoch'::timestamptz)
		FROM entity_changes ec JOIN sync_batches b ON b.dataset_id=ec.dataset_id AND b.batch_id=ec.batch_id
		WHERE ec.dataset_id=$1 AND ec.sample_type=$2 AND ec.sample_uuid=$3`, p.DatasetID, syncing.WorkoutType, id).Scan(&d.Batches, &d.LastBatchAt); err != nil {
		return nil, err
	}

	rows, err = tx.Query(ctx, `SELECT payload FROM workout_statistics WHERE dataset_id=$1 AND workout_uuid=$2 ORDER BY sample_type`, p.DatasetID, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			rows.Close()
			return nil, err
		}
		var stat syncing.WorkoutStatistic
		if err = json.Unmarshal(b, &stat); err != nil {
			rows.Close()
			return nil, syncing.Fail(500, "workout_statistics_invalid")
		}
		d.Statistics = append(d.Statistics, stat)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT payload FROM workout_events WHERE dataset_id=$1 AND workout_uuid=$2 ORDER BY ordinal`, p.DatasetID, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			rows.Close()
			return nil, err
		}
		var event syncing.WorkoutEvent
		if err = json.Unmarshal(b, &event); err != nil {
			rows.Close()
			return nil, syncing.Fail(500, "workout_events_invalid")
		}
		d.Events = append(d.Events, event)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	rows, err = tx.Query(ctx, `SELECT payload FROM workout_activities WHERE dataset_id=$1 AND workout_uuid=$2 ORDER BY ordinal`, p.DatasetID, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var b []byte
		if err = rows.Scan(&b); err != nil {
			rows.Close()
			return nil, err
		}
		var activity syncing.WorkoutActivity
		if err = json.Unmarshal(b, &activity); err != nil {
			rows.Close()
			return nil, syncing.Fail(500, "workout_activities_invalid")
		}
		d.Activities = append(d.Activities, activity)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	// Raw samples remain the fallback when their parent has no sealed series.
	// A sealed series replaces exactly that parent's summary, not the whole type.
	rows, err = tx.Query(ctx, `SELECT h.sample_type,h.event_start,h.event_end,h.value_numeric,COALESCE(h.unit,'')
		FROM workout_samples a JOIN health_samples h USING(dataset_id,sample_type,sample_uuid)
		LEFT JOIN series_uploads u USING(dataset_id,sample_type,sample_uuid)
		WHERE a.dataset_id=$1 AND a.workout_uuid=$2 AND h.value_numeric IS NOT NULL
		  AND (u.series_id IS NULL OR u.state<>'sealed')
		ORDER BY h.sample_type,h.event_start,h.sample_uuid`, p.DatasetID, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var o workoutdetail.Observation
		var start, end time.Time
		if err = rows.Scan(&o.Type, &start, &end, &o.Value, &o.Unit); err != nil {
			rows.Close()
			return nil, err
		}
		o.StartUs, o.EndUs = start.UnixMicro(), end.UnixMicro()
		d.Observations = append(d.Observations, o)
		d.SampleCounts[o.Type]++
		rememberObservationTime(d, o)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	rows, err = tx.Query(ctx, `SELECT u.sample_type,u.sample_uuid::text,u.sealed_at,c.points
		FROM workout_samples a
		JOIN series_uploads u USING(dataset_id,sample_type,sample_uuid)
		JOIN series_chunks c USING(dataset_id,series_id)
		WHERE a.dataset_id=$1 AND a.workout_uuid=$2 AND u.state='sealed'
		ORDER BY u.sample_type,u.sample_uuid,c.chunk_index`, p.DatasetID, id)
	if err != nil {
		return nil, err
	}
	sealedMap := map[string]time.Time{}
	for rows.Next() {
		var kind, sampleID string
		var sealedAt time.Time
		var b []byte
		if err = rows.Scan(&kind, &sampleID, &sealedAt, &b); err != nil {
			rows.Close()
			return nil, err
		}
		var points []syncing.SeriesPoint
		if err = json.Unmarshal(b, &points); err != nil {
			rows.Close()
			return nil, syncing.Fail(500, "series_points_invalid")
		}
		d.SeriesCounts[kind] += len(points)
		sealedMap[kind] = maxTime(sealedMap[kind], sealedAt)
		for _, point := range points {
			d.Observations = append(d.Observations, workoutdetail.Observation{Type: kind, StartUs: point.StartUs, EndUs: point.EndUs, Value: point.Value, Unit: point.Unit})
			if t := time.UnixMicro(point.StartUs).UTC(); t.After(d.LastObservedByType[kind]) {
				d.LastObservedByType[kind] = t
			}
		}
		if len(d.Observations) > 500000 {
			rows.Close()
			return nil, syncing.Fail(413, "workout_detail_too_large")
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for kind, t := range sealedMap {
		d.LastObservedByType[kind] = maxTime(d.LastObservedByType[kind], t)
	}

	rows, err = tx.Query(ctx, `SELECT p.recorded_at,p.latitude,p.longitude,p.altitude,p.vertical_accuracy,p.speed,r.sealed_at
		FROM workout_routes r JOIN route_points p ON p.dataset_id=r.dataset_id AND p.route_uuid=r.route_uuid
		WHERE r.dataset_id=$1 AND r.workout_uuid=$2 AND r.state='sealed'
		ORDER BY r.route_uuid,p.sequence`, p.DatasetID, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var at time.Time
		var lat, lon float64
		var alt, accuracy, speed *float64
		var sealedAt time.Time
		if err = rows.Scan(&at, &lat, &lon, &alt, &accuracy, &speed, &sealedAt); err != nil {
			rows.Close()
			return nil, err
		}
		if !at.Before(d.Start) && !at.After(d.End) {
			d.Route = append(d.Route, workoutdetail.RouteSample{AtUs: at.UnixMicro(), Latitude: lat, Longitude: lon, AltitudeM: alt, VerticalAccuracyM: accuracy, SpeedMPS: speed})
		}
		d.RoutePoints = append(d.RoutePoints, workoutdetail.RoutePoint{Latitude: lat, Longitude: lon, ElevationM: alt, SpeedMPS: speed, TimestampUs: at.UnixMicro()})
		d.RouteSealed = append(d.RouteSealed, sealedAt)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	d.RouteCount = len(d.RoutePoints)
	for _, t := range d.RouteSealed {
		d.LastFactAt = maxTime(d.LastFactAt, t)
	}
	for _, t := range sealedMap {
		d.LastFactAt = maxTime(d.LastFactAt, t)
	}
	d.LastFactAt = maxTime(d.LastFactAt, d.ReceivedAt)
	for _, e := range d.Enrichments {
		if t := time.UnixMicro(e.ObservedAtUs).UTC(); t.After(d.LastFactAt) {
			d.LastFactAt = t
		}
	}

	if err = loadFeatureAnalysis(ctx, tx, p, id, d); err != nil {
		return nil, err
	}

	if err = loadModelQualification(ctx, tx, p, id, d); err != nil {
		return nil, err
	}

	// The v2 model stack's latest day release records this Workout's load in
	// model_workout_results; that row is the Workout's published analysis.
	var loadJSON []byte
	var updated time.Time
	err = tx.QueryRow(ctx, `SELECT r.load,r.updated_at FROM model_workout_results r
 JOIN analysis_model_channels c ON c.dataset_id=r.dataset_id AND c.stack_id=r.stack_id AND c.channel='active'
 WHERE r.dataset_id=$1 AND r.workout_uuid=$2 AND r.load IS NOT NULL`, p.DatasetID, id).Scan(&loadJSON, &updated)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		d.AnalysisStatus = "pending"
	case err != nil:
		return nil, err
	default:
		var load tl.Result
		if json.Unmarshal(loadJSON, &load) != nil {
			return nil, analysis.Invalid()
		}
		m := analysis.Metric{Name: "workout_load", Algorithm: load.Model, Version: load.ModelVersion, Value: load.Load, Unit: load.Unit, Quality: "insufficient", Details: map[string]any{"qualityTier": load.QualityTier, "method": load.Method}}
		switch {
		case load.Load != nil && load.QualityTier == tl.TierComplete:
			m.Quality = "available"
		case load.Load != nil:
			m.Quality = "partial"
		default:
			reasons := make([]any, 0, len(load.ReasonCodes))
			for _, r := range load.ReasonCodes {
				reasons = append(reasons, r)
			}
			m.Details["missingComponents"] = reasons
		}
		d.Published = []analysis.Metric{m}
		d.PublishedAt = updated
		d.AnalysisAvailable, d.AnalysisStatus = true, "published"
	}

	var version int64
	var rest, maxHR, a, b float64
	var provenance string
	err = tx.QueryRow(ctx, `SELECT version,resting_hr,max_hr,coefficient_a,coefficient_b,provenance FROM analysis_parameter_sets WHERE dataset_id=$1 ORDER BY version DESC LIMIT 1`, p.DatasetID).Scan(&version, &rest, &maxHR, &a, &b, &provenance)
	if err == nil {
		d.Parameters = &analysis.Parameters{Version: version, RestHR: rest, MaxHR: maxHR, A: a, B: b, Provenance: provenance}
		d.ZoneRestHR, d.ZoneMaxHR = rest, maxHR
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	// HR zones use the same HR reference as the model stack when one is
	// effective at the Workout start; the legacy TRIMP set is only a fallback.
	ref, err := hrReferenceAt(ctx, tx, p.DatasetID, d.Start)
	if err != nil {
		return nil, err
	}
	if ref != nil {
		d.ZoneMaxHR = ref.MaxHR
		if ref.RestingHR != nil {
			d.ZoneRestHR = *ref.RestingHR
			d.HistoricalRestHR, d.HistoricalMaxHR = *ref.RestingHR, ref.MaxHR
		}
	}

	var snapshot []byte
	var completed *time.Time
	var ex executionRow
	err = tx.QueryRow(ctx, `SELECT e.id::text,e.revision,e.status,e.snapshot,e.notes,COALESCE(e.workout_id::text,''),e.completed_at,COALESCE(v.number,0)
		FROM training_executions e
		LEFT JOIN training_effective_versions v ON v.dataset_id=e.dataset_id AND v.plan_id=e.plan_id AND v.id=e.effective_id
		WHERE e.dataset_id=$1 AND e.workout_id=$2 ORDER BY e.created_at DESC LIMIT 1`, p.DatasetID, id).
		Scan(&ex.ID, &ex.Revision, &ex.Status, &snapshot, &ex.Notes, &ex.WorkoutID, &completed, &ex.Version)
	if err == nil {
		if err = json.Unmarshal(snapshot, &ex.Snapshot); err != nil {
			return nil, syncing.Fail(500, "execution_snapshot_invalid")
		}
		ex.CompletedAt = completed
		d.Execution = &ex
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	return d, tx.Commit(ctx)
}

func rememberObservationTime(d *workoutDetailData, o workoutdetail.Observation) {
	t := time.UnixMicro(o.StartUs).UTC()
	if t.After(d.LastObservedByType[o.Type]) {
		d.LastObservedByType[o.Type] = t
	}
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func (d *workoutDetailData) distanceMeters() *float64 {
	if d.Payload.Workout != nil && d.Payload.Workout.TotalDistance != nil {
		return workoutdetail.Ptr(d.Payload.Workout.TotalDistance.Value)
	}
	for i := len(d.Enrichments) - 1; i >= 0; i-- {
		if d.Enrichments[i].DistanceM != nil {
			return d.Enrichments[i].DistanceM
		}
	}
	return nil
}

func (d *workoutDetailData) energyKcal() *float64 {
	for _, stat := range d.Statistics {
		if stat.Type == workoutdetail.ActiveEnergyBurned && stat.Sum != nil {
			return workoutdetail.Ptr(stat.Sum.Value)
		}
	}
	if d.Payload.Workout != nil && d.Payload.Workout.TotalEnergyBurned != nil {
		return workoutdetail.Ptr(d.Payload.Workout.TotalEnergyBurned.Value)
	}
	for i := len(d.Enrichments) - 1; i >= 0; i-- {
		if d.Enrichments[i].EnergyKcal != nil {
			return d.Enrichments[i].EnergyKcal
		}
	}
	return nil
}

func (d *workoutDetailData) metadata() map[string]any {
	out := map[string]any{}
	for k, v := range d.Payload.Metadata {
		out[k] = v
	}
	for _, e := range d.Enrichments {
		for k, v := range e.Metadata {
			out[k] = v
		}
	}
	return out
}

func (d *workoutDetailData) stats() []workoutdetail.Statistic {
	out := make([]workoutdetail.Statistic, 0, len(d.Statistics))
	for _, stat := range d.Statistics {
		s := workoutdetail.Statistic{Type: stat.Type}
		if stat.Average != nil {
			s.Average = workoutdetail.Ptr(convertStatisticValue(stat.Type, stat.Average.Value, stat.Average.Unit))
		}
		if stat.Sum != nil {
			s.Sum = workoutdetail.Ptr(stat.Sum.Value)
		}
		if stat.Maximum != nil {
			s.Maximum = workoutdetail.Ptr(convertStatisticValue(stat.Type, stat.Maximum.Value, stat.Maximum.Unit))
		}
		out = append(out, s)
	}
	return out
}

func (d *workoutDetailData) seriesBundle() workoutdetail.SeriesBundle {
	return workoutdetail.BuildSeries(d.Start.UnixMicro(), d.End.UnixMicro(), d.Observations, d.Route)
}

func (d *workoutDetailData) measured() ([]workoutdetail.ActivityBoundary, []workoutdetail.EventBoundary) {
	activities := make([]workoutdetail.ActivityBoundary, 0, len(d.Activities))
	for _, a := range d.Activities {
		end := d.End.UnixMicro()
		if a.EndUs != nil {
			end = *a.EndUs
		}
		if a.StartUs != nil {
			activities = append(activities, workoutdetail.ActivityBoundary{StartUs: *a.StartUs, EndUs: end})
		}
	}
	events := make([]workoutdetail.EventBoundary, 0, len(d.Events))
	seen := make(map[[3]int64]bool, len(d.Events))
	add := func(e syncing.WorkoutEvent) {
		if e.Type == nil || e.StartUs == nil || e.EndUs == nil {
			return
		}
		key := [3]int64{*e.Type, *e.StartUs, *e.EndUs}
		if seen[key] {
			return
		}
		seen[key] = true
		events = append(events, workoutdetail.EventBoundary{Type: *e.Type, StartUs: *e.StartUs, EndUs: *e.EndUs})
	}
	for _, e := range d.Events {
		add(e)
	}
	// HealthKit also attaches the parent workout's segment events to each
	// activity. Read them when the top-level payload is incomplete, but never
	// count the same event twice.
	for _, a := range d.Activities {
		for _, e := range a.Events {
			add(e)
		}
	}
	return activities, events
}

func (d *workoutDetailData) planContext() *workoutdetail.PlanContext {
	if d.Execution == nil {
		return nil
	}
	s := d.Execution.Snapshot
	if s.Type == "REST" || s.Type == "" {
		return nil
	}
	steps := make([]workoutdetail.PlannedStep, 0, len(s.Steps))
	for _, step := range s.Steps {
		purpose := step.Kind
		switch purpose {
		case "warmup", "cooldown":
		case "repeat":
			purpose = "work"
		default:
			purpose = "work"
		}
		ps := workoutdetail.PlannedStep{Label: "", Purpose: purpose, DurationS: float64(step.DurationS), Repeats: step.Repeats, RecoveryS: float64(step.RecoveryS)}
		if purpose == "work" {
			ps.TargetHR = s.TargetHR
			ps.PaceSPerKm = s.PaceSPerKM
		}
		steps = append(steps, ps)
	}
	dist := s.DistanceM
	return &workoutdetail.PlanContext{LocalDate: s.Date, Kind: s.Type, Revision: d.Execution.Version, DistanceM: &dist, DurationS: s.DurationS, HasStructure: len(s.Steps) > 0, Steps: steps}
}

func (d *workoutDetailData) splitsAndGrade() ([]workoutdetail.Split, *workoutdetail.GradeSummary, []workoutdetail.KilometerMarker, workoutdetail.SeriesBundle) {
	distance := d.distanceMeters()
	if distance == nil || *distance <= 0 {
		return nil, &workoutdetail.GradeSummary{PolicyVersion: workoutdetail.GradePolicyVersion, ReasonCodes: []string{"no_official_distance"}}, nil, d.seriesBundle()
	}
	bundle := d.seriesBundle()
	heart, steps, speed := obsByType(d.Observations, workoutdetail.HeartRate), obsByType(d.Observations, workoutdetail.StepCount), obsByType(d.Observations, workoutdetail.RunningSpeed)
	splits, grade, markers := workoutdetail.BuildSplits(d.Start.UnixMicro(), d.End.UnixMicro(), *distance, speed, heart, steps, d.Route)
	return splits, grade, markers, bundle
}

func obsByType(obs []workoutdetail.Observation, kind string) []workoutdetail.Observation {
	out := make([]workoutdetail.Observation, 0, len(obs))
	for _, o := range obs {
		if o.Type == kind {
			out = append(out, o)
		}
	}
	return out
}

func (d *workoutDetailData) metric(name string) *analysis.Metric {
	for i := range d.Published {
		if d.Published[i].Name == name {
			return &d.Published[i]
		}
	}
	return nil
}

func (d *workoutDetailData) modelVersion() string {
	seen := map[string]bool{}
	parts := []string{}
	for _, m := range d.Published {
		v := m.Algorithm + "/" + m.Version
		if !seen[v] {
			seen[v] = true
			parts = append(parts, v)
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "+")
}

func (d *workoutDetailData) missingReasons() []string {
	out := []string{}
	if m := d.metric("workout_load"); m != nil {
		if xs, ok := m.Details["missingComponents"].([]any); ok {
			for _, x := range xs {
				if s, ok := x.(string); ok {
					out = append(out, s)
				}
			}
		}
	}
	if len(d.Published) == 0 {
		out = append(out, "analysis_pending")
	}
	return out
}

func (d *workoutDetailData) sourceConfidence() *float64 {
	v := workoutdetail.SourceConfidence(d.Source)
	return &v
}

func (d *workoutDetailData) average(kind string, stats []workoutdetail.Statistic, durationS float64) *float64 {
	if v := workoutdetail.StatisticAverage(stats, kind); v != nil {
		return v
	}
	switch kind {
	case workoutdetail.RunningCadence:
		if steps := workoutdetail.StatisticSum(stats, workoutdetail.StepCount); steps != nil && durationS > 0 {
			return workoutdetail.Ptr(*steps / (durationS / 60))
		}
		return workoutdetail.CadenceFromSteps(obsByType(d.Observations, workoutdetail.StepCount), durationS)
	case workoutdetail.RunningPower:
		return workoutdetail.WeightedAverage(d.Observations, kind, d.Start.UnixMicro(), d.End.UnixMicro())
	case workoutdetail.StrideLength:
		return weightedConverted(d.Observations, kind, d.Start.UnixMicro(), d.End.UnixMicro(), func(v float64, unit string) float64 {
			if unit == "cm" {
				return v / 100
			}
			return v
		})
	case workoutdetail.VerticalOscillation:
		return weightedConverted(d.Observations, kind, d.Start.UnixMicro(), d.End.UnixMicro(), func(v float64, unit string) float64 {
			if unit == "m" {
				return v * 100
			}
			return v
		})
	case workoutdetail.GroundContactTime:
		return weightedConverted(d.Observations, kind, d.Start.UnixMicro(), d.End.UnixMicro(), func(v float64, unit string) float64 {
			if unit == "s" {
				return v * 1000
			}
			return v
		})
	case workoutdetail.HeartRate:
		return workoutdetail.WeightedAverage(d.Observations, kind, d.Start.UnixMicro(), d.End.UnixMicro())
	}
	return nil
}

func loadFeatureAnalysis(ctx context.Context, tx pgx.Tx, p syncing.Principal, id string, d *workoutDetailData) error {
	var publishedAt time.Time
	var schema, hash, stack string
	var generation int64
	var payload []byte
	err := tx.QueryRow(ctx, `SELECT p.published_at,a.schema_version,a.payload,encode(a.content_sha256,'hex'),m.stack_id,p.generation
		FROM analysis_publications p
			JOIN analysis_model_channels m ON m.dataset_id=p.dataset_id AND m.channel='active'
		JOIN analysis_runs r ON r.dataset_id=p.dataset_id AND r.id=p.published_run_id AND r.status='SUCCEEDED' AND r.target_generation=p.generation
		JOIN analysis_published_artifacts a ON a.dataset_id=p.dataset_id AND a.run_id=p.published_run_id AND a.artifact_key=$3
		JOIN dirty_scopes x ON x.dataset_id=p.dataset_id AND x.scope_key=p.scope_key AND x.generation=p.generation AND x.clean_generation=p.generation
		WHERE p.dataset_id=$1 AND p.scope_key='model/'||m.stack_id||'/workout/'||$2 AND p.metric_family IN ('workout_model','model_product_v2/workout_features')
		ORDER BY p.published_at DESC LIMIT 1`, p.DatasetID, id, wf.ArtifactKey).Scan(&publishedAt, &schema, &payload, &hash, &stack, &generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var f wf.WorkoutFeatures
	if err = json.Unmarshal(payload, &f); err != nil || f.Schema != schema || f.DatasetID != p.DatasetID || f.WorkoutID != id || (f.StackID != stack && f.StackID != wf.StackID()) || f.Generation != generation {
		return syncing.Fail(500, "workout_feature_invalid")
	}
	if _, canonicalHash, err := f.MarshalArtifact(); err != nil || canonicalHash != hash {
		return syncing.Fail(500, "workout_feature_invalid")
	}
	reasons := make([]string, 0, len(f.Reasons))
	for _, r := range f.Reasons {
		reasons = append(reasons, string(r))
	}
	d.FeatureArtifact = &workoutdetail.FeatureAnalysis{Schema: f.Schema, Algorithm: f.Algorithm, Version: f.Version, StackID: stack, PublishedAtUs: publishedAt.UnixMicro(), ActiveSeconds: f.Timing.Active, HeartRateCoverage: f.HeartRate.ObservedCoverage, SpeedCoverage: f.Speed.ObservedCoverage, RouteCoverage: f.Route.Coverage, Reasons: reasons}
	return nil
}

// loadModelQualification reads why the active stack does or does not admit
// this run: as running-ability evidence from its feature qualification
// (grade-adjusted in v2), and as training load from the v2 load result itself
// (its quality tier and reasons; the feature-level load qualification is the
// frozen v1 parameter rule and would contradict a computed v2 load). Absent
// until the stack has computed the Workout.
func loadModelQualification(ctx context.Context, tx pgx.Tx, p syncing.Principal, id string, d *workoutDetailData) error {
	var raw, loadJSON []byte
	err := tx.QueryRow(ctx, `SELECT r.feature->'qualification',r.load FROM model_workout_results r
 JOIN analysis_model_channels c ON c.dataset_id=r.dataset_id AND c.stack_id=r.stack_id AND c.channel='active'
 WHERE r.dataset_id=$1 AND r.workout_uuid=$2`, p.DatasetID, id).Scan(&raw, &loadJSON)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (len(raw) == 0 || string(raw) == "null") {
		return nil
	}
	if err != nil {
		return err
	}
	var q wf.Qualifications
	if json.Unmarshal(raw, &q) != nil {
		return syncing.Fail(500, "workout_feature_invalid")
	}
	ability := q.Performance
	if q.PerformanceGAP != nil {
		ability = *q.PerformanceGAP
	}
	load := workoutdetail.Qualification{Status: "pending", ReasonCodes: []string{}}
	if len(loadJSON) > 0 && string(loadJSON) != "null" {
		var r tl.Result
		if json.Unmarshal(loadJSON, &r) != nil {
			return analysis.Invalid()
		}
		switch {
		case r.Load != nil && r.QualityTier == tl.TierComplete:
			load.Status = "eligible"
		case r.Load != nil:
			load.Status = "conditional"
		default:
			load.Status = "ineligible"
		}
		d.LoadFocus = r.Focus
		load.ReasonCodes = append(load.ReasonCodes, r.ReasonCodes...)
	}
	d.Qualification = &workoutdetail.ModelQualification{RunningAbility: detailQualification(ability), TrainingLoad: load}
	return nil
}

func detailQualification(q wf.Qualification) workoutdetail.Qualification {
	out := workoutdetail.Qualification{Status: q.Status, ReasonCodes: make([]string, 0, len(q.Reasons))}
	for _, r := range q.Reasons {
		out.ReasonCodes = append(out.ReasonCodes, string(r))
	}
	return out
}

func (d *workoutDetailData) buildDetail() workoutdetail.Detail {
	durationS := float64(d.DurationUs) / 1e6
	elapsed := d.End.Sub(d.Start).Seconds()
	distance := d.distanceMeters()
	out := workoutdetail.Detail{Workout: workoutdetail.Workout{
		WorkoutID: d.ID, StartedAtUs: d.Start.UnixMicro(), EndedAtUs: d.End.UnixMicro(),
		DurationS: round1(durationS), ElapsedS: round1(elapsed), DistanceM: distance, ActivityType: d.ActivityType,
		ActiveEnergyKcal: d.energyKcal(), SourceCategory: workoutdetail.SourceCategory(d.Source),
		AnalysisStatus: d.AnalysisStatus, AnalysisAvailable: d.AnalysisAvailable,
		ModelVersion: d.modelVersion(), AssessmentStatus: d.AnalysisStatus,
		HeartRateSampleCount: d.SampleCounts[workoutdetail.HeartRate] + d.SeriesCounts[workoutdetail.HeartRate],
		RoutePointCount:      d.RouteCount,
		SessionType:          "RUN", StimulusType: "unclassified",
		MissingReasons:   d.missingReasons(),
		ExplanationCodes: []string{"detail_series_sampled_600", "route_points_sampled_500", "quality_is_feature_coverage"},
	}}
	out.Analysis = d.FeatureArtifact
	_, distanceEvents := d.measured()
	out.DistanceDistribution = workoutdetail.BuildDistanceDistribution(d.Start.UnixMicro(), d.End.UnixMicro(), distance, d.Observations, distanceEvents, d.HistoricalRestHR, d.HistoricalMaxHR)
	out.ModelQualification = d.Qualification
	out.LoadFocus = d.LoadFocus
	if durationS > 0 && distance != nil && *distance > 0 {
		out.Workout.AveragePaceSPerKm = workoutdetail.Ptr(round1(durationS / (*distance / 1000)))
	}
	stats := d.stats()
	out.Workout.AverageHeartRateBpm = d.average(workoutdetail.HeartRate, stats, durationS)
	out.Workout.AverageCadenceSpm = d.average(workoutdetail.RunningCadence, stats, durationS)
	out.Workout.AveragePowerW = d.average(workoutdetail.RunningPower, stats, durationS)
	out.Workout.AverageStrideLengthM = d.average(workoutdetail.StrideLength, stats, durationS)
	out.Workout.AverageVerticalOscillationCm = d.average(workoutdetail.VerticalOscillation, stats, durationS)
	out.Workout.AverageGroundContactTimeMs = d.average(workoutdetail.GroundContactTime, stats, durationS)
	if v, ok := metadataBool(d.metadata(), "HKIndoorWorkout"); ok {
		out.Workout.Indoor = &v
	}
	if d.Execution != nil {
		out.Workout.SessionType = d.Execution.Snapshot.Type
	}
	if h := d.heartCoverage(); d.AnalysisAvailable && h != nil {
		out.Workout.HeartRateCoverage = h
	}
	_, grade, markers, _ := d.splitsAndGrade()
	out.Summary.AveragePaceSPerKm = out.Workout.AveragePaceSPerKm
	if d.AnalysisAvailable && grade != nil {
		out.Summary.GradeAdjustedPaceSPerKm = grade.GradeAdjustedPaceSPerKm
		out.Summary.CoveredSplits = grade.CoveredSplits
		out.Summary.TotalSplits = grade.TotalSplits
	}
	if gain := workoutdetail.ElevationGain(d.Route); d.AnalysisAvailable && gain != nil {
		out.Summary.ElevationGainM = gain
	} else if m := d.metric("elevation_gain"); m != nil {
		out.Summary.ElevationGainM = m.Value
	}
	if q := workoutdetail.RouteQuality(d.Route, d.Start.UnixMicro(), d.End.UnixMicro()); d.AnalysisAvailable && q != nil {
		out.Summary.RouteQuality = q
	} else if m := d.metric("route_coverage"); m != nil {
		out.Summary.RouteQuality = m.Value
	}
	out.Workout.RouteQuality = out.Summary.RouteQuality
	routePoints := workoutdetail.SampleRoute(d.RoutePoints, 500)
	if routePoints == nil {
		routePoints = []workoutdetail.RoutePoint{}
	}
	if markers == nil {
		markers = []workoutdetail.KilometerMarker{}
	}
	out.Route = workoutdetail.Route{Points: routePoints, KilometerMarkers: markers, PointCount: d.RouteCount}

	// Coverage values feed the feature-quality score, never the training score.
	coverages := []*float64{out.Workout.HeartRateCoverage, seriesCoverage(d.Observations, workoutdetail.RunningCadence, d.Start.UnixMicro(), d.End.UnixMicro()), seriesCoverage(d.Observations, workoutdetail.RunningPower, d.Start.UnixMicro(), d.End.UnixMicro()), out.Summary.RouteQuality}
	if d.AnalysisAvailable {
		out.Workout.SourceConfidence = d.sourceConfidence()
		out.Workout.QualityScore = workoutdetail.QualityScore(coverages, out.Workout.SourceConfidence)
	}
	if m := d.metric("workout_load"); m != nil {
		load := &workoutdetail.TrainingLoad{Load: m.Value, Source: m.Algorithm + "/" + m.Version, StimulusType: "unclassified", MissingReasons: d.missingReasons()}
		if m.Value != nil && durationS > 0 {
			load.Intensity = workoutdetail.Ptr(round1(*m.Value / (durationS / 60)))
		}
		switch m.Quality {
		case "available":
			load.Confidence = workoutdetail.Ptr(1.0)
		case "partial":
			load.Confidence = workoutdetail.Ptr(0.5)
		}
		if load.Load == nil {
			if fallback := d.fallbackLoad(durationS, load); fallback != nil {
				load = fallback
			}
		}
		if load.Load != nil {
			out.TrainingLoad = load
		}
	} else if d.AnalysisAvailable && durationS > 0 && d.Execution != nil {
		out.TrainingLoad = d.fallbackLoad(durationS, &workoutdetail.TrainingLoad{Source: "session_type_fallback/v1", StimulusType: "unclassified", MissingReasons: d.missingReasons()})
	}
	if d.AnalysisAvailable && d.ZoneMaxHR > 0 && d.ZoneRestHR > 0 {
		out.ZoneSummary = workoutdetail.BuildHRZones(d.Start.UnixMicro(), d.End.UnixMicro(), elapsed, obsByType(d.Observations, workoutdetail.HeartRate), d.ZoneRestHR, d.ZoneMaxHR)
		if out.ZoneSummary != nil {
			if d.Execution != nil {
				workoutdetail.ApplyStimulusScores(out.ZoneSummary, d.planContext(), out.ZoneSummary.HeartRateCoverage, elapsed)
			}
			out.Workout.StimulusType = out.ZoneSummary.StimulusType
			out.Workout.StimulusConfidence = out.ZoneSummary.StimulusConfidence
			if out.TrainingLoad != nil {
				out.TrainingLoad.StimulusType = out.ZoneSummary.StimulusType
			}
		}
	}
	if d.Execution != nil && (d.Execution.Snapshot.Modified || strings.TrimSpace(d.Execution.Notes) != "") {
		s := d.Execution.Snapshot
		ov := &workoutdetail.Override{Revision: d.Execution.Revision, SessionType: &s.Type, Source: "training_execution", ConditionTags: []string{}}
		if note := strings.TrimSpace(d.Execution.Notes); note != "" {
			ov.Note = &note
		} else if note := strings.TrimSpace(s.Notes); note != "" {
			ov.Note = &note
		}
		if d.Execution.CompletedAt != nil {
			ov.CreatedAt = d.Execution.CompletedAt.UTC().Format(time.RFC3339)
		}
		out.Override = ov
	}
	out.Environment = workoutdetail.BuildEnvironment(d.metadata(), distance, &durationS, out.Summary.ElevationGainM)
	if !d.AnalysisAvailable {
		out.Environment.Correction, out.Environment.Adjusted = nil, nil
		out.Environment.Status = "pending"
		out.Environment.ReasonCodes = append(out.Environment.ReasonCodes, "analysis_pending")
	}
	return out
}

func (d *workoutDetailData) heartCoverage() *float64 {
	if m := d.metric("heart_rate_coverage"); m != nil && m.Value != nil {
		return m.Value
	}
	return seriesCoverage(d.Observations, workoutdetail.HeartRate, d.Start.UnixMicro(), d.End.UnixMicro())
}

// seriesCoverage returns the union of observation intervals over the Workout
// elapsed window. It is a display-quality input, not a TRIMP eligibility gate.
func seriesCoverage(obs []workoutdetail.Observation, kind string, start, end int64) *float64 {
	if end <= start {
		return nil
	}
	xs := obsByType(obs, kind)
	sort.Slice(xs, func(i, j int) bool { return xs[i].StartUs < xs[j].StartUs })
	var covered int64
	var cursor = start
	for _, o := range xs {
		a, b := maxI64(o.StartUs, start), minI64(o.EndUs, end)
		if b <= a {
			if o.EndUs == o.StartUs && o.StartUs >= cursor && o.StartUs <= end {
				b = minI64(o.StartUs+1_000_000, end)
				a = o.StartUs
			} else {
				continue
			}
		}
		if a > cursor {
			cursor = a
		}
		if b > cursor {
			covered += b - cursor
			cursor = b
		}
	}
	if covered <= 0 {
		return nil
	}
	v := mathMin(1, float64(covered)/float64(end-start))
	return &v
}

func maxI64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
func minI64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
func mathMin(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
func round1(v float64) float64 {
	return float64(int64(v*10+0.5)) / 10
}

func metadataBool(metadata map[string]any, key string) (bool, bool) {
	v, ok := metadata[key]
	if !ok {
		return false, false
	}
	switch x := v.(type) {
	case bool:
		return x, true
	case float64:
		return x != 0, true
	case map[string]any:
		if b, ok := x["value"].(bool); ok {
			return b, true
		}
	}
	return false, false
}

// fallbackLoad estimates load from the prescribed effort or session type.
// Execution snapshots contain planned targets, never a post-workout RPE.
func (d *workoutDetailData) fallbackLoad(durationS float64, base *workoutdetail.TrainingLoad) *workoutdetail.TrainingLoad {
	if d.Execution == nil {
		return nil
	}
	intensity := 0.48
	source := "session_type_fallback/v1"
	confidence := 0.4
	if d.Execution != nil && d.Execution.Snapshot.Effort >= 1 && d.Execution.Snapshot.Effort <= 10 {
		intensity = plannedEffortIntensity[d.Execution.Snapshot.Effort]
		source = "planned_effort_fallback/v1"
	} else if d.Execution != nil {
		switch d.Execution.Snapshot.Type {
		case "RECOVERY":
			intensity = 0.30
		case "EASY":
			intensity = 0.42
		case "LONG":
			intensity = 0.55
		case "TEMPO":
			intensity = 0.70
		case "INTERVAL":
			intensity = 0.80
		case "RACE":
			intensity = 0.85
		}
	}
	load := durationS / 60 * intensity * math.Exp(1.92*intensity)
	base.Load = workoutdetail.Ptr(round1(load))
	base.Intensity = workoutdetail.Ptr(round1(intensity))
	base.Confidence = workoutdetail.Ptr(confidence)
	base.Source = source
	base.RPE = nil
	base.MissingReasons = append(base.MissingReasons, "actual_rpe_unavailable")
	base.MissingReasons = append(base.MissingReasons, "heart_rate_or_pace_load_unavailable")
	return base
}

var plannedEffortIntensity = [...]float64{0, 0.25, 0.30, 0.38, 0.45, 0.52, 0.60, 0.68, 0.76, 0.84, 0.90}

func convertStatisticValue(kind string, value float64, unit string) float64 {
	switch kind {
	case workoutdetail.VerticalOscillation:
		if unit == "m" {
			return value * 100
		}
	case workoutdetail.GroundContactTime:
		if unit == "s" {
			return value * 1000
		}
	case workoutdetail.StrideLength:
		if unit == "cm" {
			return value / 100
		}
	}
	return value
}

func weightedConverted(obs []workoutdetail.Observation, kind string, start, end int64, convert func(float64, string) float64) *float64 {
	converted := make([]workoutdetail.Observation, 0, len(obs))
	for _, o := range obs {
		if o.Type == kind {
			o.Value = convert(o.Value, o.Unit)
		}
		converted = append(converted, o)
	}
	return workoutdetail.WeightedAverage(converted, kind, start, end)
}

func (d *workoutDetailData) buildSeries() workoutdetail.SeriesResponse {
	duration := d.End.Sub(d.Start).Seconds()
	out := workoutdetail.SeriesResponse{WorkoutID: d.ID, StartedAtUs: d.Start.UnixMicro(), EndedAtUs: d.End.UnixMicro(), DurationS: round1(duration)}
	splits, grade, _, bundle := d.splitsAndGrade()
	out.Splits, out.Grade, out.Series = splits, grade, bundle
	activities, events := d.measured()
	plan := d.planContext()
	distance := 0.0
	if d.distanceMeters() != nil {
		distance = *d.distanceMeters()
	}
	segments, match, note, measured := workoutdetail.BuildSegments(d.Start.UnixMicro(), d.End.UnixMicro(), distance, obsByType(d.Observations, workoutdetail.RunningSpeed), obsByType(d.Observations, workoutdetail.HeartRate), obsByType(d.Observations, workoutdetail.StepCount), d.Route, activities, events, plan)
	out.Segments, out.SegmentPlan, out.SegmentNote, out.HasMeasuredWorkoutSteps = segments, match, note, measured
	if d.AnalysisAvailable {
		out.Dynamics = workoutdetail.BuildDynamics(duration, bundle)
	} else {
		out.Grade = nil
		for i := range out.Splits {
			out.Splits[i].Grade = nil
		}
	}
	if out.Splits == nil {
		out.Splits = []workoutdetail.Split{}
	}
	if out.Segments == nil {
		out.Segments = []workoutdetail.Segment{}
	}
	return out
}

func (d *workoutDetailData) hasStatistic(kind string) bool {
	for _, stat := range d.Statistics {
		if stat.Type == kind && (stat.Average != nil || stat.Sum != nil || stat.Maximum != nil) {
			return true
		}
	}
	return false
}

func (d *workoutDetailData) buildFreshness() workoutdetail.Freshness {
	out := workoutdetail.Freshness{
		WorkoutID: d.ID,
		Facts:     workoutdetail.FreshnessFacts{CommittedAt: d.ReceivedAt.UTC().Format(time.RFC3339), LastBatchAt: formatTime(d.LastBatchAt), Batches: d.Batches},
		Analysis: workoutdetail.FreshnessAnalysis{
			Status: d.AnalysisStatus, Available: d.AnalysisAvailable, JobID: d.AnalysisJobID,
			PublishedAt: formatTime(d.PublishedAt),
		},
		Series:        []workoutdetail.FreshnessMetric{},
		DeliveryState: "delivered", AnalysisState: d.AnalysisStatus, StabilityState: "settling",
	}
	for _, spec := range []struct{ metric, kind string }{
		{"pace", workoutdetail.RunningSpeed}, {"heartRate", workoutdetail.HeartRate},
		{"cadence", workoutdetail.RunningCadence}, {"strideLength", workoutdetail.StrideLength},
		{"power", workoutdetail.RunningPower}, {"groundContactTime", workoutdetail.GroundContactTime},
		{"verticalOscillation", workoutdetail.VerticalOscillation}, {"elevation", "route.elevation"},
	} {
		count := d.SampleCounts[spec.kind] + d.SeriesCounts[spec.kind]
		if count == 0 && d.hasStatistic(spec.kind) {
			count = 1
		}
		if spec.kind == "route.elevation" {
			count = 0
			for _, p := range d.Route {
				if p.AltitudeM != nil {
					count++
				}
			}
		}
		if spec.metric == "cadence" {
			count += d.SampleCounts[workoutdetail.StepCount] + d.SeriesCounts[workoutdetail.StepCount]
			if count == 0 && d.hasStatistic(workoutdetail.StepCount) {
				count = 1
			}
		}
		state := "not_seen_yet"
		if count > 0 {
			state = "present"
		}
		last := ""
		if t := d.LastObservedByType[spec.kind]; !t.IsZero() {
			last = t.UTC().Format(time.RFC3339)
		}
		out.Series = append(out.Series, workoutdetail.FreshnessMetric{Metric: spec.metric, Samples: count, State: state, LastAvailableAt: last})
	}
	if time.Since(d.LastFactAt) >= 24*time.Hour {
		out.StabilityState = "stable"
	}
	out.Completeness = "partial"
	if d.AnalysisStatus == "published" && out.StabilityState == "stable" {
		out.Completeness = "complete"
	}
	return out
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// Public store entry points used by httpapi.

func (s *Store) WorkoutDetail(ctx context.Context, p syncing.Principal, id string) (workoutdetail.Detail, error) {
	if !syncing.ValidID(id) {
		return workoutdetail.Detail{}, syncing.Fail(422, "invalid_workout_id")
	}
	d, err := s.loadWorkoutDetailData(ctx, p, id)
	if err != nil {
		return workoutdetail.Detail{}, err
	}
	return d.buildDetail(), nil
}

func (s *Store) WorkoutSeries(ctx context.Context, p syncing.Principal, id string) (workoutdetail.SeriesResponse, error) {
	if !syncing.ValidID(id) {
		return workoutdetail.SeriesResponse{}, syncing.Fail(422, "invalid_workout_id")
	}
	d, err := s.loadWorkoutDetailData(ctx, p, id)
	if err != nil {
		return workoutdetail.SeriesResponse{}, err
	}
	return d.buildSeries(), nil
}

func (s *Store) WorkoutFreshness(ctx context.Context, p syncing.Principal, id string) (workoutdetail.Freshness, error) {
	if !syncing.ValidID(id) {
		return workoutdetail.Freshness{}, syncing.Fail(422, "invalid_workout_id")
	}
	d, err := s.loadWorkoutDetailData(ctx, p, id)
	if err != nil {
		return workoutdetail.Freshness{}, err
	}
	return d.buildFreshness(), nil
}
