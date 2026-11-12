package trainingeffect

import (
	"math"
	"testing"

	wf "rai/server/internal/workoutfeatures"
)

// Synthetic runner of the design note §7: vT 4.3 m/s, v6 5.0 m/s, HR 50-190.
// HR follows speed through a first-order response (rise 30 s, fall 50 s).
const (
	vT, v6        = 4.3, 5.0
	restHR, maxHR = 50.0, 190.0
	t0            = int64(1_800_000_000_000_000)
	walk, stand   = 1.4, 0.0
)

var jog = .6 * vT

type phase struct{ seconds, speed float64 }

func rep(n int, work, recovery phase) []phase {
	var out []phase
	for i := 0; i < n; i++ {
		out = append(out, work)
		if i < n-1 {
			out = append(out, recovery)
		}
	}
	return out
}

func structured(main []phase) []phase {
	out := []phase{{600, .7 * vT}}
	out = append(out, main...)
	return append(out, phase{480, .7 * vT})
}

func distance(m, v float64) phase { return phase{m / v, v} }

type synth struct {
	speed []float64 // per 0.1 s
	hr    []float64 // per 0.1 s
}

const dt = .1

func build(phases []phase) synth {
	var s synth
	for _, p := range phases {
		for n := int(math.Round(p.seconds / dt)); n > 0; n-- {
			s.speed = append(s.speed, p.speed)
		}
	}
	h := .15
	for _, v := range s.speed {
		target := math.Max(.15, math.Min(1, .92*v/vT-.04))
		tc := 30.0
		if target < h {
			tc = 50
		}
		h += (target - h) * (1 - math.Exp(-dt/tc))
		s.hr = append(s.hr, restHR+h*(maxHR-restHR))
	}
	return s
}

func (s synth) at(xs []float64, sec float64) float64 {
	i := int(sec / dt)
	if i >= len(xs) {
		i = len(xs) - 1
	}
	return xs[i]
}

func us(sec float64) int64 { return t0 + int64(math.Round(sec*1e6)) }

// input samples HR every 5 s and speed in native 2.56 s spans (or a coarser
// grid when cell is set), like the real-device data of §3.
func (s synth) input(level LevelInput, cell float64) Input {
	total := float64(len(s.speed)) * dt
	in := Input{WorkoutID: "w", Start: t0, LoadTier: "complete", HR: &HRReference{RestHR: restHR, MaxHR: maxHR, Hash: "hr"},
		Fitness: &FitnessReference{VT: vT, V6: v6, At: t0 - 86400e6, Hash: "fit", Status: "supported"}, Level: level}
	in.Exposure.Schema = wf.ExposureSchema
	for a := 0.0; a+5 <= total; a += 5 {
		in.Exposure.Segments = append(in.Exposure.Segments, wf.HRSegment{Start: us(a), End: us(a + 5), HR0: s.at(s.hr, a), HR1: s.at(s.hr, a+5)})
	}
	e := &wf.EffortFeatures{Schema: wf.EffortSchema, Points: []wf.EffortPoint{}}
	var speeds []float64
	for a := 0.0; a+cell <= total; a += cell {
		sum, n := 0.0, 0
		for x := a; x < a+cell-1e-9; x += dt {
			sum += s.at(s.speed, x)
			n++
		}
		v := sum / float64(n)
		if v < .5 {
			continue
		}
		hr := s.at(s.hr, a+cell/2)
		p := wf.EffortPoint{Start: us(a), End: us(a + cell), Speed: v, Power: ptr(70 * v), Cadence: ptr(150 + 6*v), HR: &hr, Supported: true}
		e.Points = append(e.Points, p)
		speeds = append(speeds, v)
	}
	e.MedianSpeed = medianOf(speeds)
	e.MedianPower = ptr(70 * *e.MedianSpeed)
	e.MedianCadence = ptr(150 + 6**e.MedianSpeed)
	in.Effort = e
	return in
}

func medianOf(xs []float64) *float64 {
	s := append([]float64(nil), xs...)
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
	return ptr(s[len(s)/2])
}

// level6 is ~2.5 h of running a week over 28 days.
var level6 = LevelInput{Seconds28: 150 * 60 * 4, Days28: 16}

