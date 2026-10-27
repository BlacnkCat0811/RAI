package postgres

import (
	"math"
	"testing"

	"rai/server/internal/syncing"
)

func TestRoutePointValidation(t *testing.T) {
	valid := syncing.RoutePoint{RecordedAtUs: 1, Latitude: 90, Longitude: -180}
	if err := valid.Validate(); err != nil {
		t.Fatal("valid boundary rejected", err)
	}
	badAccuracy := -1.0
	badCourse := 360.0
	for name, p := range map[string]syncing.RoutePoint{
		"negative sequence": {Sequence: -1, RecordedAtUs: 1},
		"latitude":          {RecordedAtUs: 1, Latitude: 90.1},
		"longitude":         {RecordedAtUs: 1, Longitude: -180.1},
		"nan":               {RecordedAtUs: 1, Latitude: math.NaN()},
		"accuracy":          {RecordedAtUs: 1, HorizontalAccuracy: &badAccuracy},
		"course":            {RecordedAtUs: 1, Course: &badCourse},
	} {
		if p.Validate() == nil {
			t.Errorf("%s accepted", name)
		}
	}
}
