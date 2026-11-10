package postgres

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/analysis"
	"rai/server/internal/hrref"
	pf "rai/server/internal/performance"
	rd "rai/server/internal/readiness"
	rc "rai/server/internal/recovery"
	"rai/server/internal/syncing"
	tl "rai/server/internal/trainingload"
	vo2 "rai/server/internal/vo2max"
	ve "rai/server/internal/vo2maxexternal"
	wf "rai/server/internal/workoutfeatures"
)

type stackInput struct {
	ID, Kind, Hash, Provenance string
	From, Known                time.Time
	Through                    *time.Time
	Supersedes, Revokes        string
	Payload                    []byte
}

func stackInputs(ctx context.Context, tx pgx.Tx, dataset string, captured time.Time) ([]stackInput, error) {
	rows, err := tx.Query(ctx, `SELECT id::text,kind,encode(content_sha256,'hex'),provenance,effective_from,created_at,effective_through,
 COALESCE(supersedes_id::text,''),COALESCE(revokes_id::text,''),payload FROM athlete_model_inputs
 WHERE dataset_id=$1 AND input_schema IN ('rai.hr_reference/2','rai.sleep_target/1','rai.workout_effort/1',
 'rai.workout_overlap_resolution/1','rai.source_context_selection/1') AND created_at<=$2 ORDER BY created_at,id LIMIT 1025`, dataset, captured)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []stackInput{}
	for rows.Next() {
		var x stackInput
		if err := rows.Scan(&x.ID, &x.Kind, &x.Hash, &x.Provenance, &x.From, &x.Known, &x.Through, &x.Supersedes, &x.Revokes, &x.Payload); err != nil {
			return nil, err
		}
		out = append(out, x)
		if len(out) > 1024 {
			return nil, &analysis.Failure{Code: "analysis_input_limit"}
		}
	}
	return out, rows.Err()
}

func effectiveStackInputs(inputs []stackInput, effective, known time.Time) []stackInput {
	removed := map[string]bool{}
	for i := len(inputs) - 1; i >= 0; i-- {
		x := inputs[i]
		if x.Known.After(known) || removed[x.ID] {
			continue
		}
		if x.Revokes != "" {
			removed[x.Revokes] = true
		}
		if !x.From.After(effective) && x.Supersedes != "" {
			removed[x.Supersedes] = true
		}
	}
	out := []stackInput{}
	for _, x := range inputs {
		if x.Revokes == "" && !removed[x.ID] && !x.Known.After(known) && !x.From.After(effective) && (x.Through == nil || effective.Before(*x.Through)) {
			out = append(out, x)
		}
	}
	return out
}

type stackWorkout struct {
	feature  wf.WorkoutFeatures
	exposure wf.HRExposure
	raw      *wf.RawWorkoutSnapshot
	known    time.Time
}

func stackWorkouts(ctx context.Context, tx pgx.Tx, l Lease, stack string, start, at, captured time.Time, revision int64) ([]stackWorkout, error) {
	rows, err := tx.Query(ctx, `SELECT workout_uuid::text,first_received_at FROM workouts
 WHERE dataset_id=$1 AND event_end>=$2 AND event_end<=$3 AND activity_type=37
 ORDER BY event_end,workout_uuid LIMIT $4`, l.DatasetID, start, at, vo2.MaxWorkouts+1)
	if err != nil {
		return nil, err
	}
	type idAt struct {
		id    string
		known time.Time
	}
	ids, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (idAt, error) {
		var x idAt
		err := row.Scan(&x.id, &x.known)
		return x, err
	})
	if err != nil {
		return nil, err
	}
	if len(ids) > vo2.MaxWorkouts {
		return nil, &analysis.Failure{Code: "analysis_input_limit"}
	}
	out := make([]stackWorkout, 0, len(ids))
	totalPoints := 0
	for _, w := range ids {
		in := analysis.Snapshot{Generation: l.TargetGeneration, Revision: revision}
		featureLease := l
		featureLease.ScopeKey = "model/" + stack + "/workout/" + w.id
		if err := loadWorkoutFeatureFacts(ctx, tx, featureLease, &in, captured, w.id, false); err != nil {
			return nil, err
		}
		n := in.FeatureInput
		totalPoints += len(n.Quantities)
		for _, x := range n.Series {
			totalPoints += len(x.Points)
		}
		for _, x := range n.Routes {
			totalPoints += len(x.Points)
		}
		if totalPoints > 2_000_000 {
			return nil, &analysis.Failure{Code: "analysis_input_limit"}
		}
		f, err := wf.ComputeNormalized(ctx, *n)
		if err != nil {
			return nil, featureFailure(err)
		}
		e, err := wf.BuildHRExposure(*n)
		if err != nil {
			return nil, featureFailure(err)
		}
		out = append(out, stackWorkout{feature: f, exposure: e, raw: n, known: w.known})
	}
	return out, nil
}

