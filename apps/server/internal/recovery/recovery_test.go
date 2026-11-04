package recovery

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func golden(t testing.TB, tz, date string, n int) Input {
	t.Helper()
	loc, e := time.LoadLocation(tz)
	if e != nil {
		t.Fatal(e)
	}
	target, e := time.ParseInLocation("2006-01-02", date, loc)
	if e != nil {
		t.Fatal(e)
	}
	at := target.Add(12 * time.Hour).UnixMicro()
	in := Input{Dataset: "synthetic", DatasetEpoch: "epoch", Collector: "collector", Assignment: "assignment", Generation: 1, KnownThroughRevision: 1000, CapturedAt: at, AsOf: at, View: "revised", Config: DefaultConfig(tz), Facts: []Fact{}, Tombstones: []Tombstone{}, Coverage: []Coverage{}, Days: []Day{}}
	for i := -28; i <= 0; i++ {
		d := target.AddDate(0, 0, i)
		in.Days = append(in.Days, Day{Date: d.Format("2006-01-02"), Start: d.UnixMicro(), End: d.AddDate(0, 0, 1).UnixMicro(), ClockBuckets: [4]int64{d.UnixMicro(), time.Date(d.Year(), d.Month(), d.Day(), 6, 0, 0, 0, loc).UnixMicro(), time.Date(d.Year(), d.Month(), d.Day(), 12, 0, 0, 0, loc).UnixMicro(), time.Date(d.Year(), d.Month(), d.Day(), 18, 0, 0, 0, loc).UnixMicro()}})
		if i < -n {
			continue
		}
		for _, kind := range []string{HRV, RHR, Sleep} {
			f := Fact{Type: kind, ID: fmt.Sprintf("%d/%s", i, kind), Epoch: "source-epoch", Revision: 1, Start: d.Add(7 * time.Hour).UnixMicro(), End: d.Add(7 * time.Hour).UnixMicro(), KnownAt: d.Add(8 * time.Hour).UnixMicro()}
			switch kind {
			case HRV:
				f.Value = ptr(50)
			case RHR:
				f.Value = ptr(60)
			case Sleep:
				cat := 1
				f.Category = &cat
				f.Start = d.Add(-time.Hour).UnixMicro()
			}
			f.SemanticHash = Hash(struct {
				Kind       string
				Start, End int64
				V          *float64
				C          *int
			}{kind, f.Start, f.End, f.Value, f.Category})
			f.Hash = Hash(f)
			in.Facts = append(in.Facts, f)
		}
	}
	for _, kind := range []string{HRV, RHR, Sleep} {
		in.Coverage = append(in.Coverage, Coverage{kind, "stream", true, at})
	}
	return in
}
func calc(t testing.TB, in Input) Bundle {
	t.Helper()
	r, e := Calculate(in)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func changeToday(in Input, kind string, value float64) Input {
	in.Facts = append([]Fact{}, in.Facts...)
	d := in.Days[len(in.Days)-1]
	for i, f := range in.Facts {
		if f.Type == kind && f.End >= d.Start {
			if kind == Sleep {
				f.Start = f.End - int64(value*float64(Hour))
			} else {
				f.Value = ptr(value)
			}
			f.SemanticHash = Hash(struct {
				T, ID string
				S, E  int64
				V     *float64
			}{f.Type, f.ID, f.Start, f.End, f.Value})
			f.Hash = Hash(f)
			in.Facts[i] = f
		}
	}
	return in
}
func TestMathematicalOracle(t *testing.T) {
	// Independent hand calculation: SDNN=50 exp(-.3), RHR=66, sleep=6.5h.
	// zH=3,aH=1; zR=2,aR=.5; sleep deficit=1h -> aS=1.
	in := golden(t, "UTC", "2026-10-30", 28)
	in = changeToday(in, HRV, 50*math.Exp(-.3))
	in = changeToday(in, RHR, 66)
	in = changeToday(in, Sleep, 6.5)
	r := calc(t, in)
	want := 100 * math.Exp(-(.25 + .05 + .15))
	if r.Recovery.Score == nil || math.Abs(*r.Recovery.Score-want) > 1e-10 {
		t.Fatal("independent exp/burden oracle", r.Recovery.Score, want)
	}
	if math.Abs(*r.Baseline.SDNN.Center-math.Log(50)) > 1e-12 || *r.Baseline.SDNN.MAD != 0 || *r.Baseline.SDNN.Sigma != .1 || *r.Baseline.RHR.Sigma != 3 {
		t.Fatal("log/median/MAD/floor oracle")
	}
	if math.Abs(*r.Recovery.SDNNContribution+*r.Recovery.RHRContribution+*r.Recovery.SleepContribution-*r.Recovery.TotalBurden) > 1e-12 {
		t.Fatal("contribution reconstruction")
	}
	if median([]float64{9, 1, 3, 5}) != 4 {
		t.Fatal("even median")
	}
	in = changeToday(in, HRV, math.SmallestNonzeroFloat64)
	r = calc(t, in)
	if r.Recovery.SDNNDeviation.Clipped != 7 || r.Recovery.SDNNDeviation.Burden != 3 {
		t.Fatal("clipping oracle")
	}
}
func TestGoldenAthletesAndMissingness(t *testing.T) {
	in := golden(t, "Asia/Shanghai", "2026-10-30", 28)
	base := calc(t, in).Recovery
	if base.Score == nil || *base.Score != 100 || base.Confidence > .60 || base.BaselineState != "established" {
		t.Fatal("stable")
	}
	for _, tt := range []struct {
		name, kind string
		value      float64
	}{{"Low HRV", HRV, 30}, {"High RHR", RHR, 75}, {"Short sleep", Sleep, 4}} {
		t.Run(tt.name, func(t *testing.T) {
			r := calc(t, changeToday(in, tt.kind, tt.value)).Recovery
			if r.Score == nil || *r.Score >= *base.Score {
				t.Fatal("stress not detected")
			}
		})
	}
	stressed := changeToday(changeToday(changeToday(in, HRV, 30), RHR, 75), Sleep, 4)
	r := calc(t, stressed).Recovery
	if r.Score == nil || *r.Score >= 50 {
		t.Fatal("combined stress")
	}
	for _, kind := range []string{HRV, RHR, Sleep} {
		t.Run("missing "+kind, func(t *testing.T) {
			x := stressed
			x.Facts = []Fact{}
			for _, f := range stressed.Facts {
				if f.Type != kind || f.End < in.Days[28].Start {
					x.Facts = append(x.Facts, f)
				}
			}
			r := calc(t, x).Recovery
			if r.Score != nil || r.Confidence >= base.Confidence {
				t.Fatal("missing elevated full score")
			}
			if kind == Sleep && (r.Status != "partial" || r.AutonomicOnly == nil || r.SleepBurden != nil) {
				t.Fatal("missing sleep became zero")
			}
		})
	}
	for _, n := range []int{0, 8, 13, 14, 21, 27, 28} {
		r := calc(t, golden(t, "UTC", "2026-10-30", n)).Recovery
		if n < 14 && (r.Score != nil || r.Status != "collecting_baseline") {
			t.Fatal("cold start")
		}
		if n >= 14 && (r.Score == nil || r.Confidence > .60) {
			t.Fatal("maturity")
		}
	}
	x := in
	x.Facts = nil
	if r := calc(t, x).Recovery; r.Score != nil || r.Confidence != 0 {
		t.Fatal("no upload became perfect")
	}
}
func TestMonotonicConfidenceAndStressProperties(t *testing.T) {
	in := golden(t, "UTC", "2026-10-30", 28)
	for _, kind := range []string{HRV, RHR, Sleep} {
		previous := 100.0
		for i := 0; i < 50; i++ {
			v := 50 - float64(i)*.9
			if kind == RHR {
				v = 60 + float64(i)
			}
			if kind == Sleep {
				v = 8 - float64(i)*.1
			}
			r := calc(t, changeToday(in, kind, v)).Recovery
			if r.Score != nil {
				if *r.Score > previous+1e-10 {
					t.Fatal("burden monotonicity", kind)
				}
				previous = *r.Score
			}
		}
	}
	prev := 0.0
	for n := 14; n <= 28; n++ {
		r := calc(t, golden(t, "UTC", "2026-10-30", n)).Recovery
		if r.Confidence < prev {
			t.Fatal("more independent baseline days decreased support")
		}
		prev = r.Confidence
	}
	for _, kind := range []string{HRV, RHR} {
		v := 1000.0
		if kind == RHR {
			v = 1
		}
		r := calc(t, changeToday(in, kind, v)).Recovery
		if r.Score == nil || *r.Score != 100 {
			t.Fatal("abnormal good was rewarded")
		}
	}
}
func TestPermutationDuplicatesSourcesRevisions(t *testing.T) {
	in := golden(t, "UTC", "2026-10-30", 28)
	base := calc(t, in)
	rng := rand.New(rand.NewSource(526))
	for i := 0; i < 30; i++ {
		rng.Shuffle(len(in.Facts), func(i, j int) { in.Facts[i], in.Facts[j] = in.Facts[j], in.Facts[i] })
		if Hash(calc(t, in)) != Hash(base) {
			t.Fatal("input permutation changed artifact")
		}
	}
	x := in
	f := in.Facts[0]
	f.ID += "duplicate"
	x.Facts = append(append([]Fact{}, in.Facts...), f)
	dup := calc(t, x)
	if !reflect.DeepEqual(dup.Features, base.Features) || dup.Recovery.Confidence != base.Recovery.Confidence || *dup.Recovery.Score != *base.Recovery.Score {
		t.Fatal("semantic duplicate changed representative/support")
	}
	for _, kind := range []string{HRV, RHR, Sleep} {
		x := in
		for _, f := range in.Facts {
			if f.Type == kind && f.End >= in.Days[28].Start {
				f.ID += "sourceB"
				f.Epoch = "epoch-B"
				f.SemanticHash += "B"
				x.Facts = append(append([]Fact{}, in.Facts...), f)
				break
			}
		}
		r := calc(t, x).Recovery
		if r.Score != nil || r.Status != "source_conflict" {
			t.Fatal("multi-source silently mixed", kind)
		}
	}
	x = in
	for _, f := range in.Facts {
		if f.Type == RHR && f.End >= in.Days[28].Start {
			f.ID += "revision"
			f.Value = ptr(68)
			f.SemanticHash += "revision"
			x.Facts = append(append([]Fact{}, in.Facts...), f)
			break
		}
	}
	if r := calc(t, x).Recovery; r.Score != nil {
		t.Fatal("live RHR revision averaged")
	}
}
func TestSleepUnionAnchorNapsAndDST(t *testing.T) {
	for _, tt := range []struct {
		tz, day string
		hours   float64
	}{{"UTC", "2026-10-30", 8}, {"America/New_York", "2026-03-08", 7}, {"America/New_York", "2026-11-01", 9}} {
		in := golden(t, tt.tz, tt.day, 28)
		loc, _ := time.LoadLocation(tt.tz)
		d, _ := time.ParseInLocation("2006-01-02", tt.day, loc)
		start := time.Date(d.Year(), d.Month(), d.Day()-1, 23, 0, 0, 0, loc)
		end := time.Date(d.Year(), d.Month(), d.Day(), 7, 0, 0, 0, loc)
		for i, f := range in.Facts {
			if f.Type == Sleep && f.End >= in.Days[28].Start {
				f.Start = start.UnixMicro()
				f.End = end.UnixMicro()
				in.Facts[i] = f
				overlap := f
				overlap.ID += "stage"
				cat := 4
				overlap.Category = &cat
				overlap.SemanticHash += "stage"
				in.Facts = append(in.Facts, overlap)
				awake := f
				awake.ID += "awake"
				cat2 := 2
				awake.Category = &cat2
				awake.Start = f.Start + Hour
				awake.End = awake.Start + Hour/2
				awake.SemanticHash += "awake"
				in.Facts = append(in.Facts, awake)
				break
			}
		}
		r := calc(t, in)
		sleep := r.Features[28].Sleep
		if sleep.Value == nil || math.Abs(*sleep.Value-(tt.hours-.5)) > 1e-9 || *sleep.AwakeHours != .5 {
			t.Fatal("union/DST double count", tt, sleep)
		}
		dayLength := in.Days[28].End - in.Days[28].Start
		if tt.day == "2026-03-08" && dayLength != 23*Hour || tt.day == "2026-11-01" && dayLength != 25*Hour {
			t.Fatal("DST calendar")
		}
	}
	in := golden(t, "UTC", "2026-10-30", 28)
	for i, f := range in.Facts {
		if f.Type == Sleep && f.End >= in.Days[28].Start {
			f.Start = in.Days[28].Start + Hour
			f.End = in.Days[28].Start + 8*Hour
			in.Facts[i] = f
			nap := f
			nap.ID += "nap"
			nap.Start = in.Days[28].Start + 10*Hour
			nap.End = nap.Start + Hour
			nap.SemanticHash += "nap"
			in.Facts = append(in.Facts, nap)
			break
		}
	}
	in.AsOf = in.Days[28].Start + 14*Hour
	in.CapturedAt = in.AsOf
	r := calc(t, in)
	if *r.Features[28].Sleep.Value != 7 || r.Features[28].Sleep.NapCount != 1 {
		t.Fatal("01-08 or daytime nap")
	}
}
func TestFutureLeakageOutliersAndStep(t *testing.T) {
	in := golden(t, "UTC", "2026-10-30", 28)
	base := calc(t, in)
	future := in.Facts[0]
	future.ID = "future"
	future.Start = in.AsOf + Hour
	future.End = future.Start
	future.Value = ptr(1)
	future.SemanticHash = "future"
	x := in
	x.Facts = append(append([]Fact{}, in.Facts...), future)
	r := calc(t, x)
	if Hash(r.Features) != Hash(base.Features) || *r.Recovery.Score != *base.Recovery.Score {
		t.Fatal("future event leakage")
	}
	x = in
	x.View = "as_known"
	for i, f := range x.Facts {
		if f.Type == RHR && f.End >= x.Days[28].Start {
			f.KnownAt = x.AsOf + 1
			x.Facts = append([]Fact{}, x.Facts...)
			x.Facts[i] = f
			break
		}
	}
	if r := calc(t, x).Recovery; r.Score != nil {
		t.Fatal("late RHR known at D leaked")
	}
	x = in
	x.Facts = append([]Fact{}, in.Facts...)
	for i, f := range x.Facts {
		if f.Type == HRV && f.End < in.Days[28].Start {
			f.Value = ptr(1e200)
			x.Facts[i] = f
			break
		}
	}
	if *calc(t, x).Baseline.SDNN.Center != *base.Baseline.SDNN.Center {
		t.Fatal("one outlier moved median")
	}
	centers := []float64{}
	for shifted := 0; shifted <= 28; shifted++ {
		x = in
		x.Facts = append([]Fact{}, in.Facts...)
		cut := in.Days[28-shifted].Start
		for i, f := range x.Facts {
			if f.Type == HRV && f.End >= cut && f.End < in.Days[28].Start {
				f.Value = ptr(35)
				x.Facts[i] = f
			}
		}
		centers = append(centers, *calc(t, x).Baseline.SDNN.Center)
	}
	for i := 1; i < len(centers); i++ {
		if centers[i] > centers[i-1] {
			t.Fatal("step adaptation reversed")
		}
	}
	if math.Abs(centers[28]-math.Log(35)) > 1e-12 {
		t.Fatal("baseline never adapted")
	}
}
func TestMalformedAndFreshness(t *testing.T) {
	for _, v := range []float64{0, -1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		in := golden(t, "UTC", "2026-10-30", 28)
		in.Facts[0].Value = ptr(v)
		if _, e := Calculate(in); e == nil {
			t.Fatal("malformed raw clamped")
		}
	}
	in := golden(t, "UTC", "2026-10-30", 28)
	in.AsOf += 25 * Hour
	if r := calc(t, in).Recovery; r.Score != nil || r.Status != "stale" {
		t.Fatal("no evidence became better")
	}
	if a, b := AffectedDays(10, 10, true, 28); a != 9 || b != 40 {
		t.Fatal("bounded sleep operator")
	}
}
func BenchmarkRecoveryHistory(b *testing.B) {
	for _, n := range []int{14, 28, 42, 90, 180, 365} {
		b.Run(fmt.Sprintf("history_%dd", n), func(b *testing.B) {
			in := golden(b, "UTC", "2026-10-30", min(n, 28))
			b.ReportAllocs()
			for b.Loop() {
				r, e := Calculate(in)
				if e != nil {
					b.Fatal(e)
				}
				raw, _ := json.Marshal(r)
				b.ReportMetric(float64(len(raw)), "artifact_bytes")
			}
		})
	}
}

func TestCoverageAndFreshnessNeverCreateNormality(t *testing.T) {
	for _, n := range []int{14, 28} {
		in := golden(t, "UTC", "2026-10-30", n)
		full := calc(t, in).Recovery
		x := in
		x.Facts = []Fact{}
		for _, f := range in.Facts {
			if f.Type != Sleep || f.End < in.Days[28].Start {
				x.Facts = append(x.Facts, f)
			}
		}
		partial := calc(t, x).Recovery
		if partial.Score != nil || partial.Confidence >= full.Confidence {
			t.Fatal("missing sleep increased confidence", n, partial.Confidence, full.Confidence)
		}
		aged, e := Freshen(full, in.AsOf+Hour)
		if e != nil || aged.Confidence > full.Confidence || aged.Score != nil && *aged.Score > *full.Score {
			t.Fatal("no new evidence improved recovery")
		}
		aged, e = Freshen(full, in.AsOf+25*Hour)
		if e != nil || aged.Score != nil || aged.SDNNFreshness.State != "stale" {
			t.Fatal("per-component age")
		}
		x = in
		x.Coverage = []Coverage{}
		r := calc(t, x).Recovery
		if r.Score != nil || r.Status != "partial" {
			t.Fatal("coverage absent treated normal")
		}
	}
}
func TestBaselineSparseSourceShiftAndNoise(t *testing.T) {
	in := golden(t, "UTC", "2026-10-30", 28)
	base := calc(t, in)
	// A source revision/device epoch starts an independent baseline.
	x := in
	x.Facts = append([]Fact{}, in.Facts...)
	for i, f := range x.Facts {
		if f.End >= in.Days[28].Start {
			f.Epoch = "new-source-epoch"
			x.Facts[i] = f
		}
	}
	if r := calc(t, x).Recovery; r.Score != nil || r.Status != "collecting_baseline" {
		t.Fatal("source switch reused baseline")
	}
	rng := rand.New(rand.NewSource(28526))
	x = in
	x.Facts = append([]Fact{}, in.Facts...)
	for i, f := range x.Facts {
		if f.Type == HRV && f.End < in.Days[28].Start {
			f.Value = ptr(50 + rng.NormFloat64()*2)
			x.Facts[i] = f
		}
	}
	if r := calc(t, x); math.Abs(*r.Baseline.SDNN.Center-*base.Baseline.SDNN.Center) > .05 {
		t.Fatal("bounded noise destabilized robust baseline")
	}
	// 21 historical days can still fail the explicit recent-coverage requirement.
	x = in
	x.Facts = []Fact{}
	for _, f := range in.Facts {
		if f.End < in.Days[21].Start || f.End >= in.Days[28].Start {
			x.Facts = append(x.Facts, f)
		}
	}
	r := calc(t, x)
	if r.Recovery.Score != nil || r.Baseline.SDNN.RecentDays != 0 || r.Baseline.SDNN.UsableDays != 21 {
		t.Fatal("consecutive gap hidden by lifetime count")
	}
	x = in
	x.View = "as_known"
	x.Facts = append([]Fact{}, in.Facts...)
	for i, f := range x.Facts {
		if f.End >= in.Days[28].Start {
			f.KnownAt = in.AsOf + 7*24*Hour
			x.Facts[i] = f
		}
	}
	if r := calc(t, x).Recovery; r.Score != nil {
		t.Fatal("future recorded time leaked")
	}
}
func BenchmarkRecoveryStages(b *testing.B) {
	in := golden(b, "UTC", "2026-10-30", 28)
	fs := eligible(in)
	bundle := calc(b, in)
	b.Run("daily_aggregate", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			for _, d := range in.Days {
				_ = quantity(fs, d, HRV)
				_ = quantity(fs, d, RHR)
				_ = sleepDaily(fs, d, in.Config, in.AsOf)
			}
		}
	})
	b.Run("baseline", func(b *testing.B) {
		today := bundle.Features[28]
		history := bundle.Features[:28]
		b.ReportAllocs()
		for b.Loop() {
			_ = baseline(history, today.SDNN, HRV, in.Config)
			_ = baseline(history, today.RHR, RHR, in.Config)
			_ = baseline(history, today.Sleep.Component, Sleep, in.Config)
		}
	})
	b.Run("bounded_recompute", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, e := Calculate(in); e != nil {
				b.Fatal(e)
			}
		}
	})
}

