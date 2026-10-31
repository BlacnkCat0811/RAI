package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"rai/server/internal/syncing"
)

type rateBudget struct {
	PerMinute int `json:"perMinute"`
	Burst     int `json:"burst"`
}

type ratePolicy struct {
	Device  rateBudget `json:"device"`
	Dataset rateBudget `json:"dataset"`
}

type rateBudgetSet struct {
	Read   ratePolicy `json:"read"`
	Write  ratePolicy `json:"write"`
	Upload ratePolicy `json:"upload"`
}

// ApplyRateBudgets applies an optional deployment-specific admission policy.
// The fixed defaults remain in effect when RAI_RATE_BUDGETS is unset.
func ApplyRateBudgets() error {
	raw := strings.TrimSpace(os.Getenv("RAI_RATE_BUDGETS"))
	if raw == "" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewBufferString(raw))
	dec.DisallowUnknownFields()
	var set rateBudgetSet
	if err := dec.Decode(&set); err != nil {
		return errors.New("invalid rate budget configuration")
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return errors.New("invalid rate budget configuration")
	}
	policies := map[syncing.AdmissionLane]syncing.AdmissionPolicy{}
	for lane, policy := range map[syncing.AdmissionLane]ratePolicy{
		syncing.ReadLane: set.Read, syncing.WriteLane: set.Write, syncing.UploadLane: set.Upload,
	} {
		if policy.Device.PerMinute < 1 || policy.Device.Burst < 1 || policy.Dataset.PerMinute < 1 || policy.Dataset.Burst < 1 ||
			policy.Device.PerMinute > 100000 || policy.Device.Burst > 100000 || policy.Dataset.PerMinute > 100000 || policy.Dataset.Burst > 100000 {
			return errors.New("rate budget values out of range")
		}
		policies[lane] = syncing.AdmissionPolicy{
			Device:  syncing.RequestBudget{PerMinute: policy.Device.PerMinute, Burst: policy.Device.Burst},
			Dataset: syncing.RequestBudget{PerMinute: policy.Dataset.PerMinute, Burst: policy.Dataset.Burst},
		}
	}
	syncing.SetRequestBudgets(policies)
	return nil
}
