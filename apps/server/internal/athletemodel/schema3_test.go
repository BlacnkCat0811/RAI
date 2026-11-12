package athletemodel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	rd "rai/server/internal/readiness"
	rc "rai/server/internal/recovery"
	rf "rai/server/internal/runfitness"
	sr "rai/server/internal/sessionrecovery"
	tl "rai/server/internal/trainingload"
	wf "rai/server/internal/workoutfeatures"
)

func syntheticFitness(date string, n int) rf.State {
	ref := &rf.Reference{MaxHR: 190, RestHR: 50, MaxBasis: "measured", RestBasis: "user", Hash: "synthetic"}
	var obs []rf.SingleResult
	start, _ := time.Parse(time.DateOnly, "2026-10-01")
	for i := 0; i < n; i++ {
		d := start.AddDate(0, 0, i)
		f := &wf.RunFitnessFeatures{Schema: wf.RunFitnessSchema, DistanceCheck: "consistent", Indoor: new(bool), TemperatureC: number(15), Humidity: number(.5), RunningSeconds: 1800}
		for k := 0; k < 9; k++ {
			f.Segments = append(f.Segments, wf.RunFitnessSegment{Kind: "steady", Start: int64(120+k*180) * 1e6, End: int64(300+k*180) * 1e6, Seconds: 180, RunningAt: float64(120 + k*180),
				Speed: 3, FlatSpeed: number(3), Grade: number(0), MaxAbsGrade: number(0), HR: 145, SpeedCV: .02, MinSpeed: 2.9, Coverage: 1, GradeSupport: 1, SpeedEpoch: "w", HREpoch: "w"})
		}
		obs = append(obs, rf.Single(rf.SingleInput{WorkoutID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i), Day: d.Format(time.DateOnly), Start: d.Add(7 * time.Hour).UnixMicro(), End: d.Add(8 * time.Hour).UnixMicro(), Features: f, Reference: ref}))
	}
	return rf.CalculateState(rf.StateInput{DatasetID: "synthetic", Date: date, Observations: obs})
}

func TestRunningFitnessSharedContract(t *testing.T) {
	s := NewSnapshotV4(1794369600000000, 1, "Asia/Shanghai", "2026-11-11", Assignment{Channel: "active", StackID: "synthetic-v4", Version: 1})
	at := s.AsOf
	score := 70.
	evidence := at - 24*sr.Hour
	recovery := rc.V3Result{V2Result: rc.V2Result{Result: rc.Result{ModelVersion: rc.ModelV3, Schema: rc.SchemaV3, Status: "available", Quality: "estimated", Score: &score, BaselineState: "established", Confidence: .3, AsOf: at}}, Mode: "estimated", BaselineNights: 28, TrendNights: 6, EvidenceAt: &evidence, ValidBefore: at + 12*sr.Hour, MissingComponents: []string{"hrv"}, Components: map[string]rc.V3Component{"hrv": {State: "missing_after_sync", Trend: number(50), TrendNights: 6, BaselineNights: 28, EvidenceAt: &evidence, Source: "synthetic-watch"}}}
	s.PhysiologicalRecovery = ProjectRecoveryV3(recovery, at, at)
	s.TrainingReadiness = ProjectReadinessV4(rd.V4Result{Result: rd.Result{ModelVersion: rd.ModelV4, Schema: rd.SchemaV4, AsOf: at, Status: "available", Quality: "estimated", Score: &score, ValidBefore: at + 12*sr.Hour}, Mode: "estimated", PhysiologicalCap: &score, SessionCap: number(80), Range: &rd.ScoreRange{Low: 70, High: 72}, EvidenceAt: &evidence, WeeklyCap: number(100)}, at, at)
	session := ProjectSessionRecovery(sr.Result{Schema: sr.Schema, AsOf: at, Status: "available", Mode: "estimated", RemainingHours: number(20), Range: &sr.Range{Low: 18, High: 20}, Scope: "recorded_running_next_hard_session", ValidBefore: at + 12*sr.Hour}, at, at)
	s.SessionRecovery = &session
	focus := ProjectFocus(tl.FocusResult{AsOf: at, Windows: []tl.FocusWindow{{WindowDays: 7, Buckets: tl.Buckets{Low: 100, High: 30, Unclassified: 20}, UnknownWorkouts: 1, ClassifiedCoverage: number(130. / 150), Denominator: "known_load", Method: "hrr_proxy"}}}, at, at)
	s.LoadFocus = &focus
	fitness := ProjectRunningFitness(syntheticFitness("2026-10-25", 25), "synthetic-publication", at, at)
	s.RunningFitness = &fitness
	b, e := json.MarshalIndent(s, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	b = append(b, '\n')
	for _, path := range []string{"../../../../contracts/fixtures/running-fitness-v4.json", "../../../../ios/RAI/Tests/ProductCoreTests/Fixtures/RunningFitnessV4.json"} {
		if os.Getenv("RAI_UPDATE_M7_GOLDENS") == "1" {
			if e := os.WriteFile(path, b, 0644); e != nil {
				t.Fatal(e)
			}
		}
		want, e := os.ReadFile(path)
		if e != nil || !bytes.Equal(b, want) {
			t.Fatal("shared v4 fixture differs", e)
		}
	}
	v := s.RunningFitness.Value
	if s.SchemaVersion != Schema4 || s.RunningAbility.Value != nil || v == nil || v.Overall.Score == nil || v.Dimensions.Speed.Rating != nil || len(v.Zones.Zones) != 6 || len(v.Predictions) != 4 {
		t.Fatal("schema4 running fitness contract")
	}
}
