package research

import (
	"hash/fnv"
	"math"
	"math/rand"
	"sort"
	"time"
)

const (
	clusterBootstrapResamples = 1000
	clusterBootstrapSeed      = uint64(0x9E3779B97F4A7C15)
)

// LinkedPair is one prediction matched to one outcome.
type LinkedPair struct {
	Prediction Prediction
	Outcome    Outcome
}

// LinkResult reports every prediction and outcome disposition. Withdrawn
// predictions are never matched; Paired, UnpairedPredictions and
// UnpairedOutcomes are disjoint by row.
type LinkResult struct {
	Pairs               []LinkedPair
	UnpairedPredictions []Prediction
	UnpairedOutcomes    []Outcome
	Withdrawn           []Prediction
	OverlapExcluded     int
}

// LinkOutcomes pairs outcomes to predictions only when athlete and cluster
// keys match and the absolute observed-predicted time difference is within the
// prediction horizon. Each outcome is used at most once; when several
// predictions are eligible the closest one wins (earlier observed time and
// then input order break ties). Withdrawn predictions are reported separately.
func LinkOutcomes(predictions []Prediction, outcomes []Outcome) LinkResult {
	byKey := make(map[string][]int, len(outcomes))
	for i, outcome := range outcomes {
		key := outcome.AthleteKey + "\x00" + outcome.ClusterKey
		byKey[key] = append(byKey[key], i)
	}

	used := make([]bool, len(outcomes))
	var result LinkResult
	for _, prediction := range predictions {
		if prediction.WithdrawnAt != nil || prediction.State == PredictionStateWithdrawn {
			result.Withdrawn = append(result.Withdrawn, clonePrediction(prediction))
			continue
		}

		best := -1
		var bestDifference time.Duration
		if prediction.HorizonDays >= 0 {
			window := time.Duration(prediction.HorizonDays) * 24 * time.Hour
			key := prediction.AthleteKey + "\x00" + prediction.ClusterKey
			for _, index := range byKey[key] {
				if used[index] {
					continue
				}
				if outcomeReusesPredictionInput(prediction, outcomes[index]) {
					result.OverlapExcluded++
					continue
				}
				difference := absDuration(outcomes[index].ObservedAt.Sub(prediction.PredictedAt))
				if difference > window {
					continue
				}
				if best == -1 || difference < bestDifference ||
					(difference == bestDifference && outcomes[index].ObservedAt.Before(outcomes[best].ObservedAt)) {
					best = index
					bestDifference = difference
				}
			}
		}
		if best == -1 {
			result.UnpairedPredictions = append(result.UnpairedPredictions, clonePrediction(prediction))
			continue
		}
		used[best] = true
		result.Pairs = append(result.Pairs, LinkedPair{
			Prediction: clonePrediction(prediction),
			Outcome:    cloneOutcome(outcomes[best]),
		})
	}

	for i, outcome := range outcomes {
		if !used[i] {
			result.UnpairedOutcomes = append(result.UnpairedOutcomes, cloneOutcome(outcome))
		}
	}
	return result
}

func outcomeReusesPredictionInput(prediction Prediction, outcome Outcome) bool {
	if outcome.WorkoutID != "" {
		for _, id := range prediction.InputWorkoutIDs {
			if id == outcome.WorkoutID {
				return true
			}
		}
	}
	if prediction.InputFrom == nil || prediction.InputTo == nil || outcome.WindowFrom == nil || outcome.WindowTo == nil {
		return false
	}
	return prediction.InputFrom.Before(*outcome.WindowTo) && outcome.WindowFrom.Before(*prediction.InputTo)
}

// ClusterStat summarizes one cluster. N is the number of linked pairs in the
// cluster; NValues is the number of those pairs with a numeric, uncensored
// prediction and outcome used by the mean and bootstrap interval. Suppressed
// clusters retain counts only: MeanDelta and both CI bounds stay nil.
type ClusterStat struct {
	ClusterKey string
	N          int
	NValues    int
	MeanDelta  *float64
	CI95Lower  *float64
	CI95Upper  *float64
	Suppressed bool
}

// ClusterSummaryReport is deterministic cluster output in ClusterKey order.
type ClusterSummaryReport struct {
	Clusters           []ClusterStat
	SuppressedClusters []string
}

