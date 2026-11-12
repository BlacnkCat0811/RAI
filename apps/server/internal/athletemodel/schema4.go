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
	m.Envelope.MappingVersion = MappingVersion4
	m.Value = &RunningFitnessValue{PublicationID: publication, AlgorithmVersion: rf.Version, RatingPolicy: s.RatingPolicy, HistoryView: "revised", Date: s.Date, Status: s.Status, Freshness: s.Freshness,
		LatestEvidenceDay: s.LatestEvidenceDay, ValidUntil: s.ValidUntil, Overall: s.Overall, ReferenceDailyScore: s.ReferenceDailyScore, Params: s.Params, Dimensions: s.Dimensions,
		Predictions: s.Predictions, Zones: s.Zones, RacePaces: s.RacePaces, Preparation: s.Preparation, Support: s.Support, Stability: s.Stability, Observations: s.Observations, ReasonCodes: s.Reasons}
	return m
}
