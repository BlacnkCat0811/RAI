package vo2maxexternal

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"testing"
)

func testInput() ExternalVO2Snapshot {
	at := int64(1800000000000000)
	lower := at - 90*DayUs
	queried := at - 1
	return ExternalVO2Snapshot{Dataset: "synthetic", DatasetEpoch: "epoch", Collector: "collector", Generation: 1, AsOf: at, CapturedAt: at, KnownThroughRevision: 100, Config: DefaultConfig(), Assignment: "assignment", View: "revised", Facts: []Fact{}, Tombstones: []Tombstone{}, Selections: []Selection{{ID: "selection", Hash: "control", Epoch: "A", EffectiveFrom: at - 1, RecordedAt: at - 1}}, Coverage: Coverage{Stream: "stream", Sequence: 1, Complete: true, BootstrapStart: &lower, QueryCompletedAt: &queried}}
}
func testFact(in ExternalVO2Snapshot) Fact {
	testType := int64(2)
	return Fact{ID: "sample", Hash: "raw", Epoch: "A", Start: in.AsOf - DayUs, End: in.AsOf - DayUs, KnownRevision: 1, Value: 42.123456789, Origin: Apple, Supported: true, SupportHash: "context", SemanticHash: "semantic", TestType: &testType}
}
func TestAutoSelectionUsesMostRecentAppleSource(t *testing.T) {
	in := testInput()
	in.Selections = nil
	old := testFact(in)
	old.ID, old.Epoch, old.End, old.Start = "old-watch", "A", in.AsOf-5*DayUs, in.AsOf-5*DayUs
	current := testFact(in)
	current.ID, current.Epoch, current.Value = "current-watch", "B", 47
	in.Facts = []Fact{old, current}
	r, err := Calculate(in)
	if err != nil || r.Status != "available" || r.SourceEpoch == nil || *r.SourceEpoch != "B" || r.SelectionID != nil || *r.Value != 47 {
		t.Fatalf("auto selection %+v %v", r, err)
	}
	found := false
	for _, reason := range r.Reasons {
		found = found || reason == "source_auto_selected"
	}
	if !found {
		t.Fatal("auto selection is not stated")
	}
	// An explicit choice always wins over the most recent source.
	in.Selections = []Selection{{ID: "selection", Hash: "control", Epoch: "A", EffectiveFrom: in.AsOf - 1, RecordedAt: in.AsOf - 1}}
	if r, err = Calculate(in); err != nil || r.SourceEpoch == nil || *r.SourceEpoch != "A" {
		t.Fatalf("explicit selection lost %+v %v", r, err)
	}
}
func TestGoldenDecisionTable(t *testing.T) {
	tests := []struct {
		name, status string
		value        bool
		change       func(*ExternalVO2Snapshot)
	}{
		{"no sample", "no_sample_observed", false, func(in *ExternalVO2Snapshot) { in.Facts = nil }},
		{"incomplete", "sync_incomplete", false, func(in *ExternalVO2Snapshot) { in.Coverage.Complete = false }},
		{"pending observation", "sync_incomplete", false, func(in *ExternalVO2Snapshot) { in.Coverage = Coverage{} }},
		{"fresh", "available", true, func(in *ExternalVO2Snapshot) {}},
		{"stale", "stale", true, func(in *ExternalVO2Snapshot) { in.Facts[0].End = in.AsOf - 31*DayUs }},
		{"expired", "expired", false, func(in *ExternalVO2Snapshot) { in.Facts[0].End = in.AsOf - 90*DayUs }},
		{"future", "invalid_input", false, func(in *ExternalVO2Snapshot) { in.Facts[0].End = in.AsOf + 1 }},
		{"future known", "no_sample_observed", false, func(in *ExternalVO2Snapshot) { in.Facts[0].KnownRevision = 101 }},
		{"missing selection auto-selects the only Apple source", "available", true, func(in *ExternalVO2Snapshot) { in.Selections = nil }},
		{"auto selection tie between sources", "source_selection_required", false, func(in *ExternalVO2Snapshot) {
			in.Selections = nil
			f := in.Facts[0]
			f.ID, f.Epoch = "other-watch", "B"
			in.Facts = append(in.Facts, f)
		}},
		{"third party", "source_selection_required", false, func(in *ExternalVO2Snapshot) { in.Facts[0].Origin = "third_party_estimate" }},
		{"manual", "source_selection_required", false, func(in *ExternalVO2Snapshot) { in.Facts[0].Origin = "user_entered" }},
		{"unclassified maxExercise", "source_selection_required", false, func(in *ExternalVO2Snapshot) {
			in.Facts[0].Origin = "unclassified"
			v := int64(1)
			in.Facts[0].TestType = &v
		}},
		{"invalid latest", "invalid_input", false, func(in *ExternalVO2Snapshot) { in.Facts[0].Value = 0 }},
		{"same time same value", "available", true, func(in *ExternalVO2Snapshot) { f := in.Facts[0]; f.ID = "duplicate"; in.Facts = append(in.Facts, f) }},
		{"same time different value", "conflicting_data", false, func(in *ExternalVO2Snapshot) {
			f := in.Facts[0]
			f.ID = "conflict"
			f.Value++
			in.Facts = append(in.Facts, f)
		}},
		{"same time conflicting support", "conflicting_data", false, func(in *ExternalVO2Snapshot) {
			f := in.Facts[0]
			f.ID = "conflict"
			f.SupportHash = "different"
			in.Facts = append(in.Facts, f)
		}},
		{"sync conflict", "conflicting_data", false, func(in *ExternalVO2Snapshot) {
			in.Facts[0].SyncKey = "sync"
			f := in.Facts[0]
			f.ID = "conflict"
			f.End--
			f.SemanticHash = "different"
			f.Epoch = "B"
			in.Facts = append(in.Facts, f)
		}},
		{"deleted all", "deleted", false, func(in *ExternalVO2Snapshot) {
			in.Facts = nil
			in.Tombstones = []Tombstone{{ID: "deleted", Epoch: "A", Origin: Apple, Revision: 1}}
		}},
		{"unknown tombstone", "no_sample_observed", false, func(in *ExternalVO2Snapshot) {
			in.Facts = nil
			in.Tombstones = []Tombstone{{ID: "unknown", Revision: 1}}
		}},
		{"manual tombstone", "no_sample_observed", false, func(in *ExternalVO2Snapshot) {
			in.Facts = nil
			in.Tombstones = []Tombstone{{ID: "manual", Epoch: "A", Origin: "user_entered", Revision: 1}}
		}},
		{"deleted latest fallback", "available", true, func(in *ExternalVO2Snapshot) {
			in.Tombstones = []Tombstone{{ID: "deleted", Epoch: "A", Origin: Apple, Revision: 1}}
		}},
		{"deleted no expired fallback", "deleted", false, func(in *ExternalVO2Snapshot) {
			in.Facts[0].End = in.AsOf - 91*DayUs
			in.Tombstones = []Tombstone{{ID: "deleted", Epoch: "A", Origin: Apple, Revision: 1}}
		}},
		{"revoke selection", "source_selection_required", false, func(in *ExternalVO2Snapshot) {
			in.Selections = append(in.Selections, Selection{ID: "revoked", Hash: "revoked", Epoch: "A", EffectiveFrom: in.AsOf - 1, RecordedAt: in.AsOf - 1, Supersedes: "selection", Revoked: true})
		}},
		{"overlap", "source_selection_required", false, func(in *ExternalVO2Snapshot) {
			s := in.Selections[0]
			s.ID = "overlap"
			in.Selections = append(in.Selections, s)
		}},
		{"future control falls back to auto selection", "available", true, func(in *ExternalVO2Snapshot) { in.Selections[0].RecordedAt = in.AsOf + 1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := testInput()
			in.Facts = []Fact{testFact(in)}
			tt.change(&in)
			for i := range in.Tombstones {
				if in.Tombstones[i].Epoch == "A" && in.Tombstones[i].Origin == Apple && in.Tombstones[i].End == nil {
					v := in.AsOf - DayUs
					in.Tombstones[i].End = &v
				}
			}
			r, err := Calculate(in)
			if err != nil || r.Status != tt.status || (r.Value != nil) != tt.value {
				t.Fatalf("decision %s value=%v err=%v", r.Status, r.Value != nil, err)
			}
			if r.Confidence != nil || r.ParameterVersion != nil || r.FeatureVersion != nil {
				t.Fatal("fake confidence/parameter lineage")
			}
			if r.Value != nil && *r.Value != in.Facts[0].Value {
				t.Fatal("external value adjusted")
			}
		})
	}
}
func TestFreshnessMicrosecondBoundaries(t *testing.T) {
	for _, tt := range []struct {
		age    int64
		status string
		value  bool
	}{{30*DayUs - 1, "available", true}, {30 * DayUs, "stale", true}, {90*DayUs - 1, "stale", true}, {90 * DayUs, "expired", false}} {
		in := testInput()
		f := testFact(in)
		f.End = in.AsOf - tt.age
		in.Facts = []Fact{f}
		r, e := Calculate(in)
		if e != nil || r.Status != tt.status || (r.Value != nil) != tt.value {
			t.Fatal("boundary", tt.status, r.Status, e)
		}
	}
	in := testInput()
	f := testFact(in)
	f.End = in.AsOf - 29*DayUs
	in.Facts = []Fact{f}
	r, _ := Calculate(in)
	original := Hash(r)
	stale, _ := Freshen(r, in.AsOf+2*DayUs)
	expired, _ := Freshen(r, in.AsOf+61*DayUs)
	if stale.Status != "stale" || stale.Value == nil || expired.Status != "expired" || expired.Value != nil || Hash(r) != original {
		t.Fatal("reader freshness")
	}
	if _, e := Freshen(r, in.AsOf-1); e == nil {
		t.Fatal("backwards reader")
	}
}
func TestBoundsAndCanonicalProperties(t *testing.T) {
	for _, n := range []int{0, 1, 128, 1024, 1025} {
		in := testInput()
		for i := 0; i < n; i++ {
			f := testFact(in)
			f.ID = fmt.Sprintf("%04d", i)
			f.End -= int64(i)
			in.Facts = append(in.Facts, f)
		}
		_, e := Calculate(in)
		if (e == ErrLimit) != (n > 1024) {
			t.Fatal(n, e)
		}
	}
	for _, n := range []int{32, 33} {
		in := testInput()
		for i := 0; i < n; i++ {
			f := testFact(in)
			f.ID = fmt.Sprint(i)
			f.Epoch = fmt.Sprint(i)
			in.Facts = append(in.Facts, f)
		}
		_, e := Calculate(in)
		if (e == ErrLimit) != (n > 32) {
			t.Fatal("epochs", n, e)
		}
	}
	for _, n := range []int{256, 257} {
		in := testInput()
		for i := 0; i < n; i++ {
			f := testFact(in)
			f.ID = fmt.Sprint(i)
			in.Facts = append(in.Facts, f)
		}
		_, e := Calculate(in)
		if (e == ErrLimit) != (n > 256) {
			t.Fatal("ties", n, e)
		}
	}
	in := testInput()
	for i := 0; i < 128; i++ {
		f := testFact(in)
		f.ID = fmt.Sprint(i)
		f.End -= int64(i)
		in.Facts = append(in.Facts, f)
	}
	want, _ := Calculate(in)
	rng := rand.New(rand.NewSource(34811))
	for i := 0; i < 100; i++ {
		rng.Shuffle(len(in.Facts), func(a, b int) { in.Facts[a], in.Facts[b] = in.Facts[b], in.Facts[a] })
		got, e := Calculate(in)
		if e != nil || Hash(got) != Hash(want) {
			t.Fatal("permutation changed output", e)
		}
	}
	in.View = "as_known"
	if _, e := Calculate(in); e != ErrHistorical {
		t.Fatal("invented historical snapshot")
	}
	in.HistoricalCapable = true
	if _, e := Calculate(in); e != nil {
		t.Fatal(e)
	}
}
func BenchmarkProjectionMatrix(b *testing.B) {
	for _, n := range []int{0, 1, 128, 1024} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			in := testInput()
			for i := 0; i < n; i++ {
				f := testFact(in)
				f.ID = fmt.Sprintf("%036d", i)
				f.Hash = fmt.Sprintf("%064d", i)
				f.End -= int64(i)
				in.Facts = append(in.Facts, f)
			}
			r, e := Calculate(in)
			if e != nil {
				b.Fatal(e)
			}
			raw, _ := json.Marshal(r)
			b.ReportMetric(float64(len(raw)), "artifact_bytes")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, e = Calculate(in); e != nil {
					b.Fatal(e)
				}
			}
		})
	}
}

