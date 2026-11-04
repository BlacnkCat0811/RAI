//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"rai/server/internal/admin"
	"rai/server/internal/analysis"
	"rai/server/internal/jobs"
	"rai/server/internal/syncing"
	"rai/server/internal/training"
	ve "rai/server/internal/vo2maxexternal"
	"testing"
	"time"
)

func externalFixture(t *testing.T) (*fixture, syncing.VO2Batch, ve.Selection) {
	t.Helper()
	f := setup(t)
	b := externalBatch(t, f)
	at := b.FlowCreatedAtUs
	x := &b.Facts[0]
	requireVO2(t, f, b)
	sel := ve.Selection{ID: syncing.NewID(), Epoch: x.Provenance.EpochID(), EffectiveFrom: at}
	if e := admin.SetVO2SourceSelection(context.Background(), f.s, f.p.DatasetID, syncing.NewID(), "synthetic external", sel); e != nil {
		t.Fatal(e)
	}
	if e := admin.QueueVO2ExternalCandidate(context.Background(), f.s, f.p.DatasetID, syncing.NewID(), "synthetic external"); e != nil {
		t.Fatal(e)
	}
	return f, b, sel
}
func externalBatch(t *testing.T, f *fixture) syncing.VO2Batch {
	t.Helper()
	b := vo2Batch(t, f)
	at := time.Now().UTC().Add(-time.Hour).UnixMicro()
	b.FlowCreatedAtUs = at
	b.BootstrapStartUs = at - syncing.VO2WindowUs
	b.QueryCompletedAtUs = at + 1
	b.QueryFingerprint = syncing.VO2Fingerprint(b.BootstrapStartUs)
	x := &b.Facts[0]
	x.StartUs = at - int64(24*time.Hour/time.Microsecond)
	x.EndUs = x.StartUs
	x.Quantity.Value = 42
	product, manufacturer, model := "Watch6,1", "Apple", "Watch"
	x.Provenance.SourceRevision.Source.BundleIdentifier = "com.apple.health"
	x.Provenance.SourceRevision.ProductType = &product
	x.Provenance.Device = &syncing.VO2Device{Manufacturer: &manufacturer, Model: &model}
	return b
}
func computeExternal(t *testing.T, f *fixture, l analysis.Lease) (analysis.Snapshot, string, []analysis.Metric) {
	t.Helper()
	ctx := context.Background()
	in, e := f.s.LoadAnalysis(ctx, l, time.Now().UTC())
	if e != nil {
		t.Fatal(e)
	}
	id, e := f.s.StartAnalysisRun(ctx, l, in)
	if e != nil {
		t.Fatal(e)
	}
	ms, e := analysis.CalculateCandidateVO2External(&in)
	if e != nil {
		t.Fatal(e)
	}
	return in, id, ms
}
func externalRead(t *testing.T, f *fixture, status string, value *float64) (ve.Result, string) {
	t.Helper()
	r, id, e := f.s.CandidateVO2External(context.Background(), f.p.DatasetID, time.Now().UTC())
	if e != nil || r.Status != status || (r.Value == nil) != (value == nil) {
		t.Fatalf("candidate status=%s value=%v err=%v", r.Status, r.Value != nil, e)
	}
	if value != nil && *r.Value != *value {
		t.Fatal("changed raw external value")
	}
	return r, id
}
func TestVO2ExternalLifecycleCandidateIsolation(t *testing.T) {
	f, b, _ := externalFixture(t)
	ctx := context.Background()
	runOne(t, f, ve.JobKind)
	value := 42.0
	r, id := externalRead(t, f, "available", &value)
	if r.Provider != "apple_health" || r.Confidence != nil || r.ParameterVersion != nil || r.FeatureVersion != nil || r.Quality != "partial" {
		t.Fatal("wrong model identity/quality")
	}
	stale, _, e := f.s.CandidateVO2External(ctx, f.p.DatasetID, time.UnixMicro(r.Selected.End+31*ve.DayUs))
	if e != nil || stale.Status != "stale" || stale.Value == nil {
		t.Fatal("read stale", e)
	}
	expired, _, e := f.s.CandidateVO2External(ctx, f.p.DatasetID, time.UnixMicro(r.Selected.End+90*ve.DayUs))
	if e != nil || expired.Status != "expired" || expired.Value != nil {
		t.Fatal("read expired", e)
	}
	var active string
	var polluted int
	if e = f.s.Pool.QueryRow(ctx, `SELECT stack_id FROM analysis_model_channels WHERE dataset_id=$1 AND channel='active'`, f.p.DatasetID).Scan(&active); e != nil || active != "legacy" {
		t.Fatal("activated candidate")
	}
	if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE dataset_id=$1 AND kind<>$2`, f.p.DatasetID, ve.JobKind).Scan(&polluted); e != nil || polluted != 0 {
		t.Fatal("other model enqueued", e)
	}
	// M3-looking-high evidence and independent future RAI family can coexist;
	// this fixture supplies no executable estimated handler or parameter path.
	if _, e = f.s.Pool.Exec(ctx, `INSERT INTO analysis_results(dataset_id,run_id,day,metric,algorithm_name,algorithm_version,value,unit,quality) VALUES($1,$2,'0001-01-01','synthetic_future_rai_vo2','synthetic_fixture','v1',55,'mL/kg/min','partial')`, f.p.DatasetID, id); e != nil {
		t.Fatal(e)
	}
	externalRead(t, f, "available", &value)
	del := nextVO2(b)
	del.DeletedUUIDs = []string{b.Facts[0].UUID}
	requireVO2(t, f, del)
	if _, _, e = f.s.CandidateVO2External(ctx, f.p.DatasetID, time.Now().UTC()); e == nil {
		t.Fatal("deleted selected sample remains current")
	}
	runOne(t, f, ve.JobKind)
	externalRead(t, f, "deleted", nil)
	historical, withdrawn, e := f.s.HistoricalVO2External(ctx, f.p.DatasetID, id)
	if e != nil || !withdrawn || historical.Value == nil || *historical.Value != 42 {
		t.Fatal("immutable withdrawn history", e)
	}
	if e = f.s.LoadHistoricalVO2External(ctx, f.p.DatasetID, r.AsOf); e != ve.ErrHistorical {
		t.Fatal("fake as-known history")
	}
}
func TestVO2ExternalFirstPublicationRaces(t *testing.T) {
	for _, change := range []string{"delete_after_snapshot", "delete_before_publication_lock", "new_fact", "selection_switch", "selection_revoke", "config", "assignment", "collector", "device", "lease", "lease_takeover"} {
		t.Run(change, func(t *testing.T) {
			f, b, sel := externalFixture(t)
			ctx := context.Background()
			l := claimAnalysis(t, f, ve.JobKind)
			in, id, ms := computeExternal(t, f, l)
			switch change {
			case "delete_after_snapshot", "delete_before_publication_lock":
				d := nextVO2(b)
				d.DeletedUUIDs = []string{b.Facts[0].UUID}
				requireVO2(t, f, d)
			case "new_fact":
				d := nextVO2(b)
				x := b.Facts[0]
				x.UUID = syncing.NewID()
				x.EndUs++
				d.Facts = []syncing.VO2Fact{x}
				requireVO2(t, f, d)
			case "selection_switch", "selection_revoke":
				sel.Supersedes = sel.ID
				sel.ID = syncing.NewID()
				if change == "selection_switch" {
					sel.Epoch = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
				} else {
					sel.Revoked = true
				}
				if e := admin.SetVO2SourceSelection(ctx, f.s, f.p.DatasetID, syncing.NewID(), "synthetic change", sel); e != nil {
					t.Fatal(e)
				}
			case "config":
				if _, e := f.s.Pool.Exec(ctx, `UPDATE analysis_model_channels SET extension_configs=jsonb_set(extension_configs,ARRAY[$2,'configHash'],to_jsonb(repeat('b',64))) WHERE dataset_id=$1 AND channel='candidate'`, f.p.DatasetID, ve.Model); e != nil {
					t.Fatal(e)
				}
			case "assignment":
				if _, e := f.s.Pool.Exec(ctx, `UPDATE analysis_model_channels SET extension_configs=jsonb_set(extension_configs,ARRAY[$2,'assignmentID'],to_jsonb($3::text)) WHERE dataset_id=$1 AND channel='candidate'`, f.p.DatasetID, ve.Model, syncing.NewID()); e != nil {
					t.Fatal(e)
				}
			case "collector":
				if _, e := f.s.Pool.Exec(ctx, `UPDATE collector_leases SET collector_epoch=$2 WHERE dataset_id=$1`, f.p.DatasetID, syncing.NewID()); e != nil {
					t.Fatal(e)
				}
			case "device":
				if _, e := f.s.Pool.Exec(ctx, `UPDATE devices SET revoked_at=clock_timestamp() WHERE dataset_id=$1 AND id=$2`, f.p.DatasetID, f.p.DeviceID); e != nil {
					t.Fatal(e)
				}
			case "lease", "lease_takeover":
				if _, e := f.s.Pool.Exec(ctx, `UPDATE jobs SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, l.ID); e != nil {
					t.Fatal(e)
				}
				if change == "lease_takeover" {
					if _, e := f.s.RequeueExpired(ctx); e != nil {
						t.Fatal(e)
					}
					if _, e := f.s.Pool.Exec(ctx, `UPDATE jobs SET run_after=clock_timestamp() WHERE id=$1`, l.ID); e != nil {
						t.Fatal(e)
					}
					newLease := claimAnalysis(t, f, ve.JobKind)
					jobs.Process(ctx, f.s, newLease, analysisLog)
				}
			}
			status, e := f.s.PublishAnalysis(ctx, l, id, in, ms)
			if change == "lease" || change == "lease_takeover" {
				if e == nil {
					t.Fatal("expired lease published")
				}
			} else if e != nil || status != "SUPERSEDED" {
				t.Fatal("stale snapshot published", status, e)
			}
			var n int
			if e = f.s.Pool.QueryRow(ctx, `SELECT count(*) FROM analysis_artifacts WHERE dataset_id=$1 AND run_id=$2`, f.p.DatasetID, id).Scan(&n); e != nil || n != 0 {
				t.Fatal("obsolete artifact published", e)
			}
		})
	}
}
func TestVO2ExternalOperationRetryAndNullPublication(t *testing.T) {
	f, b, sel := externalFixture(t)
	ctx := context.Background()
	op := syncing.NewID()
	for i := 0; i < 2; i++ {
		if e := admin.QueueVO2ExternalCandidate(ctx, f.s, f.p.DatasetID, op, "synthetic retry"); e != nil {
			t.Fatal(e)
		}
	}
	var before int64
	if e := f.s.Pool.QueryRow(ctx, `SELECT generation FROM dirty_scopes WHERE dataset_id=$1 AND scope_key=$2`, f.p.DatasetID, ve.Scope).Scan(&before); e != nil {
		t.Fatal(e)
	}
	op = syncing.NewID()
	sel.ID = syncing.NewID()
	for i := 0; i < 2; i++ {
		if e := admin.SetVO2SourceSelection(ctx, f.s, f.p.DatasetID, op, "synthetic retry", sel); e != nil {
			t.Fatal(e)
		}
	}
	var after int64
	if e := f.s.Pool.QueryRow(ctx, `SELECT generation FROM dirty_scopes WHERE dataset_id=$1 AND scope_key=$2`, f.p.DatasetID, ve.Scope).Scan(&after); e != nil || after != before+1 {
		t.Fatal("selection retry bumped twice", e)
	}
	runOne(t, f, ve.JobKind)
	externalRead(t, f, "source_selection_required", nil)
	// Replay immutable B2 ACK cannot bump external generation or resurrect facts.
	requireVO2(t, f, b)
	var replay int64
	if e := f.s.Pool.QueryRow(ctx, `SELECT generation FROM dirty_scopes WHERE dataset_id=$1 AND scope_key=$2`, f.p.DatasetID, ve.Scope).Scan(&replay); e != nil || replay != after {
		t.Fatal("B2 replay changed generation", e)
	}
}
func TestVO2ExternalGoldenDatabaseCases(t *testing.T) {
	for _, tt := range []struct {
		name, status string
		mutate       func(*syncing.VO2Batch)
	}{
		{"pending", "sync_incomplete", func(b *syncing.VO2Batch) { b.HasMore = true }},
		{"stale", "stale", func(b *syncing.VO2Batch) { b.Facts[0].StartUs -= 31 * ve.DayUs; b.Facts[0].EndUs = b.Facts[0].StartUs }},
		{"future", "invalid_input", func(b *syncing.VO2Batch) { b.Facts[0].EndUs = time.Now().Add(24 * time.Hour).UnixMicro() }},
		{"third party", "source_selection_required", func(b *syncing.VO2Batch) {
			b.Facts[0].Provenance.SourceRevision.Source.BundleIdentifier = "org.synthetic.external"
		}},
		{"manual", "deleted", func(b *syncing.VO2Batch) {
			b.Facts[0].Metadata["HKWasUserEntered"] = syncing.VO2Metadata{Type: "boolean", Value: json.RawMessage(`true`)}
		}},
		{"maxExercise", "deleted", func(b *syncing.VO2Batch) {
			b.Facts[0].Metadata["HKVO2MaxTestType"] = syncing.VO2Metadata{Type: "int64", Value: json.RawMessage(`1`)}
		}},
		{"same time conflict", "conflicting_data", func(b *syncing.VO2Batch) {
			x := b.Facts[0]
			x.UUID = syncing.NewID()
			x.Quantity.Value = 55
			b.Facts = append(b.Facts, x)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f, b, sel := externalFixture(t)
			d := nextVO2(b)
			d.DeletedUUIDs = []string{b.Facts[0].UUID}
			requireVO2(t, f, d)
			d = nextVO2(d)
			d.Facts = []syncing.VO2Fact{b.Facts[0]}
			d.Facts[0].UUID = syncing.NewID()
			tt.mutate(&d)
			requireVO2(t, f, d)
			if d.Facts[0].Provenance.EpochID() != sel.Epoch {
				old := sel.ID
				sel.ID = syncing.NewID()
				sel.Supersedes = old
				sel.Epoch = d.Facts[0].Provenance.EpochID()
				if e := admin.SetVO2SourceSelection(context.Background(), f.s, f.p.DatasetID, syncing.NewID(), "synthetic source", sel); e != nil {
					t.Fatal(e)
				}
			}
			runOne(t, f, ve.JobKind)
			var v *float64
			if tt.status == "stale" {
				x := 42.0
				v = &x
			}
			externalRead(t, f, tt.status, v)
		})
	}
}

