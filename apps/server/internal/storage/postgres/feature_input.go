package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/analysis"
	"rai/server/internal/syncing"
	wf "rai/server/internal/workoutfeatures"
)

func featureJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
func featureHash(v any) string          { h := sha256.Sum256(featureJSON(v)); return hex.EncodeToString(h[:]) }
func featureEvent(e syncing.WorkoutEvent) wf.Event {
	return wf.Event{Type: *e.Type, Start: *e.StartUs, End: *e.EndUs, Metadata: featureJSON(e.Metadata)}
}
func featureStatistic(s syncing.WorkoutStatistic) wf.Statistic {
	q := func(v *syncing.Quantity) *float64 {
		if v == nil {
			return nil
		}
		return &v.Value
	}
	return wf.Statistic{Type: s.Type, Unit: syncing.Units[s.Type], Start: *s.StartUs, End: *s.EndUs, Sum: q(s.Sum), Average: q(s.Average), Maximum: q(s.Maximum), Minimum: q(s.Minimum)}
}
func featureFailure(err error) error {
	code := wf.ErrorCode(err)
	if code == "input_limit" {
		return &analysis.Failure{Code: "analysis_input_limit"}
	}
	return analysis.Invalid()
}

// loadFeatureInput is called inside LoadAnalysis's single REPEATABLE READ
// transaction. It never replays facts from historical import bundles.
func (s *Store) loadFeatureInput(ctx context.Context, tx pgx.Tx, l Lease, in *analysis.Snapshot, asOf time.Time) error {
	// Clock is captured after the transaction's initial generation/revision read,
	// so it describes this stable snapshot. Historical snapshot reconstruction is
	// not implied by a caller's older wall-clock timestamp.
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&asOf); err != nil {
		return err
	}
	id := strings.TrimPrefix(l.ScopeKey, "model/"+wf.StackID()+"/workout/")
	if !syncing.ValidID(id) || l.ScopeKey != wf.Scope(id) {
		return analysis.Invalid()
	}
	var enabled bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM analysis_model_channels WHERE dataset_id=$1 AND channel='candidate' AND stack_id=$2 AND config_sha256=decode($3,'hex'))`, l.DatasetID, wf.StackID(), wf.DefaultPolicy().Hash()).Scan(&enabled); err != nil {
		return err
	}
	if !enabled {
		return &analysis.Failure{Code: "analysis_unsupported_algorithm"}
	}
	return loadWorkoutFeatureFacts(ctx, tx, l, in, asOf, id, true)
}

func loadWorkoutFeatureFacts(ctx context.Context, tx pgx.Tx, l Lease, in *analysis.Snapshot, asOf time.Time, id string, legacyParameters bool) error {
	n := wf.RawWorkoutSnapshot{DatasetID: l.DatasetID, WorkoutID: id, Generation: in.Generation, Revision: in.Revision, AsOf: asOf.UTC().UnixMicro(), Policy: wf.DefaultPolicy(), AlgorithmVersion: wf.Algorithm + "/" + wf.Version, Fact: wf.FactRef{Kind: "workout", ID: id, State: wf.Present}, Availability: map[string]wf.State{}}
	in.FeatureInput = &n
	var body []byte
	var start, end time.Time
	err := tx.QueryRow(ctx, `SELECT payload,event_start,event_end,duration_us,encode(content_sha256,'hex'),activity_type FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2`, l.DatasetID, id).Scan(&body, &start, &end, &n.DurationUs, &n.Fact.Hash, &n.ActivityType)
	if errors.Is(err, pgx.ErrNoRows) {
		n.Fact.State = wf.Deleted
		n.Start = in.From.UnixMicro()
		n.End = in.Through.UnixMicro()
		return nil
	}
	if err != nil {
		return err
	}
	n.Start, n.End = start.UnixMicro(), end.UnixMicro()
	in.From, in.Through = start.UTC(), end.UTC()
	var event syncing.Event
	if json.Unmarshal(body, &event) != nil || event.Workout == nil {
		return analysis.Invalid()
	}
	if event.Workout.TotalEnergyBurned != nil {
		n.EnergyKcal = &event.Workout.TotalEnergyBurned.Value
	}
	if event.Workout.TotalDistance != nil {
		n.Distance = &event.Workout.TotalDistance.Value
	}
	metadata := event.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	merge := func(m map[string]any) {
		for k, v := range m {
			if old, ok := metadata[k]; ok && string(featureJSON(old)) != string(featureJSON(v)) {
				n.Availability["environment"] = wf.Ambiguous
			} else {
				metadata[k] = v
			}
		}
	}
	// Complete provenance is read across bounded immutable observations. Conflicts
	// for the same fact are represented as ambiguous, never latest-row wins.
	singleObservationProven := map[string]bool{}
	factSources := map[string]map[string]wf.Source{}
	allSources := map[string]wf.Source{}
	addSource := func(key string, source syncing.HealthKitSource, parent *syncing.HealthKitSource) {
		src := wf.NewSource(source.BundleID, source.Classification(parent), featureJSON(source))
		if factSources[key] == nil {
			factSources[key] = map[string]wf.Source{}
		}
		factSources[key][src.Epoch] = src
		allSources[src.Epoch] = src
	}
	rows, err := tx.Query(ctx, `SELECT bundle_sha256, payload->'observation' FROM workout_imports WHERE dataset_id=$1 AND workout_uuid=$2 AND payload ? 'observation' AND (payload->>'protocolVersion') IS DISTINCT FROM '6' ORDER BY bundle_sha256 LIMIT 2001`, l.DatasetID, id)
	if err != nil {
		return err
	}
	count := 0
	provenanceEntries := 0
	observationBytes := 0
	for rows.Next() {
		count++
		if count > 2000 {
			rows.Close()
			return &analysis.Failure{Code: "analysis_input_limit"}
		}
		var h, b []byte
		if err = rows.Scan(&h, &b); err != nil {
			rows.Close()
			return err
		}
		observationBytes += len(b)
		if observationBytes > 32<<20 {
			rows.Close()
			return &analysis.Failure{Code: "analysis_input_limit"}
		}
		var o syncing.BundleObservation
		if json.Unmarshal(b, &o) != nil {
			rows.Close()
			return analysis.Invalid()
		}
		n.References = append(n.References, wf.FactRef{Kind: "observation", ID: hex.EncodeToString(h), Hash: featureHash(o), State: wf.Present})
		provenanceEntries += 1 + len(o.Provenance.Samples) + len(o.Provenance.Routes)
		if provenanceEntries > wf.MaxPoints {
			rows.Close()
			return &analysis.Failure{Code: "analysis_input_limit"}
		}
		merge(o.Metadata)
		addSource("workout", o.Provenance.Workout, nil)
		for _, p := range o.Provenance.Samples {
			if c, ok := o.Coverage["series:"+p.SampleType]; ok && c.QueryCompleted && c.LocalPersisted && (c.State == "complete" || c.State == "no_records_observed") {
				singleObservationProven[p.SampleType+":"+p.SampleUUID] = true
			}
			addSource(p.SampleType+":"+p.SampleUUID, p.Source, &o.Provenance.Workout)
		}
		for _, p := range o.Provenance.Routes {
			addSource("route:"+p.RouteID, p.Source, &o.Provenance.Workout)
		}
		for k, v := range o.Coverage {
			if v.State == "unsupported" {
				n.Availability[k] = wf.Unsupported
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// A protocol-6 payload carries one top-level source that every fact inherits
	// instead of repeating it in an observation. The latest import read model
	// supplies attribution for the v6 facts; its digest still names the raw bytes.
	var compactRaw []byte
	var compactDigest []byte
	compactErr := tx.QueryRow(ctx, `SELECT bundle_sha256,payload FROM workout_imports WHERE dataset_id=$1 AND workout_uuid=$2 AND (payload->>'protocolVersion')::int = 6 ORDER BY imported_at DESC,bundle_sha256 DESC LIMIT 1`, l.DatasetID, id).Scan(&compactDigest, &compactRaw)
	if compactErr != nil && !errors.Is(compactErr, pgx.ErrNoRows) {
		return compactErr
	}
	if compactErr == nil {
		table, err := workoutImportSources(compactRaw)
		if err != nil {
			return analysis.Invalid()
		}
		if table.Workout.Classification(nil) == "apple_watch" {
			n.References = append(n.References, wf.FactRef{Kind: "workout_import", ID: hex.EncodeToString(compactDigest), Hash: featureHash(json.RawMessage(compactRaw)), State: wf.Present})
			addSource("workout", table.Workout, nil)
			for key, source := range table.Samples {
				class := source.Classification(&table.Workout)
				if class != "apple_watch" && class != "associated_apple_watch" {
					continue
				}
				addSource(key, source, &table.Workout)
				singleObservationProven[key] = true
			}
			for routeID, source := range table.Routes {
				class := source.Classification(&table.Workout)
				if class != "apple_watch" && class != "associated_apple_watch" {
					continue
				}
				addSource("route:"+routeID, source, &table.Workout)
			}
		}
	}
	sourceFor := func(key, bundle string) (string, wf.State) {
		xs := factSources[key]
		if len(xs) > 1 {
			return "", wf.Ambiguous
		}
		for _, v := range xs {
			if v.BundleID != bundle {
				return "", wf.Ambiguous
			}
			return v.Epoch, wf.Present
		}
		src := wf.NewSource(bundle, "source_unknown", nil)
		allSources[src.Epoch] = src
		n.Availability["source:"+key] = wf.Missing
		return src.Epoch, wf.Present
	}
	if event.Source != nil {
		epoch, state := sourceFor("workout", event.Source.BundleID)
		n.WorkoutSourceEpoch = epoch
		n.Availability["workout_source"] = state
	}
	rows, err = tx.Query(ctx, `SELECT enrichment_id::text,encode(content_sha256,'hex'),total_distance_m,metadata,total_energy_kcal,route_available,source_bundle_id,source_version,observed_at_us FROM workout_enrichments WHERE dataset_id=$1 AND workout_uuid=$2 ORDER BY enrichment_id LIMIT 2001`, l.DatasetID, id)
	if err != nil {
		return err
	}
	count = 0
	for rows.Next() {
		count++
		if count > 2000 {
			rows.Close()
			return &analysis.Failure{Code: "analysis_input_limit"}
		}
		var ref wf.FactRef
		var enrichment wf.Enrichment
		var distance *float64
		var b []byte
		if err = rows.Scan(&ref.ID, &ref.Hash, &distance, &b, &enrichment.EnergyKcal, &enrichment.RouteAvailable, &enrichment.SourceBundleID, &enrichment.SourceVersion, &enrichment.ObservedAt); err != nil {
			rows.Close()
			return err
		}
		ref.Kind = "enrichment"
		ref.State = wf.Present
		n.References = append(n.References, ref)
		enrichment.Fact = ref
		enrichment.Distance = distance
		enrichment.Metadata = b
		n.Enrichments = append(n.Enrichments, enrichment)
		if distance != nil {
			if n.Distance != nil && *n.Distance != *distance {
				rows.Close()
				return analysis.Invalid()
			}
			n.Distance = distance
		}
		var m map[string]any
		if json.Unmarshal(b, &m) != nil {
			rows.Close()
			return analysis.Invalid()
		}
		merge(m)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	n.Metadata = featureJSON(metadata)
	// Projections preserve late package events/activities which need not appear
	// in the original parent payload.
	for _, kind := range []string{"statistics", "events", "activities"} {
		query := `SELECT payload FROM workout_` + kind + ` WHERE dataset_id=$1 AND workout_uuid=$2 LIMIT 2001`
		rows, err = tx.Query(ctx, query, l.DatasetID, id)
		if err != nil {
			return err
		}
		count = 0
		for rows.Next() {
			count++
			if count > 2000 {
				rows.Close()
				return &analysis.Failure{Code: "analysis_input_limit"}
			}
			var b []byte
			if err = rows.Scan(&b); err != nil {
				rows.Close()
				return err
			}
			n.References = append(n.References, wf.FactRef{Kind: kind, ID: featureHash(json.RawMessage(b)), Hash: featureHash(json.RawMessage(b)), State: wf.Present})
			switch kind {
			case "statistics":
				var x syncing.WorkoutStatistic
				if json.Unmarshal(b, &x) != nil {
					rows.Close()
					return analysis.Invalid()
				}
				n.Statistics = append(n.Statistics, featureStatistic(x))
			case "events":
				var x syncing.WorkoutEvent
				if json.Unmarshal(b, &x) != nil {
					rows.Close()
					return analysis.Invalid()
				}
				n.Events = append(n.Events, featureEvent(x))
			case "activities":
				var x syncing.WorkoutActivity
				if json.Unmarshal(b, &x) != nil {
					rows.Close()
					return analysis.Invalid()
				}
				a := wf.Activity{ID: x.UUID, Start: *x.StartUs, End: x.EndUs, DurationUs: *x.DurationUs, Configuration: featureJSON(x.Configuration), Metadata: featureJSON(x.Metadata)}
				for _, e := range x.Events {
					a.Events = append(a.Events, featureEvent(e))
				}
				for _, st := range x.Statistics {
					a.Statistics = append(a.Statistics, featureStatistic(st))
				}
				n.Activities = append(n.Activities, a)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	rows, err = tx.Query(ctx, `SELECT h.sample_type,h.sample_uuid::text,h.event_start,h.event_end,h.value_numeric,h.value_category,COALESCE(h.unit,''),encode(h.content_sha256,'hex'),h.source_bundle_id,h.metadata,u.series_id::text,u.state,encode(u.manifest_sha256,'hex') FROM workout_samples a JOIN health_samples h USING(dataset_id,sample_type,sample_uuid) LEFT JOIN series_uploads u USING(dataset_id,sample_type,sample_uuid) WHERE a.dataset_id=$1 AND a.workout_uuid=$2 ORDER BY h.sample_type,h.sample_uuid LIMIT 250001`, l.DatasetID, id)
	if err != nil {
		return err
	}
	seriesIndex := map[string]int{}
	count = 0
	for rows.Next() {
		count++
		if count > wf.MaxPoints {
			rows.Close()
			return &analysis.Failure{Code: "analysis_input_limit"}
		}
		var q wf.Quantity
		var a, b time.Time
		var v *float64
		var bundle string
		var sid, state, manifest *string
		if err = rows.Scan(&q.Type, &q.Fact.ID, &a, &b, &v, &q.Category, &q.Unit, &q.Fact.Hash, &bundle, &q.Metadata, &sid, &state, &manifest); err != nil {
			rows.Close()
			return err
		}
		q.Fact.Kind = "quantity:" + q.Type
		q.Fact.State = wf.Present
		q.Start, q.End = a.UnixMicro(), b.UnixMicro()
		q.Source, q.Fact.State = sourceFor(q.Type+":"+q.Fact.ID, bundle)
		if v == nil {
			q.Fact.State = wf.Unsupported
		} else {
			q.Value = *v
		}
		if q.End == q.Start {
			q.Support = wf.InstantPoint
		} else {
			q.Support = wf.IntervalObservation
		}
		if !singleObservationProven[q.Type+":"+q.Fact.ID] {
			q.Support = wf.CondensedParent
		}
		if sid != nil {
			q.Support = wf.CondensedParent
			n.Availability["series:"+q.Fact.ID] = wf.Condensed
			if state != nil && *state == "sealed" && manifest != nil {
				seriesIndex[*sid] = len(n.Series)
				n.Series = append(n.Series, wf.Series{Fact: wf.FactRef{Kind: "series", ID: *sid, Hash: *manifest, State: wf.SeriesBacked}, ParentID: q.Fact.ID, Type: q.Type, Source: q.Source})
				n.Availability["series:"+q.Fact.ID] = wf.SeriesBacked
			}
		}

		n.Quantities = append(n.Quantities, q)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = tx.Query(ctx, `SELECT u.series_id::text,c.points FROM workout_samples a JOIN health_samples h USING(dataset_id,sample_type,sample_uuid) JOIN series_uploads u USING(dataset_id,sample_type,sample_uuid) JOIN series_chunks c USING(dataset_id,series_id) WHERE a.dataset_id=$1 AND a.workout_uuid=$2 AND u.state='sealed' ORDER BY u.series_id,c.chunk_index LIMIT 250001`, l.DatasetID, id)
	if err != nil {
		return err
	}
	count = len(n.Quantities)
	for rows.Next() {
		var sid string
		var b []byte
		if err = rows.Scan(&sid, &b); err != nil {
			rows.Close()
			return err
		}
		var points []syncing.SeriesPoint
		if json.Unmarshal(b, &points) != nil {
			rows.Close()
			return analysis.Invalid()
		}
		count += len(points)
		if count > wf.MaxPoints {
			rows.Close()
			return &analysis.Failure{Code: "analysis_input_limit"}
		}
		index, ok := seriesIndex[sid]
		if !ok {
			rows.Close()
			return analysis.Invalid()
		}
		series := &n.Series[index]
		for _, x := range points {
			series.Points = append(series.Points, wf.Quantity{Type: series.Type, Start: x.StartUs, End: x.EndUs, Value: x.Value, Unit: x.Unit, Source: series.Source, Support: wf.SealedSeries})
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = tx.Query(ctx, `SELECT r.route_uuid::text,encode(r.manifest_sha256,'hex'),r.source_bundle_id,p.sequence,p.recorded_at,p.latitude,p.longitude,p.altitude,p.horizontal_accuracy,p.vertical_accuracy,p.speed,p.course FROM workout_routes r JOIN route_points p USING(dataset_id,route_uuid) WHERE r.dataset_id=$1 AND r.workout_uuid=$2 AND r.state='sealed' ORDER BY r.route_uuid,p.sequence LIMIT 250001`, l.DatasetID, id)
	if err != nil {
		return err
	}
	count = 0
	last := ""
	for rows.Next() {
		count++
		if count > wf.MaxPoints {
			rows.Close()
			return &analysis.Failure{Code: "analysis_input_limit"}
		}
		var key, h, bundle string
		var x wf.RoutePoint
		var at time.Time
		if err = rows.Scan(&key, &h, &bundle, &x.Sequence, &at, &x.Latitude, &x.Longitude, &x.Altitude, &x.HorizontalAccuracy, &x.VerticalAccuracy, &x.Speed, &x.Course); err != nil {
			rows.Close()
			return err
		}
		x.At = at.UnixMicro()
		if key != last {
			epoch, state := sourceFor("route:"+key, bundle)
			n.Routes = append(n.Routes, wf.Route{Fact: wf.FactRef{Kind: "route", ID: key, Hash: h, State: state}, Source: epoch})
			last = key
		}
		n.Routes[len(n.Routes)-1].Points = append(n.Routes[len(n.Routes)-1].Points, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}

	// Missing staged routes are explicit availability, not usable route support.
	rows, err = tx.Query(ctx, `SELECT route_uuid::text,encode(manifest_sha256,'hex') FROM workout_routes WHERE dataset_id=$1 AND workout_uuid=$2 AND state<>'sealed' ORDER BY route_uuid LIMIT 2001`, l.DatasetID, id)
	if err != nil {
		return err
	}
	count = 0
	for rows.Next() {
		count++
		if count > 2000 {
			rows.Close()
			return &analysis.Failure{Code: "analysis_input_limit"}
		}
		var key, h string
		if err = rows.Scan(&key, &h); err != nil {
			rows.Close()
			return err
		}
		n.Availability["route:"+key] = wf.Missing
		n.References = append(n.References, wf.FactRef{Kind: "route", ID: key, Hash: h, State: wf.Missing})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// Last-run identities may locate deletion barriers; never load the deleted
	// observation values or route points from historical bundles/artifacts.
	rows, err = tx.Query(ctx, `WITH previous AS (SELECT lineage->'factRefs' AS refs FROM analysis_runs WHERE dataset_id=$1 AND scope_key=$2 AND algorithm_name=$3 AND status='SUCCEEDED' ORDER BY target_generation DESC,started_at DESC LIMIT 1),
 identities AS (SELECT CASE WHEN f.kind LIKE 'quantity:%' THEN substring(f.kind FROM length('quantity:')+1) WHEN f.kind='route' THEN 'HKWorkoutRouteTypeIdentifier' ELSE 'HKWorkoutTypeIdentifier' END AS sample_type,f.id::uuid AS sample_uuid
 FROM previous p,jsonb_to_recordset(p.refs) AS f(kind text,id text) WHERE f.kind LIKE 'quantity:%' OR f.kind IN ('route','workout') UNION SELECT 'HKWorkoutTypeIdentifier',$4::uuid)
 SELECT t.sample_type,t.sample_uuid::text,t.deleted_rev FROM identities i JOIN entity_tombstones t ON t.dataset_id=$1 AND t.sample_type=i.sample_type AND t.sample_uuid=i.sample_uuid LIMIT 250001`, l.DatasetID, l.ScopeKey, wf.Algorithm, id)
	if err != nil {
		return err
	}
	count = 0
	for rows.Next() {
		count++
		if count > wf.MaxPoints {
			rows.Close()
			return &analysis.Failure{Code: "analysis_input_limit"}
		}
		var typ, key string
		var rev int64
		if err = rows.Scan(&typ, &key, &rev); err != nil {
			rows.Close()
			return err
		}
		n.References = append(n.References, wf.FactRef{Kind: "tombstone:" + typ, ID: key, Hash: featureHash(rev), State: wf.Deleted})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}

	if legacyParameters {
		rows, err = tx.Query(ctx, `SELECT id::text,encode(content_sha256,'hex'),effective_from,effective_through,supersedes_id::text,payload,provenance,created_at FROM athlete_model_inputs WHERE dataset_id=$1 AND kind='hr_reference' AND schema_version='rai.hr_reference/1' AND effective_from<=$2 AND (effective_through IS NULL OR effective_through>$2) AND created_at<=$3 ORDER BY effective_from,id LIMIT 257`, l.DatasetID, start, asOf)
		if err != nil {
			return err
		}
		for rows.Next() {
			if len(n.Parameters) >= 256 {
				rows.Close()
				return &analysis.Failure{Code: "analysis_input_limit"}
			}
			var x wf.Parameter
			var from, created time.Time
			var through *time.Time
			var supersedes *string
			var b []byte
			var provenance string
			if err = rows.Scan(&x.ID, &x.Hash, &from, &through, &supersedes, &b, &provenance, &created); err != nil {
				rows.Close()
				return err
			}
			if json.Unmarshal(b, &x) != nil {
				rows.Close()
				return analysis.Invalid()
			} // restore authoritative indexed lineage after payload decoding
			x.EffectiveFrom = from.UnixMicro()
			x.RecordedAt = created.UnixMicro()
			x.Provenance = provenance
			if through != nil {
				v := through.UnixMicro()
				x.EffectiveThrough = &v
			}
			if supersedes != nil {
				x.Supersedes = *supersedes
			}
			n.Parameters = append(n.Parameters, x)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	for _, src := range allSources {
		n.Provenance = append(n.Provenance, src)
	}
	normalized, err := wf.Normalize(n)
	if err != nil {
		return featureFailure(err)
	}
	in.FeatureInput = &normalized
	return nil
}
