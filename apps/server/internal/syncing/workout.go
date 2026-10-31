package syncing

import (
	"bytes"
	"encoding/json"
	"math"
)

const (
	WorkoutType          = "HKWorkoutTypeIdentifier"
	MaxWorkoutStatistics = 128
	MaxWorkoutEvents     = 256
	MaxWorkoutActivities = 128
	MaxWorkoutDetails    = 2000 // total activity/statistic/event rows across the input batch
)

func ValidateDetailBudget(events []Event) error {
	total := 0
	for _, event := range events {
		if event.Workout != nil {
			total += len(event.Workout.Statistics) + len(event.Workout.Events) + len(event.Workout.Activities)
			for _, activity := range event.Workout.Activities {
				total += len(activity.Statistics) + len(activity.Events)
			}
			if total > MaxWorkoutDetails {
				return Fail(413, "batch_too_large")
			}
		}
	}
	return nil
}

// Workout supports the v1 fields plus bounded v2 activities. Routes, series,
// zones and explicit sample associations require separate future contracts.
// A producer must retain unsupported source fields locally, never drop them.
type Workout struct {
	ActivityType      *int64             `json:"activityType"`
	DurationUs        *int64             `json:"durationUs"`
	TotalDistance     *Quantity          `json:"totalDistance,omitempty"`
	TotalEnergyBurned *Quantity          `json:"totalEnergyBurned,omitempty"`
	Statistics        []WorkoutStatistic `json:"statistics,omitempty"`
	Events            []WorkoutEvent     `json:"events,omitempty"`
	Activities        []WorkoutActivity  `json:"activities,omitempty"`
}

// Activity order and UUIDs come from the source. An absent endUs stays absent;
// the parent end is only a validation bound, never a synthesized source value.
type WorkoutActivity struct {
	UUID          string                `json:"uuid"`
	StartUs       *int64                `json:"startUs"`
	EndUs         *int64                `json:"endUs,omitempty"`
	DurationUs    *int64                `json:"durationUs"`
	Configuration *WorkoutConfiguration `json:"configuration"`
	Metadata      map[string]any        `json:"metadata,omitempty"`
	Statistics    []WorkoutStatistic    `json:"statistics,omitempty"`
	Events        []WorkoutEvent        `json:"events,omitempty"`
}

// Preserve raw enum identifiers, including zero for unspecified locations.
// Future source configuration fields must not be silently discarded.
type WorkoutConfiguration struct {
	ActivityType         *int64    `json:"activityType"`
	LocationType         *int64    `json:"locationType"`
	SwimmingLocationType *int64    `json:"swimmingLocationType"`
	LapLength            *Quantity `json:"lapLength,omitempty"`
}

// Statistics are sorted by type in the wire format. Their fields are optional
// because HealthKit can provide a sum, an average, or other aggregates.
type WorkoutStatistic struct {
	Type    string    `json:"type"`
	StartUs *int64    `json:"startUs"`
	EndUs   *int64    `json:"endUs"`
	Sum     *Quantity `json:"sum,omitempty"`
	Average *Quantity `json:"average,omitempty"`
	Minimum *Quantity `json:"minimum,omitempty"`
	Maximum *Quantity `json:"maximum,omitempty"`
}

