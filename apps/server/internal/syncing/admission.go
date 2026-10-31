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
