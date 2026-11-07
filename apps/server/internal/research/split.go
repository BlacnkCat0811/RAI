package research

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"time"
)

// TemporalSplitReport holds a time-ordered train/test partition. Observations
// strictly before cutoff are training rows; rows at or after cutoff are test
// rows. Input order is preserved inside each partition.
type TemporalSplitReport struct {
	Cutoff     time.Time
	Train      []Prediction
	Test       []Prediction
	TrainCount int
	TestCount  int
}

// TemporalSplit partitions predictions by their prediction timestamp. A
// prediction at exactly cutoff belongs to the test partition so no test row is
// visible before the cutoff.
func TemporalSplit(observations []Prediction, cutoff time.Time) TemporalSplitReport {
	report := TemporalSplitReport{Cutoff: cutoff}
	for _, observation := range observations {
		if observation.PredictedAt.Before(cutoff) {
			report.Train = append(report.Train, observation)
		} else {
			report.Test = append(report.Test, observation)
		}
	}
	report.TrainCount = len(report.Train)
	report.TestCount = len(report.Test)
	return report
}

// AthleteSplitReport holds an athlete-disjoint train/test partition.
type AthleteSplitReport struct {
	Train      []string
	Test       []string
	TrainCount int
	TestCount  int
}

// AthleteSplit assigns whole athlete keys to train or test sets. Duplicate keys
// are collapsed, assignment depends only on the sorted unique key set, the
// holdout fraction and the seed, and no athlete can appear in both partitions.
func AthleteSplit(keys []string, holdoutFraction float64, seed int64) (AthleteSplitReport, error) {
	if math.IsNaN(holdoutFraction) || math.IsInf(holdoutFraction, 0) ||
		holdoutFraction < 0 || holdoutFraction > 1 {
		return AthleteSplitReport{}, fmt.Errorf("research: holdout fraction must be within [0,1], got %v", holdoutFraction)
	}

	unique := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, key)
	}
	sort.Strings(unique)

	holdoutCount := int(math.Round(holdoutFraction * float64(len(unique))))
	if holdoutCount > len(unique) {
		holdoutCount = len(unique)
	}
	if holdoutCount < 0 {
		holdoutCount = 0
	}

	shuffled := append([]string(nil), unique...)
	rng := rand.New(rand.NewSource(seed))
	rng.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})

	report := AthleteSplitReport{
		Test:  append([]string(nil), shuffled[:holdoutCount]...),
		Train: append([]string(nil), shuffled[holdoutCount:]...),
	}
	sort.Strings(report.Train)
	sort.Strings(report.Test)
	report.TrainCount = len(report.Train)
	report.TestCount = len(report.Test)
	return report, nil
}
