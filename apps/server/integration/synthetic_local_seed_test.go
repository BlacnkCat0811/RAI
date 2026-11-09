//go:build integration && syntheticseed

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rai/server/internal/admin"
	"rai/server/internal/analysis"
	am "rai/server/internal/athletemodel"
	diag "rai/server/internal/diagnostics"
	"rai/server/internal/httpapi"
	"rai/server/internal/modelmanifest"
	"rai/server/internal/storage/postgres"
	"rai/server/internal/syncing"
	"rai/server/internal/training"
	wf "rai/server/internal/workoutfeatures"
)

func markCompleteCoverage(b *syncing.WorkoutBundle) {
	if b.Observation == nil {
		return
	}
	b.CompleteRoutes = true
	for _, kind := range syncing.WorkoutCompletenessQuantities {
		count := 0
		for _, sample := range b.Samples {
			if sample.Type == kind {
				count++
			}
		}
		state := "no_records_observed"
		if count > 0 {
			state = "complete"
		}
		b.Observation.Coverage[kind] = syncing.BundleCoverage{State: state, SampleCount: count, QueryCompleted: true, LocalPersisted: true}
		points := 0
		for _, series := range b.Series {
			if series.SampleType == kind {
				points += len(series.Points)
			}
		}
		seriesState := "no_records_observed"
		if points > 0 {
			seriesState = "complete"
		}
		b.Observation.Coverage["series:"+kind] = syncing.BundleCoverage{State: seriesState, SampleCount: points, QueryCompleted: true, LocalPersisted: true}
	}
	state := "no_records_observed"
	if len(b.Routes) > 0 {
		state = "complete"
	}
	points := 0
	for _, route := range b.Routes {
		points += len(route.Points)
	}
	b.Observation.Coverage["route"] = syncing.BundleCoverage{State: state, SampleCount: points, QueryCompleted: true, LocalPersisted: true}
}

// seedRecoveryDays uploads 29 synthetic nights of sleep, HRV and resting heart
// rate in one stream per type. The morning sleep episode ends at 05:30 local so
// every day inside the recovery window is complete and non-pending.
func seedRecoveryDays(t *testing.T, f *fixture) {
	t.Helper()
	ctx := context.Background()
	var tz string
	if err := f.s.Pool.QueryRow(ctx, `SELECT timezone FROM datasets WHERE id=$1`, f.p.DatasetID).Scan(&tz); err != nil {
		t.Fatal(err)
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		t.Fatal(err)
	}
	local := time.Now().In(loc)
	if local.Hour() < 7 {
		t.Fatalf("recovery seed requires local time after 07:00 so the last sleep episode is complete (now %s)", local.Format(time.RFC3339))
	}
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	observed := time.Now().UTC()
	source := syncing.HealthKitSource{BundleID: "com.apple.health", Version: "18.0", ProductType: "Watch7,1",
		OperatingSystemVersion: "18.0", Device: &syncing.HealthKitDevice{Manufacturer: "Apple Inc.", Model: "Watch", HardwareVersion: "Watch7,1", SoftwareVersion: "18.0"}}
	meta := map[string]any{"HKAlgorithmVersion": int64(1), "HKTimeZone": tz}
	bootstrap := today.AddDate(0, 0, -35)
	sleep, hrv, rhr := dailyBatch(f, syncing.DailySleep), dailyBatch(f, syncing.DailyHRV), dailyBatch(f, syncing.DailyRHR)
	for _, b := range []*syncing.DailyBatch{&sleep, &hrv, &rhr} {
		b.BootstrapStartUs = bootstrap.UnixMicro()
		b.ObservedAtUs = observed.UnixMicro()
	}
	fact := func(kind string, start, end time.Time, value float64, category int) syncing.DailyFact {
		x := dailyFact(kind)
		x.StartUs, x.EndUs = start.UnixMicro(), end.UnixMicro()
		x.Source, x.Metadata = source, meta
		if kind == syncing.DailySleep {
			stage := category
			x.Category = &stage
		} else {
			x.Quantity = &syncing.Quantity{Value: value, Unit: syncing.Units[kind]}
		}
		return x
	}
	for n := 28; n >= 0; n-- {
		d := today.AddDate(0, 0, -n)
		night := d.AddDate(0, 0, -1)
		sleep.Facts = append(sleep.Facts,
			fact(syncing.DailySleep, night.Add(21*time.Hour+30*time.Minute), d, 0, 3),
			fact(syncing.DailySleep, d, d.Add(30*time.Minute), 0, 2),
			fact(syncing.DailySleep, d.Add(30*time.Minute), d.Add(5*time.Hour+30*time.Minute), 0, 3))
		hrv.Facts = append(hrv.Facts,
			fact(syncing.DailyHRV, d.Add(90*time.Minute), d.Add(95*time.Minute), 48, 0),
			fact(syncing.DailyHRV, d.Add(2*time.Hour), d.Add(2*time.Hour+5*time.Minute), 52, 0))
		rhr.Facts = append(rhr.Facts, fact(syncing.DailyRHR, d.Add(5*time.Hour+45*time.Minute), d.Add(5*time.Hour+45*time.Minute), 58, 0))
	}
	for _, b := range []*syncing.DailyBatch{&sleep, &hrv, &rhr} {
		requireDaily(t, f, *b)
	}
}

