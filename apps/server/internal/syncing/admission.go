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

// Allow spends one slot of key's bucket at now. On denial it reports how long
// the caller should wait. A denied request does not move the bucket.
func (l *Limiter) Allow(key string, budget RequestBudget, now time.Time) (time.Duration, bool) {
	interval := time.Minute / time.Duration(budget.PerMinute)
	l.mu.Lock()
	defer l.mu.Unlock()
	tat := l.tat[key]
	if tat.Before(now) {
		tat = now
	}
	next := tat.Add(interval)
	allowAt := next.Add(-time.Duration(budget.Burst) * interval)
	if now.Before(allowAt) {
		return allowAt.Sub(now), false
	}
	l.tat[key] = next
	if len(l.tat) > 4096 {
		for k, v := range l.tat {
			if v.Before(now) {
				delete(l.tat, k)
			}
		}
	}
	return 0, true
}