func BenchmarkProjectionStages(b *testing.B) {
	in := testInput()
	for i := 0; i < 1024; i++ {
		f := testFact(in)
		f.ID = fmt.Sprintf("%036d", i)
		f.Hash = fmt.Sprintf("%064d", i)
		f.End -= int64(i)
		in.Facts = append(in.Facts, f)
	}
	r, _ := Calculate(in)
	for _, stage := range []string{"canonicalize", "selection", "freshness", "serialize"} {
		b.Run(stage, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				switch stage {
				case "canonicalize":
					Canonical(in)
				case "selection":
					Calculate(in)
				case "freshness":
					Freshen(r, in.AsOf+31*DayUs)
				case "serialize":
					json.Marshal(r)
				}
			}
		})
	}
	for _, n := range []int{32, 256} {
		b.Run(fmt.Sprintf("epochs_or_ties_%d", n), func(b *testing.B) {
			in := testInput()
			for i := 0; i < n; i++ {
				f := testFact(in)
				f.ID = fmt.Sprint(i)
				if n == 32 {
					f.Epoch = fmt.Sprint(i)
				}
				in.Facts = append(in.Facts, f)
			}
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, e := Calculate(in); e != nil {
					b.Fatal(e)
				}
			}
		})
	}
}

func TestFutureWithdrawalCannotBackfillPast(t *testing.T) {
	in := testInput()
	future := in.AsOf + DayUs
	in.Tombstones = []Tombstone{{ID: "future", Hash: "withdrawal", Epoch: "A", Origin: Apple, Revision: 1, End: &future}}
	r, e := Calculate(in)
	if e != nil || r.Status != "no_sample_observed" {
		t.Fatal("future event withdrawal backfilled past", r.Status, e)
	}
}
