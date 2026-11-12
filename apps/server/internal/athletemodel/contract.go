// Package athletemodel projects existing model results. It contains no model
// fitting, baseline calculation, database access or ambient clock.
package athletemodel

import (
	rd "rai/server/internal/readiness"
	rc "rai/server/internal/recovery"
	tl "rai/server/internal/trainingload"
	"slices"
	"sort"
)

const Schema = "rai.athlete_model/1"
const MappingVersion = "rai.product_metric_mapping/1"

type ProductMetricState string

const (
	Available   ProductMetricState = "available"
	Limited     ProductMetricState = "limited"
	Collecting  ProductMetricState = "collecting"
	Unavailable ProductMetricState = "unavailable"
	Stale       ProductMetricState = "stale"
)

// Unknown statuses fail closed; original status and reasons always survive.
func MapProductState(status, freshness string, supported bool) ProductMetricState {
	if !supported || status == "unsupported" {
		return Limited
	}
	// Still collecting comes before stale: a metric that never had enough
	// evidence is not "expired", and the next qualifying data fixes both.
	switch status {
	case "collecting", "collecting_baseline", "insufficient_long_evidence":
		return Collecting
	}
	if freshness == "expired" || freshness == "stale" || status == "stale" || status == "expired" {
		return Stale
	}
	switch status {
	case "available", "complete":
		return Available
	case "partial", "limited", "unstable":
		return Limited
	default:
		return Unavailable
	}
}

// reasonPriority orders product reasons by what the user should resolve first:
// an inactive/broken model, then a source decision, then missing sync, then
// missing history, then specific inputs, and staleness last among causes.
// productReasonCodes stays in its frozen lexicographic order; primaryReason is
// the one to show.
var reasonPriority = []ProductReasonCode{"not_product_activated", "unsupported", "technical_failure", "model_update_required",
	"model_result_pending", "deleted", "source_selection_required", "source_conflict", "data_sync_incomplete", "not_enough_history",
	"missing_heart_rate", "training_load_unknown", "recovery_data_incomplete", "data_stale", "insufficient_evidence", "no_sample", "metric_unavailable"}

// PrimaryReason picks the highest-priority product reason, or "" when none.
func PrimaryReason(codes []ProductReasonCode) ProductReasonCode {
	for _, p := range reasonPriority {
		if slices.Contains(codes, p) {
			return p
		}
	}
	if len(codes) > 0 {
		return codes[0]
	}
	return ""
}

type ProductReasonCode string

func CanonicalReasons(xs []string) []string {
	xs = append([]string{}, xs...)
	sort.Strings(xs)
	out := []string{}
	for _, x := range xs {
		if x != "" && (len(out) == 0 || out[len(out)-1] != x) {
			out = append(out, x)
		}
	}
	return out
}

// Lexicographic code order is the frozen V1 priority, independent of DB order.
func MapReasons(status string, reasons []string) []ProductReasonCode {
	out := []string{}
	for _, s := range append(append([]string{}, reasons...), status) {
		switch s {
		case "collecting", "collecting_baseline", "insufficient_long_evidence", "baseline_insufficient", "load_baseline_insufficient":
			out = append(out, "not_enough_history")
		case "insufficient_data":
			out = append(out, "insufficient_evidence")
		case "pending":
			out = append(out, "model_result_pending")
		case "model_update_required":
			out = append(out, "model_update_required")
		case "sync_incomplete", "load_sync_incomplete", "daily_coverage_incomplete", "candidate_superseded":
			out = append(out, "data_sync_incomplete")
		case "source_conflict", "ability_source_disagreement", "hrv_source_conflict", "rhr_source_conflict", "sleep_source_conflict", "parameter_conflict", "load_parameter_conflict", "recovery_source_conflict":
			out = append(out, "source_conflict")
		case "source_selection_required":
			out = append(out, "source_selection_required")
		case "stale", "expired", "sample_expired", "evidence_expired", "input_stale", "recovery_stale":
			out = append(out, "data_stale")
		case "missing_heart_rate", "hr_missing", "missing_hr", "hr_coverage_low":
			out = append(out, "missing_heart_rate")
		case "night_data_missing", "night_sparse", "recent_trend_used", "recent_observation_mean_used", "health_default_baseline_used", "typed_resting_hr_used", "health_baseline_bootstrap", "regularity_collecting", "estimate_expired", "recovery_missing", "recovery_partial", "hrv_missing", "rhr_missing", "sleep_missing", "autonomic_only_full_score_unavailable":
			out = append(out, "recovery_data_incomplete")
		case "load_unknown", "load_incompatible", "recent_load_unknown":
			out = append(out, "training_load_unknown")
		case "unsupported_input":
			out = append(out, "unsupported")
		case "workout_deleted":
			out = append(out, "deleted")
		case "unsupported", "not_product_activated", "technical_failure", "no_sample", "deleted":
			out = append(out, s)
		}
	}
	if len(out) == 0 && status != "available" && status != "complete" {
		out = append(out, "metric_unavailable")
	}
	codes := []ProductReasonCode{}
	for _, s := range CanonicalReasons(out) {
		codes = append(codes, ProductReasonCode(s))
	}
	return codes
}

