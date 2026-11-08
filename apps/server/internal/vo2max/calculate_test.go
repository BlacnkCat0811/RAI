package vo2max

import (
	"math"
	"testing"
)

const minute = int64(60 * 1e6)

func f(v float64) *float64 { return &v }

func sampleSeries(start int64, durationMin int, hr, speed float64, grade float64, withHR, withSpeed bool) []Sample {
	var out []Sample
	for i := 0; i < durationMin*60; i++ {
		s := Sample{Start: start + int64(i)*int64(1e6), End: start + int64(i+1)*int64(1e6)}
		if withHR {
			s.HR = f(hr)
		}
		if withSpeed {
			s.Speed = f(speed)
		}
		if grade >= 0 {
			s.Grade = f(grade)
		}
		out = append(out, s)
	}
	return out
}

func workout(id string, start int64, minutes int, hr, speed float64) Workout {
	return Workout{ID: id, DatasetID: "ds", Generation: 1, ActivityType: 37, Outdoor: true,
		Start: start, End: start + int64(minutes)*minute, Day: "2026-01-01",
		Samples:           sampleSeries(start, minutes, hr, speed, 0, true, true),
		DistanceM:         f(speed * float64(minutes*60)),
		RecordedDistanceM: f(speed * float64(minutes*60) * 1.01),
		TemperatureC:      f(15), HumidityPct: f(50), KnownAt: start + int64(minutes)*minute}
}

func ref() Reference {
	return Reference{MaxHR: 190, RestHR: 50, MaxMeasured: 1, RestMeasured: 1, ReferenceID: "ref", Hash: "h", SourceEpoch: "e1"}
}

func TestEvaluateQualifiedWorkout(t *testing.T) {
	ev, err := EvaluateWorkout(workout("w1", 0, 30, 141, 2.6), ref())
	if err != nil {
		t.Fatal(err)
	}
	if !ev.Qualified || ev.Windows != 4 {
		t.Fatalf("want 4 windows qualified: %+v", ev)
	}
	// r=.65, v=156 m/min -> 3.5 + 0.2*156/.65 = 51.5
	if math.Abs(*ev.CandidateV-51.5) > 1e-9 {
		t.Fatalf("V=%.6f", *ev.CandidateV)
	}
}

func TestEvaluateRejectsShortPauseGapEnvironmentDistance(t *testing.T) {
	w := workout("w", 0, 20, 154, 2.6)
	ev, _ := EvaluateWorkout(w, ref())
	if ev.Qualified || len(ev.Reasons) == 0 {
		t.Fatalf("short should reject: %+v", ev)
	}
	w = workout("w", 0, 30, 154, 2.6)
	w.Pauses = []Pause{{Start: 5 * minute, End: 7 * minute}}
	ev, _ = EvaluateWorkout(w, ref())
	if ev.Qualified {
		t.Fatal("2/30 pause > 2% must reject")
	}
	w = workout("w", 0, 30, 154, 2.6)
	w.Pauses = []Pause{{Start: 11 * minute, End: 11*minute + 20*int64(1e6)}}
	ev, _ = EvaluateWorkout(w, ref())
	if !ev.Qualified || ev.Windows != 3 {
		t.Fatalf("20s pause should leave 3 windows: %+v", ev)
	}
	w = workout("w", 0, 30, 154, 2.6)
	for i := 12 * 60; i < 12*60+30; i++ {
		w.Samples[i].HR = nil
	}
	ev, _ = EvaluateWorkout(w, ref())
	if !ev.Qualified || ev.Windows != 3 {
		t.Fatalf("gap should leave 3 windows: %+v", ev)
	}
	w = workout("w", 0, 30, 154, 2.6)
	w.TemperatureC = f(30)
	ev, _ = EvaluateWorkout(w, ref())
	if ev.Qualified {
		t.Fatal("hot environment must reject")
	}
	w = workout("w", 0, 30, 154, 2.6)
	w.HumidityPct = nil
	ev, _ = EvaluateWorkout(w, ref())
	if !ev.Qualified || ev.Quality > 0.7 {
		t.Fatalf("missing humidity cap .7: %+v", ev)
	}
	w = workout("w", 0, 30, 154, 2.6)
	w.RecordedDistanceM = f(*w.DistanceM * 1.10)
	ev, _ = EvaluateWorkout(w, ref())
	if ev.Qualified {
		t.Fatal("distance mismatch must reject")
	}
}

func ev(id, day string, at int64, v, hr, speed, q float64) Evidence {
	return Evidence{WorkoutID: id, Day: day, At: at, SourceEpoch: "e1", ReferenceHash: "h", V: v, Q: q,
		HRMean: hr, SpeedMPS: speed, MaxHR: 190, RestHR: 50, KnownAt: at, EnvironmentKnown: true}
}

