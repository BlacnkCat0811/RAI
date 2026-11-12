package runfitness

import (
	"errors"
	"math"
	"sort"
)

// Curve is the two-segment ability curve (section 10.1).
type Curve struct {
	VT, V6, BL float64
}

// cT and c6 are the prior log relations vT/A and v6/vT.
func cT(p Policy) float64 { return p.Beta * math.Log(p.RT/RefIntensity) }
func c6(bS float64) float64 {
	return (1/bS - 1) * math.Log(360/TT)
}

// Speed is v(t) for t >= 180 s (180-360 s only for speed tests).
func (c Curve) Speed(t float64) float64 {
	switch {
	case t > TT:
		return c.VT * math.Pow(t/TT, 1/c.BL-1)
	case t >= 360:
		z := math.Log(t/360) / math.Log(TT/360)
		return math.Exp((1-z)*math.Log(c.V6) + z*math.Log(c.VT))
	default:
		return c.V6 * math.Pow(t/360, 1/BS0-1)
	}
}

// Distance is D(t) = t*v(t).
func (c Curve) Distance(t float64) float64 { return t * c.Speed(t) }

// Valid: positive anchors, v6 >= vT, bL in domain, v non-increasing and D
// strictly increasing.
func (c Curve) Valid(p Policy) bool {
	if !(c.VT > 0 && c.V6 > 0 && finite(c.VT) && finite(c.V6)) || c.V6 < c.VT || c.BL < p.BLDomain[0]-1e-12 || c.BL > p.BLDomain[1]+1e-12 {
		return false
	}
	slope := -math.Log(c.V6/c.VT) / math.Log(TT/360)
	return slope > -1
}

