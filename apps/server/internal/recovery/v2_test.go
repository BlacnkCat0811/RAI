package recovery

import (
	"math"
	"testing"
	"time"
)

func nightInput(t *testing.T) V2Input {
	in := golden(t, "UTC", "2026-03-29", 28)
	in.Config = DefaultV2Config("UTC")
	for i := range in.Facts {
		f := &in.Facts[i]
		if f.Type == HRV {
			d := in.Days[len(in.Days)-1]
			for _, day := range in.Days {
				if f.End >= day.Start && f.End < day.End {
					d = day
					break
				}
			}
			f.Start = d.Start + 3*3600*1e6
			f.End = f.Start + 30*60*1e6
		}
		if f.Type == Sleep {
			d := in.Days[len(in.Days)-1]
			for _, day := range in.Days {
				if f.End >= day.Start && f.End < day.End {
					d = day
					break
				}
			}
			f.Start = d.Start - 2*3600*1e6
			f.End = d.Start + 7*3600*1e6
		}
	}
	var copies []Fact
	for _, f := range in.Facts {
		if f.Type == HRV {
			f.ID += "-b"
			f.SemanticHash = Hash(struct {
				ID string
				V  float64
			}{f.ID, *f.Value})
			f.Hash = Hash(f)
			copies = append(copies, f)
		}
	}
	in.Facts = append(in.Facts, copies...)
	return V2Input{Input: in, SleepTargets: []SleepTarget{{ID: "t", Hours: 9, EffectiveFrom: in.Days[0].Start, KnownAt: in.Days[0].Start, Source: "typed"}}}
}

func TestV2NightSDNNAndFormula(t *testing.T) {
	in := nightInput(t)
	r, err := CalculateV2(in)
	if err != nil {
		t.Fatal(err)
	}
	if r.SleepRegularity.Quality != "complete" || r.SleepTarget.Hours != 9 {
		t.Fatalf("regularity/target: %+v %+v", r.SleepRegularity, r.SleepTarget)
	}
	if r.Score == nil || math.Abs(*r.Score-100) > 1e-6 {
		t.Fatalf("perfect day should be 100: score=%v status=%s reasons=%v sdnn=%+v rhr=%+v base=%+v", r.Score, r.Status, r.Reasons, r.SDNNDeviation, r.RHRDeviation, r.BaselineState)
	}
	// SDNN z=2 -> aH=.5; dominant autonomic burden .125 -> 88.25.
	in2 := nightInput(t)
	d := in2.Days[len(in2.Days)-1]
	for i := range in2.Facts {
		f := &in2.Facts[i]
		if f.Type == HRV && f.End >= d.Start {
			f.Value = ptr(math.Exp(math.Log(50) - 0.2))
		}
	}
	r2, err := CalculateV2(in2)
	if err != nil {
		t.Fatal(err)
	}
	want := 100 * math.Exp(-.25*.5)
	if r2.Score == nil || math.Abs(*r2.Score-want) > 1e-6 {
		t.Fatalf("zH=2 score %v want %v reasons %v", r2.Score, want, r2.Reasons)
	}
}

