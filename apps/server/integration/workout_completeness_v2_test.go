//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"rai/server/internal/httpapi"
	"rai/server/internal/syncing"
	"strings"
	"testing"
	"time"
)

// completenessManifestV2 builds a v2 manifest whose visible inventory is the
// eligible bundles plus the excluded identities, exactly as the client must.
func completenessManifestV2(f *fixture, bundles []syncing.WorkoutBundle, excluded []excludedSet) syncing.WorkoutSyncCompletenessManifestV2 {
	now := time.Now().Add(-time.Second).UnixMicro()
	m := syncing.WorkoutSyncCompletenessManifestV2{Contract: syncing.WorkoutCompletenessV2Contract, ProtocolVersion: syncing.WorkoutCompletenessV2Version, StreamSetVersion: 1, SourcePolicyVersion: syncing.WorkoutSourcePolicyVersion, SourcePolicySHA256: syncing.WorkoutSourcePolicySHA256, ClassificationVersion: syncing.WorkoutSourceClassificationVer, DatasetID: f.p.DatasetID, DatasetEpoch: f.enrollment.DatasetEpoch, CollectorEpoch: f.enrollment.CollectorEpoch, ManifestID: syncing.NewID(), Generation: 1, WindowFromUs: now - 200*86400000000, KnownCutoffUs: now, CreatedAtUs: now + 1000, SupportedStreams: syncing.WorkoutCompletenessTypes(), UnsupportedStreams: append([]string{}, syncing.WorkoutCompletenessUnsupported...), NotApplicableStreams: []string{}, Streams: []syncing.CompletenessStream{}, Workouts: []syncing.CompletenessWorkout{}, Exclusions: []syncing.CompletenessExclusion{}}
	page := func(adds []string) syncing.CompletenessPage {
		return syncing.CompletenessPage{PageID: syncing.NewID(), Sequence: 1, CandidateAnchorSHA256: strings.Repeat("a", 64), QueryStartedAtUs: now, QueryCompletedAtUs: now + 1, Additions: adds, Deletions: []string{}}
	}
	visible := []string{}
	eligibleIDs := []string{}
	for _, b := range bundles {
		visible = append(visible, b.WorkoutID)
		eligibleIDs = append(eligibleIDs, b.WorkoutID)
	}
	for _, x := range excluded {
		visible = append(visible, x.id)
	}
	m.Streams = append(m.Streams, syncing.CompletenessStream{Key: "workouts", SampleType: syncing.WorkoutType, QueryFingerprint: syncing.CompletenessFingerprintV2(m.WindowFromUs, "workouts"), Pages: []syncing.CompletenessPage{page(visible)}})
	for _, b := range bundles {
		raw, _ := json.Marshal(b)
		w := syncing.CompletenessWorkout{WorkoutID: b.WorkoutID, BundleSHA256: syncing.BundleDigest(raw), RouteState: "absent", Series: []syncing.CompletenessSeries{}}
		if len(b.Routes) > 0 {
			w.RouteState = "present"
		}
		for _, kind := range append(append([]string{}, syncing.WorkoutCompletenessQuantities...), syncing.RouteType) {
			ids := []string{}
			for _, s := range b.Samples {
				if s.Type == kind {
					ids = append(ids, s.UUID)
				}
			}
			if kind == syncing.RouteType {
				for _, r := range b.Routes {
					ids = append(ids, r.RouteID)
				}
			}
			id := b.WorkoutID
			key := syncing.CompletenessKey(kind, id)
			m.Streams = append(m.Streams, syncing.CompletenessStream{Key: key, SampleType: kind, WorkoutID: &id, QueryFingerprint: syncing.CompletenessFingerprintV2(m.WindowFromUs, key), Pages: []syncing.CompletenessPage{page(ids)}})
			if kind != syncing.RouteType {
				hash, count := syncing.CompletenessSeriesDigest(b.Series, kind)
				w.Series = append(w.Series, syncing.CompletenessSeries{SampleType: kind, SHA256: hash, PointCount: count, QueryCompletedAtUs: now + 2})
			}
		}
		m.Workouts = append(m.Workouts, w)
	}
	if len(bundles) == 0 {
		m.NotApplicableStreams = append(append([]string{}, syncing.WorkoutCompletenessQuantities...), syncing.RouteType)
	}
	excludedIDs := []string{}
	for _, x := range excluded {
		excludedIDs = append(excludedIDs, x.id)
		m.Exclusions = append(m.Exclusions, x.entry)
	}
	m.Inventory = syncing.CompletenessInventory{VisibleCount: len(visible), EligibleCount: len(eligibleIDs), ExcludedCount: len(excluded), UnclassifiedCount: 0, PendingClassificationCount: 0,
		VisibleSHA256: syncing.InventorySHA256(identities(append(append([]string{}, eligibleIDs...), excludedIDs...))), EligibleSHA256: syncing.InventorySHA256(identities(eligibleIDs)), ExcludedSHA256: syncing.InventorySHA256(identities(excludedIDs))}
	return m
}

