package workoutdetail

import (
	"fmt"
	"math"
	"sort"
)

// DistanceDistribution is independent of the published training model. It
// integrates the original quantity intervals, never the display-sampled series.
// Unknown distance includes gaps, ambiguous observations and missing HRR inputs.
type DistanceDistribution struct {
	PolicyVersion          string         `json:"policyVersion"`
	TotalDistanceMeters    float64        `json:"totalDistanceMeters"`
	Pace                   []DistanceZone `json:"pace"`
	HeartRate              []DistanceZone `json:"heartRate"`
	PaceUnknownMeters      float64        `json:"paceUnknownMeters"`
	HeartRateUnknownMeters float64        `json:"heartRateUnknownMeters"`
	HeartRateProfileID     string         `json:"heartRateProfileID"`
	ReasonCodes            []string       `json:"reasonCodes"`
}

type DistanceZone struct {
	Zone           string  `json:"zone"`
	DistanceMeters float64 `json:"distanceMeters"`
}

// BuildDistanceDistribution uses half-open intervals. Speed samples are
// constant over their stated interval; point samples are not extrapolated.
// Identical overlaps count once and conflicting overlaps count as unknown.
// Only excess integrated distance is scaled down to the official total: gaps
// never cause measured distance to be scaled up. HR uses that same scale.
func BuildDistanceDistribution(startUs, endUs int64, total *float64, obs []Observation, events []EventBoundary, restHR, maxHR float64) *DistanceDistribution {
	if total == nil || !valid(*total) || *total < 0 || endUs <= startUs {
		return nil
	}
	out := &DistanceDistribution{PolicyVersion: "distance-zones/v1", TotalDistanceMeters: *total,
		Pace:        []DistanceZone{{Zone: "under_4"}, {Zone: "4_to_5"}, {Zone: "5_to_6"}, {Zone: "6_to_7"}, {Zone: "over_7"}},
		HeartRate:   []DistanceZone{{Zone: "recovery"}, {Zone: "aerobic_base"}, {Zone: "aerobic_power"}, {Zone: "lactate_threshold"}, {Zone: "anaerobic"}},
		ReasonCodes: []string{},
	}
	hrValid := valid(restHR) && valid(maxHR) && restHR > 0 && maxHR > restHR
	if hrValid {
		out.HeartRateProfileID = fmt.Sprintf("hrr:%g:%g", restHR, maxHR)
	} else {
		out.ReasonCodes = append(out.ReasonCodes, "historical_hrr_unavailable")
	}
	type edge struct {
		at    int64
		kind  int
		value float64
		delta int
	}
	edges := make([]edge, 0, len(obs)*2)
	add := func(a, b int64, kind int, value float64) {
		a, b = max64(a, startUs), min64(b, endUs)
		if b > a {
			edges = append(edges, edge{a, kind, value, 1}, edge{b, kind, value, -1})
		}
	}
	for _, o := range obs {
		if !valid(o.Value) || o.Value < 0 {
			continue
		}
		switch o.Type {
		case RunningSpeed:
			if v, ok := speedToMetersPerSecond(o.Value, o.Unit); ok && valid(v) && v >= 0 {
				add(o.StartUs, o.EndUs, 0, v)
			}
		case HeartRate:
			if o.Value > 0 && (o.Unit == "count/min" || o.Unit == "bpm") {
				add(o.StartUs, o.EndUs, 1, o.Value)
			}
		}
	}
	for _, p := range pauseIntervals(events, endUs) {
		add(p[0], p[1], 2, 1)
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].at < edges[j].at })
	active := [3]map[float64]int{{}, {}, {}}
	previous := startUs
	var speedConflict, heartConflict bool
	for i := 0; i < len(edges); {
		at := edges[i].at
		if at > previous && len(active[2]) == 0 {
			speedConflict = speedConflict || len(active[0]) > 1
			heartConflict = heartConflict || len(active[1]) > 1
			if len(active[0]) == 1 {
				for speed := range active[0] {
					if speed == 0 {
						continue
					}
					distance := speed * (float64(at-previous) / 1e6)
					if !valid(distance) {
						return nil // unavailable, never encode an overflow as zero
					}
					zone := 0
					for _, boundary := range []float64{240, 300, 360, 420} {
						if speed <= 1000/boundary {
							zone++
						}
					}
					out.Pace[zone].DistanceMeters += distance
					if !valid(out.Pace[zone].DistanceMeters) {
						return nil
					}
					if hrValid && len(active[1]) == 1 {
						for hr := range active[1] {
							ratio := (hr - restHR) / (maxHR - restHR)
							hz := 0
							for _, boundary := range []float64{.6, .7, .8, .9} {
								if ratio >= boundary {
									hz++
								}
							}
							out.HeartRate[hz].DistanceMeters += distance
						}
					}
				}
			}
		}
		for i < len(edges) && edges[i].at == at {
			e := edges[i]
			active[e.kind][e.value] += e.delta
			if active[e.kind][e.value] == 0 {
				delete(active[e.kind], e.value)
			}
			i++
		}
		previous = at
	}
	if speedConflict {
		out.ReasonCodes = append(out.ReasonCodes, "ambiguous_speed_intervals_excluded")
	}
	if heartConflict {
		out.ReasonCodes = append(out.ReasonCodes, "ambiguous_heart_rate_intervals_excluded")
	}
	var raw float64
	for _, z := range out.Pace {
		raw += z.DistanceMeters
	}
	if !valid(raw) {
		return nil
	}
	scale := 1.0
	if raw > *total && raw > 0 {
		scale = *total / raw
		out.ReasonCodes = append(out.ReasonCodes, "speed_distance_exceeds_workout_total_scaled_down")
	}
	unknown := func(zones []DistanceZone) float64 {
		remaining := *total
		for i := range zones {
			zones[i].DistanceMeters = math.Min(remaining, zones[i].DistanceMeters*scale)
			remaining = math.Max(0, remaining-zones[i].DistanceMeters)
		}
		return remaining
	}
	out.PaceUnknownMeters, out.HeartRateUnknownMeters = unknown(out.Pace), unknown(out.HeartRate)
	return out
}

// WorkoutDistanceItem is one running Workout of a statistics window:
// available (with its distribution), unavailable (no distance to split) or
// failed (its own facts could not be read).
type WorkoutDistanceItem struct {
	WorkoutID    string                `json:"workoutID"`
	Status       string                `json:"status"`
	Distribution *DistanceDistribution `json:"distanceDistribution"`
}
