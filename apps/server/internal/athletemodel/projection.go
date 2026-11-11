package athletemodel

import (
	pf "rai/server/internal/performance"
	rd "rai/server/internal/readiness"
	rc "rai/server/internal/recovery"
	tl "rai/server/internal/trainingload"
	ve "rai/server/internal/vo2maxexternal"
)

func number(v float64) *float64 { return &v }

// All functions below receive already validated, Freshen-ed results. They never
// query facts or recalculate any physiological model.
func ProjectAbility(r pf.Result, anchor, updated, at int64) Metric[RunningAbilityValue] {
	v := &RunningAbilityValue{EvidenceMode: r.EvidenceMode, Stability: r.Stability, EvidenceCount: r.Support.IndependentWorkouts, SpeedUnit: "m/s", ScoreUnit: "score_0_100"}
	if r.Ability != nil {
		v.Score = number(r.Ability.Score)
		v.V60 = number(r.Ability.V60)
		v.Gamma = number(r.Ability.Gamma)
		v.EnduranceRetention = number(r.Ability.Retention)
	}
	e := NewEnvelope("runningAbility", r.Model+"/"+r.Version, r.Schema, r.Status, "not_provided", r.Freshness, &r.Confidence, &anchor, &updated, at, r.ReasonCodes, []string{"RAI-derived", "Apple Health"})
	m := Metric[RunningAbilityValue]{Envelope: e, Value: v, Trend: NoTrend("no_comparable_result")}
	m.Explanation = BuildMetricExplanation(m.Envelope, []Fact{{"running_ability.v60", v.V60, "m/s"}, {"running_ability.evidence_count", number(float64(v.EvidenceCount)), "count"}}, []string{"recent_demonstrated_running_performance", "not_vo2max", "evidence_support_not_probability"})
	return m
}
func ProjectVO2(r ve.Result, updated, at int64) Metric[ExternalVO2Value] {
	fresh := "unknown"
	if r.Status == "available" {
		fresh = "fresh"
	}
	if r.Status == "stale" {
		fresh = "stale"
	}
	if r.Status == "expired" {
		fresh = "expired"
	}
	v := &ExternalVO2Value{Value: r.Value, Unit: r.Unit, Provider: r.Provider, Origin: r.Origin, ExternalEstimate: true}
	if r.Selected != nil {
		v.SampleDate = &r.Selected.End
	}
	m := Metric[ExternalVO2Value]{Envelope: NewEnvelope("externalVO2Max", r.ModelVersion, r.Schema, r.Status, r.Quality, fresh, nil, &r.AsOf, &updated, at, r.Reasons, []string{"Apple Health estimate"}), Value: v, Trend: NoTrend("source_value_history_only")}
	m.Explanation = BuildMetricExplanation(m.Envelope, []Fact{{"external_vo2.apple_health_estimate", r.Value, r.Unit}}, []string{"external_estimate", "not_rai_measured", "not_laboratory_ground_truth"})
	return m
}
func component(f rc.Freshness, d *rc.Deviation, adverseDirection string) ComponentState {
	s := ComponentState{Availability: f.State, RelativeToBaseline: "unknown"}
	if d != nil {
		// M5B already encodes adverse direction in Burden. Positive SDNN/sleep
		// deviation means lower than baseline; positive RHR means higher.
		// Reuse that result instead of inventing a product-level z threshold.
		s.RelativeToBaseline = "no_adverse_deviation"
		if d.Burden > 0 {
			s.RelativeToBaseline = adverseDirection
		}
	}
	return s
}
func ProjectRecovery(r rc.Result, updated, at int64) Metric[RecoveryValue] {
	fresh := "unknown"
	if r.SDNNFreshness.State == "fresh" && r.RHRFreshness.State == "fresh" && r.SleepFreshness.State == "fresh" {
		fresh = "fresh"
	}
	if r.Status == "stale" {
		fresh = "stale"
	}
	v := &RecoveryValue{Score: r.Score, Unit: "score_0_100", BaselineState: r.BaselineState, DeviationLevel: r.DeviationLevel, HRV: component(r.SDNNFreshness, r.SDNNDeviation, "below_baseline"), RHR: component(r.RHRFreshness, r.RHRDeviation, "above_baseline"), Sleep: component(r.SleepFreshness, r.SleepDeviation, "below_baseline")}
	m := Metric[RecoveryValue]{Envelope: NewEnvelope("physiologicalRecovery", r.ModelVersion, r.Schema, r.Status, r.Quality, fresh, &r.Confidence, &r.AsOf, &updated, at, r.Reasons, []string{"RAI-derived", "Apple Health"}), Value: v, Trend: NoTrend("no_comparable_valid_daily_results")}
	facts := []Fact{{"recovery.score", r.Score, "score_0_100"}}
	for _, c := range []struct {
		name  string
		state ComponentState
	}{{"hrv", v.HRV}, {"rhr", v.RHR}, {"sleep", v.Sleep}} {
		facts = append(facts, Fact{Code: "recovery." + c.name + "." + c.state.RelativeToBaseline, Unit: ""})
	}
	m.Explanation = BuildMetricExplanation(m.Envelope, facts, []string{"hrv_sdnn_resting_hr_sleep_personal_baseline_proxy", "not_diagnosis", "evidence_support_not_probability"})
	return m
}
func ProjectReadiness(r rd.Result, updated, at int64) Metric[ReadinessValue] {
	fresh := "unknown"
	if r.Status == "available" {
		fresh = "fresh"
	}
	if r.Status == "stale" {
		fresh = "stale"
	}
	v := &ReadinessValue{Score: r.Score, RecoveryScore: r.RecoveryScore, LoadMultiplier: r.Penalty, RecentRecordedLoad: r.Burden, Unit: "score_0_100", Scope: "running_focused"}
	m := Metric[ReadinessValue]{Envelope: NewEnvelope("trainingReadiness", r.ModelVersion, r.Schema, r.Status, r.Quality, fresh, &r.Confidence, &r.AsOf, &updated, at, r.Reasons, []string{"RAI-derived", "Apple Health"}), Value: v, Trend: NoTrend("instant_state_no_long_term_trend")}
	m.Explanation = BuildMetricExplanation(m.Envelope, []Fact{{"readiness.recovery_score", v.RecoveryScore, "score_0_100"}, {"readiness.recent_recorded_running_load", v.RecentRecordedLoad, "RAI_AU"}, {"readiness.load_multiplier", v.LoadMultiplier, "ratio"}}, []string{"running_focused", "informational_not_training_instruction", "evidence_support_not_probability"})
	return m
}
func ProjectLoad(workout *tl.Result, state *tl.DailyState, workoutAt *int64, anchor, updated, at int64) Metric[TrainingLoadValue] {
	v := &TrainingLoadValue{Knownness: "unknown", Unit: tl.Unit, StateUnit: tl.Unit + "/day", StateModelVersion: tl.StateAlgorithm + "/v1", LatestWorkoutAsOf: workoutAt}
	status, quality := "insufficient_data", "insufficient"
	reasons := []string{}
	if workout != nil {
		v.LatestWorkoutLoad = workout.Load
		reasons = append(reasons, workout.ReasonCodes...)
		status = workout.Status
		quality = workout.Quality
	}
	fresh := "unknown"
	if state != nil {
		v.Acute = state.Acute.Value
		v.Chronic = state.Chronic.Value
		v.Balance = state.Balance
		v.Trend7 = state.Trend7
		quality = state.Quality
		fresh = "fresh"
		status = "available"
		if state.UnknownWorkouts > 0 || v.Acute == nil || v.Chronic == nil {
			status = "partial"
			reasons = append(reasons, "load_unknown")
		} else {
			v.Knownness = "known_recorded_running"
			if state.Quality != "complete" {
				status = "partial"
				reasons = append(reasons, "input_partial")
			}
		}
	}
	if state == nil {
		reasons = append(reasons, "load_unknown")
		if v.LatestWorkoutLoad != nil {
			status = "partial"
		}
	}
	m := Metric[TrainingLoadValue]{Envelope: NewEnvelope("trainingLoad", tl.Algorithm+"/v1", tl.Schema, status, quality, fresh, nil, &anchor, &updated, at, reasons, []string{"RAI-derived", "Apple Health"}), Value: v, Trend: NoTrend("m2_trend_unavailable")}
	if state != nil {
		m.Trend = Trend{Kind: "m2_trend7", Value: state.Trend7, Unit: "RAI_AU/day²", ReasonCodes: []string{}}
	}
	m.Explanation = BuildMetricExplanation(m.Envelope, []Fact{{"load.latest_recorded_workout", v.LatestWorkoutLoad, tl.Unit}, {"load.recent_recorded_running", v.Acute, tl.Unit + "/day"}, {"load.long_term_recorded_running", v.Chronic, tl.Unit + "/day"}}, []string{"recorded_running_load", "not_total_body_load", "not_overtraining_diagnosis"})
	return m
}

