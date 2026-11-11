package postgres

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/analysis"
	pf "rai/server/internal/performance"
	rd "rai/server/internal/readiness"
	rc "rai/server/internal/recovery"
	ra "rai/server/internal/runningability"
	sr "rai/server/internal/sessionrecovery"
	tl "rai/server/internal/trainingload"
	vo2 "rai/server/internal/vo2max"
)

// Prior abilities are one bounded batch read, never today's estimate fed back
// into its own evidence. Invalidated publications cannot classify repetitions.
func preWorkoutAbilities(ctx context.Context, tx pgx.Tx, dataset, stack string, from, to time.Time) ([]tl.AbilityReference, error) {
	rows, err := tx.Query(ctx, `SELECT a.payload,encode(a.content_sha256,'hex'),p.published_at FROM analysis_publications p
 JOIN analysis_runs r ON r.dataset_id=p.dataset_id AND r.id=p.published_run_id AND r.status='SUCCEEDED'
 JOIN analysis_artifacts a ON a.dataset_id=r.dataset_id AND a.run_id=r.id AND a.artifact_key='performance_state_v2'
 JOIN dirty_scopes d ON d.dataset_id=p.dataset_id AND d.scope_key=p.scope_key AND d.generation=p.generation AND d.clean_generation=p.generation
 WHERE p.dataset_id=$1 AND p.scope_key LIKE 'model/'||$2||'/athlete/day/v2/%' AND p.day BETWEEN $3::date AND $4::date
 ORDER BY p.day LIMIT 187`, dataset, stack, from.Format(time.DateOnly), to.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []tl.AbilityReference
	for rows.Next() {
		var payload []byte
		var hash string
		var published time.Time
		if err := rows.Scan(&payload, &hash, &published); err != nil {
			return nil, err
		}
		var r pf.Result
		if json.Unmarshal(payload, &r) == nil && r.DatasetID == dataset && r.Schema == pf.SchemaV2 && pf.Hash(r) == hash && r.Ability != nil && r.Status == "available" {
			out = append(out, tl.AbilityReference{V60: r.Ability.V60, At: max(r.AsOf, published.UnixMicro()), Hash: hash, Mode: "demonstrated"})
		}
	}
	return out, rows.Err()
}
func floatMedian(xs []float64) *float64 {
	if len(xs) == 0 {
		return nil
	}
	sort.Float64s(xs)
	v := xs[len(xs)/2]
	if len(xs)%2 == 0 {
		v = (xs[len(xs)/2-1] + v) / 2
	}
	return &v
}

func optimizedStackOutputs(ctx context.Context, tx pgx.Tx, l Lease, stack string, date, at, captured time.Time, workouts []stackWorkout, pending []stackPendingLoad, loads []rd.WorkoutLoad, state tl.StateResult, recovery rc.V3Result, ability, maximal pf.Result, owned vo2.Result, efforts []pf.EffortAnnotation) ([]stackArtifact, rd.V4Result, error) {
	var artifacts []stackArtifact
	loc := date.Location()
	prior, err := preWorkoutAbilities(ctx, tx, l.DatasetID, stack, date.AddDate(0, 0, -186), date)
	if err != nil {
		return nil, rd.V4Result{}, err
	}
	var focus []tl.WorkoutFocus
	var sessions []sr.Workout
	byID := map[string]stackWorkout{}
	for _, w := range workouts {
		byID[w.id] = w
	}
	for _, p := range pending {
		w, r := p.w, p.r
		var pre *tl.AbilityReference
		for i := range prior {
			p := &prior[i]
			if p.At < w.start && w.start-p.At < 28*sr.Hour*24 && (pre == nil || p.At > pre.At) {
				pre = p
			}
		}
		f, e := tl.ClassifyLoad(r.load, w.exposure, w.feature, r.doseRef, pre, loc)
		if e != nil {
			return nil, rd.V4Result{}, analysis.Invalid()
		}
		focus = append(focus, f)
		// B and longRef use only completed, comparable runs before this run's
		// start; current-day evidence cannot redefine its own fatigue pulse.
		// The 28 days before the run are preferred. After a break they hold too
		// few training days, so the 90 days before it are the fallback: the
		// same source and the same five-day, 14-day-span requirement, marked on
		// the Workout so the result is reported as estimated.
		reference := func(days int) (*float64, *float64) {
			daily := map[string]float64{}
			var minutes []float64
			from := time.UnixMicro(w.start).AddDate(0, 0, -days).UnixMicro()
			unknownDays := map[string]bool{}
			earliest, latest := "", ""
			for _, old := range loads {
				ow := byID[old.ID]
				if old.End >= w.start || old.End < from || ow.sourceEpoch != w.sourceEpoch {
					continue
				}
				d := time.UnixMicro(old.End).In(loc).Format(time.DateOnly)
				if old.AU == nil || !old.Compatible || old.Conflict {
					unknownDays[d] = true
					continue
				}
				daily[d] += *old.AU
				if earliest == "" || d < earliest {
					earliest = d
				}
				if d > latest {
					latest = d
				}
				if ow.feature.Timing.Active != nil {
					minutes = append(minutes, *ow.feature.Timing.Active/60)
				}
			}
			var values []float64
			for d, v := range daily {
				if v > 0 && !unknownDays[d] {
					values = append(values, v)
				}
			}
			first, _ := time.Parse(time.DateOnly, earliest)
			last, _ := time.Parse(time.DateOnly, latest)
			if len(values) >= 5 && last.Sub(first) >= 14*24*time.Hour {
				return floatMedian(values), floatMedian(minutes)
			}
			return nil, floatMedian(minutes)
		}
		baseline, longRef := reference(28)
		extended := false
		if baseline == nil {
			if wide, wideLong := reference(90); wide != nil {
				baseline, longRef, extended = wide, wideLong, true
			}
		}
		duration := float64(w.end-w.start) / 60e6
		if w.feature.Timing.Active != nil {
			duration = *w.feature.Timing.Active / 60
		}
		s := sr.Workout{ID: w.id, Hash: f.InputHash, Start: w.start, End: w.end, AU: r.load.Load, Baseline: baseline, BaselineExtended: extended, LongRef: longRef, Minutes: duration}
		if s.AU != nil && *s.AU > 0 {
			s.HighShare = f.Buckets.High / *s.AU
			s.AnaerobicShare = f.Buckets.Anaerobic / *s.AU
			s.UnclassifiedShare = f.Buckets.Unclassified / *s.AU
		}
		sessions = append(sessions, s)
	}
	focusResult := tl.AggregateFocus(l.DatasetID, stack, l.TargetGeneration, at.UnixMicro(), date, focus)
	sessionInput := sr.Input{Dataset: l.DatasetID, Stack: stack, Generation: l.TargetGeneration, AsOf: at.UnixMicro(), ValidBefore: date.AddDate(0, 0, 1).UnixMicro(), Workouts: sessions}
	session, err := sr.Calculate(sessionInput)
	if err != nil {
		return nil, rd.V4Result{}, analysis.Invalid()
	}
	readyInput := rd.V4Input{Snapshot: rd.ReadinessSnapshot{Dataset: l.DatasetID, Assignment: l.ID, LoadAssignment: stack, Generation: l.TargetGeneration, AsOf: at.UnixMicro(), CapturedAt: captured.UnixMicro(), Config: rd.DefaultV4Config(loc.String()), Loads: loads},
		Physiology: rd.Physiological{Score: recovery.Score, Mode: recovery.Mode, Status: recovery.Status, Confidence: recovery.Confidence, EvidenceAt: recovery.EvidenceAt, ValidBefore: recovery.ValidBefore, Hash: rc.Hash(recovery)}, SessionAvailable: session.Residual != nil, SessionMode: session.Mode, SessionUnknown: session.UnknownBurden}
	for _, t := range session.Terms {
		readyInput.Fatigue = append(readyInput.Fatigue, rd.FatigueTerm{End: t.End, Low: t.Pulse.Low, High: t.Pulse.High, TauLow: t.Tau.Low, TauHigh: t.Tau.High})
	}
	for _, d := range state.Days {
		if d.Day == date.Format(time.DateOnly) {
			readyInput.Acute = d.Acute.Value
			readyInput.Chronic = d.Chronic.Value
		}
	}
	for d := date.AddDate(0, 0, -readyInput.Snapshot.Config.BaselineDays); !d.After(date); d = d.AddDate(0, 0, 1) {
		readyInput.Snapshot.Days = append(readyInput.Snapshot.Days, rd.Day{Date: d.Format(time.DateOnly), Start: d.UnixMicro(), End: d.AddDate(0, 0, 1).UnixMicro()})
	}
	ready, err := rd.CalculateV4(readyInput)
	if err != nil {
		return nil, rd.V4Result{}, analysis.Invalid()
	}
	branch := func(r pf.Result) ra.Branch {
		epoch := ""
		if len(r.Support.SourceEpochs) == 1 {
			epoch = r.Support.SourceEpochs[0]
		}
		b := ra.Branch{Mode: r.EvidenceMode, Status: r.Status, Source: epoch, EvidenceAt: r.Support.LatestEvidence, Confidence: r.Confidence}
		if r.Ability != nil {
			a := r.Ability
			b.V60 = &a.V60
			b.Score = &a.Score
			b.Gamma = &a.Gamma
		}
		return b
	}
	vin := ra.VO2{Value: owned.Value, CandidateOnly: owned.CandidateOnly, SupportOK: owned.LOO.SupportOK, Status: owned.Status, Stable: owned.Stability == "stable", Count: owned.Workouts, Source: owned.SourceEpoch, Confidence: owned.Confidence}
	if owned.EvidenceAt != nil {
		vin.EvidenceAt = *owned.EvidenceAt
	}
	if owned.Sensitivity.Range != nil {
		vin.Range = &ra.Range{Low: owned.Sensitivity.Range.Low, High: owned.Sensitivity.Range.High}
	}
	ain := ra.Input{Dataset: l.DatasetID, Stack: stack, Generation: l.TargetGeneration, AsOf: at.UnixMicro(), Demonstrated: branch(ability), Maximal: branch(maximal), VO2: vin, Progress: ra.Progress{Workouts: ability.Support.IndependentWorkouts, SpanDays: ability.Support.HistorySpan, ShortWorkouts: ability.Support.ShortWorkouts, LongWorkouts: ability.Support.LongWorkouts, TargetDays: ability.ValidTargetDays}}
	for _, e := range ability.Evidence {
		if e.Duration == 3600 && e.SourceEpoch == vin.Source && e.WorkoutEnd >= at.AddDate(0, 0, -28).UnixMicro() && (ain.Observed60 == nil || e.MeanSpeed > *ain.Observed60) {
			v := e.MeanSpeed
			ain.Observed60 = &v
			ain.Observed60Source = e.SourceEpoch
		}
	}
	ain.Calibrations, err = stackAbilityCalibrations(workouts, efforts, owned, at, loc)
	if err != nil {
		return nil, rd.V4Result{}, err
	}
	estimate, err := ra.Calculate(ain)
	if err != nil {
		return nil, rd.V4Result{}, analysis.Invalid()
	}
	for _, a := range []stackArtifact{
		{key: sr.Key, schema: sr.Schema, algorithm: sr.Algorithm, version: "v1", value: session, inputHash: session.InputHash},
		{key: ra.Key, schema: ra.Schema, algorithm: ra.Algorithm, version: "v1", value: estimate, inputHash: estimate.InputHash},
		{key: "load_focus", schema: tl.FocusSchema, algorithm: tl.FocusAlgorithm, version: "v1", value: focusResult, inputHash: focusResult.InputHash},
	} {
		a.family = "model_product_v3/" + a.key
		artifacts = append(artifacts, a)
	}
	return artifacts, ready, nil
}

// Calibration is deliberately limited to three or more independently confirmed
// approximately one-hour efforts, paired with already-known product-qualified
// RAI VO2 evidence before each effort. No fitted curve is replicated as several
// independent observations and current effort/VO2 never validate themselves.
func stackAbilityCalibrations(workouts []stackWorkout, efforts []pf.EffortAnnotation, owned vo2.Result, at time.Time, loc *time.Location) ([]ra.Calibration, error) {
	if owned.Value == nil || owned.CandidateOnly || !owned.LOO.SupportOK {
		return nil, nil
	}
	byID := map[string]stackWorkout{}
	for _, w := range workouts {
		byID[w.id] = w
	}
	var candidates []ra.Calibration
	var starts []int64
	seen := map[string]bool{}
	for _, a := range efforts {
		if seen[a.WorkoutID] || a.Revoked || a.Mode != pf.ModeMaximalV2 || (!a.UserConfirmed && !a.IndependentlyVerified) || a.KnownAt > at.UnixMicro() {
			continue
		}
		w, ok := byID[a.WorkoutID]
		if !ok || w.sourceEpoch != owned.SourceEpoch || w.feature.Timing.Active == nil || *w.feature.Timing.Active < 3600 || *w.feature.Timing.Active > 3780 {
			continue
		}
		for _, windows := range w.feature.Windows {
			g := windows.GAP
			if windows.Duration != 3600 || g == nil || g.GAPSpeed == nil || g.Qualification.Status == "ineligible" {
				continue
			}
			seen[a.WorkoutID] = true
			candidates = append(candidates, ra.Calibration{Day: time.UnixMicro(w.end).In(loc).Format(time.DateOnly), WorkoutID: w.id, At: w.end, KnownAt: max(w.known.UnixMicro(), a.KnownAt, a.AnnotatedAt), V60: *g.GAPSpeed, Source: owned.SourceEpoch})
			starts = append(starts, w.start)
			break
		}
	}
	// Keep event order stable even when confirmations arrive out of order.
	type calibrationCandidate struct {
		value ra.Calibration
		start int64
	}
	ordered := make([]calibrationCandidate, len(candidates))
	for i, c := range candidates {
		ordered[i] = calibrationCandidate{c, starts[i]}
	}
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i].value, ordered[j].value
		if a.At != b.At {
			return a.At < b.At
		}
		return a.WorkoutID < b.WorkoutID
	})
	for i, c := range ordered {
		candidates[i] = c.value
		starts[i] = c.start
	}
	if len(candidates) < 3 {
		return nil, nil
	}
	var out []ra.Calibration
	// The 90-day feature pool and 512-Workout loader already bound this work.
	// Only the latest six one-hour calibration efforts can affect this release.
	first := max(0, len(candidates)-6)
	for i := first; i < len(candidates); i++ {
		c := candidates[i]
		var evidence []vo2.Evidence
		for _, e := range owned.Evidence {
			if e.At < starts[i] && e.KnownAt <= starts[i] && e.WorkoutID != c.WorkoutID {
				evidence = append(evidence, e)
			}
		}
		result, e := vo2.Aggregate(vo2.AggregateInput{DatasetID: owned.DatasetID, Generation: owned.Generation, AsOf: starts[i], ComputedAt: starts[i], Timezone: loc.String(), ReferenceHash: owned.ReferenceHash, SourceEpoch: owned.SourceEpoch, Evidence: evidence, View: "as_known"})
		if e != nil {
			return nil, analysis.Invalid()
		}
		if result.Value != nil && !result.CandidateOnly && result.LOO.SupportOK && result.Stability == "stable" && result.Sensitivity.Width != nil && *result.Sensitivity.Width <= .3 {
			c.VO2 = *result.Value
			out = append(out, c)
		}
	}
	return out, nil
}
