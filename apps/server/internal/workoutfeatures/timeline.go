package workoutfeatures

import (
	"math"
	"rai/server/internal/workoutdomain"
	"sort"
)

// AnalysisTimeline is bounded, ephemeral, and contains no presentation points.
type AnalysisTimeline struct {
	Pause     PauseTimeline
	HeartRate Signal
	Speed     Signal
	Route     RouteSupport
	Start     int64
	Cells     []cell
}
type cell struct {
	gradedMeters, flatMeters                                        float64
	modestMeters, gapMeters                                         float64
	speedGap                                                        float64
	speed, speed2, hr, hrTime, hrMoment, grade, gradeTime, flatTime float64
	epoch, hrEpoch                                                  string
	continuous                                                      bool
	activeSegment                                                   int
}

func BuildTimeline(s RawWorkoutSnapshot) AnalysisTimeline {
	t := AnalysisTimeline{Pause: BuildPause(s)}
	t.HeartRate = BuildSignal(s, t.Pause, HR)
	t.Speed = BuildSignal(s, t.Pause, Speed)
	t.Route = BuildRoute(s, t.Pause)
	return BuildAnalysisIndex(s, t.Pause, t.HeartRate, t.Speed, t.Route)
}

// BuildAnalysisIndex aligns native support to an ephemeral one-second index.
func BuildAnalysisIndex(s RawWorkoutSnapshot, p PauseTimeline, hr, speed Signal, route RouteSupport) AnalysisTimeline {
	t := AnalysisTimeline{Pause: p, HeartRate: hr, Speed: speed, Route: route}
	second := s.Start / int64(1e6)
	if s.Start%int64(1e6) > 0 {
		second++
	}
	t.Start = second * int64(1e6)
	n := int((s.End - t.Start) / 1e6)
	if n < 0 {
		n = 0
	}
	t.Cells = make([]cell, n)
	scan := func(spans []Span, fn func(*cell, Span, int64, int64)) {
		i := 0
		for j := range t.Cells {
			a, b := t.Start+int64(j)*1e6, t.Start+int64(j+1)*1e6
			for i < len(spans) && spans[i].End <= a {
				i++
			}
			for k := i; k < len(spans) && spans[k].Start < b; k++ {
				x := spans[k]
				lo, hi := max(a, x.Start), min(b, x.End)
				if lo < hi {
					fn(&t.Cells[j], x, lo, hi)
				}
			}
		}
	}
	speedTimes := make([]float64, n)
	conflict := make([]bool, n)
	scan(t.Speed.Spans, func(c *cell, x Span, a, b int64) {
		index := int((a - t.Start) / 1e6)
		dt := float64(b-a) / 1e6
		speedTimes[index] += dt
		c.speedGap = math.Max(c.speedGap, x.SupportGap)
		if c.epoch != "" && (c.epoch != x.Epoch || c.activeSegment != x.ActiveSegment) {
			conflict[index] = true
		}
		c.epoch = x.Epoch
		c.activeSegment = x.ActiveSegment
		c.speed += x.integral(a, b)
		v, w := x.value(a), x.value(b)
		c.speed2 += (v*v + v*w + w*w) / 3 * dt
	})
	scan(t.HeartRate.Spans, func(c *cell, x Span, a, b int64) {
		dt := float64(b-a) / 1e6
		v, w := x.value(a), x.value(b)
		c.hr += x.integral(a, b)
		c.hrTime += dt
		mid := float64(a-t.Start) / 1e6 // exact first moment of a linear native span
		c.hrMoment += mid*(v+w)*0.5*dt + (v+2*w)/6*dt*dt
		if c.hrEpoch != "" && c.hrEpoch != x.Epoch {
			c.hrEpoch = "ambiguous"
		} else if c.hrEpoch == "" {
			c.hrEpoch = x.Epoch
		}
	})
	scan(t.Route.Grades, func(c *cell, x Span, a, b int64) {
		dt := float64(b-a) / 1e6
		c.grade += x.V0 * dt
		c.gradeTime += dt
		if math.Abs(x.V0) <= 0.01 {
			c.flatTime += dt
		}
	})
	// Grade gates are weighted by exact common native speed/route distance,
	// rather than seconds; fast uphill seconds cannot hide inside slow flat time.
	gi := 0
	for _, speed := range t.Speed.Spans {
		for gi < len(t.Route.Grades) && t.Route.Grades[gi].End <= speed.Start {
			gi++
		}
		for k := gi; k < len(t.Route.Grades) && t.Route.Grades[k].Start < speed.End; k++ {
			g := t.Route.Grades[k]
			a, b := max(speed.Start, g.Start, t.Start), min(speed.End, g.End, t.Start+int64(n)*1e6)
			for a < b {
				i := int((a - t.Start) / 1e6)
				end := min(b, t.Start+int64(i+1)*1e6)
				meters := speed.integral(a, end)
				t.Cells[i].gradedMeters += meters
				if math.Abs(g.V0) <= .01 {
					t.Cells[i].flatMeters += meters
				}
				if math.Abs(g.V0) <= MaxGradeAdjusted {
					t.Cells[i].modestMeters += meters
					t.Cells[i].gapMeters += meters * workoutdomain.GradeAdjustment(g.V0)
				}
				a = end
			}
		}
	}

	for i := range t.Cells {
		t.Cells[i].continuous = speedTimes[i] >= 1-1e-9 && !conflict[i]
	}
	// A 30s supported rolling median; isolated points cannot create a robust max.
	values := make([]float64, 0, 30)
	for i := 29; i < n; i++ {
		values = values[:0]
		epoch := ""
		ok := true
		for j := i - 29; j <= i; j++ {
			c := t.Cells[j]
			if c.hrTime < 1-1e-9 || c.hrEpoch == "ambiguous" || c.hrEpoch == "" || epoch != "" && epoch != c.hrEpoch || containing(t.Pause.Active, t.Start+int64(i-29)*1e6, t.Start+int64(i+1)*1e6) < 0 {
				ok = false
				break
			}
			epoch = c.hrEpoch
			values = append(values, c.hr)
		}
		if ok {
			sort.Float64s(values)
			v := (values[14] + values[15]) / 2
			if t.HeartRate.Summary.RobustMax == nil || v > *t.HeartRate.Summary.RobustMax {
				t.HeartRate.Summary.RobustMax = ptr(v)
			}
		}
	}
	return t
}
