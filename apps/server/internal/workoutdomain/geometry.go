// Package workoutdomain holds version-neutral pure measurement primitives.
package workoutdomain

import "math"

// Haversine uses the existing RAI mean Earth radius and operation order.
func Haversine(lat1, lon1, lat2, lon2 float64) float64 {
	rad := math.Pi / 180
	dlat := (lat2 - lat1) * rad
	dlon := (lon2 - lon1) * rad
	x := math.Sin(dlat/2)*math.Sin(dlat/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dlon/2)*math.Sin(dlon/2)
	return 2 * 6371008.8 * math.Asin(math.Sqrt(math.Min(1, x)))
}

func Finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// CadenceProxy names the derived StepCount/time quantity honestly.
func CadenceProxy(count, seconds float64) float64 { return count / (seconds / 60) }