func levelFor(l float64) LevelInput {
	for m := 1.0; m < 2000; m += .5 {
		if v, _ := TrainingLevel(LevelInput{Seconds28: m * 60 * 4, Days28: 16}); v >= l-1e-9 {
			return LevelInput{Seconds28: m * 60 * 4, Days28: 16}
		}
	}
	panic("level")
}

func score(t *testing.T, name string, phases []phase, level LevelInput) (float64, float64, Result) {
	t.Helper()
	r := Calculate(build(phases).input(level, 2.56))
	if r.Aerobic.Value == nil || r.Anaerobic.Value == nil {
		t.Fatalf("%s: unavailable %+v %+v", name, r.Aerobic, r.Anaerobic)
	}
	t.Logf("%-28s aerobic %.1f anaerobic %.1f  EPOC %.0f  W/D' %.2f  peak/D' %.2f bouts %d", name, *r.Aerobic.Value, *r.Anaerobic.Value, *r.EPOCPeak,
		*r.AnaerobicWork / *r.AnaerobicReserve, *r.AnaerobicPeak / *r.AnaerobicReserve, r.Bouts)
	return *r.Aerobic.Value, *r.Anaerobic.Value, r
}

var (
	easy      = []phase{{45 * 60, .75 * vT}}
	threshold = structured([]phase{{1200, vT}})
	tempoPlus = structured([]phase{{1200, 1.03 * vT}})
	sprints   = func(rec phase) []phase { return structured(rep(10, distance(50, 7.5), rec)) }
	fourHund  = func(rec phase) []phase { return structured(rep(10, distance(400, 1.04*v6), rec)) }
	twoHund   = func(rec phase) []phase { return structured(rep(12, distance(200, 1.2*v6), rec)) }
)

// Requirements 1-4 (§7.1): the four archetypal sessions.
func TestArchetypalSessions(t *testing.T) {
	a, an, _ := score(t, "easy 45min @.75vT", easy, level6)
	if a < 2 || a > 3.5 || an != 0 {
		t.Fatalf("easy run must be mainly aerobic: %.1f/%.1f", a, an)
	}
	a, an, _ = score(t, "threshold 20min @vT", threshold, level6)
	if a < 3 || an > .5 {
		t.Fatalf("threshold: aerobic clear, anaerobic very low: %.1f/%.1f", a, an)
	}
	a2, an2, _ := score(t, "threshold 20min @1.03vT", tempoPlus, level6)
	if a2 < a || an2 > 1 || an2 < an {
		t.Fatalf("slightly faster threshold: %.1f/%.1f", a2, an2)
	}
	a, an, r := score(t, "10x50m / 90s walk", sprints(phase{90, walk}), level6)
	if an < 1.5 || an <= a || r.Bouts != 10 {
		t.Fatalf("short sprints must register anaerobic: %.1f/%.1f bouts %d", a, an, r.Bouts)
	}
	a, an, _ = score(t, "10x400m @1.04v6 / 90s jog", fourHund(phase{90, jog}), level6)
	if a < 3 || an < 3 {
		t.Fatalf("400s are both aerobic and anaerobic: %.1f/%.1f", a, an)
	}
}

// Requirement 5 (§7.2): same work, shorter recovery never scores lower.
func TestRecoveryStructureIsMonotone(t *testing.T) {
	for name, sessions := range map[string][][]phase{
		"10x400": {fourHund(phase{45, jog}), fourHund(phase{90, jog}), fourHund(phase{180, walk}), fourHund(phase{300, stand})},
		"12x200": {twoHund(phase{30, jog}), twoHund(phase{90, walk}), twoHund(phase{240, walk})},
		"10x50":  {sprints(phase{30, walk}), sprints(phase{90, walk}), sprints(phase{180, walk})},
	} {
		prevA, prevAn := 6.0, 6.0
		var first, last float64
		for i, s := range sessions {
			a, an, r := score(t, name, s, level6)
			if a > prevA || an > prevAn {
				t.Fatalf("%s: longer recovery scored higher (%.1f/%.1f after %.1f/%.1f)", name, a, an, prevA, prevAn)
			}
			if i == 0 {
				first = *r.AnaerobicPeak
			}
			last = *r.AnaerobicPeak
			prevA, prevAn = a, an
		}
		if last >= first {
			t.Fatalf("%s: peak balance must fall with longer recovery", name)
		}
	}
}

