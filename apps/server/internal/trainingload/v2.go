package trainingload

import (
	"errors"
	wf "rai/server/internal/workoutfeatures"
)

const (
	AlgorithmV2      = "rai.training_load.hrr_integral"
	StateAlgorithmV2 = "rai.load_state.exp_finite"
	VersionV2        = "v2"
	SchemaV2         = "rai.training_load/2"
	StateSchemaV2    = "rai.load_state/2"
	ScopeNameV2      = "recorded_running_load_v2"
)

var ErrV2Invalid = errors.New("training_load_v2_invalid_input")

type Reference struct {
	ID           string  `json:"id"`
	Hash         string  `json:"hash"`
	RestHR       float64 `json:"restingHR"`
	MaxHR        float64 `json:"maxHR"`
	MeasuredAt   int64   `json:"measuredAtUs"`
	RecordedAt   int64   `json:"recordedAtUs"`
	Provenance   string  `json:"provenance"`
	SourceEpoch  string  `json:"sourceEpoch"`
	Baseline     bool    `json:"baselineRHR"`
	BaselineRun  string  `json:"baselineRunID,omitempty"`
	BaselineHash string  `json:"baselineHash,omitempty"`
}

type V2Input struct {
	WorkoutInput
	Reference         *Reference `json:"reference"`
	ReferenceConflict bool       `json:"referenceConflict"`
	// ReferenceInvalid isolates a reference row that failed validation to this
	// Workout: its load is unknown with an explicit reason instead of failing
	// the whole day release.
	ReferenceInvalid bool `json:"referenceInvalid,omitempty"`
	OverlapResolved  bool `json:"overlapResolved"`
	OverlapUnknown   bool `json:"overlapUnknown"`
}

// ConfigHashV2 identifies the bounded product policy: quality tiers, HRR
// clamped to [0,1] instead of zero tolerance, and imputation of a bounded
// unknown-day weight in the load state.
func ConfigHashV2() string {
	return Hash(struct {
		Load, State, Exposure                                   string
		CompleteCoverage, CompleteMaxGap                        float64
		EstimatedCoverage, EstimatedMaxGap                      float64
		Summary, Ratio                                          float64
		AcuteUnknownMass, ChronicUnknownMass                    float64
		Horizon                                                 int
		ReferencePolicy, Overlap, BoundPolicy, ImputationPolicy string
	}{AlgorithmV2 + "/" + VersionV2, StateAlgorithmV2 + "/" + VersionV2, wf.ExposureSchema, CompleteCoverage, CompleteMaxGap, EstimatedCoverage, EstimatedMaxGap, .35, 1, AcuteUnknownMassMax, ChronicUnknownMassMax, Horizon, "hr_reference/2;latest-effective;hrmax-no-expiry-review365d;rhr90d;baseline28d;revised-known-at/v2.2", "positive-intersection-knownness/v1", "hrr-clamp-0-1;tiers;no-pace-approximation/v4", "ring-mean-known-training-day/v1"})
}

func ScopeV2(workout string) string { return ScopeV2For(workout, wf.StackID()) }

func ScopeV2For(workout, stack string) string {
	return "model/" + stack + "/workout_load/" + workout + "/v2"
}

func ScopeStateV2() string { return ScopeStateV2For(wf.StackID()) }

func ScopeStateV2For(stack string) string {
	return "model/" + stack + "/athlete/load/v2"
}