func TestVO2ExternalSnapshotRepeatableRead(t *testing.T) {
	f, b, _ := externalFixture(t)
	ctx := context.Background()
	l := claimAnalysis(t, f, ve.JobKind)
	block, e := f.s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer block.Rollback(ctx)
	if _, e = block.Exec(ctx, `LOCK TABLE health_samples IN ACCESS EXCLUSIVE MODE`); e != nil {
		t.Fatal(e)
	}
	type outcome struct {
		in  analysis.Snapshot
		err error
	}
	done := make(chan outcome, 1)
	go func() { in, e := f.s.LoadAnalysis(ctx, l, time.Now().UTC()); done <- outcome{in, e} }()
	waiting := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if e = f.s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'SELECT sample_uuid::text,event_start,%')`).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !waiting {
		t.Fatal("loader did not reach snapshot barrier")
	}
	d := nextVO2(b)
	d.QueryCompletedAtUs++
	requireVO2(t, f, d)
	if e = block.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	got := <-done
	if got.err != nil || got.in.Generation != l.TargetGeneration || got.in.ExternalInput == nil || got.in.ExternalInput.Coverage.Sequence != 1 {
		t.Fatal("mixed generation/coverage snapshot", got.err)
	}
}
func TestVO2ExternalOtherModelsDoNotDirtyOrAdjust(t *testing.T) {
	f, _, _ := externalFixture(t)
	ctx := context.Background()
	runOne(t, f, ve.JobKind)
	v := 42.0
	r, id := externalRead(t, f, "available", &v)
	b := m3Bundle(t, f, time.Now().Add(-48*time.Hour), 1200)
	importPhase3(t, f, b)
	var features, externalJobs int
	if e := f.s.Pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE kind='analysis.workout_features'),count(*) FILTER(WHERE kind=$2) FROM jobs WHERE dataset_id=$1`, f.p.DatasetID, ve.JobKind).Scan(&features, &externalJobs); e != nil || features != 0 || externalJobs != 1 {
		t.Fatal("external enrollment/dependency pollution", e, features, externalJobs)
	}
	if _, e := f.s.Pool.Exec(ctx, `INSERT INTO analysis_parameter_sets(dataset_id,version,resting_hr,max_hr,coefficient_a,coefficient_b,provenance) VALUES($1,1,60,190,1,1,'synthetic HR')`, f.p.DatasetID); e != nil {
		t.Fatal(e)
	}
	got, gotID := externalRead(t, f, "available", &v)
	if gotID != id || got.Generation != r.Generation {
		t.Fatal("HR/workout changed external")
	}
	// Owner can explicitly enroll M1 afterwards without rewriting external entry.
	if e := admin.QueueFeatureCandidate(ctx, f.s, f.p.DatasetID, b.WorkoutID, syncing.NewID(), "synthetic explicit M1"); e != nil {
		t.Fatal(e)
	}
	_, gotID = externalRead(t, f, "available", &v)
	if gotID != id {
		t.Fatal("M1 enrollment changed external")
	}
}