// TimeFor solves D(t)=distance on [360 s, 8 h] by deterministic bisection.
func (c Curve) TimeFor(distance float64) (float64, bool) {
	lo, hi := 360.0, ZeroSeconds
	if c.Distance(lo) > distance || c.Distance(hi) < distance {
		return 0, false
	}
	for hi-lo > .05 {
		mid := (lo + hi) / 2
		if c.Distance(mid) < distance {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2, true
}

const (
	obsA = iota
	obsRace
	obsLower
	obsVT
	obsPrior
)

// fitObs is linear in theta=[lnA, dT, d6] for a fixed bL:
// prediction = g.theta + h, residual (x - prediction)/sigma.
type fitObs struct {
	kind     int
	x, sigma float64
	w        float64
	g        [3]float64
	h        float64
	day      string
	workout  string
	duration float64
	// bLDependent observations change with the bL grid.
	bLDependent bool
}

// speedObs builds the linear prediction of ln v(t).
func speedObs(p Policy, t, bL float64) ([3]float64, float64) {
	ct, c := cT(p), c6(p.BS0)
	switch {
	case t > TT:
		return [3]float64{1, 1, 0}, ct + (1/bL-1)*math.Log(t/TT)
	case t >= 360:
		z := math.Log(t/360) / math.Log(TT/360)
		return [3]float64{1, 1, 1 - z}, ct + (1-z)*c
	default:
		return [3]float64{1, 1, 1}, ct + c + (1/p.BS0-1)*math.Log(t/360)
	}
}

func huberRho(u, delta float64) float64 {
	a := math.Abs(u)
	if a <= delta {
		return u * u / 2
	}
	return delta*a - delta*delta/2
}

func huberWeight(u, delta float64) float64 {
	a := math.Abs(u)
	if a <= delta {
		return 1
	}
	return delta / a
}

// FitResult holds the joint fit at its selected bL.
type FitResult struct {
	Theta     [3]float64
	BL        float64
	Objective float64
	Cov       [3][3]float64
	ScaleA    float64
	Curve     Curve
	A         float64
	Iter      int
}

var errUnidentified = errors.New("running_fitness_unidentified")

// weightedMedian is the first-crossing weighted median.
func weightedMedian(xs, ws []float64) float64 {
	idx := make([]int, len(xs))
	total := 0.0
	for i := range idx {
		idx[i] = i
		total += ws[i]
	}
	sort.SliceStable(idx, func(a, b int) bool { return xs[idx[a]] < xs[idx[b]] })
	acc := 0.0
	for _, i := range idx {
		acc += ws[i]
		if acc >= total/2 {
			return xs[i]
		}
	}
	return xs[idx[len(idx)-1]]
}

// robustScale is max(1.4826 * weighted MAD, minScale) of A observations.
func robustScale(p Policy, xs, ws []float64) float64 {
	if len(xs) == 0 {
		return p.MinScale
	}
	m := weightedMedian(xs, ws)
	dev := make([]float64, len(xs))
	for i, x := range xs {
		dev[i] = math.Abs(x - m)
	}
	return math.Max(1.4826*weightedMedian(dev, ws), p.MinScale)
}

// fitObservations converts evidence into fit observations for a bL.
func withBL(p Policy, obs []fitObs, bL float64) []fitObs {
	out := make([]fitObs, len(obs))
	copy(out, obs)
	for i := range out {
		if out[i].bLDependent {
			out[i].g, out[i].h = speedObs(p, out[i].duration, bL)
		}
	}
	return out
}

// irls solves theta for fixed observations with Huber weights. Lower-bound
// observations only act when violated (one-sided).
func irls(p Policy, obs []fitObs, start [3]float64) ([3]float64, float64, [3][3]float64, int, error) {
	theta := start
	var m [3][3]float64
	iter := 0
	for iter = 1; iter <= p.MaxIterations; iter++ {
		var b [3]float64
		m = [3][3]float64{}
		for _, o := range obs {
			pred := o.g[0]*theta[0] + o.g[1]*theta[1] + o.g[2]*theta[2] + o.h
			u := (o.x - pred) / o.sigma
			if o.kind == obsLower && u <= 0 {
				continue
			}
			c := o.w * huberWeight(u, p.HuberDelta) / (o.sigma * o.sigma)
			for i := 0; i < 3; i++ {
				if o.g[i] == 0 {
					continue
				}
				for j := 0; j < 3; j++ {
					if o.g[j] != 0 {
						m[i][j] += c * o.g[i] * o.g[j]
					}
				}
				b[i] += c * o.g[i] * (o.x - o.h)
			}
		}
		next, err := solve3(m, b)
		if err != nil {
			return theta, 0, m, iter, err
		}
		delta := math.Max(math.Abs(next[0]-theta[0]), math.Max(math.Abs(next[1]-theta[1]), math.Abs(next[2]-theta[2])))
		theta = next
		if delta <= p.Tolerance {
			break
		}
	}
	if iter > p.MaxIterations {
		iter = p.MaxIterations
	}
	j := 0.0
	for _, o := range obs {
		pred := o.g[0]*theta[0] + o.g[1]*theta[1] + o.g[2]*theta[2] + o.h
		u := (o.x - pred) / o.sigma
		if o.kind == obsLower && u <= 0 {
			continue
		}
		j += o.w * huberRho(u, p.HuberDelta)
	}
	return theta, j, m, iter, nil
}

// solve3 solves the symmetric system by Gaussian elimination with partial
// pivoting. Exact zero couplings keep decoupled parameters bit-identical to
// their one-dimensional solution.
func solve3(m [3][3]float64, b [3]float64) ([3]float64, error) {
	a := m
	x := b
	idx := [3]int{0, 1, 2}
	for col := 0; col < 3; col++ {
		pivot := col
		for r := col + 1; r < 3; r++ {
			if math.Abs(a[r][col]) > math.Abs(a[pivot][col]) {
				pivot = r
			}
		}
		if math.Abs(a[pivot][col]) < 1e-12 {
			return [3]float64{}, errUnidentified
		}
		a[col], a[pivot] = a[pivot], a[col]
		x[col], x[pivot] = x[pivot], x[col]
		idx[col], idx[pivot] = idx[pivot], idx[col]
		for r := col + 1; r < 3; r++ {
			f := a[r][col] / a[col][col]
			if f == 0 {
				continue
			}
			for c := col; c < 3; c++ {
				a[r][c] -= f * a[col][c]
			}
			x[r] -= f * x[col]
		}
	}
	var out [3]float64
	for r := 2; r >= 0; r-- {
		s := x[r]
		for c := r + 1; c < 3; c++ {
			if a[r][c] != 0 {
				s -= a[r][c] * out[c]
			}
		}
		out[r] = s / a[r][r]
	}
	return out, nil
}

func invert3(m [3][3]float64) ([3][3]float64, bool) {
	var inv [3][3]float64
	for k := 0; k < 3; k++ {
		var e [3]float64
		e[k] = 1
		col, err := solve3(m, e)
		if err != nil {
			return inv, false
		}
		for i := 0; i < 3; i++ {
			inv[i][k] = col[i]
		}
	}
	return inv, true
}

// Fit is the only running-fitness estimator: the bounded joint robust fit of
// ln A, ln vT, ln v6 and bL (section 10.1). Priors stay in the objective as
// penalties at all times, so shape parameters shrink continuously.
func Fit(p Policy, evidence []fitObs) (FitResult, error) {
	var xs, ws []float64
	hasA, dependent := false, false
	for _, o := range evidence {
		if o.kind == obsA {
			xs = append(xs, o.x)
			ws = append(ws, o.w)
			hasA = true
		}
		if o.bLDependent {
			dependent = true
		}
	}
	scale := robustScale(p, xs, ws)
	obs := make([]fitObs, 0, len(evidence)+2)
	start := [3]float64{}
	identified := hasA
	for _, o := range evidence {
		if o.kind == obsA {
			o.sigma = scale
		}
		if o.kind == obsRace || o.kind == obsVT {
			identified = true
		}
		obs = append(obs, o)
	}
	if !identified {
		return FitResult{}, errUnidentified
	}
	if hasA {
		start[0] = weightedMedian(xs, ws)
	} else {
		// No daily observation: start from the first anchored performance.
		for _, o := range obs {
			if o.kind == obsRace || o.kind == obsVT {
				g, h := o.g, o.h
				if o.bLDependent {
					g, h = speedObs(p, o.duration, p.BL0)
				}
				_ = g
				start[0] = o.x - h
				break
			}
		}
	}
	obs = append(obs,
		fitObs{kind: obsPrior, x: 0, sigma: p.SigmaThreshold, w: 1, g: [3]float64{0, 1, 0}},
		fitObs{kind: obsPrior, x: 0, sigma: p.SigmaShape, w: 1, g: [3]float64{0, 0, 1}})
	blPrior := func(bL float64) float64 {
		return huberRho(math.Log(bL/p.BL0)/p.BLPriorSigma, p.HuberDelta)
	}
	best := FitResult{Objective: math.Inf(1)}
	try := func(bL float64) error {
		set := withBL(p, obs, bL)
		theta, j, m, iter, err := irls(p, set, start)
		if err != nil {
			return err
		}
		j += blPrior(bL)
		if j < best.Objective-1e-12 {
			cov, _ := invert3(m)
			best = FitResult{Theta: theta, BL: bL, Objective: j, Cov: cov, ScaleA: scale, Iter: iter}
		}
		return nil
	}
	if !dependent {
		if err := try(p.BL0); err != nil {
			return FitResult{}, err
		}
	} else {
		lo, hi := int(math.Round(p.BLDomain[0]/p.BLStep)), int(math.Round(p.BLDomain[1]/p.BLStep))
		for k := lo; k <= hi; k++ {
			if err := try(float64(k) / 1000); err != nil {
				return FitResult{}, err
			}
		}
	}
	best.A = math.Exp(best.Theta[0])
	lnvT := best.Theta[0] + cT(p) + best.Theta[1]
	lnv6 := lnvT + c6(p.BS0) + best.Theta[2]
	best.Curve = Curve{VT: math.Exp(lnvT), V6: math.Exp(lnv6), BL: best.BL}
	return best, nil
}

// impliedBS is the short-shape exponent implied by v6/vT.
func impliedBS(c Curve) float64 {
	return 1 / (1 + math.Log(c.V6/c.VT)/math.Log(360/TT))
}