func workoutReference(w stackWorkout, inputs []stackInput, captured time.Time, daily []hrref.DailyRHR) (hrref.Snapshot, error) {
	start := time.UnixMicro(w.raw.Start)
	var refs []hrref.Reference
	for _, row := range effectiveStackInputs(inputs, start, captured) {
		if row.Kind != "hr_reference" {
			continue
		}
		var x struct {
			Max    float64   `json:"maxHR"`
			Rest   float64   `json:"restingHR"`
			MaxAt  time.Time `json:"measuredAt"`
			RestAt time.Time `json:"restingMeasuredAt"`
			Method string    `json:"method"`
			Unit   string    `json:"unit"`
		}
		if json.Unmarshal(row.Payload, &x) != nil || x.MaxAt.IsZero() {
			continue // pre-registry inputs without measurements cannot establish a reference
		}
		epoch := w.raw.WorkoutSourceEpoch
		if epoch == "" {
			continue
		}
		r := hrref.Reference{Schema: hrref.Schema, ID: row.ID, DatasetID: w.raw.DatasetID, SubjectKey: "athlete", EffectiveFrom: row.From.UnixMicro(),
			KnownAt: row.Known.UnixMicro(), SourceEpoch: epoch, Provenance: row.Provenance, ContentHash: row.Hash,
			HRMax: &hrref.Measurement{Value: x.Max, MeasuredAt: x.MaxAt.UnixMicro(), RecordedAt: row.Known.UnixMicro(), Method: x.Method, Unit: x.Unit}}
		if x.Rest > 0 && !x.RestAt.IsZero() {
			r.RestingHR = &hrref.Measurement{Value: x.Rest, MeasuredAt: x.RestAt.UnixMicro(), RecordedAt: row.Known.UnixMicro(), Method: x.Method, Unit: x.Unit}
		}
		refs = append(refs, r)
	}
	return hrref.Resolve(hrref.Input{AsOf: captured.UnixMicro(), WorkoutStart: w.raw.Start, SourceEpoch: w.raw.WorkoutSourceEpoch, References: refs, DailyRHR: daily})
}

func dailyRHRFacts(in *rc.Input, workoutStart int64, epoch string, loc *time.Location) []hrref.DailyRHR {
	if in == nil || epoch == "" {
		return nil
	}
	start := time.UnixMicro(workoutStart).In(loc)
	from := start.AddDate(0, 0, -29)
	through := start.AddDate(0, 0, -1)
	byDate := map[string]hrref.DailyRHR{}
	known := map[string]int64{}
	for _, f := range in.Facts {
		if f.Type != rc.RHR || f.Value == nil || f.Start < from.UnixMicro() || f.Start >= start.UnixMicro() {
			continue
		}
		day := time.UnixMicro(f.Start).In(loc)
		if day.Before(from) || day.After(through) {
			continue
		}
		key := day.Format(time.DateOnly)
		if old, ok := known[key]; ok && old >= f.KnownAt {
			continue
		}
		known[key] = f.KnownAt
		byDate[key] = hrref.DailyRHR{Date: key, Start: f.Start, Value: f.Value, SourceEpoch: epoch, RunID: f.ID, Hash: f.Hash, Live: true}
	}
	out := make([]hrref.DailyRHR, 0, len(byDate))
	for _, v := range byDate {
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b hrref.DailyRHR) int { return cmp.Compare(a.Start, b.Start) })
	return out
}

