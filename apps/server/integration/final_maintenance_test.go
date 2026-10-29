//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"rai/server/internal/syncing"
)

// The explicit baseline mode records the unmodified maintenance baseline. It
// asserts the known old behavior; normal runs always assert the fixed contract.
func TestFinalMaintenanceMixedSamples(t *testing.T) {
	baseline := os.Getenv("RAI_MAINTENANCE_BASELINE") == "1"
	for _, tc := range []struct {
		name                               string
		upserts, deletes, days, duplicates int
	}{
		{"A_all_upsert", 1000, 0, 1, 0},
		{"B_mixed", 999, 1, 1, 0},
		{"D_multiple_deletes", 996, 4, 1, 0},
		{"E_all_deletes", 0, 4, 1, 0},
		{"G_multiple_days", 996, 4, 3, 0},
		{"H_collapsed", 4, 2, 1, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			var seed, events []syncing.Event
			for i := 0; i < tc.deletes; i++ {
				seed = append(seed, sample(syncing.NewID(), 120))
			}
			seq := int64(1)
			var previous *string
			if len(seed) > 0 {
				e, _, _ := f.commit(t, seed, seq, previous)
				seq++
				previous = &e.BatchID
			}
			beforeJobs := count(t, f, "jobs")
			for i := 0; i < tc.upserts; i++ {
				e := sample(syncing.NewID(), 120)
				stamp := *e.StartUs + int64(i%tc.days)*86400_000000
				e.StartUs, e.EndUs = &stamp, &stamp
				events = append(events, e)
			}
			for _, e := range seed {
				events = append(events, deleted(e.UUID))
			}
			for i := 0; i < tc.duplicates; i++ {
				events = append(events, events[0])
			}
			// Same-page delete wins over both duplicate and conflicting upserts.
			if tc.duplicates > 0 {
				events = append(events, seed[0], sample(seed[0].UUID, 121))
			}
			start := time.Now()
			e, receipt, original := f.commit(t, events, seq, previous)
			duration := time.Since(start)
			jobs := count(t, f, "jobs") - beforeJobs
			wantJobs := tc.days
			if baseline && tc.deletes > 0 {
				wantJobs = tc.upserts + tc.deletes
			}
			if jobs != wantJobs || len(receipt.AnalysisJobIDs) != wantJobs || receipt.Counts.Inserted != tc.upserts || receipt.Counts.Deleted != tc.deletes || count(t, f, "health_samples") != tc.upserts || count(t, f, "entity_tombstones") != tc.deletes {
				t.Fatalf("counts/jobs incorrect: jobs=%d want=%d counts=%+v", jobs, wantJobs, receipt.Counts)
			}
			var scopes, generations, changes int
			var keys []string
			if err := f.s.Pool.QueryRow(ctx, `SELECT count(*),coalesce(sum(generation),0),array_agg(scope_key ORDER BY scope_key) FROM dirty_scopes`).Scan(&scopes, &generations, &keys); err != nil {
				t.Fatal(err)
			}
			if err := f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM change_events WHERE commit_rev=$1`, receipt.CommitRevision).Scan(&changes); err != nil {
				t.Fatal(err)
			}
			seedGenerations := 0
			if tc.deletes > 0 {
				seedGenerations = 1
			}
			if scopes != tc.days || generations-seedGenerations != wantJobs || changes != tc.upserts+tc.deletes {
				t.Fatal("dirty/log generation mismatch")
			}
			for _, id := range receipt.AnalysisJobIDs {
				var valid bool
				if err := f.s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM jobs WHERE id=$1 AND dataset_id=$2 AND kind='analysis.sample')`, id, f.p.DatasetID).Scan(&valid); err != nil || !valid {
					t.Fatal("receipt refers to missing job", err)
				}
			}
			retry, err := f.s.Commit(ctx, f.p, e, events)
			if err != nil || !bytes.Equal(retry, original) || count(t, f, "jobs") != beforeJobs+jobs {
				t.Fatal("retry changed receipt/jobs", err)
			}
			if len(seed) > 0 {
				_, suppressed, _ := f.commit(t, seed, seq+1, &e.BatchID)
				if suppressed.Counts.Suppressed != len(seed) || count(t, f, "health_samples") != tc.upserts || count(t, f, "jobs") != beforeJobs+jobs {
					t.Fatal("old create resurrected or created jobs")
				}
			}
			t.Logf("MAINTENANCE_SAMPLE %s", mustJSON(map[string]any{"case": tc.name, "baseline": baseline, "input_events": len(events), "facts_written": tc.upserts, "deleted": tc.deletes, "dirty_scopes": scopes, "job_count": jobs, "job_keys": keys, "generation_delta": generations - seedGenerations, "receipt_counts": receipt.Counts, "receipt_jobs": len(receipt.AnalysisJobIDs), "receipt_stable": true, "duration_ms": float64(duration.Microseconds()) / 1000}))
		})
	}
}

