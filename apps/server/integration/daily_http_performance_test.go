//go:build integration && m5aperf

package integration

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"rai/server/internal/httpapi"
	"rai/server/internal/syncing"
)

// Explicit slow acceptance: keep the real shared HTTP admission budget on.
func TestDailyHTTPBootstrapWithRealAdmission(t *testing.T) {
	for _, days := range []int{7, 30, 90, 180} {
		t.Run(fmt.Sprint(days), func(t *testing.T) {
			f := setup(t)
			server := httptest.NewServer(httpapi.New(f.s, slog.New(slog.NewTextHandler(io.Discard, nil))))
			defer server.Close()
			client := server.Client()
			b := dailyBatch(f, syncing.DailySleep)
			b.BootstrapStartUs = b.ObservedAtUs - int64(days)*86400000000
			total, batches, payload, retries := days*32, 0, 0, 0
			send := func(raw []byte) {
				for attempt := 0; attempt < 8; attempt++ {
					req, err := http.NewRequest(http.MethodPost, server.URL+"/v2/sync/daily-facts", bytes.NewReader(raw))
					if err != nil {
						t.Fatal(err)
					}
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
					response, err := client.Do(req)
					if err != nil {
						t.Fatal("synthetic HTTP failed")
					}
					_, err = io.Copy(io.Discard, response.Body)
					response.Body.Close()
					if err != nil {
						t.Fatal("synthetic ACK read failed")
					}
					if response.StatusCode == 200 {
						return
					}
					if response.StatusCode != 429 {
						t.Fatal("unexpected synthetic status", response.StatusCode)
					}
					retries++
					delay, err := strconv.Atoi(response.Header.Get("Retry-After"))
					if err != nil || delay < 1 || delay > 10 {
						t.Fatal("invalid admission retry delay")
					}
					time.Sleep(time.Duration(delay) * time.Second)
				}
				t.Fatal("admission never recovered")
			}
			begin := time.Now()
			for start := 0; start < total; start += syncing.MaxDailyChanges {
				end := min(start+syncing.MaxDailyChanges, total)
				for i := start; i < end; i++ {
					fact := dailyFact(b.SampleType)
					fact.StartUs = b.BootstrapStartUs + int64(i)*86400000000/32
					fact.EndUs = fact.StartUs + 1800000000
					b.Facts = append(b.Facts, fact)
				}
				b.HasMore = end < total
				raw := rawDaily(b)
				payload += len(raw)
				send(raw)
				batches++
				b = nextDaily(b)
			}
			bootstrap := time.Since(begin)
			b.Facts = []syncing.DailyFact{dailyFact(b.SampleType)}
			hotStart := time.Now()
			send(rawDaily(b))
			hot := time.Since(hotStart)
			t.Logf("M5A_HTTP_PERF days=%d facts=%d batches=%d payload_bytes=%d http_bootstrap_ms=%.2f hot_ms=%.2f admission_retries=%d", days, total, batches, payload, float64(bootstrap.Microseconds())/1000, float64(hot.Microseconds())/1000, retries)
		})
	}
}