// excludedWorkout is a synthetic non-Apple-Watch Workout: it is visible in
// HealthKit, never imported, and must be explained by source policy alone.
type excludedWorkout struct {
	id     string
	policy syncing.SourcePolicyEvidence
}

func thirdPartyExcluded(id string) excludedWorkout {
	return excludedWorkout{id: id, policy: syncing.SourcePolicyEvidence{BundleID: "com.strava.Strava", Name: "Strava", ProductType: "iPhone14,5"}}
}

func userEnteredExcluded(id string) excludedWorkout {
	return excludedWorkout{id: id, policy: syncing.SourcePolicyEvidence{BundleID: "com.apple.Health", Name: "Health"}}
}

// identities maps Workout UUIDs to the only identity form that may appear in
// inventory evidence.
func identities(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, syncing.WorkoutIdentitySHA256(id))
	}
	return out
}

type excludedSet struct {
	id    string
	entry syncing.CompletenessExclusion
}

func (x excludedWorkout) set(t *testing.T) excludedSet {
	return excludedSet{id: x.id, entry: x.entry(t)}
}

func (x excludedWorkout) entry(t *testing.T) syncing.CompletenessExclusion {
	t.Helper()
	classification := x.policy.Classification()
	outcome := syncing.SourcePolicyOutcome(classification)
	if outcome != syncing.SourceOutcomeExcluded {
		t.Fatalf("fixture %s is not excluded by policy: %s", x.id, classification)
	}
	return syncing.CompletenessExclusion{WorkoutIdentitySHA256: syncing.WorkoutIdentitySHA256(x.id), Classification: classification, Outcome: outcome, Reason: syncing.SourcePolicyReason(outcome), ClassificationVersion: syncing.WorkoutSourceClassificationVer, SourcePolicyVersion: syncing.WorkoutSourcePolicyVersion, EvidenceSHA256: x.policy.SHA256(), SourceEvidence: x.policy}
}

func commitCompletenessV2(t *testing.T, f *fixture, m syncing.WorkoutSyncCompletenessManifestV2) ([]byte, error) {
	t.Helper()
	ctx := context.Background()
	lease, err := f.s.AcquireUpload(ctx, f.p)
	if err != nil {
		return nil, err
	}
	defer f.s.ReleaseUpload(ctx, f.p, lease)
	raw, _ := json.Marshal(m)
	return f.s.CommitWorkoutCompletenessV2(ctx, f.p, raw, lease)
}

func nextCompletenessV2(m syncing.WorkoutSyncCompletenessManifestV2) syncing.WorkoutSyncCompletenessManifestV2 {
	raw, _ := json.Marshal(m)
	var n syncing.WorkoutSyncCompletenessManifestV2
	_ = json.Unmarshal(raw, &n)
	hash := syncing.BundleDigest(raw)
	n.PreviousManifestSHA256 = &hash
	n.Generation++
	n.ManifestID = syncing.NewID()
	n.KnownCutoffUs = time.Now().UnixMicro()
	n.CreatedAtUs = n.KnownCutoffUs + 1000
	for i, s := range n.Streams {
		last := s.Pages[len(s.Pages)-1]
		ph, ah := syncing.CompletenessPageDigest(last), last.CandidateAnchorSHA256
		n.Streams[i].Pages = []syncing.CompletenessPage{{PageID: syncing.NewID(), Sequence: last.Sequence + 1, PreviousPageSHA256: &ph, PreviousAnchorSHA256: &ah, CandidateAnchorSHA256: strings.Repeat("b", 64), QueryStartedAtUs: n.KnownCutoffUs, QueryCompletedAtUs: n.KnownCutoffUs + 1, Additions: []string{}, Deletions: []string{}}}
	}
	for i := range n.Workouts {
		for j := range n.Workouts[i].Series {
			n.Workouts[i].Series[j].QueryCompletedAtUs = n.KnownCutoffUs + 2
		}
	}
	return n
}