func TestDailySDNNMedianCivilBucketsAndInputLimits(t *testing.T) {
	in := golden(t, "America/New_York", "2026-11-01", 28)
	d := in.Days[28]
	loc, _ := time.LoadLocation(in.Config.Timezone)
	at, _ := time.ParseInLocation("2006-01-02 15:04", d.Date+" 06:00", loc)
	rows := []Fact{}
	for _, f := range in.Facts {
		if f.Type == HRV && f.End >= d.Start {
			for i, v := range []float64{1, 50, 10000} {
				x := f
				x.ID = fmt.Sprintf("sdnn-robust-%d", i)
				x.Value = ptr(v)
				x.Start = at.UnixMicro() + int64(i)
				x.End = x.Start
				x.SemanticHash = Hash(x)
				rows = append(rows, x)
			}
		} else {
			rows = append(rows, f)
		}
	}
	in.Facts = rows
	r := calc(t, in)
	if *r.Features[28].SDNN.Value != 50 || r.Features[28].SDNN.Count != 3 || r.Features[28].SDNN.TimeOfDayCounts[1] != 3 {
		t.Fatal("SDNN median/civil 06:00 DST bucket")
	}
	in = golden(t, "UTC", "2026-10-30", 28)
	f := in.Facts[0]
	for len(in.Facts) <= MaxFacts {
		f.ID = fmt.Sprint(len(in.Facts))
		in.Facts = append(in.Facts, f)
	}
	if _, e := Calculate(in); e != ErrLimit {
		t.Fatal("bounded facts cap")
	}
	if Hash(map[string]int{"b": 2, "a": 1}) != Hash(map[string]int{"a": 1, "b": 2}) {
		t.Fatal("map serialization order")
	}
}
func TestSleepLongAmbiguousEpisodeAndCensoring(t *testing.T) {
	in := golden(t, "UTC", "2026-10-30", 28)
	for i, f := range in.Facts {
		if f.Type == Sleep && f.End >= in.Days[28].Start {
			f.Start = f.End - 30*Hour
			in.Facts[i] = f
			break
		}
	}
	r := calc(t, in)
	if r.Recovery.Score != nil || r.Recovery.Status != "source_conflict" {
		t.Fatal("unbounded episode treated as normal")
	}
	in = golden(t, "UTC", "2026-10-30", 28)
	in.AsOf = in.Days[28].Start + 8*Hour
	in.CapturedAt = in.AsOf
	r = calc(t, in)
	if r.Recovery.Score != nil || r.Features[28].Sleep.Value != nil {
		t.Fatal("unfinalized episode scored as complete")
	}
}

