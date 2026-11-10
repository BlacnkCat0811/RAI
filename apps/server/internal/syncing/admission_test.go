package syncing

import (
	"testing"
	"time"
)

func TestLimiterBurstThenRefill(t *testing.T) {
	limiter := NewLimiter()
	budget := RequestBudget{PerMinute: 60, Burst: 3}
	now := time.Date(2026, 11, 10, 8, 0, 0, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if _, ok := limiter.Allow("k", budget, now); !ok {
			t.Fatalf("burst request %d denied", i+1)
		}
	}
	wait, ok := limiter.Allow("k", budget, now)
	if ok || wait != time.Second {
		t.Fatalf("fourth request: ok=%v wait=%v", ok, wait)
	}
	// A denied request spends nothing, and one interval later one slot is back.
	if _, ok = limiter.Allow("k", budget, now.Add(time.Second)); !ok {
		t.Fatal("slot did not refill after one interval")
	}
	if _, ok = limiter.Allow("k", budget, now.Add(time.Second)); ok {
		t.Fatal("refilled more than one slot")
	}
	// Other keys have their own bucket.
	if _, ok = limiter.Allow("other", budget, now); !ok {
		t.Fatal("independent bucket denied")
	}
}
