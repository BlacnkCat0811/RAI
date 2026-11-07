package athletemodel

import (
	"errors"

	"rai/server/internal/vo2max"
)

// Schema2 is the product contract that exposes the independently-versioned
// RAI models. Schema1 remains decodable and is never relabelled.
const Schema2 = "rai.athlete_model/2"
const MappingVersion2 = "rai.product_metric_mapping/2"

type RAIVO2Value struct {
	Value            *float64 `json:"value"`
	Unit             string   `json:"unit"`
	Experimental     bool     `json:"experimental"`
	EvidenceMode     string   `json:"evidenceMode"`
	Stability        string   `json:"stability"`
	EvidenceCount    int      `json:"independentWorkoutCount"`
	SensitivityLow   *float64 `json:"sensitivityLow"`
	SensitivityHigh  *float64 `json:"sensitivityHigh"`
	SensitivityWidth *float64 `json:"sensitivityRelativeWidth"`
	Source           string   `json:"source"`
}

type MetricV2[T any] struct {
	Envelope    Envelope    `json:"envelope"`
	Value       *T          `json:"value"`
	Trend       Trend       `json:"trend"`
	Explanation Explanation `json:"explanation"`
}

func NewSnapshotV2(at, revision int64, tz, date string, a Assignment) AthleteModelSnapshot {
	s := NewSnapshot(at, revision, tz, date, a)
	s.SchemaVersion = Schema2
	for _, e := range []*Envelope{&s.TrainingLoad.Envelope, &s.RunningAbility.Envelope, &s.ExternalVO2Max.Envelope, &s.PhysiologicalRecovery.Envelope, &s.TrainingReadiness.Envelope} {
		e.MappingVersion = MappingVersion2
	}
	s.PresentationOrder = []string{"trainingReadiness", "physiologicalRecovery", "trainingLoad", "runningAbility", "raiVO2Max", "externalVO2Max"}
	v := Missing[RAIVO2Value]("raiVO2Max", at, "collecting")
	v.Envelope.MappingVersion = MappingVersion2
	s.RAIVO2Max = &v
	return s
}

func NewEnvelopeV2(metric, model, payload, status, quality, freshness string, confidence *float64, asOf, updated *int64, at int64, reasons, sources []string) Envelope {
	e := NewEnvelope(metric, model, payload, status, quality, freshness, confidence, asOf, updated, at, reasons, sources)
	e.MappingVersion = MappingVersion2
	e.EvidenceAt = asOf
	e.ComputedAt = &at
	e.ValidationStatus = "not_available"
	return e
}

func ProjectRAIVO2(r vo2max.Result, updated, at int64) Metric[RAIVO2Value] {
	if r.Schema != vo2max.Schema || r.Model != vo2max.Model || r.ModelVersion != vo2max.Version || r.DatasetID == "" || r.Generation < 1 || !r.Experimental || r.Unit != vo2max.Unit {
		m := Missing[RAIVO2Value]("raiVO2Max", at, "technical_failure")
		m.Envelope.MappingVersion = MappingVersion2
		return m
	}
	r = vo2max.Freshen(r, at)
	status := r.Status
	mapped := status
	reasons := append([]string{}, r.Reasons...)
	reasons = append(reasons, r.Sensitivity.Reasons...)
	reasons = append(reasons, r.LOO.Reasons...)
	present := r.Value != nil && !r.CandidateOnly &&
		r.Sensitivity.Range != nil && r.Sensitivity.Width != nil && *r.Sensitivity.Width <= .30
	switch status {
	case "established":
		mapped = "available"
	case "preliminary":
		mapped = "limited"
	}
	if (mapped == "available" || mapped == "limited") && !present {
		mapped = "limited"
	}
	conf := r.Confidence
	e := NewEnvelopeV2("raiVO2Max", vo2max.Model+"/"+vo2max.Version, r.Schema, mapped, r.Quality, r.Freshness, &conf, &r.AsOf, &updated, at, reasons, []string{"recorded_running"})
	e.EvidenceAt = r.EvidenceAt
	e.ModelStatus = status
	e.EvidenceSupport = "experimental_candidate"
	e.ValidationStatus = "experimental"
	m := Metric[RAIVO2Value]{Envelope: e, Trend: NoTrend("insufficient_serial_history")}
	if present && (status == "established" || status == "preliminary") {
		v := RAIVO2Value{Value: r.Value, Unit: r.Unit, Experimental: true, EvidenceMode: "submax", Stability: r.Stability, EvidenceCount: r.Workouts, Source: "rai_owned_submax"}
		v.SensitivityLow = &r.Sensitivity.Range.Low
		v.SensitivityHigh = &r.Sensitivity.Range.High
		v.SensitivityWidth = r.Sensitivity.Width
		m.Value = &v
	}
	m.Explanation = Explanation{Headline: "raiVO2Max.state", SupportingFacts: []Fact{}, Limitations: append([]string{"experimental_submax_estimate"}, reasons...)}
	return m
}

var ErrNotActivated = errors.New("athlete_model_not_activated")

// SetProjectedResponseState records whether any model slot could be projected.
// A syntactically complete schema2 document is not a complete response when
// every published artifact is absent or stale.
func SetProjectedResponseState(s *AthleteModelSnapshot) {
	available := 0
	for _, e := range []Envelope{s.TrainingLoad.Envelope, s.RunningAbility.Envelope, s.ExternalVO2Max.Envelope, s.PhysiologicalRecovery.Envelope, s.TrainingReadiness.Envelope} {
		if e.State == Available {
			available++
		}
	}
	if s.RAIVO2Max != nil && s.RAIVO2Max.Envelope.State == Available {
		available++
	}
	switch {
	case available == 0:
		s.ResponseState = "unavailable_response"
	case available < 6:
		s.ResponseState = "partial_response"
	default:
		s.ResponseState = "complete_response"
	}
}