func stackAnnotations(inputs []stackInput, asOf time.Time) ([]pf.EffortAnnotation, []rc.SleepTarget, []hrref.Resolution, string) {
	var efforts []pf.EffortAnnotation
	var targets []rc.SleepTarget
	var resolutions []hrref.Resolution
	epochs := map[string]bool{}
	for _, row := range effectiveStackInputs(inputs, asOf, asOf) {
		switch row.Kind {
		case "workout_effort":
			var x pf.EffortAnnotation
			if json.Unmarshal(row.Payload, &x) == nil && x.UserConfirmed {
				x.ID, x.AnnotatedAt, x.KnownAt = row.ID, row.From.UnixMicro(), row.Known.UnixMicro()
				efforts = append(efforts, x)
			}
		case "sleep_target":
			var x rc.SleepTarget
			if json.Unmarshal(row.Payload, &x) == nil {
				x.ID, x.EffectiveFrom, x.KnownAt, x.Source = row.ID, row.From.UnixMicro(), row.Known.UnixMicro(), "user_entered"
				targets = append(targets, x)
			}
		case "workout_overlap_resolution":
			var x hrref.Resolution
			if json.Unmarshal(row.Payload, &x) == nil {
				x.ID, x.KnownAt = row.ID, row.Known.UnixMicro()
				resolutions = append(resolutions, x)
			}
		case "source_context_selection":
			var x struct {
				SourceEpoch string `json:"sourceEpoch"`
			}
			if json.Unmarshal(row.Payload, &x) == nil && x.SourceEpoch != "" {
				epochs[x.SourceEpoch] = true
			}
		}
	}
	epoch := ""
	if len(epochs) > 1 {
		epoch = "conflict"
	}
	if len(epochs) == 1 {
		for key := range epochs {
			epoch = key
		}
	}
	return efforts, targets, resolutions, epoch
}

