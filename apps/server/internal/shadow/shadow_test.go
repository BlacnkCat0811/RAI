package shadow

import (
	"encoding/json"
	"testing"
)

func f(v float64) *float64 { return &v }

func TestEvaluateSuppressesSmallCohortsAndOmitsAthleteKeys(t *testing.T) {
	rows := []Observation{}
	for i := 0; i < 5; i++ {
		rows = append(rows, Observation{AthleteKey: "a" + string(rune('0'+i)), CohortKey: "c1", Candidate: f(10), Legacy: f(9)})
	}
	for i := 0; i < 4; i++ {
		rows = append(rows, Observation{AthleteKey: "b" + string(rune('0'+i)), CohortKey: "c2", Candidate: f(100), Legacy: f(1)})
	}
	report, err := Evaluate(Batch{DatasetID: "synthetic", Synthetic: true, CandidateMode: "v2", LegacyMode: "v1", Observations: rows, MinCohort: 5})
	if err != nil {
		t.Fatal(err)
	}
	if report.Paired != 9 || len(report.Cohorts) != 2 || len(report.Suppressed) != 1 || report.Suppressed[0] != "c2" {
		t.Fatalf("report=%+v", report)
	}
	if report.Cohorts[0].MeanAbsDiff == nil || *report.Cohorts[0].MeanAbsDiff != 1 || report.Cohorts[1].MeanAbsDiff != nil {
		t.Fatalf("cohorts=%+v", report.Cohorts)
	}
	encoded, _ := json.Marshal(report)
	var decoded map[string]any
	_ = json.Unmarshal(encoded, &decoded)
	text := string(encoded)
	if contains(text, "athleteKey") || contains(text, "a0") || contains(text, "b0") {
		t.Fatalf("individual key leaked: %s", text)
	}
	if report.AffectsActive || !report.SyntheticOnly {
		t.Fatalf("active/synthetic flags wrong: %+v", report)
	}
}

func TestEvaluateRefusesNonSyntheticOrAllSuppressed(t *testing.T) {
	if _, err := Evaluate(Batch{DatasetID: "real", Synthetic: false, CandidateMode: "v2", LegacyMode: "v1", Observations: []Observation{{AthleteKey: "a", CohortKey: "c", Candidate: f(1), Legacy: f(2)}}}); err != ErrNotSynthetic {
		t.Fatalf("expected non-synthetic rejection, got %v", err)
	}
	if _, err := Evaluate(Batch{DatasetID: "synthetic", Synthetic: true, CandidateMode: "v2", LegacyMode: "v1", MinCohort: 5, Observations: []Observation{{AthleteKey: "a", CohortKey: "c", Candidate: f(1), Legacy: f(2)}}}); err != ErrInvalid {
		t.Fatalf("expected suppression error, got %v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
