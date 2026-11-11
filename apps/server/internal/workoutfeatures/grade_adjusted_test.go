package workoutfeatures

import (
	"math"
	"testing"
)

// A rolling route (+/-3% every 200 m) fails the flat-only gate but yields
// grade-adjusted windows whose flat-equivalent distance exceeds the raw one.
func TestRollingRouteQualifiesGradeAdjusted(t *testing.T) {
	s := synthetic(2400)
	for i := range s.Routes[0].Points {
		d := math.Mod(float64(i)*3, 400)
		alt := 100 + .03*d
		if d > 200 {
			alt = 100 + .03*(400-d)
		}
		s.Routes[0].Points[i].Altitude = ptr(alt)
	}
	f := compute(t, s)
	if f.Qualification.Performance.Status != "ineligible" || !hasReason(f.Qualification.Performance, RouteAccuracyLow) {
		t.Fatalf("flat gate should reject a rolling route: %+v", f.Qualification.Performance)
	}
	if f.Qualification.PerformanceGAP == nil || f.Qualification.PerformanceGAP.Status == "ineligible" {
		t.Fatalf("grade-adjusted gate: %+v", f.Qualification.PerformanceGAP)
	}
	found := false
	for _, c := range f.Windows {
		if c.Duration < 1200 {
			if c.GAP != nil {
				t.Fatal("grade-adjusted windows start at 20 min")
			}
			continue
		}
		if c.GAP == nil || c.GAP.Qualification.Status == "ineligible" || c.GAP.GAPDistance == nil {
			continue
		}
		found = true
		if *c.GAP.GAPDistance < c.GAP.Distance || *c.GAP.GAPDistance > c.GAP.Distance*1.05 {
			t.Fatalf("flat-equivalent %v vs raw %v", *c.GAP.GAPDistance, c.GAP.Distance)
		}
	}
	if !found {
		t.Fatal("no qualified grade-adjusted window")
	}
	// A steep route (12%) is still rejected.
	for i := range s.Routes[0].Points {
		s.Routes[0].Points[i].Altitude = ptr(100 + .12*math.Mod(float64(i)*3, 400))
	}
	f = compute(t, s)
	if f.Qualification.PerformanceGAP.Status != "ineligible" {
		t.Fatalf("steep route qualified: %+v", f.Qualification.PerformanceGAP)
	}
}