// CalculateTrainingLoadV2 keeps the v1 dose kernel and writes a distinct v2
// identity. It accepts only the resolved hr_reference/2 projection and makes
// unresolved overlap explicit instead of dropping either workout.
func CalculateTrainingLoadV2(in V2Input, generation int64) (Result, error) {
	base := in.WorkoutInput
	if in.Deleted {
		r, err := calculateLoad(base, generation, boundedPolicy)
		if err != nil {
			return Result{}, err
		}
		r.Schema, r.Model, r.ModelVersion, r.ConfigHash, r.Scope = SchemaV2, AlgorithmV2, VersionV2, ConfigHashV2(), ScopeV2(r.WorkoutID)
		return r, nil
	}
	if in.ReferenceConflict {
		r, err := calculateLoad(base, generation, boundedPolicy)
		if err != nil {
			return Result{}, err
		}
		r.Schema, r.Model, r.ModelVersion, r.ConfigHash, r.Scope = SchemaV2, AlgorithmV2, VersionV2, ConfigHashV2(), ScopeV2(r.WorkoutID)
		r.Status, r.Quality, r.Load, r.ObservedLoad, r.EstimatedLoad = "source_conflict", "insufficient", nil, nil, nil
		r.ReasonCodes = append(r.ReasonCodes, "reference_conflict")
		return r, nil
	}
	if in.ReferenceInvalid {
		base.Exposure.Parameter = nil
		r, err := calculateLoad(base, generation, boundedPolicy)
		if err != nil {
			return Result{}, err
		}
		r.Schema, r.Model, r.ModelVersion, r.ConfigHash, r.Scope = SchemaV2, AlgorithmV2, VersionV2, ConfigHashV2(), ScopeV2(r.WorkoutID)
		r.ReasonCodes = append(r.ReasonCodes, "reference_invalid")
		return r, nil
	}
	if in.Reference == nil || in.Reference.ID == "" || in.Reference.Hash == "" || !finite(in.Reference.RestHR) || !finite(in.Reference.MaxHR) || in.Reference.RestHR <= 0 || in.Reference.MaxHR <= in.Reference.RestHR || in.Reference.MeasuredAt <= 0 || in.Reference.RecordedAt <= 0 || in.Reference.Provenance == "" {
		if in.Reference == nil {
			base.Exposure.Parameter = nil
			r, err := calculateLoad(base, generation, boundedPolicy)
			if err != nil {
				return Result{}, err
			}
			r.Schema, r.Model, r.ModelVersion, r.ConfigHash, r.Scope = SchemaV2, AlgorithmV2, VersionV2, ConfigHashV2(), ScopeV2(r.WorkoutID)
			r.ReasonCodes = append(r.ReasonCodes, "reference_missing")
			return r, nil
		}
		return Result{}, ErrV2Invalid
	}
	base.Exposure.Parameter = &wf.Parameter{ID: in.Reference.ID, Hash: in.Reference.Hash, EffectiveFrom: in.Reference.MeasuredAt, RecordedAt: in.Reference.RecordedAt, RestHR: in.Reference.RestHR, MaxHR: in.Reference.MaxHR, Provenance: in.Reference.Provenance}
	r, err := calculateLoad(base, generation, boundedPolicy)
	if err != nil {
		return Result{}, err
	}
	r.Schema, r.Model, r.ModelVersion, r.ConfigHash, r.Scope = SchemaV2, AlgorithmV2, VersionV2, ConfigHashV2(), ScopeV2(r.WorkoutID)
	r.Evidence["referenceID"] = in.Reference.ID
	r.Evidence["referenceHash"] = in.Reference.Hash
	r.Evidence["referencePolicy"] = "hr_reference/2"
	if in.Reference.Baseline {
		r.Evidence["rhrBaselineRun"] = in.Reference.BaselineRun
		r.Evidence["rhrBaselineHash"] = in.Reference.BaselineHash
	}
	if in.OverlapUnknown {
		if r.Status == "available" {
			r.Status = "partial"
			r.Quality = "partial"
			r.Load = nil
			r.ObservedLoad = nil
			r.EstimatedLoad = nil
			r.ScaleFactor = nil
		}
		r.ReasonCodes = append(r.ReasonCodes, "overlap_unresolved")
		r.Evidence["overlapKnownness"] = "unknown"
	} else {
		r.Evidence["overlapKnownness"] = "resolved"
	}
	return r, nil
}

func CalculateLoadStateV2(in StateInput) (StateResult, error) {
	r, err := calculateLoadState(in, true)
	if err != nil {
		return StateResult{}, err
	}
	r.Schema, r.Model, r.Version, r.ConfigHash, r.Scope = StateSchemaV2, StateAlgorithmV2, VersionV2, ConfigHashV2(), ScopeStateV2()
	return r, nil
}
