package recovery

import (
	"fmt"
	"math"
	"slices"
	"testing"
)

func qualityNightInput(t *testing.T, nights int) V2Input {
	in := nightInput(t)
	in.Config = DefaultV3Config("UTC")
	in.Facts = slices.DeleteFunc(in.Facts, func(f Fact) bool { return f.Type == HRV })
	for i, d := range in.Days {
		if i < 28-nights {
			continue
		}
		for j, h := range []int64{0, 1, 3, 5} {
			v := 50.
			f := Fact{Type: HRV, ID: fmt.Sprintf("h-%d-%d", i, j), Epoch: "watch", Value: &v, Start: d.Start + h*Hour, End: d.Start + h*Hour + 30e6, KnownAt: in.CapturedAt, Revision: 1}
			f.SemanticHash = Hash(f.ID)
			f.Hash = Hash(f)
			in.Facts = append(in.Facts, f)
		}
	}
	// Existing synthetic helper uses a different epoch. Match each type's stable
	// source without changing either fact validity or its time support.
	for i := range in.Facts {
		in.Facts[i].Epoch = "watch"
	}
	return in
}
func TestV3ContinuousRecoveryAndBaselineStates(t *testing.T) {
	for _, n := range []int{6, 7, 13, 14, 27, 28} {
		in := qualityNightInput(t, n)
		d := in.Days[28]
		in.Facts = slices.DeleteFunc(in.Facts, func(f Fact) bool { return f.Type == RHR && f.End < in.Days[28-n].Start })
		r, e := CalculateV3(in)
		if e != nil {
			t.Fatal(e)
		}
		if n < 7 {
			if r.Score != nil {
				t.Fatal("six nights scored")
			}
			continue
		}
		if r.Score == nil {
			t.Fatalf("n=%d: %+v", n, r)
		}
		if n < 14 {
			if r.Mode != "estimated" || r.Confidence > .35 {
				t.Fatal(n, r.Mode, r.Confidence)
			}
		} else if n < 28 {
			if r.BaselineState != "provisional" || r.Confidence > .45 {
				t.Fatal(n, r.BaselineState, r.Confidence)
			}
		} else if r.Mode != "complete" || math.Abs(*r.Score-82) > 1e-8 {
			t.Fatal("constant healthy trajectory", r.Mode, *r.Score)
		}
		for i := range in.Facts {
			if in.Facts[i].Type == HRV && in.Facts[i].End >= d.Start {
				in.Facts[i].Value = ptr(49)
			}
		}
		bad, e := CalculateV3(in)
		if e != nil || bad.Score == nil || n >= 14 && *bad.Score >= *r.Score {
			t.Fatal("small adverse change should lower score", e)
		}
	}
}
func TestV3SingleMissingNightDeletionAndSyncPending(t *testing.T) {
	in := qualityNightInput(t, 28)
	d := in.Days[28]
	in.Facts = slices.DeleteFunc(in.Facts, func(f Fact) bool { return f.Type == HRV && f.End >= d.Start })
	r, e := CalculateV3(in)
	if e != nil || r.Score == nil || r.Mode != "estimated" || *r.Score > 75 || r.Confidence > .4 || r.BaselineNights != 28 {
		t.Fatal("single missing", e, r.Mode, r.Score, r.BaselineNights, r.Components)
	}
	for i := range in.Coverage {
		if in.Coverage[i].Type == HRV {
			in.Coverage[i].Complete = false
		}
	}
	pending, _ := CalculateV3(in)
	if pending.Score != nil || pending.Mode != "pending" || pending.Components["rhr"].Observed == nil {
		t.Fatal("pending must not estimate, other components survive", pending)
	}
	in = qualityNightInput(t, 28)
	in.Facts = slices.DeleteFunc(in.Facts, func(f Fact) bool { return f.Type == HRV && f.End >= in.Days[27].Start })
	r, _ = CalculateV3(in)
	if r.Score != nil {
		t.Fatal("two missing nights scored")
	}
	in = qualityNightInput(t, 28)
	for _, f := range in.Facts {
		if f.Type == HRV && f.End >= d.Start {
			a, b := f.Start, f.End
			in.Tombstones = append(in.Tombstones, Tombstone{Type: f.Type, ID: f.ID, Hash: Hash("delete" + f.ID), KnownAt: in.CapturedAt, Revision: 1, Start: &a, End: &b})
		}
	}
	r, e = CalculateV3(in)
	if e != nil || r.Components["hrv"].Observed != nil || r.Score == nil || !slices.Contains(r.Reasons, "input_deleted") {
		t.Fatal("deleted facts cannot revive through fallback", e, r)
	}
	f, e := FreshenV3(r, r.ValidBefore)
	if e != nil || f.Score != nil {
		t.Fatal("exclusive estimate boundary", e)
	}
}
func TestV3BlocksRejectConcentratedSamplingAndCanonicalOrder(t *testing.T) {
	in := qualityNightInput(t, 28)
	a, _ := CalculateV3(in)
	slices.Reverse(in.Facts)
	b, _ := CalculateV3(in)
	if Hash(a) != Hash(b) {
		t.Fatal("order changes result")
	}
	d := in.Days[28]
	for i := range in.Facts {
		f := &in.Facts[i]
		if f.Type == HRV && f.End >= d.Start {
			f.Start = d.Start + Hour
			f.End = f.Start + 30e6
		}
	}
	r, _ := CalculateV3(in)
	if r.Components["hrv"].NightQuality.Weight != 0 || r.Mode != "estimated" {
		t.Fatal("same-minute samples create independent support", r.Components["hrv"])
	}
}
func TestV2ProvisionalBaselinePropagates(t *testing.T) {
	in := nightInput(t)
	start := in.Days[14].Start
	in.Facts = slices.DeleteFunc(in.Facts, func(f Fact) bool { return f.End < start })
	r, e := CalculateV2(in)
	if e != nil || r.BaselineState != "provisional" || r.Confidence > .45 {
		t.Fatal(e, r.BaselineState, r.Confidence)
	}
}

