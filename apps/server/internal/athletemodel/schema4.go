package athletemodel

import (
	rf "rai/server/internal/runfitness"
)

// Schema4 replaces the retired running ability with running fitness v1.
const Schema4 = "rai.athlete_model/4"
const MappingVersion4 = "rai.product_metric_mapping/4"

// RunningFitnessValue is the product view of one published running-fitness
// day state. Values are physical anchors first, then RAI-defined ratings; it
// carries no probability and no brand score.
type RunningFitnessValue struct {
	PublicationID       string           `json:"publicationID"`
	AlgorithmVersion    string           `json:"algorithmVersion"`
	RatingPolicy        string           `json:"ratingPolicy"`
	HistoryView         string           `json:"historyView"`
	Date                string           `json:"date"`
	Status              string           `json:"status"`
	Freshness           string           `json:"freshness"`
	LatestEvidenceDay   string           `json:"latestEvidenceDay,omitempty"`
	ValidUntil          string           `json:"validUntil,omitempty"`
	Overall             rf.Overall       `json:"overall"`
	ReferenceDailyScore *float64         `json:"referenceDailyScore"`
	Params              rf.Params        `json:"params"`
	Dimensions          rf.Dimensions    `json:"dimensions"`
	Predictions         []rf.Prediction  `json:"predictions"`
	Zones               rf.Zones         `json:"zones"`
	RacePaces           []rf.RacePace    `json:"racePaces"`
	Preparation         rf.Preparation   `json:"preparation"`
	Support             rf.Support       `json:"support"`
	Stability           rf.Stability     `json:"stability"`
	Observations        []rf.SingleBrief `json:"singleRuns"`
	ReasonCodes         []string         `json:"reasonCodes"`
}

func NewSnapshotV4(at, revision int64, tz, date string, a Assignment) AthleteModelSnapshot {
	s := NewSnapshotV3(at, revision, tz, date, a)
	s.SchemaVersion = Schema4
	fitness := Missing[RunningFitnessValue]("runningFitness", at, "pending")
	fitness.Envelope.MappingVersion = MappingVersion4
	s.RunningFitness = &fitness
	retired := Missing[RunningAbilityValue]("runningAbility", at, "model_update_required")
	s.RunningAbility = retired
	s.PresentationOrder = []string{"trainingReadiness", "physiologicalRecovery", "sessionRecovery", "trainingLoad", "loadFocus", "runningFitness", "raiVO2Max", "externalVO2Max"}
	return s
}

// setResponseStateV4 counts the eight schema4 product metrics. The retired
// runningAbility slot is never part of a schema4 response.
func setResponseStateV4(s *AthleteModelSnapshot) {
	envelopes := []*Envelope{&s.TrainingLoad.Envelope, &s.ExternalVO2Max.Envelope, &s.PhysiologicalRecovery.Envelope, &s.TrainingReadiness.Envelope}
	if s.RAIVO2Max != nil {
		envelopes = append(envelopes, &s.RAIVO2Max.Envelope)
	}
	for _, m := range []*Envelope{envelopeOf(s.SessionRecovery), envelopeOf(s.LoadFocus), envelopeOf(s.RunningFitness)} {
		if m != nil {
			envelopes = append(envelopes, m)
		}
	}
	available := 0
	for _, e := range envelopes {
		if e.State == Available {
			available++
		}
	}
	switch {
	case available == 0:
		s.ResponseState = "unavailable_response"
	case available < 8:
		s.ResponseState = "partial_response"
	default:
		s.ResponseState = "complete_response"
	}
}

func envelopeOf[T any](m *Metric[T]) *Envelope {
	if m == nil {
		return nil
	}
	return &m.Envelope
}

func fitnessEnvelopeStatus(s rf.State) (status, freshness string) {
	switch s.Status {
	case "established":
		status = "available"
	case "initial", "preliminary", "race_only":
		status = "limited"
	case "conflict":
		status = "source_conflict"
	default:
		status = "collecting"
	}
	switch s.Freshness {
	case "stale":
		freshness = "stale"
	case "expired":
		freshness = "expired"
		status = "insufficient_data"
	default:
		freshness = "fresh"
	}
	return
}