// Requirement 6 (§7.4): training level individualises, smoothly and bounded.
func TestTrainingLevelScaling(t *testing.T) {
	type row struct{ easyA, thrA, fourA, fourAn, sprintAn float64 }
	var rows []row
	for _, l := range []float64{3, 4.5, 6, 7.5, 9} {
		lv := levelFor(l)
		var x row
		x.easyA, _, _ = score(t, "easy", easy, lv)
		x.thrA, _, _ = score(t, "threshold", threshold, lv)
		x.fourA, x.fourAn, _ = score(t, "10x400", fourHund(phase{90, jog}), lv)
		_, x.sprintAn, _ = score(t, "10x50", sprints(phase{90, walk}), lv)
		rows = append(rows, x)
	}
	for i := 1; i < len(rows); i++ {
		p, c := rows[i-1], rows[i]
		if c.easyA > p.easyA || c.thrA > p.thrA || c.fourA > p.fourA || c.fourAn > p.fourAn || c.sprintAn > p.sprintAn {
			t.Fatalf("higher level must not score higher: %+v -> %+v", p, c)
		}
	}
	lo, hi := rows[0], rows[len(rows)-1]
	if lo.thrA >= 5 || lo.thrA-hi.thrA > 2 || lo.easyA-hi.easyA < 1 {
		t.Fatalf("graded scaling: threshold %.1f..%.1f easy %.1f..%.1f", lo.thrA, hi.thrA, lo.easyA, hi.easyA)
	}
	if lo.fourAn-hi.fourAn > 1 {
		t.Fatalf("anaerobic individualisation comes mainly from vT/v6: %.1f..%.1f", lo.fourAn, hi.fourAn)
	}
}

// §7.3: native resolution keeps short sprints; a 10 s grid dilutes them.
func TestNativeResolutionPreservesSprints(t *testing.T) {
	s := build(sprints(phase{90, walk}))
	native := Calculate(s.input(level6, 2.56))
	coarse := Calculate(s.input(level6, 10))
	if *native.Anaerobic.Value < 1.5 || *coarse.Anaerobic.Value >= *native.Anaerobic.Value {
		t.Fatalf("native %.1f coarse %.1f", *native.Anaerobic.Value, *coarse.Anaerobic.Value)
	}
}

// §4.3: magnitude check against the 2005 white paper table.
func TestEPOCMagnitudes(t *testing.T) {
	constant := func(minutes, i float64) float64 {
		hr := restHR + i*(maxHR-restHR)
		var segs []wf.HRSegment
		for a := 0.0; a < minutes*60; a += 5 {
			segs = append(segs, wf.HRSegment{Start: us(a), End: us(a + 5), HR0: hr, HR1: hr})
		}
		p, _, ok := EPOCPeak(segs, restHR, maxHR)
		if !ok {
			t.Fatal("invalid")
		}
		return p
	}
	for _, c := range []struct{ minutes, i, lo, hi float64 }{{60, .6, 40, 90}, {30, .7, 60, 120}, {40, .85, 120, 260}, {210, .75, 120, 650}} {
		if p := constant(c.minutes, c.i); p < c.lo || p > c.hi {
			t.Fatalf("%v min @%.2f: %.0f outside %v-%v", c.minutes, c.i, p, c.lo, c.hi)
		}
	}
	if p := constant(60, .45); p > 25 || AerobicTE(p, 6) >= 1 {
		t.Fatalf("brisk walk %.1f", p)
	}
	if a, b := constant(30, .8), constant(30, .9); b <= a {
		t.Fatal("higher intensity must accumulate faster")
	}
}

