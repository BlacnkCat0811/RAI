//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"rai/server/internal/admin"
	"rai/server/internal/analysis"
	"rai/server/internal/syncing"
	ve "rai/server/internal/vo2maxexternal"
	"testing"
	"time"
)

func externalMeaning(r ve.Result) string {
	return ve.Hash(struct {
		Status        string
		Value         *float64
		Origin, Epoch *string
		Confidence    *float64
		Reasons       []string
	}{r.Status, r.Value, r.Origin, r.SourceEpoch, r.Confidence, r.Reasons})
}
func TestVO2ExternalIncrementalEqualsBoundedRebuild(t *testing.T) {
	f, b, sel := externalFixture(t)
	ctx := context.Background()
	rng := rand.New(rand.NewSource(250031))
	live := []string{b.Facts[0].UUID}
	base := b.Facts[0]
	last := b
	for i := 0; i < 48; i++ {
		next := nextVO2(last)
		switch rng.Intn(6) {
		case 0, 1, 2:
			x := base
			x.UUID = syncing.NewID()
			x.EndUs -= int64(rng.Intn(60)) * ve.DayUs
			x.StartUs = x.EndUs
			x.Quantity.Value = float64(40 + rng.Intn(8))
			if rng.Intn(2) == 1 {
				version := "synthetic epoch B"
				x.Provenance.SourceRevision.Version = &version
			}
			next.Facts = []syncing.VO2Fact{x}
			live = append(live, x.UUID)
		case 3:
			if len(live) > 0 {
				j := rng.Intn(len(live))
				next.DeletedUUIDs = []string{live[j]}
				live = append(live[:j], live[j+1:]...)
			}
		case 4:
			requireVO2(t, f, last)
		case 5:
			old := sel.ID
			sel.ID = syncing.NewID()
			sel.Supersedes = old
			sel.Revoked = rng.Intn(4) == 0
			if rng.Intn(2) == 0 {
				p := base.Provenance
				version := "synthetic epoch B"
				p.SourceRevision.Version = &version
				sel.Epoch = p.EpochID()
			} else {
				sel.Epoch = base.Provenance.EpochID()
			}
			if e := admin.SetVO2SourceSelection(ctx, f.s, f.p.DatasetID, syncing.NewID(), "synthetic randomized", sel); e != nil {
				t.Fatal(e)
			}
		}
		requireVO2(t, f, next)
		last = next
		runOne(t, f, ve.JobKind)
		incremental, _, e := f.s.CandidateVO2External(ctx, f.p.DatasetID, time.Now().UTC())
		if e != nil {
			t.Fatal(e)
		}
		if i%6 == 0 {
			if e = admin.QueueVO2ExternalCandidate(ctx, f.s, f.p.DatasetID, syncing.NewID(), "synthetic bounded rebuild"); e != nil {
				t.Fatal(e)
			}
			runOne(t, f, ve.JobKind)
			rebuilt, _, e := f.s.CandidateVO2External(ctx, f.p.DatasetID, time.Now().UTC())
			if e != nil || externalMeaning(incremental) != externalMeaning(rebuilt) {
				t.Fatal("incremental != bounded rebuild", e)
			}
		}
	}
}
func TestVO2ExternalDatabaseBoundsAndPerformance(t *testing.T) {
	for _, n := range []int{0, 1, 128, 1024, 1025} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			f, b, _ := externalFixture(t)
			ctx := context.Background()
			base := b.Facts[0]
			last := b
			if n == 0 {
				d := nextVO2(last)
				d.DeletedUUIDs = []string{base.UUID}
				requireVO2(t, f, d)
				last = d
			} else {
				for count := 1; count < n; {
					d := nextVO2(last)
					for len(d.Facts) < 128 && count < n {
						f := base
						f.UUID = syncing.NewID()
						f.StartUs += int64(count)
						f.EndUs = f.StartUs
						d.Facts = append(d.Facts, f)
						count++
					}
					requireVO2(t, f, d)
					last = d
				}
			}
			start := time.Now()
			l := claimAnalysis(t, f, ve.JobKind)
			in, e := f.s.LoadAnalysis(ctx, l, time.Now().UTC())
			snapshot := time.Since(start)
			if n > 1024 {
				code, _ := analysis.ErrorCode(e)
				if e == nil || code != "analysis_input_limit" {
					t.Fatal("limit+1 did not fail closed", e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			id, e := f.s.StartAnalysisRun(ctx, l, in)
			if e != nil {
				t.Fatal(e)
			}
			start = time.Now()
			ms, e := analysis.CalculateCandidateVO2External(&in)
			selection := time.Since(start)
			if e != nil {
				t.Fatal(e)
			}
			raw, _ := json.Marshal(in.ExternalArtifact)
			if len(raw) > ve.MaxArtifactBytes {
				t.Fatal("artifact size")
			}
			start = time.Now()
			status, e := f.s.PublishAnalysis(ctx, l, id, in, ms)
			publication := time.Since(start)
			if e != nil || status != "SUCCEEDED" {
				t.Fatal(status, e)
			}
			t.Logf("synthetic refs=%d snapshot_us=%d selection_serialize_us=%d publish_us=%d artifact_bytes=%d", n, snapshot.Microseconds(), selection.Microseconds(), publication.Microseconds(), len(raw))
		})
	}
}

