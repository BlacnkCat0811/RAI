package research

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

var testBaseTime = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

func testDay(offset int) time.Time {
	return testBaseTime.AddDate(0, 0, offset)
}

func testValue(value float64) *float64 {
	return &value
}

func testSnapshot(t *testing.T, observations []Prediction) ProspectiveSnapshot {
	t.Helper()
	snapshot, err := CreateSnapshot(ProspectiveSnapshot{
		DatasetID:    "ds-synthetic-001",
		Model:        "rai.endurance",
		ManifestSHA:  "sha256:synthetic",
		AsOf:         testDay(30),
		KnownAt:      testDay(30),
		CohortID:     "cohort-synthetic-001",
		Athletes:     2,
		Observations: observations,
	})
	if err != nil {
		t.Fatalf("CreateSnapshot: %v", err)
	}
	return snapshot
}

func testPrediction(athlete, cluster string, predictedAt time.Time, horizonDays int, value float64) Prediction {
	return Prediction{
		AthleteKey:      athlete,
		ClusterKey:      cluster,
		PredictedAt:     predictedAt,
		HorizonDays:     horizonDays,
		Value:           testValue(value),
		State:           PredictionStateProspective,
		Version:         "rai.endurance/v1",
		Mode:            "research",
		FeaturesKnownAt: predictedAt.Add(-time.Hour),
	}
}

func TestCreateSnapshotRejectsFutureLeak(t *testing.T) {
	valid, err := CreateSnapshot(ProspectiveSnapshot{
		DatasetID:   "ds-synthetic-001",
		Model:       "rai.endurance",
		ManifestSHA: "sha256:synthetic",
		AsOf:        testDay(10),
		KnownAt:     testDay(10),
		CohortID:    "cohort-synthetic-001",
		Observations: []Prediction{
			testPrediction("athlete-a", "cluster-1", testDay(1), 14, 42),
		},
	})
	if err != nil {
		t.Fatalf("valid snapshot rejected: %v", err)
	}
	if got := len(valid.Observations); got != 1 {
		t.Fatalf("valid snapshot observations = %d, want 1", got)
	}

	future := valid
	future.KnownAt = testDay(11)
	if _, err = CreateSnapshot(future); !errors.Is(err, ErrFutureLeak) {
		t.Fatalf("future knownAt error = %v, want ErrFutureLeak", err)
	}

	cases := []struct {
		name   string
		mutate func(*ProspectiveSnapshot)
	}{
		{"empty dataset", func(s *ProspectiveSnapshot) { s.DatasetID = " " }},
		{"empty model", func(s *ProspectiveSnapshot) { s.Model = "" }},
		{"duplicate athlete and time", func(s *ProspectiveSnapshot) {
			s.Observations = append(s.Observations, s.Observations[0])
			s.Observations[1].ClusterKey = "cluster-2"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			broken := valid
			tc.mutate(&broken)
			if _, err := CreateSnapshot(broken); !errors.Is(err, ErrInvalidSnapshot) {
				t.Fatalf("error = %v, want ErrInvalidSnapshot", err)
			}
		})
	}
}

func TestFreezeKnownAtKeepsKnownRowsOnly(t *testing.T) {
	observations := []Prediction{
		testPrediction("athlete-a", "cluster-1", testDay(1), 14, 10),
		testPrediction("athlete-b", "cluster-1", testDay(2), 14, 20),
		testPrediction("athlete-c", "cluster-1", testDay(3), 14, 30),
	}
	observations[0].FeaturesKnownAt = testDay(1)
	observations[1].FeaturesKnownAt = testDay(5)
	observations[2].FeaturesKnownAt = testDay(6)
	snapshot := testSnapshot(t, observations)

	frozen := FreezeKnownAt(snapshot, testDay(5))
	if got := len(frozen.Observations); got != 2 {
		t.Fatalf("frozen observations = %d, want 2", got)
	}
	if frozen.Observations[0].AthleteKey != "athlete-a" || frozen.Observations[1].AthleteKey != "athlete-b" {
		t.Fatalf("unexpected frozen athletes: %+v", frozen.Observations)
	}
	if got := len(snapshot.Observations); got != 3 {
		t.Fatalf("FreezeKnownAt mutated input, observations = %d", got)
	}

	snapshot.Observations[2].FeaturesKnownAt = testDay(31)
	if err := VerifyNoFutureLeak(snapshot); !errors.Is(err, ErrFutureLeak) {
		t.Fatalf("future feature error = %v, want ErrFutureLeak", err)
	}
	observations[2].FeaturesKnownAt = testDay(6)
	if err := VerifyNoFutureLeak(testSnapshot(t, observations)); err != nil {
		t.Fatalf("VerifyNoFutureLeak: %v", err)
	}
}

