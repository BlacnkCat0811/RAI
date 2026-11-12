// Package trainingeffect computes a run's aerobic and anaerobic training
// effect (0.0-5.0 each). It follows the published Firstbeat principles (an
// EPOC-like accumulation for the aerobic score, supra-threshold work for the
// anaerobic score) with RAI's own, openly stated constants. It is not and does
// not claim to be the Garmin/Firstbeat score.
//
// The package is pure: no clock, no I/O, no mutable globals. It must not import
// trainingload (that package embeds this result).
package trainingeffect

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"sort"

	wf "rai/server/internal/workoutfeatures"
)

const (
	Schema    = "rai.training_effect/1"
	Algorithm = "rai.training_effect"
	Version   = "v1"
)

// Model constants. Any change must change Policy, and therefore the stack.
const (
	// EPOC proxy: dE/dt = r(i)(1-E/Einf) - lambda(i)E, per minute.
	epocRate      = 40.0
	epocOnset     = .30
	epocCeiling   = 900.0
	epocHalfLifeM = 20.0
	epocDecayAt   = .50

	levelMin, levelMax, levelDefault = 3.0, 9.0, 5.0
	levelMinDays                     = 5

	boundaryRatio   = .97
	reserveSeconds  = 360.0
	recoveryTauS    = 300.0
	durationFullS   = 120.0
	durationFloorS  = 600.0
	durationFloorW  = .4
	peakWeight      = .6
	peakCap         = 1.5
	x0Base          = 2.0
	x0PerLevel      = .05
	shortBoutS      = 20.0
	hrRiseBPM       = 5.0
	hrHighFraction  = .8
	hrWindowS       = 30.0
	powerRatio      = 1.25
	cadenceRatio    = 1.05
	maxSpeedMPS     = 12.0
	v90sGuard       = 1.5
	v6Guard         = 1.6
	downhillGrade   = -.03
	contiguousUs    = 1_000_000
	unsupportedMax  = .30
	exceededRatio   = 1.5
	exceededSeconds = 30.0
)

var (
	aerobicBase  = [5]float64{8, 35, 90, 180, 320}
	aerobicGamma = [5]float64{1, 1, .8, .6, .45}
	// Weekly running minutes (band midpoints) -> training level.
	levelKnots = [][2]float64{{7.5, 2}, {22.5, 3}, {45, 4}, {90, 5}, {150, 6}, {240, 7}, {360, 7.5}, {480, 8}, {600, 8.5}, {720, 9}}
)

// Policy is the fixed identity of every constant above.
const Policy = "epoc:R40;onset.30;cubic;Einf900;half20m;decay<.50;abs-1s-substeps|" +
	"aerobic:base8/35/90/180/320;gamma1/1/.8/.6/.45;9^(g(L-6)/9);linear;cap5|" +
	"level:running-28d-weekly-minutes;fallback90d;min5days;default5;knots-band-midpoints;score-clamp3-9|" +
	"anaerobic:vb.97vT;reserve(v6-vb)x360;quadratic-below-v6;linear-above;tau300;wdur120-600-.4;x=W/D+.6min(1.5,Bpk/D);x0=2(1+.05(L-6));5(1-e^-x/x0)|" +
	"guard:12mps;1.5v90s|1.6v6;contiguous1s;bout20s;hr+5|.80hrr;window30s;power1.25;cadence1.05;gap-preferred;downhill-.03;unsupported.30;exceeded1.5x30s|" +
	"round1/v1"

func ConfigHash() string { return digest(Policy) }

