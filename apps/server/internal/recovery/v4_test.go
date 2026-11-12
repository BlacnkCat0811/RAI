package recovery

import (
	"fmt"

	"math"
	"slices"
	"testing"
	"time"
)

func bootstrapInput(t *testing.T) V4Input {
	in := golden(t, "UTC", "2026-11-12", 0)
	in.Config = DefaultV4Config("UTC")
	in.Facts, in.Coverage = nil, nil
	today := time.UnixMicro(in.Days[len(in.Days)-1].Start)
	in.Days = nil
	for i := -HistoryDaysV4; i <= 0; i++ {
		d := today.AddDate(0, 0, i)
		in.Days = append(in.Days, Day{Date: d.Format(time.DateOnly), Start: d.UnixMicro(), End: d.AddDate(0, 0, 1).UnixMicro(), ClockBuckets: [4]int64{d.UnixMicro(), d.Add(6 * time.Hour).UnixMicro(), d.Add(12 * time.Hour).UnixMicro(), d.Add(18 * time.Hour).UnixMicro()}})
	}
	return V4Input{V2Input: V2Input{Input: in}}
}

func addObservedNight(in *V4Input, age int, hrv, rhr, sleep float64, epoch string) {
	d := in.Days[len(in.Days)-1-age]
	cat := 1
	add := func(kind, id string, from, to int64, value *float64, category *int) {
		f := Fact{Type: kind, ID: fmt.Sprintf("%s/%d/%s", epoch, age, id), Epoch: epoch, Start: from, End: to, Value: value, Category: category, Revision: 1, KnownAt: in.CapturedAt}
		f.SemanticHash, f.Hash = Hash(f.ID), Hash(f)
		in.Facts = append(in.Facts, f)
	}
	if sleep > 0 {
		add(Sleep, "sleep", d.Start+7*Hour-int64(sleep*float64(Hour)), d.Start+7*Hour, nil, &cat)
	}
	if rhr > 0 {
		add(RHR, "rhr", d.Start+8*Hour, d.Start+8*Hour, ptr(rhr), nil)
	}
	if hrv > 0 {
		for j, hour := range []int64{0, 1, 3, 5} {
			add(HRV, fmt.Sprint(j), d.Start+hour*Hour, d.Start+hour*Hour+30e6, ptr(hrv), nil)
		}
	}
}

func TestV4BootstrapAndTypedRestingHR(t *testing.T) {
	in := bootstrapInput(t)
	r, err := CalculateV4(in)
	if err != nil || r.Score == nil || *r.Score != 82 || r.Mode != "estimated" || r.Status != "available" || r.EvidenceAt != nil || r.Confidence > .2 {
		t.Fatal("empty health history must give an explicit baseline estimate", err, r.Status, r.Mode)
	}
	for name, expected := range map[string]float64{"hrv": 50, "rhr": 60, "sleep": 8} {
		c := r.Components[name]
		if c.Observed != nil || c.Used == nil || *c.Used != expected || c.InputBasis != "default_baseline" || c.MeanSamples != 0 {
			t.Fatal("model defaults were presented as observations", name)
		}
	}
	in.RestingHR = &TypedRHR{Value: 48, ID: "typed", Hash: "synthetic-hash", MeasuredAt: in.AsOf - 100*24*Hour, KnownAt: in.AsOf}
	typed, err := CalculateV4(in)
	if err != nil || typed.Score == nil || *typed.Components["rhr"].Used != 48 || typed.Components["rhr"].InputBasis != "user_entered" || typed.InputHash == r.InputHash {
		t.Fatal("explicit RHR fallback missing or absent from lineage", err)
	}
	fresh, err := FreshenV4(typed, typed.AsOf+Hour)
	if err != nil || fresh.Score == nil {
		t.Fatal("bootstrap estimate did not survive within the day", err)
	}
	expired, err := FreshenV4(typed, typed.ValidBefore)
	if err != nil || expired.Score != nil || expired.Status != "expired" {
		t.Fatal("estimate survived the next day boundary", err)
	}
}