func TestV2NightContextMissing(t *testing.T) {
	in := nightInput(t)
	d := in.Days[len(in.Days)-1]
	for i := range in.Facts {
		f := &in.Facts[i]
		if f.Type == HRV && f.End >= d.Start {
			f.Start = d.Start + 12*3600*1e6
			f.End = f.Start
		}
		if f.Type == Sleep {
			f.Start = d.Start + 13*3600*1e6
			f.End = d.Start + 14*3600*1e6
		}
	}
	r, err := CalculateV2(in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Score != nil || r.AutonomicOnlyV2 != nil {
		t.Fatalf("no night context must not score: %+v", r)
	}
	found := false
	for _, x := range r.Reasons {
		if x == "night_context_missing" {
			found = true
		}
	}
	if !found {
		t.Fatalf("reasons %v", r.Reasons)
	}
}

func TestV2SleepMissingIsPartialNotZero(t *testing.T) {
	in := nightInput(t)
	for i := range in.Facts {
		f := &in.Facts[i]
		if f.Type == Sleep {
			d := in.Days[len(in.Days)-1]
			for _, day := range in.Days {
				if f.End >= day.Start && f.End < day.End {
					d = day
					break
				}
			}
			// A 30 min nap overlapping the night SDNN samples: the SDNN is
			// still recorded asleep, but no main sleep episode exists.
			f.Start = d.Start + 3*3600*1e6
			f.End = f.Start + 30*60*1e6
		}
	}
	r, err := CalculateV2(in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Score != nil || r.AutonomicOnlyV2 == nil || r.Status != "partial" {
		t.Fatalf("sleep missing must be partial: %+v", r)
	}
}

func TestCircularMidpoint(t *testing.T) {
	if d := circularDeltaHours(23+50.0/60, 10.0/60); math.Abs(d-20.0/60) > 1e-9 {
		t.Fatalf("23:50 vs 00:10 = %v", d)
	}
}

func TestV2TargetExpiryAndRevocation(t *testing.T) {
	now := time.Now().UnixMicro()
	targets := []SleepTarget{
		{ID: "a", Hours: 8, EffectiveFrom: now - 10, KnownAt: now - 10, Revoked: true},
		{ID: "b", Hours: 7, EffectiveFrom: now - 5, KnownAt: now - 5},
		{ID: "c", Hours: 9, EffectiveFrom: now - 3, KnownAt: now - 3},
	}
	got, ok := selectSleepTarget(targets, now)
	if !ok || got.ID != "c" {
		t.Fatalf("target selection %+v", got)
	}
	until := now - 1
	if _, ok := selectSleepTarget([]SleepTarget{{ID: "x", Hours: 8, EffectiveFrom: now - 9, EffectiveUntil: &until, KnownAt: now - 9}}, now); ok {
		t.Fatal("expired target selected")
	}
}

func withSecondSource(in V2Input, rank int, scale float64) V2Input {
	var extra []Fact
	for _, f := range in.Facts {
		g := f
		g.ID += "-app"
		g.Epoch, g.SourceRank = "third-party-app", rank
		if g.Value != nil {
			v := *g.Value * scale
			g.Value = &v
		}
		g.SemanticHash = Hash(struct{ ID, Epoch string }{g.ID, g.Epoch})
		g.Hash = Hash(g.SemanticHash)
		extra = append(extra, g)
	}
	in.Facts = append(append([]Fact{}, in.Facts...), extra...)
	return in
}

// A second app writing HRV, resting HR and sleep is deprioritized per night
// instead of turning every night into a source conflict.
func TestV2SourcePriorityResolvesConflicts(t *testing.T) {
	alone, err := CalculateV2(nightInput(t))
	if err != nil || alone.Score == nil {
		t.Fatalf("single source: %v %+v", err, alone.Reasons)
	}
	both, err := CalculateV2(withSecondSource(nightInput(t), 2, 1.3))
	if err != nil || both.Score == nil || both.Status == "source_conflict" {
		t.Fatalf("second source must not conflict: %v %s %v", err, both.Status, both.Reasons)
	}
	if *both.Score != *alone.Score || !containsReason(both.Reasons, "source_deprioritized") {
		t.Fatalf("watch must win: %v vs %v %v", *both.Score, *alone.Score, both.Reasons)
	}
	if alone.DeviationLevel == "" || both.DeviationLevel != alone.DeviationLevel {
		t.Fatalf("deviation level %q %q", alone.DeviationLevel, both.DeviationLevel)
	}
}

// Two resting HR values of one source on one day: the latest is used.
func TestV2LatestRestingHRWins(t *testing.T) {
	in := nightInput(t)
	last := in.Days[len(in.Days)-1]
	for _, f := range in.Facts {
		if f.Type == RHR && f.End >= last.Start && f.End < last.End {
			g := f
			g.ID += "-later"
			g.End, g.Start = f.End+Hour, f.Start+Hour
			v := *f.Value + 20
			g.Value = &v
			g.SemanticHash = Hash(g.ID)
			in.Facts = append(in.Facts, g)
			break
		}
	}
	r, err := CalculateV2(in)
	if err != nil || r.Status == "source_conflict" || r.RHRDeviation == nil || r.RHRDeviation.Raw <= 1 {
		t.Fatalf("latest resting HR must be used: %v %s %+v", err, r.Status, r.RHRDeviation)
	}
}

// Late sleepers keep their night SDNN: samples while asleep up to noon count.
func TestV2NightSDNNFollowsSleep(t *testing.T) {
	in := nightInput(t)
	for i := range in.Facts {
		f := &in.Facts[i]
		d := in.Days[len(in.Days)-1]
		for _, day := range in.Days {
			if f.End >= day.Start && f.End < day.End {
				d = day
				break
			}
		}
		switch f.Type {
		case Sleep:
			f.Start, f.End = d.Start+2*Hour, d.Start+10*Hour
		case HRV:
			f.Start = d.Start + 8*Hour
			f.End = f.Start + 30*60*1e6
		}
	}
	r, err := CalculateV2(in)
	if err != nil || r.SDNNDeviation == nil {
		t.Fatalf("late sleeper lost night SDNN: %v %v", err, r.Reasons)
	}
}

func TestDeviationLevels(t *testing.T) {
	for _, x := range []struct {
		burden     float64
		persistent bool
		want       string
	}{{0, false, "normal"}, {.04, false, "normal"}, {.04, true, "mild_deviation"}, {.1, false, "mild_deviation"}, {.1, true, "marked_deviation"}, {.3, false, "marked_deviation"}} {
		if got := deviationLevel(x.burden, x.persistent); got != x.want {
			t.Fatalf("%v/%v = %s want %s", x.burden, x.persistent, got, x.want)
		}
	}
}

func containsReason(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
