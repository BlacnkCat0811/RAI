// Package hrref implements the versioned HR reference contract (hr_reference/2)
// and deterministic Workout overlap/source-context operators. It is pure: no
// clock, I/O, database or raw mutation.
package hrref

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"time"
)

const (
	Schema       = "rai.hr_reference/2"
	LegacySchema = "rai.hr_reference/1"
	MinBPM       = 30.0
	MaxBPM       = 220.0
	HRMaxDays    = 365
	RHRDays      = 90
	BaselineDays = 28
	MinBaseline  = 14
	RecentDays   = 7
	RecentMin    = 3
	HourUs       = int64(3600000000)
	DayUs        = 24 * HourUs
)

var ErrInvalid = errors.New("hr_reference_invalid")
var ErrConflict = errors.New("hr_reference_conflict")
var ErrNotEffective = errors.New("hr_reference_not_effective")

// Measurement records measurement and recording time independently. RecordedAt
// is the server-confirmed known-at time; MeasuredAt is when the physiology was
// observed. A historical evaluation may only use rows with RecordedAt <= asOf.
type Measurement struct {
	Value      float64 `json:"value"`
	MeasuredAt int64   `json:"measuredAtUs"`
	RecordedAt int64   `json:"recordedAtUs"`
	Method     string  `json:"method"`
	Unit       string  `json:"unit"`
}

type Reference struct {
	Schema           string       `json:"schemaVersion"`
	ID               string       `json:"id"`
	DatasetID        string       `json:"datasetID"`
	SubjectKey       string       `json:"subjectKey"`
	HRMax            *Measurement `json:"hrMax,omitempty"`
	RestingHR        *Measurement `json:"restingHeartRate,omitempty"`
	EffectiveFrom    int64        `json:"effectiveFromUs"`
	EffectiveThrough *int64       `json:"effectiveThroughUs,omitempty"`
	SupersedesID     *string      `json:"supersedesID,omitempty"`
	Revoked          bool         `json:"revoked"`
	SourceEpoch      string       `json:"sourceEpoch"`
	Provenance       string       `json:"provenance"`
	KnownAt          int64        `json:"knownAtUs"`
	ContentHash      string       `json:"contentSHA256"`
}

