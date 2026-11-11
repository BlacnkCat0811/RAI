package athletemodel

import (
	rd "rai/server/internal/readiness"
	rc "rai/server/internal/recovery"
	ra "rai/server/internal/runningability"
	sr "rai/server/internal/sessionrecovery"
	tl "rai/server/internal/trainingload"
)

const Schema3 = "rai.athlete_model/3"
const MappingVersion3 = "rai.product_metric_mapping/3"

func NewSnapshotV3(at, revision int64, tz, date string, a Assignment) AthleteModelSnapshot {
	s := NewSnapshotV2(at, revision, tz, date, a)
	s.SchemaVersion = Schema3
	session := Missing[SessionRecoveryValue]("sessionRecovery", at, "pending")
	s.SessionRecovery = &session
	focus := Missing[LoadFocusValue]("loadFocus", at, "pending")
	s.LoadFocus = &focus
	s.PresentationOrder = []string{"trainingReadiness", "physiologicalRecovery", "sessionRecovery", "trainingLoad", "loadFocus", "runningAbility", "raiVO2Max", "externalVO2Max"}
	return s
}
func stampV3(e *Envelope, mode string, evidence *int64, valid int64) {
	e.MappingVersion = MappingVersion3
	e.Mode = mode
	e.EvidenceAt = evidence
	if valid > 0 {
		e.ValidBefore = &valid
	}
}
func ProjectRecoveryV3(r rc.V3Result, updated, at int64) Metric[RecoveryValue] {
	m := ProjectRecovery(r.Result, updated, at)
	stampV3(&m.Envelope, r.Mode, r.EvidenceAt, r.ValidBefore)
	m.Envelope.Freshness = "fresh"
	if r.Status == "expired" {
		m.Envelope.Freshness = "expired"
	}
	m.Value.Components = r.Components
	m.Value.MissingComponents = r.MissingComponents
	m.Value.BaselineNights = r.BaselineNights
	m.Value.TrendNights = r.TrendNights
	m.Value.SleepRelativeToHabit = &r.SleepTarget.RelativeToHabit
	for name, x := range r.Components {
		c := ComponentState{Availability: x.State, RelativeToBaseline: "unknown"}
		switch name {
		case "hrv":
			m.Value.HRV = c
		case "rhr":
			m.Value.RHR = c
		case "sleep":
			m.Value.Sleep = c
		}
	}
	return m
}
func ProjectReadinessV4(r rd.V4Result, updated, at int64) Metric[ReadinessValue] {
	m := ProjectReadiness(r.Result, updated, at)
	stampV3(&m.Envelope, r.Mode, r.EvidenceAt, r.ValidBefore)
	m.Value.Range = r.Range
	m.Value.PhysiologicalCap = r.PhysiologicalCap
	m.Value.SessionCap = r.SessionCap
	m.Value.WeeklyCap = r.WeeklyCap
	return m
}
func ProjectSessionRecovery(r sr.Result, updated, at int64) Metric[SessionRecoveryValue] {
	m := Metric[SessionRecoveryValue]{Envelope: NewEnvelopeV2("sessionRecovery", sr.Algorithm+"/v1", sr.Schema, r.Status, r.Mode, "fresh", &r.Confidence, &r.AsOf, &updated, at, r.Reasons, []string{"recorded_running"}), Trend: NoTrend("instant_state_no_long_term_trend"), Explanation: Explanation{Headline: "sessionRecovery.state", SupportingFacts: []Fact{}, Limitations: []string{"recorded_running_next_hard_session", "estimated_not_full_body_recovery"}}}
	stampV3(&m.Envelope, r.Mode, &r.AsOf, r.ValidBefore)
	v := SessionRecoveryValue{RemainingHours: r.RemainingHours, ReadyAfter: r.ReadyAfter, Overflow: r.Overflow, Scope: r.Scope}
	if r.Range != nil {
		v.Range = &rd.ScoreRange{Low: r.Range.Low, High: r.Range.High}
	}
	m.Value = &v
	return m
}
func ProjectAbilityEstimate(r ra.Result, updated, at int64) Metric[RunningAbilityValue] {
	b := r.Demonstrated
	switch r.SelectedMode {
	case "maximal_supported":
		b = r.Maximal
	case "submax_estimated":
		b = r.Submax
	}
	status := b.Status
	if r.SelectedMode == "unavailable" {
		status = "collecting"
	}
	if r.SelectedMode == "source_conflict" {
		status = "source_conflict"
		b.V60 = nil
		b.Score = nil
		b.Gamma = nil
	}
	if r.SelectedMode == "submax_estimated" && b.V60 != nil {
		status = "limited"
	}
	m := Metric[RunningAbilityValue]{Envelope: NewEnvelopeV2("runningAbility", ra.Algorithm+"/v1", ra.Schema, status, "limited", "fresh", &b.Confidence, &r.AsOf, &updated, at, r.Reasons, []string{"recorded_running"}), Trend: NoTrend("source_specific_only"), Explanation: Explanation{Headline: "runningAbility.state", SupportingFacts: []Fact{}, Limitations: []string{"independent_evidence_branches", "submax_no_endurance_curve"}}}
	stampV3(&m.Envelope, r.SelectedMode, &b.EvidenceAt, 0)
	m.Value = &RunningAbilityValue{SubmaxPrior: r.Prior, Score: b.Score, V60: b.V60, Gamma: b.Gamma, SpeedUnit: "m/s", ScoreUnit: "score_0_100", EvidenceMode: r.SelectedMode, SelectedMode: r.SelectedMode, EvidenceCount: r.Progress.Workouts, Stability: "not_provided", Demonstrated: &r.Demonstrated, Maximal: &r.Maximal, Submax: &r.Submax, Progress: &r.Progress}
	return m
}
func ProjectFocus(r tl.FocusResult, updated, at int64) Metric[LoadFocusValue] {
	m := Metric[LoadFocusValue]{Envelope: NewEnvelopeV2("loadFocus", tl.FocusAlgorithm+"/v1", tl.FocusSchema, "available", "partial", "fresh", nil, &r.AsOf, &updated, at, []string{"known_load_denominator"}, []string{"recorded_running"}), Value: &LoadFocusValue{Windows: r.Windows}, Trend: NoTrend("descriptive_distribution"), Explanation: Explanation{Headline: "loadFocus.state", SupportingFacts: []Fact{}, Limitations: []string{"hrr_proxy", "anaerobic_proxy", "no_automatic_target"}}}
	stampV3(&m.Envelope, "observed", &r.AsOf, 0)
	return m
}
