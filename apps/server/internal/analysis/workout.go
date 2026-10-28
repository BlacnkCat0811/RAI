package analysis

import (
	"context"
	"math"
	"sort"
)

const (
	HeartRate  = "HKQuantityTypeIdentifierHeartRate"
	Cadence    = "HKQuantityTypeIdentifierRunningCadence"
	Power      = "HKQuantityTypeIdentifierRunningPower"
	WorkoutDay = "0001-01-01"
)

type interval struct{ a, b int64 }

func coverage(xs []interval, start, end int64) float64 {
	if end <= start || len(xs) == 0 {
		return 0
	}
	sort.Slice(xs, func(i, j int) bool {
		if xs[i].a == xs[j].a {
			return xs[i].b < xs[j].b
		}
		return xs[i].a < xs[j].a
	})
	a, b := xs[0].a, xs[0].b
	var total float64
	for _, x := range xs[1:] {
		if x.a <= b {
			if x.b > b {
				b = x.b
			}
		} else {
			total += float64(b - a)
			a, b = x.a, x.b
		}
	}
	total += float64(b - a)
	return math.Min(1, math.Max(0, total/float64(end-start)))
}
func metric(name, algorithm, unit string, v *float64, q string, details map[string]any) Metric {
	if v == nil {
		q = "insufficient"
	}
	if details == nil {
		details = map[string]any{}
	}
	return Metric{Day: WorkoutDay, Name: name, Algorithm: algorithm, Version: Version, Value: v, Unit: unit, Quality: q, Details: details}
}

type trace struct {
	mean, max *float64
	coverage  float64
	ambiguous bool
	count     int
}