func TestVO2ExternalCandidateDoesNotLeakProductDTOs(t *testing.T) {
	f, _, _ := externalFixture(t)
	ctx := context.Background()
	runOne(t, f, ve.JobKind)
	b := m3Bundle(t, f, time.Now().Add(-48*time.Hour), 1200)
	importPhase3(t, f, b)
	values := []any{}
	home, e := f.s.ProductOverview(ctx, f.p)
	if e != nil {
		t.Fatal(e)
	}
	values = append(values, home)
	metrics, e := f.s.ProductHistory(ctx, f.p, 14)
	if e != nil {
		t.Fatal(e)
	}
	values = append(values, metrics)
	workouts, e := f.s.ProductWorkouts(ctx, f.p, "", 10, "")
	if e != nil {
		t.Fatal(e)
	}
	values = append(values, workouts)
	detail, e := f.s.WorkoutDetail(ctx, f.p, b.WorkoutID)
	if e != nil {
		t.Fatal(e)
	}
	values = append(values, detail)
	plan, e := f.s.CreateTrainingPlan(ctx, f.p, training.CreateRequest{CommandID: syncing.NewID(), PlanID: syncing.NewID(), Config: trainingConfig()})
	if e != nil {
		t.Fatal(e)
	}
	savedPlan, e := f.s.TrainingPlan(ctx, f.p, plan.ID)
	if e != nil {
		t.Fatal(e)
	}
	values = append(values, savedPlan)
	// Training generation reads only the legacy training-state family. A current
	// candidate must not make the generic state reader succeed by accident.
	if current, e := f.s.CurrentTrainingState(ctx, f.p); e == nil {
		values = append(values, current)
	}
	for _, v := range values {
		raw, _ := json.Marshal(v)
		for _, private := range []string{ve.Model, ve.Metric, ve.Family, ve.ArtifactKey} {
			if bytes.Contains(raw, []byte(private)) {
				t.Fatal("candidate leaked to product")
			}
		}
	}
}

