package readiness

import (
	"math"
	"testing"
)

func v4Fixture() V4Input {
	s := v2Fixture()
	s.Config = DefaultV4Config("UTC")
	score, acute, chronic := 82., 20., 20.
	ev := s.AsOf - Hour
	return V4Input{Snapshot: s, Physiology: Physiological{Score: &score, Mode: "complete", Status: "available", Confidence: .6, EvidenceAt: &ev, ValidBefore: s.Days[len(s.Days)-1].End}, SessionAvailable: true, SessionMode: "complete", Acute: &acute, Chronic: &chronic, Fatigue: []FatigueTerm{{End: s.AsOf - Hour, Low: .3, High: .3, TauLow: 18, TauHigh: 18}}}
}
func TestV4CapsAndEstimatedDoNotPretendComplete(t *testing.T) {
	in := v4Fixture()
	r, e := CalculateV4(in)
	if e != nil || r.Score == nil || math.Abs(*r.Score-82) > 1e-9 || r.Mode != "complete" {
		t.Fatal(e, r)
	}
	in.Physiology.Mode = "estimated"
	r, e = CalculateV4(in)
	if e != nil || r.Score == nil || r.Mode != "estimated" || math.Abs(*r.Score-82) > 1e-9 || r.Confidence > .4 {
		t.Fatal(e, r)
	}
	in.Physiology.Score = nil
	r, _ = CalculateV4(in)
	if r.Score != nil || r.Mode != "workout_only" {
		t.Fatal("missing physiology scored")
	}
	in = v4Fixture()
	in.Physiology.Status = "sync_incomplete"
	r, _ = CalculateV4(in)
	if r.Score != nil || r.Mode != "pending" {
		t.Fatal("pending got estimated")
	}
}

func TestV4EstimatedScoreIsNotCappedByAnyEstimateReason(t *testing.T) {
	for _, reason := range []string{"physiology", "session", "unknown", "baseline"} {
		t.Run(reason, func(t *testing.T) {
			in := v4Fixture()
			in.Fatigue = nil
			in.Physiology.Score = ptr(96)
			switch reason {
			case "physiology":
				in.Physiology.Mode = "estimated"
			case "session":
				in.SessionMode = "estimated"
			case "unknown":
				in.SessionUnknown = .01
			case "baseline":
				in.Chronic = nil
			}
			r, err := CalculateV4(in)
			if err != nil || r.Score == nil || *r.Score != 96 || r.Mode != "estimated" || r.Confidence > .4 {
				t.Fatal("estimated input silently capped readiness", err)
			}
			fresh, err := FreshenV4(r, r.AsOf+Hour)
			if err != nil || fresh.Score == nil || *fresh.Score != 96 || fresh.Mode != "estimated" {
				t.Fatal("reading the published estimate reintroduced a score cap", err)
			}
		})
	}
}

func TestV4RetainedCappedPolicyDoesNotChangeOnRead(t *testing.T) {
	in := v4Fixture()
	in.Snapshot.Config.Policy = PolicyV4Capped
	in.Physiology.Mode = "estimated"
	r, err := CalculateV4(in)
	if err != nil || r.Score == nil || *r.Score != 75 {
		t.Fatal("retained capped policy changed", err)
	}
	fresh, err := FreshenV4(r, r.AsOf+Hour)
	if err != nil || fresh.Score == nil || *fresh.Score != 75 {
		t.Fatal("retained publication was reinterpreted as uncapped", err)
	}
}
func TestV4UnknownRangeAndFrozenProjection(t *testing.T) {
	in := v4Fixture()
	in.Fatigue[0].Low = 1
	in.Fatigue[0].High = 4
	in.Fatigue[0].TauHigh = 72
	in.SessionMode = "estimated"
	in.SessionUnknown = .1
	r, e := CalculateV4(in)
	if e != nil || r.Score != nil || r.Range == nil || r.Range.High-r.Range.Low <= 10 {
		t.Fatal("wide range supplied point", e, r)
	}
	in = v4Fixture()
	in.Fatigue[0].Low = 2
	in.Fatigue[0].High = 2
	r, _ = CalculateV4(in)
	later, e := FreshenV4(r, in.Snapshot.AsOf+Hour)
	if e != nil || later.Score == nil || *later.Score < *r.Score {
		t.Fatal("fatigue cannot rise without new facts")
	}
	expired, e := FreshenV4(r, r.ValidBefore)
	if e != nil || expired.Score != nil {
		t.Fatal("day boundary leaked")
	}
	in.Fatigue[0].High = math.Inf(1)
	if _, e := CalculateV4(in); e == nil {
		t.Fatal("Inf accepted")
	}
}

// The fatigue bounds decay at different rates: a range can open after the
// publication and close again later. The projection follows it both ways and
// never leaves the day stuck on "workout only" or a stale reason behind.
func TestV4ProjectionReopensAfterAWideRange(t *testing.T) {
	in := v4Fixture()
	in.Fatigue[0].Low, in.Fatigue[0].High = 1.2, 3
	in.Fatigue[0].TauLow, in.Fatigue[0].TauHigh = 18, 48
	in.Fatigue[0].End = in.Snapshot.AsOf - 30*Hour
	in.SessionMode = "estimated"
	r, e := CalculateV4(in)
	if e != nil || r.Score != nil || r.Mode != "workout_only" || r.Quality != "estimated" {
		t.Fatal("fixture should start with a wide range", e, r.Mode, r.Quality, r.Range)
	}
	r.ValidBefore = in.Snapshot.AsOf + 400*Hour
	later, e := FreshenV4(r, in.Snapshot.AsOf+200*Hour)
	if e != nil || later.Score == nil || later.Mode != "estimated" || later.Status != "available" {
		t.Fatal("a narrowed range stayed withheld", e, later.Mode, later.Status, later.Range)
	}
	for _, code := range later.Reasons {
		if code == "load_sensitivity_wide" {
			t.Fatal("stale wide-range reason")
		}
	}
	again, _ := FreshenV4(r, in.Snapshot.AsOf+Hour)
	n := 0
	for _, code := range again.Reasons {
		if code == "load_sensitivity_wide" {
			n++
		}
	}
	if again.Score != nil || n != 1 || len(r.Reasons) != len(again.Reasons) {
		t.Fatal("re-evaluation duplicated or lost the wide-range reason", again.Reasons, r.Reasons)
	}
}