// ProjectRunningFitness maps a validated, Freshen-ed day state.
func ProjectRunningFitness(s rf.State, publication string, updated, at int64) Metric[RunningFitnessValue] {
	status, freshness := fitnessEnvelopeStatus(s)
	m := Metric[RunningFitnessValue]{Envelope: NewEnvelopeV2("runningFitness", rf.Algorithm+"/"+rf.Version, rf.Schema, status, s.Overall.Basis, freshness, nil, &s.AsOf, &updated, at, s.Reasons, []string{"recorded_running"}),
		Trend: NoTrend("history_endpoint"), Explanation: Explanation{Headline: "runningFitness.state", SupportingFacts: []Fact{}, Limitations: []string{"model_range_not_probability", "rai_scale_not_brand_score", "unvalidated_design_parameters"}}}
	stampV3(&m.Envelope, s.Status, s.EvidenceAt, 0)
	if status == "limited" {
		// A limited fitness value is shown; "metric_unavailable" would contradict it.
		codes := []ProductReasonCode{}
		for _, c := range m.Envelope.ProductReasonCodes {
			if c != "metric_unavailable" {
				codes = append(codes, c)
			}
		}
		m.Envelope.ProductReasonCodes = codes
		m.Envelope.PrimaryReason = PrimaryReason(codes)
	}
	m.Envelope.MappingVersion = MappingVersion4
	m.Value = &RunningFitnessValue{PublicationID: publication, AlgorithmVersion: rf.Version, RatingPolicy: s.RatingPolicy, HistoryView: "revised", Date: s.Date, Status: s.Status, Freshness: s.Freshness,
		LatestEvidenceDay: s.LatestEvidenceDay, ValidUntil: s.ValidUntil, Overall: s.Overall, ReferenceDailyScore: s.ReferenceDailyScore, Params: s.Params, Dimensions: s.Dimensions,
		Predictions: s.Predictions, Zones: s.Zones, RacePaces: s.RacePaces, Preparation: s.Preparation, Support: s.Support, Stability: s.Stability, Observations: s.Observations, ReasonCodes: s.Reasons}
	return m
}

// Downgrade serves an older App the schema it asked for from the current
// schema4 snapshot, keeping every metric whose meaning is unchanged so the old
// App still shows numbers. Slots the old contract cannot express truthfully
// become "model_update_required": the one case where updating the App helps.
//   - schema3: drops runningFitness (new in schema4); everything else as is.
//   - schema2: also drops sessionRecovery and loadFocus, and keeps recovery
//     and readiness only when computed from complete inputs, because schema2
//     has no notion of an estimated score.
func Downgrade(s AthleteModelSnapshot, schema string) AthleteModelSnapshot {
	if s.SchemaVersion != Schema4 || (schema != Schema3 && schema != Schema2) {
		return s
	}
	s.SchemaVersion = Schema3
	s.RunningFitness = nil
	s.PresentationOrder = []string{"trainingReadiness", "physiologicalRecovery", "sessionRecovery", "trainingLoad", "loadFocus", "runningAbility", "raiVO2Max", "externalVO2Max"}
	if schema == Schema2 {
		s.SchemaVersion = Schema2
		s.SessionRecovery, s.LoadFocus = nil, nil
		s.PresentationOrder = []string{"trainingReadiness", "physiologicalRecovery", "trainingLoad", "runningAbility", "raiVO2Max", "externalVO2Max"}
		at := s.EvaluationAt
		if !completeMode(s.PhysiologicalRecovery.Envelope) {
			s.PhysiologicalRecovery = Missing[RecoveryValue]("physiologicalRecovery", at, "model_update_required")
			s.PhysiologicalRecovery.Envelope.MappingVersion = MappingVersion2
		}
		if !completeMode(s.TrainingReadiness.Envelope) {
			s.TrainingReadiness = Missing[ReadinessValue]("trainingReadiness", at, "model_update_required")
			s.TrainingReadiness.Envelope.MappingVersion = MappingVersion2
		}
	}
	SetProjectedResponseState(&s)
	return s
}

// completeMode is true for a metric with a value computed from complete
// inputs, and for one without a value (its missing reason is still valid).
func completeMode(e Envelope) bool {
	return e.State != Available && e.State != Limited || e.Mode == "" || e.Mode == "complete"
}