type Envelope struct {
	Mode               string              `json:"mode,omitempty"`
	ValidBefore        *int64              `json:"validBefore,omitempty"`
	ID                 string              `json:"id"`
	Metric             string              `json:"metric"`
	ModelVersion       string              `json:"modelVersion"`
	PayloadVersion     string              `json:"payloadVersion"`
	MappingVersion     string              `json:"mappingVersion"`
	State              ProductMetricState  `json:"state"`
	ModelStatus        string              `json:"modelStatus"`
	Quality            string              `json:"quality"`
	Confidence         *float64            `json:"confidence"`
	EvidenceSupport    string              `json:"evidenceSupport"`
	AsOf               *int64              `json:"asOf"`
	EvidenceAt         *int64              `json:"evidenceAt,omitempty"`
	EvaluationAt       int64               `json:"evaluationAt"`
	ComputedAt         *int64              `json:"computedAt,omitempty"`
	PublishedAt        *int64              `json:"publishedAt,omitempty"`
	ValidationStatus   string              `json:"validationStatus,omitempty"`
	Freshness          string              `json:"freshness"`
	UpdatedAt          *int64              `json:"updatedAt"`
	ReasonCodes        []string            `json:"reasonCodes"`
	ProductReasonCodes []ProductReasonCode `json:"productReasonCodes"`
	PrimaryReason      ProductReasonCode   `json:"primaryReason,omitempty"`
	LineageSummary     LineageSummary      `json:"lineageSummary"`
	TechnicalError     *string             `json:"technicalError"`
}
type LineageSummary struct {
	DataSources []string `json:"dataSources"`
}
type Fact struct {
	Code  string   `json:"code"`
	Value *float64 `json:"value"`
	Unit  string   `json:"unit"`
}
type Explanation struct {
	Headline        string   `json:"headline"`
	SupportingFacts []Fact   `json:"supportingFacts"`
	Limitations     []string `json:"limitations"`
}
type Trend struct {
	Kind        string   `json:"kind"`
	Value       *float64 `json:"value"`
	Unit        string   `json:"unit"`
	ReasonCodes []string `json:"reasonCodes"`
}
type Metric[T any] struct {
	Envelope    Envelope    `json:"envelope"`
	Value       *T          `json:"value"`
	Trend       Trend       `json:"trend"`
	Explanation Explanation `json:"explanation"`
}

type TrainingLoadValue struct {
	LatestWorkoutLoad *float64 `json:"latestWorkoutLoad"`
	LatestWorkoutAsOf *int64   `json:"latestWorkoutAsOf"`
	Acute             *float64 `json:"acute"`
	Chronic           *float64 `json:"chronic"`
	Balance           *float64 `json:"balance"`
	Trend7            *float64 `json:"trend7"`
	Knownness         string   `json:"knownness"`
	Unit              string   `json:"unit"`
	StateUnit         string   `json:"stateUnit"`
	StateModelVersion string   `json:"stateModelVersion"`
}

