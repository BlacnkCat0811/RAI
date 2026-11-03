package performance

import (
	"math"
	"sort"
)

type point struct {
	x, y, w float64
	workout string
}
type residual struct {
	z     float64
	index int
}
type fit struct {
	alpha, gamma, loss float64
	ok                 bool
}

func quantileFit(ps []point, sorted []residual, skip string, gamma float64) fit {
	total := 0.0
	for _, p := range ps {
		if p.workout != skip {
			total += p.w
		}
	}
	if total <= 0 {
		return fit{}
	}
	cum, alpha := 0.0, 0.0
	for _, r := range sorted {
		p := ps[r.index]
		if p.workout == skip {
			continue
		}
		cum += p.w
		alpha = r.z
		if cum >= .8*total {
			break
		}
	}
	loss := 0.0
	for _, p := range ps {
		if p.workout == skip {
			continue
		}
		u := p.y - alpha - gamma*p.x
		if u < 0 {
			loss += p.w * u * (-.2)
		} else {
			loss += p.w * u * .8
		}
	}
	return fit{alpha, gamma, loss, true}
}
func better(a, b fit) bool {
	if !b.ok {
		return a.ok
	}
	// Frozen numerical tie tolerance; canonical input ordering fixes sums.
	if math.Abs(a.loss-b.loss) > 1e-12 {
		return a.loss < b.loss
	}
	return a.gamma > b.gamma || (a.gamma == b.gamma && a.alpha < b.alpha)
}

// fitCurve evaluates all 301 grid points. The same sorted residuals are reused
// for LOO fits, avoiding a general optimizer and repeated sorting per workout.
func fitCurve(es []WeightedEvidence, loo bool) (*Curve, string) {
	ps := []point{}
	ids := []string{}
	seen := map[string]bool{}
	for _, e := range es {
		if e.Weight <= 0 {
			continue
		}
		ps = append(ps, point{math.Log(float64(e.Duration) / 1800), math.Log(e.Distance), e.Weight, e.WorkoutID})
		if !seen[e.WorkoutID] {
			seen[e.WorkoutID] = true
			ids = append(ids, e.WorkoutID)
		}
	}
	if len(ps) == 0 {
		return nil, "collecting"
	}
	best := fit{}
	removed := make([]fit, len(ids))
	sorted := make([]residual, len(ps))
	for g := 700; g <= 1000; g++ {
		gamma := float64(g) / 1000
		for i, p := range ps {
			sorted[i] = residual{p.y - gamma*p.x, i}
		}
		sort.Slice(sorted, func(i, j int) bool {
			if sorted[i].z != sorted[j].z {
				return sorted[i].z < sorted[j].z
			}
			return sorted[i].index < sorted[j].index
		})
		f := quantileFit(ps, sorted, "", gamma)
		if better(f, best) {
			best = f
		}
		if loo {
			for i, id := range ids {
				f = quantileFit(ps, sorted, id, gamma)
				if better(f, removed[i]) {
					removed[i] = f
				}
			}
		}
	}
	a := ability(best.alpha, best.gamma)
	c := &Curve{D30: a.D30, Gamma: a.Gamma, V20: a.V20, V60: a.V60, Retention: a.Retention, Loss: best.loss}
	abs := make([]float64, len(ps))
	for i, p := range ps {
		abs[i] = math.Abs(p.y - best.alpha - best.gamma*p.x)
	}
	sort.Float64s(abs)
	c.MedianAbsoluteResidual = abs[len(abs)/2]
	if len(abs)%2 == 0 {
		c.MedianAbsoluteResidual = (abs[len(abs)/2-1] + abs[len(abs)/2]) / 2
	}
	if best.gamma == .7 || c.MedianAbsoluteResidual > .06 {
		return c, "curve_not_identified"
	}
	if loo {
		for _, f := range removed {
			if !f.ok || f.gamma == .7 {
				return c, "unstable"
			}
			v := ability(f.alpha, f.gamma).V60
			c.LOOMaxChange = math.Max(c.LOOMaxChange, math.Abs(v/c.V60-1))
		}
		if c.LOOMaxChange > .05+1e-12 {
			return c, "unstable"
		}
	}
	return c, "available"
}

// Fit is an inspectable mathematical entry point. Production gating additionally
// requires independent workouts, duration spread, recency and smoothing targets.
func Fit(es []WeightedEvidence) (*Curve, string) {
	es = append([]WeightedEvidence{}, es...)
	sort.Slice(es, func(i, j int) bool {
		if es[i].WorkoutID != es[j].WorkoutID {
			return es[i].WorkoutID < es[j].WorkoutID
		}
		return es[i].Duration < es[j].Duration
	})
	return fitCurve(es, true)
}
