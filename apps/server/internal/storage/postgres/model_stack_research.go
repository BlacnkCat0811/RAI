package postgres

import (
	diag "rai/server/internal/diagnostics"
	rd "rai/server/internal/readiness"
	tl "rai/server/internal/trainingload"
)

// researchEnvelope is the persisted surface for one independently versioned
// research model. The model result stays optional: missing evidence is a typed
// unavailable artifact, not a failed stack release or a fabricated value.
type researchEnvelope struct {
	SchemaVersion string            `json:"schemaVersion"`
	DatasetID     string            `json:"datasetID"`
	Generation    int64             `json:"generation"`
	Identity      diag.Identity     `json:"identity"`
	Scope         string            `json:"scope"`
	AsOf          int64             `json:"asOfUs"`
	Status        diag.Status       `json:"status"`
	Reasons       []diag.ReasonCode `json:"reasonCodes"`
	Result        any               `json:"result,omitempty"`
}

func unavailableResearch(id diag.Identity, scope string, at int64, reason diag.ReasonCode) researchEnvelope {
	return researchEnvelope{Identity: id, Scope: scope, AsOf: at, Status: diag.StatusInsufficient, Reasons: []diag.ReasonCode{reason}}
}

func returnedResearch(id diag.Identity, scope string, at int64, status diag.Status, reasons []diag.ReasonCode, result any) researchEnvelope {
	if len(reasons) == 0 && status != diag.StatusOK {
		reasons = []diag.ReasonCode{diag.ReasonInsufficientHistory}
	}
	return researchEnvelope{Identity: id, Scope: scope, AsOf: at, Status: status, Reasons: reasons, Result: result}
}

func researchArtifact(key, dataset string, generation int64, envelope researchEnvelope) stackArtifact {
	envelope.SchemaVersion = envelope.Identity.Schema
	envelope.DatasetID = dataset
	envelope.Generation = generation
	return stackArtifact{
		key:       key,
		family:    "model_research_v1/" + key,
		schema:    envelope.Identity.Schema,
		algorithm: envelope.Identity.Model,
		version:   envelope.Identity.Version,
		value:     envelope,
		inputHash: featureHash(envelope),
	}
}

func modelResearchArtifacts(workouts []stackWorkout, state tl.StateResult, ready rd.Result, dataset string, generation int64, stack string, at int64) []stackArtifact {
	var out []stackArtifact
	scope := "model/" + stack + "/athlete/diagnostic/v1/"

	durability := unavailableResearch(diag.WorkoutDurabilityIdentity(), scope+"workout_durability", at, diag.ReasonInsufficientHistory)
	if result, ok := latestDurability(workouts); ok {
		durability = returnedResearch(diag.WorkoutDurabilityIdentity(), scope+"workout_durability", at, result.Status, result.Reasons, result)
	}
	out = append(out, researchArtifact("workout_durability", dataset, generation, durability))

	aerobic := unavailableResearch(diag.AerobicEfficiencyIdentity(), scope+"aerobic_efficiency", at, diag.ReasonInsufficientHistory)
	if result, ok := latestAerobic(workouts); ok {
		aerobic = returnedResearch(diag.AerobicEfficiencyIdentity(), scope+"aerobic_efficiency", at, result.Status, result.Reasons, result)
	}
	out = append(out, researchArtifact("aerobic_efficiency", dataset, generation, aerobic))

	critical := unavailableResearch(diag.CriticalSpeedIdentity(), scope+"critical_speed", at, diag.ReasonInsufficientHistory)
	if result, ok := criticalSpeed(workouts, at); ok {
		critical = returnedResearch(diag.CriticalSpeedIdentity(), scope+"critical_speed", at, result.Status, result.Reasons, result)
	}
	out = append(out, researchArtifact("critical_speed", dataset, generation, critical))

	settling := unavailableResearch(diag.LoadSettlingIdentity(), scope+"load_settling", at, diag.ReasonInsufficientHistory)
	if result, ok := loadSettling(state, ready); ok {
		settling = returnedResearch(diag.LoadSettlingIdentity(), scope+"load_settling", at, result.Status, result.Reasons, result)
	}
	out = append(out, researchArtifact("load_settling", dataset, generation, settling))
	return out
}

