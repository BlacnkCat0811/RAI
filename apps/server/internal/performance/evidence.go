package performance

import (
	"math"
	wf "rai/server/internal/workoutfeatures"
	"sort"
	"time"
)

func qualified(q wf.Qualification) bool { return q.Status == "eligible" || q.Status == "conditional" }
func duration(d int) bool               { return d == 1200 || d == 1800 || d == 2400 || d == 3600 || d == 5400 }

// Extract accepts only M1's best qualified windows. Backup and short diagnostic
// windows never become independent athlete evidence. Conflicting duplicate run
// identities are rejected instead of silently choosing a database order.
func Extract(in Input) ([]Evidence, error) { return extract(in, false) }

// ExtractGradeAdjusted reads the grade-adjusted windows instead: hills within
// the feature's MaxGradeAdjusted are converted to flat-equivalent speed, so a
// rolling route is evidence instead of being excluded (v2 running ability).
func ExtractGradeAdjusted(in Input) ([]Evidence, error) { return extract(in, true) }

func extract(in Input, gap bool) ([]Evidence, error) {
	if in.Calendar == nil || in.DatasetID == "" || in.Generation < 1 || in.AsOf <= 0 || (in.View != "revised" && in.View != "as_known") {
		return nil, invalid()
	}
	seen := map[string]string{}
	out := []Evidence{}
	for _, ref := range in.Features {
		f := ref.Feature
		if ref.KnownAt > in.AsOf || f.Timing.End > in.AsOf {
			continue
		}
		firstKnown := ref.FirstReceivedAt
		if firstKnown == 0 {
			firstKnown = ref.KnownAt
		}
		// An arrival already outside the 90-day live horizon cannot later enter
		// current output through a smoothing seed or an explicit rebuild.
		if firstKnown-f.Timing.End > 90*86400e6 {
			continue
		}
		if f.DatasetID != in.DatasetID || f.Schema != wf.Schema || f.Version != wf.Version || f.Algorithm != wf.Algorithm || f.StackID != wf.StackID() || ref.RunID == "" || ref.KnownAt <= 0 {
			return nil, invalid()
		}
		h := Hash(ref)
		if prior, ok := seen[f.WorkoutID]; ok {
			if prior != h {
				return nil, invalid()
			}
			continue
		}
		seen[f.WorkoutID] = h
		if len(seen) > MaxWorkouts {
			return nil, errorsLimit()
		}
		workoutQ := f.Qualification.Performance
		if gap {
			if f.Qualification.PerformanceGAP == nil {
				continue
			}
			workoutQ = *f.Qualification.PerformanceGAP
		}
		if f.State == wf.Deleted || f.State == wf.Missing || !qualified(workoutQ) {
			continue
		}
		byDuration := map[int]Evidence{}
		for _, c := range f.Windows {
			w := c.Best
			if gap {
				w = nil
				if c.GAP != nil && c.GAP.GAPDistance != nil && c.GAP.GAPSpeed != nil {
					x := *c.GAP
					x.Distance, x.MeanSpeed = *c.GAP.GAPDistance, *c.GAP.GAPSpeed
					w = &x
				}
			}
			if w == nil || !duration(c.Duration) || !qualified(w.Qualification) {
				continue
			}
			if w.Duration != c.Duration || w.End-w.Start != int64(w.Duration)*1e6 || w.Start < f.Timing.Start || w.End > f.Timing.End || !finite(w.Distance) || !finite(w.MeanSpeed) || w.Distance <= 0 || w.MeanSpeed <= 0 || math.Abs(w.Distance/float64(w.Duration)-w.MeanSpeed) > 1e-8 || w.Coverage < 0 || w.Coverage > 1 || !finite(w.Coverage) {
				return nil, invalid()
			}
			q := Quality{Coverage: w.Coverage, DistanceAgreement: 1, Terrain: 1, Source: 1, Gap: 1, Qualification: 1}
			// These are existing feature facts; M3 adds no second quality filter.
			if f.Distance.DifferenceRatio != nil {
				if !finite(*f.Distance.DifferenceRatio) {
					return nil, invalid()
				}
				q.DistanceAgreement = math.Max(0, 1-math.Abs(*f.Distance.DifferenceRatio))
			}
			if w.SourceEpoch == "" {
				q.Source = .5
			}
			if w.Quality != "supported" {
				q.Gap = .8
			}
			if w.Qualification.Status == "conditional" || workoutQ.Status == "conditional" {
				q.Qualification = .9
			}
			q.Q = q.Coverage * q.DistanceAgreement * q.Terrain * q.Source * q.Gap * q.Qualification
			terrain := "M1_qualified_comparable_flat"
			if gap {
				terrain = "M1_qualified_grade_adjusted"
			}
			e := Evidence{f.WorkoutID, w.Duration, w.Distance, w.MeanSpeed, w.Start, w.End, f.Timing.End, ref.KnownAt, f.Algorithm + "/" + f.Version, ref.RunID, f.Generation, w.SourceEpoch, terrain, q, w.Qualification, Mode, w.Evidence}
			if old, ok := byDuration[w.Duration]; !ok || e.MeanSpeed > old.MeanSpeed || (e.MeanSpeed == old.MeanSpeed && e.StartedAt < old.StartedAt) {
				byDuration[w.Duration] = e
			}
		}
		for _, e := range byDuration {
			out = append(out, e)
		}
	}
	sortEvidence(out)
	return out, nil
}

