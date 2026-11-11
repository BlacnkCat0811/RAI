package performance

import (
	"math"
	"sort"
	"time"
)

const (
	VersionV2          = "v2"
	SchemaV2           = "rai.performance/2"
	EvidenceSchemaV2   = "rai.performance_evidence/2"
	CurveSchemaV2      = "rai.performance_curve/2"
	AbilitySchemaV2    = "rai.running_ability/2"
	EnduranceSchemaV2  = "rai.endurance/2"
	ArtifactKeyV2      = "performance_state_v2"
	EvidenceKeyV2      = "performance_evidence_v2"
	CurveKeyV2         = "performance_curve_v2"
	AbilityKeyV2       = "running_ability_v2"
	EnduranceKeyV2     = "endurance_v2"
	ModeDemonstratedV2 = "demonstrated"
	ModeMaximalV2      = "maximal_supported"
)

type EffortAnnotation struct {
	ID                    string `json:"id"`
	WorkoutID             string `json:"workoutID"`
	Mode                  string `json:"mode"`
	UserConfirmed         bool   `json:"userConfirmed"`
	IndependentlyVerified bool   `json:"independentlyVerified"`
	AnnotatedAt           int64  `json:"annotatedAtUs"`
	KnownAt               int64  `json:"knownAtUs"`
	Revoked               bool   `json:"revoked"`
	Context               string `json:"context,omitempty"`
}

type V2Input struct {
	Input
	Mode        string             `json:"mode"`
	Annotations []EffortAnnotation `json:"annotations"`
}

// v2Options: grade-adjusted evidence, the top 3 workouts per duration, and a
// median fit through them.
var v2Options = calcOptions{gap: true, topK: 3, quantile: .5, lower: .5}

func ConfigHashV2() string {
	return Hash(struct {
		Policy string
		V      string
	}{"window90;quantile.8;gamma.7-1;step.001;recency45;residual.06;outlier.10;confirmation.05;loo.05;cap.55;maximal.50;cap.7;stable-source-key;annotation-known-at;grade-adjusted-minetti.08;envelope-top3-median/v2.2", VersionV2})
}

func annotate(es []FeatureRef, annotations []EffortAnnotation, at int64, maximal bool) ([]FeatureRef, []string) {
	byID := map[string]EffortAnnotation{}
	for _, a := range annotations {
		if a.Revoked || a.KnownAt > at || a.AnnotatedAt > at || a.WorkoutID == "" {
			continue
		}
		if maximal && (a.Mode != ModeMaximalV2 || (!a.UserConfirmed && !a.IndependentlyVerified)) {
			continue
		}
		byID[a.WorkoutID] = a
	}
	out := []FeatureRef{}
	reasons := []string{}
	for _, f := range es {
		if _, ok := byID[f.Feature.WorkoutID]; ok {
			out = append(out, f)
		}
	}
	if maximal {
		if len(out) < 3 {
			reasons = append(reasons, "maximal_annotations_insufficient")
		}
		minD, maxD := 0, 0
		for _, f := range out {
			d := 0
			for _, w := range f.Feature.Windows {
				if w.Best != nil && w.Best.Duration > d {
					d = w.Best.Duration
				}
			}
			if d == 0 {
				d = int((f.Feature.Timing.End - f.Feature.Timing.Start) / 1e6)
			}
			if minD == 0 || d < minD {
				minD = d
			}
			if d > maxD {
				maxD = d
			}
		}
		if minD == 0 || maxD < 2400 || maxD < 2*minD {
			reasons = append(reasons, "maximal_duration_coverage_insufficient")
		}
	}
	return out, reasons
}

func CalculateV2(in V2Input) (Result, error) {
	if in.Mode == "" {
		in.Mode = ModeDemonstratedV2
	}
	if in.Mode != ModeDemonstratedV2 && in.Mode != ModeMaximalV2 {
		return Result{}, invalid()
	}
	if in.Mode == ModeMaximalV2 {
		return calculateMaximal(in)
	}
	r, err := calculate(in.Input, v2Options)
	if err != nil {
		return Result{}, err
	}
	r.Schema, r.Version, r.ConfigHash, r.EvidenceMode = SchemaV2, VersionV2, ConfigHashV2(), in.Mode
	return r, nil
}

func calculateMaximal(in V2Input) (Result, error) {
	r := Result{Schema: SchemaV2, DatasetID: in.DatasetID, Generation: in.Generation, Model: AbilityModel, Version: VersionV2,
		ConfigHash: ConfigHashV2(), AsOf: in.AsOf, View: in.View, EvidenceMode: ModeMaximalV2, MaximalSupported: "unavailable",
		Status: "collecting", Freshness: "unknown", Stability: "not_evaluated", Targets: []Target{}, Evidence: []WeightedEvidence{}, ReasonCodes: []string{}}
	filtered := in.Input
	filtered.Features, _ = annotate(in.Features, in.Annotations, in.AsOf, true)
	evidence, err := ExtractGradeAdjusted(filtered)
	if err != nil {
		return Result{}, err
	}
	if len(evidence) == 0 {
		r.ReasonCodes = append(r.ReasonCodes, "no_qualified_maximal_effort")
		return r, nil
	}
	var latest int64
	for _, e := range evidence {
		latest = max(latest, e.WorkoutEnd)
	}
	anchor := date(time.UnixMicro(latest), in.Calendar)
	r.AnchorDay = anchor.Format(time.DateOnly)
	r.Evidence, r.Support = pool(evidence, anchor, in.AsOf, in.View)
	if r.Support.IndependentWorkouts < 3 {
		r.ReasonCodes = append(r.ReasonCodes, "maximal_annotations_insufficient")
		return r, nil
	}
	longest := map[string]int{}
	for i, e := range r.Evidence {
		if e.Weight > 0 {
			longest[e.WorkoutID] = max(longest[e.WorkoutID], e.Duration)
			r.Evidence[i].EffortMode = ModeMaximalV2
		}
	}
	short, long := r.Support.LongestDuration, 0
	for _, duration := range longest {
		short, long = min(short, duration), max(long, duration)
	}
	if long < 2400 || long < 2*short {
		r.ReasonCodes = append(r.ReasonCodes, "maximal_duration_coverage_insufficient")
		return r, nil
	}
	r.MaximalSupported = "supported"
	r.Curve, r.Status = fitCurveQuantile(r.Evidence, true, .5, .5)
	if r.Status != "available" {
		r.ReasonCodes = append(r.ReasonCodes, r.Status)
		return r, nil
	}
	a := ability(math.Log(r.Curve.D30), r.Curve.Gamma)
	r.Ability = &a
	r.Stability = "stable"
	r.Confidence = min(.7, r.Support.QualitySupport)
	sort.Strings(r.ReasonCodes)
	return Freshen(r, in.AsOf), nil
}

// CapabilityScore is the original 0-100 scale. It is kept separate from the
// maximal evidence mode so a confirmed maximal effort cannot silently upgrade
// the demonstrated anchor.
func CapabilityScore(v60 float64) (float64, error) {
	if !finite(v60) || v60 <= 0 {
		return 0, invalid()
	}
	raw, clipped, _ := Score(v60)
	_ = raw
	return clipped, nil
}
