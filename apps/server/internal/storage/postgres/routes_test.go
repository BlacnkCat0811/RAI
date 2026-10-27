package postgres

import (
	"math"
	"testing"
	"time"
)

func TestRoutePointValidation(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	valid := RoutePoint{RecordedAt: now, Latitude: 90, Longitude: -180}
	if !validRoutePoint(valid) {
		t.Fatal("valid boundary rejected")
	}
	badAccuracy := -1.0
	badCourse := 360.0
	for name, p := range map[string]RoutePoint{
		"missing time":      {},
		"negative sequence": {RecordedAt: now, Sequence: -1},
		"latitude":          {RecordedAt: now, Latitude: 90.1},
		"longitude":         {RecordedAt: now, Longitude: -180.1},
		"nan":               {RecordedAt: now, Latitude: math.NaN()},
		"accuracy":          {RecordedAt: now, HorizontalAccuracy: &badAccuracy},
		"course":            {RecordedAt: now, Course: &badCourse},
	} {
		if validRoutePoint(p) {
			t.Errorf("%s accepted", name)
		}
	}
}