func TestVO2ExternalAdminPrivacyAndPromotion(t *testing.T) {
	f, b, _ := externalFixture(t)
	ctx := context.Background()
	runOne(t, f, ve.JobKind)
	for _, action := range []string{"inspect", "promotion-plan"} {
		var out bytes.Buffer
		if e := admin.VO2ExternalCommand(ctx, f.s.Pool.Config().ConnString(), []string{action, "--dataset", f.p.DatasetID}, &out); e != nil {
			t.Fatal(e)
		}
		for _, forbidden := range []string{f.p.DatasetID, b.Facts[0].UUID, b.Facts[0].Provenance.SourceRevision.Source.BundleIdentifier, `"value":42`, `"promotable":true`} {
			if bytes.Contains(out.Bytes(), []byte(forbidden)) {
				t.Fatal("admin privacy/promotion leak")
			}
		}
	}
}

func TestVO2ExternalSelectionAuditHashAndExactRetry(t *testing.T) {
	f, _, sel := externalFixture(t)
	ctx := context.Background()
	sel.ID = syncing.NewID()
	operation := syncing.NewID()
	through := int64(253402214400000001)
	sel.EffectiveThrough = &through
	if e := admin.SetVO2SourceSelection(ctx, f.s, f.p.DatasetID, operation, "synthetic audit", sel); e != nil {
		t.Fatal(e)
	}
	var bound bool
	if e := f.s.Pool.QueryRow(ctx, `SELECT encode(i.content_sha256,'hex')=a.details->>'hash' FROM athlete_model_inputs i JOIN admin_operations a ON a.subject_dataset=i.dataset_id AND a.operation_id=$2 WHERE i.dataset_id=$1 AND i.id=$3`, f.p.DatasetID, operation, sel.ID).Scan(&bound); e != nil || !bound {
		t.Fatal("selection audit hash not bound", e)
	}
	if e := admin.SetVO2SourceSelection(ctx, f.s, f.p.DatasetID, operation, "synthetic audit", sel); e != nil {
		t.Fatal(e)
	}
	through++
	if e := admin.SetVO2SourceSelection(ctx, f.s, f.p.DatasetID, operation, "synthetic audit", sel); e == nil {
		t.Fatal("operation retry rounded an Int64 microsecond")
	}
}