func TestTemporalSplitUsesCutoff(t *testing.T) {
	observations := []Prediction{
		testPrediction("athlete-a", "cluster-1", testDay(1), 14, 10),
		testPrediction("athlete-b", "cluster-1", testDay(2), 14, 20),
		testPrediction("athlete-c", "cluster-1", testDay(3), 14, 30),
		testPrediction("athlete-d", "cluster-1", testDay(4), 14, 40),
	}
	report := TemporalSplit(observations, testDay(3))
	if report.TrainCount != 2 || report.TestCount != 2 {
		t.Fatalf("counts = %d/%d, want 2/2", report.TrainCount, report.TestCount)
	}
	if report.Test[0].AthleteKey != "athlete-c" {
		t.Fatalf("cutoff row belongs to %q, want test partition", report.Test[0].AthleteKey)
	}
}

func TestAthleteSplitIsDisjointAndDeterministic(t *testing.T) {
	keys := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "a"}
	first, err := AthleteSplit(keys, 0.3, 20261107)
	if err != nil {
		t.Fatalf("AthleteSplit: %v", err)
	}
	second, err := AthleteSplit(keys, 0.3, 20261107)
	if err != nil {
		t.Fatalf("AthleteSplit second call: %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("AthleteSplit is not deterministic: %+v != %+v", first, second)
	}
	if first.TrainCount != 7 || first.TestCount != 3 {
		t.Fatalf("counts = %d/%d, want 7/3", first.TrainCount, first.TestCount)
	}
	train := make(map[string]bool, len(first.Train))
	for _, key := range first.Train {
		train[key] = true
	}
	for _, key := range first.Test {
		if train[key] {
			t.Fatalf("athlete %q appears in both partitions", key)
		}
	}
	if got := len(first.Train) + len(first.Test); got != 10 {
		t.Fatalf("partition sizes = %d, want 10 unique athletes", got)
	}

	if _, err := AthleteSplit(keys, 1.5, 1); err == nil {
		t.Fatal("invalid holdout fraction accepted")
	}
}

func TestLinkOutcomesWindowAndWithdrawal(t *testing.T) {
	withdrawnAt := testDay(1)
	predictions := []Prediction{
		testPrediction("athlete-a", "cluster-1", testDay(0), 14, 10),
		testPrediction("athlete-b", "cluster-1", testDay(0), 14, 20),
		testPrediction("athlete-c", "cluster-2", testDay(0), 7, 30),
		testPrediction("athlete-d", "cluster-2", testDay(0), 7, 40),
	}
	predictions[3].State = PredictionStateWithdrawn
	predictions[3].WithdrawnAt = &withdrawnAt

	outcomes := []Outcome{
		{AthleteKey: "athlete-a", ClusterKey: "cluster-1", ObservedAt: testDay(14), Value: testValue(12), Source: "synthetic", Independent: true},
		{AthleteKey: "athlete-b", ClusterKey: "cluster-1", ObservedAt: testDay(15), Value: testValue(25), Source: "synthetic", Independent: true},
		{AthleteKey: "athlete-c", ClusterKey: "cluster-2", ObservedAt: testDay(7), Value: testValue(33), Source: "synthetic", Independent: true},
		{AthleteKey: "athlete-d", ClusterKey: "cluster-2", ObservedAt: testDay(2), Value: testValue(44), Source: "synthetic", Independent: true},
		{AthleteKey: "athlete-a", ClusterKey: "cluster-3", ObservedAt: testDay(1), Value: testValue(99), Source: "synthetic", Independent: true},
	}

	result := LinkOutcomes(predictions, outcomes)
	if got := len(result.Pairs); got != 2 {
		t.Fatalf("pairs = %d, want 2", got)
	}
	if got := len(result.UnpairedPredictions); got != 1 || result.UnpairedPredictions[0].AthleteKey != "athlete-b" {
		t.Fatalf("unpaired predictions = %+v, want athlete-b only", result.UnpairedPredictions)
	}
	if got := len(result.UnpairedOutcomes); got != 3 {
		t.Fatalf("unpaired outcomes = %d, want 3", got)
	}
	if got := len(result.Withdrawn); got != 1 || result.Withdrawn[0].AthleteKey != "athlete-d" {
		t.Fatalf("withdrawn = %+v, want athlete-d only", result.Withdrawn)
	}
	if result.Pairs[0].Prediction.AthleteKey != "athlete-a" || result.Pairs[0].Outcome.Value == nil ||
		*result.Pairs[0].Outcome.Value != 12 {
		t.Fatalf("unexpected first pair: %+v", result.Pairs[0])
	}
}