func computeModelStackDay(ctx context.Context, tx pgx.Tx, l Lease, stack string, date, at, captured time.Time, revision int64, epoch, collector string) ([]stackArtifact, error) {
	inputs, err := stackInputs(ctx, tx, l.DatasetID, captured)
	if err != nil {
		return nil, err
	}
	from := date.AddDate(0, 0, -186)
	workouts, err := stackWorkouts(ctx, tx, l, stack, from, at, captured, revision)
	if err != nil {
		return nil, err
	}
	efforts, targets, resolutions, source := stackAnnotations(inputs, at)
	suppressed, ambiguous := stackOverlap(workouts, resolutions, at)
	generation := l.TargetGeneration
	in := analysis.Snapshot{Generation: generation, Revision: revision}
	if err := loadRecoveryFacts(ctx, tx, l, &in, at, captured, date, epoch, collector, l.ID); err != nil {
		return nil, err
	}
	var ledger []tl.LedgerLoad
	var loads []rd.WorkoutLoad
	var evidence []vo2.Evidence
	var features []pf.FeatureRef
	for _, workout := range workouts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id, start, end := workout.raw.WorkoutID, time.UnixMicro(workout.raw.Start), time.UnixMicro(workout.raw.End)
		ref, err := workoutReference(workout, inputs, captured, dailyRHRFacts(in.RecoveryInput, workout.raw.Start, workout.raw.WorkoutSourceEpoch, date.Location()))
		if err != nil && !errors.Is(err, hrref.ErrConflict) && !errors.Is(err, hrref.ErrNotEffective) {
			return nil, analysis.Invalid()
		}
		if suppressed[id] {
			continue
		}
		var doseRef *tl.Reference
		if ref.HRMax != nil && ref.RestingHR != nil {
			stableHash := hrref.Hash(struct {
				Max, Rest *hrref.Resolved
				Epoch     string
			}{ref.HRMax, ref.RestingHR, ref.SourceEpoch})
			doseRef = &tl.Reference{ID: ref.HRMax.ReferenceID, Hash: stableHash, RestHR: ref.RestingHR.Value, MaxHR: ref.HRMax.Value,
				MeasuredAt: ref.HRMax.MeasuredAt, RecordedAt: ref.HRMax.RecordedAt, Provenance: ref.HRMax.Method, SourceEpoch: ref.SourceEpoch}
		}
		load, err := tl.CalculateTrainingLoadV2(tl.V2Input{WorkoutInput: tl.WorkoutInput{
			Calendar: date.Location(), Timezone: date.Location().String(), CalendarIdentity: tl.CalendarIdentity(date.Location(), start, end),
			Feature: workout.feature, Exposure: workout.exposure, FeatureRunID: workout.feature.Evidence.InputHash,
		}, Reference: doseRef, ReferenceConflict: errors.Is(err, hrref.ErrConflict), OverlapUnknown: ambiguous[id]}, generation)
		if err != nil {
			return nil, analysis.Invalid()
		}
		load.Scope = tl.ScopeV2For(id, stack)
		row := rd.WorkoutLoad{ID: id, End: end.UnixMicro(), FactHash: workout.raw.Fact.Hash, Hash: tl.Hash(load), Model: rd.LoadModelV2,
			ConfigHash: load.ConfigHash, AU: load.Load, Quality: load.Quality, Compatible: !ambiguous[id], Conflict: ambiguous[id]}
		for _, d := range load.Days {
			v := d.Load
			if ambiguous[id] {
				v = nil
			}
			row.Days = append(row.Days, rd.DailyLoad{Day: d.Day, AU: v, Quality: d.Quality})
			if d.Day >= from.Format(time.DateOnly) {
				ledger = append(ledger, tl.LedgerLoad{WorkoutID: id, Day: d.Day, Load: v, Quality: d.Quality})
			}
		}
		if doseRef != nil {
			row.ParameterID = doseRef.ID
		}
		loads = append(loads, row)
		if ambiguous[id] {
			continue
		}
		if end.After(date.AddDate(0, 0, -145)) && (source == "" || workout.raw.WorkoutSourceEpoch == source) {
			features = append(features, pf.FeatureRef{Feature: workout.feature, RunID: workout.feature.Evidence.InputHash, KnownAt: workout.known.UnixMicro(), FirstReceivedAt: workout.known.UnixMicro()})
		}
		if doseRef != nil && end.After(at.AddDate(0, 0, -76)) && (source == "" || doseRef.SourceEpoch == source) {
			v, err := stackVO2Evidence(workout, *doseRef, ref, date.Location())
			if err != nil {
				return nil, err
			}
			if v != nil {
				evidence = append(evidence, *v)
			}
		}
	}
	upload, err := workoutSyncEvidence(ctx, tx, l.DatasetID)
	if err != nil {
		return nil, err
	}
	day := date.Format(time.DateOnly)
	stateInput := tl.StateInput{Calendar: date.Location(), Timezone: date.Location().String(), DatasetID: l.DatasetID,
		Generation: generation, From: day, Through: day, HistoryFrom: from.Format(time.DateOnly), Loads: ledger}
	state, err := tl.CalculateLoadStateV2(stateInput)
	if err != nil {
		return nil, analysis.Invalid()
	}
	state.Scope = tl.ScopeStateV2For(stack)
	if source == "" && len(features) > 0 {
		latestEpoch := features[len(features)-1].Feature.Evidence.SourceEpochs
		features = slices.DeleteFunc(features, func(f pf.FeatureRef) bool { return !slices.Equal(f.Feature.Evidence.SourceEpochs, latestEpoch) })
	}
	abilityInput := pf.V2Input{Input: pf.Input{Calendar: date.Location(), Timezone: date.Location().String(), DatasetID: l.DatasetID,
		Generation: generation, AsOf: at.UnixMicro(), View: "revised", Features: features}, Annotations: efforts}
	ability, err := pf.CalculateV2(abilityInput)
	if err != nil {
		return nil, analysis.Invalid()
	}
	maximalInput := abilityInput
	maximalInput.Mode = pf.ModeMaximalV2
	maximal, err := pf.CalculateV2(maximalInput)
	if err != nil {
		return nil, analysis.Invalid()
	}
	maximal.Schema = pf.AbilitySchemaV2
	var referenceHash, sourceEpoch string
	if len(evidence) > 0 {
		last := evidence[len(evidence)-1]
		referenceHash, sourceEpoch = last.ReferenceHash, last.SourceEpoch
	}
	ownedInput := vo2.AggregateInput{DatasetID: l.DatasetID, Generation: generation, AsOf: at.UnixMicro(), ComputedAt: captured.UnixMicro(),
		Timezone: date.Location().String(), ReferenceHash: referenceHash, SourceEpoch: sourceEpoch, Evidence: evidence, View: "revised"}
	owned, err := vo2.Aggregate(ownedInput)
	if err != nil {
		return nil, analysis.Invalid()
	}

	in.RecoveryInput.Config = rc.DefaultV2Config(date.Location().String())
	recoveryInput := rc.V2Input{Input: *in.RecoveryInput, SleepTargets: targets}
	recovery, err := rc.CalculateV2(recoveryInput)
	if err != nil {
		return nil, analysis.Invalid()
	}
	if err := loadExternalFacts(ctx, tx, l, &in, at, captured, epoch, collector, l.ID); err != nil {
		return nil, err
	}
	external, err := ve.Calculate(*in.ExternalInput)
	if err != nil {
		return nil, analysis.Invalid()
	}
	recoveryID := syncing.NewID()
	readyInput := rd.ReadinessSnapshot{Dataset: l.DatasetID, Epoch: epoch, Collector: collector, Assignment: l.ID, LoadAssignment: stack,
		Generation: generation, AsOf: at.UnixMicro(), CapturedAt: captured.UnixMicro(), Config: rd.DefaultV3Config(date.Location().String()),
		Sync: upload, Loads: loads}
	readyInput.Recovery = &rd.RecoveryEvidence{RunID: recoveryID, Hash: rc.Hash(recovery), Generation: generation, Model: rc.ModelV2,
		Status: recovery.Status, Quality: recovery.Quality, Score: recovery.Score, AsOf: recovery.AsOf, Confidence: recovery.Confidence, Coverage: recovery.ComponentCoverage,
		FreshUntil: min(date.AddDate(0, 0, 1).UnixMicro(), recovery.AsOf+36*rd.Hour)}
	for d := date.AddDate(0, 0, -readyInput.Config.BaselineDays); !d.After(date); d = d.AddDate(0, 0, 1) {
		readyInput.Days = append(readyInput.Days, rd.Day{Date: d.Format(time.DateOnly), Start: d.UnixMicro(), End: d.AddDate(0, 0, 1).UnixMicro()})
	}
	ready, err := rd.CalculateV3(readyInput)
	if err != nil {
		return nil, analysis.Invalid()
	}
	artifacts := []stackArtifact{
		{key: "load_state", schema: tl.StateSchemaV2, algorithm: tl.StateAlgorithmV2, version: tl.VersionV2, value: state,
			inputHash: featureHash(stateInput)},
		{key: pf.ArtifactKeyV2, schema: pf.SchemaV2, algorithm: pf.CurveModel, version: pf.VersionV2, value: ability, inputHash: featureHash(abilityInput)},
		{key: "running_ability_maximal_v2", family: "model_research_v1/running_ability_maximal_v2", schema: pf.AbilitySchemaV2, algorithm: pf.CurveModel, version: pf.VersionV2, value: maximal, inputHash: featureHash(maximalInput)},
		{key: ve.ArtifactKey, schema: ve.Schema, algorithm: ve.Algorithm, version: "v1", value: external, inputHash: featureHash(in.ExternalInput)},
		{key: vo2.ArtifactKey, schema: vo2.Schema, algorithm: vo2.Algorithm, version: vo2.Version, value: owned, inputHash: featureHash(ownedInput)},
		{key: "physiological_recovery", schema: rc.SchemaV2, algorithm: rc.Algorithm, version: "v2", value: recovery, runID: recoveryID, inputHash: featureHash(recoveryInput)},
		{key: "training_readiness", schema: rd.SchemaV3, algorithm: rd.Algorithm, version: "v3", value: ready, inputHash: ready.InputHash},
	}
	artifacts = append(artifacts, modelResearchArtifacts(workouts, state, ready, l.DatasetID, generation, stack, at.UnixMicro())...)
	if date.Format(time.DateOnly) == captured.In(date.Location()).Format(time.DateOnly) {
		for _, w := range workouts {
			artifacts = append(artifacts, stackArtifact{key: wf.ArtifactKey, schema: wf.Schema, algorithm: wf.Algorithm, version: wf.Version, value: w.feature, inputHash: w.feature.Evidence.InputHash})
		}
	}
	return artifacts, nil
}