func TestVO2ExternalIndependentGoldenRawSnapshots(t *testing.T) {
	for _, name := range []string{"empty", "no_stream", "expired", "third_party_only", "manual_only", "unclassified_only", "multiple_epochs_no_selection", "equivalent", "cross_epoch_old_sync_conflict", "unknown_tombstone"} {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			b := externalBatch(t, f)
			x := b.Facts[0]
			epoch := x.Provenance.EpochID()
			expected := "source_selection_required"
			var value *float64
			switch name {
			case "empty", "no_stream":
				b.Facts = []syncing.VO2Fact{}
				expected = "no_sample_observed"
				if name == "no_stream" {
					expected = "sync_incomplete"
				}
			case "expired":
				b.FlowCreatedAtUs -= 32 * ve.DayUs
				b.BootstrapStartUs = b.FlowCreatedAtUs - syncing.VO2WindowUs
				b.QueryFingerprint = syncing.VO2Fingerprint(b.BootstrapStartUs)
				b.Facts[0].StartUs -= 90 * ve.DayUs
				b.Facts[0].EndUs = b.Facts[0].StartUs
				expected = "expired"
			case "third_party_only":
				b.Facts[0].Provenance.SourceRevision.Source.BundleIdentifier = "org.synthetic.other"
				epoch = b.Facts[0].Provenance.EpochID()
			case "manual_only":
				b.Facts[0].Metadata["HKWasUserEntered"] = syncing.VO2Metadata{Type: "boolean", Value: json.RawMessage(`true`)}
			case "unclassified_only":
				b.Facts[0].Metadata["HKVO2MaxTestType"] = syncing.VO2Metadata{Type: "int64", Value: json.RawMessage(`1`)}
			case "multiple_epochs_no_selection":
				second := x
				second.UUID = syncing.NewID()
				version := "epoch B"
				second.Provenance.SourceRevision.Version = &version
				b.Facts = append(b.Facts, second)
			case "equivalent":
				second := x
				second.UUID = syncing.NewID()
				b.Facts = append(b.Facts, second)
				v := 42.0
				value = &v
				expected = "available"
			case "cross_epoch_old_sync_conflict":
				b.FlowCreatedAtUs -= 32 * ve.DayUs
				b.BootstrapStartUs = b.FlowCreatedAtUs - syncing.VO2WindowUs
				b.QueryFingerprint = syncing.VO2Fingerprint(b.BootstrapStartUs)
				b.Facts[0].Metadata["HKMetadataKeySyncIdentifier"] = syncing.VO2Metadata{Type: "string", Value: json.RawMessage(`"synthetic-sync"`)}
				b.Facts[0].Metadata["HKMetadataKeySyncVersion"] = syncing.VO2Metadata{Type: "int64", Value: json.RawMessage(`1`)}
				second := b.Facts[0]
				second.UUID = syncing.NewID()
				second.StartUs -= 91 * ve.DayUs
				second.EndUs = second.StartUs
				version := "epoch B"
				second.Provenance.SourceRevision.Version = &version
				b.Facts = append(b.Facts, second)
				expected = "conflicting_data"
			case "unknown_tombstone":
				b.Facts = []syncing.VO2Fact{}
				b.DeletedUUIDs = []string{syncing.NewID()}
				expected = "no_sample_observed"
			}
			if name != "no_stream" {
				requireVO2(t, f, b)
			}
			if name != "multiple_epochs_no_selection" {
				if e := admin.SetVO2SourceSelection(context.Background(), f.s, f.p.DatasetID, syncing.NewID(), "synthetic golden", ve.Selection{ID: syncing.NewID(), Epoch: epoch, EffectiveFrom: b.FlowCreatedAtUs}); e != nil {
					t.Fatal(e)
				}
			}
			if e := admin.QueueVO2ExternalCandidate(context.Background(), f.s, f.p.DatasetID, syncing.NewID(), "synthetic golden"); e != nil {
				t.Fatal(e)
			}
			runOne(t, f, ve.JobKind)
			externalRead(t, f, expected, value)
		})
	}
}

