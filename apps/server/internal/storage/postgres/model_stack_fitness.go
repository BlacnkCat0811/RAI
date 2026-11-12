package postgres

import (
	"encoding/json"
	"math"
	"strings"
	"time"

	"rai/server/internal/hrref"
	rf "rai/server/internal/runfitness"
)

// stackEffort is a user-confirmed effort label (rai.workout_effort/1). The
// legacy "maximal_supported" mode is read as a race.
type stackEffort struct {
	ID, WorkoutID, Kind string
	AnnotatedAt         int64
	KnownAt             int64
}

func effortKind(mode string) string {
	switch mode {
	case "race", "maximal_supported":
		return "race"
	case "threshold_test", "speed_test", "sprint_test":
		return mode
	}
	return ""
}

// stackEfforts reads the labels in the revised view: every label recorded by
// the computation time applies to its Workout, whenever it was marked, and a
// revocation removes it. (A label marked today for last month's race must
// change last month's history when it is recomputed.)
func stackEfforts(inputs []stackInput, captured time.Time) []stackEffort {
	var out []stackEffort
	for _, row := range effectiveStackInputs(inputs, captured, captured) {
		if row.Kind == "workout_effort" {
			if e, ok := parseEffort(row); ok {
				out = append(out, e)
			}
		}
	}
	return out
}

func parseEffort(row stackInput) (stackEffort, bool) {
	var x struct {
		WorkoutID     string `json:"workoutID"`
		Mode          string `json:"mode"`
		UserConfirmed bool   `json:"userConfirmed"`
	}
	if json.Unmarshal(row.Payload, &x) != nil || !x.UserConfirmed || effortKind(x.Mode) == "" {
		return stackEffort{}, false
	}
	// Workout UUIDs are stored lowercase; HealthKit prints them uppercase.
	return stackEffort{ID: row.ID, WorkoutID: strings.ToLower(x.WorkoutID), Kind: effortKind(x.Mode), AnnotatedAt: row.From.UnixMicro(), KnownAt: row.Known.UnixMicro()}, true
}

// fitnessReference maps the resolved event-time HR reference. The resting
// value is rounded to 1 bpm so a new daily resting-HR fact that does not move
// the rounded reference cannot change any running-fitness output.
func fitnessReference(ref hrref.Snapshot, conflict bool) (*rf.Reference, bool) {
	if conflict {
		return nil, true
	}
	if ref.HRMax == nil || ref.RestingHR == nil {
		return nil, false
	}
	out := &rf.Reference{MaxHR: ref.HRMax.Value, RestHR: ref.RestingHR.Value, MaxBasis: "measured", RestBasis: "user"}
	if ref.HRMax.Method == "age_estimated" {
		out.MaxBasis = "age_estimated"
	}
	if ref.RestingHR.Baseline {
		out.RestBasis = "baseline"
		out.RestHR = math.Round(out.RestHR)
	}
	out.Hash = rf.Hash(struct {
		Max, Rest           float64
		MaxBasis, RestBasis string
		MaxID               string
	}{out.MaxHR, out.RestHR, out.MaxBasis, out.RestBasis, ref.HRMax.ReferenceID})
	return out, false
}

// stackRunFitness computes every single-run result of the 90 days up to the
// evaluation day and rebuilds that day's long-term state from them. Single
// results depend only on each Workout's own features, the reference in
// effect at its start and its effort label known at evaluation time.
func stackRunFitness(dataset string, generation int64, date, at time.Time, pending []stackPendingLoad, efforts []stackEffort, source string, ambiguous map[string]bool) rf.State {
	loc := date.Location()
	first := date.AddDate(0, 0, -89).Format(time.DateOnly)
	day := date.Format(time.DateOnly)
	label := map[string]stackEffort{}
	for _, e := range efforts {
		if old, ok := label[e.WorkoutID]; !ok || e.KnownAt >= old.KnownAt {
			label[e.WorkoutID] = e
		}
	}
	// Without an explicit source selection the latest Workout's stable source
	// identity is used; other devices are not fused without cross-calibration.
	if source == "" {
		for i := len(pending) - 1; i >= 0; i-- {
			if p := pending[i]; p.r.failure == "" && !ambiguous[p.w.id] && p.w.sourceEpoch != "" {
				source = p.w.sourceEpoch
				break
			}
		}
	}
	var obs []rf.SingleResult
	for _, p := range pending {
		w := p.w
		if p.r.failure != "" || ambiguous[w.id] || w.feature.RunFitness == nil || source != "" && w.sourceEpoch != source {
			continue
		}
		d := time.UnixMicro(w.start).In(loc).Format(time.DateOnly)
		if d < first || d > day {
			continue
		}
		ref, conflict := fitnessReference(p.r.ref, p.r.refConflict)
		in := rf.SingleInput{WorkoutID: w.id, Day: d, Start: w.start, End: w.end, KnownAt: w.known.UnixMicro(), SourceEpoch: w.sourceEpoch,
			Features: w.feature.RunFitness, Reference: ref, ReferenceConflict: conflict}
		if e, ok := label[w.id]; ok {
			in.Effort = &rf.Effort{Kind: e.Kind, ID: e.ID}
			in.KnownAt = max(in.KnownAt, e.KnownAt)
		}
		obs = append(obs, rf.Single(in))
	}
	state := rf.CalculateState(rf.StateInput{DatasetID: dataset, Date: day, AsOf: at.UnixMicro(), View: "revised", Observations: obs})
	state.Generation = generation
	return state
}
