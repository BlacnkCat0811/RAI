package syncing

import (
	"sync"
	"time"
)

const (
	DatasetUploads     = 2
	UploadLeaseSeconds = 30
)

// UploadLease is server-owned operational state, never part of an envelope or receipt.
type UploadLease struct {
	Token     string
	ExpiresAt time.Time
}

// AdmissionLane separates recovery reads, JSON registrations and expensive body
// attempts. An upload reserves a whole MaxWireBytes budget even for chunked,
// invalid or retried input, before reading any body bytes.
type AdmissionLane string

const (
	ReadLane   AdmissionLane = "read"
	WriteLane  AdmissionLane = "write"
	UploadLane AdmissionLane = "upload"
)

type RequestBudget struct {
	PerMinute int `json:"perMinute"`
	Burst     int `json:"burst"`
}

type AdmissionPolicy struct {
	Device  RequestBudget `json:"device"`
	Dataset RequestBudget `json:"dataset"`
}

var (
	requestBudgetsMu       sync.RWMutex
	requestBudgetsOverride map[AdmissionLane]AdmissionPolicy
)

// Fixed initial safety budgets, not measured production throughput guarantees.
// Every interval divides a minute exactly at PostgreSQL microsecond precision.
func defaultRequestBudgets() map[AdmissionLane]AdmissionPolicy {
	return map[AdmissionLane]AdmissionPolicy{
		ReadLane:   {Device: RequestBudget{120, 30}, Dataset: RequestBudget{240, 60}},
		WriteLane:  {Device: RequestBudget{60, 10}, Dataset: RequestBudget{120, 20}},
		UploadLane: {Device: RequestBudget{30, 2}, Dataset: RequestBudget{60, 4}},
	}
}

// SetRequestBudgets applies an explicit deployment policy before serving.
// A nil map restores the fixed defaults; the override is process-wide.
func SetRequestBudgets(policies map[AdmissionLane]AdmissionPolicy) {
	requestBudgetsMu.Lock()
	defer requestBudgetsMu.Unlock()
	if len(policies) == 0 {
		requestBudgetsOverride = nil
		return
	}
	copied := make(map[AdmissionLane]AdmissionPolicy, len(policies))
	for lane, policy := range policies {
		copied[lane] = policy
	}
	requestBudgetsOverride = copied
}

func RequestBudgets() map[AdmissionLane]AdmissionPolicy {
	requestBudgetsMu.RLock()
	if requestBudgetsOverride != nil {
		copied := make(map[AdmissionLane]AdmissionPolicy, len(requestBudgetsOverride))
		for lane, policy := range requestBudgetsOverride {
			copied[lane] = policy
		}
		requestBudgetsMu.RUnlock()
		return copied
	}
	requestBudgetsMu.RUnlock()
	return defaultRequestBudgets()
}

// Limiter is an in-process GCRA token bucket. One personal server instance
// needs no shared state: a restart simply refills every bucket, which is the
// same as a quiet minute. It replaces the per-request PostgreSQL bucket writes.
type Limiter struct {
	mu  sync.Mutex
	tat map[string]time.Time
}

func NewLimiter() *Limiter { return &Limiter{tat: map[string]time.Time{}} }

// Bucket is one budget a request spends a slot from.
type Bucket struct {
	Key    string
	Budget RequestBudget
}

// Allow spends one slot of key's bucket at now. On denial it reports how long
// the caller should wait. A denied request does not move the bucket.
func (l *Limiter) Allow(key string, budget RequestBudget, now time.Time) (time.Duration, bool) {
	_, wait, ok := l.AllowAll(now, Bucket{key, budget})
	return wait, ok
}

// AllowAll spends one slot from every bucket or from none. A request refused by
// any bucket leaves all of them untouched, so a device that exceeds its own
// budget never drains the shared dataset budget. On denial it reports the first
// refusing bucket in argument order and the longest wait among refusing ones,
// the earliest moment every bucket would admit the request again.
func (l *Limiter) AllowAll(now time.Time, buckets ...Bucket) (denied int, wait time.Duration, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	next := make([]time.Time, len(buckets))
	denied = -1
	for i, b := range buckets {
		interval := time.Minute / time.Duration(b.Budget.PerMinute)
		tat := l.tat[b.Key]
		if tat.Before(now) {
			tat = now
		}
		next[i] = tat.Add(interval)
		allowAt := next[i].Add(-time.Duration(b.Budget.Burst) * interval)
		if now.Before(allowAt) {
			if denied < 0 {
				denied = i
			}
			wait = max(wait, allowAt.Sub(now))
		}
	}
	if denied >= 0 {
		return denied, wait, false
	}
	for i, b := range buckets {
		l.tat[b.Key] = next[i]
	}
	if len(l.tat) > 4096 {
		for k, v := range l.tat {
			if v.Before(now) {
				delete(l.tat, k)
			}
		}
	}
	return -1, 0, true
}