func TestAggregatePreliminaryAndLOO(t *testing.T) {
	base := int64(0)
	in := AggregateInput{DatasetID: "ds", Generation: 1, AsOf: 10 * DayUs, ReferenceHash: "h", SourceEpoch: "e1",
		Evidence: []Evidence{ev("a", "d0", base+1, 50, 154, 2.6, 1), ev("b", "d4", base+4*DayUs+1, 52, 154, 2.6, 1), ev("c", "d8", base+8*DayUs+1, 48, 154, 2.6, 1)}}
	r, err := Aggregate(in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Value != nil || r.Status != "collecting" || !r.Experimental {
		t.Fatalf("preliminary: %+v", r)
	}
	if r.LOO.SupportOK || r.LOO.Range != nil || r.Confidence > 0.4 {
		t.Fatalf("3 evidence LOO must be insufficient: %+v", r.LOO)
	}
}

func TestAggregateEstablishedSensitivityAndLOO(t *testing.T) {
	var es []Evidence
	vals := []float64{49, 51, 50, 52, 48}
	for i, v := range vals {
		es = append(es, ev(string(rune('a'+i)), "d", int64(i*4)*DayUs+1, v, 154, 2.6, 1))
	}
	r, err := Aggregate(AggregateInput{DatasetID: "ds", Generation: 1, AsOf: 21 * DayUs, ReferenceHash: "h", SourceEpoch: "e1", Evidence: es})
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "established" || r.Value != nil || !r.CandidateOnly {
		t.Fatalf("established: %+v", r)
	}
	if r.Sensitivity.Unbounded || r.Sensitivity.Corners != 8 || r.Sensitivity.Range == nil {
		t.Fatalf("sensitivity: %+v", r.Sensitivity)
	}
	if !r.LOO.SupportOK || r.LOO.Folds != 5 || r.LOO.Range == nil {
		t.Fatalf("loo: %+v", r.LOO)
	}
}

func TestAggregatePublishesValueWhenSensitivityNarrow(t *testing.T) {
	// r=(182-35)/(209-35)=.8448; V=3.5+.2*134/r=35.222. All eight corners
	// stay inside the 30% relative-width gate, unlike the abstention fixture
	// above where the same evidence center is much lower.
	var es []Evidence
	for i := 0; i < 5; i++ {
		e := ev(string(rune('a'+i)), "d", int64(i*4)*DayUs+1, 35.22244897959183, 182, 134.0/60, 1)
		e.MaxHR, e.RestHR = 209, 35
		es = append(es, e)
	}
	r, err := Aggregate(AggregateInput{DatasetID: "ds", Generation: 1, AsOf: 21 * DayUs, ReferenceHash: "h", SourceEpoch: "e1", Evidence: es})
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != "established" || r.Value == nil || math.Abs(*r.Value-35.22244897959183) > 1e-9 || r.CandidateOnly {
		t.Fatalf("publishable value: %+v", r)
	}
	if r.Sensitivity.Unbounded || r.Sensitivity.Corners != 8 || r.Sensitivity.Width == nil || *r.Sensitivity.Width > .30 {
		t.Fatalf("sensitivity: %+v", r.Sensitivity)
	}
	if !r.LOO.SupportOK || r.LOO.Folds != 5 || r.LOO.Range == nil {
		t.Fatalf("loo: %+v", r.LOO)
	}
}

func TestAggregateOutlierIsolationAndConfirmation(t *testing.T) {
	var es []Evidence
	for i := 0; i < 4; i++ {
		es = append(es, ev(string(rune('a'+i)), "d", int64(i*5)*DayUs+1, 50, 154, 2.6, 1))
	}
	bad := ev("bad", "d", 18*DayUs+1, 100, 154, 2.6, 1)
	r, _ := Aggregate(AggregateInput{DatasetID: "ds", AsOf: 19 * DayUs, ReferenceHash: "h", SourceEpoch: "e1", Evidence: append(es, bad)})
	if r.TargetLog == nil || math.Abs(math.Exp(*r.TargetLog)-50) > 1e-9 || r.Workouts != 4 {
		t.Fatalf("outlier should be isolated: %+v", r)
	}
	ok := ev("ok", "d", 19*DayUs+1, 102, 154, 2.6, 1)
	r, _ = Aggregate(AggregateInput{DatasetID: "ds", AsOf: 20 * DayUs, ReferenceHash: "h", SourceEpoch: "e1", Evidence: append(es, bad, ok)})
	if r.Workouts != 6 {
		t.Fatalf("confirmed outlier should remain: %+v", r)
	}
}

func TestSameDayAndSingleWorkoutCaps(t *testing.T) {
	es := []Evidence{
		ev("a", "same", 1, 50, 154, 2.6, 1),
		ev("b", "same", 2, 50, 154, 2.6, 1),
		ev("c", "same", 3, 50, 154, 2.6, 1),
		ev("d", "other", 5*DayUs, 50, 154, 2.6, 1),
	}
	w := []float64{1, 1, 1, 1}
	capSameDay(es, w)
	if w[0]+w[1]+w[2] > 1.0000001 {
		t.Fatalf("same day cap: %v", w)
	}
	waterFill(w)
	sum := 0.0
	for _, x := range w {
		sum += x
		if x > 1.0/3.0+1e-9 {
			t.Fatalf("single workout cap: %v", w)
		}
	}
	if math.Abs(sum-1) > 1e-9 {
		t.Fatalf("sum %v", sum)
	}
}

func TestAggregateNoEvidenceAndStale(t *testing.T) {
	r, _ := Aggregate(AggregateInput{DatasetID: "ds", AsOf: 10 * DayUs})
	if r.Status != "collecting" || r.Value != nil {
		t.Fatalf("empty: %+v", r)
	}
	var es []Evidence
	for i := 0; i < 3; i++ {
		es = append(es, ev(string(rune('a'+i)), "d", int64(i)*DayUs+1, 50, 154, 2.6, 1))
	}
	r, _ = Aggregate(AggregateInput{DatasetID: "ds", AsOf: 40 * DayUs, ReferenceHash: "h", SourceEpoch: "e1", Evidence: es})
	if r.Status != "stale" || r.Value != nil {
		t.Fatalf("stale: %+v", r)
	}
}

func TestSubmaxScopeCarriesStack(t *testing.T) {
	if got := ScopeFor("synthetic-v2"); got != "model/synthetic-v2/athlete/vo2_submax" {
		t.Fatalf("scope=%s", got)
	}
}
