//go:build integration

package integration

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pressly/goose/v3"
	"rai/server/internal/admin"
	"rai/server/internal/syncing"
	"rai/server/migrations"
)

func seriesParent(id string, startUs, endUs int64, value float64) syncing.Event {
	return syncing.Event{Op: "upsert", Kind: "sample", Type: kind, UUID: id, StartUs: &startUs, EndUs: &endUs,
		Quantity: &syncing.Quantity{Value: value, Unit: "count/min"}, Source: &syncing.Source{BundleID: "synthetic.test"}}
}

func seriesRequest(f *fixture, seriesID, parentID string) syncing.SeriesRequest {
	return syncing.SeriesRequest{DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch,
		CollectorEpoch: f.enrollment.CollectorEpoch, SeriesID: seriesID, SampleType: kind, SampleUUID: parentID}
}

func seriesPoints(startUs int64, values ...float64) []syncing.SeriesPoint {
	points := make([]syncing.SeriesPoint, len(values))
	for i, value := range values {
		points[i] = syncing.SeriesPoint{StartUs: startUs + int64(i)*1000000, EndUs: startUs + int64(i+1)*1000000,
			Value: value, Unit: "count/min"}
	}
	return points
}

func seriesWire(t *testing.T, points []syncing.SeriesPoint) []byte {
	t.Helper()
	var decoded bytes.Buffer
	for _, point := range points {
		if err := json.NewEncoder(&decoded).Encode(point); err != nil {
			t.Fatal(err)
		}
	}
	var wire bytes.Buffer
	g := gzip.NewWriter(&wire)
	if _, err := g.Write(decoded.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	return wire.Bytes()
}

func parseSeries(t *testing.T, f *fixture, seriesID string, index int, points []syncing.SeriesPoint) (syncing.SeriesChunk, []byte) {
	t.Helper()
	state, err := f.s.Series(context.Background(), f.p, seriesID)
	if err != nil {
		t.Fatal(err)
	}
	wire := seriesWire(t, points)
	chunk, err := syncing.ParseSeriesChunk(bytes.NewReader(wire), state.SampleType, state.SampleStartUs, state.SampleEndUs)
	if err != nil {
		t.Fatal(err)
	}
	chunk.SeriesID = seriesID
	chunk.ChunkIndex = index
	return chunk, wire
}

func commitSeries(t *testing.T, f *fixture, seriesID string, index int, points []syncing.SeriesPoint) (syncing.SeriesChunkReceipt, []byte) {
	t.Helper()
	chunk, wire := parseSeries(t, f, seriesID, index, points)
	lease := takeUpload(t, f.s, f.p)
	body, err := f.s.CommitSeriesChunk(context.Background(), f.p, chunk, lease)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.ReleaseUpload(context.Background(), f.p, lease); err != nil {
		t.Fatal(err)
	}
	var receipt syncing.SeriesChunkReceipt
	if err := json.Unmarshal(body, &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt, wire
}

func seriesSnapshot(t *testing.T, f *fixture) map[string][]byte {
	t.Helper()
	out := businessSnapshot(t, f)
	for _, table := range []string{"series_uploads", "series_chunks"} {
		var raw []byte
		if err := f.s.Pool.QueryRow(context.Background(), `SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY to_jsonb(x)::text),'[]') FROM `+pgx.Identifier{table}.Sanitize()+` x`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		out[table] = raw
	}
	return out
}

func sameSeries(t *testing.T, f *fixture, before map[string][]byte) {
	t.Helper()
	after := seriesSnapshot(t, f)
	for table, value := range before {
		if !bytes.Equal(value, after[table]) {
			t.Fatal("series failure changed state:", table)
		}
	}
}

func TestQuantitySeriesChunksAndSeal(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	parentID, seriesID := syncing.NewID(), syncing.NewID()
	base := int64(1793059200000000)
	e, _, _ := f.commit(t, []syncing.Event{seriesParent(parentID, base, base+4*1000000, 4)}, 1, nil)
	state, err := f.s.RegisterSeries(ctx, f.p, seriesRequest(f, seriesID, parentID))
	if err != nil {
		t.Fatal(err)
	}
	if state.IngestStatus != "pending" || state.CommittedChunks != 0 || state.TotalPoints != 0 ||
		state.ManifestSHA256 != strings.Repeat("0", 64) || state.SampleStartUs != base || state.SampleEndUs != base+4000000 {
		t.Fatalf("bad initial series state: %+v", state)
	}
	retry, err := f.s.RegisterSeries(ctx, f.p, seriesRequest(f, seriesID, parentID))
	if err != nil || retry.SeriesID != seriesID {
		t.Fatal("series registration retry changed identity", err)
	}
	_, err = f.s.RegisterSeries(ctx, f.p, seriesRequest(f, syncing.NewID(), parentID))
	requireCode(t, err, "series_identity_conflict")
	_, err = f.s.RegisterSeries(ctx, f.p, seriesRequest(f, syncing.NewID(), syncing.NewID()))
	requireCode(t, err, "series_parent_unavailable")

	first := seriesPoints(base, 60.5, 61.25)
	r0, _ := commitSeries(t, f, seriesID, 0, first)
	if r0.IngestStatus != "pending" || r0.CommittedChunks != 1 || r0.PointCount != 2 || len(r0.ManifestSHA256) != 64 {
		t.Fatalf("bad chunk receipt: %+v", r0)
	}
	// A retry of the exact same chunk wire returns the original receipt.
	parsed, _ := parseSeries(t, f, seriesID, 0, first)
	lease := takeUpload(t, f.s, f.p)
	original, err := f.s.CommitSeriesChunk(ctx, f.p, parsed, lease)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.ReleaseUpload(ctx, f.p, lease); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, mustSeriesReceipt(t, r0)) {
		t.Fatal("chunk retry did not return original receipt")
	}
	// A different chunk body at the same index is an immutable-identity conflict.
	other, _ := parseSeries(t, f, seriesID, 0, seriesPoints(base, 99))
	lease = takeUpload(t, f.s, f.p)
	_, err = f.s.CommitSeriesChunk(ctx, f.p, other, lease)
	requireCode(t, err, "series_chunk_conflict")
	_ = f.s.ReleaseUpload(ctx, f.p, lease)
	// Non-contiguous chunks cannot skip the sequence.
	gap, _ := parseSeries(t, f, seriesID, 2, seriesPoints(base+2000000, 70))
	lease = takeUpload(t, f.s, f.p)
	_, err = f.s.CommitSeriesChunk(ctx, f.p, gap, lease)
	requireCode(t, err, "series_chunk_gap")
	var gapErr *syncing.Error
	if !errors.As(err, &gapErr) || gapErr.ExpectedChunkIndex == nil || *gapErr.ExpectedChunkIndex != 1 {
		t.Fatal("gap error lacks expectedChunkIndex=1", err)
	}
	_ = f.s.ReleaseUpload(ctx, f.p, lease)

	second := seriesPoints(base+2000000, 62.5, 63.75)
	r1, _ := commitSeries(t, f, seriesID, 1, second)
	if r1.CommittedChunks != 2 {
		t.Fatalf("second chunk count lost: %+v", r1)
	}
	state, err = f.s.Series(ctx, f.p, seriesID)
	if err != nil || state.IngestStatus != "pending" || state.CommittedChunks != 2 || state.TotalPoints != 4 {
		t.Fatalf("pending series state wrong: %+v %v", state, err)
	}
	manifest := r1.ManifestSHA256
	_, err = f.s.SealSeries(ctx, f.p, seriesID, syncing.SeriesSeal{ChunkCount: 1, TotalPoints: 4, ManifestSHA256: manifest})
	requireCode(t, err, "series_incomplete")
	_, err = f.s.SealSeries(ctx, f.p, seriesID, syncing.SeriesSeal{ChunkCount: 2, TotalPoints: 4, ManifestSHA256: strings.Repeat("f", 64)})
	requireCode(t, err, "series_manifest_mismatch")
	sealed, err := f.s.SealSeries(ctx, f.p, seriesID, syncing.SeriesSeal{ChunkCount: 2, TotalPoints: 4, ManifestSHA256: manifest})
	if err != nil {
		t.Fatal(err)
	}
	var receipt syncing.SeriesReceipt
	if err = json.Unmarshal(sealed, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.IngestStatus != "sealed" || receipt.ChunkCount != 2 || receipt.TotalPoints != 4 ||
		receipt.CommitRevision != 2 || receipt.AnalysisStatus != "queued" || len(receipt.AnalysisJobIDs) != 1 {
		t.Fatalf("bad seal receipt: %+v", receipt)
	}
	if seriesCount(t, f, "jobs", "kind='analysis.series'") != 1 || seriesCount(t, f, "dirty_scopes", "scope_key LIKE 'series:%'") != 1 || seriesCount(t, f, "change_events", "entity_kind='series'") != 1 {
		t.Fatal("seal did not commit exactly one series scope, job and change event")
	}
	retrySeal, err := f.s.SealSeries(ctx, f.p, seriesID, syncing.SeriesSeal{ChunkCount: 2, TotalPoints: 4, ManifestSHA256: manifest})
	if err != nil || !bytes.Equal(retrySeal, sealed) {
		t.Fatal("seal retry changed original receipt", err)
	}
	state, err = f.s.Series(ctx, f.p, seriesID)
	if err != nil || state.IngestStatus != "sealed" || state.ChunkCount == nil || *state.ChunkCount != 2 || !bytes.Equal(state.Receipt, sealed) {
		t.Fatal("sealed state or receipt lost", err)
	}
	// A retry of an already committed chunk after seal still returns its original receipt.
	retryChunk, _ := commitSeries(t, f, seriesID, 0, first)
	if retryChunk != r0 {
		t.Fatal("sealed chunk retry changed original receipt")
	}
	after, _ := parseSeries(t, f, seriesID, 2, seriesPoints(base+3000000, 80))
	lease = takeUpload(t, f.s, f.p)
	_, err = f.s.CommitSeriesChunk(ctx, f.p, after, lease)
	requireCode(t, err, "series_sealed")
	_ = f.s.ReleaseUpload(ctx, f.p, lease)

	// Parent deletion cascades the detail and prevents resurrection by a late chunk.
	f.commit(t, []syncing.Event{deleted(parentID)}, 2, &e.BatchID)
	if count(t, f, "series_uploads") != 0 || count(t, f, "series_chunks") != 0 {
		t.Fatal("parent deletion left series detail")
	}
	if _, err = f.s.Series(ctx, f.p, seriesID); err == nil {
		t.Fatal("deleted series remains readable")
	}
	_, err = f.s.RegisterSeries(ctx, f.p, seriesRequest(f, syncing.NewID(), parentID))
	requireCode(t, err, "series_parent_unavailable")
	late, _ := syncing.ParseSeriesChunk(bytes.NewReader(seriesWire(t, seriesPoints(base, 81))), kind, base, base+4000000)
	late.SeriesID, late.ChunkIndex = seriesID, 0
	lease = takeUpload(t, f.s, f.p)
	_, err = f.s.CommitSeriesChunk(ctx, f.p, late, lease)
	requireCode(t, err, "series_not_found")
	_ = f.s.ReleaseUpload(ctx, f.p, lease)
	_, err = f.s.SealSeries(ctx, f.p, seriesID, syncing.SeriesSeal{ChunkCount: 2, TotalPoints: 4, ManifestSHA256: manifest})
	requireCode(t, err, "series_not_found")
}

func seriesCount(t *testing.T, f *fixture, table, where string) int {
	t.Helper()
	var n int
	if err := f.s.Pool.QueryRow(context.Background(), "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()+" WHERE "+where).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func mustSeriesReceipt(t *testing.T, r syncing.SeriesChunkReceipt) []byte {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSeriesFailureRollsBackSealAndLease(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	parentID, seriesID := syncing.NewID(), syncing.NewID()
	base := int64(1793059200000000)
	firstBatch, _, _ := f.commit(t, []syncing.Event{seriesParent(parentID, base, base+4000000, 4)}, 1, nil)
	if _, err := f.s.RegisterSeries(ctx, f.p, seriesRequest(f, seriesID, parentID)); err != nil {
		t.Fatal(err)
	}
	commitSeries(t, f, seriesID, 0, seriesPoints(base, 60.5, 61.25))
	commitSeries(t, f, seriesID, 1, seriesPoints(base+2000000, 62.5, 63.75))
	state, err := f.s.Series(ctx, f.p, seriesID)
	if err != nil {
		t.Fatal(err)
	}
	before := seriesSnapshot(t, f)
	if _, err = f.s.Pool.Exec(ctx, `CREATE FUNCTION fail_series_seal() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic series seal failure'; END $$;
CREATE TRIGGER fail_series_seal BEFORE UPDATE ON series_uploads FOR EACH ROW WHEN (NEW.state='sealed') EXECUTE FUNCTION fail_series_seal()`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.SealSeries(ctx, f.p, seriesID, syncing.SeriesSeal{ChunkCount: 2, TotalPoints: 4, ManifestSHA256: state.ManifestSHA256}); err == nil {
		t.Fatal("series seal failure not injected")
	}
	// The failure is a database error, not a protocol error; every business row
	// including series state, revision, dirty scope, job and change event must
	// remain unchanged, and the chunks remain available for a later seal.
	sameSeries(t, f, before)
	if _, err = f.s.Pool.Exec(ctx, `DROP TRIGGER fail_series_seal ON series_uploads`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.SealSeries(ctx, f.p, seriesID, syncing.SeriesSeal{ChunkCount: 2, TotalPoints: 4, ManifestSHA256: state.ManifestSHA256}); err != nil {
		t.Fatal("seal could not recover", err)
	}
	if seriesCount(t, f, "jobs", "kind='analysis.series'") != 1 || seriesCount(t, f, "dirty_scopes", "scope_key LIKE 'series:%'") != 1 {
		t.Fatal("recovered seal did not create one series job/scope")
	}

	// A lease expiry at chunk commit rolls back the chunk row and counters,
	// then a fresh lease commits the same immutable chunk.
	parent2, series2 := syncing.NewID(), syncing.NewID()
	start2 := base + 5000000
	f.commit(t, []syncing.Event{seriesParent(parent2, start2, start2+1000000, 1)}, 2, &firstBatch.BatchID)
	if _, err = f.s.RegisterSeries(ctx, f.p, seriesRequest(f, series2, parent2)); err != nil {
		t.Fatal(err)
	}
	newState, err := f.s.Series(ctx, f.p, series2)
	if err != nil {
		t.Fatal(err)
	}
	beforeLease := seriesSnapshot(t, f)
	chunk, _ := parseSeries(t, f, series2, 0, seriesPoints(start2, 70))
	lease := takeUpload(t, f.s, f.p)
	if _, err = f.s.Pool.Exec(ctx, `UPDATE sync_upload_slots SET expires_at=clock_timestamp()-interval '1 second' WHERE token=$1`, lease.Token); err != nil {
		t.Fatal(err)
	}
	_, err = f.s.CommitSeriesChunk(ctx, f.p, chunk, lease)
	requireCode(t, err, "upload_lease_lost")
	sameSeries(t, f, beforeLease)
	if err = f.s.ReleaseUpload(ctx, f.p, lease); err != nil {
		t.Fatal(err)
	}
	fresh := takeUpload(t, f.s, f.p)
	if _, err = f.s.CommitSeriesChunk(ctx, f.p, chunk, fresh); err != nil {
		t.Fatal("chunk could not recover with fresh lease", err)
	}
	if err = f.s.ReleaseUpload(ctx, f.p, fresh); err != nil {
		t.Fatal(err)
	}
	afterState, err := f.s.Series(ctx, f.p, series2)
	if err != nil || afterState.CommittedChunks != 1 || afterState.ManifestSHA256 == newState.ManifestSHA256 {
		t.Fatal("fresh lease did not commit chunk", err)
	}
}

func TestSchema7UpgradeAddsSeriesWithoutRewritingFacts(t *testing.T) {
	f := setupWithMigration(t, func(ctx context.Context, url string) error {
		db, err := sql.Open("pgx", url)
		if err != nil {
			return err
		}
		defer db.Close()
		provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.Files)
		if err != nil {
			return err
		}
		_, err = provider.UpTo(ctx, 7)
		return err
	})
	ctx := context.Background()
	parentID := syncing.NewID()
	base := int64(1793059200000000)
	parent := seriesParent(parentID, base, base+4000000, 4)
	e, r, original := f.commit(t, []syncing.Event{parent}, 1, nil)
	if r.Counts.Inserted != 1 {
		t.Fatal(r)
	}
	before := businessSnapshot(t, f)
	if err := f.s.Ready(ctx); err == nil {
		t.Fatal("schema 7 accepted by schema 9 binary")
	}
	for range 2 {
		if err := admin.Migrate(ctx, f.s.Pool.Config().ConnString()); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.s.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	sameBusinessAcrossDeviceMigration(t, f, before)
	if count(t, f, "series_uploads") != 0 || count(t, f, "series_chunks") != 0 {
		t.Fatal("migration fabricated series detail")
	}
	retry, err := f.s.Commit(ctx, f.p, e, []syncing.Event{parent})
	if err != nil || !bytes.Equal(retry, original) {
		t.Fatal("upgrade changed original sample receipt", err)
	}
	newBatch, _, _ := f.commit(t, []syncing.Event{sample(syncing.NewID(), 120)}, 2, &e.BatchID)
	var batchDevice string
	if err = f.s.Pool.QueryRow(ctx, `SELECT device_id FROM sync_batches WHERE dataset_id=$1 AND batch_id=$2`, f.p.DatasetID, newBatch.BatchID).Scan(&batchDevice); err != nil || batchDevice != f.p.DeviceID {
		t.Fatal("new batch on legacy stream lost device provenance", err)
	}
	seriesID := syncing.NewID()
	state, err := f.s.RegisterSeries(ctx, f.p, seriesRequest(f, seriesID, parentID))
	if err != nil || state.IngestStatus != "pending" {
		t.Fatal("series unavailable after upgrade", err)
	}
	chunk, _ := parseSeries(t, f, seriesID, 0, seriesPoints(base, 60.5, 61.25))
	lease := takeUpload(t, f.s, f.p)
	chunkBody, err := f.s.CommitSeriesChunk(ctx, f.p, chunk, lease)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.s.ReleaseUpload(ctx, f.p, lease); err != nil {
		t.Fatal(err)
	}
	var chunkReceipt syncing.SeriesChunkReceipt
	if err = json.Unmarshal(chunkBody, &chunkReceipt); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.SealSeries(ctx, f.p, seriesID, syncing.SeriesSeal{ChunkCount: 1, TotalPoints: 2, ManifestSHA256: chunkReceipt.ManifestSHA256}); err != nil {
		t.Fatal("series seal unavailable after upgrade", err)
	}
}

func TestMaximumSeriesChunksSeal(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	parentID, seriesID := syncing.NewID(), syncing.NewID()
	base := int64(1793059200000000)
	span := int64(syncing.MaxSeriesChunks) * 1000000
	f.commit(t, []syncing.Event{seriesParent(parentID, base, base+span, 1000)}, 1, nil)
	if _, err := f.s.RegisterSeries(ctx, f.p, seriesRequest(f, seriesID, parentID)); err != nil {
		t.Fatal(err)
	}
	lease := takeUpload(t, f.s, f.p)
	for i := 0; i < syncing.MaxSeriesChunks; i++ {
		if i > 0 && i%100 == 0 {
			if err := f.s.ReleaseUpload(ctx, f.p, lease); err != nil {
				t.Fatal(err)
			}
			lease = takeUpload(t, f.s, f.p)
		}
		points := seriesPoints(base+int64(i)*1000000, float64(60+i%10))
		chunk, err := syncing.ParseSeriesChunk(bytes.NewReader(seriesWire(t, points)), kind, base, base+span)
		if err != nil {
			t.Fatal(err)
		}
		chunk.SeriesID, chunk.ChunkIndex = seriesID, i
		if _, err = f.s.CommitSeriesChunk(ctx, f.p, chunk, lease); err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
	}
	if err := f.s.ReleaseUpload(ctx, f.p, lease); err != nil {
		t.Fatal(err)
	}
	state, err := f.s.Series(ctx, f.p, seriesID)
	if err != nil || state.CommittedChunks != syncing.MaxSeriesChunks || state.TotalPoints != syncing.MaxSeriesChunks {
		t.Fatalf("maximum chunk state wrong: %+v %v", state, err)
	}
	sealed, err := f.s.SealSeries(ctx, f.p, seriesID, syncing.SeriesSeal{ChunkCount: syncing.MaxSeriesChunks, TotalPoints: syncing.MaxSeriesChunks, ManifestSHA256: state.ManifestSHA256})
	if err != nil {
		t.Fatal(err)
	}
	var receipt syncing.SeriesReceipt
	if err = json.Unmarshal(sealed, &receipt); err != nil || receipt.IngestStatus != "sealed" || receipt.ChunkCount != syncing.MaxSeriesChunks {
		t.Fatalf("maximum series seal invalid: %v %+v", err, receipt)
	}
}