// RunningAbilityValue is the retired running-ability slot. Running fitness v1
// replaced the v60 model; the slot stays decodable for older clients and is
// always returned without a value ("model_update_required").
type RunningAbilityValue struct {
	Score     *float64 `json:"score"`
	SpeedUnit string   `json:"speedUnit"`
	ScoreUnit string   `json:"scoreUnit"`
}
type ExternalVO2Value struct {
	Value            *float64 `json:"value"`
	Unit             string   `json:"unit"`
	Provider         string   `json:"provider"`
	Origin           *string  `json:"origin"`
	SampleDate       *int64   `json:"sampleDate"`
	ExternalEstimate bool     `json:"externalEstimate"`
}
type ComponentState struct {
	Availability       string `json:"availability"`
	RelativeToBaseline string `json:"relativeToBaseline"`
}
type RecoveryValue struct {
	Components           map[string]rc.V3Component `json:"components,omitempty"`
	MissingComponents    []string                  `json:"missingComponents,omitempty"`
	BaselineNights       int                       `json:"baselineNights,omitempty"`
	TrendNights          int                       `json:"trendNights,omitempty"`
	SleepRelativeToHabit *bool                     `json:"sleepRelativeToHabit,omitempty"`
	Score                *float64                  `json:"score"`
	Unit                 string                    `json:"unit"`
	BaselineState        string                    `json:"baselineState"`
	HRV                  ComponentState            `json:"hrv"`
	RHR                  ComponentState            `json:"rhr"`
	Sleep                ComponentState            `json:"sleep"`
	// DeviationLevel: normal, mild_deviation or marked_deviation; omitted when
	// the model published no score.
	DeviationLevel string `json:"deviationLevel,omitempty"`
}
type ReadinessValue struct {
	Range              *rd.ScoreRange `json:"range,omitempty"`
	PhysiologicalCap   *float64       `json:"physiologicalCap,omitempty"`
	SessionCap         *float64       `json:"sessionCap,omitempty"`
	WeeklyCap          *float64       `json:"weeklyCap,omitempty"`
	Score              *float64       `json:"score"`
	RecoveryScore      *float64       `json:"recoveryScore"`
	LoadMultiplier     *float64       `json:"loadMultiplier"`
	RecentRecordedLoad *float64       `json:"recentRecordedLoad"`
	Unit               string         `json:"unit"`
	Scope              string         `json:"scope"`
}
type SessionRecoveryValue struct {
	RemainingHours *float64       `json:"remainingHours"`
	Range          *rd.ScoreRange `json:"range"`
	ReadyAfter     *int64         `json:"readyAfter"`
	Overflow       bool           `json:"overflow"`
	Scope          string         `json:"scope"`
}
type LoadFocusValue struct {
	Windows []tl.FocusWindow `json:"windows"`
}
type AthleteModelSnapshot struct {
	RunningFitness        *Metric[RunningFitnessValue]  `json:"runningFitness,omitempty"`
	SessionRecovery       *Metric[SessionRecoveryValue] `json:"sessionRecovery,omitempty"`
	LoadFocus             *Metric[LoadFocusValue]       `json:"loadFocus,omitempty"`
	SchemaVersion         string                        `json:"schemaVersion"`
	AsOf                  int64                         `json:"asOf"`
	EvaluationAt          int64                         `json:"evaluationAt"`
	DatasetRevision       int64                         `json:"datasetRevision"`
	Timezone              string                        `json:"timezone"`
	CalendarDate          string                        `json:"calendarDate"`
	Assignment            Assignment                    `json:"assignment"`
	ResponseState         string                        `json:"responseState"`
	PresentationOrder     []string                      `json:"presentationOrder"`
	Notices               []string                      `json:"notices"`
	TrainingLoad          Metric[TrainingLoadValue]     `json:"trainingLoad"`
	RunningAbility        Metric[RunningAbilityValue]   `json:"runningAbility"`
	ExternalVO2Max        Metric[ExternalVO2Value]      `json:"externalVO2Max"`
	PhysiologicalRecovery Metric[RecoveryValue]         `json:"physiologicalRecovery"`
	TrainingReadiness     Metric[ReadinessValue]        `json:"trainingReadiness"`
	RAIVO2Max             *Metric[RAIVO2Value]          `json:"raiVO2Max,omitempty"`
}

// A distinct transport type prevents confusing an administrative preview with
// an activated product response. There is no automatic conversion at HTTP.
type AthleteModelCandidateSnapshot struct {
	CandidateOnly bool                 `json:"candidateOnly"`
	Snapshot      AthleteModelSnapshot `json:"snapshot"`
}
type Assignment struct {
	Channel string `json:"channel"`
	StackID string `json:"stackID"`
	Version int64  `json:"version"`
}