func TestFinalMaintenanceStreamTypeBoundary(t *testing.T) {
	for _, other := range []string{"workout", "sample"} {
		t.Run(other, func(t *testing.T) {
			f := setup(t)
			events := make([]syncing.Event, 999)
			for i := range events {
				events[i] = sample(syncing.NewID(), 120)
			}
			e := workout(t, syncing.NewID())
			if other == "sample" {
				e = sample(syncing.NewID(), 120)
				e.Type = "HKQuantityTypeIdentifierStepCount"
				e.Quantity.Unit = "count"
			}
			events = append(events, e)
			env, _ := f.envelope(t, events, 1, nil)
			if _, err := f.s.RegisterBatch(context.Background(), f.p, env); err != nil {
				t.Fatal(err)
			}
			_, err := f.s.Commit(context.Background(), f.p, env, events)
			requireCode(t, err, "unsupported_payload")
			if count(t, f, "health_samples") != 0 || count(t, f, "jobs") != 0 {
				t.Fatal("invalid mixed stream wrote facts")
			}
		})
	}
}

func TestFinalMaintenanceMultipleTypeStreams(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	for _, sampleType := range []string{kind, "HKQuantityTypeIdentifierStepCount"} {
		stream, err := f.s.RegisterStream(ctx, f.p, syncing.StreamRequest{DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch, StreamID: syncing.NewID(), SampleType: sampleType, QueryFingerprint: "sha256:" + strings.Repeat("b", 64)})
		if err != nil {
			t.Fatal(err)
		}
		f.stream = stream
		a := sample(syncing.NewID(), 120)
		a.Type = sampleType
		a.Quantity.Unit = syncing.Units[sampleType]
		e, _, _ := f.commit(t, []syncing.Event{a}, 1, nil)
		b := a
		b.UUID = syncing.NewID()
		_, r, _ := f.commit(t, []syncing.Event{b, {Op: "delete", Kind: "sample", Type: sampleType, UUID: a.UUID}}, 2, &e.BatchID)
		want := 1
		if os.Getenv("RAI_MAINTENANCE_BASELINE") == "1" {
			want = 2
		}
		if len(r.AnalysisJobIDs) != want || r.Counts.Inserted != 1 || r.Counts.Deleted != 1 {
			t.Fatal("type grouping drift")
		}
	}
	if count(t, f, "health_samples") != 2 || count(t, f, "dirty_scopes") != 2 {
		t.Fatal("type scopes merged")
	}
}

func TestFinalMaintenanceMixedIdentityConflictRollsBack(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	a := sample(syncing.NewID(), 120)
	first, _, _ := f.commit(t, []syncing.Event{a}, 1, nil)
	events := []syncing.Event{sample(syncing.NewID(), 120), sample(a.UUID, 121), deleted(syncing.NewID())}
	env, _ := f.envelope(t, events, 2, &first.BatchID)
	if _, err := f.s.RegisterBatch(ctx, f.p, env); err != nil {
		t.Fatal(err)
	}
	before := businessSnapshot(t, f)
	_, err := f.s.Commit(ctx, f.p, env, events)
	requireCode(t, err, "sample_identity_conflict")
	sameBusiness(t, f, before)
}