func TestV4MeansUseLatestSevenValidNightsAcrossCalendarGaps(t *testing.T) {
	in := bootstrapInput(t)
	for i := 0; i < 9; i++ {
		addObservedNight(&in, 15+i*10, 40+float64(i), 50+float64(i), 7+float64(i)/10, "watch")
	}
	r, err := CalculateV4(in)
	if err != nil || r.Score == nil || r.Mode != "estimated" {
		t.Fatal("observations older than seven days must remain usable", err)
	}
	for name, expected := range map[string]float64{"hrv": 43, "rhr": 53, "sleep": 7.3} {
		c := r.Components[name]
		if c.MeanSamples != 7 || c.Observed != nil || c.InputBasis != "recent_mean" || math.Abs(*c.Used-expected) > 1e-9 {
			t.Fatal("wrong count/order/arithmetic mean", name, c.MeanSamples)
		}
	}
	// Input order must not alter the selected means or the immutable hash.
	slices.Reverse(in.Facts)
	reversed, err := CalculateV4(in)
	if err != nil || Hash(reversed) != Hash(r) {
		t.Fatal("input order changed the result", err)
	}
}

func TestV4CurrentSleepWinsAndSparseHistoryIsUsable(t *testing.T) {
	in := bootstrapInput(t)
	addObservedNight(&in, 30, 50, 60, 8, "watch")
	addObservedNight(&in, 0, 70, 64, 5, "watch")
	r, err := CalculateV4(in)
	if err != nil || r.Score == nil || r.Mode != "estimated" {
		t.Fatal("two nights should suffice for a bounded estimate", err)
	}
	if math.Abs(*r.Components["hrv"].Used-60) > 1e-9 || *r.Components["rhr"].Used != 62 || *r.Components["sleep"].Used != 5 || r.Components["sleep"].InputBasis != "observed" {
		t.Fatal("current sleep or two-observation cardiac mean was not used")
	}
	if *r.Score >= 82 {
		t.Fatal("short current sleep did not reduce the estimate")
	}
}

func TestV4MissingDataChangesSupportNotScore(t *testing.T) {
	full := bootstrapInput(t)
	for age := 0; age <= 28; age++ {
		addObservedNight(&full, age, 50, 60, 8, "watch")
	}
	for _, kind := range []string{HRV, RHR, Sleep} {
		full.Coverage = append(full.Coverage, Coverage{Type: kind, Complete: true, Through: full.AsOf})
	}
	complete, err := CalculateV4(full)
	if err != nil || complete.Score == nil || complete.Mode != "complete" {
		t.Fatal("invalid complete fixture", err)
	}
	for _, kinds := range [][]string{{HRV}, {RHR}, {Sleep}, {HRV, RHR, Sleep}} {
		in := full
		in.Facts = slices.DeleteFunc(slices.Clone(full.Facts), func(f Fact) bool {
			return f.End >= in.Days[len(in.Days)-1].Start && slices.Contains(kinds, f.Type)
		})
		r, err := CalculateV4(in)
		missing := len(kinds)
		// Night HRV also needs sleep context; without today's sleep it uses
		// historical observations even if raw daytime HRV samples exist.
		if slices.Contains(kinds, Sleep) && !slices.Contains(kinds, HRV) {
			missing++
		}
		if err != nil || r.Score == nil || math.Abs(*r.Score-*complete.Score) > 1e-9 || r.Mode != "estimated" || r.Confidence > .35 || len(r.MissingComponents) != missing {
			t.Fatal("missing observations deducted points or lost their estimate marker", err)
		}
	}
	full.Coverage = nil
	unsynced, err := CalculateV4(full)
	if err != nil || unsynced.Score == nil || *unsynced.Score != 82 || unsynced.Mode != "estimated" {
		t.Fatal("coverage status reintroduced the 75 score cap", err)
	}
}

func TestV4RetainedRecoveryPolicyStillProjects(t *testing.T) {
	in := bootstrapInput(t)
	r, err := CalculateV4(in)
	if err != nil {
		t.Fatal(err)
	}
	legacy := DefaultV4Config("UTC")
	legacy.Policy = PolicyV4Capped
	r.ConfigHash, r.Score = legacy.Hash(), ptr(67)
	fresh, err := FreshenV4(r, r.AsOf+Hour)
	if err != nil || fresh.Score == nil || *fresh.Score != 67 {
		t.Fatal("retained recovery was relabeled or rejected", err)
	}
}

