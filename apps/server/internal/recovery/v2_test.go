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
			f.Start = d.Start + 2*3600*1e6
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