func TestClusterSummarySuppressesSmallGroups(t *testing.T) {
	pairs := make([]LinkedPair, 0, 9)
	addPair := func(cluster string, predicted, observed float64, censored bool) {
		pairs = append(pairs, LinkedPair{
			Prediction: Prediction{ClusterKey: cluster, Value: testValue(predicted)},
			Outcome:    Outcome{ClusterKey: cluster, Value: testValue(observed), Censored: censored},
		})
	}
	for i := 0; i < 4; i++ {
		addPair("cluster-large", 10, 12, false) // delta +2 each
	}
	for i := 0; i < 2; i++ {
		addPair("cluster-small", 10, 11, false)
	}
	addPair("cluster-censored", 10, 20, false)
	addPair("cluster-censored", 10, 0, true)
	addPair("cluster-censored", 10, 0, true)

	report := ClusterSummary(pairs, 3)
	if got := len(report.Clusters); got != 3 {
		t.Fatalf("clusters = %d, want 3", got)
	}
	if got := report.SuppressedClusters; !reflect.DeepEqual(got, []string{"cluster-censored", "cluster-small"}) {
		t.Fatalf("suppressed = %v", got)
	}

	byKey := make(map[string]ClusterStat, len(report.Clusters))
	for _, cluster := range report.Clusters {
		byKey[cluster.ClusterKey] = cluster
	}

	large := byKey["cluster-large"]
	if large.ClusterKey != "cluster-large" || large.N != 4 || large.NValues != 4 || large.Suppressed {
		t.Fatalf("large cluster = %+v", large)
	}
	if large.MeanDelta == nil || *large.MeanDelta != 2 {
		t.Fatalf("large mean = %v, want 2", large.MeanDelta)
	}
	if large.CI95Lower == nil || large.CI95Upper == nil ||
		*large.CI95Lower > *large.MeanDelta || *large.MeanDelta > *large.CI95Upper {
		t.Fatalf("large CI = [%v,%v], mean %v", large.CI95Lower, large.CI95Upper, large.MeanDelta)
	}

	for _, key := range []string{"cluster-censored", "cluster-small"} {
		cluster := byKey[key]
		if !cluster.Suppressed {
			t.Fatalf("cluster %q not suppressed: %+v", key, cluster)
		}
		if cluster.MeanDelta != nil || cluster.CI95Lower != nil || cluster.CI95Upper != nil {
			t.Fatalf("suppressed cluster %q leaked values: %+v", key, cluster)
		}
	}
	censored := byKey["cluster-censored"]
	if censored.N != 3 || censored.NValues != 1 {
		t.Fatalf("censored counts = %d/%d, want N=3 NValues=1", censored.N, censored.NValues)
	}
}

