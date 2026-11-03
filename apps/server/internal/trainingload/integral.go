package trainingload

import "math"

// Intensity defines one model AU per minute at HRR .65. It is not a
// physiological calibration or a conversion to any other vendor/model unit.
func Intensity(r float64) float64 {
	if r <= 0 {
		return 0
	}
	return r / .65 * math.Exp(1.92*(r-.65))
}

// LinearIntegral returns AU for an exactly linear HRR segment. The centered
// analytic formula avoids subtracting near-equal antiderivatives. r<=0 has
// zero intensity, including a linear crossing of zero. r>1 is a caller error.
func LinearIntegral(a, b, seconds float64) float64 {
	if seconds <= 0 || a <= 0 && b <= 0 {
		return 0
	}
	if a < 0 {
		seconds *= b / (b - a)
		a = 0
	}
	if b < 0 {
		seconds *= a / (a - b)
		b = 0
	}
	m, delta := (a+b)/2, (b-a)/2
	z := 1.92 * delta
	var s, t float64
	if math.Abs(z) < .01 {
		z2 := z * z
		s = 1 + z2*(1.0/6+z2*(1.0/120+z2*(1.0/5040+z2/362880)))
		t = z * (1.0/3 + z2*(1.0/30+z2*(1.0/840+z2*(1.0/45360+z2/3991680))))
	} else {
		s = math.Sinh(z) / z
		t = (z*math.Cosh(z) - math.Sinh(z)) / (z * z)
	}
	return seconds / 60 * math.Exp(1.92*(m-.65)) / .65 * (m*s + delta*t)
}