// TestWorkoutCompletenessV2EligiblePlusExcluded is the real-device shape: the
// eligible Workout is proved by receipts, the third-party Workout is proved by
// source-policy evidence, and neither is silently dropped.
func TestWorkoutCompletenessV2EligiblePlusExcluded(t *testing.T) {
	f := setup(t)
	b := completenessBundle(t, f, false, false)
	excluded := thirdPartyExcluded(syncing.NewID())
	m := completenessManifestV2(f, []syncing.WorkoutBundle{b}, []excludedSet{excluded.set(t)})
	ack, err := commitCompletenessV2(t, f, m)
	if err != nil {
		t.Fatal(err)
	}
	var receipt syncing.WorkoutCompletenessReceiptV2
	if err = json.Unmarshal(ack, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.VisibleCount != 2 || receipt.EligibleCount != 1 || receipt.ExcludedCount != 1 || receipt.UnclassifiedCount != 0 || receipt.SourcePolicyVersion != syncing.WorkoutSourcePolicyVersion {
		t.Fatalf("receipt inventory %+v", receipt)
	}
	// Lost ACK: the same body must return the same logical receipt.
	retry, err := commitCompletenessV2(t, f, m)
	if err != nil || !bytes.Equal(ack, retry) {
		t.Fatal("immutable retry", err)
	}
	// The excluded Workout never entered the fact domain.
	if count(t, f, "workout_imports") != 1 {
		t.Fatal("excluded Workout imported")
	}
	var summary []byte
	if err = f.s.Pool.QueryRow(context.Background(), `SELECT inventory_summary FROM workout_sync_completeness WHERE manifest_id=$1`, m.ManifestID).Scan(&summary); err != nil {
		t.Fatal(err)
	}
	var summaryFields map[string]any
	if err = json.Unmarshal(summary, &summaryFields); err != nil || summaryFields["excludedCount"] != float64(1) || summaryFields["visibleCount"] != float64(2) {
		t.Fatalf("persisted summary %s", summary)
	}
	// A second generation extends the same lineage.
	if _, err = commitCompletenessV2(t, f, nextCompletenessV2(m)); err != nil {
		t.Fatal("incremental committed anchor chain", err)
	}
	if count(t, f, "workout_sync_completeness") != 2 || count(t, f, "workout_imports") != 1 {
		t.Fatal("repeat duplicated facts/import")
	}
}

// TestWorkoutCompletenessV2ThirdPartyOnly proves the "eligible load is zero"
// case keeps the excluded evidence: zero eligible is complete, but never
// "the user did not exercise".
func TestWorkoutCompletenessV2ThirdPartyOnly(t *testing.T) {
	f := setup(t)
	excluded := []excludedSet{}
	for i := 0; i < 5; i++ {
		excluded = append(excluded, thirdPartyExcluded(syncing.NewID()).set(t))
	}
	m := completenessManifestV2(f, nil, excluded)
	ack, err := commitCompletenessV2(t, f, m)
	if err != nil {
		t.Fatal(err)
	}
	var receipt syncing.WorkoutCompletenessReceiptV2
	if err = json.Unmarshal(ack, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.EligibleCount != 0 || receipt.ExcludedCount != 5 || receipt.WorkoutCount != 0 {
		t.Fatalf("receipt %+v", receipt)
	}
	var payload []byte
	if err = f.s.Pool.QueryRow(context.Background(), `SELECT payload FROM change_events WHERE entity_kind='workout_completeness'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var eventFields map[string]any
	if err = json.Unmarshal(payload, &eventFields); err != nil || eventFields["excludedCount"] != float64(5) || eventFields["eligibleCount"] != float64(0) {
		t.Fatalf("exclusion count missing from change event: %s", payload)
	}
}

// TestWorkoutCompletenessV2RejectsUnexplainedInventory covers every way a
// client could claim completeness without explaining a visible Workout.
func TestWorkoutCompletenessV2RejectsUnexplainedInventory(t *testing.T) {
	for _, name := range []string{"count_only", "unclassified", "missing_exclusion", "forged_evidence", "evidence_reclassifies", "excluded_imported", "policy_version", "classification_version", "v1_lineage"} {
		t.Run(name, func(t *testing.T) {
			f := setup(t)
			b := completenessBundle(t, f, false, false)
			excluded := thirdPartyExcluded(syncing.NewID())
			m := completenessManifestV2(f, []syncing.WorkoutBundle{b}, []excludedSet{excluded.set(t)})
			switch name {
			case "count_only":
				m.Exclusions = []syncing.CompletenessExclusion{}
			case "unclassified":
				m.Inventory.UnclassifiedCount = 1
			case "missing_exclusion":
				m.Inventory.ExcludedCount = 2
			case "forged_evidence":
				m.Exclusions[0].EvidenceSHA256 = strings.Repeat("d", 64)
			case "evidence_reclassifies":
				m.Exclusions[0].SourceEvidence = syncing.SourcePolicyEvidence{BundleID: "com.health.thirdparty", ProductType: "Watch7,3", Device: syncing.SourcePolicyDevice{Manufacturer: "Apple Inc.", Model: "Watch7,3"}}
			case "excluded_imported":
				// An excluded Workout that was in fact imported is not excluded.
				other := completenessBundle(t, f, false, false)
				m = completenessManifestV2(f, []syncing.WorkoutBundle{b}, []excludedSet{excluded.set(t)})
				m.Workouts = append(m.Workouts, syncing.CompletenessWorkout{WorkoutID: other.WorkoutID, BundleSHA256: strings.Repeat("e", 64), RouteState: "absent", Series: m.Workouts[0].Series})
				m.Inventory.EligibleCount = 2
				m.Inventory.VisibleCount = 3
				m.Inventory.EligibleSHA256 = syncing.InventorySHA256(identities([]string{b.WorkoutID, other.WorkoutID}))
				m.Inventory.VisibleSHA256 = syncing.InventorySHA256(identities([]string{b.WorkoutID, other.WorkoutID, excluded.id}))
			case "policy_version":
				m.SourcePolicyVersion = "rai.workout.source_policy.all_sources/v1"
			case "classification_version":
				m.Exclusions[0].ClassificationVersion = 2
			case "v1_lineage":
				v1 := completenessManifest(f, []syncing.WorkoutBundle{b})
				if _, err := commitCompleteness(t, f, v1); err != nil {
					t.Fatal(err)
				}
				m.Generation = 2
				raw, _ := json.Marshal(v1)
				hash := syncing.BundleDigest(raw)
				m.PreviousManifestSHA256 = &hash
			}
			if _, err := commitCompletenessV2(t, f, m); err == nil {
				t.Fatal("unexplained inventory accepted")
			}
			if name != "v1_lineage" && count(t, f, "workout_sync_completeness") != 0 {
				t.Fatal("failed manifest persisted")
			}
			if name == "v1_lineage" && count(t, f, "workout_sync_completeness") != 1 {
				t.Fatal("v2 manifest extended a v1 lineage")
			}
		})
	}
}

// TestWorkoutCompletenessV2HTTPAndImmutability posts v2 over the real handler
// and proves the exclusion evidence is immutable once committed.
func TestWorkoutCompletenessV2HTTPAndImmutability(t *testing.T) {
	f := setup(t)
	b := completenessBundle(t, f, false, false)
	excluded := userEnteredExcluded(syncing.NewID())
	m := completenessManifestV2(f, []syncing.WorkoutBundle{b}, []excludedSet{excluded.set(t)})
	body, _ := json.Marshal(m)
	req := httptest.NewRequest(http.MethodPost, "/v2/sync/workout-completeness", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+f.enrollment.Token)
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	httpapi.New(f.s, analysisLog).ServeHTTP(response, req)
	if response.Code != 200 {
		t.Fatal("v2 http", response.Code, response.Body.String())
	}
	ctx := context.Background()
	for _, q := range []string{`UPDATE workout_sync_completeness SET exclusions='[]'::jsonb`, `UPDATE workout_sync_completeness SET inventory_summary='{}'::jsonb`, `DELETE FROM workout_sync_completeness`} {
		if _, err := f.s.Pool.Exec(ctx, q); err == nil {
			t.Fatal("immutable exclusion evidence mutable:", q)
		}
	}
	// A v1 manifest still commits unchanged on the same dataset lineage.
	v1Fixture := setup(t)
	v1 := completenessManifest(v1Fixture, nil)
	if _, err := commitCompleteness(t, v1Fixture, v1); err != nil {
		t.Fatal("v1 contract regressed", err)
	}
	var protocol int
	if err := v1Fixture.s.Pool.QueryRow(ctx, `SELECT protocol_version FROM workout_sync_completeness WHERE manifest_id=$1`, v1.ManifestID).Scan(&protocol); err != nil || protocol != 1 {
		t.Fatal("v1 row identity", err, protocol)
	}
}
