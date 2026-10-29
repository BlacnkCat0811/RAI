package analysis

import (
	"context"
	"math"
	"time"
)

func TrainingState(ctx context.Context, in StateInput) ([]Metric, error) {
	loc, e := time.LoadLocation(in.Timezone)
	if e != nil {
		return nil, Invalid()
	}
	from, e := time.ParseInLocation("2006-01-02", in.From, loc)
	if e != nil {
		return nil, Invalid()
	}
	through, e := time.ParseInLocation("2006-01-02", in.Through, loc)
	if e != nil || through.Before(from) || through.After(from.AddDate(0, 0, 365)) {
		return nil, Invalid()
	}
	type daily struct {
		sum     float64
		unknown bool
		count   int
		runs    []string
	}
	loads := map[string]daily{}
	for _, l := range in.Loads {
		day, e := time.ParseInLocation("2006-01-02", l.Day, loc)
		if e != nil || day.Before(from.AddDate(0, 0, -179)) || day.After(through) {
			return nil, Invalid()
		}
		x := loads[l.Day]
		x.count++
		x.runs = append(x.runs, l.RunID)
		if l.Value == nil {
			x.unknown = true
		} else {
			if !Finite(*l.Value) || *l.Value < 0 {
				return nil, Invalid()
			}
			x.sum += *l.Value
		}
		loads[l.Day] = x
	}
	var ms []Metric
	for day := from; !day.After(through); day = day.AddDate(0, 0, 1) {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		var fitness, fatigue float64
		count := 0
		unknown := false
		for k := 0; k < WindowDays; k++ {
			x := loads[day.AddDate(0, 0, -k).Format("2006-01-02")]
			count += x.count
			unknown = unknown || x.unknown
			fitness += (1 - math.Exp(-1.0/42)) * math.Exp(-float64(k)/42) * x.sum
			fatigue += (1 - math.Exp(-1.0/7)) * math.Exp(-float64(k)/7) * x.sum
		}
		valid := count > 0 && !unknown
		reason := []string{"sleep", "HRV", "resting_HR_trend", "import_coverage_unverified"}
		if unknown {
			reason = append(reason, "workout_load")
		}
		if count == 0 {
			reason = append(reason, "recorded_load_history")
		}
		details := map[string]any{"timezone": in.Timezone, "windowDays": WindowDays, "fitnessTauDays": 42, "fatigueTauDays": 7, "recordedWorkouts": count, "missingComponents": reason, "interpretation": "load_only", "previousState": "not_used_finite_kernel"}
		value := func(v float64) *float64 {
			if valid {
				return Ptr(v)
			}
			return nil
		}
		q := "partial"
		d := loads[day.Format("2006-01-02")]
		// A day's recorded load has its own availability. Missing historical
		// load only invalidates the convolution, never a known daily total.
		dailyValue := Ptr(d.sum)
		if d.unknown {
			dailyValue = nil
		}
		var recovery, readiness *float64
		if valid && fitness > 0 {
			r := math.Max(0, math.Min(1, fitness/math.Max(fitness, fatigue)))
			recovery = Ptr(r)
			readiness = Ptr(100 * r)
		}
		details["components"] = map[string]any{"fitness": value(fitness), "fatigue": value(fatigue), "balance": value(fitness - fatigue), "recovery": recovery}
		for _, m := range []Metric{metric("daily_load", "fitness_fatigue", "AU", dailyValue, q, map[string]any{"upstreamRuns": d.runs, "dayAssignment": "workout_start", "timezone": in.Timezone}), metric("fitness", "fitness_fatigue", "AU/day", value(fitness), q, details), metric("fatigue", "fitness_fatigue", "AU/day", value(fatigue), q, details), metric("balance", "fitness_fatigue", "AU/day", value(fitness-fatigue), q, details), metric("recovery", "recovery", "1", recovery, q, details), metric("readiness", "readiness", "score", readiness, q, details)} {
			m.Day = day.Format("2006-01-02")
			ms = append(ms, m)
		}
	}
	return ms, ValidateMetrics(ms)
}

// Analyze is a small static registry. A bundle version freezes all component
// versions; unsupported kinds fail rather than reporting a successful job.
func Analyze(ctx context.Context, kind string, in Snapshot) ([]Metric, error) {
	switch kind {
	case "analysis.workout":
		if in.Workout == nil {
			return nil, Invalid()
		}
		return WorkoutFeatures(ctx, *in.Workout)
	case "analysis.training_state":
		if in.State == nil {
			return nil, Invalid()
		}
		return TrainingState(ctx, *in.State)
	case "analysis.sample", "analysis.series":
		return nil, nil // real dependency publication is the storage transaction
	default:
		return nil, &Failure{Code: "analysis_unsupported_algorithm"}
	}
}
