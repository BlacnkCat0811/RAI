package workoutdetail

import (
	"sort"

	wf "rai/server/internal/workoutfeatures"
)

// PaceAnalysisResponse is GET /v2/workouts/{id}/pace-analysis. The review is
// computed at read time by replaying the running-fitness state, so it is kept
// apart from the detail facts and loaded after them.
type PaceAnalysisResponse struct {
	WorkoutID    string        `json:"workoutID"`
	PaceAnalysis *PaceAnalysis `json:"paceAnalysis"`
}

// Pace reviews precomputed by the day release (artifact workout_pace_reviews):
// the review of every Workout that starts on the release's local day, keyed by
// Workout ID. Published atomically with the day's other products, so a clean
// day publication carries reviews computed from the current facts.
const (
	PaceReviewsArtifactKey = "workout_pace_reviews"
	// Its own publication family: not one of the product slots that the
	// release readiness check counts.
	PaceReviewsFamily   = "model_pace_review_v1/workout_pace_reviews"
	PaceReviewsSchema   = "rai.workout_pace_reviews/1"
	PaceReviewAlgorithm = "rai.workout_pace_review"
	PaceReviewVersion   = "v1"
)

type PaceReviews struct {
	SchemaVersion string                   `json:"schemaVersion"`
	DatasetID     string                   `json:"datasetID"`
	Generation    int64                    `json:"generation"`
	Date          string                   `json:"date"`
	StackID       string                   `json:"stackID"`
	Reviews       map[string]*PaceAnalysis `json:"reviews"`
}

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
