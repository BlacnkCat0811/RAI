package workoutdetail

import (
	"math"
	"sort"

	wf "rai/server/internal/workoutfeatures"
)

const PaceZonePolicyV2 = "pace-zones/2"

// BuildSupportedPaceZones uses the foundation's sole source, pause, outlier and
// interpolation path. Spans are ephemeral; neither facts nor publications change.
func BuildSupportedPaceZones(raw wf.RawWorkoutSnapshot, profile *PaceZoneProfile) PaceZoneResult {
	out := BuildPaceZones(raw.Start, raw.End, float64(raw.End-raw.Start)/1e6, nil, nil, profile)
	out.PolicyVersion = PaceZonePolicyV2
	// The initializer has no speed input; its missing-speed diagnostic does not
	// describe the supported signal computed below.
	codes := out.Codes[:0]
	for _, code := range out.Codes {
		if code != "speed_intervals_unavailable" {
			codes = append(codes, code)
		}
	}
	out.Codes = codes
	pause := wf.BuildPause(raw)
	out.AnalyzableSeconds = 0
	if pause.Timing.State == wf.Ambiguous {
		out.Status = "timing_unavailable"
		out.Codes = appendOnce(out.Codes, "active_time_uncertain")
		return out
	}
	for _, iv := range pause.Active {
		out.AnalyzableSeconds += float64(iv.End-iv.Start) / 1e6
	}
	if profile == nil || profile.Expired || out.Status == PaceZonesAnchorConflict || len(out.Bands) != 6 {
		return out
	}
	for i := range out.Bands {
		if !PaceBandClassifiable(out.Bands[i].Support) {
			out.Bands[i].FastPaceSecondsPerKm, out.Bands[i].SlowPaceSecondsPerKm = nil, nil
		} else if out.Bands[i].Support != "supported" {
			out.Codes = appendOnce(out.Codes, "pace_zones_not_fully_supported")
		}
	}
	signal := wf.BuildSignal(raw, pause, wf.Speed)
	out.SpeedEvaluated = true
	for _, reason := range signal.Summary.Reasons {
		out.Codes = appendOnce(out.Codes, string(reason))
	}
	bounds, _ := paceBoundaries(profile.Zones)
	for i := 1; i < len(out.Bands); i++ {
		slow := profile.Zones.Zones[i].SlowSpeed
		if PaceBandClassifiable(out.Bands[i].Support) && (slow == nil || !valid(*slow) || math.Abs(*slow-bounds[i-1]) > 1e-9) {
			out.Status = PaceZonesAnchorConflict
			out.Codes = appendOnce(out.Codes, "pace_zone_boundaries_invalid")
			return out
		}
	}
	durations, distances := make([]float64, 6), make([]float64, 6)
	var supported float64
	for _, span := range signal.Spans {
		seconds := float64(span.End-span.Start) / 1e6
		if seconds <= 0 {
			continue
		}
		supported += seconds
		if span.Reconstructed {
			out.ReconstructedSeconds += seconds
		} else {
			out.ObservedSeconds += seconds
		}
		cuts := []float64{0, 1}
		if span.V1 != span.V0 {
			for _, bound := range bounds {
				fraction := (bound - span.V0) / (span.V1 - span.V0)
				if fraction > 0 && fraction < 1 {
					cuts = append(cuts, fraction)
				}
			}
		}
		sort.Float64s(cuts)
		for i := 1; i < len(cuts); i++ {
			a, b := cuts[i-1], cuts[i]
			speed := span.V0 + (span.V1-span.V0)*(a+b)/2
			zone := sort.Search(len(bounds), func(j int) bool { return bounds[j] > speed })
			if !PaceBandClassifiable(out.Bands[zone].Support) {
				out.Codes = appendOnce(out.Codes, "pace_zone_evidence_insufficient")
				continue
			}
			dt := seconds * (b - a)
			durations[zone] += dt
			distances[zone] += speed * dt // exact integral of the linear span
			out.ClassifiedSeconds += dt
		}
	}
	if out.ClassifiedSeconds <= 0 {
		out.Status = PaceZonesSpeedUnavailable
		if supported > 0 {
			out.Status = "zone_evidence_insufficient"
		}
		return out
	}
	out.Status = PaceZonesAvailable
	out.Exposure = []ZoneExposure{}
	out.Coverage = round(clamp(out.ClassifiedSeconds/math.Max(out.AnalyzableSeconds, 1e-9), 0, 1), 4)
	for i, band := range out.Bands {
		out.Exposure = append(out.Exposure, ZoneExposure{Zone: band.Zone, DurationSeconds: round(durations[i], 1), DistanceMeters: round(distances[i], 1), Fraction: round(durations[i]/out.ClassifiedSeconds, 4)})
	}
	return out
}

// Prior-based and limited boundaries remain descriptive, explicitly estimated.
// Missing evidence and conflicts must never be rendered as zero exposure.
func PaceBandClassifiable(support string) bool {
	switch support {
	case "supported", "estimated", "limited", "prior_based":
		return true
	}
	return false
}