// seedExternalVO2 uploads one Apple Watch estimate and explicitly selects its
// source epoch, activating the independent external VO2 projection.
func seedExternalVO2(t *testing.T, f *fixture, at time.Time) {
	t.Helper()
	ctx := context.Background()
	provenance := syncing.VO2Provenance{
		SchemaVersion: syncing.VO2ProvenanceSchema,
		SourceRevision: syncing.VO2Revision{
			Source:                 syncing.VO2Source{BundleIdentifier: "com.apple.health", Name: "Health"},
			Version:                ptr("11.0"),
			ProductType:            ptr("Watch7,1"),
			OperatingSystemVersion: syncing.VO2OS{Major: 11},
		},
		Device: &syncing.VO2Device{Name: ptr("Apple Watch"), Manufacturer: ptr("Apple Inc."), Model: ptr("Watch"),
			HardwareVersion: ptr("Watch7,1"), FirmwareVersion: ptr("11.0"), SoftwareVersion: ptr("11.0")},
	}
	b := syncing.VO2Batch{
		Contract: syncing.VO2Contract, Version: 1, MetadataRegistryVersion: 1,
		DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch,
		StreamID: syncing.NewID(), BatchID: syncing.NewID(), ClientSeq: 1, SampleType: syncing.VO2Type,
		BootstrapStartUs: at.Add(-91 * 24 * time.Hour).UnixMicro(), QueryCompletedAtUs: at.Add(-time.Hour).UnixMicro(),
		HasMore: false, DeletedUUIDs: []string{},
		Facts: []syncing.VO2Fact{{
			UUID: syncing.NewID(), StartUs: at.Add(-4 * time.Hour).UnixMicro(), EndUs: at.Add(-3 * time.Hour).UnixMicro(),
			Quantity: syncing.Quantity{Value: 45.2, Unit: "mL/kg/min"}, Provenance: provenance,
			Metadata: map[string]syncing.VO2Metadata{
				"HKVO2MaxTestType":                   {Type: "int64", Value: json.RawMessage("2")},
				"HKMetadataKeyAppleDeviceCalibrated": {Type: "boolean", Value: json.RawMessage("true")},
			},
		}},
	}
	b.FlowCreatedAtUs = b.BootstrapStartUs + syncing.VO2WindowUs
	b.QueryFingerprint = syncing.VO2Fingerprint(b.BootstrapStartUs)
	if _, err := commitVO2(t, f, b); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"sourceEpoch": provenance.EpochID(), "revoked": false})
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	effective := at.Add(-3 * time.Hour)
	if _, err = f.s.Pool.Exec(ctx, `INSERT INTO athlete_model_inputs(
 dataset_id,id,kind,subject_key,effective_from,effective_through,supersedes_id,schema_version,content_sha256,
 payload,provenance,revokes_id,input_schema,created_at)
 VALUES($1,$2,'vo2max_source_selection','athlete',$3,NULL,NULL,'rai.vo2max.source_selection/1',$4,
 $5::jsonb,'synthetic-local-debug',NULL,'rai.vo2max.source_selection/1',$3)`,
		f.p.DatasetID, syncing.NewID(), effective, digest[:], payload); err != nil {
		t.Fatal(err)
	}
}