func NewSnapshot(at, revision int64, tz, date string, a Assignment) AthleteModelSnapshot {
	s := AthleteModelSnapshot{SchemaVersion: Schema, AsOf: at, EvaluationAt: at, DatasetRevision: revision, Timezone: tz, CalendarDate: date, Assignment: a, ResponseState: "complete_response", PresentationOrder: []string{"trainingReadiness", "physiologicalRecovery", "trainingLoad", "runningAbility", "externalVO2Max"}, Notices: []string{"informational_fitness_metrics", "not_medical_diagnosis"}}
	s.TrainingLoad = Missing[TrainingLoadValue]("trainingLoad", at, "collecting")
	s.RunningAbility = Missing[RunningAbilityValue]("runningAbility", at, "collecting")
	s.ExternalVO2Max = Missing[ExternalVO2Value]("externalVO2Max", at, "sync_incomplete")
	s.PhysiologicalRecovery = Missing[RecoveryValue]("physiologicalRecovery", at, "collecting_baseline")
	s.TrainingReadiness = Missing[ReadinessValue]("trainingReadiness", at, "insufficient_data")
	if a.Channel == "active" {
		s.TrainingLoad = Missing[TrainingLoadValue]("trainingLoad", at, "not_product_activated")
		s.RunningAbility = Missing[RunningAbilityValue]("runningAbility", at, "not_product_activated")
		s.ExternalVO2Max = Missing[ExternalVO2Value]("externalVO2Max", at, "not_product_activated")
		s.PhysiologicalRecovery = Missing[RecoveryValue]("physiologicalRecovery", at, "not_product_activated")
		s.TrainingReadiness = Missing[ReadinessValue]("trainingReadiness", at, "not_product_activated")
	}
	return s
}
func Missing[T any](metric string, at int64, status string) Metric[T] {
	e := NewEnvelope(metric, "", "", status, "insufficient", "unknown", nil, nil, nil, at, []string{status}, nil)
	if status == "technical_failure" {
		code := "artifact_invalid"
		e.TechnicalError = &code
	}
	return Metric[T]{Envelope: e, Trend: NoTrend("no_comparable_result"), Explanation: Explanation{Headline: metric + ".state", SupportingFacts: []Fact{}, Limitations: []string{status}}}
}
func NoTrend(reason string) Trend {
	return Trend{Kind: "none", Unit: "", ReasonCodes: []string{reason}}
}
func NewEnvelope(metric, model, payload, status, quality, freshness string, confidence *float64, asOf, updated *int64, at int64, reasons, sources []string) Envelope {
	e := Envelope{ID: metric, Metric: metric, ModelVersion: model, PayloadVersion: payload, MappingVersion: MappingVersion, ModelStatus: status, Quality: quality, Freshness: freshness, Confidence: confidence, AsOf: asOf, UpdatedAt: updated, EvaluationAt: at, ReasonCodes: CanonicalReasons(reasons), ProductReasonCodes: MapReasons(status, reasons), LineageSummary: LineageSummary{CanonicalReasons(sources)}, EvidenceSupport: "not_presented"}
	e.State = MapProductState(status, freshness, true)
	e.PrimaryReason = PrimaryReason(e.ProductReasonCodes)
	if confidence != nil {
		e.EvidenceSupport = "limited_evidence"
		if *confidence >= .2 {
			e.EvidenceSupport = "moderate_evidence"
		}
		if *confidence >= .4 {
			e.EvidenceSupport = "stronger_evidence"
		}
	}
	return e
}

// StampPublication binds a projected metric envelope to the exact published
// run/artifact observed by the product reader. It never changes the value or
// freshness; callers set validation to "validated" only after schema, model,
// generation and artifact checks succeed.
func StampPublication[T any](m *Metric[T], publishedAt, computedAt int64, validation string) {
	if publishedAt > 0 {
		m.Envelope.PublishedAt = &publishedAt
	}
	if computedAt > 0 {
		m.Envelope.ComputedAt = &computedAt
	}
	if validation != "" {
		m.Envelope.ValidationStatus = validation
	}
}
