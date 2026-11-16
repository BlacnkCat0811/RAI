package postgres

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"rai/server/internal/analysis"
	rd "rai/server/internal/readiness"
	rc "rai/server/internal/recovery"
	rf "rai/server/internal/runfitness"
	sr "rai/server/internal/sessionrecovery"
	te "rai/server/internal/trainingeffect"
	tl "rai/server/internal/trainingload"
)

// Prior running-fitness speed anchors are one bounded batch read of clean
// published day states, never today's estimate fed back into its own
// evidence. Only a speed dimension with direct evidence can classify short
// repetitions; invalidated publications cannot. The same day state also
// carries vT/v90s for the training-effect anaerobic reserve; loadFocus keeps
// its own v6-only reference so its input identity is unchanged.
type priorFitness struct {
	ability tl.AbilityReference
	fitness *te.FitnessReference
}

// priorFitnessCache keeps the extracted speed references of published
// running-fitness day states. Artifacts are immutable and the key carries the
// run ID and the verified content hash, so an entry can never go stale; it only
// saves re-reading, decoding and re-hashing up to 187 day states per job.
var priorFitnessCache = struct {
	sync.Mutex
	m map[string]priorFitnessEntry
}{m: map[string]priorFitnessEntry{}}

type priorFitnessEntry struct {
	usable bool
	asOf   int64
	status string
	vt, v6 float64
	hasVT  bool
	v90s   *float64
}

const priorFitnessCacheMax = 8192

func preWorkoutAbilities(ctx context.Context, tx pgx.Tx, dataset, stack string, from, to time.Time) ([]priorFitness, error) {
	rows, err := tx.Query(ctx, `SELECT r.id::text,encode(a.content_sha256,'hex'),p.published_at FROM analysis_publications p
 JOIN analysis_runs r ON r.dataset_id=p.dataset_id AND r.id=p.published_run_id AND r.status='SUCCEEDED'
 JOIN analysis_artifacts a ON a.dataset_id=r.dataset_id AND a.run_id=r.id AND a.artifact_key=$5
 JOIN dirty_scopes d ON d.dataset_id=p.dataset_id AND d.scope_key=p.scope_key AND d.generation=p.generation AND d.clean_generation=p.generation
 WHERE p.dataset_id=$1 AND p.scope_key LIKE 'model/'||$2||'/athlete/day/v2/%' AND p.day BETWEEN $3::date AND $4::date
 ORDER BY p.day LIMIT 187`, dataset, stack, from.Format(time.DateOnly), to.Format(time.DateOnly), rf.ArtifactKey)
	if err != nil {
		return nil, err
	}
	type ref struct {
		run, hash string
		published time.Time
	}
	refs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ref, error) {
		var x ref
		return x, r.Scan(&x.run, &x.hash, &x.published)
	})
	if err != nil {
		return nil, err
	}
	key := func(x ref) string { return dataset + "/" + x.run + "/" + x.hash }
	entries := make(map[string]priorFitnessEntry, len(refs))
	var missing []string
	priorFitnessCache.Lock()
	for _, x := range refs {
		if e, ok := priorFitnessCache.m[key(x)]; ok {
			entries[x.run] = e
		} else {
			missing = append(missing, x.run)
		}
	}
	priorFitnessCache.Unlock()
	if len(missing) > 0 {
		rows, err := tx.Query(ctx, `SELECT a.run_id::text,a.payload,encode(a.content_sha256,'hex') FROM analysis_artifacts a
 WHERE a.dataset_id=$1 AND a.artifact_key=$2 AND a.run_id=ANY($3::uuid[])`, dataset, rf.ArtifactKey, missing)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var run, hash string
			var payload []byte
			if err := rows.Scan(&run, &payload, &hash); err != nil {
				rows.Close()
				return nil, err
			}
			var r rf.State
			e := priorFitnessEntry{}
			if json.Unmarshal(payload, &r) == nil && r.DatasetID == dataset && r.Schema == rf.Schema && rf.Hash(r) == hash && r.Params.V6 != nil {
				if s := r.Dimensions.Speed.Status; s == "supported" || s == "estimated" {
					e = priorFitnessEntry{usable: true, asOf: r.AsOf, status: s, v6: *r.Params.V6, v90s: r.Params.V90s}
					if r.Params.VT != nil {
						e.vt, e.hasVT = *r.Params.VT, true
					}
				}
			}
			entries[run] = e
			priorFitnessCache.Lock()
			if len(priorFitnessCache.m) >= priorFitnessCacheMax {
				priorFitnessCache.m = map[string]priorFitnessEntry{}
			}
			priorFitnessCache.m[dataset+"/"+run+"/"+hash] = e
			priorFitnessCache.Unlock()
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	var out []priorFitness
	for _, x := range refs {
		e, ok := entries[x.run]
		if !ok || !e.usable {
			continue
		}
		at := max(e.asOf, x.published.UnixMicro())
		p := priorFitness{ability: tl.AbilityReference{V6: e.v6, At: at, Hash: x.hash, Status: e.status}}
		if e.hasVT {
			p.fitness = &te.FitnessReference{VT: e.vt, V6: e.v6, V90s: e.v90s, At: at, Hash: x.hash, Status: e.status}
		}
		out = append(out, p)
	}
	return out, nil
}