func stackOverlap(workouts []stackWorkout, resolutions []hrref.Resolution, at time.Time) (map[string]bool, map[string]bool) {
	var in []hrref.Workout
	for _, w := range workouts {
		f := w.feature
		x := hrref.Workout{ID: f.WorkoutID, DatasetID: f.DatasetID, ActivityType: f.ActivityType, Start: f.Timing.Start, End: f.Timing.End, ActiveCertain: f.Timing.Active != nil}
		for _, a := range w.exposure.Active {
			x.Active = append(x.Active, hrref.Interval{Start: a.Start, End: a.End})
		}
		in = append(in, x)
	}
	overlaps := hrref.Detect(in)
	pairs := map[[2]string]bool{}
	for _, o := range overlaps {
		pairs[[2]string{o.First, o.Second}], pairs[[2]string{o.Second, o.First}] = true, true
	}
	resolutions = slices.DeleteFunc(resolutions, func(r hrref.Resolution) bool { return !pairs[[2]string{r.PrimaryID, r.DuplicateID}] })
	resolved := hrref.ApplyResolutions(in, resolutions, at.UnixMicro())
	suppressed, ambiguous := map[string]bool{}, map[string]bool{}
	for _, id := range resolved.Suppressed {
		suppressed[id] = true
	}
	for _, id := range resolved.Conflicts {
		ambiguous[id] = true
	}
	// Opposing or chained resolutions cannot suppress a primary as well.
	for _, r := range resolutions {
		if suppressed[r.PrimaryID] {
			ambiguous[r.PrimaryID], ambiguous[r.DuplicateID] = true, true
		}
	}
	for id := range ambiguous {
		delete(suppressed, id)
	}
	for _, o := range overlaps {
		if !suppressed[o.First] && !suppressed[o.Second] {
			ambiguous[o.First], ambiguous[o.Second] = true, true
		}
	}
	return suppressed, ambiguous
}