func TestSleepFutureEpisodeCannotRewriteEarlierBaselineDays(t *testing.T) {
	in := golden(t, "UTC", "2026-10-30", 28)
	base := calc(t, in)
	var later Fact
	for _, f := range in.Facts {
		if f.Type == Sleep && f.End >= in.Days[28].Start {
			later = f
			break
		}
	}
	later.ID = "late-long-overlap"
	later.Start = in.Days[24].Start
	later.SemanticHash = "late-long-overlap"
	in.Facts = append(in.Facts, later)
	r := calc(t, in)
	for i := 0; i < 28; i++ {
		if Hash(r.Features[i].Sleep) != Hash(base.Features[i].Sleep) {
			t.Fatal("later-ending sleep changed an earlier daily baseline fact", i)
		}
	}
}

func TestAmbiguousCoveragePermutation(t *testing.T) {
	in := golden(t, "UTC", "2026-10-30", 28)
	in.Coverage = append(in.Coverage, Coverage{HRV, "another-stream", true, in.AsOf})
	a := calc(t, in)
	if a.Recovery.Score != nil {
		t.Fatal("multiple current streams treated complete")
	}
	for i, j := 0, len(in.Coverage)-1; i < j; i, j = i+1, j-1 {
		in.Coverage[i], in.Coverage[j] = in.Coverage[j], in.Coverage[i]
	}
	if Hash(calc(t, in)) != Hash(a) {
		t.Fatal("coverage row order changed artifact")
	}
}

func TestRandomMissingHistoricalDays(t *testing.T) {
	in := golden(t, "UTC", "2026-10-30", 28)
	rng := rand.New(rand.NewSource(528143))
	permutation := rng.Perm(21)
	drop := map[int]bool{}
	for _, n := range permutation[:5] {
		drop[n] = true
	}
	rows := []Fact{}
	for _, f := range in.Facts {
		removed := false
		for n := range drop {
			if f.End >= in.Days[n].Start && f.End < in.Days[n].End {
				removed = true
			}
		}
		if !removed {
			rows = append(rows, f)
		}
	}
	in.Facts = rows
	r := calc(t, in)
	if r.Baseline.SDNN.UsableDays != 23 || r.Recovery.BaselineState != "provisional" || r.Recovery.Score == nil || *r.Recovery.Score != 100 {
		t.Fatal("random missing days treated as zero/normal history")
	}
}