func TestVO2ExternalControlTimeBoundaryFencesReaderAndPoller(t *testing.T) {
	f, _, sel := externalFixture(t)
	ctx := context.Background()
	future := time.Now().Add(time.Hour)
	x := ve.Selection{ID: syncing.NewID(), Epoch: sel.Epoch, EffectiveFrom: future.UnixMicro(), Supersedes: sel.ID, Revoked: true}
	if e := admin.SetVO2SourceSelection(ctx, f.s, f.p.DatasetID, syncing.NewID(), "synthetic scheduled revoke", x); e != nil {
		t.Fatal(e)
	}
	runOne(t, f, ve.JobKind)
	v := 42.0
	externalRead(t, f, "available", &v)
	if _, _, e := f.s.CandidateVO2External(ctx, f.p.DatasetID, future); e == nil {
		t.Fatal("expired effective selection remained current")
	}
	if e := f.s.AdvanceAnalysisDay(ctx, future); e != nil {
		t.Fatal(e)
	}
	var n int64
	if e := f.s.Pool.QueryRow(ctx, `SELECT generation FROM dirty_scopes WHERE dataset_id=$1 AND scope_key=$2`, f.p.DatasetID, ve.Scope).Scan(&n); e != nil {
		t.Fatal(e)
	}
	if e := f.s.AdvanceAnalysisDay(ctx, future); e != nil {
		t.Fatal(e)
	}
	var again int64
	if e := f.s.Pool.QueryRow(ctx, `SELECT generation FROM dirty_scopes WHERE dataset_id=$1 AND scope_key=$2`, f.p.DatasetID, ve.Scope).Scan(&again); e != nil || again != n {
		t.Fatal("poll duplicated boundary work", e)
	}
	if _, _, e := f.s.CandidateVO2External(ctx, f.p.DatasetID, time.Now()); e == nil {
		t.Fatal("poll left stale current pointer")
	}
}

