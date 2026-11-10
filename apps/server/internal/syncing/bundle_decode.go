package syncing

import (
	"fmt"
	"math"
)

func ValidateWorkoutBundle(bundle WorkoutBundle) error {
	if err := bundleValues(bundle); err != nil {
		return err
	}
	if err := bundle.Validate(); err != nil {
		if failure, ok := err.(*Error); ok {
			if failure.Field == "" {
				failure.Field = "$"
				failure.Reason = "invalid_value_or_membership"
			}
		}
		return err
	}
	return nil
}

func bundleField(path, reason string) *Error {
	if path == "" {
		path = "$"
	}
	return &Error{Status: 422, Code: "unsupported_payload", Field: path, Reason: reason}
}

func bundleValues(b WorkoutBundle) error {
	id := func(v, path string) error {
		if !ValidID(v) {
			return bundleField(path, "invalid_uuid")
		}
		return nil
	}
	for _, item := range []struct{ v, path string }{{b.DatasetID, "datasetID"}, {b.DatasetEpoch, "datasetEpoch"}, {b.CollectorEpoch, "collectorEpoch"}, {b.WorkoutID, "workoutID"}} {
		if err := id(item.v, item.path); err != nil {
			return err
		}
	}
	if b.ProtocolVersion < 3 || b.ProtocolVersion > 6 {
		return bundleField("protocolVersion", "unsupported_protocol")
	}
	events := append([]Event{b.Workout}, b.Samples...)
	for i, e := range events {
		path := "workout"
		if i > 0 {
			path = fmt.Sprintf("samples[%d]", i-1)
		}
		if err := id(e.UUID, bundlePath(path, "uuid")); err != nil {
			return err
		}
		if e.StartUs != nil && e.EndUs != nil && (*e.StartUs > *e.EndUs || *e.StartUs < minBundleTimeUs || *e.EndUs > maxBundleTimeUs) {
			return bundleField(path+".endUs", "invalid_time_range")
		}
		if e.Source != nil && (len(e.Source.BundleID) == 0 || len(e.Source.BundleID) > 255) {
			return bundleField(path+".source.bundleID", "invalid_source")
		}
		if e.Quantity != nil {
			if e.Quantity.Unit != Units[e.Type] {
				return bundleField(path+".quantity.unit", "unsupported_unit")
			}
			if math.IsNaN(e.Quantity.Value) || math.IsInf(e.Quantity.Value, 0) {
				return bundleField(path+".quantity.value", "nonfinite")
			}
		}
	}
	for i, s := range b.Series {
		for j, p := range s.Points {
			path := fmt.Sprintf("series[%d].points[%d]", i, j)
			if p.Unit != Units[s.SampleType] {
				return bundleField(path+".unit", "unsupported_unit")
			}
			if p.StartUs > p.EndUs {
				return bundleField(path+".endUs", "invalid_time_range")
			}
		}
	}
	for i, r := range b.Routes {
		for j, p := range r.Points {
			path := fmt.Sprintf("routes[%d].points[%d]", i, j)
			if p.Latitude < -90 || p.Latitude > 90 {
				return bundleField(path+".latitude", "out_of_range")
			}
			if p.Longitude < -180 || p.Longitude > 180 {
				return bundleField(path+".longitude", "out_of_range")
			}
			if err := p.ToRoutePoint(0).Validate(); err != nil {
				return bundleField(path, "invalid_route_point")
			}
		}
	}
	return nil
}

func bundlePath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}
