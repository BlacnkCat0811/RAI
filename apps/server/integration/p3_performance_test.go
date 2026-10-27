//go:build integration && scale

package integration

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"sort"
	"testing"
	"time"

	"rai/server/internal/syncing"
)

type p3SampleMetric struct {
	batchSize int
	wireBytes int
	decoded   int
	gzipTime  time.Duration
	register  time.Duration
	parse     time.Duration
	commit    time.Duration
	total     time.Duration
}

func measureSampleBatch(t *testing.T, f *fixture, size int, seq int64, previous *string) (p3SampleMetric, string) {
	t.Helper()
	ctx := context.Background()
	events := make([]syncing.Event, size)
	for i := range events {
		events[i] = sample(syncing.NewID(), float64(80+i%60))
	}
	start := time.Now()
	envelope, wire := f.envelope(t, events, seq, previous)
	gzipTime := time.Since(start)
	start = time.Now()
	if _, err := f.s.RegisterBatch(ctx, f.p, envelope); err != nil {
		t.Fatal(err)
	}
	register := time.Since(start)
	start = time.Now()
	parsed, err := syncing.ParseBody(bytes.NewReader(wire), envelope, f.stream.SampleType)
	if err != nil {
		t.Fatal(err)
	}
	parse := time.Since(start)
	start = time.Now()
	if _, err = f.s.Commit(ctx, f.p, envelope, parsed); err != nil {
		t.Fatal(err)
	}
	commit := time.Since(start)
	return p3SampleMetric{batchSize: size, wireBytes: len(wire), decoded: envelope.DecodedBytes,
		gzipTime: gzipTime, register: register, parse: parse, commit: commit,
		total: gzipTime + register + parse + commit}, envelope.BatchID
}

func TestP3PerformanceBaseline(t *testing.T) {
	for _, size := range []int{250, 500, 1000} {
		t.Run("samples_"+itoaIntegration64(int64(size)), func(t *testing.T) {
			f := setup(t)
			m, _ := measureSampleBatch(t, f, size, 1, nil)
			t.Logf("P3_PERF samples size=%d wire=%d decoded=%d gzip_ms=%.3f register_ms=%.3f parse_ms=%.3f commit_ms=%.3f total_ms=%.3f",
				m.batchSize, m.wireBytes, m.decoded, ms(m.gzipTime), ms(m.register), ms(m.parse), ms(m.commit), ms(m.total))
		})
	}
	t.Run("samples_2000_two_batches", func(t *testing.T) {
		f := setup(t)
		first, firstID := measureSampleBatch(t, f, 1000, 1, nil)
		second, _ := measureSampleBatch(t, f, 1000, 2, &firstID)
		t.Logf("P3_PERF samples size=2000 batches=2 wire=%d commit_ms=%.3f total_ms=%.3f",
			first.wireBytes+second.wireBytes, ms(first.commit+second.commit), ms(first.total+second.total))
	})
	t.Run("samples_1000_repeat_p95", func(t *testing.T) {
		f := setup(t)
		var totals, commits []time.Duration
		var previous *string
		for i := 0; i < 8; i++ {
			m, batchID := measureSampleBatch(t, f, 1000, int64(i+1), previous)
			previous = &batchID
			totals = append(totals, m.total)
			commits = append(commits, m.commit)
		}
		sort.Slice(totals, func(i, j int) bool { return totals[i] < totals[j] })
		sort.Slice(commits, func(i, j int) bool { return commits[i] < commits[j] })
		p95 := func(values []time.Duration) time.Duration { return values[(len(values)*95+99)/100-1] }
		t.Logf("P3_PERF samples=1000 runs=8 commit_p95_ms=%.3f total_p95_ms=%.3f", ms(p95(commits)), ms(p95(totals)))
	})
	for _, points := range []int{500, 1000, 5000} {
		t.Run("route_"+itoaIntegration64(int64(points)), func(t *testing.T) {
			f := setup(t)
			workoutStream(t, f)
			start := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
			w := workoutAt(start, points)
			f.commit(t, []syncing.Event{w}, 1, nil)
			route := syncing.NewID()
			routePoints := testRoutePoints(start, points, 0)
			encodeStart := time.Now()
			var decoded bytes.Buffer
			for _, point := range routePoints {
				_ = json.NewEncoder(&decoded).Encode(point)
			}
			var wire bytes.Buffer
			gz := gzip.NewWriter(&wire)
			_, _ = gz.Write(decoded.Bytes())
			_ = gz.Close()
			encode := time.Since(encodeStart)
			registerTestRoute(t, f, w.UUID, route, start, start.Add(time.Duration(points-1)*time.Second), 1, int64(points))
			chunkStart := time.Now()
			receipt := uploadTestRouteChunk(t, f, route, 0, routePoints)
			chunkTime := time.Since(chunkStart)
			sealStart := time.Now()
			sealTestRoute(t, f, route, 1, int64(points), receipt.ManifestSHA256, receipt.PayloadManifestSHA256)
			sealTime := time.Since(sealStart)
			t.Logf("P3_PERF route points=%d wire=%d decoded=%d encode_gzip_ms=%.3f chunk_ms=%.3f seal_ms=%.3f",
				points, wire.Len(), decoded.Len(), ms(encode), ms(chunkTime), ms(sealTime))
		})
	}
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