func digest(v any) string {
	var b []byte
	if s, ok := v.(string); ok {
		b = []byte(s)
	} else {
		b, _ = json.Marshal(v)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func finite(v float64) bool  { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func ptr(v float64) *float64 { return &v }
func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

// HRReference is the HR reference effective at the run (rest/max, bpm).
type HRReference struct {
	RestHR float64 `json:"restingHR"`
	MaxHR  float64 `json:"maxHR"`
	Hash   string  `json:"hash"`
}

// FitnessReference is the running-fitness speed parameter set published
// before the run (never today's estimate).
type FitnessReference struct {
	VT     float64  `json:"vTMPS"`
	V6     float64  `json:"v6MPS"`
	V90s   *float64 `json:"v90sMPS"`
	At     int64    `json:"asOfUs"`
	Hash   string   `json:"hash"`
	Status string   `json:"speedStatus"`
}

// LevelInput summarises completed same-source runs before this run's start.
type LevelInput struct {
	Seconds28 float64 `json:"activeSeconds28d"`
	Days28    int     `json:"trainingDays28d"`
	Seconds90 float64 `json:"activeSeconds90d"`
	Days90    int     `json:"trainingDays90d"`
}

type Input struct {
	WorkoutID string             `json:"workoutID"`
	Start     int64              `json:"startUs"`
	LoadTier  string             `json:"loadQualityTier"`
	Exposure  wf.HRExposure      `json:"exposure"`
	Effort    *wf.EffortFeatures `json:"effort"`
	HR        *HRReference       `json:"hrReference"`
	Fitness   *FitnessReference  `json:"fitnessReference"`
	Level     LevelInput         `json:"level"`
}

type Score struct {
	Value       *float64 `json:"value"`
	Label       string   `json:"label"`
	Status      string   `json:"status"`
	ReasonCodes []string `json:"reasonCodes"`
}

type Result struct {
	Schema               string   `json:"schemaVersion"`
	WorkoutID            string   `json:"workoutID"`
	Aerobic              Score    `json:"aerobic"`
	Anaerobic            Score    `json:"anaerobic"`
	EPOCPeak             *float64 `json:"epocPeak"`
	EPOCPeakAt           *int64   `json:"epocPeakAtUs"`
	Level                *float64 `json:"trainingLevel"`
	LevelStatus          string   `json:"trainingLevelStatus"`
	AnaerobicWork        *float64 `json:"anaerobicWorkM"`
	AnaerobicReserve     *float64 `json:"anaerobicReserveM"`
	AnaerobicPeak        *float64 `json:"anaerobicPeakM"`
	HighIntensitySeconds float64  `json:"highIntensitySeconds"`
	UnsupportedSeconds   float64  `json:"unsupportedSeconds"`
	Bouts                int      `json:"bouts"`
	PrimaryBenefit       string   `json:"primaryBenefit,omitempty"`
	Policy               string   `json:"policy"`
	InputHash            string   `json:"inputHash"`
}

// Label maps a rounded score to its fixed band.
func Label(v float64) string {
	switch {
	case v < 1:
		return "none"
	case v < 2:
		return "minor"
	case v < 3:
		return "maintaining"
	case v < 4:
		return "improving"
	case v < 5:
		return "highly_improving"
	}
	return "overreaching"
}

func unavailable(reasons ...string) Score {
	return Score{Status: "unavailable", ReasonCodes: append([]string{}, reasons...)}
}
func scored(v float64, estimated bool, reasons []string) Score {
	v = round1(math.Min(5, math.Max(0, v)))
	s := Score{Value: &v, Label: Label(v), Status: "complete", ReasonCodes: reasons}
	if s.ReasonCodes == nil {
		s.ReasonCodes = []string{}
	}
	if estimated {
		s.Status = "estimated"
	}
	return s
}

// TrainingLevel maps pre-run weekly running minutes to a training level.
func TrainingLevel(in LevelInput) (float64, string) {
	var weekly float64
	status := "observed"
	switch {
	case in.Days28 >= levelMinDays:
		weekly = in.Seconds28 / 60 / 4
	case in.Days90 >= levelMinDays:
		weekly, status = in.Seconds90/60/(90.0/7), "estimated"
	default:
		return levelDefault, "default"
	}
	if !finite(weekly) || weekly < 0 {
		return levelDefault, "default"
	}
	k := levelKnots
	if weekly <= k[0][0] {
		return k[0][1], status
	}
	for i := 1; i < len(k); i++ {
		if weekly <= k[i][0] {
			f := (weekly - k[i-1][0]) / (k[i][0] - k[i-1][0])
			return k[i-1][1] + f*(k[i][1]-k[i-1][1]), status
		}
	}
	return k[len(k)-1][1], status
}

func scoringLevel(l float64) float64 { return math.Min(levelMax, math.Max(levelMin, l)) }

// AerobicThresholds returns the EPOC proxy needed for TE 1..5 at level l.
func AerobicThresholds(l float64) [5]float64 {
	l = scoringLevel(l)
	var t [5]float64
	for k := range t {
		t[k] = aerobicBase[k] * math.Pow(9, aerobicGamma[k]*(l-6)/9)
	}
	return t
}

// AerobicTE maps an EPOC proxy peak to the unrounded 0-5 aerobic score.
func AerobicTE(peak, level float64) float64 {
	if peak <= 0 {
		return 0
	}
	t := AerobicThresholds(level)
	prev := 0.0
	for k, th := range t {
		if peak < th {
			return float64(k) + (peak-prev)/(th-prev)
		}
		prev = th
	}
	return 5
}

// EPOCPeak integrates the EPOC proxy over native HR segments. Sub-steps are
// cut at absolute whole seconds, so splitting a segment on a second boundary
// changes nothing. Gaps between segments decay at zero intensity.
func EPOCPeak(segments []wf.HRSegment, rest, maxHR float64) (float64, int64, bool) {
	if !(maxHR > rest) || !finite(rest) || !finite(maxHR) {
		return 0, 0, false
	}
	e, peak := 0.0, 0.0
	var peakAt int64
	var last int64
	decay := func(minutes, i float64) float64 {
		if i >= epocDecayAt {
			return 0
		}
		return math.Ln2 / epocHalfLifeM * (epocDecayAt - i) / epocDecayAt * minutes
	}
	for n, s := range segments {
		if s.End <= s.Start || !finite(s.HR0) || !finite(s.HR1) || n > 0 && s.Start < last {
			return 0, 0, false
		}
		if n > 0 && s.Start > last {
			e *= math.Exp(-decay(float64(s.Start-last)/60e6, 0))
		}
		for a := s.Start; a < s.End; {
			b := min(s.End, (a/1_000_000+1)*1_000_000)
			mid := float64(a+b) / 2
			hr := s.HR0 + (s.HR1-s.HR0)*(mid-float64(s.Start))/float64(s.End-s.Start)
			i := math.Min(1, math.Max(0, (hr-rest)/(maxHR-rest)))
			h := float64(b-a) / 60e6
			r := epocRate * math.Pow(math.Max(0, i-epocOnset), 3)
			k := r/epocCeiling + decay(1, i)
			if k > 0 {
				eq := r / k
				e = eq + (e-eq)*math.Exp(-k*h)
			} else {
				e += r * h
			}
			if e > peak {
				peak, peakAt = e, b
			}
			a = b
		}
		last = s.End
	}
	return peak, peakAt, true
}

// Calculate never fails a day release: invalid or missing inputs become
// explicit unavailable scores with reason codes, never a 0.
func Calculate(in Input) Result {
	r := Result{Schema: Schema, WorkoutID: in.WorkoutID, Policy: Policy,
		InputHash: digest(struct {
			Input  Input
			Policy string
		}{in, Policy})}
	level, levelStatus := TrainingLevel(in.Level)
	r.Level, r.LevelStatus = ptr(round1(level)), levelStatus
	r.Aerobic = aerobic(in, level, levelStatus, &r)
	r.Anaerobic = anaerobic(in, level, &r)
	return r
}

func aerobic(in Input, level float64, levelStatus string, r *Result) Score {
	if in.HR == nil || !(in.HR.MaxHR > in.HR.RestHR) {
		return unavailable("hr_reference_missing")
	}
	if in.LoadTier != "complete" && in.LoadTier != "estimated" || len(in.Exposure.Segments) == 0 {
		return unavailable("hr_exposure_insufficient")
	}
	peak, at, ok := EPOCPeak(in.Exposure.Segments, in.HR.RestHR, in.HR.MaxHR)
	if !ok {
		return unavailable("invalid_input")
	}
	r.EPOCPeak = ptr(math.Round(peak*10) / 10)
	if peak > 0 {
		r.EPOCPeakAt = &at
	}
	reasons := []string{}
	if in.LoadTier == "estimated" {
		reasons = append(reasons, "hr_exposure_estimated")
	}
	if levelStatus != "observed" {
		reasons = append(reasons, "training_level_"+levelStatus)
	}
	return scored(AerobicTE(peak, level), len(reasons) > 0, reasons)
}

type bout struct {
	first, last  int
	start, end   int64
	corroborated bool
}

func usableFitness(f *FitnessReference, start int64) bool {
	return f != nil && finite(f.VT) && finite(f.V6) && f.VT > 0 && f.V6 > boundaryRatio*f.VT && f.Hash != "" && f.At < start &&
		(f.Status == "supported" || f.Status == "estimated") && (f.V90s == nil || finite(*f.V90s) && *f.V90s > 0)
}

func anaerobic(in Input, level float64, r *Result) Score {
	if !usableFitness(in.Fitness, in.Start) {
		return unavailable("speed_reference_unavailable")
	}
	e := in.Effort
	if e == nil || e.Schema != wf.EffortSchema {
		return unavailable("effort_features_missing")
	}
	if e.MedianSpeed == nil {
		return unavailable("speed_missing")
	}
	pts := e.Points
	for i, p := range pts {
		if p.End <= p.Start || !finite(p.Speed) || i > 0 && p.Start < pts[i-1].End {
			return unavailable("invalid_input")
		}
	}
	f := in.Fitness
	vb := boundaryRatio * f.VT
	span := f.V6 - vb
	reserve := span * reserveSeconds
	limit := v6Guard * f.V6
	if f.V90s != nil {
		limit = v90sGuard * *f.V90s
	}
	limit = math.Min(maxSpeedMPS, limit)
	speed := func(p wf.EffortPoint) float64 {
		if p.GAPSpeed != nil && finite(*p.GAPSpeed) {
			return *p.GAPSpeed
		}
		return p.Speed
	}
	downhill := func(p wf.EffortPoint) bool { return p.Grade != nil && *p.Grade < downhillGrade }
	eligible := func(p wf.EffortPoint) bool { return p.Supported && p.Speed <= limit }
	above := func(p wf.EffortPoint) bool { return eligible(p) && !downhill(p) && speed(p) > vb }

	reasons := map[string]bool{}
	boutOf := make([]int, len(pts)) // bout index+1 per point
	var bouts []bout
	for i := 0; i < len(pts); i++ {
		if !above(pts[i]) {
			continue
		}
		b := bout{first: i, last: i, start: pts[i].Start, end: pts[i].End}
		for i+1 < len(pts) && above(pts[i+1]) && pts[i+1].Start-pts[i].End <= contiguousUs {
			i++
			b.last, b.end = i, pts[i].End
		}
		bouts = append(bouts, b)
	}
	for n := range bouts {
		b := &bouts[n]
		b.corroborated = corroborate(pts, *b, e, in.HR, reasons)
		for i := b.first; i <= b.last; i++ {
			boutOf[i] = n + 1
		}
		if b.corroborated {
			r.Bouts++
		}
	}

	var work, balance, peak, exceeded, high, unsupported float64
	var last int64
	for i, p := range pts {
		dt := float64(p.End-p.Start) / 1e6
		if i > 0 && p.Start > last {
			balance *= math.Exp(-float64(p.Start-last) / 1e6 / recoveryTauS)
		}
		last = p.End
		if n := boutOf[i]; n > 0 {
			high += dt
			b := bouts[n-1]
			if b.corroborated {
				v := speed(p)
				z := (v - vb) / span
				rate := v - vb
				if z < 1 {
					rate = span * z * z
				}
				t := float64(p.Start+p.End)/2e6 - float64(b.start)/1e6
				spend := rate * dt * durationWeight(t)
				work += spend
				balance += spend
			} else {
				unsupported += dt
				balance *= math.Exp(-dt / recoveryTauS)
			}
		} else {
			if !eligible(p) && p.Speed > vb {
				high += dt
				unsupported += dt
				reasons["speed_point_unsupported"] = true
			}
			balance *= math.Exp(-dt / recoveryTauS)
		}
		peak = math.Max(peak, balance)
		if balance > exceededRatio*reserve {
			exceeded += dt
		}
	}
	r.AnaerobicWork = ptr(math.Round(work*10) / 10)
	r.AnaerobicReserve = ptr(math.Round(reserve*10) / 10)
	r.AnaerobicPeak = ptr(math.Round(peak*10) / 10)
	r.HighIntensitySeconds = math.Round(high*10) / 10
	r.UnsupportedSeconds = math.Round(unsupported*10) / 10
	if exceeded > exceededSeconds {
		reasons["ability_reference_exceeded"] = true
	}
	estimated := false
	if high > 0 && unsupported/high > unsupportedMax {
		estimated = true
		reasons["high_intensity_unsupported"] = true
	}
	if e.Indoor != nil && *e.Indoor {
		estimated = true
		reasons["indoor_speed"] = true
	}
	if f.Status == "estimated" {
		estimated = true
		reasons["speed_reference_estimated"] = true
	}
	x := work/reserve + peakWeight*math.Min(peakCap, peak/reserve)
	x0 := x0Base * (1 + x0PerLevel*(scoringLevel(level)-6))
	codes := make([]string, 0, len(reasons))
	for k := range reasons {
		codes = append(codes, k)
	}
	sort.Strings(codes)
	return scored(5*(1-math.Exp(-x/x0)), estimated, codes)
}

func durationWeight(t float64) float64 {
	switch {
	case t <= durationFullS:
		return 1
	case t >= durationFloorS:
		return durationFloorW
	}
	return 1 - (1-durationFloorW)*(t-durationFullS)/(durationFloorS-durationFullS)
}

// corroborate rejects GPS-only speed. Bouts of >=20 s need an HR response;
// shorter ones (or ones without any HR) need concurrent power or cadence.
func corroborate(pts []wf.EffortPoint, b bout, e *wf.EffortFeatures, ref *HRReference, reasons map[string]bool) bool {
	dur := float64(b.end-b.start) / 1e6
	if dur >= shortBoutS {
		var startHR *float64
		for i := b.first; i >= 0 && b.start-pts[i].End <= 10e6; i-- {
			if pts[i].HR != nil {
				startHR = pts[i].HR
				break
			}
		}
		maxHR, seen := 0.0, false
		for i := b.first; i < len(pts) && pts[i].Start <= b.end+int64(hrWindowS*1e6); i++ {
			if pts[i].HR != nil {
				maxHR, seen = math.Max(maxHR, *pts[i].HR), true
			}
		}
		if seen {
			if startHR != nil && maxHR-*startHR >= hrRiseBPM || ref != nil && ref.MaxHR > ref.RestHR && maxHR >= ref.RestHR+hrHighFraction*(ref.MaxHR-ref.RestHR) {
				return true
			}
			reasons["bout_uncorroborated"] = true
			return false
		}
	}
	var power, cadence, pt, ct float64
	for i := b.first; i <= b.last; i++ {
		dt := float64(pts[i].End-pts[i].Start) / 1e6
		if pts[i].Power != nil {
			power += *pts[i].Power * dt
			pt += dt
		}
		if pts[i].Cadence != nil {
			cadence += *pts[i].Cadence * dt
			ct += dt
		}
	}
	okPower := pt > 0 && e.MedianPower != nil && *e.MedianPower > 0 && power/pt >= powerRatio**e.MedianPower
	okCadence := ct > 0 && e.MedianCadence != nil && *e.MedianCadence > 0 && cadence/ct >= cadenceRatio**e.MedianCadence
	if okPower || okCadence {
		return true
	}
	reasons["short_sprint_uncorroborated"] = true
	return false
}