func TestEPOCIsIndependentOfSegmentation(t *testing.T) {
	whole := []wf.HRSegment{{Start: us(0), End: us(600), HR0: 120, HR1: 180}, {Start: us(600), End: us(900), HR0: 180, HR1: 100}}
	split := []wf.HRSegment{{Start: us(0), End: us(300), HR0: 120, HR1: 150}, {Start: us(300), End: us(600), HR0: 150, HR1: 180}, whole[1]}
	a, _, _ := EPOCPeak(whole, restHR, maxHR)
	b, _, _ := EPOCPeak(split, restHR, maxHR)
	if a != b {
		t.Fatalf("whole-second split changed EPOC: %v vs %v", a, b)
	}
	odd := []wf.HRSegment{{Start: us(0), End: us(333.3), HR0: 120, HR1: 120 + 60*333.3/600}, {Start: us(333.3), End: us(600), HR0: 120 + 60*333.3/600, HR1: 180}, whole[1]}
	c, _, _ := EPOCPeak(odd, restHR, maxHR)
	if math.Abs(c-a)/a > 1e-4 {
		t.Fatalf("sub-second split moved EPOC: %v vs %v", c, a)
	}
	// Low intensity after a hard block decays, never adds to the peak.
	low := append(append([]wf.HRSegment{}, whole...), wf.HRSegment{Start: us(900), End: us(2700), HR0: 90, HR1: 90})
	p1, at1, _ := EPOCPeak(whole, restHR, maxHR)
	p2, at2, _ := EPOCPeak(low, restHR, maxHR)
	if p1 != p2 || at1 != at2 {
		t.Fatal("easy tail moved the peak")
	}
}

func TestAerobicMapping(t *testing.T) {
	if l1, l10 := 35*math.Pow(9, -5.0/9), 35*math.Pow(9, 4.0/9); math.Abs(l1-10) > 1.5 || math.Abs(l10-93) > 3 {
		t.Fatalf("TE2 anchors %v %v", l1, l10)
	}
	for _, l := range []float64{3, 4.5, 6, 7.5, 9} {
		th := AerobicThresholds(l)
		for k := 1; k < 5; k++ {
			if th[k] <= th[k-1] {
				t.Fatalf("crossing thresholds at L=%v: %v", l, th)
			}
		}
	}
	for _, peak := range []float64{20, 80, 150, 300} {
		if AerobicTE(peak, 3) < AerobicTE(peak, 6) || AerobicTE(peak, 6) < AerobicTE(peak, 9) {
			t.Fatal("higher level must need more EPOC")
		}
	}
	if AerobicTE(1e6, 6) != 5 || AerobicTE(0, 6) != 0 || AerobicTE(90, 6) != 3 {
		t.Fatal("bounds")
	}
	if AerobicThresholds(1) != AerobicThresholds(3) || AerobicThresholds(12) != AerobicThresholds(9) {
		t.Fatal("level must be clamped to 3-9 for scoring")
	}
}

func TestTrainingLevelInput(t *testing.T) {
	if l, s := TrainingLevel(LevelInput{}); l != 5 || s != "default" {
		t.Fatal(l, s)
	}
	if _, s := TrainingLevel(LevelInput{Days28: 3, Seconds90: 3600 * 20, Days90: 20}); s != "estimated" {
		t.Fatal(s)
	}
	prev, _ := TrainingLevel(LevelInput{Days28: 10})
	for m := 0.0; m < 1000; m++ {
		l, _ := TrainingLevel(LevelInput{Seconds28: m * 60 * 4, Days28: 10})
		if l < prev || l-prev > .1 {
			t.Fatalf("level must be smooth and monotone at %v min/week", m)
		}
		prev = l
	}
}

func TestAnaerobicGuards(t *testing.T) {
	s := build(easy)
	in := s.input(level6, 2.56)
	// A GPS spike without power/cadence corroboration scores nothing.
	spike := in.Effort.Points[300]
	spike.Speed, spike.Power, spike.Cadence = 7, nil, nil
	in.Effort.Points[300] = spike
	r := Calculate(in)
	if *r.Anaerobic.Value != 0 || r.UnsupportedSeconds == 0 || !contains(r.Anaerobic.ReasonCodes, "short_sprint_uncorroborated") {
		t.Fatalf("spike scored: %+v", r.Anaerobic)
	}
	// A speed beyond the absolute guard is excluded.
	in = s.input(level6, 2.56)
	in.Effort.Points[300].Speed = 13
	if r := Calculate(in); *r.Anaerobic.Value != 0 {
		t.Fatal("guard")
	}
	// Downhill fast running is not anaerobic work.
	in = build(sprints(phase{90, walk})).input(level6, 2.56)
	for i := range in.Effort.Points {
		in.Effort.Points[i].Grade = ptr(-.06)
	}
	if r := Calculate(in); *r.Anaerobic.Value != 0 {
		t.Fatalf("downhill scored %v", *r.Anaerobic.Value)
	}
	// Uphill uses the grade-adjusted speed.
	in = build(threshold).input(level6, 2.56)
	flat := *Calculate(in).Anaerobic.Value
	for i := range in.Effort.Points {
		in.Effort.Points[i].Grade = ptr(.04)
		in.Effort.Points[i].GAPSpeed = ptr(in.Effort.Points[i].Speed * 1.15)
	}
	if up := *Calculate(in).Anaerobic.Value; up <= flat {
		t.Fatal("GAP not used")
	}
}