func TestClusterBootstrapIsDeterministic(t *testing.T) {
	pairs := make([]LinkedPair, 0, 12)
	for i := 0; i < 12; i++ {
		predicted := 10 + float64(i)
		observed := predicted + float64((i*7)%5-2)
		pairs = append(pairs, LinkedPair{
			Prediction: Prediction{AthleteKey: fmt.Sprintf("athlete-%02d", i), ClusterKey: "cluster-1", Value: testValue(predicted)},
			Outcome:    Outcome{AthleteKey: fmt.Sprintf("athlete-%02d", i), ClusterKey: "cluster-1", Value: testValue(observed)},
		})
	}

	first := ClusterSummary(pairs, 1)
	second := ClusterSummary(pairs, 1)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("bootstrap is not deterministic:\n%+v\n%+v", first, second)
	}

	reversed := make([]LinkedPair, len(pairs))
	for i := range pairs {
		reversed[i] = pairs[len(pairs)-1-i]
	}
	if third := ClusterSummary(reversed, 1); !reflect.DeepEqual(first, third) {
		t.Fatalf("bootstrap depends on pair order:\n%+v\n%+v", first, third)
	}

	stat := first.Clusters[0]
	if stat.MeanDelta == nil || stat.CI95Lower == nil || stat.CI95Upper == nil {
		t.Fatalf("missing bootstrap output: %+v", stat)
	}
	if *stat.CI95Lower > *stat.CI95Upper {
		t.Fatalf("inverted CI: %+v", stat)
	}
}

func TestWithdrawPreservesHistory(t *testing.T) {
	snapshot := testSnapshot(t, []Prediction{
		testPrediction("athlete-a", "cluster-1", testDay(1), 14, 10),
		testPrediction("athlete-b", "cluster-1", testDay(1), 14, 20),
	})

	withdrawn, count := Withdraw(snapshot, []string{"athlete-a"}, testDay(10))
	if count != 1 {
		t.Fatalf("withdrawn count = %d, want 1", count)
	}
	if len(withdrawn.Observations) != 2 {
		t.Fatalf("withdraw removed history: %d observations", len(withdrawn.Observations))
	}
	first := withdrawn.Observations[0]
	if first.WithdrawnAt == nil || !first.WithdrawnAt.Equal(testDay(10)) ||
		first.State != PredictionStateWithdrawn {
		t.Fatalf("first observation not withdrawn: %+v", first)
	}
	if withdrawn.Observations[1].WithdrawnAt != nil {
		t.Fatalf("unrelated athlete withdrawn: %+v", withdrawn.Observations[1])
	}
	if snapshot.Observations[0].WithdrawnAt != nil {
		t.Fatal("Withdraw mutated the input snapshot")
	}

	again, count := Withdraw(withdrawn, []string{"athlete-a"}, testDay(20))
	if count != 0 {
		t.Fatalf("second withdrawal count = %d, want 0", count)
	}
	if again.Observations[0].WithdrawnAt == nil || !again.Observations[0].WithdrawnAt.Equal(testDay(10)) {
		t.Fatalf("first withdrawal time was overwritten: %+v", again.Observations[0])
	}

	linked := LinkOutcomes(again.Observations, []Outcome{
		{AthleteKey: "athlete-a", ClusterKey: "cluster-1", ObservedAt: testDay(2), Value: testValue(12), Independent: true},
	})
	if len(linked.Pairs) != 0 || len(linked.Withdrawn) != 1 {
		t.Fatalf("withdrawn prediction was linked: %+v", linked)
	}
}

