package workoutdetail

import (
	"sort"

	wf "rai/server/internal/workoutfeatures"
)

// PaceAnalysis is a read-only review projection, never a training prescription.
type PaceAnalysis struct {
	SchemaVersion string         `json:"schemaVersion"`
	PolicyVersion string         `json:"policyVersion"`
	DefaultView   string         `json:"defaultView"`
	Reference     *PaceReference `json:"reference"`
	Personal      *ZoneSummary   `json:"personal"`
	Absolute      *ZoneSummary   `json:"absolute"`
}
type PaceReference struct {
	View                  string `json:"view"`
	Freshness             string `json:"freshness"`
	ManifestSHA256        string `json:"manifestSHA256"`
	PolicyHash            string `json:"policyHash"`
	Source                string `json:"source"`
	EvidenceCutoffAtUs    int64  `json:"evidenceCutoffAtUs"`
	LatestEvidenceAtUs    *int64 `json:"latestEvidenceAtUs"`
	IncludesTargetWorkout bool   `json:"includesTargetWorkout"`
	InputHash             string `json:"inputHash"`
	ProfileID             string `json:"profileID"`
	StackID               string `json:"stackID"`
	Purpose               string `json:"purpose"`
}

func NewPaceAnalysis() *PaceAnalysis {
	return &PaceAnalysis{SchemaVersion: "rai.workout_pace_analysis/1", PolicyVersion: "pace-zones/3", DefaultView: "summary", Personal: NewZoneSummary(), Absolute: NewZoneSummary()}
}

// BuildAbsolutePace uses the same supported signal as personal zones. Values
// are descriptive seconds/km, never physiological zone labels.
func BuildAbsolutePace(raw wf.RawWorkoutSnapshot) *ZoneSummary {
	z := NewZoneSummary()
	r := PaceZoneResult{PolicyVersion: "absolute-pace-support/1", Status: PaceZonesSpeedUnavailable, SpeedEvaluated: true, Bands: []PaceZoneBand{}, Exposure: []ZoneExposure{}}
	pause := wf.BuildPause(raw)
	if pause.Timing.State == wf.Ambiguous {
		r.Status = "timing_unavailable"
		AttachPaceZones(z, r)
		return z
	}
	for _, iv := range pause.Active {
		r.AnalyzableSeconds += float64(iv.End-iv.Start) / 1e6
	}
	signal := wf.BuildSignal(raw, pause, wf.Speed)
	for _, reason := range signal.Summary.Reasons {
		r.Codes = appendOnce(r.Codes, string(reason))
	}
	bounds := []float64{1000.0 / 420, 1000.0 / 360, 1000.0 / 300, 1000.0 / 240}
	names := []string{"over_7", "6_to_7", "5_to_6", "4_to_5", "under_4"}
	seconds, meters := make([]float64, 5), make([]float64, 5)
	for _, s := range signal.Spans {
		dt := float64(s.End-s.Start) / 1e6
		if s.Reconstructed {
			r.ReconstructedSeconds += dt
		} else {
			r.ObservedSeconds += dt
		}
		cuts := []float64{0, 1}
		if s.V0 != s.V1 {
			for _, b := range bounds {
				f := (b - s.V0) / (s.V1 - s.V0)
				if f > 0 && f < 1 {
					cuts = append(cuts, f)
				}
			}
		}
		sort.Float64s(cuts)
		for i := 1; i < len(cuts); i++ {
			a, b := cuts[i-1], cuts[i]
			v := s.V0 + (s.V1-s.V0)*(a+b)/2
			if v <= 0 {
				continue
			}
			j := sort.Search(len(bounds), func(j int) bool { return bounds[j] >= v })
			seconds[j] += dt * (b - a)
			meters[j] += v * dt * (b - a)
			r.ClassifiedSeconds += dt * (b - a)
		}
	}
	for i, name := range names {
		band := PaceZoneBand{Zone: name, Label: name, Support: "supported"}
		if i > 0 {
			band.SlowPaceSecondsPerKm = Ptr(1000 / bounds[i-1])
		}
		if i < 4 {
			band.FastPaceSecondsPerKm = Ptr(1000 / bounds[i])
		}
		r.Bands = append(r.Bands, band)
		fraction := 0.0
		if r.ClassifiedSeconds > 0 {
			fraction = seconds[i] / r.ClassifiedSeconds
		}
		r.Exposure = append(r.Exposure, ZoneExposure{Zone: name, DurationSeconds: round(seconds[i], 1), DistanceMeters: round(meters[i], 1), Fraction: round(fraction, 4)})
	}
	if r.ClassifiedSeconds > 0 {
		r.Status = PaceZonesAvailable
		r.Coverage = round(clamp(r.ClassifiedSeconds/max(r.AnalyzableSeconds, 1e-9), 0, 1), 4)
	} else {
		r.Exposure = []ZoneExposure{}
	}
	AttachPaceZones(z, r)
	return z
}
