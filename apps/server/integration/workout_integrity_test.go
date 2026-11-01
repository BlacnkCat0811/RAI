//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"rai/server/internal/syncing"
	"reflect"
	"testing"
	"time"
)

func phase3Observation(b *syncing.WorkoutBundle) {
	source := syncing.HealthKitSource{BundleID: "com.apple.health", Name: "Synthetic Watch source", Version: "1", ProductType: "Watch7,1", Device: &syncing.HealthKitDevice{Manufacturer: "Apple Inc.", Model: "Watch", HardwareVersion: "Watch7,1", SoftwareVersion: "11"}}
	b.Workout.Source.BundleID = source.BundleID
	p := syncing.BundleProvenance{Workout: source, Samples: []syncing.SampleProvenance{}, Routes: []syncing.RouteProvenance{}}
	for i := range b.Samples {
		b.Samples[i].Source.BundleID = source.BundleID
		p.Samples = append(p.Samples, syncing.SampleProvenance{SampleType: b.Samples[i].Type, SampleUUID: b.Samples[i].UUID, Source: source})
	}
	for i := range b.Routes {
		b.Routes[i].Source.BundleID = source.BundleID
		p.Routes = append(p.Routes, syncing.RouteProvenance{RouteID: b.Routes[i].RouteID, Source: source})
	}
	b.Observation = &syncing.BundleObservation{Version: 1, ObservedAtUs: 1, Provenance: p, Metadata: map[string]any{"HKIndoorWorkout": false}, Coverage: map[string]syncing.BundleCoverage{"power": {State: "no_records_observed", QueryCompleted: true, LocalPersisted: true}}}
}
func importPhase3(t *testing.T, f *fixture, b syncing.WorkoutBundle) (syncing.WorkoutImportReceipt, []byte) {
	t.Helper()
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	body, err := f.s.ImportWorkoutBundle(context.Background(), f.p, b, raw)
	if err != nil {
		t.Fatal(err)
	}
	var receipt syncing.WorkoutImportReceipt
	if err = json.Unmarshal(body, &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt, body
}
func TestPhase3RouteClockRawOrderCountsChunksHashesAndReplay(t *testing.T) {
	f := setup(t)
	b, _ := testWorkoutBundle(t, f, 0)
	route := &b.Routes[0]
	route.Points = nil
	start, end := *b.Workout.StartUs, *b.Workout.EndUs
	for i := 0; i < 10005; i++ {
		stamp := start + int64(i)*1000
		if i == 5000 {
			stamp = start - 1
		}
		if i == 5001 {
			stamp = start - 2
		}
		if i == 5002 {
			stamp = start - 2
		}
		if i == 10004 {
			stamp = end + 1
		}
		route.Points = append(route.Points, syncing.WorkoutBundlePoint{RecordedAtUs: stamp, Latitude: 0, Longitude: 0})
	}
	original, _ := json.Marshal(b.Routes[0].Points)
	receipt, first := importPhase3(t, f, b)
	_, again := importPhase3(t, f, b)
	if !bytes.Equal(first, again) {
		t.Fatal("retry receipt changed")
	}
	if len(receipt.RouteDiagnostics[route.RouteID]) != 4 {
		t.Fatal("missing clock quality diagnostics")
	}
	ctx := context.Background()
	rows, err := f.s.Pool.Query(ctx, `SELECT sequence,(extract(epoch from recorded_at)*1000000)::bigint FROM route_points WHERE dataset_id=$1 AND route_uuid=$2 ORDER BY sequence`, f.p.DatasetID, route.RouteID)
	if err != nil {
		t.Fatal(err)
	}
	index := 0
	for rows.Next() {
		var seq, stamp int64
		if err = rows.Scan(&seq, &stamp); err != nil {
			t.Fatal(err)
		}
		if seq != int64(index) || stamp != route.Points[index].RecordedAtUs {
			t.Fatal("raw point timestamp/order changed", index)
		}
		index++
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if index != 10005 {
		t.Fatal("point loss", index)
	}
	rows, err = f.s.Pool.Query(ctx, `SELECT chunk_index,point_count,content_sha256,points,manifest_sha256 FROM route_chunks WHERE dataset_id=$1 AND route_uuid=$2 ORDER BY chunk_index`, f.p.DatasetID, route.RouteID)
	if err != nil {
		t.Fatal(err)
	}
	manifest := syncing.EmptyRouteManifest()
	ordinal := 0
	var reconstructed []syncing.WorkoutBundlePoint
	for rows.Next() {
		var index, n int
		var hash, raw, chain []byte
		if err = rows.Scan(&index, &n, &hash, &raw, &chain); err != nil {
			t.Fatal(err)
		}
		var points []syncing.RoutePoint
		if err = json.Unmarshal(raw, &points); err != nil {
			t.Fatal(err)
		}
		_, want := syncing.RoutePointsCanonical(points)
		manifest = syncing.AdvanceRouteManifest(manifest, want)
		if index != ordinal || n != len(points) || !bytes.Equal(hash, want) || !bytes.Equal(chain, manifest) {
			t.Fatal("chunk/hash mismatch")
		}
		for _, p := range points {
			reconstructed = append(reconstructed, syncing.WorkoutBundlePoint{RecordedAtUs: p.RecordedAtUs, Latitude: p.Latitude, Longitude: p.Longitude})
		}
		ordinal++
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(reconstructed)
	if !bytes.Equal(original, encoded) || ordinal != 3 {
		t.Fatal("canonical source changed")
	}
	var sealed string
	var total int
	var stored []byte
	if err = f.s.Pool.QueryRow(ctx, `SELECT state,total_points,manifest_sha256 FROM workout_routes WHERE dataset_id=$1 AND route_uuid=$2`, f.p.DatasetID, route.RouteID).Scan(&sealed, &total, &stored); err != nil {
		t.Fatal(err)
	}
	if sealed != "sealed" || total != 10005 || hex.EncodeToString(stored) != hex.EncodeToString(manifest) {
		t.Fatal("seal mismatch")
	}
}
func TestPhase3MetadataProvenanceCoverageAppendHistoryAndImmutableCore(t *testing.T) {
	f := setup(t)
	b, _ := testWorkoutBundle(t, f, 1)
	phase3Observation(&b)
	firstReceipt, _ := importPhase3(t, f, b)
	if firstReceipt.SourceClassification != "apple_watch" || firstReceipt.SourceObservation == nil {
		t.Fatalf("source evidence not returned: %+v", firstReceipt)
	}
	ctx := context.Background()
	var before []byte
	if err := f.s.Pool.QueryRow(ctx, `SELECT payload FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2`, f.p.DatasetID, b.WorkoutID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	b.Observation.ObservedAtUs = 2
	b.Observation.Provenance.Workout.Version = "2"
	b.Observation.Provenance.Workout.Device.Model = "Watch changed descriptor"
	b.Observation.Metadata["HKTimeZone"] = "Asia/Shanghai"
	b.Observation.Coverage["power"] = syncing.BundleCoverage{State: "failed", ErrorCode: "synthetic_query_failed"}
	receipt, _ := importPhase3(t, f, b)
	if receipt.AnalysisStatus != "queued" {
		t.Fatal("observation did not invalidate analysis")
	}
	var after []byte
	if err := f.s.Pool.QueryRow(ctx, `SELECT payload FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2`, f.p.DatasetID, b.WorkoutID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("raw identity overwritten")
	}
	if count(t, f, "workout_imports") != 2 || count(t, f, "health_samples") != 1 || count(t, f, "route_points") != 3 {
		t.Fatal("observation lost facts/history")
	}
	// Same observation content with a new observation time is a new historical
	// receipt, but no new dirty generation or analysis job.
	b.Observation.ObservedAtUs = 3
	receipt, _ = importPhase3(t, f, b)
	if receipt.AnalysisStatus != "not_required" {
		t.Fatal("repeated observation created work")
	}
	b.Observation.ObservedAtUs = 0
	b.Observation.Metadata["HKTimeZone"] = "UTC"
	receipt, _ = importPhase3(t, f, b)
	if receipt.AnalysisStatus != "not_required" {
		t.Fatal("late old observation replaced current")
	}
	b.Workout.Metadata = map[string]any{"HKIndoorWorkout": true}
	b.Observation.ObservedAtUs = 4
	importPhase3(t, f, b)
	if err := f.s.Pool.QueryRow(ctx, `SELECT payload FROM workouts WHERE dataset_id=$1 AND workout_uuid=$2`, f.p.DatasetID, b.WorkoutID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("metadata replaced raw core")
	}
}
func TestPhase3ExplicitChildDeletesAreAtomicIdempotentAndScoped(t *testing.T) {
	f := setup(t)
	b, _ := testWorkoutBundle(t, f, 1)
	importPhase3(t, f, b)
	other, _ := testWorkoutBundle(t, f, 1)
	importPhase3(t, f, other)
	deletedSample, deletedRoute := b.Samples[0], b.Routes[0].RouteID
	deletion := b
	deletion.Samples = []syncing.Event{}
	deletion.Series = nil
	deletion.Routes = nil
	deletion.SampleDeletions = []syncing.SampleDeletion{{SampleType: deletedSample.Type, SampleUUID: deletedSample.UUID}, {SampleType: kind, SampleUUID: syncing.NewID()}}
	deletion.RouteDeletions = []string{deletedRoute}
	receipt, first := importPhase3(t, f, deletion)
	_, replay := importPhase3(t, f, deletion)
	if !bytes.Equal(first, replay) || receipt.Counts.SamplesDeleted != 1 || receipt.Counts.AssociationsDeleted != 1 || receipt.Counts.RoutesDeleted != 1 {
		t.Fatal("bad deletion receipt")
	}
	if count(t, f, "workouts") != 2 || count(t, f, "health_samples") != 1 || count(t, f, "workout_routes") != 1 || count(t, f, "entity_tombstones") != 3 {
		t.Fatal("wrong deletion scope")
	}
	raw, _ := json.Marshal(b)
	if _, err := f.s.ImportWorkoutBundle(context.Background(), f.p, b, raw); err == nil {
		t.Fatal("stale bundle resurrected child")
	}
	bad := deletion
	bad.SampleDeletions = []syncing.SampleDeletion{{SampleType: kind, SampleUUID: other.Samples[0].UUID}}
	bad.RouteDeletions = nil
	snapshot := phase1Snapshot(t, f)
	raw, _ = json.Marshal(bad)
	if _, err := f.s.ImportWorkoutBundle(context.Background(), f.p, bad, raw); err == nil {
		t.Fatal("other workout child deleted")
	}
	if !reflect.DeepEqual(snapshot, phase1Snapshot(t, f)) {
		t.Fatal("failed scoped delete mutated state")
	}
}
func TestPhase3ChildDeleteObservationCommitFailureRollsBack(t *testing.T) {
	for _, table := range []string{"entity_tombstones", "change_events", "workout_imports", "commit"} {
		t.Run(table, func(t *testing.T) {
			f := setup(t)
			b, _ := testWorkoutBundle(t, f, 1)
			importPhase3(t, f, b)
			d := b
			d.Samples = []syncing.Event{}
			d.Series = nil
			d.Routes = nil
			d.SampleDeletions = []syncing.SampleDeletion{{SampleType: kind, SampleUUID: b.Samples[0].UUID}}
			d.RouteDeletions = []string{b.Routes[0].RouteID}
			ctx := context.Background()
			if _, err := f.s.Pool.Exec(ctx, `CREATE FUNCTION phase3_fail() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic_failure'; END $$`); err != nil {
				t.Fatal(err)
			}
			statement := "CREATE TRIGGER phase3_fail BEFORE INSERT ON " + table + " FOR EACH ROW EXECUTE FUNCTION phase3_fail()"
			if table == "commit" {
				statement = "CREATE CONSTRAINT TRIGGER phase3_fail AFTER INSERT ON workout_imports DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION phase3_fail()"
			}
			if _, err := f.s.Pool.Exec(ctx, statement); err != nil {
				t.Fatal(err)
			}
			before := phase1Snapshot(t, f)
			raw, _ := json.Marshal(d)
			body, err := f.s.ImportWorkoutBundle(ctx, f.p, d, raw)
			if err == nil || len(body) != 0 {
				t.Fatal("failed transaction returned ACK")
			}
			if !reflect.DeepEqual(before, phase1Snapshot(t, f)) {
				t.Fatal("partial deletion transaction")
			}
		})
	}
}

func TestPhase3LegacyImportAcceptedButUnclassifiedAnalysisBlocked(t *testing.T) {
	f := setup(t)
	b, _ := testWorkoutBundle(t, f, 1)
	receipt, _ := importPhase3(t, f, b)
	if receipt.SourceClassification != "source_unknown" {
		t.Fatal("legacy import falsely classified")
	}
	lease := claimAnalysis(t, f, "analysis.workout")
	if _, err := f.s.LoadAnalysis(context.Background(), lease, time.UnixMicro(*b.Workout.EndUs)); err == nil || err.Error() != "analysis_source_unclassified" {
		t.Fatal("unclassified facts entered formal analysis", err)
	}
}

func TestPhase3LateChildDeletionAfterWorkoutBarrierDoesNotResurrectParent(t *testing.T) {
	f := setup(t)
	b, _ := testWorkoutBundle(t, f, 1)
	importPhase3(t, f, b)
	ctx := context.Background()
	if _, err := f.s.DeleteWorkoutBundle(ctx, f.p, bundleDeleteRequest(f, b.WorkoutID)); err != nil {
		t.Fatal(err)
	}
	if count(t, f, "health_samples") != 1 {
		t.Fatal("fixture child was already removed")
	}
	d := b
	d.DeletionOnly = true
	d.SampleDeletions = []syncing.SampleDeletion{{SampleType: kind, SampleUUID: b.Samples[0].UUID}}
	d.RouteDeletions = []string{b.Routes[0].RouteID}
	d.Samples = []syncing.Event{}
	d.Routes = nil
	d.Series = nil
	receipt, first := importPhase3(t, f, d)
	_, again := importPhase3(t, f, d)
	if !bytes.Equal(first, again) || receipt.Counts.SamplesDeleted != 1 || receipt.Counts.WorkoutInserted || receipt.AnalysisStatus != "not_required" {
		t.Fatal("invalid deleted-parent receipt")
	}
	if count(t, f, "workouts") != 0 || count(t, f, "health_samples") != 0 || count(t, f, "workout_routes") != 0 || count(t, f, "workout_imports") != 0 {
		t.Fatal("deleted parent/raw payload resurrected")
	}
	restored, err := f.s.WorkoutImport(ctx, f.p, d.WorkoutID, receipt.BundleSHA256)
	if err != nil || !bytes.Equal(first, restored) {
		t.Fatal("cannot restore child-delete receipt", err)
	}
	raw, _ := json.Marshal(b)
	if _, err = f.s.ImportWorkoutBundle(ctx, f.p, b, raw); err == nil {
		t.Fatal("old upsert bypassed parent barrier")
	}
}

func TestPhase3RouteMicrosecondsRemainExactAcrossAllowedRange(t *testing.T) {
	f := setup(t)
	b, _ := testWorkoutBundle(t, f, 0)
	stamps := []int64{-62135596800000000, -1000001, -1, 0, 1, 253402300799999999}
	b.Routes[0].Points = nil
	for _, stamp := range stamps {
		b.Routes[0].Points = append(b.Routes[0].Points, syncing.WorkoutBundlePoint{RecordedAtUs: stamp})
	}
	importPhase3(t, f, b)
	rows, err := f.s.Pool.Query(context.Background(), `SELECT sequence,(extract(epoch from recorded_at)*1000000)::bigint FROM route_points WHERE dataset_id=$1 AND route_uuid=$2 ORDER BY sequence`, f.p.DatasetID, b.Routes[0].RouteID)
	if err != nil {
		t.Fatal(err)
	}
	i := 0
	for rows.Next() {
		var seq, us int64
		if err = rows.Scan(&seq, &us); err != nil {
			t.Fatal(err)
		}
		if seq != int64(i) || us != stamps[i] {
			t.Fatal("projection changed original microseconds", i)
		}
		i++
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if i != len(stamps) {
		t.Fatal("lost legal timestamp points")
	}
}

func TestPhase3LegacySampleMetadataEnrichmentRetainsFrozenFactAndOldReceipt(t *testing.T) {
	f := setup(t)
	b, _ := testWorkoutBundle(t, f, 1)
	phase3Observation(&b)
	b.Observation = nil
	_, originalReceipt := importPhase3(t, f, b)
	ctx := context.Background()
	var oldHash, oldMetadata []byte
	if err := f.s.Pool.QueryRow(ctx, `SELECT content_sha256,metadata FROM health_samples WHERE dataset_id=$1 AND sample_uuid=$2`, f.p.DatasetID, b.Samples[0].UUID).Scan(&oldHash, &oldMetadata); err != nil {
		t.Fatal(err)
	}
	legacy := b
	legacy.Samples = append([]syncing.Event(nil), b.Samples...)
	phase3Observation(&b)
	b.Samples[0].Metadata = map[string]any{"HKMetadataKeyHeartRateMotionContext": 1}
	first, _ := importPhase3(t, f, b)
	if first.Counts.SamplesInserted != 0 || first.Counts.SamplesUnchanged != 1 || first.AnalysisStatus != "queued" {
		t.Fatal("late metadata was not a distinct observation")
	}
	b.Observation.ObservedAtUs++
	b.Samples[0].Metadata = map[string]any{"HKMetadataKeyHeartRateMotionContext": 2}
	updated, _ := importPhase3(t, f, b)
	if updated.AnalysisStatus != "queued" {
		t.Fatal("sample metadata observation did not invalidate snapshot")
	}
	b.Observation.ObservedAtUs++
	same, _ := importPhase3(t, f, b)
	if same.AnalysisStatus != "not_required" {
		t.Fatal("unchanged sample metadata created duplicate work")
	}
	var hash, metadata []byte
	if err := f.s.Pool.QueryRow(ctx, `SELECT content_sha256,metadata FROM health_samples WHERE dataset_id=$1 AND sample_uuid=$2`, f.p.DatasetID, b.Samples[0].UUID).Scan(&hash, &metadata); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(oldHash, hash) || !bytes.Equal(oldMetadata, metadata) {
		t.Fatal("metadata enrichment rewrote original Sample")
	}
	_, replayed := importPhase3(t, f, legacy)
	if !bytes.Equal(originalReceipt, replayed) {
		t.Fatal("legacy exact receipt changed")
	}
	// Extra metadata cannot excuse a change to a core health value.
	b.Samples[0].Quantity.Value++
	raw, _ := json.Marshal(b)
	_, err := f.s.ImportWorkoutBundle(ctx, f.p, b, raw)
	requireCode(t, err, "sample_identity_conflict")
}

func TestPhase3WorkoutSourceCannotReclassifyUnprovenLegacyChildren(t *testing.T) {
	for _, which := range []string{"sample", "route", "historical_positive_evidence"} {
		t.Run(which, func(t *testing.T) {
			f := setup(t)
			b, _ := testWorkoutBundle(t, f, 1)
			phase3Observation(&b)
			switch which {
			case "sample":
				b.Observation = nil
				b.Routes = nil
				b.Samples[0].Source.BundleID = "third.party.example"
			case "route":
				b.Observation = nil
				b.Samples = nil
				b.Series = nil
				b.Routes[0].Source.BundleID = "third.party.example"
			}
			importPhase3(t, f, b)
			samples, routes := count(t, f, "health_samples"), count(t, f, "workout_routes")
			b.Samples = nil
			b.Series = nil
			b.Routes = nil
			phase3Observation(&b)
			b.Observation.ObservedAtUs = 2
			importPhase3(t, f, b)
			if count(t, f, "health_samples") != samples || count(t, f, "workout_routes") != routes {
				t.Fatal("partial observation erased retained facts")
			}
			lease := claimAnalysis(t, f, "analysis.workout")
			_, err := f.s.LoadAnalysis(context.Background(), lease, time.UnixMicro(*b.Workout.EndUs))
			if which == "historical_positive_evidence" {
				if err != nil {
					t.Fatal("previous genuine provenance was not reused", err)
				}
			} else if err == nil || err.Error() != "analysis_source_unclassified" {
				t.Fatal("Workout provenance silently reclassified a legacy child", err)
			}
		})
	}
}