func TestVO2ExternalControlExpiresDuringFirstComputation(t *testing.T) {
	f, _, sel := externalFixture(t)
	ctx := context.Background()
	boundary := time.Now().Add(2 * time.Second)
	end := boundary.UnixMicro()
	old := sel.ID
	sel.ID = syncing.NewID()
	sel.Supersedes = old
	sel.EffectiveThrough = &end
	if e := admin.SetVO2SourceSelection(ctx, f.s, f.p.DatasetID, syncing.NewID(), "synthetic expiring selection", sel); e != nil {
		t.Fatal(e)
	}
	l := claimAnalysis(t, f, ve.JobKind)
	in, id, ms := computeExternal(t, f, l)
	time.Sleep(time.Until(boundary) + time.Millisecond)
	status, e := f.s.PublishAnalysis(ctx, l, id, in, ms)
	if e != nil || status != "SUPERSEDED" {
		t.Fatal("expired source control published", status, e)
	}
	var dirty bool
	if e = f.s.Pool.QueryRow(ctx, `SELECT generation>clean_generation FROM dirty_scopes WHERE dataset_id=$1 AND scope_key=$2`, f.p.DatasetID, ve.Scope).Scan(&dirty); e != nil || !dirty {
		t.Fatal("superseded worker cleaned next generation", e)
	}
	runOne(t, f, ve.JobKind)
	// The superseded old selection's unlimited interval resumes outside the new
	// input's effective range, as frozen interval-local supersession specifies.
	v := 42.0
	externalRead(t, f, "available", &v)
}

func TestVO2ExternalInvalidCollectorCannotSucceed(t *testing.T) {
	f, _, _ := externalFixture(t)
	ctx := context.Background()
	if _, e := f.s.Pool.Exec(ctx, `UPDATE devices SET revoked_at=clock_timestamp() WHERE dataset_id=$1 AND id=$2`, f.p.DatasetID, f.p.DeviceID); e != nil {
		t.Fatal(e)
	}
	l := claimAnalysis(t, f, ve.JobKind)
	in, e := f.s.LoadAnalysis(ctx, l, time.Now())
	code, _ := analysis.ErrorCode(e)
	if e == nil || code != "analysis_dependency_pending" || in.ExternalArtifact != nil {
		t.Fatal("invalid collector became successful null-state", e)
	}
}
