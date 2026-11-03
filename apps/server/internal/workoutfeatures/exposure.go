package workoutfeatures

import "encoding/json"

// M2's companion artifact is independently versioned. The frozen v1 feature
// payload and its qualification/time/source semantics are unchanged.
const ExposureSchema = "rai.hr_exposure/1"
const ExposureKey = "hr_exposure"
const MaxExposureBytes = 2 << 20
const MaxExposureSegments = 20000

type HRSegment struct {
	Start         int64   `json:"startUs"`
	End           int64   `json:"endUs"`
	HR0           float64 `json:"hrStart"`
	HR1           float64 `json:"hrEnd"`
	Source        int     `json:"sourceIndex"`
	Reconstructed bool    `json:"reconstructed"`
	ActiveSegment int     `json:"activeSegment"`
}
type HRExposure struct {
	Schema           string      `json:"schemaVersion"`
	DatasetID        string      `json:"datasetID"`
	WorkoutID        string      `json:"workoutID"`
	Generation       int64       `json:"generation"`
	InputHash        string      `json:"inputHash"`
	Active           []Interval  `json:"activeIntervals"`
	Segments         []HRSegment `json:"segments"`
	SourceEpochs     []string    `json:"sourceEpochs"`
	Parameter        *Parameter  `json:"parameter"`
	ParameterReasons []Reason    `json:"parameterReasons"`
	SummaryHR        *float64    `json:"summaryHR"`
	SummarySemantics string      `json:"summarySemantics,omitempty"`
}

// BuildHRExposure reuses M1's sole pause, support, source and outlier path.
// It never filters HR again. Only exactly constant adjacent support is merged;
// no lossy simplification, resampling, extrapolation or raw database copy.
func BuildHRExposure(s RawWorkoutSnapshot) (HRExposure, error) {
	p := BuildPause(s)
	h := BuildSignal(s, p, HR)
	e := HRExposure{Schema: ExposureSchema, DatasetID: s.DatasetID, WorkoutID: s.WorkoutID, Generation: s.Generation, InputHash: s.Hash(), Active: p.Active, Segments: []HRSegment{}, SourceEpochs: []string{}}
	sourceIndex := map[string]int{}
	e.Parameter, e.ParameterReasons = resolveParameter(s)
	for _, x := range h.Spans {
		index, ok := sourceIndex[x.Epoch]
		if !ok {
			index = len(e.SourceEpochs)
			sourceIndex[x.Epoch] = index
			e.SourceEpochs = append(e.SourceEpochs, x.Epoch)
		}
		seg := HRSegment{x.Start, x.End, x.V0, x.V1, index, x.Reconstructed, x.ActiveSegment}
		n := len(e.Segments)
		if n > 0 {
			old := &e.Segments[n-1]
			if old.End == seg.Start && old.HR0 == old.HR1 && seg.HR0 == seg.HR1 && old.HR1 == seg.HR0 && old.Source == seg.Source && old.Reconstructed == seg.Reconstructed && old.ActiveSegment == seg.ActiveSegment {
				old.End = seg.End
				continue
			}
		}
		e.Segments = append(e.Segments, seg)
		if len(e.Segments) > MaxExposureSegments {
			return e, failure("feature_artifact_limit")
		}
	}
	// A whole-workout statistic has matching active semantics only when the
	// entire elapsed interval is provably active. Condensed parents do not qualify.
	sourceOK := false
	for _, src := range s.Provenance {
		if src.Epoch == s.WorkoutSourceEpoch && sourceAllowed(src) {
			sourceOK = true
		}
	}
	if len(e.Segments) == 0 && len(p.Active) == 1 && p.Active[0].Start == s.Start && p.Active[0].End == s.End && sourceOK && h.Summary.AmbiguousSeconds == 0 && len(h.Summary.Reasons) == 0 {
		conflict := false
		for _, st := range s.Statistics {
			if st.Type != HR || st.Unit != "count/min" || st.Start != s.Start || st.End != s.End || st.Average == nil {
				continue
			}
			v := *st.Average
			if !finite(v) || v < 30 || v >= s.Policy.HRSpikeBPM {
				conflict = true
				continue
			}
			if e.SummaryHR != nil && *e.SummaryHR != v {
				conflict = true
			}
			e.SummaryHR = ptr(v)
		}
		if conflict {
			e.SummaryHR = nil
		}
		if e.SummaryHR != nil {
			e.SummarySemantics = "whole_elapsed_equals_active"
		}
	}
	return e, nil
}

// The versioned wire tuple avoids repeating field names and a 64-byte source
// hash for every span. Integer timestamps are decoded without float conversion.
// [startUs,endUs,HR_start,HR_end,sourceIndex,reconstructed,activeSegment]
func (s HRSegment) MarshalJSON() ([]byte, error) {
	return json.Marshal([7]any{s.Start, s.End, s.HR0, s.HR1, s.Source, s.Reconstructed, s.ActiveSegment})
}
func (s *HRSegment) UnmarshalJSON(b []byte) error {
	var fields []json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return err
	}
	if len(fields) != 7 {
		return failure("feature_invalid_input")
	}
	dst := []any{&s.Start, &s.End, &s.HR0, &s.HR1, &s.Source, &s.Reconstructed, &s.ActiveSegment}
	for i, p := range dst {
		if string(fields[i]) == "null" {
			return failure("feature_invalid_input")
		}
		if err := json.Unmarshal(fields[i], p); err != nil {
			return err
		}
	}
	return nil
}
