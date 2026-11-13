package workoutdetail

import (
	"sort"

	rf "rai/server/internal/runfitness"
)

// Pace-zone statuses on ZoneSummary.PaceZoneStatus.
const (
	PaceZonesAvailable        = "available"
	PaceZonesProfileMissing   = "threshold_pace_unavailable" // no published pre-run running-fitness zones
	PaceZonesAnchorConflict   = "zone_anchor_conflict"       // published boundaries missing or not strictly increasing
	PaceZonesSpeedUnavailable = "speed_unavailable"          // zones exist, but no usable speed intervals
)

// PaceZoneProfile is the running-fitness pace-zone set published for the local
// day before the Workout started. The run itself never shapes the zones it is
// classified into.
type PaceZoneProfile struct {
	Date  string
	Zones rf.Zones
}

// PaceZoneBand echoes the boundaries used for classification so the client can
// show the actual pace ranges; nil bounds are open ends.
type PaceZoneBand struct {
	Zone                 string   `json:"zone"`
	Label                string   `json:"label"`
	FastPaceSecondsPerKm *float64 `json:"fastPaceSecondsPerKm"`
	SlowPaceSecondsPerKm *float64 `json:"slowPaceSecondsPerKm"`
	Support              string   `json:"support"`
}

// PaceZoneResult is attached to a ZoneSummary by AttachPaceZones.
type PaceZoneResult struct {
	Status      string
	ProfileDate string
	Coverage    float64
	Bands       []PaceZoneBand
	Exposure    []ZoneExposure
	Codes       []string
}

// paceBoundaries returns the five internal speed boundaries (m/s, ascending)
// between Z1..Z6: Z1 is open on the slow side, Z6 open on the fast side. Any
// missing or non-increasing boundary is a conflict and is never sorted into
// a different meaning.
func paceBoundaries(z rf.Zones) ([]float64, bool) {
	if len(z.Zones) != 6 {
		return nil, false
	}
	out := make([]float64, 0, 5)
	for i := 0; i < 5; i++ {
		fast := z.Zones[i].FastSpeed
		if fast == nil || !valid(*fast) || *fast <= 0 {
			return nil, false
		}
		if len(out) > 0 && *fast <= out[len(out)-1] {
			return nil, false
		}
		out = append(out, *fast)
	}
	return out, true
}

