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
	"rai/server/internal/syncing"
)

func TestSyntheticPromotionAndRollbackAudits(t *testing.T) {
	f := setup(t)
	api, worker := b4Roles(t, f)
	ctx := context.Background()
	if _, err := f.s.Pool.Exec(ctx, `UPDATE datasets SET synthetic_test=true WHERE id=$1`, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}
	stack, m := modelmanifest.StackID(), modelmanifest.Current()
	manifest, err := modelmanifest.CurrentHash()
	if err != nil {
		t.Fatal(err)
	}
	h := strings.Repeat("b", 64)
	g := am.PromotionGate{Model: m.VO2Max.ModelID, Version: m.VO2Max.Version, ManifestSHA: manifest, ConfigSHA: manifest,
		FeatureSchema: m.Features.Schema, SourcePolicy: m.VO2Max.SourcePolicy, InputRegistry: m.InputRegistry,
		MappingVersion: am.MappingVersion2, ConsumerSchema: am.Schema2, DatasetID: f.p.DatasetID, SyntheticDataset: true,
		EngineeringReport: h, DeviceDataReport: h, ApplicabilityReport: h, UXPrivacyReport: h, RollbackReport: h, Authorization: h,
		Reviewer: "synthetic-reviewer", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
	jobID, err := f.s.PrepareModelStack(ctx, f.p.DatasetID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.PromoteModelStack(ctx, f.p.DatasetID, "synthetic-admin", stack, g, manifest); !errors.Is(err, am.ErrGateClosed) {
		t.Fatalf("unready candidate promotion: %v", err)
	}
	l, err := worker.Claim(ctx, []string{modelmanifest.StackJobKind})
	if err != nil || l.ID != jobID {
		t.Fatalf("claim prepared candidate: %v", err)
	}
	status, err := worker.ProcessModelStack(ctx, l, time.Now())
	if err != nil || status != "SUCCEEDED" {
		t.Fatalf("candidate execution: %q %v", status, err)
	}
	bad := g
	bad.EngineeringReport = strings.Repeat("x", 64)
	if _, err = f.s.PromoteModelStack(ctx, f.p.DatasetID, "synthetic-admin", stack, bad, manifest); !errors.Is(err, am.ErrGateClosed) {
		t.Fatalf("invalid gate promotion: %v", err)
	}
	visible := func(want int) {
		t.Helper()
		var n int
		if err := api.Pool.QueryRow(ctx, `SELECT count(*) FROM analysis_published_artifacts WHERE dataset_id=$1`, f.p.DatasetID).Scan(&n); err != nil || n != want {
			t.Fatalf("visible artifacts=%d want %d: %v", n, want, err)
		}
	}
	visible(0)
	promoteStarted := time.Now()
	a, err := f.s.PromoteModelStack(ctx, f.p.DatasetID, "synthetic-admin", stack, g, manifest)
	if err != nil || a.Action != "promote" || a.FromStack != "legacy" {
		t.Fatal("promote", a, err)
	}
	if elapsed := time.Since(promoteStarted); elapsed > 2*time.Second {
		t.Fatalf("synthetic promote latency %s exceeds 2s", elapsed)
	}
	visible(6)
	handler := httpapi.New(api, analysisLog)
	read := func(want int) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/v2/analysis/athlete", nil)
		r.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("status=%d want %d", w.Code, want)
		}
	}
	read(http.StatusOK)
	if _, err = f.s.Pool.Exec(ctx, `UPDATE datasets SET synthetic_test=false WHERE id=$1`, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.RollbackModelStack(ctx, f.p.DatasetID, "synthetic-admin", manifest); err == nil {
		t.Fatal("unmarked dataset rollback was allowed")
	}
	if _, err = f.s.Pool.Exec(ctx, `UPDATE datasets SET synthetic_test=true WHERE id=$1`, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}
	rollbackStarted := time.Now()
	r, err := f.s.RollbackModelStack(ctx, f.p.DatasetID, "synthetic-admin", manifest)
	if err != nil || r.Action != "rollback" || r.FromStack != stack || r.Revision <= a.Revision {
		t.Fatal("rollback", r, err)
	}
	if elapsed := time.Since(rollbackStarted); elapsed > 2*time.Second {
		t.Fatalf("synthetic rollback latency %s exceeds 2s", elapsed)
	}
	visible(0)
	read(http.StatusConflict)
	var count int
	if err = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM analysis_assignment_audit WHERE dataset_id=$1`, f.p.DatasetID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("assignment audit count=%d: %v", count, err)
	}
}

func TestManagedModelStackPublishesWorkoutDetailAndWithdrawsOnDelete(t *testing.T) {
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
	if _, err = f.s.Pool.Exec(ctx, `UPDATE datasets SET synthetic_test=true WHERE id=$1`, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.s.PrepareModelStack(ctx, f.p.DatasetID); err != nil {
		t.Fatal(err)
	}
	lease, err := worker.Claim(ctx, []string{modelmanifest.StackJobKind})
	if err != nil {
		t.Fatal(err)
	}
	if status, err := worker.ProcessModelStack(ctx, lease, time.Now()); err != nil || status != "SUCCEEDED" {
		t.Fatal("workout and stack publication", status, err)
	}
	detail, err := api.WorkoutDetail(ctx, f.p, bundle.WorkoutID)
	if err != nil || detail.Analysis != nil {
		t.Fatal("candidate detail exposed", err)
	}
	m := modelmanifest.Current()
	manifest, _ := modelmanifest.CurrentHash()
	report := strings.Repeat("a", 64)
	gate := am.PromotionGate{Model: m.VO2Max.ModelID, Version: m.VO2Max.Version, ManifestSHA: manifest, ConfigSHA: manifest,
		FeatureSchema: m.Features.Schema, SourcePolicy: m.VO2Max.SourcePolicy, InputRegistry: m.InputRegistry,
		MappingVersion: am.MappingVersion2, ConsumerSchema: am.Schema2, DatasetID: f.p.DatasetID, SyntheticDataset: true,
		EngineeringReport: report, DeviceDataReport: report, ApplicabilityReport: report, UXPrivacyReport: report, RollbackReport: report, Authorization: report,
		Reviewer: "synthetic", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)}
	if _, err = f.s.PromoteModelStack(ctx, f.p.DatasetID, "synthetic", modelmanifest.StackID(), gate, manifest); err != nil {
		t.Fatal(err)
	}
	detail, err = api.WorkoutDetail(ctx, f.p, bundle.WorkoutID)
	if err != nil || detail.Analysis == nil || detail.Analysis.ActiveSeconds == nil {
		t.Fatal("active detail missing", err)
	}

	if _, err = f.s.DeleteWorkoutBundle(ctx, f.p, syncing.WorkoutDeleteRequest{
		DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch,
		CollectorEpoch: f.enrollment.CollectorEpoch, WorkoutID: bundle.WorkoutID, Reason: "synthetic_delete"}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := api.Pool.QueryRow(ctx, `SELECT count(*) FROM analysis_published_artifacts WHERE dataset_id=$1`, f.p.DatasetID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("deleted evidence still visible: artifacts=%d err=%v", count, err)
	}
}
