package lactatethreshold

import (
	"math"
	"slices"
	"sort"
)

func ptr[T any](v T) *T { return &v }

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round4(v float64) float64 { return math.Round(v*1e4) / 1e4 }

func add(xs []string, rs ...string) []string {
	for _, r := range rs {
		if r != "" && !slices.Contains(xs, r) {
			xs = append(xs, r)
		}
	}
	return xs
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	s := slices.Clone(xs)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

// weightedMedian is the lower weighted median, ties broken by value.
func weightedMedian(xs, ws []float64) float64 {
	idx := make([]int, len(xs))
	total := 0.0
	for i := range xs {
		idx[i] = i
		total += ws[i]
	}
	sort.SliceStable(idx, func(a, b int) bool { return xs[idx[a]] < xs[idx[b]] })
	acc := 0.0
	for _, i := range idx {
		acc += ws[i]
		if acc >= total/2-1e-12 {
			return xs[i]
		}
	}
	return xs[idx[len(idx)-1]]
}

func weightedMAD(xs, ws []float64) float64 {
	m := weightedMedian(xs, ws)
	d := make([]float64, len(xs))
	for i, x := range xs {
		d[i] = math.Abs(x - m)
	}
	return weightedMedian(d, ws)
}

func huberWeight(u, delta float64) float64 {
	if a := math.Abs(u); a > delta {
		return delta / a
	}
	return 1
}

// huberLocation: weighted median start, scale max(3, 1.4826 MAD), bounded
// iterations; false when it does not converge.
func huberLocation(p Policy, xs, ws []float64) (float64, bool) {
	if len(xs) == 0 {
		return 0, false
	}
	total := 0.0
	for _, w := range ws {
		total += w
	}
	if !(total > 0) {
		return 0, false
	}
	mu := weightedMedian(xs, ws)
	s := math.Max(3, 1.4826*weightedMAD(xs, ws))
	for i := 0; i < p.MaxIterations; i++ {
		num, den := 0.0, 0.0
		for k, x := range xs {
			c := ws[k] * huberWeight((x-mu)/s, p.HuberDelta)
			num += c * x
			den += c
		}
		next := num / den
		if math.Abs(next-mu) <= p.Tolerance {
			return next, true
		}
		mu = next
	}
	return mu, false
}

// huberLine fits y = a + b x by bounded IRLS; false when ill-conditioned or
// not converged.
func huberLine(p Policy, xs, ys, ws []float64) (a, b float64, ok bool) {
	solve := func(rw []float64) (float64, float64, bool) {
		var sw, sx, sy, sxx, sxy float64
		for i := range xs {
			w := rw[i]
			sw += w
			sx += w * xs[i]
			sy += w * ys[i]
			sxx += w * xs[i] * xs[i]
			sxy += w * xs[i] * ys[i]
		}
		det := sw*sxx - sx*sx
		if !(sw > 0) || math.Abs(det) <= 1e-12*sw*sw {
			return 0, 0, false
		}
		bb := (sw*sxy - sx*sy) / det
		return (sy - bb*sx) / sw, bb, true
	}
	a, b, ok = solve(ws)
	if !ok {
		return 0, 0, false
	}
	s := math.Max(3, 1.4826*weightedMAD(ys, ws))
	rw := make([]float64, len(xs))
	for it := 0; it < p.MaxIterations; it++ {
		for i := range xs {
			rw[i] = ws[i] * huberWeight((ys[i]-a-b*xs[i])/s, p.HuberDelta)
		}
		na, nb, good := solve(rw)
		if !good {
			return 0, 0, false
		}
		if math.Abs(na-a) < p.Tolerance && math.Abs(nb-b) < p.Tolerance {
			return na, nb, true
		}
		a, b = na, nb
	}
	return 0, 0, false
}

func weightedSlope(xs, ys, ws []float64) float64 {
	var sw, mx, my float64
	for i := range xs {
		sw += ws[i]
		mx += ws[i] * xs[i]
		my += ws[i] * ys[i]
	}
	if sw == 0 {
		return 0
	}
	mx /= sw
	my /= sw
	num, den := 0.0, 0.0
	for i := range xs {
		num += ws[i] * (xs[i] - mx) * (ys[i] - my)
		den += ws[i] * (xs[i] - mx) * (xs[i] - mx)
	}
	if den == 0 {
		return 0
	}
	return num / den
}