func TestValidateAddendumGate(t *testing.T) {
	valid := StudyAddendum{
		CohortID:                "cohort-synthetic-001",
		Purpose:                 "synthetic prospective model evaluation",
		Model:                   "rai.endurance",
		Version:                 "v1",
		PrimaryOutcome:          "race_time",
		OutcomeSource:           "independent race result",
		ObservationFrom:         testDay(1),
		ObservationTo:           testDay(90),
		AcceptanceThreshold:     0.05,
		SampleSizeJustification: "synthetic power calculation",
		MinDetectableEffect:     0.5,
		Alpha:                   0.05,
		Power:                   0.8,
		PreregisteredAt:         testDay(0),
		Reviewer:                "reviewer-1",
		AuthorizationBasis:      "synthetic engineering approval",
		WithdrawalProcedure:     "withdraw predictions without deleting history",
		StoppingRule:            "stop on privacy or independence breach",
		PrivacyApproved:         true,
		ExpiresAt:               testDay(365),
	}
	open, err := ValidateAddendum(valid)
	if err != nil || !open {
		t.Fatalf("valid addendum: open=%v err=%v", open, err)
	}

	cases := []struct {
		name   string
		mutate func(*StudyAddendum)
	}{
		{"missing cohort", func(a *StudyAddendum) { a.CohortID = "" }},
		{"missing purpose", func(a *StudyAddendum) { a.Purpose = "" }},
		{"missing primary outcome", func(a *StudyAddendum) { a.PrimaryOutcome = "" }},
		{"missing outcome source", func(a *StudyAddendum) { a.OutcomeSource = "" }},
		{"missing observation window", func(a *StudyAddendum) { a.ObservationTo = time.Time{} }},
		{"invalid observation window", func(a *StudyAddendum) { a.ObservationTo = a.ObservationFrom }},
		{"missing acceptance threshold", func(a *StudyAddendum) { a.AcceptanceThreshold = 0 }},
		{"missing sample size justification", func(a *StudyAddendum) { a.SampleSizeJustification = "" }},
		{"missing minimum detectable effect", func(a *StudyAddendum) { a.MinDetectableEffect = 0 }},
		{"missing reviewer", func(a *StudyAddendum) { a.Reviewer = "" }},
		{"missing authorization", func(a *StudyAddendum) { a.AuthorizationBasis = "" }},
		{"missing withdrawal procedure", func(a *StudyAddendum) { a.WithdrawalProcedure = "" }},
		{"missing stopping rule", func(a *StudyAddendum) { a.StoppingRule = "" }},
		{"missing preregistration", func(a *StudyAddendum) { a.PreregisteredAt = time.Time{} }},
		{"missing privacy approval", func(a *StudyAddendum) { a.PrivacyApproved = false }},
		{"missing expiry", func(a *StudyAddendum) { a.ExpiresAt = time.Time{} }},
		{"non rai model", func(a *StudyAddendum) { a.Model = "other.endurance" }},
		{"empty version", func(a *StudyAddendum) { a.Version = "" }},
		{"invalid alpha", func(a *StudyAddendum) { a.Alpha = 0 }},
		{"invalid power", func(a *StudyAddendum) { a.Power = 1.5 }},
		{"expiry before preregistration", func(a *StudyAddendum) { a.ExpiresAt = a.PreregisteredAt.Add(-time.Hour) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addendum := valid
			tc.mutate(&addendum)
			open, err := ValidateAddendum(addendum)
			if open || err == nil {
				t.Fatalf("gate open=%v err=%v, want closed with error", open, err)
			}
			if !errors.Is(err, ErrAddendumInvalid) {
				t.Fatalf("error = %v, want ErrAddendumInvalid", err)
			}
		})
	}
}

func TestAddendumGateCoversEachProductModel(t *testing.T) {
	base := StudyAddendum{
		CohortID: "cohort-synthetic", Purpose: "synthetic per-model gate", Version: "v1",
		PrimaryOutcome: "independent outcome", OutcomeSource: "independent test",
		ObservationFrom: testDay(1), ObservationTo: testDay(90), AcceptanceThreshold: 0.05,
		SampleSizeJustification: "synthetic precision", MinDetectableEffect: 0.5,
		Alpha: 0.05, Power: 0.8, PreregisteredAt: testDay(0), Reviewer: "reviewer",
		AuthorizationBasis: "synthetic approval", WithdrawalProcedure: "append-only withdrawal",
		StoppingRule: "stop on privacy breach", PrivacyApproved: true, ExpiresAt: testDay(365),
	}
	for _, model := range []string{
		"rai.vo2max.submax", "rai.vo2max.external_projection", "rai.recovery.physiological",
		"rai.readiness.training", "rai.running_ability", "rai.training_load.hrr_integral",
	} {
		a := base
		a.Model = model
		if open, err := ValidateAddendum(a); err != nil || !open {
			t.Fatalf("model %s gate=%v err=%v", model, open, err)
		}
		a.StoppingRule = ""
		if open, err := ValidateAddendum(a); err == nil || open {
			t.Fatalf("model %s gate stayed open without stopping rule", model)
		}
	}
}
