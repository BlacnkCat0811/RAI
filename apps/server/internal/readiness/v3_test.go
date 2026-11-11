package readiness

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

func v2Fixture() ReadinessSnapshot {
	in := fixture()
	in.Config = DefaultV3Config("UTC")
	in.Recovery.Model = RecoveryModelV2
	for i := range in.Loads {
		in.Loads[i].Model = LoadModelV2
	}
	return in
}

func TestV2AllowlistAndIdentity(t *testing.T) {
	in := v2Fixture()
	r, err := CalculateV3(in)
	if err != nil {
		t.Fatal(err)
	}
	if r.ModelVersion != ModelV3 || r.Schema != SchemaV3 || r.Score == nil || *r.Score <= 0 || r.Status != "available" {
		t.Fatalf("v2 result %+v", r)
	}
	// Mixing v1 into an allowlist v2 calculation fails closed.
	in = v2Fixture()
	in.Loads[0].Model = LoadModel
	r, err = CalculateV3(in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Score != nil || r.Status != "source_conflict" {
		t.Fatalf("mixed load must fail closed: %+v", r)
	}
	found := false
	for _, x := range r.Reasons {
		if x == "load_model_not_allowed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("reasons %v", r.Reasons)
	}
	// Partial Recovery v2 never feeds a full readiness score.
	in = v2Fixture()
	in.Recovery.Status = "partial"
	in.Recovery.Quality = "partial"
	r, _ = CalculateV3(in)
	if r.Score != nil {
		t.Fatalf("partial recovery must not produce readiness: %+v", r)
	}
}

func TestV2NoWorkoutRefreshAndSupersede(t *testing.T) {
	in := v2Fixture()
	r, err := CalculateV3(in)
	if err != nil || r.Score == nil {
		t.Fatal(err, r.Score)
	}
	f, err := FreshenV3(r, in.AsOf+6*Hour)
	if err != nil || f.Score == nil {
		t.Fatal(err, f.Score)
	}
	if *f.Score <= 0 || *f.Score > *r.Score+0.01 {
		t.Fatalf("load-free refresh must remain a valid bounded score: %v %v", *r.Score, *f.Score)
	}
	before, err := FreshenV3(r, r.ValidBefore)
	if err != nil {
		t.Fatal(err)
	}
	if before.Score != nil || before.Status != "superseded" {
		t.Fatalf("must supersede at civil-day anchor boundary: %+v", before)
	}
}

func withLoad(in ReadinessSnapshot, id string, end int64, au *float64, quality string, conflict bool) ReadinessSnapshot {
	day := time.UnixMicro(end).UTC().Format(time.DateOnly)
	in.Loads = append(in.Loads, WorkoutLoad{ID: id, End: end, Model: LoadModelV2, AU: au, Quality: quality, Compatible: !conflict, Conflict: conflict, Days: []DailyLoad{{day, au, quality}}})
	return in
}

// Estimated-tier (partial quality) loads are known; only their value counts.
func TestV3PartialLoadIsKnown(t *testing.T) {
	complete, err := CalculateV3(v2Fixture())
	if err != nil {
		t.Fatal(err)
	}
	in := v2Fixture()
	for i := range in.Loads {
		in.Loads[i].Quality = "partial"
		in.Loads[i].Days[0].Quality = "partial"
	}
	partial, err := CalculateV3(in)
	if err != nil || partial.Score == nil || *partial.Score != *complete.Score || partial.Baseline.TrainingDays != complete.Baseline.TrainingDays {
		t.Fatalf("partial loads changed readiness: %v %+v", err, partial.Reasons)
	}
}

// E8: an unknown Workout months ago no longer vetoes today's score, while a
// recent one whose decayed weight is material still does.
func TestV3UnknownBurdenIsDecayed(t *testing.T) {
	base, _ := CalculateV3(v2Fixture())
	old := withLoad(v2Fixture(), "old-unknown", v2Fixture().AsOf-100*24*Hour, nil, "insufficient", false)
	r, err := CalculateV3(old)
	if err != nil || r.Score == nil || r.UnknownBurden == nil || *r.UnknownBurden > 1e-9 {
		t.Fatalf("old unknown vetoed readiness: %v %+v", err, r.Reasons)
	}
	near(t, *r.Score, *base.Score)
	threeDays := withLoad(v2Fixture(), "older-unknown", v2Fixture().AsOf-72*Hour, nil, "insufficient", false)
	r, _ = CalculateV3(threeDays)
	if r.Score == nil || *r.Burden <= *base.Burden || !r.Terms[len(r.Terms)-1].Imputed || r.Confidence >= base.Confidence {
		t.Fatalf("three-day-old unknown must be imputed at the typical load: %+v", r)
	}
	recent := withLoad(v2Fixture(), "recent-unknown", v2Fixture().AsOf-2*Hour, nil, "insufficient", false)
	r, _ = CalculateV3(recent)
	if r.Score != nil || !slices.Contains(r.Reasons, "recent_load_unknown") || r.Status == "source_conflict" {
		t.Fatalf("recent unknown must block: %+v", r)
	}
	conflict := withLoad(v2Fixture(), "recent-conflict", v2Fixture().AsOf-2*Hour, nil, "insufficient", true)
	r, _ = CalculateV3(conflict)
	if r.Status != "source_conflict" || !slices.Contains(r.Reasons, "load_parameter_conflict") {
		t.Fatalf("material conflict must stay explicit: %+v", r)
	}
	oldConflict := withLoad(v2Fixture(), "old-conflict", v2Fixture().AsOf-60*24*Hour, nil, "insufficient", true)
	r, _ = CalculateV3(oldConflict)
	if r.Score == nil || r.Status != "available" {
		t.Fatalf("old conflict vetoed readiness: %+v", r)
	}
}

// E7: a habitual daily runner with full recovery is not penalized for training
// often; a burden well above the habit still is.
func TestV3HabitualBurdenCalibration(t *testing.T) {
	daily := func(extra float64) ReadinessSnapshot {
		in := v2Fixture()
		in.Loads = nil
		in.Recovery.Score = ptr(100)
		for k := 1; k <= 42; k++ {
			d := in.Days[42-k]
			au := 60.
			if k <= 3 {
				au *= extra
			}
			in.Loads = append(in.Loads, WorkoutLoad{ID: fmt.Sprint("run", k), End: d.Start + 19*Hour, Model: LoadModelV2, AU: ptr(au), Quality: "complete", Compatible: true, Days: []DailyLoad{{d.Date, ptr(au), "complete"}}})
		}
		return in
	}
	r, err := CalculateV3(daily(1))
	if err != nil || r.Score == nil || r.HabitualBurden == nil {
		t.Fatalf("daily runner: %v %+v", err, r.Reasons)
	}
	if *r.Score < 95 {
		t.Fatalf("habitual daily training penalized: score=%v ratio=%v", *r.Score, *r.BurdenRatio)
	}
	t.Logf("daily runner score=%.1f ratio=%.2f", *r.Score, *r.BurdenRatio)
	r, _ = CalculateV3(daily(3))
	if r.Score == nil || *r.Score > 60 || *r.Score < 35 {
		t.Fatalf("three days at triple load: score=%v ratio=%v", *r.Score, *r.BurdenRatio)
	}
}
