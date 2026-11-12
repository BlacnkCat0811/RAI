package workoutfeatures

import (
	"context"
	"encoding/json"
	"math"
)

func environment(s RawWorkoutSnapshot) Environment {
	e := Environment{State: Missing}
	var m map[string]json.RawMessage
	_ = json.Unmarshal(s.Metadata, &m)
	if b, ok := m["HKIndoorWorkout"]; ok {
		var v bool
		if json.Unmarshal(b, &v) == nil {
			e.Indoor = &v
		}
	}
	read := func(key, unit string) *float64 {
		b, ok := m[key]
		if !ok {
			return nil
		}
		var q struct {
			Value float64 `json:"value"`
			Unit  string  `json:"unit"`
		}
		if json.Unmarshal(b, &q) == nil && q.Unit == unit && finite(q.Value) {
			return ptr(q.Value)
		}
		return nil
	}
	e.TemperatureC = read("HKWeatherTemperature", "degC")
	e.Humidity = read("HKWeatherHumidity", "1")
	if e.Indoor != nil || e.TemperatureC != nil || e.Humidity != nil {
		e.State = Present
	}
	if s.Availability["environment"] == Ambiguous {
		e = Environment{State: Ambiguous}
	}
	return e
}
func chooseDistance(s RawWorkoutSnapshot, t AnalysisTimeline) DistanceChoice {
	d := DistanceChoice{Preferred: s.Distance, SelectedSource: "workout_total", Reason: "source_total_preferred", RouteDistance: t.Route.Summary.Distance}
	if len(t.Speed.Spans) > 0 {
		sum := 0.0
		for _, x := range t.Speed.Spans {
			sum += x.integral(x.Start, x.End)
		}
		d.SpeedIntegral = ptr(sum)
	}
	if d.Preferred == nil {
		if t.Speed.Summary.ReconstructedCoverage >= 0.98 {
			d.Preferred = d.SpeedIntegral
			d.SelectedSource = "running_speed_integral"
			d.Reason = "total_missing_supported_speed"
		} else if t.Route.Summary.Coverage >= 0.98 {
			d.Preferred = d.RouteDistance
			d.SelectedSource = "route_distance"
			d.Reason = "total_and_speed_missing_supported_route"
		} else {
			d.SelectedSource = ""
			d.Reason = "distance_missing"
		}
	}
	if d.SelectedSource != "running_speed_integral" && d.SpeedIntegral != nil {
		d.CrossCheck = d.SpeedIntegral
		d.CrossCheckSource = "running_speed_integral"
	} else if d.SelectedSource != "route_distance" && d.RouteDistance != nil {
		d.CrossCheck = d.RouteDistance
		d.CrossCheckSource = "route_distance"
	}
	if d.Preferred != nil && *d.Preferred > 0 && d.CrossCheck != nil {
		d.DifferenceRatio = ptr(math.Abs(*d.CrossCheck-*d.Preferred) / *d.Preferred)
	}
	if d.Preferred != nil && *d.Preferred > 0 && d.RouteDistance != nil && t.Route.Summary.Coverage >= .98 {
		d.RouteDifferenceRatio = ptr(math.Abs(*d.RouteDistance-*d.Preferred) / *d.Preferred)
	}
	return d
}
func Compute(ctx context.Context, in RawWorkoutSnapshot) (WorkoutFeatures, error) {
	s, err := Normalize(in)
	if err != nil {
		return WorkoutFeatures{}, err
	}
	return ComputeNormalized(ctx, s)
}