// TestSyntheticLocalSeed populates an empty local database with a synthetic
// dataset and activates the managed model stack with all six product metrics.
// It is excluded from normal integration runs by the syntheticseed tag.
func TestSyntheticLocalSeed(t *testing.T) {
	url := os.Getenv("RAI_SEED_DATABASE_URL")
	out := os.Getenv("RAI_SEED_CONFIG_OUT")
	if url == "" || out == "" {
		t.Fatal("RAI_SEED_DATABASE_URL and RAI_SEED_CONFIG_OUT are required")
	}
	ctx := context.Background()
	s, err := postgres.Open(ctx, url, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Pool.Close()

	var occupied bool
	if err = s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM datasets)`).Scan(&occupied); err != nil {
		t.Fatal(err)
	}
	if occupied {
		t.Fatal("seed requires a database without a dataset")
	}
	enrollment, err := admin.Init(ctx, s.Pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE datasets SET synthetic_test=true WHERE id=$1`, enrollment.DatasetID); err != nil {
		t.Fatal(err)
	}
	p, err := s.Authenticate(ctx, enrollment.Token)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{s: s, enrollment: enrollment, p: p}

	if err = admin.SetAnalysisParameters(ctx, s, p.DatasetID, analysis.Parameters{
		RestHR: 50, MaxHR: 190, A: 0.64, B: 1.92, Provenance: "synthetic-local-debug",
	}); err != nil {
		t.Fatal(err)
	}

	// Seven qualified submax runs over three weeks. The 134 m/min fixture keeps
	// the HR-ratio corners inside the publishable sensitivity width.
	now := time.Now().UTC().Truncate(time.Minute)
	schedule := []struct {
		daysAgo, seconds int
	}{{22, 2400}, {18, 1800}, {15, 2400}, {11, 1800}, {8, 2400}, {5, 1800}, {2, 2400}}
	bundles := make([]syncing.WorkoutBundle, 0, len(schedule))
	for _, item := range schedule {
		end := now.Add(-time.Duration(item.daysAgo) * 24 * time.Hour)
		b := m4Bundle(t, f, end, item.seconds)
		markCompleteCoverage(&b)
		importPhase3(t, f, b)
		known := time.UnixMicro(*b.Workout.EndUs).Add(time.Minute)
		if _, err = s.Pool.Exec(ctx, `UPDATE workouts SET first_received_at=$3,available_at=$3 WHERE dataset_id=$1 AND workout_uuid=$2`,
			p.DatasetID, b.WorkoutID, known); err != nil {
			t.Fatal(err)
		}
		bundles = append(bundles, b)
	}
	for range schedule {
		runOne(t, f, "analysis.workout")
	}
	runOne(t, f, "analysis.training_state")
	through := now.AddDate(0, 0, 1)
	if err = admin.SetFeatureHRReference(ctx, s, p.DatasetID, wf.Parameter{ID: syncing.NewID(),
		EffectiveFrom: now.AddDate(0, 0, -365).UnixMicro(), EffectiveThrough: ptr(through.UnixMicro()),
		RestHR: 50, MaxHR: 200, Provenance: "synthetic-local-debug"}); err != nil {
		t.Fatal(err)
	}
	if _, err = commitCompletenessV2(t, f, completenessManifestV2(f, bundles, nil)); err != nil {
		t.Fatal(err)
	}
	seedRecoveryDays(t, f)
	firstStart := time.UnixMicro(*bundles[0].Workout.StartUs)
	seedNativeHRReference(t, f, firstStart, firstStart.AddDate(0, 0, -60), firstStart.AddDate(0, 0, -40), firstStart.AddDate(0, 0, -39))
	seedExternalVO2(t, f, now)

	if _, err = s.PrepareModelStack(ctx, p.DatasetID); err != nil {
		t.Fatal(err)
	}
	l, err := s.Claim(ctx, []string{modelmanifest.StackJobKind})
	if err != nil {
		t.Fatal(err)
	}
	if status, err := s.ProcessModelStack(ctx, l, time.Now()); status != "SUCCEEDED" || err != nil {
		t.Fatalf("model stack worker status=%s err=%v", status, err)
	}
	m := modelmanifest.Current()
	hash, err := modelmanifest.CurrentHash()
	if err != nil {
		t.Fatal(err)
	}
	report := strings.Repeat("c", 64)
	gate := am.PromotionGate{Model: m.VO2Max.ModelID, Version: m.VO2Max.Version, ManifestSHA: hash, ConfigSHA: hash,
		FeatureSchema: m.Features.Schema, SourcePolicy: m.VO2Max.SourcePolicy, InputRegistry: m.InputRegistry,
		MappingVersion: am.MappingVersion2, ConsumerSchema: am.Schema2, DatasetID: p.DatasetID, SyntheticDataset: true,
		EngineeringReport: report, DeviceDataReport: report, ApplicabilityReport: report, UXPrivacyReport: report,
		RollbackReport: report, Authorization: report, Reviewer: "synthetic-local",
		ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)}
	if _, err = s.PromoteModelStack(ctx, p.DatasetID, "synthetic-local-debug", modelmanifest.StackID(), gate, hash); err != nil {
		t.Fatal(err)
	}

	if _, err = s.CreateTrainingPlan(ctx, p, training.CreateRequest{CommandID: syncing.NewID(), PlanID: syncing.NewID(), Config: trainingConfig()}); err != nil {
		t.Fatal(err)
	}
	runTraining(t, f)
	plan, err := s.TrainingPlan(ctx, p, "")
	if err != nil || plan.Effective == nil {
		t.Fatalf("training plan not published: err=%v effective=%v", err, plan.Effective)
	}

	handler := httpapi.New(s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	server := httptest.NewServer(handler)
	defer server.Close()
	read := func(path string) (int, []byte) {
		request, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+enrollment.Token)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, raw
	}
	code, raw := read("/v2/analysis/athlete")
	if code != 200 {
		t.Fatalf("athlete product status=%d %s", code, raw)
	}
	var snapshot am.AthleteModelSnapshot
	if err = json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	states := map[string]am.ProductMetricState{
		"trainingLoad":          snapshot.TrainingLoad.Envelope.State,
		"physiologicalRecovery": snapshot.PhysiologicalRecovery.Envelope.State,
		"trainingReadiness":     snapshot.TrainingReadiness.Envelope.State,
		"runningAbility":        snapshot.RunningAbility.Envelope.State,
		"externalVO2Max":        snapshot.ExternalVO2Max.Envelope.State,
	}
	if snapshot.RAIVO2Max != nil {
		states["raiVO2Max"] = snapshot.RAIVO2Max.Envelope.State
	}
	if snapshot.SchemaVersion != am.Schema2 || snapshot.ResponseState != "complete_response" || len(states) != 6 {
		t.Fatalf("product response=%s states=%v", snapshot.ResponseState, states)
	}
	for name, state := range states {
		if state != am.Available {
			t.Fatalf("%s state=%s reasons=%v", name, state, snapshot.TrainingLoad.Envelope.ReasonCodes)
		}
	}
	if snapshot.TrainingLoad.Value == nil || snapshot.TrainingLoad.Value.Acute == nil {
		t.Fatalf("training load value missing: %+v", snapshot.TrainingLoad.Value)
	}
	if snapshot.PhysiologicalRecovery.Value == nil || snapshot.PhysiologicalRecovery.Value.Score == nil || *snapshot.PhysiologicalRecovery.Value.Score < 99 {
		t.Fatalf("recovery value: %+v", snapshot.PhysiologicalRecovery.Value)
	}
	if snapshot.TrainingReadiness.Value == nil || snapshot.TrainingReadiness.Value.Score == nil {
		t.Fatalf("readiness value: %+v", snapshot.TrainingReadiness.Value)
	}
	if snapshot.RunningAbility.Value == nil || snapshot.RunningAbility.Value.Score == nil {
		t.Fatalf("running ability value: %+v", snapshot.RunningAbility.Value)
	}
	if snapshot.RAIVO2Max == nil || snapshot.RAIVO2Max.Value == nil || snapshot.RAIVO2Max.Value.Value == nil ||
		*snapshot.RAIVO2Max.Value.Value < 30 || snapshot.RAIVO2Max.Value.SensitivityWidth == nil || *snapshot.RAIVO2Max.Value.SensitivityWidth > .30 {
		t.Fatalf("rai VO2 value: %+v", snapshot.RAIVO2Max)
	}
	if snapshot.ExternalVO2Max.Value == nil || snapshot.ExternalVO2Max.Value.Value == nil || math.Abs(*snapshot.ExternalVO2Max.Value.Value-45.2) > 1e-9 {
		t.Fatalf("external VO2 value: %+v", snapshot.ExternalVO2Max.Value)
	}
	t.Logf("STATES load=%s recovery=%.1f readiness=%.1f ability=%.1f raiVO2=%.2f externalVO2=%.1f response=%s",
		states["trainingLoad"], *snapshot.PhysiologicalRecovery.Value.Score, *snapshot.TrainingReadiness.Value.Score,
		*snapshot.RunningAbility.Value.Score, *snapshot.RAIVO2Max.Value.Value, *snapshot.ExternalVO2Max.Value.Value, snapshot.ResponseState)
	code, raw = read("/v2/analysis/athlete/diagnostics")
	if code != 200 {
		t.Fatalf("diagnostics status=%d %s", code, raw)
	}
	var diagnostics diag.ProductSnapshot
	if err = json.Unmarshal(raw, &diagnostics); err != nil {
		t.Fatal(err)
	}
	code, raw = read("/v2/workouts")
	if code != 200 {
		t.Fatalf("workouts status=%d %s", code, raw)
	}
	var page struct {
		Items []json.RawMessage `json:"items"`
	}
	if err = json.Unmarshal(raw, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != len(schedule) {
		t.Fatalf("workouts=%d want %d", len(page.Items), len(schedule))
	}
	code, raw = read("/v2/training/plans/current")
	if code != 200 {
		t.Fatalf("training plan status=%d %s", code, raw)
	}
	var current struct {
		Effective json.RawMessage `json:"effective"`
	}
	if err = json.Unmarshal(raw, &current); err != nil || len(current.Effective) == 0 {
		t.Fatalf("training plan effective missing: err=%v %s", err, raw)
	}

	secret, err := json.Marshal(map[string]any{
		"configuration": map[string]string{"baseURL": "http://127.0.0.1:8080", "collectorEpoch": enrollment.CollectorEpoch},
		"token":         enrollment.Token,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(out), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(out, secret, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("SEEDED dataset=%s collector_epoch=%s workouts=%d diagnostics=%d plan=%s", p.DatasetID, enrollment.CollectorEpoch, len(page.Items), len(diagnostics.Items), plan.ID)
}
