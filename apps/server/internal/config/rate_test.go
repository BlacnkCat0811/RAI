package config

import (
	"testing"

	"rai/server/internal/syncing"
)

func TestApplyRateBudgets(t *testing.T) {
	t.Cleanup(func() { syncing.SetRequestBudgets(nil) })
	t.Setenv("RAI_RATE_BUDGETS", "")
	if err := ApplyRateBudgets(); err != nil {
		t.Fatal(err)
	}
	if got := syncing.RequestBudgets()[syncing.UploadLane].Device.PerMinute; got != 30 {
		t.Fatalf("default upload per minute = %d", got)
	}
	t.Setenv("RAI_RATE_BUDGETS", `{"read":{"device":{"perMinute":600,"burst":60},"dataset":{"perMinute":1200,"burst":120}},"write":{"device":{"perMinute":300,"burst":30},"dataset":{"perMinute":600,"burst":60}},"upload":{"device":{"perMinute":300,"burst":20},"dataset":{"perMinute":600,"burst":40}}}`)
	if err := ApplyRateBudgets(); err != nil {
		t.Fatal(err)
	}
	if got := syncing.RequestBudgets()[syncing.UploadLane].Device.PerMinute; got != 300 {
		t.Fatalf("override upload per minute = %d", got)
	}
	t.Setenv("RAI_RATE_BUDGETS", `{"read":{"device":{"perMinute":0,"burst":1}}}`)
	if err := ApplyRateBudgets(); err == nil {
		t.Fatal("invalid values accepted")
	}
}