func latestDurability(workouts []stackWorkout) (diag.DurabilityResult, bool) {
	for i := len(workouts) - 1; i >= 0; i-- {
		w := workouts[i]
		f := w.feature
		first, second := f.LongRun.First, f.LongRun.Second
		if first == nil || second == nil {
			continue
		}
		firstSeconds := float64(first.End-first.Start) / 1e6
		secondSeconds := float64(second.End-second.Start) / 1e6
		if firstSeconds <= 0 || secondSeconds <= 0 {
			continue
		}
		samples := []diag.DurabilitySample{
			{StartSeconds: 0, EndSeconds: 600, SpeedMPS: first.Speed, HeartRateBPM: first.HR, SourceEpoch: first.SourceEpoch, KnownAt: w.known.UnixMicro()},
			{StartSeconds: 600, EndSeconds: 600 + firstSeconds, SpeedMPS: first.Speed, HeartRateBPM: first.HR, SourceEpoch: first.SourceEpoch, Terrain: string(f.Route.State), Environment: diag.Environment{Indoor: f.Environment.Indoor, TemperatureC: f.Environment.TemperatureC}, KnownAt: w.known.UnixMicro()},
			{StartSeconds: 600 + firstSeconds, EndSeconds: 600 + firstSeconds + secondSeconds, SpeedMPS: second.Speed, HeartRateBPM: second.HR, SourceEpoch: second.SourceEpoch, Terrain: string(f.Route.State), Environment: diag.Environment{Indoor: f.Environment.Indoor, TemperatureC: f.Environment.TemperatureC}, KnownAt: w.known.UnixMicro()},
		}
		result, err := diag.CalculateDurability(diag.DurabilityInput{WorkoutID: f.WorkoutID, Samples: samples})
		if err == nil {
			return result, true
		}
	}
	return diag.DurabilityResult{}, false
}

func latestAerobic(workouts []stackWorkout) (diag.AerobicEfficiencyResult, bool) {
	for i := len(workouts) - 1; i >= 0; i-- {
		w := workouts[i]
		if len(w.feature.Steady) == 0 {
			continue
		}
		supports := make([]diag.SteadySupport, 0, len(w.feature.Steady))
		for _, s := range w.feature.Steady {
			supports = append(supports, diag.SteadySupport{
				SpeedMPS:          &s.Window.MeanSpeed,
				HeartRateBPM:      s.HR,
				ValidSeconds:      float64(s.Window.Duration),
				HRSupportFraction: s.HRCoverage,
				SourceEpoch:       s.Window.SourceEpoch,
				Terrain:           "",
				KnownAt:           w.known.UnixMicro(),
			})
		}
		result, err := diag.CalculateAerobicEfficiency(diag.AerobicEfficiencyInput{WorkoutID: w.feature.WorkoutID, Supports: supports})
		if err == nil {
			return result, true
		}
	}
	return diag.AerobicEfficiencyResult{}, false
}

func criticalSpeed(workouts []stackWorkout, at int64) (diag.CriticalSpeedResult, bool) {
	var efforts []diag.CriticalSpeedEffort
	for _, w := range workouts {
		for _, candidate := range w.feature.Windows {
			best := candidate.Best
			if best == nil || best.Duration < 180 || best.Duration > 900 {
				continue
			}
			quality := 0.4
			if best.Quality == "high" {
				quality = 1
			} else if best.Quality == "moderate" {
				quality = 0.7
			}
			efforts = append(efforts, diag.CriticalSpeedEffort{WorkoutID: w.feature.WorkoutID, DurationSeconds: float64(best.Duration), DistanceMeters: best.Distance, MeanSpeedMPS: best.MeanSpeed, Quality: quality, SourceEpoch: best.SourceEpoch, KnownAt: w.known.UnixMicro()})
		}
	}
	if len(efforts) == 0 {
		return diag.CriticalSpeedResult{}, false
	}
	result, err := diag.CalculateCriticalSpeed(diag.CriticalSpeedInput{AsOf: at, Workouts: efforts})
	return result, err == nil
}

func loadSettling(state tl.StateResult, ready rd.Result) (diag.SettlingResult, bool) {
	if len(state.Days) == 0 {
		return diag.SettlingResult{}, false
	}
	day := state.Days[len(state.Days)-1]
	load := 0.0
	if day.RecordedLoad != nil {
		load = *day.RecordedLoad
	}
	baseline := 0.0
	if day.Chronic.Value != nil {
		baseline = *day.Chronic.Value
	}
	score := 0.0
	if ready.Score != nil {
		score = *ready.Score
	}
	result, err := diag.SettlingWindow(load, baseline, score, day.Quality == "complete", ready.Confidence)
	return result, err == nil
}
