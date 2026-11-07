package postgres

import (
	"encoding/json"
	"testing"
	"time"

	am "rai/server/internal/athletemodel"
	pf "rai/server/internal/performance"
	rd "rai/server/internal/readiness"
	rc "rai/server/internal/recovery"
	tl "rai/server/internal/trainingload"
	vo2 "rai/server/internal/vo2max"
	ve "rai/server/internal/vo2maxexternal"
)

func TestHistoryPointDecodesEveryV2Scalar(t *testing.T) {
	now := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	day := now.Format("2006-01-02")
	score := 71.0
	acute := 22.0
	ability := 55.0
	vo2v := 52.0
	cases := []struct {
		metric string
		value  float64
		unit   string
		body   any
	}{
		{"trainingLoad", acute, tl.Unit + "/day", tl.StateResult{Unit: tl.Unit, Days: []tl.DailyState{{Day: day, Quality: "complete", Acute: tl.KernelValue{Value: &acute}}}}},
		{"runningAbility", ability, "score_0_100", pf.Result{AsOf: now.UnixMicro(), Status: "available", Ability: &pf.Ability{Score: ability}}},
		{"physiologicalRecovery", score, "score_0_100", rc.Result{AsOf: now.UnixMicro(), Status: "available", Score: &score}},
		{"trainingReadiness", score, "score_0_100", rd.Result{AsOf: now.UnixMicro(), Status: "available", Score: &score}},
		{"raiVO2Max", vo2v, vo2.Unit, vo2.Result{AsOf: now.UnixMicro(), Status: "available", Unit: vo2.Unit, Value: &vo2v}},
		{"externalVO2Max", vo2v, ve.Unit, ve.Result{AsOf: now.UnixMicro(), Status: "available", Unit: ve.Unit, Value: &vo2v}},
	}
	for _, tc := range cases {
		t.Run(tc.metric, func(t *testing.T) {
			raw, err := json.Marshal(tc.body)
			if err != nil {
				t.Fatal(err)
			}
			p := historyPoint(tc.metric, day, "algorithm", "v2", "schema", now, raw)
			if p.Value == nil || *p.Value != tc.value || p.Unit != tc.unit || p.State != "available" {
				t.Fatalf("point=%+v", p)
			}
		})
	}
}

func TestHistoryTrendUsesRealTwentyEightDayPair(t *testing.T) {
	to, from := 15.0, 10.0
	unit := "RAI_AU/day"
	points := []am.HistoryPoint{
		{Date: "2026-11-07", Value: &to, Unit: unit, ModelVersion: "load/v2", PayloadVersion: "load/2"},
		{Date: "2026-11-06", Value: &to, Unit: unit, ModelVersion: "load/v2", PayloadVersion: "load/2"},
		{Date: "2026-10-10", Value: &from, Unit: unit, ModelVersion: "load/v2", PayloadVersion: "load/2"},
	}
	trend := historyTrend(points)
	if trend == nil || trend.Days != 28 || trend.Delta == nil || *trend.Delta != 5 || trend.FromDate != "2026-10-10" || trend.ToDate != "2026-11-07" {
		t.Fatalf("trend=%+v", trend)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*am.HistoryPoint)
	}{
		{"version", func(p *am.HistoryPoint) { p.ModelVersion = "load/v1" }},
		{"config", func(p *am.HistoryPoint) { p.ConfigHash = "different" }},
		{"mode", func(p *am.HistoryPoint) { p.Mode = "different" }},
		{"source", func(p *am.HistoryPoint) { p.Source = "different" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := append([]am.HistoryPoint(nil), points...)
			tc.mutate(&changed[2])
			broken := historyTrend(changed)
			if broken == nil || broken.Delta != nil || len(broken.ReasonCodes) != 1 || broken.ReasonCodes[0] != "history_trend_context_break" {
				t.Fatalf("break=%+v", broken)
			}
		})
	}
}
