package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"rai/server/internal/analysis"
	am "rai/server/internal/athletemodel"
	rf "rai/server/internal/runfitness"
	"rai/server/internal/syncing"
	"rai/server/internal/workoutdetail"
)

func loadPaceAnalysis(ctx context.Context, tx pgx.Tx, p syncing.Principal, id string, d *workoutDetailData) error {
	a := workoutdetail.NewPaceAnalysis()
	d.PaceAnalysis = a
	fail := func(err error) error {
		var f *analysis.Failure
		if !errors.As(err, &f) {
			return err
		}
		if a.Absolute.PaceZonePolicyVersion == "" {
			a.Absolute.PaceZoneStatus = "input_unavailable"
		}
		a.Personal.PaceZoneStatus = "input_unavailable"
		a.Personal.ExplanationCodes = append(a.Personal.ExplanationCodes, f.Code)
		return nil
	}
	captured := time.Now().UTC()
	var generation int64
	if err := tx.QueryRow(ctx, `SELECT generation FROM dirty_scopes WHERE dataset_id=$1 AND scope_key='workout:HKWorkoutTypeIdentifier:'||$2`, p.DatasetID, id).Scan(&generation); err != nil {
		return err
	}
	raw, err := stackRaw(ctx, tx, Lease{DatasetID: p.DatasetID, TargetGeneration: generation}, "", id, captured, 0)
	if err != nil {
		return fail(err)
	}
	a.Absolute = workoutdetail.BuildAbsolutePace(*raw)
	if a.Absolute.PaceZoneStatus == "available" {
		a.DefaultView = "absolute"
	}
	act, err := activeFitnessRelease(ctx, tx, p.DatasetID)
	if errors.Is(err, am.ErrNotActivated) || errors.Is(err, am.ErrUnsupportedRelease) {
		a.Personal.PaceZoneStatus = "model_version_unsupported"
		return nil
	}
	if err != nil {
		return err
	}
	loc, err := time.LoadLocation(act.tz)
	if err != nil {
		return err
	}
	inputs, err := stackInputs(ctx, tx, p.DatasetID, captured)
	if err != nil {
		return fail(err)
	}
	l := Lease{DatasetID: p.DatasetID, TargetGeneration: generation}
	// Query once, then filter exact end timestamps separately for each reference.
	workouts, err := stackWorkouts(ctx, tx, l, act.stack, d.End.In(loc).AddDate(0, 0, -90), d.End, captured, 0)
	if err != nil {
		return fail(err)
	}
	resting, err := stackRestingHRFacts(ctx, tx, p.DatasetID, d.End.In(loc).AddDate(0, 0, -120), d.End, captured)
	if err != nil {
		return fail(err)
	}
	for _, post := range []bool{false, true} {
		cutoff := d.Start
		if post {
			cutoff = d.End
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
			r := stackWorkoutLoad(w, inputs, captured, resting, loc, act.stack, 0, ambiguous[w.id])
			pending = append(pending, stackPendingLoad{w, r})
		}
		state := stackRunFitness(p.DatasetID, 0, cutoff.In(loc), cutoff, pending, stackEfforts(inputs, captured), source, ambiguous)
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
		a.Reference = &workoutdetail.PaceReference{View: "revised", Freshness: state.Freshness, ManifestSHA256: act.manifest, PolicyHash: rf.ConfigHash(), Source: sourceName, EvidenceCutoffAtUs: cutoff.UnixMicro(), LatestEvidenceAtUs: state.EvidenceAt, IncludesTargetWorkout: post, InputHash: state.InputHash, ProfileID: rf.Hash(state), StackID: act.stack, Purpose: "workout_review"}
		workoutdetail.AttachPaceZones(a.Personal, result)
		if result.Status == "available" {
			a.DefaultView = "personal"
		}
		return nil
	}
	a.Personal.PaceZoneStatus = "personal_evidence_insufficient"
	return nil
}