func TestFinalMaintenanceChunkLeaseRetry(t *testing.T) {
	for _, kind := range []string{"route", "series", "package"} {
		t.Run(kind, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			var put func(syncing.UploadLease, bool) (json.RawMessage, error)
			var seal func()
			start := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
			switch kind {
			case "route":
				workoutStream(t, f)
				w := workoutAt(start, 12)
				f.commit(t, []syncing.Event{w}, 1, nil)
				id := syncing.NewID()
				registerTestRoute(t, f, w.UUID, id, start, start.Add(time.Second), 1, 2)
				chunk := testRouteChunk(id, 0, testRoutePoints(start, 2, 0))
				put = func(l syncing.UploadLease, conflict bool) (json.RawMessage, error) {
					c := chunk
					if conflict {
						c.WireSHA256 = strings.Repeat("a", 64)
					}
					return f.s.CommitRouteChunk(ctx, f.p, c, l)
				}
				seal = func() { m, p := routeStateChains(t, f, id); sealTestRoute(t, f, id, 1, 2, m, p) }
			case "series":
				parent, id := syncing.NewID(), syncing.NewID()
				f.commit(t, []syncing.Event{seriesParent(parent, start.UnixMicro(), start.Add(time.Minute).UnixMicro(), 120)}, 1, nil)
				if _, err := f.s.RegisterSeries(ctx, f.p, seriesRequest(f, id, parent)); err != nil {
					t.Fatal(err)
				}
				chunk, _ := parseSeries(t, f, id, 0, seriesPoints(start.UnixMicro(), 120, 121))
				put = func(l syncing.UploadLease, conflict bool) (json.RawMessage, error) {
					c := chunk
					if conflict {
						c.WireSHA256 = strings.Repeat("a", 64)
					}
					return f.s.CommitSeriesChunk(ctx, f.p, c, l)
				}
				seal = func() {
					r, err := f.s.Series(ctx, f.p, id)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = f.s.SealSeries(ctx, f.p, id, syncing.SeriesSeal{ChunkCount: 1, TotalPoints: 2, ManifestSHA256: r.ManifestSHA256}); err != nil {
						t.Fatal(err)
					}
				}
			case "package":
				id := syncing.NewID()
				if _, err := f.s.RegisterWorkoutPackage(ctx, f.p, workoutPackageRequest(f, id, start.UnixMicro(), start.Add(time.Minute).UnixMicro())); err != nil {
					t.Fatal(err)
				}
				wire := workoutPackageWire(t, workoutPackageActivityLine(t, 0, start.UnixMicro(), start.Add(time.Minute).UnixMicro()))
				chunk, err := syncing.ParseWorkoutDetailChunk(bytes.NewReader(wire), start.UnixMicro(), start.Add(time.Minute).UnixMicro())
				if err != nil {
					t.Fatal(err)
				}
				chunk.WorkoutID = id
				put = func(l syncing.UploadLease, conflict bool) (json.RawMessage, error) {
					c := chunk
					if conflict {
						c.WireSHA256 = strings.Repeat("a", 64)
					}
					return f.s.CommitWorkoutPackageChunk(ctx, f.p, c, l)
				}
				seal = func() {
					r, err := f.s.WorkoutPackage(ctx, f.p, id)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = f.s.SealWorkoutPackage(ctx, f.p, id, syncing.WorkoutPackageSeal{ChunkCount: 1, TotalDetails: r.TotalDetails, ManifestSHA256: r.ManifestSHA256}); err != nil {
						t.Fatal(err)
					}
				}
			}
			lease := takeUpload(t, f.s, f.p)
			original, err := put(lease, false)
			if err != nil {
				t.Fatal(err)
			}
			for _, state := range []string{"pending", "sealed"} {
				if state == "sealed" {
					seal()
				}
				beforeJobs := count(t, f, "jobs")
				retry, err := put(lease, false)
				if err != nil || !bytes.Equal(retry, original) {
					t.Fatal("valid lease lost original receipt", err)
				}
				_, err = put(lease, true)
				conflictCode := map[string]string{"route": "route_chunk_conflict", "series": "series_chunk_conflict", "package": "workout_package_chunk_conflict"}[kind]
				if kind == "route" && state == "sealed" {
					conflictCode = "route_already_sealed"
				}
				requireCode(t, err, conflictCode)
				for _, invalid := range []string{"missing", "stale", "expired", "replaced"} {
					bad := lease
					switch invalid {
					case "missing":
						bad = syncing.UploadLease{}
					case "stale":
						bad.Token = syncing.NewID()
					case "expired":
						if _, err = f.s.Pool.Exec(ctx, `UPDATE sync_upload_slots SET expires_at=clock_timestamp()-interval '1 second' WHERE token=$1`, lease.Token); err != nil {
							t.Fatal(err)
						}
					case "replaced":
						if err = f.s.ReleaseUpload(ctx, f.p, lease); err != nil {
							t.Fatal(err)
						}
						lease = takeUpload(t, f.s, f.p)
					}
					body, err := put(bad, false)
					// The old route path checks at the end for pending routes only.
					bypass := os.Getenv("RAI_MAINTENANCE_BASELINE") == "1" && (kind != "route" || state == "sealed")
					if bypass {
						if err != nil || !bytes.Equal(body, original) {
							t.Fatal("baseline bypass not reproduced", err)
						}
					} else if os.Getenv("RAI_MAINTENANCE_BASELINE") == "1" && kind == "route" && state == "pending" && invalid == "missing" {
						if err == nil || !strings.Contains(err.Error(), "22P02") {
							t.Fatal("baseline missing-token behavior changed")
						}
					} else {
						requireCode(t, err, "upload_lease_lost")
						if len(body) != 0 {
							t.Fatal("receipt disclosed")
						}
					}
					t.Logf("MAINTENANCE_LEASE kind=%s state=%s lease=%s receipt_disclosed=%t", kind, state, invalid, len(body) > 0)
				}
				retry, err = put(lease, false)
				if err != nil || !bytes.Equal(retry, original) {
					t.Fatal("replacement lease recovery failed", err)
				}
				table := map[string]string{"route": "route_chunks", "series": "series_chunks", "package": "workout_package_chunks"}[kind]
				if count(t, f, table) != 1 || count(t, f, "jobs") != beforeJobs {
					t.Fatal("retry multiplied chunks or jobs")
				}
			}
		})
	}
}
