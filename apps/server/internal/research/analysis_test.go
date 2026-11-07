package research

import (
	"testing"
	"time"
)

func TestLinkOutcomesRejectsSharedWorkoutAndOverlappingInputWindow(t *testing.T) {
	t0 := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(time.Hour)
	t2 := t0.Add(2 * time.Hour)
	pred := Prediction{AthleteKey: "a", ClusterKey: "c", PredictedAt: t2, HorizonDays: 7, InputWorkoutIDs: []string{"w1"}, InputFrom: &t0, InputTo: &t2}
	out := Outcome{AthleteKey: "a", ClusterKey: "c", ObservedAt: t2.Add(time.Hour), WorkoutID: "w1", WindowFrom: &t1, WindowTo: &t2, Value: researchPtr(50), Independent: true}
	got := LinkOutcomes([]Prediction{pred}, []Outcome{out})
	if len(got.Pairs) != 0 || got.OverlapExcluded != 1 || len(got.UnpairedPredictions) != 1 || len(got.UnpairedOutcomes) != 1 {
		t.Fatalf("shared workout linked: %+v", got)
	}
	out.WorkoutID = "w2"
	out.WindowFrom = &t2
	out.WindowTo = &t2
	got = LinkOutcomes([]Prediction{pred}, []Outcome{out})
	if len(got.Pairs) != 1 {
		t.Fatalf("independent outcome not linked: %+v", got)
	}
}

func researchPtr(v float64) *float64 { return &v }

func TestClusterSummarySingleBlockOracleAndZeroDenominator(t *testing.T) {
	mk := func(athlete, cluster string, pred, obs float64, censored bool) LinkedPair {
		return LinkedPair{
			Prediction: Prediction{AthleteKey: athlete, ClusterKey: cluster, Value: researchPtr(pred)},
			Outcome:    Outcome{AthleteKey: athlete, ClusterKey: cluster, Value: researchPtr(obs), Censored: censored},
		}
	}
	// Every resample draws the sole athlete block, so the cluster mean and both
	// percentile bounds must equal the hand-computed mean 2 exactly.
	one := ClusterSummary([]LinkedPair{
		mk("a", "one", 10, 11, false),
		mk("a", "one", 10, 13, false),
		mk("a", "one", 10, 12, false),
	}, 2)
	if len(one.Clusters) != 1 {
		t.Fatalf("clusters=%+v", one.Clusters)
	}
	s := one.Clusters[0]
	if s.N != 3 || s.NValues != 3 || s.Suppressed || s.MeanDelta == nil || *s.MeanDelta != 2 || s.CI95Lower == nil || *s.CI95Lower != 2 || s.CI95Upper == nil || *s.CI95Upper != 2 {
		t.Fatalf("single-block oracle failed: %+v", s)
	}
	// Censored outcomes are abstentions, not zero deltas; a zero denominator is
	// suppressed instead of producing NaN.
	zero := ClusterSummary([]LinkedPair{
		mk("a", "zero", 10, 0, true),
		mk("b", "zero", 10, 0, true),
	}, 2)
	if len(zero.Clusters) != 1 || zero.Clusters[0].N != 2 || zero.Clusters[0].NValues != 0 || !zero.Clusters[0].Suppressed || zero.Clusters[0].MeanDelta != nil {
		t.Fatalf("zero denominator not abstained: %+v", zero.Clusters)
	}
}
