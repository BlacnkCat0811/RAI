//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	am "rai/server/internal/athletemodel"
	"rai/server/internal/httpapi"
	"rai/server/internal/modelmanifest"
)

func ownerAuthorization(dataset, stack, manifest string) am.OwnerAuthorization {
	now := time.Now().UTC()
	return am.OwnerAuthorization{
		Schema:         am.OwnerAuthorizationSchema,
		DatasetID:      dataset,
		StackID:        stack,
		ManifestSHA256: manifest,
		Scope:          am.OwnerAuthorizationScope,
		AuthorizedBy:   "integration-owner",
		AuthorizedAt:   now.Add(-time.Minute).Format(time.RFC3339),
		ExpiresAt:      now.Add(time.Hour).Format(time.RFC3339),
		Statement:      am.OwnerAuthorizationPhrase,
	}
}

func TestOwnerAuthorizedRealDatasetActivationAndRollback(t *testing.T) {
	f := setup(t)
	api, worker := b4Roles(t, f)
	ctx := context.Background()
	server := httptest.NewServer(httpapi.New(api, analysisLog))
	defer server.Close()
	bundle := featureBundle(t, f, 35*60)
	body, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = api.ImportWorkoutBundle(ctx, f.p, bundle, body); err != nil {
		t.Fatalf("upload: %v", err)
	}
	stack := modelmanifest.StackID()
	manifest, err := modelmanifest.CurrentHash()
	if err != nil {
		t.Fatal(err)
	}
	authorization := ownerAuthorization(f.p.DatasetID, stack, manifest)
	raw, err := json.Marshal(authorization)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.PrepareModelStack(ctx, f.p.DatasetID); !errors.Is(err, am.ErrGateClosed) {
		t.Fatalf("unauthorized real prepare: %v", err)
	}
	bad := authorization
	bad.ManifestSHA256 = strings.Repeat("0", 64)
	badRaw, _ := json.Marshal(bad)
	if _, err = f.s.PrepareModelStackAuthorized(ctx, f.p.DatasetID, badRaw); !errors.Is(err, am.ErrOwnerAuthorizationInvalid) {
		t.Fatalf("mismatched authorization prepare: %v", err)
	}
	jobID, err := f.s.PrepareModelStackAuthorized(ctx, f.p.DatasetID, raw)
	if err != nil {
		t.Fatal(err)
	}
	l, err := worker.Claim(ctx, []string{modelmanifest.StackJobKind})
	if err != nil || l.ID != jobID {
		t.Fatalf("claim prepared candidate: %v", err)
	}
	if status, err := worker.ProcessModelStack(ctx, l, time.Now()); err != nil || status != "SUCCEEDED" {
		t.Fatalf("candidate execution: %q %v", status, err)
	}
	read := func(want int) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/v2/analysis/athlete", nil)
		r.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
		w := httptest.NewRecorder()
		apiHTTP := httpapi.New(api, analysisLog)
		apiHTTP.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("status=%d want %d body=%s", w.Code, want, w.Body.String())
		}
	}
	read(http.StatusConflict)
	if _, err = f.s.PromoteModelStackAuthorized(ctx, f.p.DatasetID, "owner", stack, manifest, badRaw); !errors.Is(err, am.ErrOwnerAuthorizationInvalid) {
		t.Fatalf("mismatched authorization promote: %v", err)
	}
	if _, err = f.s.PromoteModelStack(ctx, f.p.DatasetID, "owner", stack, am.PromotionGate{DatasetID: f.p.DatasetID}, manifest); !errors.Is(err, am.ErrGateClosed) {
		t.Fatalf("synthetic gate on real dataset: %v", err)
	}
	promoted, err := f.s.PromoteModelStackAuthorized(ctx, f.p.DatasetID, "owner", stack, manifest, raw)
	if err != nil || promoted.Action != "promote" || promoted.FromStack != "legacy" {
		t.Fatal("authorized promote", promoted, err)
	}
	var summary map[string]any
	if err = json.Unmarshal(promoted.GateSummary, &summary); err != nil || summary["ownerAuthorized"] != true || summary["standardGatesVerified"] != false {
		t.Fatalf("authorization audit summary=%v err=%v", summary, err)
	}
	read(http.StatusOK)
	if _, err = f.s.RollbackModelStack(ctx, f.p.DatasetID, "owner", manifest); !errors.Is(err, am.ErrGateClosed) {
		t.Fatalf("unauthorized real rollback: %v", err)
	}
	rolled, err := f.s.RollbackModelStackAuthorized(ctx, f.p.DatasetID, "owner", manifest, raw)
	if err != nil || rolled.Action != "rollback" || rolled.FromStack != stack || rolled.ToStack != "legacy" {
		t.Fatal("authorized rollback", rolled, err)
	}
	read(http.StatusConflict)
	var audits int
	if err = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM analysis_assignment_audit WHERE dataset_id=$1`, f.p.DatasetID).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("assignment audits=%d err=%v", audits, err)
	}
}