func TestV3MissingRequiresDiscoveryOfEvaluationNight(t *testing.T) {
	in := qualityNightInput(t, 28)
	d := in.Days[28]
	in.Facts = slices.DeleteFunc(in.Facts, func(f Fact) bool { return f.Type == HRV && f.End >= d.Start })
	for i := range in.Coverage {
		in.Coverage[i].Through = d.Start - Hour
	}
	r, e := CalculateV3(in)
	if e != nil || r.Score != nil || r.Mode != "pending" || r.Components["hrv"].State != "sync_pending" {
		t.Fatal("yesterday's heartbeat was treated as a completed missing night", e)
	}
}

func TestV3NoiseAndAcuteSyntheticTrajectories(t *testing.T) {
	// The latent trajectory is held fixed. Only the last night's measurement
	// changes; histories and the underlying adverse shift are identical.
	base := qualityNightInput(t, 28)
	d := base.Days[28]
	for i := range base.Facts {
		f := &base.Facts[i]
		if f.Type == HRV && f.End >= base.Days[26].Start {
			f.Value = ptr(50 * math.Exp(-.15))
		}
	}
	oldIn := base
	oldIn.Config = DefaultV2Config("UTC")
	oldAnchor, e := CalculateV2(oldIn)
	if e != nil {
		t.Fatal(e)
	}
	newAnchor, e := CalculateV3(base)
	if e != nil {
		t.Fatal(e)
	}
	oldError, newError := 0., 0.
	for i := -10; i <= 10; i++ {
		in := base
		in.Facts = slices.Clone(base.Facts)
		for k := range in.Facts {
			f := &in.Facts[k]
			if f.Type == HRV && f.End >= d.Start {
				f.Value = ptr(50 * math.Exp(-.15-float64(i)*.002))
			}
		}
		modern, e := CalculateV3(in)
		if e != nil || modern.Score == nil {
			t.Fatal(e)
		}
		in.Config = DefaultV2Config("UTC")
		old, _ := CalculateV2(in)
		oldError += math.Pow(*old.Score-*oldAnchor.Score, 2)
		newError += math.Pow(*modern.Score-*newAnchor.Score, 2)
	}
	if newError >= oldError {
		t.Fatal("trend did not reduce measurement sensitivity")
	}
	acute := qualityNightInput(t, 28)
	for i := range acute.Facts {
		f := &acute.Facts[i]
		if f.End >= acute.Days[26].Start {
			if f.Type == HRV {
				f.Value = ptr(35)
			}
			if f.Type == RHR {
				f.Value = ptr(70)
			}
		}
	}
	r, e := CalculateV3(acute)
	if e != nil || r.Score == nil || *r.Score >= *newAnchor.Score || r.DeviationLevel != "marked_deviation" || len(r.PersistentDeviation) == 0 {
		t.Fatal("three-night corroborated change was smoothed away", e)
	}
	t.Logf("SYNTHETIC_RECOVERY perturbation_mse_ratio=%.3f stable_trajectory_full_score_fraction=0 acute_three_night_direction=adverse", newError/oldError)
}
