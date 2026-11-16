package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"rai/server/internal/analysis"
	am "rai/server/internal/athletemodel"
	rc "rai/server/internal/recovery"
	rf "rai/server/internal/runfitness"
	"rai/server/internal/syncing"
	"rai/server/internal/workoutdetail"
)

// paceRelease is the model stack a pace review replays.
type paceRelease struct{ stack, tz, manifest string }

// paceInputs are stack inputs a day release has already loaded. The workouts
// must cover [end-90 local days, end] like the live query.
type paceInputs struct {
	inputs   []stackInput
	workouts []stackWorkout
	resting  *rc.Input
}

// loadPaceAnalysis is the live (read-time) review against the active stack.
func loadPaceAnalysis(ctx context.Context, tx pgx.Tx, p syncing.Principal, id string, d *workoutDetailData) error {
	var generation int64
	if err := tx.QueryRow(ctx, `SELECT generation FROM dirty_scopes WHERE dataset_id=$1 AND scope_key='workout:HKWorkoutTypeIdentifier:'||$2`, p.DatasetID, id).Scan(&generation); err != nil {
		return err
	}
	var rel *paceRelease
	act, relErr := activeFitnessRelease(ctx, tx, p.DatasetID)
	if relErr == nil {
		rel = &paceRelease{stack: act.stack, tz: act.tz, manifest: act.manifest}
	}
	a, err := computePaceAnalysis(ctx, tx, p.DatasetID, id, d.Start, d.End, generation, time.Now().UTC(), rel, relErr, nil)
	if err != nil {
		return err
	}
	d.PaceAnalysis = a
	return nil
}

// computePaceAnalysis replays the running-fitness state at the Workout start
// (pre-run) and, when that has no bands, at its end (post-run estimate).
// Errors of this Workout's own inputs become explicit statuses; only storage
// errors are returned. relErr is the outcome of resolving rel.
func computePaceAnalysis(ctx context.Context, tx pgx.Tx, dataset, id string, start, end time.Time, generation int64, captured time.Time,
	rel *paceRelease, relErr error, pre *paceInputs) (*workoutdetail.PaceAnalysis, error) {
	a := workoutdetail.NewPaceAnalysis()
	fail := func(err error) (*workoutdetail.PaceAnalysis, error) {
		var f *analysis.Failure
		if !errors.As(err, &f) {
			return nil, err
		}
		if a.Absolute.PaceZonePolicyVersion == "" {
			a.Absolute.PaceZoneStatus = "input_unavailable"
		}
		a.Personal.PaceZoneStatus = "input_unavailable"
		a.Personal.ExplanationCodes = append(a.Personal.ExplanationCodes, f.Code)
		return a, nil
	}
	raw, err := stackRaw(ctx, tx, Lease{DatasetID: dataset, TargetGeneration: generation}, "", id, captured, 0)
	if err != nil {
		return fail(err)
	}
	a.Absolute = workoutdetail.BuildAbsolutePace(*raw)
	if a.Absolute.PaceZoneStatus == "available" {
		a.DefaultView = "absolute"
	}
	if errors.Is(relErr, am.ErrNotActivated) || errors.Is(relErr, am.ErrUnsupportedRelease) {
		a.Personal.PaceZoneStatus = "model_version_unsupported"
		return a, nil
	}
	if relErr != nil {
		return nil, relErr
	}
	loc, err := time.LoadLocation(rel.tz)
	if err != nil {
		return nil, err
	}
	from := end.In(loc).AddDate(0, 0, -90)
	var inputs []stackInput
	var workouts []stackWorkout
	var resting *rc.Input
	if pre != nil {
		inputs, resting = pre.inputs, pre.resting
		for _, w := range pre.workouts {
			if w.end >= from.UnixMicro() && w.end <= end.UnixMicro() {
				workouts = append(workouts, w)
			}
		}
	} else {
		if inputs, err = stackInputs(ctx, tx, dataset, captured); err != nil {
			return fail(err)
		}
		l := Lease{DatasetID: dataset, TargetGeneration: generation}
		// Query once, then filter exact end timestamps separately for each reference.
		if workouts, err = stackWorkouts(ctx, tx, l, rel.stack, from, end, captured, 0); err != nil {
			return fail(err)
		}
		if resting, err = stackRestingHRFacts(ctx, tx, dataset, end.In(loc).AddDate(0, 0, -120), end, captured); err != nil {
			return fail(err)
		}
	}
	for _, post := range []bool{false, true} {
		cutoff := start
		if post {
			cutoff = end
		}
		selected := []stackWorkout{}
		for _, w := range workouts {
			if w.end > cutoff.UnixMicro() || !post && (w.id == id || w.end == cutoff.UnixMicro()) {
				continue
			}
			selected = append(selected, w)
		}
		_, resolutions, source := stackAnnotations(inputs, cutoff)
		suppressed, ambiguous := stackOverlap(selected, resolutions, cutoff)
		pending := []stackPendingLoad{}
		for _, w := range selected {
			if suppressed[w.id] {
				continue
			}
			r := stackWorkoutLoad(w, inputs, captured, resting, loc, rel.stack, 0, ambiguous[w.id])
			pending = append(pending, stackPendingLoad{w, r})
		}
		state := stackRunFitness(dataset, 0, cutoff.In(loc), cutoff, pending, stackEfforts(inputs, captured), source, ambiguous)
		profile := &workoutdetail.PaceZoneProfile{Date: state.LatestEvidenceDay, Zones: state.Zones}
		result := workoutdetail.BuildSupportedPaceZones(*raw, profile)
		result.PolicyVersion = "pace-zones/3"
		if len(result.Bands) == 0 {
			continue
		}
		sourceName := "pre_run"
		if post {
			sourceName = "post_run_estimate"
		}
		a.Reference = &workoutdetail.PaceReference{View: "revised", Freshness: state.Freshness, ManifestSHA256: rel.manifest, PolicyHash: rf.ConfigHash(), Source: sourceName, EvidenceCutoffAtUs: cutoff.UnixMicro(), LatestEvidenceAtUs: state.EvidenceAt, IncludesTargetWorkout: post, InputHash: state.InputHash, ProfileID: rf.Hash(state), StackID: rel.stack, Purpose: "workout_review"}
		workoutdetail.AttachPaceZones(a.Personal, result)
		if result.Status == "available" {
			a.DefaultView = "personal"
		}
		return a, nil
	}
	a.Personal.PaceZoneStatus = "personal_evidence_insufficient"
	return a, nil
}