// BuildPaceZones integrates RunningSpeed intervals over the elapsed window,
// excluding pauses, stationary readings and conflicting overlaps (identical
// overlaps count once). Point samples are not extrapolated. Speeds are raw,
// not grade-adjusted.
func BuildPaceZones(startUs, endUs int64, elapsedS float64, obs []Observation, events []EventBoundary, profile *PaceZoneProfile) PaceZoneResult {
	out := PaceZoneResult{Status: PaceZonesProfileMissing, Bands: []PaceZoneBand{}, Exposure: []ZoneExposure{}, Codes: []string{}}
	if profile == nil {
		out.Codes = append(out.Codes, "pace_zone_profile_unavailable")
		return out
	}
	out.ProfileDate = profile.Date
	bounds, ok := paceBoundaries(profile.Zones)
	if !ok {
		allMissing := true
		for _, z := range profile.Zones.Zones {
			if z.FastSpeed != nil || z.SlowSpeed != nil {
				allMissing = false
			}
		}
		if allMissing {
			out.Codes = append(out.Codes, "pace_zone_profile_unavailable")
			return out
		}
		out.Status = PaceZonesAnchorConflict
		out.Codes = append(out.Codes, "pace_zone_boundaries_invalid")
		return out
	}
	for i, z := range profile.Zones.Zones {
		band := PaceZoneBand{Zone: z.Name, Label: z.Label, Support: z.Support}
		if i > 0 {
			band.SlowPaceSecondsPerKm = Ptr(round(rf.Pace(bounds[i-1]), 1))
		}
		if i < 5 {
			band.FastPaceSecondsPerKm = Ptr(round(rf.Pace(bounds[i]), 1))
		}
		out.Bands = append(out.Bands, band)
		if i >= 2 && i <= 3 && z.Support != "supported" {
			out.Codes = appendOnce(out.Codes, "pace_zones_not_fully_supported")
		}
	}
	out.Codes = append(out.Codes, "pace_zones_from_pre_run_fitness", "pace_zones_raw_speed_not_grade_adjusted")
	if elapsedS <= 0 || endUs <= startUs {
		out.Status = PaceZonesSpeedUnavailable
		return out
	}

	type edge struct {
		at    int64
		kind  int // 0 speed, 1 pause
		value float64
		delta int
	}
	edges := []edge{}
	add := func(a, b int64, kind int, value float64) {
		a, b = max64(a, startUs), min64(b, endUs)
		if b > a {
			edges = append(edges, edge{a, kind, value, 1}, edge{b, kind, value, -1})
		}
	}
	for _, o := range obs {
		if o.Type != RunningSpeed || !valid(o.Value) || o.Value < 0 {
			continue
		}
		if v, ok := speedToMetersPerSecond(o.Value, o.Unit); ok && valid(v) && v >= 0 {
			add(o.StartUs, o.EndUs, 0, v)
		}
	}
	for _, p := range pauseIntervals(events, endUs) {
		add(p[0], p[1], 1, 1)
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i].at < edges[j].at })

	duration := make([]float64, 6)
	distance := make([]float64, 6)
	active := [2]map[float64]int{{}, {}}
	previous := startUs
	var covered, paused float64
	var conflict, stationary bool
	for i := 0; i < len(edges); {
		at := edges[i].at
		if at > previous && len(active[1]) > 0 {
			paused += float64(at-previous) / 1e6
		}
		if at > previous && len(active[1]) == 0 {
			conflict = conflict || len(active[0]) > 1
			if len(active[0]) == 1 {
				for speed := range active[0] {
					if speed == 0 {
						stationary = true
						continue
					}
					d := float64(at-previous) / 1e6
					zone := 0
					for _, b := range bounds {
						if speed >= b {
							zone++
						}
					}
					duration[zone] += d
					distance[zone] += speed * d
					covered += d
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
	if conflict {
		out.Codes = append(out.Codes, "ambiguous_speed_intervals_excluded")
	}
	if stationary {
		out.Codes = append(out.Codes, "stationary_speed_excluded")
	}
	if covered <= 0 || !valid(covered) {
		out.Status = PaceZonesSpeedUnavailable
		out.Codes = append(out.Codes, "speed_intervals_unavailable")
		return out
	}
	out.Status = PaceZonesAvailable
	// Coverage is measured against moving time: paused spans can never carry
	// pace, so they do not lower it. Overlapping pause channels count once.
	moving := elapsedS - paused
	if moving <= 0 || !valid(moving) {
		moving = elapsedS
	}
	out.Coverage = round(clamp(covered/moving, 0, 1), 4)
	for i, band := range out.Bands {
		out.Exposure = append(out.Exposure, ZoneExposure{Zone: band.Zone, DurationSeconds: round(duration[i], 1), DistanceMeters: round(distance[i], 1), Fraction: round(duration[i]/covered, 4)})
	}
	return out
}

// NewZoneSummary is the empty, unclassified summary used when only one of the
// heart-rate or pace dimensions is available.
func NewZoneSummary() *ZoneSummary {
	return &ZoneSummary{
		StimulusType:           "unclassified",
		SecondaryStimulusTypes: []string{},
		HeartRateExposure:      []ZoneExposure{},
		PaceExposure:           []ZoneExposure{},
		PaceZones:              []PaceZoneBand{},
		StimulusScores:         []StimulusScore{},
		ExplanationCodes:       []string{"stimulus_requires_matched_plan"},
		PaceZoneStatus:         PaceZonesProfileMissing,
	}
}

// AttachPaceZones copies a pace result onto the summary. Stimulus scoring stays
// heart-rate based; pace exposure is descriptive only.
func AttachPaceZones(z *ZoneSummary, r PaceZoneResult) {
	if z == nil {
		return
	}
	z.PaceZoneStatus = r.Status
	z.PaceZoneProfileDate = r.ProfileDate
	z.PaceCoverage = r.Coverage
	z.PaceExposure = r.Exposure
	z.PaceZones = r.Bands
	for _, c := range r.Codes {
		z.ExplanationCodes = appendOnce(z.ExplanationCodes, c)
	}
}

func appendOnce(xs []string, s string) []string {
	for _, x := range xs {
		if x == s {
			return xs
		}
	}
	return append(xs, s)
}

// pauseIntervals returns paused spans. Pause and motion-pause are independent
// channels; a resume for one does not clear the other. A missing resume
// conservatively excludes the tail up to endUs.
func pauseIntervals(events []EventBoundary, endUs int64) [][2]int64 {
	out := [][2]int64{}
	sorted := append([]EventBoundary(nil), events...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].StartUs == sorted[j].StartUs {
			return sorted[i].Type < sorted[j].Type
		}
		return sorted[i].StartUs < sorted[j].StartUs
	})
	for _, pair := range [][2]int64{{1, 2}, {5, 6}} {
		var paused bool
		var at int64
		for _, e := range sorted {
			if e.Type == pair[0] {
				if e.EndUs > e.StartUs {
					out = append(out, [2]int64{e.StartUs, e.EndUs})
					continue
				}
				if !paused {
					paused, at = true, e.StartUs
				}
			} else if e.Type == pair[1] && paused {
				out = append(out, [2]int64{at, e.StartUs})
				paused = false
			}
		}
		if paused {
			out = append(out, [2]int64{at, endUs})
		}
	}
	return out
}
