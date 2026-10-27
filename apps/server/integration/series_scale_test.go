//go:build integration && scale

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"rai/server/internal/syncing"
)

// Exercise the total point limit with full-size chunks, retaining and checking
// every synthetic point rather than treating counters as proof of persistence.
func TestScaleSeriesMillionPoints(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	const chunks = syncing.MaxSeriesPoints / syncing.MaxSeriesPointsPerChunk
	const base int64 = 1793059200000000
	end := base + syncing.MaxSeriesPoints + 1
	parentID, seriesID := syncing.NewID(), syncing.NewID()
	f.commit(t, []syncing.Event{seriesParent(parentID, base, end, 1)}, 1, nil)
	if _, err := f.s.RegisterSeries(ctx, f.p, seriesRequest(f, seriesID, parentID)); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	chain := make([]byte, sha256.Size)
	var firstChunk syncing.SeriesChunk
	var firstReceipt []byte
	for i := 0; i < chunks; i++ {
		points := make([]syncing.SeriesPoint, syncing.MaxSeriesPointsPerChunk)
		for j := range points {
			n := i*len(points) + j
			points[j] = syncing.SeriesPoint{StartUs: base + int64(n), EndUs: base + int64(n) + 1, Value: float64(n%1000) + 0.5, Unit: "count/min"}
		}
		wire := seriesWire(t, points)
		chunk, err := syncing.ParseSeriesChunk(bytes.NewReader(wire), kind, base, end)
		if err != nil {
			t.Fatalf("parse chunk %d: %v", i, err)
		}
		chunk.SeriesID, chunk.ChunkIndex = seriesID, i
		canonical, err := json.Marshal(points)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(canonical)
		next := sha256.Sum256(append(append([]byte(nil), chain...), digest[:]...))
		chain = next[:]
		lease := takeUpload(t, f.s, f.p)
		body, err := f.s.CommitSeriesChunk(ctx, f.p, chunk, lease)
		if err != nil {
			t.Fatalf("commit chunk %d: %v", i, err)
		}
		if err = f.s.ReleaseUpload(ctx, f.p, lease); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstChunk, firstReceipt = chunk, append([]byte(nil), body...)
		}
		if i == chunks/2 {
			nextStore := uploadPool(t, f)
			f.s.Pool.Close()
			f.s = nextStore
			lease = takeUpload(t, f.s, f.p)
			retry, err := f.s.CommitSeriesChunk(ctx, f.p, firstChunk, lease)
			if err != nil || !bytes.Equal(retry, firstReceipt) {
				t.Fatalf("retry after reconnect: %v", err)
			}
			if err = f.s.ReleaseUpload(ctx, f.p, lease); err != nil {
				t.Fatal(err)
			}
		}
	}
	state, err := f.s.Series(ctx, f.p, seriesID)
	if err != nil || state.IngestStatus != "pending" || state.CommittedChunks != chunks || state.TotalPoints != syncing.MaxSeriesPoints || state.ManifestSHA256 != hex.EncodeToString(chain) {
		t.Fatalf("pending state: %+v, %v", state, err)
	}
	if seriesCount(t, f, "jobs", "kind='analysis.series'") != 0 || seriesCount(t, f, "change_events", "entity_kind='series'") != 0 || seriesCount(t, f, "dirty_scopes", "scope_key LIKE 'series:%'") != 0 {
		t.Fatal("pending series published work")
	}
	extra, err := syncing.ParseSeriesChunk(bytes.NewReader(seriesWire(t, []syncing.SeriesPoint{{StartUs: end - 1, EndUs: end, Value: 42, Unit: "count/min"}})), kind, base, end)
	if err != nil {
		t.Fatal(err)
	}
	extra.SeriesID, extra.ChunkIndex = seriesID, chunks
	lease := takeUpload(t, f.s, f.p)
	_, err = f.s.CommitSeriesChunk(ctx, f.p, extra, lease)
	requireCode(t, err, "series_too_large")
	if err = f.s.ReleaseUpload(ctx, f.p, lease); err != nil {
		t.Fatal(err)
	}
	after, err := f.s.Series(ctx, f.p, seriesID)
	if err != nil || after.CommittedChunks != state.CommittedChunks || after.TotalPoints != state.TotalPoints || after.ManifestSHA256 != state.ManifestSHA256 {
		t.Fatalf("over-limit changed state: %+v, %v", after, err)
	}
	seal := syncing.SeriesSeal{ChunkCount: chunks, TotalPoints: syncing.MaxSeriesPoints, ManifestSHA256: state.ManifestSHA256}
	sealStarted := time.Now()
	body, err := f.s.SealSeries(ctx, f.p, seriesID, seal)
	if err != nil {
		t.Fatal(err)
	}
	sealDuration := time.Since(sealStarted)
	var receipt syncing.SeriesReceipt
	if err = json.Unmarshal(body, &receipt); err != nil || receipt.IngestStatus != "sealed" || receipt.TotalPoints != syncing.MaxSeriesPoints || receipt.ChunkCount != chunks || receipt.CommitRevision != 2 || receipt.AnalysisStatus != "queued" {
		t.Fatalf("seal receipt: %+v, %v", receipt, err)
	}
	if retry, err := f.s.SealSeries(ctx, f.p, seriesID, seal); err != nil || !bytes.Equal(body, retry) {
		t.Fatalf("seal retry: %v", err)
	}
	if seriesCount(t, f, "jobs", "kind='analysis.series'") != 1 || seriesCount(t, f, "change_events", "entity_kind='series'") != 1 || seriesCount(t, f, "dirty_scopes", "scope_key LIKE 'series:%'") != 1 {
		t.Fatal("seal publication incomplete")
	}
	if count(t, f, "series_chunks") != chunks {
		t.Fatal("stored chunk count differs")
	}
	// Bound each verification query to one immutable chunk; processing the
	// entire result stream in Go can exceed the online statement timeout.
	for index := 0; index < chunks; index++ {
		var count int
		var payload []byte
		if err := f.s.Pool.QueryRow(ctx, `SELECT point_count, points FROM series_chunks WHERE dataset_id=$1 AND series_id=$2 AND chunk_index=$3`, f.p.DatasetID, seriesID, index).Scan(&count, &payload); err != nil {
			t.Fatal(err)
		}
		var points []syncing.SeriesPoint
		if err := json.Unmarshal(payload, &points); err != nil {
			t.Fatal(err)
		}
		if count != syncing.MaxSeriesPointsPerChunk || len(points) != count {
			t.Fatalf("chunk %d count/order mismatch", index)
		}
		for j, point := range points {
			n := index*syncing.MaxSeriesPointsPerChunk + j
			want := syncing.SeriesPoint{StartUs: base + int64(n), EndUs: base + int64(n) + 1, Value: float64(n%1000) + 0.5, Unit: "count/min"}
			if point != want {
				t.Fatalf("point %d differs: %+v", n, point)
			}
		}
	}
	t.Logf("SCALE_SERIES total=%d chunks=%d staging_seal_and_verify=%s seal=%s", syncing.MaxSeriesPoints, chunks, time.Since(started), sealDuration)
}