// Localization keys and typed facts only. Natural language belongs to clients.
func BuildMetricExplanation(e Envelope, facts []Fact, limitations []string) Explanation {
	return Explanation{Headline: e.Metric + "." + string(e.State), SupportingFacts: facts, Limitations: CanonicalReasons(limitations)}
}

// V1 does not load history in the current reader. These pure comparability
// contracts can be used by a future bounded history endpoint without UI math.
func RunningAbilityTrend(a, b pf.Result) *float64 {
	if a.Model != b.Model || a.Version != b.Version || a.EvidenceMode != b.EvidenceMode || a.View != b.View || a.ConfigHash != b.ConfigHash || a.Status != "available" || b.Status != "available" || a.Ability == nil || b.Ability == nil || a.Freshness != "fresh" || b.Freshness != "fresh" {
		return nil
	}
	return number(a.Ability.Score - b.Ability.Score)
}
func RecoveryDailyTrend(a, b rc.Result) *float64 {
	if a.ModelVersion != b.ModelVersion || a.ConfigHash != b.ConfigHash || a.Timezone != b.Timezone || a.Date <= b.Date || a.Status != "available" || b.Status != "available" || a.Quality != "complete" || b.Quality != "complete" || a.Score == nil || b.Score == nil {
		return nil
	}
	return number(*a.Score - *b.Score)
}

// ProjectLoadV2 projects a v2 load state with v2 identity. The v1 ProjectLoad
// function stays frozen for legacy callers and its output is never relabeled.
func ProjectLoadV2(state *tl.DailyState, anchor, updated, at int64) Metric[TrainingLoadValue] {
	m := ProjectLoad(nil, state, nil, anchor, updated, at)
	if state != nil && state.Imputed {
		// Acute/chronic include unknown days valued at the typical training day.
		m.Envelope.ReasonCodes = CanonicalReasons(append(m.Envelope.ReasonCodes, "load_imputed"))
	}
	m.Envelope.ModelVersion = tl.AlgorithmV2 + "/" + tl.VersionV2
	m.Envelope.PayloadVersion = tl.SchemaV2
	if m.Value != nil {
		m.Value.StateModelVersion = tl.StateAlgorithmV2 + "/" + tl.VersionV2
	}
	return m
}
