// Package shadow evaluates synthetic-only candidate/legacy comparisons. It is
// deliberately aggregate-only: no athlete key, raw value or per-person row is
// returned. It never reads or writes an active model assignment.
package shadow

import (
	"errors"
	"math"
	"sort"
)

var ErrNotSynthetic = errors.New("shadow_requires_synthetic_dataset")
var ErrInvalid = errors.New("shadow_invalid_input")

// Observation is one synthetic paired comparison. Candidate and Legacy are
// scores on the same unit/scale. Nil means unavailable and is never treated as
// zero.
type Observation struct {
	AthleteKey string   `json:"athleteKey"`
	CohortKey  string   `json:"cohortKey"`
	Candidate  *float64 `json:"candidate"`
	Legacy     *float64 `json:"legacy"`
}

// Batch is the caller-supplied synthetic shadow input. Synthetic must be true;
// the harness refuses to produce a real-cohort report.
type Batch struct {
	DatasetID     string        `json:"datasetID"`
	Synthetic     bool          `json:"synthetic"`
	CandidateMode string        `json:"candidateMode"`
	LegacyMode    string        `json:"legacyMode"`
	Observations  []Observation `json:"observations"`
	MinCohort     int           `json:"minCohort"`
}

// CohortReport is an aggregate, group-suppressed comparison. MeanAbsDiff and
// Bias stay nil when the cohort is suppressed or no pair has both values.
type CohortReport struct {
	CohortKey   string   `json:"cohortKey"`
	N           int      `json:"n"`
	NPaired     int      `json:"nPaired"`
	MeanAbsDiff *float64 `json:"meanAbsDiff"`
	Bias        *float64 `json:"bias"`
	Suppressed  bool     `json:"suppressed"`
}

// Report is the only shadow output. It contains no athlete identifiers or
// individual predictions, and it explicitly states that active was untouched.
type Report struct {
	DatasetID     string         `json:"datasetID"`
	CandidateMode string         `json:"candidateMode"`
	LegacyMode    string         `json:"legacyMode"`
	Observations  int            `json:"observations"`
	Paired        int            `json:"paired"`
	Missing       int            `json:"missing"`
	Cohorts       []CohortReport `json:"cohorts"`
	Suppressed    []string       `json:"suppressedCohorts"`
	SyntheticOnly bool           `json:"syntheticOnly"`
	AffectsActive bool           `json:"affectsActive"`
}

// Evaluate validates and aggregates a synthetic batch. At least one cohort
// whose paired count reaches MinCohort must remain visible; otherwise the
// caller receives an error instead of a misleading empty success.
func Evaluate(batch Batch) (Report, error) {
	if !batch.Synthetic || batch.DatasetID == "" || len(batch.Observations) == 0 {
		return Report{}, ErrNotSynthetic
	}
	if batch.CandidateMode == "" || batch.LegacyMode == "" || batch.CandidateMode == batch.LegacyMode {
		return Report{}, ErrInvalid
	}
	min := batch.MinCohort
	if min < 1 {
		min = 5
	}
	grouped := map[string][]Observation{}
	report := Report{DatasetID: batch.DatasetID, CandidateMode: batch.CandidateMode, LegacyMode: batch.LegacyMode, Observations: len(batch.Observations), SyntheticOnly: true, AffectsActive: false}
	for _, o := range batch.Observations {
		if o.AthleteKey == "" || o.CohortKey == "" || !finite(o.Candidate) || !finite(o.Legacy) {
			return Report{}, ErrInvalid
		}
		grouped[o.CohortKey] = append(grouped[o.CohortKey], o)
	}
	keys := make([]string, 0, len(grouped))
	for k := range grouped {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	visible := 0
	for _, key := range keys {
		rows := grouped[key]
		c := CohortReport{CohortKey: key, N: len(rows)}
		var sumAbs, sumBias float64
		for _, o := range rows {
			if o.Candidate == nil || o.Legacy == nil {
				continue
			}
			d := *o.Candidate - *o.Legacy
			sumAbs += math.Abs(d)
			sumBias += d
			c.NPaired++
		}
		report.Paired += c.NPaired
		if c.NPaired < min {
			c.Suppressed = true
			report.Suppressed = append(report.Suppressed, key)
		} else {
			v := sumAbs / float64(c.NPaired)
			b := sumBias / float64(c.NPaired)
			c.MeanAbsDiff, c.Bias = &v, &b
			visible++
		}
		report.Cohorts = append(report.Cohorts, c)
	}
	report.Missing = report.Observations - report.Paired
	if len(report.Suppressed) == 0 {
		report.Suppressed = []string{}
	}
	if visible == 0 {
		return Report{}, ErrInvalid
	}
	return report, nil
}

func finite(v *float64) bool {
	return v == nil || (!math.IsNaN(*v) && !math.IsInf(*v, 0))
}