func TestV4HistoricalMeansKeepFavorablePersonalDeviations(t *testing.T) {
	in := bootstrapInput(t)
	for age := 0; age <= 28; age++ {
		hrv, rhr := 50., 60.
		if age <= 7 {
			hrv, rhr = 60, 55
		}
		addObservedNight(&in, age, hrv, rhr, 8, "watch")
	}
	for _, kind := range []string{HRV, RHR, Sleep} {
		in.Coverage = append(in.Coverage, Coverage{Type: kind, Complete: true, Through: in.AsOf})
	}
	observed, err := CalculateV4(in)
	if err != nil || observed.Score == nil || *observed.Score <= 82 {
		t.Fatal("invalid favorable fixture", err)
	}
	in.Facts = slices.DeleteFunc(in.Facts, func(f Fact) bool { return f.End >= in.Days[len(in.Days)-1].Start })
	estimated, err := CalculateV4(in)
	if err != nil || estimated.Score == nil || math.Abs(*estimated.Score-*observed.Score) > 1e-9 || estimated.Mode != "estimated" || estimated.Confidence > .35 {
		t.Fatal("missingness silently canceled favorable personal deviations", err)
	}
}

func TestV4DeletionSourceIsolationAndFutureFacts(t *testing.T) {
	in := bootstrapInput(t)
	addObservedNight(&in, 20, 50, 60, 8, "old-watch")
	addObservedNight(&in, 10, 80, 55, 7, "new-watch")
	before, err := CalculateV4(in)
	if err != nil || math.Abs(*before.Components["hrv"].Used-80) > 1e-9 || before.Components["hrv"].MeanSamples != 1 {
		t.Fatal("different watch histories were blended", err)
	}
	// Delete every new-watch fact: the old valid source becomes the fallback.
	for _, f := range in.Facts {
		if f.Epoch == "new-watch" {
			in.Tombstones = append(in.Tombstones, Tombstone{Type: f.Type, ID: f.ID, Hash: "deleted-" + f.Hash, Revision: 2, KnownAt: in.AsOf, Start: &f.Start, End: &f.End})
		}
	}
	deleted, err := CalculateV4(in)
	if err != nil || deleted.Score == nil || math.Abs(*deleted.Components["hrv"].Used-50) > 1e-9 || deleted.InputHash == before.InputHash || len(deleted.TombstoneRefs) == 0 {
		t.Fatal("deleted evidence was reused or blocked the entire result", err)
	}
	future := in.Facts[0]
	for _, f := range in.Facts {
		if f.Type == HRV {
			future = f
			break
		}
	}
	future.ID, future.SemanticHash, future.Hash = "future", "future-semantic", "future-hash"
	future.Start, future.End = in.AsOf+Hour, in.AsOf+2*Hour
	future.Value = ptr(999)
	in.Facts = append(in.Facts, future)
	r, err := CalculateV4(in)
	if err != nil || math.Abs(*r.Components["hrv"].Used-50) > 1e-9 {
		t.Fatal("future data leaked into the mean", err)
	}
}

func TestV4FullObservationSupportsCompleteModeAndAdverseDirection(t *testing.T) {
	in := bootstrapInput(t)
	for age := 0; age <= 28; age++ {
		addObservedNight(&in, age, 50, 60, 8, "watch")
	}
	for _, kind := range []string{HRV, RHR, Sleep} {
		in.Coverage = append(in.Coverage, Coverage{Type: kind, Complete: true, Through: in.AsOf})
	}
	r, err := CalculateV4(in)
	if err != nil || r.Score == nil || r.Mode != "complete" || math.Abs(*r.Score-82) > 1e-8 {
		t.Fatal("complete stable history should have the normal 82 center", err, r.Mode)
	}
	for i := range in.Facts {
		f := &in.Facts[i]
		if f.End >= in.Days[len(in.Days)-7].Start {
			if f.Type == HRV {
				f.Value = ptr(35)
			}
			if f.Type == RHR {
				f.Value = ptr(75)
			}
		}
	}
	adverse, err := CalculateV4(in)
	if err != nil || adverse.Score == nil || *adverse.Score >= *r.Score {
		t.Fatal("adverse cardiac changes did not lower recovery", err)
	}
}
