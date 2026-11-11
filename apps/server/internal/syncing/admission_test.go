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

// One phone over its own budget must not spend the shared dataset budget: a
// request any bucket refuses moves none of them.
func TestLimiterAllowAllSpendsAllOrNothing(t *testing.T) {
	limiter := NewLimiter()
	device := Bucket{"v:phone", RequestBudget{PerMinute: 30, Burst: 2}}
	dataset := Bucket{"d:set", RequestBudget{PerMinute: 60, Burst: 4}}
	now := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	for i := 0; i < 2; i++ {
		if _, _, ok := limiter.AllowAll(now, device, dataset); !ok {
			t.Fatalf("burst request %d denied", i+1)
		}
	}
	for i := 0; i < 5; i++ {
		denied, wait, ok := limiter.AllowAll(now, device, dataset)
		if ok || denied != 0 || wait != 2*time.Second {
			t.Fatalf("over-budget request %d: denied=%d wait=%v ok=%v", i+1, denied, wait, ok)
		}
	}
	// The dataset bucket spent only the two admitted requests: two of its four
	// burst slots remain for another device right now.
	other := Bucket{"v:other", RequestBudget{PerMinute: 30, Burst: 2}}
	for i := 0; i < 2; i++ {
		if _, _, ok := limiter.AllowAll(now, other, dataset); !ok {
			t.Fatalf("dataset slot %d was drained by denied requests", i+3)
		}
	}
	denied, wait, ok := limiter.AllowAll(now, Bucket{"v:third", other.Budget}, dataset)
	if ok || denied != 1 || wait != time.Second {
		t.Fatalf("exhausted dataset: denied=%d wait=%v ok=%v", denied, wait, ok)
	}
	// The longest wait among refusing buckets is reported.
	if denied, wait, ok = limiter.AllowAll(now, device, dataset); ok || denied != 0 || wait != 2*time.Second {
		t.Fatalf("both refusing: denied=%d wait=%v ok=%v", denied, wait, ok)
	}
}
