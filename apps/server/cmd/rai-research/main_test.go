package main

import (
	"testing"
)

func TestRunSyntheticResearchReport(t *testing.T) {
	body := []byte(`{
	  "snapshot":{"DatasetID":"synthetic-ds","Model":"rai.vo2max.submax/v1","ManifestSHA":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","AsOf":"2025-04-01T00:00:00Z","KnownAt":"2025-04-01T00:00:00Z","CohortID":"synthetic","Athletes":4,"Observations":[
	    {"AthleteKey":"a","ClusterKey":"c1","PredictedAt":"2025-01-01T00:00:00Z","HorizonDays":30,"Value":50,"FeaturesKnownAt":"2025-01-01T00:00:00Z","Version":"v1","Mode":"experimental"},
	    {"AthleteKey":"b","ClusterKey":"c1","PredictedAt":"2025-02-01T00:00:00Z","HorizonDays":30,"Value":52,"FeaturesKnownAt":"2025-02-01T00:00:00Z","Version":"v1","Mode":"experimental"},
	    {"AthleteKey":"c","ClusterKey":"c2","PredictedAt":"2025-03-01T00:00:00Z","HorizonDays":30,"Value":48,"FeaturesKnownAt":"2025-03-01T00:00:00Z","Version":"v1","Mode":"experimental"}]},
	  "outcomes":[
	    {"AthleteKey":"a","ClusterKey":"c1","ObservedAt":"2025-01-20T00:00:00Z","Value":50,"Source":"independent","Independent":true},
	    {"AthleteKey":"b","ClusterKey":"c1","ObservedAt":"2025-02-20T00:00:00Z","Value":53,"Source":"independent","Independent":true},
	    {"AthleteKey":"c","ClusterKey":"c2","ObservedAt":"2025-03-20T00:00:00Z","Value":49,"Source":"independent","Independent":true}],
	  "temporalCutoff":"2025-02-15T00:00:00Z","holdoutFraction":0.5,"splitSeed":7,"minCluster":2,
	  "addendum":{"CohortID":"synthetic","Purpose":"synthetic engineering verification","Model":"rai.vo2max.submax","Version":"v1","PrimaryOutcome":"independent submax VO2","OutcomeSource":"independent test","ObservationFrom":"2025-01-01T00:00:00Z","ObservationTo":"2025-06-01T00:00:00Z","AcceptanceThreshold":0.05,"SampleSizeJustification":"synthetic fixture only","MinDetectableEffect":0.5,"Alpha":0.05,"Power":0.8,"PreregisteredAt":"2025-01-01T00:00:00Z","Reviewer":"synthetic","AuthorizationBasis":"synthetic engineering","WithdrawalProcedure":"withdraw without deleting history","StoppingRule":"stop on privacy or independence breach","PrivacyApproved":true,"ExpiresAt":"2027-01-01T00:00:00Z"}
	}`)
	r, err := run(body)
	if err != nil {
		t.Fatal(err)
	}
	if !r.SyntheticOnly || r.Observations != 3 || r.Paired != 3 || r.TemporalTrain != 2 || r.TemporalTest != 1 {
		t.Fatalf("report=%+v", r)
	}
	if r.AthleteTrain+r.AthleteTest != 3 || !r.AddendumValid {
		t.Fatalf("split/addendum=%+v", r)
	}
	if len(r.Clusters) != 2 || len(r.SuppressedClusters) != 1 {
		t.Fatalf("clusters=%+v", r.Clusters)
	}
}

func TestRunWithPseudonymWritesSeparateMapping(t *testing.T) {
	body := []byte(`{
	  "snapshot":{"DatasetID":"synthetic-ds","Model":"rai.vo2max.submax/v1","ManifestSHA":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","AsOf":"2025-04-01T00:00:00Z","KnownAt":"2025-04-01T00:00:00Z","CohortID":"synthetic","Athletes":1,"Observations":[
	    {"AthleteKey":"subject-a","ClusterKey":"c1","PredictedAt":"2025-01-01T00:00:00Z","HorizonDays":30,"Value":50,"FeaturesKnownAt":"2025-01-01T00:00:00Z","Version":"v1","Mode":"experimental"}]},
	  "outcomes":[{"AthleteKey":"subject-a","ClusterKey":"c1","ObservedAt":"2025-01-20T00:00:00Z","Value":51,"Source":"independent","Independent":true}],
	  "temporalCutoff":"2025-02-15T00:00:00Z","holdoutFraction":0.5,"splitSeed":7,"minCluster":1,
	  "addendum":{"CohortID":"synthetic","Purpose":"synthetic engineering verification","Model":"rai.vo2max.submax","Version":"v1","PrimaryOutcome":"independent submax VO2","OutcomeSource":"independent test","ObservationFrom":"2025-01-01T00:00:00Z","ObservationTo":"2025-06-01T00:00:00Z","AcceptanceThreshold":0.05,"SampleSizeJustification":"synthetic fixture only","MinDetectableEffect":0.5,"Alpha":0.05,"Power":0.8,"PreregisteredAt":"2025-01-01T00:00:00Z","Reviewer":"synthetic","AuthorizationBasis":"synthetic engineering","WithdrawalProcedure":"withdraw without deleting history","StoppingRule":"stop on privacy or independence breach","PrivacyApproved":true,"ExpiresAt":"2027-01-01T00:00:00Z"}
	}`)
	r, mapping, err := runWithPseudonym(body, []byte("synthetic-secret-0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Paired != 1 || mapping["subject-a"] == "" || mapping["subject-a"] == "subject-a" {
		t.Fatalf("report=%+v mapping=%v", r, mapping)
	}
	for key, value := range mapping {
		if key == value || len(value) < 8 {
			t.Fatalf("invalid mapping %q=%q", key, value)
		}
	}
}