func TestAbilityExceededOnlyFlags(t *testing.T) {
	in := build(fourHund(phase{45, jog})).input(level6, 2.56)
	base := Calculate(in)
	in.Fitness.V6 = 4.5 // stale, too-low parameters: reserve is much smaller
	r := Calculate(in)
	if !contains(r.Anaerobic.ReasonCodes, "ability_reference_exceeded") || *r.Anaerobic.Value < *base.Anaerobic.Value {
		t.Fatalf("exceeded must flag, not subtract: %+v", r.Anaerobic)
	}
}

func TestMissingInputsAreNullNeverZero(t *testing.T) {
	good := build(threshold).input(level6, 2.56)
	cases := map[string]func(*Input) (Score, string){
		"hr": func(in *Input) (Score, string) { in.HR = nil; return Calculate(*in).Aerobic, "hr_reference_missing" },
		"tier": func(in *Input) (Score, string) {
			in.LoadTier = "approximate"
			return Calculate(*in).Aerobic, "hr_exposure_insufficient"
		},
		"segments": func(in *Input) (Score, string) {
			in.Exposure.Segments = nil
			return Calculate(*in).Aerobic, "hr_exposure_insufficient"
		},
		"fitness": func(in *Input) (Score, string) {
			in.Fitness = nil
			return Calculate(*in).Anaerobic, "speed_reference_unavailable"
		},
		"future": func(in *Input) (Score, string) {
			in.Fitness.At = in.Start + 1
			return Calculate(*in).Anaerobic, "speed_reference_unavailable"
		},
		"status": func(in *Input) (Score, string) {
			in.Fitness.Status = "insufficient"
			return Calculate(*in).Anaerobic, "speed_reference_unavailable"
		},
		"effort": func(in *Input) (Score, string) {
			in.Effort = nil
			return Calculate(*in).Anaerobic, "effort_features_missing"
		},
	}
	for name, fn := range cases {
		in := good
		f := *good.Fitness
		in.Fitness = &f
		s, reason := fn(&in)
		if s.Value != nil || s.Status != "unavailable" || s.Label != "" || !contains(s.ReasonCodes, reason) {
			t.Fatalf("%s: %+v", name, s)
		}
	}
	r := Calculate(good)
	if r.Aerobic.Status != "complete" || r.Anaerobic.Status != "complete" {
		t.Fatalf("complete inputs: %+v %+v", r.Aerobic, r.Anaerobic)
	}
	good.Level = LevelInput{}
	if r := Calculate(good); r.Aerobic.Status != "estimated" || r.LevelStatus != "default" {
		t.Fatal("default level must mark the aerobic score estimated")
	}
}

func TestDeterministicAndCausal(t *testing.T) {
	in := build(fourHund(phase{90, jog})).input(level6, 2.56)
	a, b := Calculate(in), Calculate(in)
	if a.InputHash != b.InputHash || *a.Aerobic.Value != *b.Aerobic.Value || a.Policy != Policy || len(ConfigHash()) != 64 {
		t.Fatal("not deterministic")
	}
	// Only this run's facts and pre-run references enter the input; any of
	// them changing changes the identity.
	in.Level.Seconds28++
	if Calculate(in).InputHash == a.InputHash {
		t.Fatal("input change not in hash")
	}
}

func TestLabels(t *testing.T) {
	for v, want := range map[float64]string{0: "none", .9: "none", 1: "minor", 2.5: "maintaining", 3: "improving", 4.9: "highly_improving", 5: "overreaching"} {
		if Label(v) != want {
			t.Fatal(v)
		}
	}
	if s := scored(4.96, false, nil); *s.Value != 5 || s.Label != "overreaching" {
		t.Fatal("label must follow the rounded value")
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
