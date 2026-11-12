package workoutfeatures

import (
	"encoding/json"
	"math"
	"sort"

	"rai/server/internal/workoutdomain"
)

// EffortSchema is the native-resolution intensity point series used by the
// training-effect model's anaerobic branch. It is a companion block of the
// feature artifact; the frozen ten-second Structure slices stay unchanged.
const EffortSchema = "rai.workout_effort/1"

// EffortPolicy is part of the feature configuration identity.
const EffortPolicy = "effort-native/v1;support-span5s-gap5s;retain-.9median-context30s;grade-cover.9-gap.08;power-cadence-cover.5;hr-mid-interp"

// MaxEffortPoints bounds one Workout's series; beyond it the feature fails
// with feature_artifact_limit rather than being truncated.
const MaxEffortPoints = 20000

// EffortPoint is one native speed span (about 2.56 s on Apple Watch).
type EffortPoint struct {
	Start, End int64
	Speed      float64  // raw running speed, m/s
	GAPSpeed   *float64 // grade-adjusted speed; nil without measured grade or beyond MaxGradeAdjusted
	Grade      *float64
	Power      *float64 // concurrent running power, W
	Cadence    *float64 // concurrent step cadence, steps/min
	HR         *float64 // HR interpolated at the span midpoint
	Supported  bool     // inside one active segment, native span and support gap <=5 s
}

type EffortFeatures struct {
	Schema        string   `json:"schemaVersion"`
	Policy        string   `json:"policy"`
	MedianSpeed   *float64 `json:"medianSpeedMPS"`
	MedianPower   *float64 `json:"medianPowerW"`
	MedianCadence *float64 `json:"medianCadenceSPM"`
	Indoor        *bool    `json:"indoor"`
	TotalPoints   int      `json:"totalPoints"`
	// Points keeps only spans at >=0.9 x median speed plus 30 s of context on
	// each side; everything dropped is slower than the retained threshold.
	Points []EffortPoint `json:"points"`
}

// [startUs,endUs,speed,gapSpeed,grade,power,cadence,hr,supported]
func (p EffortPoint) MarshalJSON() ([]byte, error) {
	return json.Marshal([9]any{p.Start, p.End, p.Speed, p.GAPSpeed, p.Grade, p.Power, p.Cadence, p.HR, p.Supported})
}
func (p *EffortPoint) UnmarshalJSON(b []byte) error {
	var fields []json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return err
	}
	if len(fields) != 9 {
		return failure("feature_invalid_input")
	}
	for i, dst := range []any{&p.Start, &p.End, &p.Speed} {
		if string(fields[i]) == "null" {
			return failure("feature_invalid_input")
		}
		if err := json.Unmarshal(fields[i], dst); err != nil {
			return err
		}
	}
	for i, dst := range []**float64{&p.GAPSpeed, &p.Grade, &p.Power, &p.Cadence, &p.HR} {
		if err := json.Unmarshal(fields[3+i], dst); err != nil {
			return err
		}
	}
	return json.Unmarshal(fields[8], &p.Supported)
}

func sortedSpans(xs []Span) []Span {
	out := append([]Span(nil), xs...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	return out
}

// spanMean is the time-weighted mean of xs over [a,b) and the covered fraction.
func spanMean(xs []Span, a, b int64) (float64, float64) {
	i := sort.Search(len(xs), func(i int) bool { return xs[i].End > a })
	var area, covered float64
	for ; i < len(xs) && xs[i].Start < b; i++ {
		lo, hi := max(a, xs[i].Start), min(b, xs[i].End)
		if lo < hi {
			area += xs[i].integral(lo, hi)
			covered += float64(hi-lo) / 1e6
		}
	}
	if covered == 0 {
		return 0, 0
	}
	return area / covered, covered / (float64(b-a) / 1e6)
}

func spanAt(xs []Span, at int64) *float64 {
	i := sort.Search(len(xs), func(i int) bool { return xs[i].End > at })
	if i < len(xs) && xs[i].Start <= at {
		return ptr(xs[i].value(at))
	}
	return nil
}

// Stored values are rounded (speed mm/s, grade 0.01 %, power/cadence/HR 0.1)
// so a multi-hour run stays well inside MaxArtifactBytes.
func roundTo(v, scale float64) float64 { return math.Round(v*scale) / scale }

func medianOf(xs []float64) *float64 {
	if len(xs) == 0 {
		return nil
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	v := s[len(s)/2]
	if len(s)%2 == 0 {
		v = (s[len(s)/2-1] + v) / 2
	}
	return &v
}

func effortFeatures(s RawWorkoutSnapshot, t AnalysisTimeline, env Environment) (*EffortFeatures, error) {
	out := &EffortFeatures{Schema: EffortSchema, Policy: EffortPolicy, Indoor: env.Indoor, Points: []EffortPoint{}}
	speed := sortedSpans(t.Speed.Spans)
	if len(speed) == 0 {
		return out, nil
	}
	power := sortedSpans(BuildSignal(s, t.Pause, Power).Spans)
	steps := sortedSpans(BuildSignal(s, t.Pause, Steps).Spans)
	hr := sortedSpans(t.HeartRate.Spans)
	grades := sortedSpans(t.Route.Grades)
	all := make([]EffortPoint, 0, len(speed))
	var speeds, powers, cadences []float64
	for _, x := range power {
		powers = append(powers, (x.V0+x.V1)/2)
	}
	for _, x := range steps {
		cadences = append(cadences, (x.V0+x.V1)/2)
	}
	for _, x := range speed {
		if x.End <= x.Start {
			continue
		}
		dur := float64(x.End-x.Start) / 1e6
		raw := x.integral(x.Start, x.End) / dur
		p := EffortPoint{Start: x.Start, End: x.End, Speed: roundTo(raw, 1e3)}
		p.Supported = dur <= 5 && x.SupportGap <= 5 && finite(raw)
		if g, cover := spanMean(grades, x.Start, x.End); cover >= .9 && finite(g) {
			p.Grade = ptr(roundTo(g, 1e4))
			if math.Abs(g) <= MaxGradeAdjusted {
				p.GAPSpeed = ptr(roundTo(raw*workoutdomain.GradeAdjustment(g), 1e3))
			}
		}
		if v, cover := spanMean(power, x.Start, x.End); cover >= .5 && finite(v) {
			p.Power = ptr(roundTo(v, 10))
		}
		if v, cover := spanMean(steps, x.Start, x.End); cover >= .5 && finite(v) {
			p.Cadence = ptr(roundTo(v, 10))
		}
		if v := spanAt(hr, x.Start+(x.End-x.Start)/2); v != nil {
			p.HR = ptr(roundTo(*v, 10))
		}
		if p.Supported {
			speeds = append(speeds, p.Speed)
		}
		all = append(all, p)
	}
	out.TotalPoints = len(all)
	out.MedianSpeed, out.MedianPower, out.MedianCadence = medianOf(speeds), medianOf(powers), medianOf(cadences)
	if out.MedianSpeed == nil {
		return out, nil
	}
	floor := .9 * *out.MedianSpeed
	keep := make([]bool, len(all))
	const context = int64(30e6)
	lo := 0
	for _, p := range all {
		if p.Speed < floor {
			continue
		}
		for lo < len(all) && all[lo].End < p.Start-context {
			lo++
		}
		for j := lo; j < len(all) && all[j].Start <= p.End+context; j++ {
			keep[j] = true
		}
	}
	for i, p := range all {
		if keep[i] {
			out.Points = append(out.Points, p)
		}
	}
	if len(out.Points) > MaxEffortPoints {
		return out, failure("feature_artifact_limit")
	}
	return out, nil
}