// trainingLevelInput sums completed, non-conflicting runs of the same source
// before this run's start (28 and 90 days). Running time needs no HR, so a
// run without a load still counts towards the level.
func trainingLevelInput(w stackWorkout, loads []rd.WorkoutLoad, byID map[string]stackWorkout, loc *time.Location) te.LevelInput {
	var in te.LevelInput
	days28, days90 := map[string]bool{}, map[string]bool{}
	from28 := time.UnixMicro(w.start).AddDate(0, 0, -28).UnixMicro()
	from90 := time.UnixMicro(w.start).AddDate(0, 0, -90).UnixMicro()
	for _, old := range loads {
		ow, ok := byID[old.ID]
		if !ok || old.End >= w.start || old.End < from90 || ow.sourceEpoch != w.sourceEpoch || old.Conflict || ow.feature.Timing.Active == nil {
			continue
		}
		sec := *ow.feature.Timing.Active
		d := time.UnixMicro(old.End).In(loc).Format(time.DateOnly)
		in.Seconds90 += sec
		days90[d] = true
		if old.End >= from28 {
			in.Seconds28 += sec
			days28[d] = true
		}
	}
	in.Days28, in.Days90 = len(days28), len(days90)
	return in
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

// optimizedStackOutputs computes structure, session fatigue and readiness.
// Training effects are only needed where today's release records them on each
// Workout (needEffects); a cached effect with the same input key is reused and
// only the remaining Workouts load their effort points.
func optimizedStackOutputs(ctx context.Context, tx pgx.Tx, l Lease, stack string, date, at, captured time.Time, workouts []stackWorkout, pending []stackPendingLoad, loads []rd.WorkoutLoad, state tl.StateResult, recovery rc.V3Result, needEffects bool) ([]stackArtifact, map[string]te.Result, []workoutCacheRow, rd.V4Result, error) {
	var artifacts []stackArtifact
	loc := date.Location()
	prior, err := preWorkoutAbilities(ctx, tx, l.DatasetID, stack, date.AddDate(0, 0, -186), date)
	if err != nil {
		return nil, nil, nil, rd.V4Result{}, err
	}
	type effectJob struct {
		i         int
		in        te.Input
		key       string
		estimated bool
	}
	var effectJobs []effectJob
	var effectRows []workoutCacheRow
	var focus []tl.WorkoutFocus
	effects := map[string]te.Result{}
	var sessions []sr.Workout
	byID := map[string]stackWorkout{}
	for _, w := range workouts {
		byID[w.id] = w
	}
	for pi, p := range pending {
		w, r := p.w, p.r
		var pre *tl.AbilityReference
		var fitness *te.FitnessReference
		for i := range prior {
			p := &prior[i]
			if p.ability.At < w.start && w.start-p.ability.At < 28*sr.Hour*24 && (pre == nil || p.ability.At > pre.At) {
				pre, fitness = &p.ability, p.fitness
			}
		}
		f, e := tl.ClassifyLoad(r.load, w.exposure, w.feature, r.doseRef, pre, loc)
		if e != nil {
			return nil, nil, nil, rd.V4Result{}, analysis.Invalid()
		}
		focus = append(focus, f)
		// Training effect: this run's own facts plus pre-run references only,
		// so later training never rewrites a historical score.
		if needEffects {
			effectInput := te.Input{WorkoutID: w.id, Start: w.start, LoadTier: r.load.QualityTier, Exposure: w.exposure,
				Fitness: fitness, Level: trainingLevelInput(w, loads, byID, loc)}
			if r.doseRef != nil {
				effectInput.HR = &te.HRReference{RestHR: r.doseRef.RestHR, MaxHR: r.doseRef.MaxHR, Hash: r.doseRef.Hash}
			}
			key := stackEffectKey(w, effectInput)
			estimated := r.ref.Evidence != nil && (r.ref.Evidence.MaxHR.Status == "estimated" || r.ref.Evidence.RestingHR.Status == "estimated")
			var cached te.Result
			if w.effectKey == key && len(w.effectCached) > 0 && json.Unmarshal(w.effectCached, &cached) == nil && cached.WorkoutID == w.id {
				effects[w.id] = estimateEffect(cached, estimated)
			} else {
				effectJobs = append(effectJobs, effectJob{i: pi, in: effectInput, key: key, estimated: estimated})
			}
		}
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
	if len(effectJobs) > 0 {
		need := make([]*stackWorkout, 0, len(effectJobs))
		for _, j := range effectJobs {
			need = append(need, &pending[j.i].w)
		}
		if err := loadEffortPoints(ctx, tx, l.DatasetID, stack, need); err != nil {
			return nil, nil, nil, rd.V4Result{}, err
		}
		for _, j := range effectJobs {
			w := pending[j.i].w
			if w.pointsOmitted {
				// Same snapshot as the cached feature: the points must exist.
				return nil, nil, nil, rd.V4Result{}, analysis.Invalid()
			}
			j.in.Effort = w.feature.Effort
			effect := te.Calculate(j.in)
			effectRows = append(effectRows, workoutCacheRow{id: w.id, key: j.key, value: featureJSON(effect)})
			effects[w.id] = estimateEffect(effect, j.estimated)
		}
	}
	focusResult := tl.AggregateFocus(l.DatasetID, stack, l.TargetGeneration, at.UnixMicro(), date, focus)
	sessionInput := sr.Input{Dataset: l.DatasetID, Stack: stack, Generation: l.TargetGeneration, AsOf: at.UnixMicro(), ValidBefore: date.AddDate(0, 0, 1).UnixMicro(), Workouts: sessions}
	session, err := sr.Calculate(sessionInput)
	if err != nil {
		return nil, nil, nil, rd.V4Result{}, analysis.Invalid()
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
		return nil, nil, nil, rd.V4Result{}, analysis.Invalid()
	}
	for _, a := range []stackArtifact{
		{key: sr.Key, schema: sr.Schema, algorithm: sr.Algorithm, version: "v1", value: session, inputHash: session.InputHash},
		{key: "load_focus", schema: tl.FocusSchema, algorithm: tl.FocusAlgorithm, version: "v1", value: focusResult, inputHash: focusResult.InputHash},
	} {
		a.family = "model_product_v3/" + a.key
		artifacts = append(artifacts, a)
	}
	return artifacts, effects, effectRows, ready, nil
}

// estimateEffect marks the aerobic score estimated when the HR reference is.
func estimateEffect(effect te.Result, estimated bool) te.Result {
	if estimated && effect.Aerobic.Value != nil {
		effect.Aerobic.Status = "estimated"
		effect.Aerobic.ReasonCodes = append(append([]string(nil), effect.Aerobic.ReasonCodes...), "hr_reference_estimated")
	}
	return effect
}