// EvidenceAnchor finds the latest accepted evidence without fitting any curve.
// Storage uses it to read exactly the evidence-day 90+13-day bounded range,
// even when a more recent isolated anomaly is quarantined.
func EvidenceAnchor(in Input) (int64, error) {
	es, err := Extract(in)
	if err != nil {
		return 0, err
	}
	latest := int64(0)
	for _, e := range es {
		latest = max(latest, e.WorkoutEnd)
	}
	if latest == 0 {
		return 0, nil
	}
	_, support := pool(es, date(time.UnixMicro(latest), in.Calendar), in.AsOf, in.View)
	return support.LatestEvidence, nil
}
func errorsLimit() error { return &LimitError{} }

type LimitError struct{}

func (*LimitError) Error() string { return "performance_input_limit" }
func sortEvidence(es []Evidence) {
	sort.Slice(es, func(i, j int) bool {
		a, b := es[i], es[j]
		if a.WorkoutID != b.WorkoutID {
			return a.WorkoutID < b.WorkoutID
		}
		return a.Duration < b.Duration
	})
}

// Comparable outliers are tested against other workouts at the same duration.
// Any high point quarantines the whole workout, unless an independent workout
// at that duration confirms it within 5%. Future confirmation is excluded by h.
func pool(es []Evidence, h time.Time, cutoff int64, view string) ([]WeightedEvidence, Support) {
	end := h.AddDate(0, 0, 1).UnixMicro()
	begin := h.AddDate(0, 0, -89).UnixMicro()
	selected := []Evidence{}
	byDuration := map[int][]Evidence{}
	for _, e := range es {
		if e.WorkoutEnd < begin || e.WorkoutEnd >= end || (view == "as_known" && e.KnownAt >= end) || e.KnownAt > cutoff {
			continue
		}
		selected = append(selected, e)
		byDuration[e.Duration] = append(byDuration[e.Duration], e)
	}
	quarantine := map[string]bool{}
	for _, e := range selected {
		others := []float64{}
		confirmed := false
		for _, o := range byDuration[e.Duration] {
			if e.WorkoutID == o.WorkoutID {
				continue
			}
			others = append(others, o.MeanSpeed)
			if math.Abs(o.MeanSpeed/e.MeanSpeed-1) <= .05+1e-12 {
				confirmed = true
			}
		}
		if len(others) >= 3 && !confirmed {
			sort.Float64s(others)
			median := others[len(others)/2]
			if e.MeanSpeed > median*1.10 {
				quarantine[e.WorkoutID] = true
			}
		}
	}
	counts := map[string]int{}
	for _, e := range selected {
		if !quarantine[e.WorkoutID] && e.Quality.Q > 0 {
			counts[e.WorkoutID]++
		}
	}
	out := []WeightedEvidence{}
	s := Support{Durations: []int{}, SourceEpochs: []string{}}
	workouts, short, long := map[string]bool{}, map[string]bool{}, map[string]bool{}
	durations, epochs := map[int]bool{}, map[string]bool{}
	weights := map[string]float64{}
	qualities := map[string]float64{}
	first, last := int64(0), int64(0)
	for _, e := range selected {
		w := WeightedEvidence{Evidence: e, State: "qualified"}
		if quarantine[e.WorkoutID] {
			w.State = "confirmation_required"
		} else if e.Quality.Q > 0 {
			age := math.Max(0, float64(end-1-e.WorkoutEnd)/86400e6)
			w.Weight = e.Quality.Q * math.Exp(-age/45) / float64(counts[e.WorkoutID])
			workouts[e.WorkoutID] = true
			if e.Duration <= 1800 {
				short[e.WorkoutID] = true
			}
			if e.Duration >= 2400 {
				long[e.WorkoutID] = true
			}
			durations[e.Duration] = true
			epochs[e.SourceEpoch] = true
			weights[e.WorkoutID] += w.Weight
			qualities[e.WorkoutID] += e.Quality.Q / float64(counts[e.WorkoutID])
			if first == 0 || e.WorkoutEnd < first {
				first = e.WorkoutEnd
			}
			if e.WorkoutEnd > last {
				last = e.WorkoutEnd
			}
			if e.Duration > s.LongestDuration {
				s.LongestDuration = e.Duration
			}
		}
		out = append(out, w)
	}
	s.IndependentWorkouts = len(workouts)
	s.ShortWorkouts = len(short)
	s.LongWorkouts = len(long)
	s.LatestEvidence = last
	if first > 0 {
		s.HistorySpan = days(date(time.UnixMicro(last), h.Location()), date(time.UnixMicro(first), h.Location()))
	}
	for d := range durations {
		s.Durations = append(s.Durations, d)
	}
	sort.Ints(s.Durations)
	for e := range epochs {
		s.SourceEpochs = append(s.SourceEpochs, e)
	}
	sort.Strings(s.SourceEpochs)
	// Iterate in evidence order so floating sums are byte-stable across map order.
	sum, squares := 0.0, 0.0
	for _, e := range selected {
		if !workouts[e.WorkoutID] {
			continue
		}
		w := weights[e.WorkoutID]
		sum += w
		squares += w * w
		s.QualitySupport += qualities[e.WorkoutID] / float64(s.IndependentWorkouts)
		delete(workouts, e.WorkoutID)
	}
	if squares > 0 {
		s.EffectiveWorkouts = sum * sum / squares
	}
	return out, s
}
