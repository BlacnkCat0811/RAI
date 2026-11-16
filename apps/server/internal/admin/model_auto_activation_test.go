package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"rai/server/internal/storage/postgres"
)

type fakeActivator struct {
	datasets []string
	steps    map[string][]string // per dataset, the status of each successive call
	calls    map[string]int
	fail     map[string]bool
	actors   []string
}

func (f *fakeActivator) AutoActivationDatasets(context.Context) ([]string, error) {
	return f.datasets, nil
}

func (f *fakeActivator) AutoActivateModelStack(_ context.Context, id, actor string) (postgres.AutoActivation, error) {
	f.actors = append(f.actors, actor)
	if f.fail[id] {
		return postgres.AutoActivation{}, errors.New("db")
	}
	steps := f.steps[id]
	n := f.calls[id]
	f.calls[id]++
	if n >= len(steps) {
		n = len(steps) - 1
	}
	return postgres.AutoActivation{Status: steps[n], ToStack: "new"}, nil
}

func decodeResults(t *testing.T, b []byte) []autoActivationResult {
	t.Helper()
	var r []autoActivationResult
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAutoActivateWaitsForPendingThenActivates(t *testing.T) {
	f := &fakeActivator{datasets: []string{"a", "b"}, calls: map[string]int{},
		steps: map[string][]string{"a": {"pending", "pending", "activated"}, "b": {"already_active"}}}
	var out bytes.Buffer
	if err := autoActivate(context.Background(), f, time.Minute, time.Millisecond, &out); err != nil {
		t.Fatal(err)
	}
	if f.calls["a"] != 3 || f.calls["b"] != 1 {
		t.Fatalf("calls %+v: finished datasets must not be retried", f.calls)
	}
	for _, a := range f.actors {
		if a != autoActivationActor {
			t.Fatalf("actor %q", a)
		}
	}
	r := decodeResults(t, out.Bytes())
	if len(r) != 2 || r[0].Status != "activated" || r[1].Status != "already_active" {
		t.Fatalf("results %+v", r)
	}
	if strings.Contains(out.String(), `"a"`) || len(r[0].Dataset) != 16 {
		t.Fatalf("dataset identifiers must be hashed: %s", out.String())
	}
}

func TestAutoActivateFailsWhenNotReadyBeforeTimeout(t *testing.T) {
	f := &fakeActivator{datasets: []string{"a"}, calls: map[string]int{}, steps: map[string][]string{"a": {"pending"}}}
	var out bytes.Buffer
	err := autoActivate(context.Background(), f, 5*time.Millisecond, time.Millisecond, &out)
	if err == nil || decodeResults(t, out.Bytes())[0].Status != "pending" {
		t.Fatalf("err=%v out=%s", err, out.String())
	}
}

func TestAutoActivateStopsOnFinalFailures(t *testing.T) {
	for _, status := range []string{"candidate_failed", "manual_rollback_required", "history_backfill_paused"} {
		f := &fakeActivator{datasets: []string{"a"}, calls: map[string]int{}, steps: map[string][]string{"a": {status}}}
		var out bytes.Buffer
		if err := autoActivate(context.Background(), f, time.Hour, time.Millisecond, &out); err == nil {
			t.Fatalf("%s must fail", status)
		}
		if f.calls["a"] != 1 {
			t.Fatalf("%s must not be retried: %d calls", status, f.calls["a"])
		}
	}
}

func TestAutoActivateReportsRefusalWithoutDetails(t *testing.T) {
	f := &fakeActivator{datasets: []string{"a"}, calls: map[string]int{}, fail: map[string]bool{"a": true}}
	var out bytes.Buffer
	if err := autoActivate(context.Background(), f, 5*time.Millisecond, time.Millisecond, &out); err == nil {
		t.Fatal("refusal must fail")
	}
	if r := decodeResults(t, out.Bytes()); r[0].Error != "activation_refused" {
		t.Fatalf("results %+v", r)
	}
}