// ComputeNormalized is for loaders/benchmarks that already called Normalize.
func ComputeNormalized(ctx context.Context, s RawWorkoutSnapshot) (WorkoutFeatures, error) {
	if err := ctx.Err(); err != nil {
		return WorkoutFeatures{}, err
	}
	f := WorkoutFeatures{ActivityType: s.ActivityType, Schema: Schema, Algorithm: Algorithm, Version: Version, StackID: StackID(), DatasetID: s.DatasetID, WorkoutID: s.WorkoutID, Generation: s.Generation, AsOf: s.AsOf, State: Present, Dynamics: map[string]SignalSummary{}, Windows: []WindowCandidates{}, Steady: []SteadySegment{}, Reasons: []Reason{}}
	refs := append([]FactRef{s.Fact}, s.References...)
	for _, q := range s.Quantities {
		refs = append(refs, q.Fact)
	}
	for _, x := range s.Series {
		refs = append(refs, x.Fact)
	}
	for _, x := range s.Routes {
		refs = append(refs, x.Fact)
	}
	refs = unique(refs)
	f.Evidence = Evidence{InputHash: s.Hash(), FactManifestHash: digest(refs), FactCount: len(refs), ConfigHash: s.Policy.Hash(), SourceEpochs: []string{}, ParameterIDs: []string{}, ParameterHashes: []string{}}
	for _, p := range s.Provenance {
		f.Evidence.SourceEpochs = append(f.Evidence.SourceEpochs, p.Epoch)
	}
	f.Evidence.SourceEpochs = unique(f.Evidence.SourceEpochs)
	for _, p := range s.Parameters {
		f.Evidence.ParameterIDs = append(f.Evidence.ParameterIDs, p.ID)
		f.Evidence.ParameterHashes = append(f.Evidence.ParameterHashes, p.Hash)
	}
	if s.Fact.State == Deleted || s.Fact.State == Missing {
		f.State = s.Fact.State
		f.Reasons = []Reason{WorkoutDeleted}
		q := qual(f.Reasons, nil)
		f.Qualification = Qualifications{Load: q, VO2: q, Performance: q, CS: q, Endurance: q}
		return f, nil
	}
	t := BuildTimeline(s)
	f.Structure = structureFeatures(t)
	f.Timing = t.Pause.Timing
	f.HeartRate = t.HeartRate.Summary
	f.Speed = t.Speed.Summary
	f.Route = t.Route.Summary
	f.Distance = chooseDistance(s, t)
	f.Environment = environment(s)
	f.RunFitness = runFitnessFeatures(s, t, f.Distance, f.Environment)
	effort, err := effortFeatures(s, t, f.Environment)
	if err != nil {
		return WorkoutFeatures{}, err
	}
	f.Effort = effort
	if f.Distance.RouteDifferenceRatio != nil && *f.Distance.RouteDifferenceRatio > .05 {
		f.Route.Reasons = reasonSet(f.Route.Reasons, []Reason{DistanceMismatch})
	}
	parameter, parameterReasons := resolveParameter(s)
	if len(t.Speed.Spans) == 0 {
		// Every cell is discontinuous without native speed support. Preserve the
		// eight empty candidates exactly, without allocating three prefix indexes.
		for _, d := range []int{180, 300, 600, 1200, 1800, 2400, 3600, 5400} {
			f.Windows = append(f.Windows, WindowCandidates{Duration: d})
		}
		f.Steady = []SteadySegment{}
	} else {
		p := prefixes(t)
		f.Windows = extractWindows(t, f.Distance, f.Environment, f.Evidence.InputHash, p)
		f.Steady = extractSteady(s, t, f.Environment, parameter, f.Evidence.InputHash, p)
		f.LongRun = extractLongRun(t, p)
	}
	if f.Distance.RouteDifferenceRatio != nil && *f.Distance.RouteDifferenceRatio > .05 {
		for i := range f.Steady {
			q := f.Steady[i].Qualification
			f.Steady[i].Qualification = qual(append(q.Reasons, DistanceMismatch), nil)
			f.Steady[i].Window.Qualification = f.Steady[i].Qualification
		}
	}
	for _, spec := range []struct{ kind, key string }{{Steps, "cadence_proxy"}, {Power, "power"}, {GCT, "ground_contact_time"}, {Stride, "stride_length"}, {Vertical, "vertical_oscillation"}} {
		if err := ctx.Err(); err != nil {
			return f, err
		}
		f.Dynamics[spec.key] = BuildSignal(s, t.Pause, spec.kind).Summary
	}
	f.Reasons = reasonSet(t.Pause.Reasons, f.HeartRate.Reasons, f.Speed.Reasons, f.Route.Reasons)
	load := append([]Reason{}, t.Pause.Reasons...)
	load = append(load, parameterReasons...)
	if f.HeartRate.Mean == nil {
		load = append(load, MissingHR)
	}
	if f.HeartRate.ReconstructedCoverage < 0.95 || f.HeartRate.MaxGap > 30 || f.HeartRate.AmbiguousSeconds > 0 {
		load = append(load, HRCoverageLow)
	}
	if parameter != nil && f.HeartRate.RobustMax != nil && *f.HeartRate.RobustMax > parameter.MaxHR {
		load = append(load, ParameterConflict)
	}
	for _, r := range f.HeartRate.Reasons {
		if r == SignalOutlier && f.HeartRate.ExcludedSeconds > 0.01*float64(s.DurationUs)/1e6 {
			load = append(load, SignalOutlier)
		}
	}
	f.Qualification.Load = qual(load, nil)
	performance, cs := []Reason{}, []Reason{}
	hasPerformance, hasCS := false, false
	for _, w := range f.Windows {
		if w.Best != nil && w.Best.Qualification.Status != "ineligible" {
			if w.Duration >= 1200 {
				hasPerformance = true
			}
			if w.Duration <= 900 {
				hasCS = true
			}
		}
	}
	common := append([]Reason{}, t.Pause.Reasons...)
	if f.Distance.RouteDifferenceRatio != nil && *f.Distance.RouteDifferenceRatio > .05 {
		common = append(common, DistanceMismatch)
	}
	if f.Environment.Indoor == nil {
		common = append(common, EnvironmentUnknown)
	} else if *f.Environment.Indoor {
		common = append(common, IndoorUnsupported)
	}
	if f.Distance.DifferenceRatio == nil {
		common = append(common, DistanceMissing)
	} else if *f.Distance.DifferenceRatio > 0.05 {
		common = append(common, DistanceMismatch)
	}
	if f.Route.GradeCoverage < 0.9 || f.Route.FlatDistanceFraction == nil {
		common = append(common, GradeUnknown)
	} else if *f.Route.FlatDistanceFraction < 0.9 {
		common = append(common, RouteAccuracyLow)
	}
	if !hasPerformance {
		performance = append(performance, SpeedGap)
		if f.Timing.Elapsed < 1200 {
			performance = append(performance, DurationTooShort)
		}
	}
	performance = append(performance, common...)
	if !hasCS {
		cs = append(cs, SpeedGap)
		if f.Timing.Elapsed < 180 {
			cs = append(cs, DurationTooShort)
		}
	}
	cs = append(cs, common...)
	f.Qualification.Performance = qual(performance, []Reason{EffortUnconfirmed})
	// Grade-adjusted counterpart: the same gates except that hills within
	// MaxGradeAdjusted are converted per window instead of failing the run.
	gap := []Reason{}
	hasGAP := false
	for _, w := range f.Windows {
		if w.GAP != nil && w.GAP.Qualification.Status != "ineligible" {
			hasGAP = true
		}
	}
	if !hasGAP {
		gap = append(gap, SpeedGap)
		if f.Timing.Elapsed < 1200 {
			gap = append(gap, DurationTooShort)
		}
	}
	for _, r := range common {
		if r != RouteAccuracyLow {
			gap = append(gap, r)
		}
	}
	gapQ := qual(gap, []Reason{EffortUnconfirmed})
	f.Qualification.PerformanceGAP = &gapQ
	f.Qualification.CS = qual(cs, []Reason{EffortUnconfirmed, CrossWorkoutEvidenceRequired})
	vo2 := append([]Reason{}, t.Pause.Reasons...)
	if f.Distance.RouteDifferenceRatio != nil && *f.Distance.RouteDifferenceRatio > .05 {
		vo2 = append(vo2, DistanceMismatch)
	}
	if f.HeartRate.Mean == nil {
		vo2 = append(vo2, MissingHR)
	}
	if f.Speed.Mean == nil {
		vo2 = append(vo2, SpeedGap)
	}
	if f.Route.GradeCoverage < 0.9 {
		vo2 = append(vo2, GradeUnknown)
	}
	if f.Environment.Indoor == nil {
		vo2 = append(vo2, EnvironmentUnknown)
	} else if *f.Environment.Indoor {
		vo2 = append(vo2, IndoorUnsupported)
	}
	goodSteady := 0
	vo2Soft := []Reason{}
	for _, x := range f.Steady {
		if x.Qualification.Status != "ineligible" {
			goodSteady++
			if x.Qualification.Status == "conditional" {
				vo2Soft = append(vo2Soft, x.Qualification.Reasons...)
			}
		}
	}
	if f.Timing.Elapsed < 1500 {
		vo2 = append(vo2, DurationTooShort)
	}
	if goodSteady < 3 {
		vo2 = append(vo2, SteadySupportLow)
		for _, x := range f.Steady {
			if x.Qualification.Status == "ineligible" {
				vo2 = append(vo2, x.Qualification.Reasons...)
			}
		}
	}
	vo2 = append(vo2, parameterReasons...)
	if len(f.HeartRate.SourceEpochs) > 1 {
		vo2 = append(vo2, SourceConflict)
	}
	f.Qualification.VO2 = qual(vo2, vo2Soft)
	endurance := append([]Reason{}, common...)
	enduranceSoft := []Reason{}
	if f.LongRun.LongestContinuousDuration < 3600 {
		endurance = append(endurance, DurationTooShort)
	}
	if f.LongRun.First == nil || f.LongRun.Second == nil {
		endurance = append(endurance, SteadySupportLow)
	} else if f.LongRun.First.GradeCoverage < .9 || f.LongRun.Second.GradeCoverage < .9 {
		endurance = append(endurance, GradeUnknown)
	} else if f.LongRun.First.FlatDistanceFraction < .9 || f.LongRun.Second.FlatDistanceFraction < .9 {
		endurance = append(endurance, RouteAccuracyLow)
	} else if f.LongRun.First.HR == nil || f.LongRun.Second.HR == nil {
		enduranceSoft = append(enduranceSoft, MissingHR)
	}
	f.Qualification.Endurance = qual(endurance, enduranceSoft)
	// This application/model family is explicitly running-scoped. Preserve
	// other source enums, but never qualify them as running evidence.
	if s.ActivityType != 37 {
		reject := func(q Qualification) Qualification { return qual(append(q.Reasons, UnsupportedInput), nil) }
		f.Reasons = reasonSet(f.Reasons, []Reason{UnsupportedInput})
		f.Qualification.Load = reject(f.Qualification.Load)
		f.Qualification.VO2 = reject(f.Qualification.VO2)
		f.Qualification.Performance = reject(f.Qualification.Performance)
		f.Qualification.CS = reject(f.Qualification.CS)
		f.Qualification.Endurance = reject(f.Qualification.Endurance)
		if f.Qualification.PerformanceGAP != nil {
			q := reject(*f.Qualification.PerformanceGAP)
			f.Qualification.PerformanceGAP = &q
		}
		for i := range f.Windows {
			if f.Windows[i].Best != nil {
				f.Windows[i].Best.Qualification = reject(f.Windows[i].Best.Qualification)
			}
			if f.Windows[i].Backup != nil {
				f.Windows[i].Backup.Qualification = reject(f.Windows[i].Backup.Qualification)
			}
			if f.Windows[i].GAP != nil {
				f.Windows[i].GAP.Qualification = reject(f.Windows[i].GAP.Qualification)
			}
		}
		for i := range f.Steady {
			f.Steady[i].Qualification = reject(f.Steady[i].Qualification)
			f.Steady[i].Window.Qualification = f.Steady[i].Qualification
		}
	}
	if _, _, err := f.MarshalArtifact(); err != nil {
		return f, err
	}
	return f, nil
}