var methods = map[string]bool{
	"lab_max_test": true, "field_max_test": true, "clinical": true,
	"user_entered": true, "wearable_estimate": true,
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func Hash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Validate checks one typed reference. It deliberately has no age formula and
// never derives a missing HRmax from an observed maximum.
func Validate(r Reference, asOf int64) error {
	if r.Schema != Schema || r.ID == "" || r.DatasetID == "" || r.SubjectKey == "" || r.SourceEpoch == "" || r.Provenance == "" || r.EffectiveFrom <= 0 || r.KnownAt <= 0 {
		return ErrInvalid
	}
	if r.EffectiveThrough != nil && *r.EffectiveThrough <= r.EffectiveFrom {
		return ErrInvalid
	}
	if r.KnownAt > asOf {
		return ErrInvalid
	}
	for _, m := range []*Measurement{r.HRMax, r.RestingHR} {
		if m == nil {
			continue
		}
		if m.Unit != "bpm" || !finite(m.Value) || m.Value < MinBPM || m.Value >= MaxBPM || m.MeasuredAt <= 0 || m.RecordedAt <= 0 || m.RecordedAt < m.MeasuredAt || m.RecordedAt > asOf || !methods[m.Method] {
			return ErrInvalid
		}
	}
	if r.HRMax == nil && r.RestingHR == nil {
		return ErrInvalid
	}
	if r.HRMax != nil && r.RestingHR != nil && r.HRMax.Value <= r.RestingHR.Value {
		return ErrConflict
	}
	return nil
}

// effective decides whether a reference applies to a Workout starting at at.
// The stack evaluates the revised view: Validate already requires the row to
// be known at the evaluation instant (KnownAt <= asOf), so a reference entered
// today with an earlier effective date and measurement applies to the earlier
// Workouts it covers. Requiring KnownAt <= Workout start would leave every
// Workout before the first entry without load, forever.
func effective(r Reference, at int64) bool {
	return !r.Revoked && r.EffectiveFrom <= at && (r.EffectiveThrough == nil || at < *r.EffectiveThrough)
}

func within(m Measurement, at int64, days int) bool {
	start := at - int64(days)*DayUs
	return m.MeasuredAt >= start && m.MeasuredAt <= at
}

type Lineage struct {
	ID         string `json:"id"`
	RunID      string `json:"runID,omitempty"`
	Hash       string `json:"hash,omitempty"`
	Day        string `json:"day,omitempty"`
	MeasuredAt int64  `json:"measuredAtUs,omitempty"`
}

type Resolved struct {
	Value       float64   `json:"value"`
	ReferenceID string    `json:"referenceID,omitempty"`
	Method      string    `json:"method"`
	MeasuredAt  int64     `json:"measuredAtUs"`
	RecordedAt  int64     `json:"recordedAtUs"`
	Baseline    bool      `json:"baselineFallback"`
	UsableDays  int       `json:"usableDays"`
	Lineage     []Lineage `json:"lineage"`
}

type Snapshot struct {
	Evidence    *Evidence `json:"evidence,omitempty"`
	Schema      string    `json:"schemaVersion"`
	AsOf        int64     `json:"asOfUs"`
	WorkoutAt   int64     `json:"workoutStartUs"`
	SourceEpoch string    `json:"sourceEpoch"`
	HRMax       *Resolved `json:"hrMax"`
	RestingHR   *Resolved `json:"restingHeartRate"`
	Reasons     []string  `json:"reasonCodes"`
}

func (s Snapshot) Hash() string { return Hash(s) }

type DailyRHR struct {
	Date        string   `json:"date"`
	Start       int64    `json:"startUs"`
	Value       *float64 `json:"value"`
	SourceEpoch string   `json:"sourceEpoch"`
	RunID       string   `json:"runID"`
	Hash        string   `json:"hash"`
	Live        bool     `json:"live"`
}

type Input struct {
	AsOf         int64
	WorkoutStart int64
	SourceEpoch  string
	References   []Reference
	DailyRHR     []DailyRHR
}

// Resolve selects the reference snapshot effective at the Workout start. A
// conflicting explicit value fails closed; it never silently falls back.
func Resolve(in Input) (Snapshot, error) {
	out := Snapshot{Schema: Schema, AsOf: in.AsOf, WorkoutAt: in.WorkoutStart, SourceEpoch: in.SourceEpoch, Reasons: []string{}}
	if in.AsOf <= 0 || in.WorkoutStart <= 0 || in.WorkoutStart > in.AsOf {
		return out, ErrInvalid
	}
	var maxCandidates, restCandidates []Reference
	for _, r := range in.References {
		if e := Validate(r, in.AsOf); e != nil {
			return out, e
		}
		if !effective(r, in.WorkoutStart) {
			continue
		}
		// HRmax does not expire: a measurement before the Workout stays usable,
		// and one older than HRMaxDays only asks for a review.
		if r.HRMax != nil && r.HRMax.MeasuredAt <= in.WorkoutStart && (in.SourceEpoch == "" || r.SourceEpoch == in.SourceEpoch) {
			maxCandidates = append(maxCandidates, r)
		}
		if r.RestingHR != nil && within(*r.RestingHR, in.WorkoutStart, RHRDays) && (in.SourceEpoch == "" || r.SourceEpoch == in.SourceEpoch) {
			restCandidates = append(restCandidates, r)
		}
	}
	out.HRMax = pick(maxCandidates, true)
	if out.HRMax != nil && !within(Measurement{MeasuredAt: out.HRMax.MeasuredAt}, in.WorkoutStart, HRMaxDays) {
		out.Reasons = append(out.Reasons, "hrmax_review_due")
	}
	if out.HRMax == nil {
		if len(maxCandidates) > 0 {
			out.Reasons = append(out.Reasons, "hrmax_reference_conflict")
		} else {
			out.Reasons = append(out.Reasons, "hrmax_missing")
		}
	}
	out.RestingHR = pick(restCandidates, false)
	if out.RestingHR == nil {
		if len(restCandidates) > 0 {
			out.Reasons = append(out.Reasons, "rhr_reference_conflict")
		} else {
			rhr, reason := baselineRHR(in)
			if rhr != nil {
				out.RestingHR = rhr
			} else if reason != "" {
				out.Reasons = append(out.Reasons, reason)
			}
		}
	}
	if out.HRMax != nil && out.RestingHR != nil && out.HRMax.Value <= out.RestingHR.Value {
		out.Reasons = append(out.Reasons, "reference_conflict")
		out.HRMax, out.RestingHR = nil, nil
	}
	out.Reasons = dedupe(out.Reasons)
	return out, nil
}

// pick resolves the effective explicit value. References are append-only and
// the app never sends supersedesID, so a newer entry must override an older
// one from its own effective date on: the latest EffectiveFrom wins, then the
// latest KnownAt. Only candidates tied on both with different values are a
// conflict, which fails closed.
func pick(rs []Reference, isMax bool) *Resolved {
	if len(rs) == 0 {
		return nil
	}
	sort.Slice(rs, func(i, j int) bool {
		if rs[i].EffectiveFrom != rs[j].EffectiveFrom {
			return rs[i].EffectiveFrom > rs[j].EffectiveFrom
		}
		if rs[i].KnownAt != rs[j].KnownAt {
			return rs[i].KnownAt > rs[j].KnownAt
		}
		return rs[i].ID < rs[j].ID
	})
	top := rs[0]
	for _, r := range rs[1:] {
		if r.EffectiveFrom != top.EffectiveFrom || r.KnownAt != top.KnownAt {
			break
		}
		if math.Abs(measurement(r, isMax).Value-measurement(top, isMax).Value) > 1e-9 {
			return nil // conflict: one instant, more than one distinct explicit value
		}
	}
	m := measurement(top, isMax)
	return &Resolved{Value: m.Value, ReferenceID: top.ID, Method: m.Method, MeasuredAt: m.MeasuredAt, RecordedAt: m.RecordedAt, Lineage: []Lineage{{ID: top.ID, MeasuredAt: m.MeasuredAt}}}
}

func measurement(r Reference, isMax bool) Measurement {
	if isMax {
		return *r.HRMax
	}
	return *r.RestingHR
}

func baselineRHR(in Input) (*Resolved, string) {
	return baselineWithMinimum(in, MinBaseline, RecentMin)
}

func baselineWithMinimum(in Input, minimum, recentMinimum int) (*Resolved, string) {
	if in.SourceEpoch == "" {
		return nil, "rhr_missing"
	}
	start := in.WorkoutStart - int64(BaselineDays)*DayUs
	recent := in.WorkoutStart - int64(RecentDays)*DayUs
	byDate := map[string]DailyRHR{}
	for _, d := range in.DailyRHR {
		if d.Value == nil || d.SourceEpoch != in.SourceEpoch || d.Start < start || d.Start >= in.WorkoutStart || d.Start >= in.AsOf {
			continue
		}
		if _, ok := byDate[d.Date]; !ok {
			byDate[d.Date] = d
		}
	}
	if len(byDate) < minimum {
		return nil, "rhr_baseline_insufficient"
	}
	ds := make([]DailyRHR, 0, len(byDate))
	for _, d := range byDate {
		ds = append(ds, d)
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i].Date < ds[j].Date })
	vals := make([]float64, 0, len(ds))
	recentN := 0
	lineage := make([]Lineage, 0, len(ds))
	for _, d := range ds {
		v := *d.Value
		if !finite(v) || v < MinBPM || v >= MaxBPM {
			return nil, "rhr_baseline_invalid"
		}
		vals = append(vals, v)
		if d.Start >= recent {
			recentN++
		}
		lineage = append(lineage, Lineage{ID: d.Date, RunID: d.RunID, Hash: d.Hash, Day: d.Date})
	}
	if recentN < recentMinimum {
		return nil, "rhr_baseline_recent_insufficient"
	}
	sort.Float64s(vals)
	n := len(vals)
	v := vals[n/2]
	if n%2 == 0 {
		v = (vals[n/2-1] + vals[n/2]) / 2
	}
	last := ds[len(ds)-1]
	return &Resolved{Value: v, Method: "baseline_median_28d", MeasuredAt: last.Start, RecordedAt: in.AsOf, Baseline: true, UsableDays: len(ds), Lineage: lineage}, ""
}

func dedupe(xs []string) []string {
	sort.Strings(xs)
	out := xs[:0]
	for _, x := range xs {
		if len(out) == 0 || out[len(out)-1] != x {
			out = append(out, x)
		}
	}
	return out
}

// LocalDate is a helper for callers that need the civil date key.
func LocalDate(t time.Time) string { return t.Format("2006-01-02") }