// Preserve source event order, including events sharing a timestamp. Event
// types use HealthKit's raw integer identifier, without server reinterpretation.
type WorkoutEvent struct {
	Type     *int64         `json:"type"`
	StartUs  *int64         `json:"startUs"`
	EndUs    *int64         `json:"endUs"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

func validQuantity(q *Quantity, unit string, nonnegative bool) bool {
	return q != nil && q.Unit == unit && !math.IsNaN(q.Value) && !math.IsInf(q.Value, 0) && (!nonnegative || q.Value >= 0)
}

func (w Workout) Validate(start, end int64) error {
	bad := func() error { return Fail(422, "unsupported_payload") }
	if w.ActivityType == nil || *w.ActivityType < 1 || *w.ActivityType > math.MaxInt32 ||
		w.DurationUs == nil || *w.DurationUs < 0 || *w.DurationUs > end-start ||
		len(w.Activities) > MaxWorkoutActivities {
		return bad()
	}
	if (w.TotalDistance != nil && !validQuantity(w.TotalDistance, "m", true)) ||
		(w.TotalEnergyBurned != nil && !validQuantity(w.TotalEnergyBurned, "kcal", true)) {
		return bad()
	}
	if err := validateWorkoutDetails(w.Statistics, w.Events, start, end); err != nil {
		return err
	}
	seen := make(map[string]bool, len(w.Activities))
	for _, activity := range w.Activities {
		if seen[activity.UUID] {
			return bad()
		}
		seen[activity.UUID] = true
		if err := activity.Validate(start, end); err != nil {
			return err
		}
	}
	return nil
}

// Validate is shared by inline Workout v2 activities and the durable detail
// package path. Parent bounds are the only bound for an absent source endUs.
func (activity WorkoutActivity) Validate(start, end int64) error {
	bad := func() error { return Fail(422, "unsupported_payload") }
	if !ValidID(activity.UUID) || activity.StartUs == nil ||
		*activity.StartUs < start || *activity.StartUs > end {
		return bad()
	}
	bound := end
	if activity.EndUs != nil {
		bound = *activity.EndUs
	}
	if bound < *activity.StartUs || bound > end || activity.DurationUs == nil ||
		*activity.DurationUs < 0 || *activity.DurationUs > bound-*activity.StartUs {
		return bad()
	}
	c := activity.Configuration
	if c == nil || c.ActivityType == nil || *c.ActivityType < 1 || *c.ActivityType > math.MaxInt32 ||
		c.LocationType == nil || *c.LocationType < 0 || *c.LocationType > math.MaxInt32 ||
		c.SwimmingLocationType == nil || *c.SwimmingLocationType < 0 || *c.SwimmingLocationType > math.MaxInt32 ||
		(c.LapLength != nil && !validQuantity(c.LapLength, "m", true)) {
		return bad()
	}
	if err := validateWorkoutStatistics(activity.Statistics, *activity.StartUs, bound); err != nil {
		return err
	}
	// HealthKit attaches the parent workout's segment events to each activity;
	// a segment can legitimately span across child activity boundaries while
	// still belonging to the parent workout. Bound activity events by the
	// parent window, never by the narrower activity interval.
	if err := validateWorkoutEvents(activity.Events, start, end); err != nil {
		return err
	}
	return nil
}

func validateWorkoutDetails(statistics []WorkoutStatistic, events []WorkoutEvent, start, end int64) error {
	if err := validateWorkoutStatistics(statistics, start, end); err != nil {
		return err
	}
	return validateWorkoutEvents(events, start, end)
}

func validateWorkoutStatistics(statistics []WorkoutStatistic, start, end int64) error {
	bad := func() error { return Fail(422, "unsupported_payload") }
	if len(statistics) > MaxWorkoutStatistics {
		return bad()
	}
	previous := ""
	for _, stat := range statistics {
		unit, ok := Units[stat.Type]
		if !ok || unit == "" || stat.Type <= previous || stat.StartUs == nil || stat.EndUs == nil ||
			*stat.StartUs < start || *stat.EndUs > end || *stat.StartUs > *stat.EndUs {
			return bad()
		}
		previous = stat.Type
		found := false
		for _, q := range []*Quantity{stat.Sum, stat.Average, stat.Minimum, stat.Maximum} {
			if q != nil {
				found = true
				if !validQuantity(q, unit, false) {
					return bad()
				}
			}
		}
		if !found {
			return bad()
		}
	}
	return nil
}

func validateWorkoutEvents(events []WorkoutEvent, start, end int64) error {
	bad := func() error { return Fail(422, "unsupported_payload") }
	if len(events) > MaxWorkoutEvents {
		return bad()
	}
	for _, event := range events {
		if event.Type == nil || *event.Type < 1 || *event.Type > math.MaxInt32 || event.StartUs == nil || event.EndUs == nil ||
			*event.StartUs < start || *event.EndUs > end || *event.StartUs > *event.EndUs {
			return bad()
		}
	}
	return nil
}

// DecodeJSON already rejects unknown and duplicate fields recursively. This
// additionally rejects explicit null and missing nested quantity values, which
// encoding/json would otherwise turn into valid-looking zero values.
func validateWorkoutJSON(raw json.RawMessage) error {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return Fail(422, "unsupported_payload")
	}
	for _, raw := range object {
		if bytes.Equal(raw, []byte("null")) {
			return Fail(422, "unsupported_payload")
		}
	}
	for _, key := range []string{"totalDistance", "totalEnergyBurned", "sum", "average", "minimum", "maximum", "lapLength"} {
		if raw, ok := object[key]; ok {
			var q map[string]json.RawMessage
			if json.Unmarshal(raw, &q) != nil || q["value"] == nil || bytes.Equal(q["value"], []byte("null")) {
				return Fail(422, "unsupported_payload")
			}
		}
	}
	for _, key := range []string{"statistics", "events", "activities"} {
		if raw, ok := object[key]; ok {
			var items []json.RawMessage
			if json.Unmarshal(raw, &items) != nil {
				return Fail(422, "unsupported_payload")
			}
			for _, item := range items {
				if err := validateWorkoutJSON(item); err != nil {
					return err
				}
			}
		}
	}
	if raw, ok := object["configuration"]; ok {
		if err := validateWorkoutJSON(raw); err != nil {
			return err
		}
	}
	return nil
}