func summarize(w WorkoutInput, kind, unit string) (trace, error) {
	var out trace
	var xs []Observation
	var spans []interval
	for _, o := range w.Observations {
		if o.Type != kind {
			continue
		}
		if !Finite(o.Value) || o.Value < 0 || o.Unit != unit || o.End < o.Start {
			return out, Invalid()
		}
		a, b := max(o.Start, w.Start), min(o.End, w.End)
		if a > b {
			continue
		}
		o.Start, o.End = a, b
		xs = append(xs, o)
		if b > a {
			spans = append(spans, interval{a, b})
		}
		if out.max == nil || o.Value > *out.max {
			out.max = Ptr(o.Value)
		}
	}
	sort.Slice(xs, func(i, j int) bool {
		if xs[i].Start == xs[j].Start {
			if xs[i].End == xs[j].End {
				return xs[i].ID < xs[j].ID
			}
			return xs[i].End < xs[j].End
		}
		return xs[i].Start < xs[j].Start
	})
	var weighted, seconds, sum float64
	var lastEnd int64
	hasLast := false
	for _, o := range xs {
		out.count++
		sum += o.Value
		if o.End > o.Start {
			if hasLast && o.Start < lastEnd {
				out.ambiguous = true
			}
			if !hasLast || o.End > lastEnd {
				lastEnd = o.End
			}
			hasLast = true
			d := float64(o.End-o.Start) / 1e6
			weighted += o.Value * d
			seconds += d
		}
	}
	out.coverage = coverage(spans, w.Start, w.End)
	if !out.ambiguous {
		if seconds > 0 {
			out.mean = Ptr(weighted / seconds)
		} else if len(xs) > 0 {
			out.mean = Ptr(sum / float64(len(xs)))
		}
	}
	// Summary averages are displayable but never supply trace coverage.
	for _, s := range w.Statistics {
		if s.Type == kind {
			if s.Average != nil {
				if !Finite(*s.Average) || *s.Average < 0 {
					return out, Invalid()
				}
				if out.mean == nil && !out.ambiguous {
					out.mean = Ptr(*s.Average)
				}
			}
			if s.Maximum != nil {
				if !Finite(*s.Maximum) || *s.Maximum < 0 {
					return out, Invalid()
				}
				if out.max == nil || *s.Maximum > *out.max {
					out.max = Ptr(*s.Maximum)
				}
			}
		}
	}
	return out, nil
}
func WorkoutFeatures(ctx context.Context, w WorkoutInput) ([]Metric, error) {
	if w.Deleted {
		return nil, nil
	}
	if w.End < w.Start || !Finite(w.Duration) || w.Duration < 0 || w.Duration > float64(w.End-w.Start)/1e6 {
		return nil, Invalid()
	}
	if w.Distance != nil && (!Finite(*w.Distance) || *w.Distance < 0) {
		return nil, Invalid()
	}
	ms := []Metric{metric("duration", "workout.features", "s", Ptr(w.Duration), "available", nil), metric("distance", "workout.features", "m", w.Distance, "available", nil)}
	var speed, pace *float64
	if w.Distance != nil && w.Duration > 0 {
		speed = Ptr(*w.Distance / w.Duration)
		if *w.Distance > 0 {
			pace = Ptr(w.Duration / *w.Distance)
		}
	}
	ms = append(ms, metric("average_speed", "workout.features", "m/s", speed, "available", nil), metric("average_pace", "workout.features", "s/m", pace, "available", nil))
	var hr trace
	for _, spec := range []struct{ kind, name, unit string }{{HeartRate, "heart_rate", "count/min"}, {Cadence, "cadence", "count/min"}, {Power, "power", "W"}} {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		t, err := summarize(w, spec.kind, spec.unit)
		if err != nil {
			return nil, err
		}
		if spec.kind == HeartRate {
			hr = t
		}
		q := "partial"
		if t.coverage >= 0.9 && !t.ambiguous {
			q = "available"
		}
		d := map[string]any{"observations": t.count, "coverage": t.coverage, "ambiguousIntervals": t.ambiguous}
		ms = append(ms, metric("average_"+spec.name, "workout.features", spec.unit, t.mean, q, d), metric(spec.name+"_coverage", "workout.features", "1", Ptr(t.coverage), "available", nil))
		if spec.kind == HeartRate {
			ms = append(ms, metric("max_heart_rate", "workout.features", spec.unit, t.max, q, d))
		}
	}
	route, err := routeFeatures(ctx, w)
	if err != nil {
		return nil, err
	}
	ms = append(ms, route...)
	load, err := TrainingLoad(w, hr)
	if err != nil {
		return nil, err
	}
	ms = append(ms, load)
	return ms, ValidateMetrics(ms)
}
func TrainingLoad(w WorkoutInput, hr trace) (Metric, error) {
	d := map[string]any{"method": "banister_trimp", "inputQuality": "insufficient", "missingComponents": []string{}}
	missing := []string{}
	if w.Parameters == nil {
		missing = append(missing, "personal_parameters")
	}
	if hr.mean == nil {
		missing = append(missing, "average_heart_rate")
	}
	if w.Duration != float64(w.End-w.Start)/1e6 {
		missing = append(missing, "active_time_alignment")
	}
	if hr.coverage < 0.9 || hr.ambiguous {
		missing = append(missing, "heart_rate_coverage")
	}
	d["missingComponents"] = missing
	if len(missing) > 0 {
		return metric("workout_load", "training_load.trimp", "AU", nil, "insufficient", d), nil
	}
	p := w.Parameters
	if p.Version < 1 || p.Provenance == "" || !Finite(p.RestHR) || !Finite(p.MaxHR) || !Finite(p.A) || !Finite(p.B) || p.RestHR <= 0 || p.MaxHR <= p.RestHR || p.A <= 0 || p.A > 10 || p.B <= 0 || p.B > 10 {
		return Metric{}, Invalid()
	}
	r := (*hr.mean - p.RestHR) / (p.MaxHR - p.RestHR)
	if r < 0 || r > 1 {
		return Metric{}, Invalid()
	}
	value := w.Duration / 60 * r * p.A * math.Exp(p.B*r)
	d["parameterVersion"] = p.Version
	d["parameterProvenance"] = p.Provenance
	d["inputQuality"] = "available"
	d["heartRateCoverage"] = hr.coverage
	return metric("workout_load", "training_load.trimp", "AU", Ptr(value), "available", d), nil
}
func routeFeatures(ctx context.Context, w WorkoutInput) ([]Metric, error) {
	var distance, gain, loss float64
	connections, elevations := 0, 0
	var spans []interval
	for i, p := range w.Route {
		if i%1024 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if !Finite(p.Latitude) || !Finite(p.Longitude) || math.Abs(p.Latitude) > 90 || math.Abs(p.Longitude) > 180 || p.At < w.Start || p.At > w.End {
			return nil, Invalid()
		}
		for _, v := range []*float64{p.Altitude, p.HorizontalAccuracy, p.VerticalAccuracy} {
			if v != nil && !Finite(*v) {
				return nil, Invalid()
			}
		}
		if i == 0 {
			continue
		}
		a := w.Route[i-1]
		if a.Route != p.Route {
			continue
		}
		if p.At <= a.At {
			return nil, Invalid()
		}
		if p.At-a.At > 30e6 || a.HorizontalAccuracy == nil || p.HorizontalAccuracy == nil || *a.HorizontalAccuracy < 0 || *p.HorizontalAccuracy < 0 || *a.HorizontalAccuracy > 25 || *p.HorizontalAccuracy > 25 {
			continue
		}
		distance += haversine(a, p)
		connections++
		spans = append(spans, interval{a.At, p.At})
		if a.Altitude != nil && p.Altitude != nil && a.VerticalAccuracy != nil && p.VerticalAccuracy != nil && *a.VerticalAccuracy >= 0 && *p.VerticalAccuracy >= 0 && *a.VerticalAccuracy <= 10 && *p.VerticalAccuracy <= 10 {
			diff := *p.Altitude - *a.Altitude
			if diff > 0 {
				gain += diff
			} else {
				loss -= diff
			}
			elevations++
		}
	}
	var dist, up, down *float64
	if connections > 0 {
		dist = Ptr(distance)
	}
	if elevations > 0 {
		up = Ptr(gain)
		down = Ptr(loss)
	}
	return []Metric{metric("route_distance", "workout.features", "m", dist, "partial", map[string]any{"method": "haversine", "earthRadiusM": 6371008.8}), metric("elevation_gain", "workout.features", "m", up, "partial", nil), metric("elevation_loss", "workout.features", "m", down, "partial", nil), metric("route_coverage", "workout.features", "1", Ptr(coverage(spans, w.Start, w.End)), "available", nil)}, nil
}
func haversine(a, b Point) float64 {
	rad := math.Pi / 180
	dlat := (b.Latitude - a.Latitude) * rad
	dlon := (b.Longitude - a.Longitude) * rad
	x := math.Sin(dlat/2)*math.Sin(dlat/2) + math.Cos(a.Latitude*rad)*math.Cos(b.Latitude*rad)*math.Sin(dlon/2)*math.Sin(dlon/2)
	return 2 * 6371008.8 * math.Asin(math.Sqrt(math.Min(1, x)))
}

// CalculateTrainingLoad is the independently replayable TRIMP entry point.
func CalculateTrainingLoad(w WorkoutInput) (Metric, error) {
	if w.Deleted || w.End < w.Start || !Finite(w.Duration) || w.Duration < 0 || w.Duration > float64(w.End-w.Start)/1e6 {
		return Metric{}, Invalid()
	}
	hr, e := summarize(w, HeartRate, "count/min")
	if e != nil {
		return Metric{}, e
	}
	m, e := TrainingLoad(w, hr)
	if e != nil {
		return m, e
	}
	return m, ValidateMetrics([]Metric{m})
}