func TestVO2ExternalEpochTieAndEmptyPerformance(t *testing.T) {
	for _, tt := range []struct {
		name          string
		n             int
		epochs, limit bool
	}{{"empty", 0, false, false}, {"epochs32", 32, true, false}, {"epochs33", 33, true, true}, {"ties256", 256, false, false}, {"ties257", 257, false, true}} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			f := setup(t)
			b := externalBatch(t, f)
			base := b.Facts[0]
			epoch := base.Provenance.EpochID()
			b.Facts = []syncing.VO2Fact{}
			factCount := 0
			last := b
			for factCount < tt.n || factCount == 0 {
				d := last
				if factCount > 0 {
					d = nextVO2(last)
				}
				for len(d.Facts) < 128 && factCount < tt.n {
					x := base
					x.UUID = syncing.NewID()
					if tt.epochs && factCount > 0 {
						version := fmt.Sprintf("epoch-%d", factCount)
						x.Provenance.SourceRevision.Version = &version
					}
					d.Facts = append(d.Facts, x)
					factCount++
				}
				requireVO2(t, f, d)
				last = d
				if tt.n == 0 {
					break
				}
			}
			if e := admin.SetVO2SourceSelection(ctx, f.s, f.p.DatasetID, syncing.NewID(), "synthetic matrix", ve.Selection{ID: syncing.NewID(), Epoch: epoch, EffectiveFrom: b.FlowCreatedAtUs}); e != nil {
				t.Fatal(e)
			}
			if e := admin.QueueVO2ExternalCandidate(ctx, f.s, f.p.DatasetID, syncing.NewID(), "synthetic matrix"); e != nil {
				t.Fatal(e)
			}
			l := claimAnalysis(t, f, ve.JobKind)
			start := time.Now()
			in, e := f.s.LoadAnalysis(ctx, l, time.Now())
			snapshot := time.Since(start)
			if e != nil {
				t.Fatal(e)
			}
			start = time.Now()
			ms, e := analysis.CalculateCandidateVO2External(&in)
			selection := time.Since(start)
			if tt.limit {
				code, _ := analysis.ErrorCode(e)
				if e == nil || code != "analysis_input_limit" {
					t.Fatal("epoch/tie +1 did not fail closed", e)
				}
				if count(t, f, "analysis_artifacts") != 0 {
					t.Fatal("limit published")
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			id, e := f.s.StartAnalysisRun(ctx, l, in)
			if e != nil {
				t.Fatal(e)
			}
			raw, _ := json.Marshal(in.ExternalArtifact)
			start = time.Now()
			status, e := f.s.PublishAnalysis(ctx, l, id, in, ms)
			publication := time.Since(start)
			if e != nil || status != "SUCCEEDED" {
				t.Fatal(status, e)
			}
			t.Logf("synthetic case=%s refs=%d snapshot_us=%d selection_serialize_us=%d publish_us=%d artifact_bytes=%d", tt.name, len(in.ExternalInput.Facts)+len(in.ExternalInput.Tombstones), snapshot.Microseconds(), selection.Microseconds(), publication.Microseconds(), len(raw))
		})
	}
}