func stackVO2Evidence(w stackWorkout, dose tl.Reference, ref hrref.Snapshot, loc *time.Location) (*vo2.Evidence, error) {
	f, n := w.feature, w.raw
	if f.Environment.Indoor == nil || *f.Environment.Indoor || f.Timing.Active == nil || f.Environment.State == wf.Ambiguous {
		return nil, nil
	}
	sourceSupported := false
	for _, s := range n.Provenance {
		if s.Epoch == n.WorkoutSourceEpoch && s.Classification == "apple_watch" {
			sourceSupported = true
			break
		}
	}
	if !sourceSupported {
		return nil, nil
	}
	t := wf.BuildTimeline(*n)
	input := vo2.Workout{ID: f.WorkoutID, DatasetID: f.DatasetID, Generation: f.Generation, ActivityType: f.ActivityType, Outdoor: true,
		Start: n.Start, End: n.End, Day: time.UnixMicro(n.End).In(loc).Format(time.DateOnly), KnownAt: w.known.UnixMicro(),
		DistanceM: f.Distance.SpeedIntegral, RecordedDistanceM: n.Distance, TemperatureC: f.Environment.TemperatureC}
	if f.Environment.Humidity != nil {
		p := *f.Environment.Humidity * 100
		input.HumidityPct = &p
	}
	last := n.Start
	for _, a := range t.Pause.Active {
		if a.Start > last {
			input.Pauses = append(input.Pauses, vo2.Pause{Start: last, End: a.Start})
		}
		last = a.End
	}
	if last < n.End {
		input.Pauses = append(input.Pauses, vo2.Pause{Start: last, End: n.End})
	}
	// Align three ordered, nonoverlapping support series. Unknown spans remain
	// absent, and downstream window gates still enforce common time coverage.
	h, s, g := 0, 0, 0
	for h < len(t.HeartRate.Spans) && s < len(t.Speed.Spans) && g < len(t.Route.Grades) {
		hr, speed, grade := t.HeartRate.Spans[h], t.Speed.Spans[s], t.Route.Grades[g]
		start, end := max(hr.Start, speed.Start, grade.Start), min(hr.End, speed.End, grade.End)
		if end > start {
			mid := start + (end-start)/2
			hv, sv, gv := spanValue(hr, mid), spanValue(speed, mid), spanValue(grade, mid)
			input.Samples = append(input.Samples, vo2.Sample{Start: start, End: end, HR: &hv, Speed: &sv, Grade: &gv})
		}
		if hr.End == end {
			h++
		}
		if speed.End == end {
			s++
		}
		if grade.End == end {
			g++
		}
	}
	v, err := vo2.EvaluateWorkout(input, vo2.Reference{MaxHR: dose.MaxHR, RestHR: dose.RestHR, MaxMeasured: ref.HRMax.MeasuredAt,
		RestMeasured: ref.RestingHR.MeasuredAt, ReferenceID: dose.ID, Hash: dose.Hash, SourceEpoch: dose.SourceEpoch})
	if err != nil {
		return nil, analysis.Invalid()
	}
	if !v.Qualified {
		return nil, nil
	}
	return &vo2.Evidence{WorkoutID: v.WorkoutID, Day: v.Day, At: v.End, SourceEpoch: v.SourceEpoch, ReferenceHash: v.ReferenceHash,
		V: *v.CandidateV, Q: v.Quality, HRMean: *v.MedianHR, SpeedMPS: *v.MedianSpeedMPS, MaxHR: v.MaxHR, RestHR: v.RestHR,
		KnownAt: max(v.KnownAt, v.End), EnvironmentKnown: v.EnvironmentKnown}, nil
}

func spanValue(s wf.Span, at int64) float64 {
	return s.V0 + (s.V1-s.V0)*float64(at-s.Start)/float64(s.End-s.Start)
}