// ClusterSummary aggregates linked pairs by ClusterKey. MeanDelta is the mean
// signed prediction error (observed - predicted). The 95% interval is a
// percentile cluster bootstrap with a fixed deterministic seed, so repeated
// calls over the same pairs return identical numbers.
//
// A cluster with fewer than minCluster numeric pairs is suppressed: no mean or
// interval is emitted and its key is listed in SuppressedClusters. minCluster
// values below one behave as one.
func ClusterSummary(pairs []LinkedPair, minCluster int) ClusterSummaryReport {
	if minCluster < 1 {
		minCluster = 1
	}

	grouped := make(map[string][]LinkedPair)
	for _, pair := range pairs {
		if pair.Prediction.WithdrawnAt != nil || pair.Prediction.State == PredictionStateWithdrawn {
			continue
		}
		grouped[pair.Prediction.ClusterKey] = append(grouped[pair.Prediction.ClusterKey], pair)
	}

	keys := make([]string, 0, len(grouped))
	for key := range grouped {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	report := ClusterSummaryReport{Clusters: make([]ClusterStat, 0, len(keys))}
	for _, key := range keys {
		group := grouped[key]
		stat := ClusterStat{ClusterKey: key, N: len(group)}

		deltas := make([]float64, 0, len(group))
		athletes := make([]string, 0, len(group))
		for _, pair := range group {
			if pair.Outcome.Censored || pair.Prediction.Value == nil || pair.Outcome.Value == nil {
				continue
			}
			deltas = append(deltas, *pair.Outcome.Value-*pair.Prediction.Value)
			athlete := pair.Prediction.AthleteKey
			if athlete == "" {
				athlete = pair.Outcome.AthleteKey
			}
			athletes = append(athletes, athlete)
		}
		stat.NValues = len(deltas)

		if stat.NValues < minCluster {
			stat.Suppressed = true
			report.SuppressedClusters = append(report.SuppressedClusters, key)
			report.Clusters = append(report.Clusters, stat)
			continue
		}

		mean, lower, upper := clusterMeanBootstrap(deltas, athletes, bootstrapSeedFor(key))
		stat.MeanDelta = floatPointer(mean)
		stat.CI95Lower = floatPointer(lower)
		stat.CI95Upper = floatPointer(upper)
		report.Clusters = append(report.Clusters, stat)
	}
	return report
}

func bootstrapSeedFor(clusterKey string) int64 {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(clusterKey))
	return int64(hash.Sum64() ^ clusterBootstrapSeed)
}

// clusterMeanBootstrap resamples whole athletes (not individual rows), so
// repeated observations from one athlete stay in the same block. Blocks and
// their values are sorted before resampling, making the fixed-seed interval
// independent of input row order.
func clusterMeanBootstrap(values []float64, athletes []string, seed int64) (mean, lower, upper float64) {
	for _, value := range values {
		mean += value
	}
	mean /= float64(len(values))

	blocks := make(map[string][]float64)
	for i, value := range values {
		key := athletes[i]
		blocks[key] = append(blocks[key], value)
	}
	blockKeys := make([]string, 0, len(blocks))
	for key := range blocks {
		blockKeys = append(blockKeys, key)
	}
	sort.Strings(blockKeys)
	for _, key := range blockKeys {
		sort.Float64s(blocks[key])
	}

	rng := rand.New(rand.NewSource(seed))
	means := make([]float64, clusterBootstrapResamples)
	for sample := range means {
		var sum float64
		var count int
		for resampled := 0; resampled < len(blockKeys); resampled++ {
			block := blocks[blockKeys[rng.Intn(len(blockKeys))]]
			for _, value := range block {
				sum += value
				count++
			}
		}
		means[sample] = sum / float64(count)
	}
	sort.Float64s(means)
	return mean, percentile(means, 0.025), percentile(means, 0.975)
}

func percentile(sortedValues []float64, fraction float64) float64 {
	if len(sortedValues) == 0 {
		return math.NaN()
	}
	position := fraction * float64(len(sortedValues)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return sortedValues[lower]
	}
	weight := position - float64(lower)
	return sortedValues[lower]*(1-weight) + sortedValues[upper]*weight
}

func absDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}

func floatPointer(value float64) *float64 {
	return &value
}
